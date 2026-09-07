package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"pgdu/internal/pg"
)

func trail(m *Model) string { return strings.Join(m.crumbs(), " ▸ ") }

// Every level yields a crumb even on a bare screen, so a freshly pushed
// placeholder never leaves a hole in the trail; levelLabel is the last
// fallback, so it must know every level too.
func TestCrumbEveryLevel(t *testing.T) {
	for l := levelTools; l <= levelLast; l++ {
		if levelLabel(l) == "?" {
			t.Errorf("levelLabel(%d) has no case", l)
		}
		cs := newTestModel(&screen{level: l}).crumbs()
		want := 2
		if l == levelTools {
			want = 1
		}
		if len(cs) != want {
			t.Errorf("level %d: %d crumbs, want %d: %q", l, len(cs), want, cs)
		}
		for _, c := range cs {
			if strings.TrimSpace(c) == "" || strings.Contains(c, "?") {
				t.Errorf("level %d: bad crumb %q", l, c)
			}
		}
	}
}

func TestBreadcrumbDiskTree(t *testing.T) {
	orders := pg.Table{DB: "shop", Schema: "public", Name: "orders"}
	m := newTestModel(
		&screen{level: levelDatabases, tool: toolDisk},
		&screen{level: levelSchemas, tool: toolDisk, db: "shop"},
		&screen{level: levelTables, tool: toolDisk, db: "shop", schema: "public"},
		&screen{level: levelParts, tool: toolDisk, db: "shop", schema: "public", table: orders},
		&screen{level: levelColumns, tool: toolDisk, db: "shop", schema: "public", table: orders},
	)
	if got, want := trail(m), "db:5432 ▸ disk ▸ shop ▸ public ▸ orders ▸ heap"; got != want {
		t.Errorf("trail = %q, want %q", got, want)
	}
	if got := trail(newTestModel()); got != "db:5432" {
		t.Errorf("root trail = %q", got)
	}
}

// A screen that switches tool mid-trail is prefixed with the tool name; a
// tool's own entry screen is not (its crumb already is the tool name).
func TestBreadcrumbToolSwitch(t *testing.T) {
	orders := pg.Table{DB: "shop", Schema: "public", Name: "orders"}
	unused := &pg.Diagnostic{Key: "unused_indexes", Title: "Unused indexes", PerDB: true}
	cases := []struct {
		name  string
		stack []*screen
		want  string
	}{
		{"table overview → disk parts", []*screen{
			{level: levelDatabases, tool: toolTableStats},
			{level: levelSchemas, tool: toolTableStats, db: "shop"},
			{level: levelTableStats, tool: toolTableStats, db: "shop", schema: "public"},
			{level: levelParts, tool: toolDisk, db: "shop", schema: "public", table: orders},
		}, "db:5432 ▸ tables ▸ shop ▸ public ▸ disk: orders"},
		{"describe → page inspector", []*screen{
			{level: levelDatabases, tool: toolDisk},
			{level: levelSchemas, tool: toolDisk, db: "shop"},
			{level: levelTables, tool: toolDisk, db: "shop", schema: "public"},
			{level: levelParts, tool: toolDisk, db: "shop", schema: "public", table: orders},
			{level: levelDescribe, tool: toolDisk, db: "shop", schema: "public", table: orders,
				desc: describeState{info: &pg.Description{Title: "public.orders"}}},
			{level: levelIndexPages, tool: toolPageInspect, db: "shop", schema: "public",
				pages: pageState{index: pg.Relation{Schema: "public", Name: "orders_pkey"}}},
		}, "db:5432 ▸ disk ▸ shop ▸ public ▸ orders ▸ describe orders ▸ pageinspect: orders_pkey"},
		{"triage → lock tree", []*screen{
			{level: levelTriage, tool: toolTriage, db: "postgres"},
			{level: levelLockTree, tool: toolActivity, db: "postgres"},
		}, "db:5432 ▸ triage ▸ activity: lock tree"},
		{"triage → activity entry", []*screen{
			{level: levelTriage, tool: toolTriage, db: "postgres"},
			{level: levelActivity, tool: toolActivity, db: "postgres"},
		}, "db:5432 ▸ triage ▸ activity"},
		{"triage → per-db diagnostic", []*screen{
			{level: levelTriage, tool: toolTriage, db: "postgres"},
			{level: levelDiagnosticResult, tool: toolTools, db: "shop", diag: unused},
		}, "db:5432 ▸ triage ▸ tools: Unused indexes (shop)"},
		{"activity → progress cross-link", []*screen{
			{level: levelActivity, tool: toolActivity, db: "postgres"},
			{level: levelProgress, tool: toolMaintenance, db: "postgres"},
		}, "db:5432 ▸ activity ▸ progress"},
		{"single-database fast path skips the picker", []*screen{
			{level: levelSchemas, tool: toolDisk, db: "shop"},
		}, "db:5432 ▸ disk: shop"},
		{"log file given on the command line", []*screen{
			{level: levelLogs, tool: toolLogs, db: "postgres", title: "log"},
		}, "db:5432 ▸ logs"},
	}
	for _, c := range cases {
		if got := trail(newTestModel(c.stack...)); got != c.want {
			t.Errorf("%s: trail = %q, want %q", c.name, got, c.want)
		}
	}
}

