package tui

import (
	"testing"

	"pgdu/internal/pg"
)

// TestShmemCatOf pins the category bucketing, in particular the match-order
// subtleties documented on shmemCatOf: "Backend … Buffer" regions must not
// read as the buffer pool, "Buffer Blocks" contains the substring "lock", and
// the lock tables contain "PROC".
func TestShmemCatOf(t *testing.T) {
	cases := []struct {
		name string
		a    pg.ShmemAllocation
		want shmemCat
	}{
		// The two NULL-name rows are classified by flag, never by name.
		{"anonymous", pg.ShmemAllocation{Anonymous: true}, catAnon},
		{"free tail", pg.ShmemAllocation{Free: true}, catFree},

		// Per-backend regions named "Backend … Buffer" must match before the
		// buffer-pool test.
		{"backend activity buffer", pg.ShmemAllocation{Name: "Backend Activity Buffer"}, catBackends},
		{"backend status array", pg.ShmemAllocation{Name: "Backend Status Array"}, catBackends},
		{"shmInvalBuffer", pg.ShmemAllocation{Name: "shmInvalBuffer"}, catBackends},

		// "Buffer Blocks" contains "lock" (b·lock·s): buffer must win over locks.
		{"buffer blocks", pg.ShmemAllocation{Name: "Buffer Blocks"}, catBuffer},
		{"buffer descriptors", pg.ShmemAllocation{Name: "Buffer Descriptors"}, catBuffer},
		{"checkpointer data", pg.ShmemAllocation{Name: "Checkpointer Data"}, catBuffer},
		{"checkpoint bufferids", pg.ShmemAllocation{Name: "Checkpoint BufferIds"}, catBuffer},

		// Lock tables contain "PROC": locks must win over the broad "proc"
		// backend test. "SERIALIZABLEXACT" also contains "xact": locks first.
		{"lock manager", pg.ShmemAllocation{Name: "Lock Manager"}, catLocks},
		{"proclock hash", pg.ShmemAllocation{Name: "PROCLOCK hash"}, catLocks},
		{"predicate lock target hash", pg.ShmemAllocation{Name: "PREDICATELOCKTARGET hash"}, catLocks},
		{"serializable xact hash", pg.ShmemAllocation{Name: "SERIALIZABLEXACT hash"}, catLocks},

		{"xlog ctl", pg.ShmemAllocation{Name: "XLOG Ctl"}, catWAL},
		{"wal receiver ctl", pg.ShmemAllocation{Name: "Wal Receiver Ctl"}, catWAL},

		{"async queue control", pg.ShmemAllocation{Name: "Async Queue Control"}, catXact},
		{"known assigned xids", pg.ShmemAllocation{Name: "KnownAssignedXids"}, catXact},
		{"shared multixact state", pg.ShmemAllocation{Name: "Shared MultiXact State"}, catXact},

		{"proc header", pg.ShmemAllocation{Name: "Proc Header"}, catBackends},
		{"pmsignal state", pg.ShmemAllocation{Name: "PMSignalState"}, catBackends},
		{"background worker data", pg.ShmemAllocation{Name: "Background Worker Data"}, catBackends},

		{"pg_stat_statements", pg.ShmemAllocation{Name: "pg_stat_statements"}, catStats},
		{"shared memory stats", pg.ShmemAllocation{Name: "Shared Memory Stats"}, catStats},

		{"unmatched name", pg.ShmemAllocation{Name: "Archiver Data"}, catOther},
		{"empty name", pg.ShmemAllocation{}, catOther},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shmemCatOf(c.a); got != c.want {
				t.Errorf("shmemCatOf(%q) = %v (%s), want %v (%s)",
					c.a.Name, got, got.label(), c.want, c.want.label())
			}
		})
	}
}

func TestShmemDisplayName(t *testing.T) {
	if got := shmemDisplayName(pg.ShmemAllocation{Anonymous: true}); got != "<anonymous>" {
		t.Errorf("anonymous display = %q", got)
	}
	if got := shmemDisplayName(pg.ShmemAllocation{Free: true}); got != "<free>" {
		t.Errorf("free display = %q", got)
	}
	if got := shmemDisplayName(pg.ShmemAllocation{Name: "XLOG Ctl"}); got != "XLOG Ctl" {
		t.Errorf("named display = %q", got)
	}
}
