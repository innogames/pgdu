package pg

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// logTagPattern matches the "SEVERITY:  " that follows every log_line_prefix.
// PostgreSQL emits two spaces; one is accepted so hand-edited logs still parse.
// The trailing text is captured lazily as the rest of the line.
const logTagPattern = `(LOG|ERROR|FATAL|PANIC|WARNING|NOTICE|INFO|DEBUG[1-5]?|DETAIL|HINT|STATEMENT|CONTEXT|QUERY|LOCATION):  ?(.*)$`

// prefixEscapes maps each log_line_prefix escape to the regexp fragment that
// matches its rendering. Fields that may be empty (%u, %d, %a … when there is
// no session) are allowed to match the empty string; %q handling makes the
// whole tail optional anyway.
var prefixEscapes = map[byte]string{
	'a': `[^ \[\]@,]*`,                                                   // application name
	'u': `[^ \[\]@,]*`,                                                   // user
	'd': `[^ \[\]@,]*`,                                                   // database
	'r': `(?:\[local\]|[0-9a-fA-F:.]+(?:\(\d+\))?|[^ ,]+(?:\(\d+\))?)?`, // host(port)
	'h': `(?:\[local\]|[0-9a-fA-F:.]+|[^ ,]+)?`,                          // host
	'b': `[^ ,]*`,                                                        // backend type
	'p': `\d+`,                                                           // pid
	'P': `\d*`,                                                           // leader pid (empty for non-workers)
	't': `\d{4}-\d\d-\d\d \d\d:\d\d:\d\d [A-Z0-9+\-]{1,6}`,               // timestamp
	'm': `\d{4}-\d\d-\d\d \d\d:\d\d:\d\d\.\d{3} [A-Z0-9+\-]{1,6}`,        // timestamp with ms
	'n': `\d+\.\d{3}`,                                                    // epoch
	'i': `[^ ,]*`,                                                        // command tag
	'e': `[0-9A-Z]{5}`,                                                   // SQLSTATE
	'c': `[0-9a-f]+\.[0-9a-f]+`,                                          // session id
	'l': `\d+`,                                                           // session line number
	's': `\d{4}-\d\d-\d\d \d\d:\d\d:\d\d [A-Z0-9+\-]{1,6}`,               // session start
	'v': `[0-9/]*`,                                                       // virtual xid
	'x': `\d*`,                                                           // xid
	'Q': `-?\d*`,                                                         // query id
}

// prefixMatcher is a compiled log_line_prefix: one anchored regexp whose named
// groups map escapes back to fields.
type prefixMatcher struct {
	prefix string
	re     *regexp.Regexp
	idx    map[byte]int // escape letter → subexpression index
	tag    int
	rest   int
	loc    *time.Location

	// lastTS caches the previous timestamp parse: consecutive lines almost
	// always share the same second, and time.Parse is the hot spot.
	lastTS  string
	lastVal time.Time

	// loose is the last-resort matcher for an unrecognised prefix: it splits on
	// the first "TAG:  " and scrapes a timestamp and "[pid-line]" out of
	// whatever preceded it, so attachment and time bucketing still work.
	loose bool
}

var (
	looseRe     = regexp.MustCompile(`^(.*?)\b` + logTagPattern)
	loosePIDRe  = regexp.MustCompile(`\[(\d+)(?:-(\d+))?\]`)
	looseTimeRe = regexp.MustCompile(`\d{4}-\d\d-\d\d \d\d:\d\d:\d\d(?:\.\d{3})? [A-Z0-9+\-]{1,6}`)
)

// compileLoose builds the fallback matcher; see prefixMatcher.loose.
func compileLoose(loc *time.Location) *prefixMatcher {
	if loc == nil {
		loc = time.UTC
	}
	return &prefixMatcher{prefix: "(unrecognised)", re: looseRe, idx: map[byte]int{}, tag: 2, rest: 3, loc: loc, loose: true}
}

