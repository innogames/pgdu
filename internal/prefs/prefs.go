// Package prefs persists per-user UI preferences to a small JSON file under the
// user's config directory (Linux: ~/.config/pgdu/prefs.json). The schema is an
// extensible object: only column visibility is stored today, but per-table sort
// or refresh cadence can be added as additional fields with no migration.
//
// Loading never fails the caller — a missing, unreadable, or corrupt file
// degrades to an empty (but usable) Prefs that the next Save overwrites — so a
// bad config file can never prevent pgdu from starting.
package prefs

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
)

// Prefs is the root of the on-disk preferences document.
type Prefs struct {
	Version int                   `json:"version"`
	Tables  map[string]TablePrefs `json:"tables,omitempty"`

	// path is where Save writes. Empty means the config dir could not be
	// resolved, so Save becomes a no-op rather than erroring on every keystroke.
	// Unexported, so encoding/json ignores it.
	path string
}

// TablePrefs holds one table's persisted view state. Fields are additive: a new
// build can introduce Sort/Refresh/… and old files simply omit them.
type TablePrefs struct {
	Columns map[string]bool `json:"columns,omitempty"` // column id → visible
}

const currentVersion = 1

// resolvePath returns the prefs file path, honouring PGDU_CONFIG_DIR, else
// os.UserConfigDir()/pgdu. Returns "" when no config dir can be determined.
func resolvePath() string {
	dir := os.Getenv("PGDU_CONFIG_DIR")
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(base, "pgdu")
	}
	return filepath.Join(dir, "prefs.json")
}

// Load reads the preferences file. It always returns a usable, non-nil *Prefs
// carrying the resolved path; any read/parse error yields empty prefs.
func Load() *Prefs {
	path := resolvePath()
	p := &Prefs{Version: currentVersion, path: path}
	if path == "" {
		return p
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return p
	}
	// Decode into a temporary so a corrupt file can't half-populate p.
	var decoded Prefs
	if err := json.Unmarshal(data, &decoded); err != nil {
		return p
	}
	decoded.path = path
	if decoded.Version == 0 {
		decoded.Version = currentVersion
	}
	return &decoded
}

// Save writes the preferences atomically (temp file + rename) with user-only
// permissions. It is a no-op when no path could be resolved.
func (p *Prefs) Save() error {
	if p.path == "" {
		return nil
	}
	p.Version = currentVersion
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(p.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp := p.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p.path)
}

// Columns returns the persisted visibility map for a table, or nil when none is
// stored. The returned map is a copy; callers may keep or mutate it freely.
func (p *Prefs) Columns(table string) map[string]bool {
	tp, ok := p.Tables[table]
	if !ok || tp.Columns == nil {
		return nil
	}
	return maps.Clone(tp.Columns)
}

// SetColumns records the visibility map for a table, replacing any prior value.
// The map is copied so later caller mutations don't leak into the stored state.
func (p *Prefs) SetColumns(table string, vis map[string]bool) {
	if p.Tables == nil {
		p.Tables = make(map[string]TablePrefs)
	}
	tp := p.Tables[table]
	tp.Columns = maps.Clone(vis)
	p.Tables[table] = tp
}
