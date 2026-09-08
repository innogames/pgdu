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