// CompilePrefix turns a log_line_prefix value into a matcher. Literal text is
// quoted verbatim (PostgreSQL emits it exactly, spaces included); %q opens an
// optional group that runs to the end of the prefix, which is what lets
// session-less lines (checkpointer, autovacuum launcher) match a prefix that
// mentions %u@%h.
func CompilePrefix(prefix string, loc *time.Location) (*prefixMatcher, error) {
	var b strings.Builder
	b.WriteString("^")
	idx := map[byte]int{}
	group := 0
	optional := false
	for i := 0; i < len(prefix); i++ {
		ch := prefix[i]
		if ch != '%' || i+1 >= len(prefix) {
			b.WriteString(regexp.QuoteMeta(string(ch)))
			continue
		}
		i++
		esc := prefix[i]
		switch esc {
		case '%':
			b.WriteString("%")
		case 'q':
			if !optional {
				b.WriteString("(?:") // non-capturing: no submatch index consumed
				optional = true
			}
		default:
			pat, ok := prefixEscapes[esc]
			if !ok {
				return nil, fmt.Errorf("log_line_prefix: unknown escape %%%c", esc)
			}
			group++
			idx[esc] = group
			b.WriteString("(" + pat + ")")
		}
	}
	if optional {
		b.WriteString(")?")
	}
	group++
	tag := group
	group++
	rest := group
	b.WriteString(logTagPattern)
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil, fmt.Errorf("log_line_prefix %q: %w", prefix, err)
	}
	if loc == nil {
		loc = time.UTC
	}
	return &prefixMatcher{prefix: prefix, re: re, idx: idx, tag: tag, rest: rest, loc: loc}, nil
}

// prefixFields is what one matched primary line yields.
type prefixFields struct {
	time     time.Time
	pid      int32
	line     int32
	session  []byte
	user     []byte
	db       []byte
	host     []byte
	app      []byte
	sqlstate []byte
	tag      []byte
	rest     []byte
}

// Match parses one line. ok is false for continuation lines.
func (m *prefixMatcher) Match(line []byte) (f prefixFields, ok bool) {
	if len(line) == 0 || line[0] == '\t' || line[0] == ' ' {
		return f, false
	}
	loc := m.re.FindSubmatchIndex(line)
	if loc == nil {
		return f, false
	}
	get := func(g int) []byte {
		if g == 0 || loc[2*g] < 0 {
			return nil
		}
		return line[loc[2*g]:loc[2*g+1]]
	}
	f.tag = get(m.tag)
	f.rest = get(m.rest)
	if m.loose {
		head := get(1)
		if ts := looseTimeRe.Find(head); ts != nil {
			if len(ts) > 20 && ts[19] == '.' {
				f.time = m.parseTime(ts, "2006-01-02 15:04:05.000 MST")
			} else {
				f.time = m.parseTime(ts, "2006-01-02 15:04:05 MST")
			}
		}
		if pm := loosePIDRe.FindSubmatch(head); pm != nil {
			if v, err := strconv.Atoi(string(pm[1])); err == nil {
				f.pid = int32(v)
			}
			if len(pm[2]) > 0 {
				if v, err := strconv.Atoi(string(pm[2])); err == nil {
					f.line = int32(v)
				}
			}
		}
		return f, true
	}
	if ts := get(m.idx['m']); ts != nil {
		f.time = m.parseTime(ts, "2006-01-02 15:04:05.000 MST")
	} else if ts := get(m.idx['t']); ts != nil {
		f.time = m.parseTime(ts, "2006-01-02 15:04:05 MST")
	} else if ts := get(m.idx['n']); ts != nil {
		if v, err := strconv.ParseFloat(string(ts), 64); err == nil {
			f.time = time.Unix(int64(v), int64((v-float64(int64(v)))*1e9)).In(m.loc)
		}
	}
	if p := get(m.idx['p']); p != nil {
		if v, err := strconv.Atoi(string(p)); err == nil {
			f.pid = int32(v)
		}
	}
	if l := get(m.idx['l']); l != nil {
		if v, err := strconv.Atoi(string(l)); err == nil {
			f.line = int32(v)
		}
	}
	f.session = get(m.idx['c'])
	f.user = get(m.idx['u'])
	f.db = get(m.idx['d'])
	f.app = get(m.idx['a'])
	f.sqlstate = get(m.idx['e'])
	if h := get(m.idx['h']); h != nil {
		f.host = h
	} else if r := get(m.idx['r']); r != nil {
		f.host = r
	}
	return f, true
}

