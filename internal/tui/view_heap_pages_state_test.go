package tui

import (
	"strings"
	"testing"

	"pgdu/internal/pg"
)

// Transaction ids the tables below point at. Package-level so the composite
// literals can take their addresses without a helper call.
var (
	xid0  uint32
	xid10 uint32 = 10
	xid99 uint32 = 99
)

// TestHeapTupleState pins the xmax verdict, whose bits do not read in the
// obvious order: HEAP_XMAX_LOCK_ONLY makes a set, committed xmax a *row lock*
// on a live row rather than a delete, and a multixact only means "locked" when
// it carries no committed updater.
func TestHeapTupleState(t *testing.T) {
	const (
		committed = pg.HeapXminCommitted
		lockOnly  = pg.HeapXmaxLockOnly
		xmaxCmt   = pg.HeapXmaxCommitted
		xmaxInv   = pg.HeapXmaxInvalid
		multi     = pg.HeapXmaxIsMulti
	)
	for _, tc := range []struct {
		name     string
		infomask int32
		xmax     *uint32
		want     string
	}{
		{"plain live", committed, nil, "live"},
		{"xmax zero", committed, &xid0, "live"},
		{"xmax invalid", committed | xmaxInv, &xid99, "live"},
		{"deleted committed", committed | xmaxCmt, &xid99, "dead"},
		{"delete in flight", committed, &xid99, "deleting"},

		// The regression this test exists for: SELECT … FOR UPDATE sets xmax
		// and LOCK_ONLY, and a later visibility check sets XMAX_COMMITTED.
		// Reading the commit bit first called that row dead.
		{"locked, locker committed", committed | lockOnly | xmaxCmt, &xid99, "locked"},
		{"locked, locker running", committed | lockOnly, &xid99, "locked"},
		{"locked by several", committed | lockOnly | multi | xmaxCmt, &xid99, "locked"},

		// A multixact without LOCK_ONLY carries an updater; once it commits
		// the row really is dead, so the multi check must not short-circuit.
		{"multixact with committed updater", committed | multi | xmaxCmt, &xid99, "dead"},
		{"multixact updater running", committed | multi, &xid99, "locked"},

		{"frozen", committed | pg.HeapXminInvalid, nil, "frozen"},
		{"aborted inserter", pg.HeapXminInvalid, nil, "aborted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tup := pg.HeapTuple{LPFlags: pg.LPNormal, Infomask: tc.infomask, Xmax: tc.xmax}
			if got, _ := heapTupleState(tup); got != tc.want {
				t.Errorf("heapTupleState = %q, want %q", got, tc.want)
			}
		})
	}

	// Slot state still wins over any infomask reading.
	for flags, want := range map[int32]string{
		pg.LPUnused:   "unused",
		pg.LPDead:     "dead",
		pg.LPRedirect: "redirect",
	} {
		tup := pg.HeapTuple{LPFlags: flags, Infomask: committed | lockOnly, Xmax: &xid99}
		if got, _ := heapTupleState(tup); got != want {
			t.Errorf("lp_flags %d: heapTupleState = %q, want %q", flags, got, want)
		}
	}
}

// The lifecycle sentence must agree with the verdict: a locked row is not
// "deleted", and it names the lock mode instead of the bare xid.
func TestTupleLifecycleLineLockOnly(t *testing.T) {
	locked := pg.HeapTuple{
		LPFlags:   pg.LPNormal,
		Infomask:  pg.HeapXminCommitted | pg.HeapXmaxLockOnly | pg.HeapXmaxCommitted | pg.HeapXmaxExclLock,
		Infomask2: pg.HeapKeysUpdated2,
		Xmin:      &xid10, Xmax: &xid99,
	}
	got := tupleLifecycleLine(locked, "")
	for _, want := range []string{"not deleted", "locked for update", "t99"} {
		if !strings.Contains(got, want) {
			t.Errorf("lifecycle %q missing %q", got, want)
		}
	}
	if strings.Contains(got, "deleted t99") {
		t.Errorf("lifecycle %q calls a locked row deleted", got)
	}
	// xmaxLockMode already spent HEAP_KEYS_UPDATED naming the mode.
	if strings.Contains(got, "key cols updated") {
		t.Errorf("lifecycle %q states the key-cols bit twice", got)
	}

	deleted := pg.HeapTuple{
		LPFlags:  pg.LPNormal,
		Infomask: pg.HeapXminCommitted | pg.HeapXmaxCommitted,
		Xmin:     &xid10, Xmax: &xid99,
	}
	if got := tupleLifecycleLine(deleted, ""); !strings.Contains(got, "deleted t99 (committed)") {
		t.Errorf("lifecycle %q should still report a real delete", got)
	}
}

