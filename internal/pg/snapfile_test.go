package pg

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestKindOf(t *testing.T) {
	cases := []struct {
		name string
		want string // kind name, "" = unclaimed
	}{
		{"app-20261030T120000Z.json.gz", "snapshot"},
		{"app-20261030T120000Z.sizes.gz", "size sample"},
		{"app-20261030T120000Z.json", ""},
		{"notes.txt", ""},
		{"", ""},
	}
	for _, c := range cases {
		k, ok := kindOf(c.name)
		if c.want == "" {
			if ok {
				t.Errorf("kindOf(%q) = %q, want unclaimed", c.name, k.name)
			}
			continue
		}
		if !ok || k.name != c.want {
			t.Errorf("kindOf(%q) = %q/%v, want %q", c.name, k.name, ok, c.want)
		}
	}
}

// writeDummySizes drops a file with the size-sample extension into dir. Step 1
// has no size sample type yet; the listers must ignore each other's files on
// the extension alone, which is exactly what this checks.
func writeDummySizes(t *testing.T, dir, stem string) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write([]byte(`{"Kind":"sizes","Database":"other"}`)); err != nil {
		t.Fatalf("compress: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("compress: %v", err)
	}
	path := filepath.Join(dir, stem+sizeExt)
	if err := os.WriteFile(path, buf.Bytes(), 0o666); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func TestListSnapshotsIgnoresSizeSamples(t *testing.T) {
	dir := t.TempDir()
	snap := &Snapshot{
		Target: "db:5432", Database: "app",
		CapturedAt: time.Now().Truncate(time.Second),
		Stats:      []QueryStat{{QueryID: 1}},
	}
	if _, err := SaveSnapshot(dir, snap); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	writeDummySizes(t, dir, "app-20261030T120000Z")

	metas, err := ListSnapshots(dir)
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(metas) != 1 {
		t.Fatalf("want 1 snapshot, got %d: %+v", len(metas), metas)
	}
	if filepath.Ext(metas[0].Path) != ".gz" || metas[0].Database != "app" {
		t.Errorf("listed the wrong file: %+v", metas[0])
	}
}

// TestMetaStoreNoCrossKindEviction is the regression test for the reason the
// cache is per-kind: with both kinds in one directory, a shared path-keyed map
// would have each lister's "drop entries for files that disappeared" sweep
// evict the other kind's entries on every listing.
func TestMetaStoreNoCrossKindEviction(t *testing.T) {
	dir := t.TempDir()
	snapPath, err := SaveSnapshot(dir, &Snapshot{
		Target: "db:5432", Database: "app",
		CapturedAt: time.Now().Truncate(time.Second),
	})
	if err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	sizePath := writeDummySizes(t, dir, "app-20261030T120000Z")

	// A stand-in for the size lister, sharing the directory.
	sizes := newMetaStore[string]()
	reads := 0
	listSizes := func() {
		if _, err := sizes.list(dir, kindSizes, func(path string, _ int64) (string, error) {
			reads++
			return path, nil
		}); err != nil {
			t.Fatalf("list sizes: %v", err)
		}
	}

	listSizes()
	if reads != 1 {
		t.Fatalf("first listing: want 1 read, got %d", reads)
	}
	// The other kind's lister runs over the same directory.
	if _, err := ListSnapshots(dir); err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	listSizes()
	if reads != 1 {
		t.Errorf("snapshot listing evicted the size entry: want 1 read total, got %d", reads)
	}
	if _, ok := sizes.m[sizePath]; !ok {
		t.Errorf("size entry %q dropped from its own cache", sizePath)
	}
	if _, ok := snapMetaCache.m[snapPath]; !ok {
		t.Errorf("snapshot entry %q dropped from its own cache", snapPath)
	}
}

// TestReadHeaderStopsAtArray proves the bulk array is never decoded: the header
// count and the array disagree, and the header value is what comes back.
func TestReadHeaderStopsAtArray(t *testing.T) {
	dir := t.TempDir()
	// QueryCount says 5, Stats holds 2. A reader that decoded the array would
	// report 2.
	raw := `{"Version":1,"Target":"db:5432","Database":"app","QueryCount":5,` +
		`"Stats":[{"QueryID":1},{"QueryID":2}]}`
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write([]byte(raw)); err != nil {
		t.Fatalf("compress: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("compress: %v", err)
	}
	path := filepath.Join(dir, "app-20261030T120000Z"+snapshotExt)
	if err := os.WriteFile(path, buf.Bytes(), 0o666); err != nil {
		t.Fatalf("write: %v", err)
	}

	meta, err := readSnapshotMeta(path)
	if err != nil {
		t.Fatalf("readSnapshotMeta: %v", err)
	}
	if meta.QueryCount != 5 {
		t.Errorf("QueryCount = %d, want 5 (the header value, array not decoded)", meta.QueryCount)
	}
	if meta.Database != "app" || meta.Target != "db:5432" {
		t.Errorf("header fields lost: %+v", meta)
	}
}

// A file predating QueryCount must still report a count, by counting tokens.
func TestReadHeaderCountsLegacyArray(t *testing.T) {
	dir := t.TempDir()
	raw := `{"Version":1,"Database":"app","Stats":[{"QueryID":1},{"QueryID":2},{"QueryID":3}]}`
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write([]byte(raw)); err != nil {
		t.Fatalf("compress: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("compress: %v", err)
	}
	path := filepath.Join(dir, "app-20261030T120001Z"+snapshotExt)
	if err := os.WriteFile(path, buf.Bytes(), 0o666); err != nil {
		t.Fatalf("write: %v", err)
	}

	meta, err := readSnapshotMeta(path)
	if err != nil {
		t.Fatalf("readSnapshotMeta: %v", err)
	}
	if meta.QueryCount != 3 {
		t.Errorf("QueryCount = %d, want 3", meta.QueryCount)
	}
}

// Unknown fields from a newer writer must be skipped, not fail the read.
func TestReadHeaderSkipsUnknownFields(t *testing.T) {
	var got []string
	dir := t.TempDir()
	raw := `{"Known":7,"Nested":{"a":[1,2,{"b":null}]},"Arr":[1,2],"Stop":[]}`
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write([]byte(raw)); err != nil {
		t.Fatalf("compress: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("compress: %v", err)
	}
	path := filepath.Join(dir, "x.json.gz")
	if err := os.WriteFile(path, buf.Bytes(), 0o666); err != nil {
		t.Fatalf("write: %v", err)
	}

	var known int
	stopped := false
	err := readHeader(path, "Stop", func(key string) any {
		got = append(got, key)
		if key == "Known" {
			return &known
		}
		return nil
	}, func(_ *json.Decoder) error {
		stopped = true
		return nil
	})
	if err != nil {
		t.Fatalf("readHeader: %v", err)
	}
	if known != 7 {
		t.Errorf("Known = %d, want 7", known)
	}
	if !stopped {
		t.Error("atStop was never called")
	}
	if len(got) != 3 {
		t.Errorf("visited keys %v, want the three before the stop key", got)
	}
}

func TestSanitizeDB(t *testing.T) {
	cases := map[string]string{
		"":          "db",
		"app":       "app",
		"my-db_2":   "my-db_2",
		"app.sizes": "app_sizes", // cannot forge the other kind's suffix
		"a/b c":     "a_b_c",
		"../../etc": "______etc",
	}
	for in, want := range cases {
		if got := sanitizeDB(in); got != want {
			t.Errorf("sanitizeDB(%q) = %q, want %q", in, got, want)
		}
	}
}
