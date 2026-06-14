package tui

import (
	"testing"
	"time"

	"pgdu/internal/cli"
	"pgdu/internal/pg"
	"pgdu/internal/prefs"
)

// TestNewModelSeedsColumnVisibility verifies that persisted column selections
// are loaded into the in-memory visibility maps when the Model is constructed.
func TestNewModelSeedsColumnVisibility(t *testing.T) {
	t.Setenv("PGDU_CONFIG_DIR", t.TempDir())
	p := prefs.Load()
	p.SetColumns(colPrefsQueries, map[string]bool{string(colWAL): false, string(colDirtied): true})
	p.SetColumns(colPrefsActivity, map[string]bool{string(actColCPU): true})

	m := NewModel(pg.New(cli.Config{}), 2*time.Second, "", p)

	if m.stmtColEnabled(colWAL, true) {
		t.Errorf("colWAL should be hidden per persisted prefs")
	}
	if !m.stmtColEnabled(colDirtied, false) {
		t.Errorf("colDirtied should be shown per persisted prefs (overriding default off)")
	}
	// A column the user never touched keeps its registry default.
	if !m.stmtColEnabled(colTotalMs, true) {
		t.Errorf("colTotalMs should fall back to its default-on")
	}
	if !m.actColEnabled(actColCPU, false) {
		t.Errorf("actColCPU should be shown per persisted prefs (overriding default off)")
	}
}

// TestNewModelNilPrefsIsSafe ensures a nil prefs object leaves visibility unseeded
// and saveColPrefs is a no-op.
func TestNewModelNilPrefsIsSafe(t *testing.T) {
	m := NewModel(pg.New(cli.Config{}), 2*time.Second, "", nil)
	if m.actColsVisible != nil || m.stmtColsVisible != nil {
		t.Errorf("expected nil visibility maps with nil prefs")
	}
	// Must not panic.
	m.saveColPrefs(colPrefsQueries, map[string]bool{"x": true})
}
