package pg

import (
	"strings"
	"testing"
)

// The progress CTE is one UNION, so a branch naming a view the server lacks
// fails the whole statement rather than losing one row type. These cases pin
// which arms each server shape emits.
func TestProgressBaseBranches(t *testing.T) {
	cases := []struct {
		name        string
		caps        progressCaps
		wantCluster bool
		wantRepack  bool
	}{
		{"pg17/18", progressCaps{cluster: true}, true, false},
		{"pg19 with both views", progressCaps{cluster: true, repack: true}, false, true},
		{"pg19 without cluster", progressCaps{repack: true}, false, true},
		{"neither view", progressCaps{}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sql := progressBase(tc.caps)
			if got := strings.Contains(sql, "pg_stat_progress_cluster"); got != tc.wantCluster {
				t.Errorf("cluster arm present = %v, want %v", got, tc.wantCluster)
			}
			if got := strings.Contains(sql, "pg_stat_progress_repack"); got != tc.wantRepack {
				t.Errorf("repack arm present = %v, want %v", got, tc.wantRepack)
			}
			// The arms every supported version has must survive every shape.
			for _, view := range []string{
				"pg_stat_progress_vacuum", "pg_stat_progress_create_index",
				"pg_stat_progress_analyze", "pg_stat_progress_copy",
				"pg_stat_progress_basebackup",
			} {
				if !strings.Contains(sql, view) {
					t.Errorf("missing %s", view)
				}
			}
			// Dropping an arm must not leave a dangling or doubled UNION ALL.
			if strings.Contains(sql, "UNION ALL\n    UNION ALL") {
				t.Error("doubled UNION ALL — an arm was removed without its separator")
			}
			if strings.HasSuffix(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(sql), ")")), "UNION ALL") {
				t.Error("trailing UNION ALL with no arm after it")
			}
		})
	}
}

// Both consumers of the CTE must carry the same arms, since they are the live
// monitor and the diagnostic over the same rows.
func TestProgressConsumersShareBranches(t *testing.T) {
	caps := progressCaps{repack: true}
	for name, sql := range map[string]string{
		"progressOpsSQL":     progressOpsSQL(caps),
		"diagProgressAllSQL": diagProgressAllSQL(caps),
	} {
		if !strings.Contains(sql, "pg_stat_progress_repack") {
			t.Errorf("%s: repack arm missing", name)
		}
		if strings.Contains(sql, "pg_stat_progress_cluster") {
			t.Errorf("%s: cluster arm should be dropped when repack is present", name)
		}
		if !strings.Contains(sql, "FROM prog p") {
			t.Errorf("%s: CTE not consumed", name)
		}
	}
}

// The registry entry is what the query view displays, so it must stay a real
// statement even though the executed SQL is resolved per server.
func TestProgressAllRegistrySQL(t *testing.T) {
	var d Diagnostic
	for _, c := range Diagnostics {
		if c.Key == "progress_all" {
			d = c
		}
	}
	if d.Key == "" {
		t.Fatal("progress_all diagnostic not registered")
	}
	if !strings.Contains(d.SQL, "pg_stat_progress_cluster") {
		t.Error("registry SQL should show the PG17/18 shape")
	}
	if strings.Contains(d.SQL, "pg_stat_progress_repack") {
		t.Error("registry SQL should not assume PG19")
	}
}