// The first db-scoped screen whose database the trail hasn't named gets a
// "(db)" suffix, once; cluster-wide screens never do.
func TestBreadcrumbDBQualifier(t *testing.T) {
	act := &screen{level: levelActivity, tool: toolActivity, db: "postgres"}
	placeholder := &screen{level: levelStatementDetail, tool: toolActivity, db: "shop", title: "query", loading: true}
	if got, want := trail(newTestModel(act, placeholder)), "db:5432 ▸ activity ▸ query (shop)"; got != want {
		t.Errorf("placeholder trail = %q, want %q", got, want)
	}
	detail := &screen{level: levelStatementDetail, tool: toolActivity, db: "shop", title: "query",
		stat: stmtState{detail: &pg.QueryStat{QueryID: 8123}}}
	values := &screen{level: levelStatementSamples, tool: toolActivity, db: "shop", title: "values"}
	disk := &screen{level: levelParts, tool: toolDisk, db: "shop", title: "disk", loading: true}
	if got, want := trail(newTestModel(act, detail, values)), "db:5432 ▸ activity ▸ query 8123 (shop) ▸ values"; got != want {
		t.Errorf("values trail = %q, want %q", got, want)
	}
	if got, want := trail(newTestModel(act, detail, disk)), "db:5432 ▸ activity ▸ query 8123 (shop) ▸ disk"; got != want {
		t.Errorf("disk placeholder trail = %q, want %q", got, want)
	}
	disk.table = pg.Table{DB: "shop", Schema: "public", Name: "orders"}
	if got, want := trail(newTestModel(act, detail, disk)), "db:5432 ▸ activity ▸ query 8123 (shop) ▸ disk: orders"; got != want {
		t.Errorf("resolved disk trail = %q, want %q", got, want)
	}
	viaQueries := newTestModel(
		&screen{level: levelDatabases, tool: toolQueries},
		&screen{level: levelStatements, tool: toolQueries, db: "shop"},
		&screen{level: levelStatementDetail, tool: toolQueries, db: "shop", stat: stmtState{detail: &pg.QueryStat{QueryID: 8123}}},
	)
	if got, want := trail(viaQueries), "db:5432 ▸ queries ▸ shop ▸ query 8123"; got != want {
		t.Errorf("queries trail = %q, want %q", got, want)
	}
}

