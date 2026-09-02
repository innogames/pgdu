package pg

import (
	"bytes"
	"hash/fnv"
	"math/rand/v2"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// AggOptions tunes Aggregate.
type AggOptions struct {
	// MaxSamples caps LogGroup.Samples so a huge window cannot pin every entry
	// through its groups. 0 means the default.
	MaxSamples int
}

const (
	defaultMaxSamples = 50
	// slowReservoirCap bounds the per-group duration reservoir used for the
	// p95: exact up to this many entries, uniform sample beyond.
	slowReservoirCap = 2048
	// maxKeyLen bounds a fingerprint; longer normalized SQL is keyed by its
	// prefix plus a hash so the map stays cheap.
	maxKeyLen = 4096
	// maxTitleLen bounds the single-line title shown in the group list.
	maxTitleLen = 400
	// histMaxBuckets caps the sparkline resolution.
	histMaxBuckets = 120
)

var histBuckets = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour, 6 * time.Hour, 24 * time.Hour}

// Aggregate fills the derived parts of r (groups, histogram, counters, covered
// time range) from r.Entries. It is a pure function of the entries, so an
// incremental refresh simply re-runs it.
func Aggregate(r *LogReport, opts AggOptions) {
	maxSamples := opts.MaxSamples
	if maxSamples <= 0 {
		maxSamples = defaultMaxSamples
	}
	r.BySeverity = [numLogSeverities]int{}
	r.ByCategory = [numLogCategories]int{}
	r.Window.From, r.Window.To = time.Time{}, time.Time{}

	groups := make(map[string]*LogGroup)
	var order []*LogGroup
	rng := rand.New(rand.NewPCG(1, 2)) // deterministic: the same log yields the same p95
	for i := range r.Entries {
		e := &r.Entries[i]
		r.BySeverity[e.Severity]++
		r.ByCategory[e.Category]++
		if !e.Time.IsZero() {
			if r.Window.From.IsZero() || e.Time.Before(r.Window.From) {
				r.Window.From = e.Time
			}
			if e.Time.After(r.Window.To) {
				r.Window.To = e.Time
			}
		}
		key, title := Fingerprint(e)
		g := groups[key]
		if g == nil {
			g = &LogGroup{Key: key, Title: title, Category: e.Category, Severity: e.Severity, First: e.Time, Last: e.Time}
			groups[key] = g
			order = append(order, g)
		}
		g.Count++
		if e.Severity > g.Severity {
			g.Severity = e.Severity
		}
		if !e.Time.IsZero() {
			if g.First.IsZero() || e.Time.Before(g.First) {
				g.First = e.Time
			}
			if e.Time.After(g.Last) {
				g.Last = e.Time
			}
		}
		if len(g.Samples) < maxSamples {
			g.Samples = append(g.Samples, i)
		} else {
			copy(g.Samples, g.Samples[1:])
			g.Samples[len(g.Samples)-1] = i
		}
		if len(e.Plan) > 0 {
			g.Plans++
		}
		switch e.Category {
		case CatSlowQuery:
			if g.Slow == nil {
				g.Slow = &LogSlowStats{}
			}
			g.Slow.SumMs += e.DurationMs
			if e.DurationMs > g.Slow.MaxMs {
				g.Slow.MaxMs = e.DurationMs
			}
			if len(g.Slow.res) < slowReservoirCap {
				g.Slow.res = append(g.Slow.res, e.DurationMs)
			} else if j := rng.IntN(g.Count); j < slowReservoirCap {
				g.Slow.res[j] = e.DurationMs
			}
		case CatCheckpoint:
			if g.Checkpoint == nil {
				g.Checkpoint = &LogCheckpointStats{}
			}
			if cf := e.Checkpoint; cf != nil && !cf.Starting {
				g.Checkpoint.Complete++
				g.Checkpoint.SumWrite += cf.WriteSec
				g.Checkpoint.SumTotal += cf.TotalSec
				g.Checkpoint.SumBuffers += cf.Buffers
				if cf.TotalSec > g.Checkpoint.MaxTotal {
					g.Checkpoint.MaxTotal = cf.TotalSec
				}
			}
		case CatTempFile:
			if g.Temp == nil {
				g.Temp = &LogTempStats{}
			}
			g.Temp.TotalBytes += e.TempBytes
			if e.TempBytes > g.Temp.MaxBytes {
				g.Temp.MaxBytes = e.TempBytes
			}
		case CatAutovacuum:
			if g.Autovac == nil {
				g.Autovac = make(map[string]int)
			}
			g.Autovac[string(e.AVTable)]++
		}
	}
	for _, g := range order {
		if g.Slow != nil {
			g.Slow.P95Ms = percentile(g.Slow.res, 0.95)
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		if order[i].Count != order[j].Count {
			return order[i].Count > order[j].Count
		}
		return order[i].Last.After(order[j].Last)
	})
	r.Groups = make([]LogGroup, len(order))
	for i, g := range order {
		r.Groups[i] = *g
	}
	r.Hist = histogram(r.Entries, r.Window.From, r.Window.To)
}

