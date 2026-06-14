package prefs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	t.Setenv("PGDU_CONFIG_DIR", t.TempDir())

	p := Load()
	p.SetColumns("queries", map[string]bool{"wal": true, "hit%": false})
	p.SetColumns("activity", map[string]bool{"cpu%": true})
	if err := p.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got := Load()
	q := got.Columns("queries")
	if q["wal"] != true || q["hit%"] != false {
		t.Errorf("queries columns = %v, want wal:true hit%%:false", q)
	}
	if a := got.Columns("activity"); a["cpu%"] != true {
		t.Errorf("activity columns = %v, want cpu%%:true", a)
	}
}

func TestColumnsCopyIsolation(t *testing.T) {
	p := &Prefs{}
	src := map[string]bool{"x": true}
	p.SetColumns("t", src)
	src["x"] = false // mutating the source must not affect stored state

	if got := p.Columns("t"); got["x"] != true {
		t.Errorf("stored map leaked source mutation: %v", got)
	}
	// And the returned map is itself a copy.
	out := p.Columns("t")
	out["x"] = false
	if again := p.Columns("t"); again["x"] != true {
		t.Errorf("returned map is not a copy: %v", again)
	}
}

func TestLoadEmptyDir(t *testing.T) {
	t.Setenv("PGDU_CONFIG_DIR", t.TempDir())
	p := Load()
	if p == nil {
		t.Fatal("Load returned nil")
	}
	if got := p.Columns("queries"); got != nil {
		t.Errorf("expected nil columns on empty dir, got %v", got)
	}
}

func TestLoadCorruptFileDegrades(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PGDU_CONFIG_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "prefs.json"), []byte("not json{"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := Load()
	if p == nil {
		t.Fatal("Load returned nil on corrupt file")
	}
	if got := p.Columns("queries"); got != nil {
		t.Errorf("expected empty prefs on corrupt file, got %v", got)
	}
	// A subsequent Save must succeed and overwrite the garbage.
	p.SetColumns("queries", map[string]bool{"wal": true})
	if err := p.Save(); err != nil {
		t.Fatalf("Save after corrupt load: %v", err)
	}
	if got := Load().Columns("queries"); got["wal"] != true {
		t.Errorf("save did not overwrite corrupt file: %v", got)
	}
}

func TestSaveFilePermissions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PGDU_CONFIG_DIR", dir)
	p := Load()
	p.SetColumns("queries", map[string]bool{"wal": true})
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "prefs.json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file perm = %o, want 600", perm)
	}
}