func TestBreadcrumbDiagFlow(t *testing.T) {
	unused := &pg.Diagnostic{Key: "unused_indexes", Title: "Unused indexes", PerDB: true}
	slots := &pg.Diagnostic{Key: "replication_slots", Title: "Replication slots"}
	list := &screen{level: levelDiagnostics, tool: toolTools}
	picker := &screen{level: levelDatabases, tool: toolTools, diag: unused}
	cases := []struct {
		name  string
		stack []*screen
		want  string
	}{
		{"picker → one db", []*screen{list, picker,
			{level: levelDiagnosticResult, tool: toolTools, diag: unused, db: "shop"}},
			"db:5432 ▸ tools ▸ Unused indexes ▸ shop"},
		{"picker → all", []*screen{list, picker,
			{level: levelDiagnosticResult, tool: toolTools, diag: unused, diagAllDBs: true}},
			"db:5432 ▸ tools ▸ Unused indexes ▸ all databases"},
		{"single-db fast path", []*screen{list,
			{level: levelDiagnosticResult, tool: toolTools, diag: unused, db: "shop"}},
			"db:5432 ▸ tools ▸ Unused indexes (shop)"},
		{"cluster-wide from the system overview", []*screen{
			{level: levelMaintenance, tool: toolMaintenance, db: "postgres"},
			{level: levelDiagnosticResult, tool: toolTools, diag: slots}},
			"db:5432 ▸ system overview ▸ tools: Replication slots"},
	}
	for _, c := range cases {
		if got := trail(newTestModel(c.stack...)); got != c.want {
			t.Errorf("%s: trail = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestBreadcrumbPageInspectorCrumbs(t *testing.T) {
	lvl := int32(2)
	idx := pg.Relation{Schema: "public", Name: "orders_pkey"}
	orders := pg.Table{DB: "shop", Schema: "public", Name: "orders"}
	m := newTestModel(
		&screen{level: levelDatabases, tool: toolPageInspect},
		&screen{level: levelSchemas, tool: toolPageInspect, db: "shop"},
		&screen{level: levelRelations, tool: toolPageInspect, db: "shop", schema: "public"},
		&screen{level: levelIndexPages, tool: toolPageInspect, db: "shop", schema: "public", pages: pageState{index: idx}},
		&screen{level: levelIndexTuples, tool: toolPageInspect, db: "shop", schema: "public",
			pages: pageState{index: idx, indexPageBlkno: 7, indexPageLevel: &lvl}},
		&screen{level: levelHeapTuples, tool: toolPageInspect, db: "shop", schema: "public", table: orders,
			pages: pageState{heapPageBlkno: 17}},
	)
	want := "db:5432 ▸ pageinspect ▸ shop ▸ public ▸ orders_pkey ▸ page #7 L2 ▸ orders page #17"
	if got := trail(m); got != want {
		t.Errorf("trail = %q, want %q", got, want)
	}
	gist := &screen{level: levelIndexTuples, tool: toolPageInspect, pages: pageState{indexPageBlkno: 3, indexPageType: "leaf"}}
	if got, _ := crumbText(gist, nil, crumbScope{}); got != "page #3 leaf" {
		t.Errorf("gist crumb = %q", got)
	}
	heap := &screen{level: levelHeapTuples, table: orders, pages: pageState{heapPageBlkno: 4}}
	if got, _ := crumbText(heap, &screen{level: levelHeapPages}, crumbScope{}); got != "page #4" {
		t.Errorf("heap crumb under heap pages = %q", got)
	}
}

// A trail wider than its budget collapses from the middle, keeping the host
// and the current location; the header never grows a line.
func TestBreadcrumbTruncation(t *testing.T) {
	orders := pg.Table{DB: "shop_production", Schema: "public_archive", Name: "orders_history"}
	m := newTestModel(
		&screen{level: levelDatabases, tool: toolDisk},
		&screen{level: levelSchemas, tool: toolDisk, db: orders.DB},
		&screen{level: levelTables, tool: toolDisk, db: orders.DB, schema: orders.Schema},
		&screen{level: levelParts, tool: toolDisk, db: orders.DB, schema: orders.Schema, table: orders},
		&screen{level: levelColumns, tool: toolDisk, db: orders.DB, schema: orders.Schema, table: orders},
	)
	m.width, m.height = 40, 24
	hdr := m.renderHeader()
	if strings.Count(hdr, "\n") != 2 {
		t.Fatalf("header must stay 3 lines:\n%s", hdr)
	}
	line := ansi.Strip(strings.SplitN(hdr, "\n", 2)[0])
	if ansi.StringWidth(line) > m.width {
		t.Errorf("header line %d cells wide, terminal is %d: %q", ansi.StringWidth(line), m.width, line)
	}
	if !strings.Contains(line, "db:5432 ▸ … ▸") || !strings.HasSuffix(line, "heap") {
		t.Errorf("collapsed line = %q", line)
	}
	m.width = 200
	if line := ansi.Strip(strings.SplitN(m.renderHeader(), "\n", 2)[0]); strings.Contains(line, "…") {
		t.Errorf("wide header must not collapse: %q", line)
	}

	plain := ansi.Strip(renderTrail([]string{"host", "middle", "tail"}, 12))
	if plain != "host ▸ … ▸ t…" && plain != "host ▸ … ▸ …" {
		t.Errorf("clipped tail = %q", plain)
	}
	if got := ansi.Strip(renderTrail([]string{"host", "tail"}, 100)); got != "host ▸ tail" {
		t.Errorf("short trail = %q", got)
	}
}

// Identity moved into the trail: the status row keeps only view state.
func TestRenderStatusDropsIdentity(t *testing.T) {
	m := newTestModel(&screen{level: levelParts, tool: toolDisk, db: "shop", table: pg.Table{Name: "orders"}})
	st := m.renderStatus(m.top())
	for _, bad := range []string{"level:", "table:", "db:", "query:"} {
		if strings.Contains(st, bad) {
			t.Errorf("parts status row still carries %q: %q", bad, st)
		}
	}
	if !strings.Contains(st, "sort:") {
		t.Errorf("status row lost the sort label: %q", st)
	}
	diag := &pg.Diagnostic{Title: "Unused indexes", PerDB: true}
	m = newTestModel(&screen{level: levelDiagnosticResult, tool: toolTools, db: "shop", diag: diag})
	if st := m.renderStatus(m.top()); strings.Contains(st, "query:") || strings.Contains(st, "db:") {
		t.Errorf("diag status row = %q", st)
	}

	rec := &screen{level: levelWALRecords, wal: walState{rmgr: "Heap", start: "0/1A", end: "0/2B"}}
	if got := walStatusLabel(rec); got != "window: 1A–2B" {
		t.Errorf("records label = %q", got)
	}
	if got := walStatusLabel(&screen{level: levelWALBlocks, wal: walState{rmgr: "Heap", recLSN: "0/1A"}}); got != "" {
		t.Errorf("blocks label = %q, want none (rmgr and record are crumbs)", got)
	}
	det := &screen{level: levelWALBlockDetail, wal: walState{blockRef: &pg.WALBlockRef{StartLSN: "0/1A", Rmgr: "Heap", RecordType: "INSERT"}}}
	if got := walStatusLabel(det); !strings.Contains(got, "Heap/INSERT") {
		t.Errorf("block detail label = %q", got)
	}
}

// Opening a query from the activity table pushes a loading placeholder that the
// loaded QueryStat fills in place — one detail screen, one crumb, and Back
// returns to activity instead of an orphaned spinner.
func TestActivityStatementFillsPlaceholder(t *testing.T) {
	act := &screen{level: levelActivity, tool: toolActivity, db: "postgres", loaded: true,
		act:   actState{rows: []pg.ActivityRow{{PID: 42, Database: "shop", QueryID: 8123, Query: "select 1"}}},
		items: []item{{name: "42", statQueryID: 8123}}}
	m := newTestModel(act)
	m.drillActivityStatement(act, act.items[0])
	if len(m.stack) != 3 || m.top().level != levelStatementDetail || !m.top().loading {
		t.Fatalf("placeholder not pushed: depth %d, top %v", len(m.stack), m.top().level)
	}
	m.onActivityStatement(activityStatementMsg{db: "shop", pid: 42, qs: &pg.QueryStat{QueryID: 8123, Query: "select 1"}})
	if len(m.stack) != 3 {
		t.Fatalf("stack depth %d after load, want 3 (placeholder filled, not doubled)", len(m.stack))
	}
	top := m.top()
	if top.loading || top.stat.detail == nil || top.stat.detail.QueryID != 8123 || top.db != "shop" {
		t.Errorf("placeholder not filled: loading=%v detail=%v db=%q", top.loading, top.stat.detail, top.db)
	}
	if got, want := trail(m), "db:5432 ▸ activity ▸ query 8123 (shop)"; got != want {
		t.Errorf("trail = %q, want %q", got, want)
	}

	// A failed load pops the placeholder so the user isn't parked on a spinner.
	m = newTestModel(act)
	m.drillActivityStatement(act, act.items[0])
	m.onActivityStatement(activityStatementMsg{db: "shop", pid: 42, err: errors.New("boom")})
	if len(m.stack) != 2 || m.top() != act || m.notice == "" {
		t.Errorf("failed load: depth %d, notice %q", len(m.stack), m.notice)
	}
}

// The whole view must fit the terminal exactly: Bubble Tea drops overflowing
// lines from the top, which would silently hide the title/breadcrumb line.
func TestViewFitsTerminal(t *testing.T) {
	diag := &pg.Diagnostic{Title: "Index bloat", PerDB: true}
	orders := pg.Table{DB: "shop", Schema: "public", Name: "orders", OID: 1}
	cases := map[string][]*screen{
		"tools":       {},
		"databases":   {{level: levelDatabases, tool: toolDisk, loaded: true, items: []item{{name: "a", data: pg.Database{Name: "a"}}}}},
		"schemas":     {{level: levelDatabases, tool: toolDisk}, {level: levelSchemas, tool: toolDisk, db: "shop", loaded: true}},
		"tables":      {{level: levelTables, tool: toolDisk, db: "shop", schema: "public", loaded: true}},
		"parts":       {{level: levelParts, tool: toolDisk, db: "shop", schema: "public", table: orders, loaded: true}},
		"loading":     {{level: levelParts, tool: toolDisk, title: "disk", loading: true}},
		"diagnostics": {{level: levelDiagnostics, tool: toolTools, loaded: true}},
		"diagresult":  {{level: levelDiagnostics, tool: toolTools}, {level: levelDiagnosticResult, tool: toolTools, diag: diag, db: "shop", loaded: true, diagBarCol: -1}},
		"statements":  {{level: levelDatabases, tool: toolQueries}, {level: levelStatements, tool: toolQueries, db: "shop", loaded: true}},
		"activity":    {{level: levelActivity, tool: toolActivity, db: "postgres", loaded: true}},
		"maintenance": {{level: levelMaintenance, tool: toolMaintenance, db: "postgres", loaded: true}},
		"triage":      {{level: levelTriage, tool: toolTriage, db: "postgres", loaded: true}},
		"wal":         {{level: levelWAL, tool: toolWAL, db: "postgres", loaded: true}},
		"pgbouncers":  {{level: levelPgBouncers, tool: toolPgBouncer, db: "postgres", loaded: true}},
		"snapshots":   {{level: levelSnapshots, tool: toolQueries, db: "shop", loaded: true}},
	}
	for name, st := range cases {
		for _, h := range []int{12, 30, 61} {
			m := newTestModel(st...)
			m.width, m.height = 120, h
			v := m.View()
			if lines := strings.Count(v, "\n") + 1; lines != h {
				t.Errorf("%s at height %d renders %d lines", name, h, lines)
			}
			if first := ansi.Strip(strings.SplitN(v, "\n", 2)[0]); !strings.Contains(first, "pgdu") || !strings.Contains(first, "db:5432") {
				t.Errorf("%s: first line lost the title/trail: %q", name, first)
			}
		}
	}
}

// When the single-schema fast path skips the schema picker, the table list
// stands in for the database and relations are qualified with their schema —
// except public, which goes without saying — until the trail has spelled it.
func TestBreadcrumbSchemaQualifier(t *testing.T) {
	orders := pg.Table{DB: "shop", Schema: "public", Name: "orders"}
	appOrders := pg.Table{DB: "shop", Schema: "app", Name: "orders"}
	pkey := pg.Relation{Schema: "app", Name: "orders_pkey"}
	lvl := int32(2)
	cases := []struct {
		name  string
		stack []*screen
		want  string
	}{
		{"fast path, public", []*screen{
			{level: levelDatabases, tool: toolDisk},
			{level: levelTables, tool: toolDisk, db: "shop", schema: "public"},
			{level: levelParts, tool: toolDisk, db: "shop", schema: "public", table: orders},
			{level: levelColumns, tool: toolDisk, db: "shop", schema: "public", table: orders},
		}, "db:5432 ▸ disk ▸ shop ▸ orders ▸ heap"},
		{"fast path, table list alone", []*screen{
			{level: levelDatabases, tool: toolDisk},
			{level: levelTables, tool: toolDisk, db: "shop", schema: "app"},
		}, "db:5432 ▸ disk ▸ shop"},
		{"fast path, other schema", []*screen{
			{level: levelDatabases, tool: toolDisk},
			{level: levelTables, tool: toolDisk, db: "shop", schema: "app"},
			{level: levelParts, tool: toolDisk, db: "shop", schema: "app", table: appOrders},
			{level: levelColumns, tool: toolDisk, db: "shop", schema: "app", table: appOrders},
			{level: levelDescribe, tool: toolDisk, db: "shop", schema: "app", table: appOrders},
		}, "db:5432 ▸ disk ▸ shop ▸ app.orders ▸ heap ▸ describe orders"},
		{"schema picked, other schema", []*screen{
			{level: levelDatabases, tool: toolDisk},
			{level: levelSchemas, tool: toolDisk, db: "shop"},
			{level: levelTables, tool: toolDisk, db: "shop", schema: "app"},
			{level: levelParts, tool: toolDisk, db: "shop", schema: "app", table: appOrders},
		}, "db:5432 ▸ disk ▸ shop ▸ app ▸ orders"},
		{"both pickers skipped", []*screen{
			{level: levelTables, tool: toolDisk, db: "shop", schema: "public"},
			{level: levelParts, tool: toolDisk, db: "shop", schema: "public", table: orders},
		}, "db:5432 ▸ disk: shop ▸ orders"},
		{"queries → disk parts", []*screen{
			{level: levelDatabases, tool: toolQueries},
			{level: levelStatements, tool: toolQueries, db: "shop"},
			{level: levelStatementDetail, tool: toolQueries, db: "shop", stat: stmtState{detail: &pg.QueryStat{QueryID: 8123}}},
			{level: levelParts, tool: toolDisk, db: "shop", title: "disk", table: appOrders},
		}, "db:5432 ▸ queries ▸ shop ▸ query 8123 ▸ disk: app.orders"},
		{"activity → describe in another db", []*screen{
			{level: levelActivity, tool: toolActivity, db: "postgres"},
			{level: levelDescribe, tool: toolActivity, db: "shop", title: "describe", table: appOrders},
		}, "db:5432 ▸ activity ▸ describe app.orders (shop)"},
		{"table overview fast path → disk parts", []*screen{
			{level: levelDatabases, tool: toolTableStats},
			{level: levelTableStats, tool: toolTableStats, db: "shop", schema: "public"},
			{level: levelParts, tool: toolDisk, db: "shop", schema: "public", table: orders},
		}, "db:5432 ▸ tables ▸ shop ▸ disk: orders"},
		{"buffers fast path → detail", []*screen{
			{level: levelDatabases, tool: toolBuffers},
			{level: levelBufferTables, tool: toolBuffers, db: "shop", schema: "app"},
			{level: levelBufferDetail, tool: toolBuffers, db: "shop", schema: "app",
				buf: bufState{detail: &pg.TableBufferStat{DB: "shop", Schema: "app", Name: "orders"}}},
		}, "db:5432 ▸ buffers ▸ shop ▸ app.orders"},
		{"page inspector fast path, index → heap hop", []*screen{
			{level: levelDatabases, tool: toolPageInspect},
			{level: levelRelations, tool: toolPageInspect, db: "shop", schema: "app"},
			{level: levelIndexPages, tool: toolPageInspect, db: "shop", schema: "app", pages: pageState{index: pkey}},
			{level: levelIndexTuples, tool: toolPageInspect, db: "shop", schema: "app",
				pages: pageState{index: pkey, indexPageBlkno: 7, indexPageLevel: &lvl}},
			{level: levelHeapTuples, tool: toolPageInspect, db: "shop", schema: "app", table: appOrders,
				pages: pageState{heapPageBlkno: 17}},
		}, "db:5432 ▸ pageinspect ▸ shop ▸ app.orders_pkey ▸ page #7 L2 ▸ orders page #17"},
	}
	for _, c := range cases {
		if got := trail(newTestModel(c.stack...)); got != c.want {
			t.Errorf("%s: trail = %q, want %q", c.name, got, c.want)
		}
	}
	// A loading parts placeholder in a non-default schema must not render a
	// dangling "schema." before the table name arrives.
	if got := (crumbScope{}).qualify("shop", "app", ""); got != "" {
		t.Errorf("qualify with no name = %q", got)
	}
}
