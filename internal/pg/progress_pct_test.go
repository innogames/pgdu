package pg

import "testing"

// OverallPct composes each command's per-phase counters into one 0–100
// estimate via commandPhaseSpans, exactly like the REINDEX banner. The exact
// weights are free to change; what must hold is the shape: phases in
// execution order map to non-overlapping, increasing slices ending at 100,
// unmappable phases return -1 so the caller's high-water clamp holds the bar,
// and commands without a span map fall back to the raw counters.
func TestProgressRowOverallPct(t *testing.T) {
	// One representative execution path per command; alternates that replace
	// a phase on that path (index-scan CLUSTER, partitioned ANALYZE) are
	// checked separately below.
	orders := map[string][]string{
		"VACUUM": {
			"initializing",
			"scanning heap",
			"vacuuming indexes",
			"vacuuming heap",
			"cleaning up indexes",
			"truncating heap",
			"performing final cleanup",
		},
		"CREATE INDEX": {
			"initializing",
			"waiting for writers before build",
			"building index: initializing",
			"building index: scanning table",
			"building index: sorting live tuples",
			"building index: loading tuples in tree",
			"waiting for writers before validation",
			"index validation: scanning index",
			"index validation: sorting tuples",
			"index validation: scanning table",
			"waiting for old snapshots",
			"waiting for readers before marking dead",
			"waiting for readers before dropping",
		},
		"ANALYZE": {
			"initializing",
			"acquiring sample rows",
			"computing statistics",
			"computing extended statistics",
			"finalizing analyze",
		},
		"CLUSTER": {
			"initializing",
			"seq scanning heap",
			"sorting tuples",
			"writing new heap",
			"swapping relation files",
			"rebuilding index",
			"performing final cleanup",
		},
		"BASE BACKUP": {
			"initializing",
			"waiting for checkpoint to finish",
			"estimating backup size",
			"streaming database files",
			"waiting for wal archiving to finish",
			"transferring wal files",
		},
	}
	for cmd, order := range orders {
		spans := commandPhaseSpans[cmd]
		if spans == nil {
			t.Fatalf("%s missing from commandPhaseSpans", cmd)
		}
		prev := -1.0
		for _, phase := range order {
			span, ok := spans[phase]
			if !ok {
				t.Fatalf("%s: phase %q missing from its span map", cmd, phase)
			}
			r := ProgressRow{Command: cmd, Phase: phase, Done: 1, Total: 2}
			pct := r.OverallPct()
			if pct < span[0] || pct > span[1] {
				t.Errorf("%s %q: pct %.1f outside span [%.0f, %.0f]", cmd, phase, pct, span[0], span[1])
			}
			if pct <= prev {
				t.Errorf("%s %q: pct %.1f not past previous phase's %.1f", cmd, phase, pct, prev)
			}
			// A finished phase must not reach past where the next one starts.
			prev = span[1]
		}
		if end := spans[order[len(order)-1]][1]; end != 100 {
			t.Errorf("%s: final phase ends at %.0f, want 100", cmd, end)
		}
	}

	// Alternate phases replace one on the path above, so they share its span.
	if analyzePhaseSpan["acquiring inherited sample rows"] != analyzePhaseSpan["acquiring sample rows"] {
		t.Errorf("inherited sample phase must share the sample phase's span")
	}
	if clusterPhaseSpan["index scanning heap"] != clusterPhaseSpan["seq scanning heap"] {
		t.Errorf("index-scan cluster phase must share the seq-scan phase's span")
	}

	for _, tc := range []struct {
		name string
		r    ProgressRow
		want float64
	}{
		{"copy falls back to raw pct",
			ProgressRow{Command: "COPY FROM", Done: 25, Total: 100}, 25},
		{"copy with unknown total stays unknown",
			ProgressRow{Command: "COPY FROM", Done: 25}, -1},
		{"unknown phase", ProgressRow{Command: "VACUUM", Phase: "doing something new"}, -1},
		{"phase start when total unknown",
			ProgressRow{Command: "VACUUM", Phase: "vacuuming indexes", Done: 3}, 60},
		{"full phase clamps at span end",
			ProgressRow{Command: "VACUUM", Phase: "scanning heap", Done: 300, Total: 200}, 60},
	} {
		if got := tc.r.OverallPct(); got != tc.want {
			t.Errorf("%s: got %.2f, want %.2f", tc.name, got, tc.want)
		}
	}

	// An AM subphase we don't know still stays inside the build slice.
	r := ProgressRow{Command: "CREATE INDEX", Phase: "building index: exotic am step", Done: 1, Total: 2}
	span := reindexPhaseSpan["building index"]
	if pct := r.OverallPct(); pct < span[0] || pct > span[1] {
		t.Errorf("unknown build subphase: pct %.1f outside build span [%.0f, %.0f]", pct, span[0], span[1])
	}
}
