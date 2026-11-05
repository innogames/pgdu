//go:build linux

package procfs

import "testing"

func TestParseStatTicks(t *testing.T) {
	// comm with spaces and a ')' inside, as "postgres: walwriter" style names have.
	stat := []byte("4242 (postgres: a (b) c) S 1 4242 4242 0 -1 4194560 100 0 0 0 30 12 0 0 20 0 1 0 1000 0 0")
	got, ok := parseStatTicks(stat)
	if !ok || got != 42 {
		t.Fatalf("parseStatTicks = %d,%v want 42,true", got, ok)
	}
	if _, ok := parseStatTicks([]byte("garbage")); ok {
		t.Errorf("garbage must not parse")
	}
}

// Statfs answers for a path that exists and declines for one that does not —
// the second case is the one that matters, since a data directory read off a
// remote server usually isn't here at all.
func TestStatfs(t *testing.T) {
	fs, ok := Statfs(t.TempDir())
	if !ok {
		t.Fatal("Statfs on a temp dir failed")
	}
	if fs.Total <= 0 || fs.Avail > fs.Total || fs.Used() > fs.Total {
		t.Errorf("implausible capacity: %+v", fs)
	}
	if _, ok := Statfs("/nonexistent-pgdu-path/PGDATA"); ok {
		t.Error("Statfs on a missing path reported a filesystem")
	}
}