// xmaxLockMode decodes HEAP_XMAX_IS_*_LOCKED: both mode bits together are FOR
// SHARE, EXCL alone is FOR NO KEY UPDATE unless HEAP_KEYS_UPDATED promotes it.
func TestXmaxLockMode(t *testing.T) {
	for _, tc := range []struct {
		im, im2 uint16
		want    string
	}{
		{pg.HeapXmaxKeyshrLock, 0, "locked for key share"},
		{pg.HeapXmaxExclLock | pg.HeapXmaxKeyshrLock, 0, "locked for share"},
		{pg.HeapXmaxExclLock, 0, "locked for no key update"},
		{pg.HeapXmaxExclLock, pg.HeapKeysUpdated2, "locked for update"},
		{pg.HeapXmaxKeyshrLock | pg.HeapXmaxIsMulti, 0, "locked for key share (multixact)"},
		{0, 0, "locked"},
	} {
		if got := xmaxLockMode(tc.im, tc.im2); got != tc.want {
			t.Errorf("xmaxLockMode(%#04x, %#04x) = %q, want %q", tc.im, tc.im2, got, tc.want)
		}
	}
}

// The flag column carries three independent facts; a page that is bloated and
// HOT-updating and TOASTing must show all three, not whichever a switch hit.
func TestHeapPageFlags(t *testing.T) {
	all := pg.HeapPageStat{
		PageSize: 8192, Lower: 24, Upper: 8192,
		LiveLP: 1, DeadLP: 5, HotUpdated: 2, HasExternal: 3,
	}
	got := heapPageFlags(all)
	for _, want := range []string{"!", "H", "T"} {
		if !strings.Contains(got, want) {
			t.Errorf("flags %q missing %q", got, want)
		}
	}

	clean := pg.HeapPageStat{PageSize: 8192, Lower: 24, Upper: 8192, LiveLP: 9}
	if got := heapPageFlags(clean); strings.TrimSpace(got) != "" {
		t.Errorf("clean page flags = %q, want blank", got)
	}

	// An all-zero page is "new", and raises nothing else.
	if got := heapPageFlags(pg.HeapPageStat{}); !strings.Contains(got, "N") {
		t.Errorf("uninitialised page flags = %q, want N", got)
	}
}

// The B-tree and GIN page summaries clamp their lower bound to block 1 (block
// 0 is the metapage), so a window asked for at 0 comes back holding blocks
// 1…N. The label must name the blocks actually loaded, not the arithmetic
// window, or it is off by one at both ends.
func TestHeapPageWindowLabel(t *testing.T) {
	idxPage := func(blk int32) item { return item{data: pg.IndexPageStat{Blkno: blk}} }

	s := &screen{
		level: levelIndexPages,
		items: []item{idxPage(1), idxPage(2), idxPage(3)},
		pages: pageState{heapWindowStart: 0, heapWindowCount: 2000, heapPageCount: 12345},
	}
	if got, want := heapPageWindowLabel(s), "pages 1–3 / 12345"; got != want {
		t.Errorf("index label = %q, want %q", got, want)
	}

	// Heap pages start at 0 and are unaffected.
	s = &screen{
		level: levelHeapPages,
		items: []item{heapPageItem(0), heapPageItem(1)},
		pages: pageState{heapWindowCount: 2000, heapPageCount: 500},
	}
	if got, want := heapPageWindowLabel(s), "pages 0–1 / 500"; got != want {
		t.Errorf("heap label = %q, want %q", got, want)
	}

	// A display sort that reorders the rows must not reorder the range.
	s.items = []item{heapPageItem(1), heapPageItem(0)}
	if got, want := heapPageWindowLabel(s), "pages 0–1 / 500"; got != want {
		t.Errorf("sorted label = %q, want %q", got, want)
	}

	// Nothing loaded, nothing to say.
	if got := heapPageWindowLabel(&screen{level: levelHeapPages}); got != "" {
		t.Errorf("empty label = %q, want \"\"", got)
	}
}

