package pg

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// snapKind identifies one family of sample files sharing the snapshot
// directory. Query snapshots and size samples live side by side there, so every
// reader must claim its own files and ignore the other's; routing that through
// one registry makes it a property of this table rather than of each lister.
type snapKind struct {
	ext  string // the on-disk suffix, including every dot
	name string // what the kind is called in error text
}

// sizeExt is the on-disk suffix for size samples. It is deliberately NOT
// ".sizes.json.gz": that ends in ".json.gz", so the snapshot lister would claim
// it as a query snapshot and then half-parse it.
const sizeExt = ".sizes.gz"

var (
	kindSnapshot = snapKind{ext: snapshotExt, name: "snapshot"}
	kindSizes    = snapKind{ext: sizeExt, name: "size sample"}
)

// snapKinds is ordered longest extension first so a future compound suffix
// (".indexes.json.gz", say) is never claimed by a shorter one it ends with.
var snapKinds = []snapKind{kindSizes, kindSnapshot}

// kindOf returns the kind owning a directory entry name.
func kindOf(name string) (snapKind, bool) {
	for _, k := range snapKinds {
		if strings.HasSuffix(name, k.ext) {
			return k, true
		}
	}
	return snapKind{}, false
}

// writeGzipJSON marshals v as indented JSON, gzips it and writes it to
// dir/<sanitizeDB(db)>-<at UTC>.<k.ext>, returning the path. The directory is
// created if missing.
//
// The directory and file are made world readable/writable so samples are
// shared across users on the same host: any user can list, load and delete any
// other user's. The directory is created mode 0o777 *without* the sticky bit
// (which a shared /tmp normally carries) so non-owners can unlink files in it;
// the explicit Chmod defeats a restrictive umask. Both Chmods are best-effort —
// they only succeed for whoever owns the path, but the first writer sets the
// permissions correctly for everyone that follows.
//
// Permissions are deliberately not a parameter: both kinds live in the one
// snapshot directory by design, so a second mode would be speculative.
func writeGzipJSON(dir, db string, at time.Time, k snapKind, v any) (string, error) {
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return "", fmt.Errorf("create %s dir %q: %w", k.name, dir, err)
	}
	_ = os.Chmod(dir, 0o777)
	name := fmt.Sprintf("%s-%s%s", sanitizeDB(db), at.UTC().Format("20060102T150405Z"), k.ext)
	path := filepath.Join(dir, name)
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode %s: %w", k.name, err)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(data); err != nil {
		return "", fmt.Errorf("compress %s: %w", k.name, err)
	}
	if err := gz.Close(); err != nil {
		return "", fmt.Errorf("compress %s: %w", k.name, err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o666); err != nil {
		return "", fmt.Errorf("write %s %q: %w", k.name, path, err)
	}
	_ = os.Chmod(path, 0o666)
	return path, nil
}

// readGzipFile reads a gzip-compressed sample file and returns the
// decompressed JSON bytes.
func readGzipFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer func() { _ = gz.Close() }()
	return io.ReadAll(gz)
}

// readHeader streams the leading fields of a gzip-JSON object file without
// decoding the bulk array that follows them.
//
// For every key it calls field(key): a non-nil return is decoded into, a nil
// return skips the value, so fields from another version are tolerated. The
// moment it reaches stopKey it calls atStop with the decoder parked just before
// that value and returns — nothing from stopKey onwards is ever decompressed.
// A file whose header ends before stopKey appears returns nil without calling
// atStop.
func readHeader(path, stopKey string, field func(key string) any, atStop func(*json.Decoder) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()

	dec := json.NewDecoder(gz)
	if tok, err := dec.Token(); err != nil {
		return err
	} else if d, ok := tok.(json.Delim); !ok || d != '{' {
		return fmt.Errorf("%q: expected JSON object", path)
	}

	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return err
		}
		key, _ := keyTok.(string)
		if key == stopKey {
			return atStop(dec)
		}
		dst := field(key)
		if dst == nil {
			if err := skipValue(dec); err != nil {
				return err
			}
			continue
		}
		if err := dec.Decode(dst); err != nil {
			return err
		}
	}
	return nil
}

