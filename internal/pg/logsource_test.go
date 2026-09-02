package pg

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// numberedLog writes n prefixed lines so tests can check exactly which lines
// survive a tail window.
func numberedLog(n int) []byte {
	var b bytes.Buffer
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "2026-09-02 00:00:%02d UTC [%d-1] u@h LOG:  line %06d %s\n", i%60, i+1, i, bytes.Repeat([]byte("x"), 40))
	}
	return b.Bytes()
}

func TestLocalTailWindow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "postgresql-17-main.log")
	data := numberedLog(20000)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	src := OpenLocalLog(path)
	buf, win, err := src.ReadTail(t.Context(), 100_000)
	if err != nil {
		t.Fatal(err)
	}
	if !win.Truncated || win.FileSize != int64(len(data)) {
		t.Errorf("window = %+v", win)
	}
	if !bytes.HasPrefix(buf, []byte("2026-")) || !bytes.HasSuffix(buf, []byte("\n")) {
		t.Errorf("window not line-aligned: %q … %q", buf[:20], buf[len(buf)-5:])
	}
	if !bytes.Equal(buf, data[win.Start:]) {
		t.Error("Start does not point at the first parsed byte")
	}
	whole, win, err := src.ReadTail(t.Context(), 0)
	if err != nil || win.Truncated || !bytes.Equal(whole, data) {
		t.Errorf("whole read: err=%v win=%+v equal=%v", err, win, bytes.Equal(whole, data))
	}
}

func TestGzipTailWindow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "postgresql-17-main.log.2.gz")
	data := numberedLog(30000) // ~3 MiB
	var z bytes.Buffer
	zw := gzip.NewWriter(&z)
	zw.Write(data)
	zw.Close()
	if err := os.WriteFile(path, z.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	src := OpenLocalLog(path)
	if src.Info().Kind != "gz" || !src.Info().Rotated {
		t.Errorf("info = %+v", src.Info())
	}
	for _, n := range []int64{1 << 20, 100_000, 1000} {
		buf, win, err := src.ReadTail(t.Context(), n)
		if err != nil {
			t.Fatal(err)
		}
		if !win.Truncated || win.FileSize != int64(len(data)) {
			t.Errorf("n=%d window = %+v", n, win)
		}
		if int64(len(buf)) > n || !bytes.Equal(buf, data[len(data)-len(buf):]) {
			t.Errorf("n=%d: tail content wrong (len %d)", n, len(buf))
		}
		if !bytes.HasPrefix(buf, []byte("2026-")) {
			t.Errorf("n=%d: not line aligned: %q", n, buf[:20])
		}
	}
	whole, win, err := src.ReadTail(t.Context(), 0)
	if err != nil || win.Truncated || !bytes.Equal(whole, data) {
		t.Errorf("whole gz read: err=%v win=%+v", err, win)
	}
	if _, err := src.ReadFrom(t.Context(), 0); err != ErrNotIncremental {
		t.Errorf("gz ReadFrom err = %v", err)
	}
}

func TestTailKeeperBoundaries(t *testing.T) {
	for _, total := range []int{0, 5, 10, 15, 20, 21, 99} {
		tk := &tailKeeper{n: 10}
		for i := 0; i < total; i++ {
			tk.Write([]byte{byte('a' + i%26)})
		}
		got := tk.Bytes()
		want := make([]byte, 0, total)
		for i := 0; i < total; i++ {
			want = append(want, byte('a'+i%26))
		}
		if len(want) > 10 {
			want = want[len(want)-10:]
		}
		if !bytes.Equal(got, want) {
			t.Errorf("total=%d: got %q want %q", total, got, want)
		}
	}
}

func TestRefreshLogIncremental(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "postgresql-17-main.log")
	first := "2026-09-02 00:00:01 UTC [1-1] u@h LOG:  duration: 1.000 ms  statement: SELECT 1\n" +
		"2026-09-02 00:00:02 UTC [2-1] u@h ERROR:  boom\n"
	if err := os.WriteFile(path, []byte(first), 0o644); err != nil {
		t.Fatal(err)
	}
	src := OpenLocalLog(path)
	r, err := LoadLog(t.Context(), src, debianPrefix, time.UTC, 32<<20, AggOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Entries) != 2 || r.Cursor == nil || r.PrefixDetected {
		t.Fatalf("initial report: entries=%d cursor=%+v detected=%v", len(r.Entries), r.Cursor, r.PrefixDetected)
	}

	// Unchanged file: same report back.
	same, err := RefreshLog(t.Context(), r, src, time.UTC, AggOptions{})
	if err != nil || same != r {
		t.Errorf("unchanged refresh: err=%v same=%v", err, same == r)
	}

	// Append a DETAIL to the last entry plus a new one.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("2026-09-02 00:00:02 UTC [2-2] u@h DETAIL:  more\n" +
		"2026-09-02 00:00:03 UTC [3-1] u@h WARNING:  careful\n")
	f.Close()
	r2, err := RefreshLog(t.Context(), r, src, time.UTC, AggOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r2.Entries) != 3 || string(r2.Entries[1].Detail) != "more" || r2.Entries[2].Severity != SevWarning {
		t.Errorf("incremental entries = %+v", r2.Entries)
	}
	if r2.BySeverity[SevWarning] != 1 || len(r2.Groups) != 3 {
		t.Errorf("re-aggregated: sev=%v groups=%d", r2.BySeverity, len(r2.Groups))
	}

	// Rotation: a new, shorter file under the same name → full reload.
	os.Rename(path, path+".1")
	os.WriteFile(path, []byte("2026-09-02 01:00:00 UTC [9-1] u@h LOG:  fresh\n"), 0o644)
	r3, err := RefreshLog(t.Context(), r2, src, time.UTC, AggOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r3.Entries) != 1 || string(r3.Entries[0].Message) != "fresh" {
		t.Errorf("after rotation: %+v", r3.Entries)
	}
}

func TestRotationOrdering(t *testing.T) {
	c := []LogCandidate{
		{Info: LogSourceInfo{Kind: "gz", Path: "/var/log/postgresql/postgresql-17-main.log.2.gz", Rotated: true}},
		{Info: LogSourceInfo{Kind: "local", Path: "/var/log/postgresql/postgresql-17-main.log", Current: true}},
		{Info: LogSourceInfo{Kind: "local", Path: "/var/log/postgresql/postgresql-17-main.log.1", Rotated: true}},
		{Info: LogSourceInfo{Kind: "gz", Path: "/var/log/postgresql/postgresql-17-main.log.10.gz", Rotated: true}},
	}
	sortCandidates(c)
	want := []string{".log", ".log.1", ".log.2.gz", ".log.10.gz"}
	for i, w := range want {
		if !strings.HasSuffix(c[i].Info.Path, w) {
			t.Errorf("position %d: %s, want suffix %s", i, c[i].Info.Path, w)
		}
	}
	if !isRotatedName("x.log.3.gz") || isRotatedName("x.log") || rotationIndex("x.log.10.gz") != 10 {
		t.Error("rotation name helpers")
	}
}