func (m *prefixMatcher) parseTime(ts []byte, layout string) time.Time {
	if string(ts) == m.lastTS {
		return m.lastVal
	}
	t, err := time.ParseInLocation(layout, string(ts), m.loc)
	if err != nil {
		// A zone abbreviation Go does not know still parses via the layout's
		// MST slot as a zero-offset location; only a malformed stamp fails.
		return time.Time{}
	}
	m.lastTS = string(ts)
	m.lastVal = t
	return t
}

// HasPID reports whether the prefix carries %p, which is what attachment
// (DETAIL → its primary) keys on.
func (m *prefixMatcher) HasPID() bool { _, ok := m.idx['p']; return ok || m.loose }

// HasLine reports whether the prefix carries %l.
func (m *prefixMatcher) HasLine() bool { _, ok := m.idx['l']; return ok || m.loose }

// commonPrefixes are tried, in order, when the server's log_line_prefix is
// unavailable or does not match the file (a rotated log written under an older
// configuration, a copied file analysed offline).
var commonPrefixes = []string{
	"%t [%p-%l] %q%u@%h ",       // Debian/Ubuntu postgresql-common default
	"%m [%p] %q%u@%d ",          // postgresql.conf.sample default (PG10+)
	"%m [%p] ",                  // pre-PG10 sample default
	"%t [%p]: [%l-1] user=%u,db=%d,app=%a,client=%h ", // pgBadger recommendation
	"%t [%p]: ",
	"%m [%p]: [%l-1] user=%u,db=%d,app=%a,client=%h ",
	"%m %u@%d %p ",
	"%t %u@%d %p ",
	"",
}

// DetectPrefix picks the log_line_prefix that parses the file best. The
// server's setting is tried first and wins outright when it matches; otherwise
// every common prefix is scored on the first sample lines and the best one is
// returned with detected=true. When nothing scores well the loose splitter is
// used so a file never fails to parse — it just loses the user/db fields.
func DetectPrefix(buf []byte, serverPrefix string, loc *time.Location) (m *prefixMatcher, detected bool) {
	sample := sampleLines(buf, 200)
	if len(sample) == 0 {
		if pm, err := CompilePrefix(serverPrefix, loc); err == nil {
			return pm, false
		}
		return compileLoose(loc), true
	}
	score := func(p string) (*prefixMatcher, float64) {
		pm, err := CompilePrefix(p, loc)
		if err != nil {
			return nil, -1
		}
		hits := 0
		for _, ln := range sample {
			if _, ok := pm.Match(ln); ok {
				hits++
			}
		}
		return pm, float64(hits) / float64(len(sample))
	}
	if serverPrefix != "" {
		if pm, sc := score(serverPrefix); sc >= 0.6 {
			return pm, false
		}
	}
	var best *prefixMatcher
	bestScore := -1.0
	for _, p := range commonPrefixes {
		pm, sc := score(p)
		if sc > bestScore {
			best, bestScore = pm, sc
		}
		if sc >= 0.95 {
			break
		}
	}
	if bestScore < 0.6 {
		return compileLoose(loc), true
	}
	return best, true
}

// sampleLines returns up to n non-continuation lines from the head of buf.
// Continuation lines (tab/space-indented, empty) are skipped because they never
// carry a prefix and would drag every score down equally.
func sampleLines(buf []byte, n int) [][]byte {
	var out [][]byte
	for len(buf) > 0 && len(out) < n {
		i := bytes.IndexByte(buf, '\n')
		var ln []byte
		if i < 0 {
			ln, buf = buf, nil
		} else {
			ln, buf = buf[:i], buf[i+1:]
		}
		if len(ln) == 0 || ln[0] == '\t' || ln[0] == ' ' {
			continue
		}
		out = append(out, ln)
	}
	return out
}

// firstLineOf returns b up to its first newline as a string.
func firstLineOf(b []byte) string {
	if i := bytes.IndexByte(b, '\n'); i >= 0 {
		return string(b[:i])
	}
	return string(b)
}