// countArrayElements consumes a JSON array from dec (positioned just before the
// opening '[') and returns its element count without materialising the values.
func countArrayElements(dec *json.Decoder) (int, error) {
	tok, err := dec.Token()
	if err != nil {
		return 0, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		return 0, nil // null or non-array: treat as empty
	}
	n := 0
	for dec.More() {
		if err := skipValue(dec); err != nil {
			return 0, err
		}
		n++
	}
	if _, err := dec.Token(); err != nil { // closing ']'
		return 0, err
	}
	return n, nil
}

// skipValue consumes exactly one JSON value from dec — scalar, object or array,
// recursing through nested containers — without allocating a destination for it.
func skipValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	d, ok := tok.(json.Delim)
	if !ok {
		return nil // scalar already consumed
	}
	switch d {
	case '{':
		for dec.More() {
			if _, err := dec.Token(); err != nil { // key
				return err
			}
			if err := skipValue(dec); err != nil { // value
				return err
			}
		}
	case '[':
		for dec.More() {
			if err := skipValue(dec); err != nil {
				return err
			}
		}
	}
	if _, err := dec.Token(); err != nil { // closing '}' or ']'
		return err
	}
	return nil
}

// metaStore memoizes parsed listing metadata of ONE kind, keyed by path. Sample
// files are immutable once written (unique timestamped names, never rewritten),
// so a (modTime,size) match means the cached metadata is still valid. This
// makes re-opening a browser or re-listing after a delete ~free: only
// new/changed files are read from disk.
//
// It is generic rather than one shared map[string]any so cross-kind eviction is
// unrepresentable: list's sweep, which drops entries for files that
// disappeared, can only ever see this kind's paths. A single map shared by the
// two kinds that live in one directory would have each lister evict the other's
// entries on every listing.
type metaStore[T any] struct {
	mu sync.Mutex
	m  map[string]cachedMeta[T]
}

type cachedMeta[T any] struct {
	modTime time.Time
	size    int64
	meta    T
}

func newMetaStore[T any]() *metaStore[T] {
	return &metaStore[T]{m: map[string]cachedMeta[T]{}}
}

// list returns the metadata of every file in dir owned by k, reading only the
// ones the cache does not already hold. A missing directory is not an error (no
// samples yet); unreadable or undecodable files are skipped so one bad file
// doesn't hide the rest. Results are unsorted — callers order them.
func (s *metaStore[T]) list(dir string, k snapKind, read func(path string, size int64) (T, error)) ([]T, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s dir %q: %w", k.name, dir, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var out []T
	seen := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), k.ext) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}
		seen[path] = struct{}{}
		if c, ok := s.m[path]; ok && c.modTime.Equal(info.ModTime()) && c.size == info.Size() {
			out = append(out, c.meta)
			continue
		}
		meta, err := read(path, info.Size())
		if err != nil {
			continue
		}
		s.m[path] = cachedMeta[T]{modTime: info.ModTime(), size: info.Size(), meta: meta}
		out = append(out, meta)
	}
	// Drop cache entries for files of this kind in this dir that disappeared
	// (deleted samples) so the cache tracks the directory rather than growing
	// without bound. Entries from other directories are left untouched; entries
	// of the other kind are unreachable from here by construction.
	for path := range s.m {
		if _, ok := seen[path]; !ok && filepath.Dir(path) == filepath.Clean(dir) {
			delete(s.m, path)
		}
	}
	return out, nil
}