func percentile(res []float64, q float64) float64 {
	if len(res) == 0 {
		return 0
	}
	s := make([]float64, len(res))
	copy(s, res)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	idx := int(q*float64(len(s)-1) + 0.5)
	return s[min(idx, len(s)-1)]
}

// histogram buckets entries by time and severity, choosing the finest bucket
// that keeps the sparkline within histMaxBuckets cells.
func histogram(entries []LogEntry, from, to time.Time) LogHistogram {
	if from.IsZero() || to.IsZero() {
		return LogHistogram{}
	}
	span := to.Sub(from)
	bucket := histBuckets[len(histBuckets)-1]
	for _, b := range histBuckets {
		if span/b <= histMaxBuckets {
			bucket = b
			break
		}
	}
	start := from.Truncate(bucket)
	n := int(to.Sub(start)/bucket) + 1
	if n <= 0 || n > histMaxBuckets+1 {
		n = histMaxBuckets + 1
	}
	h := LogHistogram{Bucket: bucket, Start: start, Counts: make([][numLogSeverities]int, n)}
	for i := range entries {
		e := &entries[i]
		if e.Time.IsZero() {
			continue
		}
		b := int(e.Time.Sub(start) / bucket)
		if b < 0 || b >= n {
			continue
		}
		h.Counts[b][e.Severity]++
	}
	return h
}

// ── fingerprints ─────────────────────────────────────────────────────────────

// Fingerprint returns the grouping key and the single-line title for e.
func Fingerprint(e *LogEntry) (key, title string) {
	switch e.Category {
	case CatSlowQuery:
		if len(e.SQL) == 0 {
			return "slow|-", "duration only (log_duration without statement text)"
		}
		norm := NormalizeSQL(string(e.SQL))
		return boundedKey("slow|", norm), clipTitle(norm)
	case CatCheckpoint:
		msg := e.FirstLine()
		kind := "checkpoint"
		if strings.HasPrefix(msg, "restartpoint") {
			kind = "restartpoint"
		}
		if e.Checkpoint != nil && e.Checkpoint.Starting {
			return "ckpt|" + kind + "|starting|" + e.Checkpoint.Reason, kind + " starting: " + e.Checkpoint.Reason
		}
		return "ckpt|" + kind + "|complete", kind + " complete"
	case CatTempFile:
		ctx := e.Statement
		if len(ctx) == 0 {
			ctx = stripContextWrapper(e.Context)
		}
		if len(ctx) == 0 {
			return "tmp|-", "temporary file (no statement context)"
		}
		norm := NormalizeSQL(string(ctx))
		return boundedKey("tmp|", norm), "temporary file · " + clipTitle(norm)
	case CatAutovacuum:
		msg := e.FirstLine()
		if strings.HasPrefix(msg, "automatic analyze") {
			return "av|analyze", "automatic analyze of table"
		}
		if strings.Contains(msg, "wraparound") {
			return "av|wraparound", "automatic vacuum to prevent wraparound"
		}
		if strings.HasPrefix(msg, "automatic aggressive") {
			return "av|aggressive", "automatic aggressive vacuum of table"
		}
		return "av|vacuum", "automatic vacuum of table"
	case CatConnection:
		msg := e.FirstLine()
		kind := msg
		if i := strings.IndexByte(msg, ':'); i >= 0 {
			kind = msg[:i]
		}
		return "conn|" + kind, kind
	}
	norm := normalizeMessage(e.FirstLine())
	if e.Orphan {
		norm = "(detail without primary line — cut off by the window start)"
	}
	return boundedKey(e.Severity.String()+"|", norm), clipTitle(norm)
}