// ctidSuccessor is the gate for the n key: only a tuple whose t_ctid names a
// different line pointer has a next version. A current row points at itself.
func TestCtidSuccessor(t *testing.T) {
	ctid := func(s string) *string { return &s }
	mk := func(lp int32, flags int32, c *string) *screen {
		return &screen{
			level:  levelHeapTuples,
			items:  []item{{data: pg.HeapTuple{LP: lp, LPFlags: flags, Ctid: c}}},
			loaded: true,
			pages:  pageState{heapPageBlkno: 7},
		}
	}
	if ctidSuccessor(mk(3, pg.LPNormal, ctid("(7,3)"))) != nil {
		t.Error("a row pointing at itself has no successor")
	}
	if ctidSuccessor(mk(3, pg.LPNormal, nil)) != nil {
		t.Error("a nil ctid has no successor")
	}
	if ctidSuccessor(mk(3, pg.LPDead, ctid("(9,1)"))) != nil {
		t.Error("a DEAD slot carries no t_ctid of its own")
	}
	if got := ctidSuccessor(mk(3, pg.LPNormal, ctid("(9,1)"))); got == nil {
		t.Error("a cross-page forward pointer is a successor")
	}
	// Same page, different lp — a HOT update whose new version stayed here.
	if got := ctidSuccessor(mk(3, pg.LPNormal, ctid("(7,8)"))); got == nil {
		t.Error("a same-page forward pointer to another lp is a successor")
	}
	// Off-level, the key must not arm at all.
	s := mk(3, pg.LPNormal, ctid("(9,1)"))
	s.level = levelHeapPages
	if ctidSuccessor(s) != nil {
		t.Error("ctidSuccessor armed off levelHeapTuples")
	}
}

// followCtid moves the cursor within the page and pushes a new page screen
// across pages, with focusLP armed so the load lands on the successor.
func TestFollowCtid(t *testing.T) {
	ctid := func(s string) *string { return &s }

	// Same page: cursor moves to lp 8, no screen pushed.
	s := &screen{
		level: levelHeapTuples,
		items: []item{
			{data: pg.HeapTuple{LP: 3, LPFlags: pg.LPNormal, Ctid: ctid("(7,8)")}},
			{data: pg.HeapTuple{LP: 8, LPFlags: pg.LPNormal, Ctid: ctid("(7,8)")}},
		},
		pages: pageState{heapPageBlkno: 7},
	}
	m := &Model{stack: []*screen{s}}
	if cmd := m.followCtid(s); cmd != nil {
		t.Error("a same-page hop must not reload")
	}
	if s.cursor != 1 {
		t.Errorf("cursor = %d, want 1 (lp 8)", s.cursor)
	}
	if len(m.stack) != 1 {
		t.Errorf("stack grew to %d for a same-page hop", len(m.stack))
	}

	// Cross-page: a new tuples screen at block 9, lp 1.
	s = &screen{
		level: levelHeapTuples,
		items: []item{{data: pg.HeapTuple{LP: 3, LPFlags: pg.LPNormal, Ctid: ctid("(9,1)")}}},
		table: pg.Table{DB: "d", Schema: "public", Name: "t", OID: 42},
		pages: pageState{heapPageBlkno: 7, tuplePick: []string{"id"}},
	}
	m = &Model{stack: []*screen{s}}
	m.followCtid(s)
	if len(m.stack) != 2 {
		t.Fatalf("stack = %d screens, want 2", len(m.stack))
	}
	next := m.stack[1]
	if next.level != levelHeapTuples || next.pages.heapPageBlkno != 9 || next.pages.focusLP != 1 {
		t.Errorf("pushed screen: level %v blk %d focusLP %d",
			next.level, next.pages.heapPageBlkno, next.pages.focusLP)
	}
	if next.table.OID != 42 {
		t.Errorf("pushed screen lost the table (OID %d)", next.table.OID)
	}
	// The column pick carries over, so the successor shows the same columns.
	if len(next.pages.tuplePick) != 1 || next.pages.tuplePick[0] != "id" {
		t.Errorf("pushed screen lost the column pick: %v", next.pages.tuplePick)
	}
}