// sanitizeDB makes a database name safe for a filename: anything that isn't a
// letter, digit, dash or underscore becomes an underscore. Empty names (the
// libpq "same as user" default) become "db" so the file still has a stem.
//
// It is also what keeps the two kinds' extensions unforgeable: a database
// called "app.sizes" becomes "app_sizes", so no name can produce a stem ending
// in another kind's suffix.
func sanitizeDB(db string) string {
	if db == "" {
		return "db"
	}
	var b strings.Builder
	for _, r := range db {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// DeleteSnapshot removes a sample file. It is kind-agnostic: the growth
// browser deletes size samples through it too.
func DeleteSnapshot(path string) error {
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("delete snapshot %q: %w", path, err)
	}
	return nil
}

// dayLayout is the UTC calendar day the once-a-day predicate keys on. It is the
// same day key the retention sweep used to bucket on before it became a count.
const dayLayout = "2006-01-02"

// sampleID is everything a kind-agnostic sweep needs from one listing entry:
// which file, whose it is, and when it was taken.
//
// SnapshotMeta and SizeSampleMeta carry these four under identical names but
// share no type, so each kind hands over a projection rather than implementing
// an interface. That keeps the metas plain JSON shapes with no behaviour, and
// keeps a []SizeSampleMeta from having to be copied element by element into a
// slice of some interface before it can be swept.
type sampleID struct {
	path     string
	target   string
	database string
	at       time.Time
}

// DefaultRetention is how many samples of ONE kind pgdu keeps per
// (target, database): 30 size samples and 30 query snapshots per database per
// host, roughly a month of once-a-day history.
//
// A count rather than a horizon, because the thing an operator wants bounded is
// the directory, and a count bounds it whether samples arrive four times an
// hour from cron or once a day from opening the tool.
const DefaultRetention = 30

// pruneKeepNewest deletes every entry of one kind belonging to
// (target, database) past the newest keep, and reports how many it removed.
//
// metas must be newest first — both listers sort that way — which is what makes
// keep a prefix count and nothing more.
//
// keep < 1 prunes nothing. That is both the documented "0 = never prune" escape
// hatch and what re-establishes the invariant the tiered policy used to get for
// free from its 7-day floor: under a count, the newest sample is only safe if
// at least one is kept. The clamp lives here rather than at each call site,
// where one caller forgetting it would wipe a database's whole history.
//
// Scoping is three-deep on purpose, because the directory is shared and
// world-writable: the caller lists through its own kind's lister, so another
// kind's file is unreachable from here whatever the extension rules become, and
// target and database are matched so sampling one database on one host never
// prunes another's. Deleting another *user's* samples of the same database is
// deliberate, and matches how the directory is already shared.
func pruneKeepNewest[T any](metas []T, id func(T) sampleID, target, database string, keep int) (int, error) {
	if keep < 1 {
		return 0, nil
	}
	seen, removed := 0, 0
	var firstErr error
	for _, m := range metas {
		s := id(m)
		if s.target != target || s.database != database {
			continue
		}
		seen++
		if seen <= keep {
			continue
		}
		if err := DeleteSnapshot(s.path); err != nil {
			// A concurrent sampler may have pruned it already; that is not a
			// failure. Any other error is reported but never stops the sweep.
			if os.IsNotExist(err) || errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		removed++
	}
	return removed, firstErr
}

// capturedToday reports whether metas already holds a capture of
// (target, database) on now's UTC calendar day.
//
// UTC and a calendar day, not local time and not a rolling 24 hours: the
// filenames, the browser labels and every scoped sweep in this package are
// already UTC, so one timezone governs the feature and two pgdu processes on
// one host under different TZs agree without comparing clocks. A rolling window
// would instead mean "at most once per 24h", which drifts the capture later
// every day until a day is skipped outright — 23:00 then 08:00 is nine hours
// apart and would not capture — and "30 kept samples" would stop meaning
// "about a month".
func capturedToday[T any](metas []T, id func(T) sampleID, target, database string, now time.Time) bool {
	day := now.UTC().Format(dayLayout)
	for _, m := range metas {
		s := id(m)
		if s.target == target && s.database == database && s.at.UTC().Format(dayLayout) == day {
			return true
		}
	}
	return false
}