// stripContextWrapper turns `SQL statement "…"` (the CONTEXT PostgreSQL adds
// for statements run inside functions) into the bare statement.
func stripContextWrapper(ctx []byte) []byte {
	const wrap = "SQL statement \""
	if !bytes.HasPrefix(ctx, []byte(wrap)) {
		return ctx
	}
	ctx = ctx[len(wrap):]
	if i := bytes.LastIndexByte(ctx, '"'); i >= 0 {
		ctx = ctx[:i]
	}
	return ctx
}

func boundedKey(prefix, norm string) string {
	if len(norm) <= maxKeyLen {
		return prefix + norm
	}
	h := fnv.New64a()
	h.Write([]byte(norm))
	return prefix + norm[:maxKeyLen] + "#" + strconv.FormatUint(h.Sum64(), 16)
}

func clipTitle(s string) string {
	if len(s) > maxTitleLen {
		return s[:maxTitleLen] + "…"
	}
	return s
}

var (
	lsnRe         = regexp.MustCompile(`\b[0-9A-F]{1,8}/[0-9A-F]{1,8}\b`)
	atCharacterRe = regexp.MustCompile(` at character N$`)
	identRe       = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.$]*$`)
)

// normalizeMessage folds the variable parts of an error/warning text so
// repeats group together: single-quoted literals become '?', digit runs
// (and LSNs, hex) become N, and "at character N" is dropped. Double-quoted
// names are kept when they look like identifiers — `column "buffers_backend"
// does not exist` and `database "pgbouncer" does not exist` are different
// problems — and replaced with "?" otherwise (they are then data, e.g. a
// duplicate-key value).
func normalizeMessage(msg string) string {
	msg = lsnRe.ReplaceAllString(msg, "N/N")
	var b strings.Builder
	b.Grow(len(msg))
	for i := 0; i < len(msg); {
		ch := msg[i]
		switch {
		case ch == '\'':
			j := i + 1
			for j < len(msg) {
				if msg[j] == '\'' {
					if j+1 < len(msg) && msg[j+1] == '\'' {
						j += 2
						continue
					}
					break
				}
				j++
			}
			b.WriteString("'?'")
			i = j + 1
		case ch == '"':
			j := strings.IndexByte(msg[i+1:], '"')
			if j < 0 {
				b.WriteString(msg[i:])
				i = len(msg)
				continue
			}
			inner := msg[i+1 : i+1+j]
			if identRe.MatchString(inner) {
				b.WriteString(msg[i : i+1+j+1])
			} else {
				b.WriteString(`"?"`)
			}
			i = i + 1 + j + 1
		case ch == '$' && i+1 < len(msg) && isDigit(msg[i+1]):
			// $n placeholders are already normalized — keep them.
			j := i + 1
			for j < len(msg) && isDigit(msg[j]) {
				j++
			}
			b.WriteString(msg[i:j])
			i = j
		case isDigit(ch) && (i == 0 || !isIdentByte(msg[i-1])):
			j := i
			if ch == '0' && i+1 < len(msg) && (msg[i+1] == 'x' || msg[i+1] == 'X') {
				j = i + 2
				for j < len(msg) && isHexDigit(msg[j]) {
					j++
				}
			} else {
				for j < len(msg) && (isDigit(msg[j]) || msg[j] == '.' && j+1 < len(msg) && isDigit(msg[j+1])) {
					j++
				}
			}
			b.WriteByte('N')
			i = j
		default:
			b.WriteByte(ch)
			i++
		}
	}
	out := atCharacterRe.ReplaceAllString(b.String(), "")
	return collapseSpaces(out)
}

// NormalizeSQL is a pg_stat_statements-style normalizer for logged statement
// text: literals become $?, whitespace collapses, IN/VALUES lists fold, and
// `/* … */` comments are kept because ORMs put the calling method there —
// that is the most useful grouping key in the sample logs. `--` comments are
// dropped (line noise).
func NormalizeSQL(sql string) string {
	var b strings.Builder
	b.Grow(len(sql))
	space := func() {
		if b.Len() > 0 {
			s := b.String()
			if s[len(s)-1] != ' ' {
				b.WriteByte(' ')
			}
		}
	}
	for i := 0; i < len(sql); {
		ch := sql[i]
		switch {
		case ch == '/' && i+1 < len(sql) && sql[i+1] == '*':
			end := strings.Index(sql[i+2:], "*/")
			if end < 0 {
				end = len(sql) - i - 2
			}
			b.WriteString(collapseSpaces(sql[i : i+2+end+2]))
			i += 2 + end + 2
		case ch == '-' && i+1 < len(sql) && sql[i+1] == '-':
			end := strings.IndexByte(sql[i:], '\n')
			if end < 0 {
				i = len(sql)
			} else {
				i += end
			}
		case ch == '\'':
			esc := i > 0 && (sql[i-1] == 'E' || sql[i-1] == 'e') && (i == 1 || !isIdentByte(sql[i-2]))
			j := i + 1
			for j < len(sql) {
				if esc && sql[j] == '\\' {
					j += 2
					continue
				}
				if sql[j] == '\'' {
					if j+1 < len(sql) && sql[j+1] == '\'' {
						j += 2
						continue
					}
					break
				}
				j++
			}
			if esc {
				// drop the E we already emitted
				s := b.String()
				b.Reset()
				b.WriteString(s[:len(s)-1])
			}
			b.WriteString("$?")
			i = j + 1
		case ch == '$' && i+1 < len(sql) && isDigit(sql[i+1]):
			j := i + 1
			for j < len(sql) && isDigit(sql[j]) {
				j++
			}
			b.WriteString(sql[i:j])
			i = j
		case ch == '$' && i+1 < len(sql) && (isIdentByte(sql[i+1]) || sql[i+1] == '$'):
			// dollar quoting: $tag$ … $tag$
			j := strings.IndexByte(sql[i+1:], '$')
			if j < 0 {
				b.WriteByte(ch)
				i++
				continue
			}
			tag := sql[i : i+1+j+1]
			end := strings.Index(sql[i+len(tag):], tag)
			if end < 0 {
				b.WriteByte(ch)
				i++
				continue
			}
			b.WriteString("$?")
			i += len(tag) + end + len(tag)
		case ch == '"':
			j := strings.IndexByte(sql[i+1:], '"')
			if j < 0 {
				b.WriteString(sql[i:])
				i = len(sql)
				continue
			}
			b.WriteString(sql[i : i+1+j+1])
			i = i + 1 + j + 1
		case isDigit(ch) && (i == 0 || !isIdentByte(sql[i-1])):
			j := i
			for j < len(sql) && (isDigit(sql[j]) || sql[j] == '.' || (sql[j] == 'e' || sql[j] == 'E') && j+1 < len(sql) && (isDigit(sql[j+1]) || sql[j+1] == '-' || sql[j+1] == '+') || (sql[j] == '-' || sql[j] == '+') && j > i && (sql[j-1] == 'e' || sql[j-1] == 'E')) {
				j++
			}
			b.WriteString("$?")
			i = j
		case ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r':
			space()
			i++
		default:
			b.WriteByte(ch)
			i++
		}
	}
	out := strings.TrimSpace(b.String())
	out = inListRe.ReplaceAllString(out, "IN ($?...)")
	out = valuesListRe.ReplaceAllString(out, "VALUES (...)")
	return out
}

var (
	inListRe     = regexp.MustCompile(`(?i)\bIN \(\s*\$[?\d]+(?:\s*,\s*\$[?\d]+)*\s*\)`)
	valuesListRe = regexp.MustCompile(`(?i)\bVALUES \((?:[^()]*)\)(?:\s*,\s*\((?:[^()]*)\))*`)
)

func isDigit(c byte) bool    { return c >= '0' && c <= '9' }
func isHexDigit(c byte) bool { return isDigit(c) || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }
func isIdentByte(c byte) bool {
	return c == '_' || isDigit(c) || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}
