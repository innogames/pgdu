package tui

import (
	"errors"
	"strings"
	"testing"

	"pgdu/internal/pg"
)

func TestPgbShowRegistryComplete(t *testing.T) {
	seen := map[string]bool{}
	for _, sp := range pgbShowRegistry() {
		if sp.what == "" || sp.title == "" || sp.desc == "" {
			t.Errorf("spec %v incomplete: %+v", sp.show, sp)
		}
		if seen[sp.what] {
			t.Errorf("duplicate SHOW %q", sp.what)
		}
		seen[sp.what] = true
		if sp.show.spec().what != sp.what {
			t.Errorf("spec() round-trip failed for %q", sp.what)
		}
		for _, h := range sp.hidden {
			if vis := pgbDefaultVis(pgbVisKey(sp.show)); vis == nil || vis[h] {
				t.Errorf("%s: hidden column %q not off in the default visibility", sp.what, h)
			}
		}
	}
}

func TestApplyPgbKinds(t *testing.T) {
	num := func(n float64) pg.DiagCell { return pg.DiagCell{Display: "raw", Num: n, HasNum: true} }
	res := &pg.DiagResult{
		Columns: []pg.DiagColumn{
			{Name: "database"}, {Name: "avg_query_time", Kind: pg.DiagInt}, {Name: "maxwait", Kind: pg.DiagInt},
			{Name: "avg_recv", Kind: pg.DiagInt}, {Name: "avg_query_count", Kind: pg.DiagInt},
			{Name: "custom_us", Kind: pg.DiagInt}, {Name: "unknown", Kind: pg.DiagInt},
		},
		Rows: [][]pg.DiagCell{
			{{Display: "shop"}, num(191), num(2), num(1536), num(1992), num(2500000), num(7)},
			{{Display: "pgbouncer"}, {Display: "—"}, num(0), num(0), num(0), num(0), num(0)},
		},
	}
	spec := pgbShowStats.spec()
	spec.kinds["maxwait"] = pgbKindSecs
	applyPgbKinds(res, spec)

	if res.Columns[1].Kind != pg.DiagDuration || res.Rows[0][1].Num != 0.191 {
		t.Errorf("µs column: kind %v num %v (want DiagDuration, 0.191 ms)", res.Columns[1].Kind, res.Rows[0][1].Num)
	}
	if res.Columns[2].Kind != pg.DiagDuration || res.Rows[0][2].Num != 2000 || !strings.Contains(res.Rows[0][2].Display, "s") {
		t.Errorf("secs column: kind %v num %v display %q", res.Columns[2].Kind, res.Rows[0][2].Num, res.Rows[0][2].Display)
	}
	if res.Columns[3].Kind != pg.DiagBytes || res.Rows[0][3].Display == "raw" {
		t.Errorf("bytes column: kind %v display %q", res.Columns[3].Kind, res.Rows[0][3].Display)
	}
	if res.Columns[4].Kind != pg.DiagCount {
		t.Errorf("count column: kind %v", res.Columns[4].Kind)
	}
	if res.Columns[5].Kind != pg.DiagDuration || res.Rows[0][5].Num != 2500 {
		t.Errorf("generic _us rule: kind %v num %v", res.Columns[5].Kind, res.Rows[0][5].Num)
	}
	if res.Columns[6].Kind != pg.DiagInt || res.Rows[0][6].Display != "raw" {
		t.Errorf("unknown column must be untouched: %+v %+v", res.Columns[6], res.Rows[0][6])
	}
	if res.Rows[1][1].Display != "—" {
		t.Errorf("NULL cell rewritten: %+v", res.Rows[1][1])
	}
	if res.SortCol != 4 || res.BarCol != -1 {
		t.Errorf("sort/bar = %d/%d, want avg_query_count (4) / none", res.SortCol, res.BarCol)
	}
}

func TestPgbInstanceItemsParallelToColumns(t *testing.T) {
	insts := []pg.PgBouncerInstance{
		{Name: "pgbouncer_1", PID: 10, SocketDir: "/run/pgbouncer_1", ListenPort: 6432, Logfile: "/var/log/postgresql/pgbouncer_1.log", Reason: "/proc", PoolMode: "transaction"},
		{Name: "pgbouncer_2", Reason: "/etc/pgbouncer", IniPath: "/etc/pgbouncer/pgbouncer_2.ini"},
		{Name: "pgbouncer_3", PID: 12, Reason: "/proc"},
	}
	probes := []pg.PgBouncerProbe{
		{Version: "PgBouncer 1.25.2", Totals: pg.PgBouncerPoolTotals{ClActive: 3, ClWaiting: 1, SvActive: 2, SvIdle: 5, Pools: 2, MaxWaitSec: 1.5}},
		{Err: errors.New("dial: no such file")},
		{Err: errors.New("SASL authentication failed"), AuthErr: true, User: "postgres"},
	}
	cols := pgbInstanceColumns()
	items := pgbInstanceItems(insts, probes)
	if len(items) != 3 {
		t.Fatalf("%d items", len(items))
	}
	for i, it := range items {
		cells := it.data.([]pg.DiagCell)
		if len(cells) != len(cols) {
			t.Errorf("row %d: %d cells vs %d columns", i, len(cells), len(cols))
		}
		if it.pgbIdx != i+1 || !it.hasChildren {
			t.Errorf("row %d: pgbIdx %d hasChildren %v", i, it.pgbIdx, it.hasChildren)
		}
	}
	state := func(i int) string { return items[i].data.([]pg.DiagCell)[1].Display }
	if state(0) != "running" || state(1) != "stopped" || state(2) != "auth denied" {
		t.Errorf("states: %q %q %q", state(0), state(1), state(2))
	}
	first := items[0].data.([]pg.DiagCell)
	if first[2].Display != "1.25.2" {
		t.Errorf("version cell = %q", first[2].Display)
	}
	if first[7].Num != 1 || first[10].Num != 1500 {
		t.Errorf("cl_waiting %v maxwait %v", first[7].Num, first[10].Num)
	}
	if !strings.Contains(items[0].name, "pgbouncer_1") || !strings.Contains(items[0].name, "/proc") {
		t.Errorf("filter text = %q", items[0].name)
	}
}

// The instance list and the overview render through the real View path with a
// hand-built screen (no client needed): the auth hint must reach the screen.
func TestRenderPgBouncerScreens(t *testing.T) {
	m := &Model{width: 200, height: 40, keys: defaultKeys(), pgbRefresh: 0}
	list := &screen{level: levelPgBouncers, title: "pgbouncer", tool: toolPgBouncer, loaded: true}
	list.pgb.insts = []pg.PgBouncerInstance{{Name: "pgbouncer_1", IniPath: "/etc/pgbouncer/pgbouncer_1.ini", SocketDir: "/var/run/pgbouncer_1", AuthFile: "/etc/pgbouncer/userlist.txt"}}
	list.pgb.probes = []pg.PgBouncerProbe{{Err: errors.New("SASL authentication failed"), AuthErr: true, User: "postgres"}}
	list.diagCols = pgbInstanceColumns()
	list.diagBarCol = -1
	list.items = pgbInstanceItems(list.pgb.insts, list.pgb.probes)
	list.diagMetricsDirty = true
	m.stack = []*screen{{level: levelTools}, list}

	hint := m.renderPgbListHint(list)
	for _, want := range []string{"login refused", `"postgres"`, "stats_users", "/etc/pgbouncer/pgbouncer_1.ini", "/var/run/pgbouncer_1:6432:pgbouncer:postgres"} {
		if !strings.Contains(hint, want) {
			t.Errorf("hint lacks %q: %s", want, hint)
		}
	}
	table := m.renderDiagResult(list, 10)
	if !strings.Contains(table, "auth denied") || !strings.Contains(table, "pgbouncer_1") {
		t.Errorf("table:\n%s", table)
	}

	ov := &screen{level: levelPgBouncer, title: "pgbouncer_1", tool: toolPgBouncer, loaded: true, pgb: pgbState{inst: &list.pgb.insts[0]}}
	ov.pgb.inst.PoolMode = "transaction"
	ov.pgb.overview = &pg.PgBouncerOverview{
		Version: "PgBouncer 1.25.2 on x86_64-pc-linux-gnu",
		State:   map[string]string{"active": "yes", "paused": "yes"},
		Lists:   map[string]int64{"databases": 2, "pools": 3},
		Totals:  pg.PgBouncerPoolTotals{Pools: 3, ClActive: 40, ClWaiting: 2, SvActive: 10, SvIdle: 6, MaxWaitSec: 4},
	}
	ov.items = pgbMenuItems(*ov.pgb.inst)
	hdr := m.renderPgBouncerHeader(ov)
	for _, want := range []string{"1.25.2", "PAUSED", "2 waiting", "max wait", "databases", "transaction"} {
		if !strings.Contains(hdr, want) {
			t.Errorf("header lacks %q:\n%s", want, hdr)
		}
	}
	if strings.Contains(hdr, " on x86_64") {
		t.Errorf("version not trimmed at ' on ': %s", hdr)
	}
	if len(ov.items) != len(pgbShowRegistry()) {
		t.Errorf("menu without a logfile must list exactly the SHOW specs: %d rows", len(ov.items))
	}
	ov.pgb.inst.Logfile = "/var/log/postgresql/pgbouncer_1.log"
	if items := pgbMenuItems(*ov.pgb.inst); len(items) != len(pgbShowRegistry())+1 || items[len(items)-1].name != "log" {
		t.Errorf("menu with a logfile must end in the log row")
	}
}

func TestPgbLevelsWired(t *testing.T) {
	for _, l := range []level{levelPgBouncers, levelPgBouncer, levelPgBouncerShow} {
		if levelLabel(l) == "" || levelLabel(l) == "?" {
			t.Errorf("levelLabel(%d) = %q", l, levelLabel(l))
		}
	}
	if tl, ok := toolByName("pgbouncer"); !ok || tl != toolPgBouncer {
		t.Errorf("toolByName(pgbouncer) = %v %v", tl, ok)
	}
	s := &screen{level: levelPgBouncerShow, pgb: pgbState{show: pgbShowClients}}
	if s.diagVisKey() != "pgbouncer/clients" {
		t.Errorf("diagVisKey = %q", s.diagVisKey())
	}
	if vis := defaultDiagVis(s.diagVisKey()); vis == nil || vis["ptr"] {
		t.Errorf("clients default visibility must hide ptr: %v", vis)
	}
}

func TestPgbSecretColumnsDroppedAndHostnameAdded(t *testing.T) {
	res := &pg.DiagResult{
		Columns: []pg.DiagColumn{{Name: "user"}, {Name: "addr"}, {Name: "port"}, {Name: "scram_client_key"}, {Name: "scram_server_key"}, {Name: "wait"}},
		Rows: [][]pg.DiagCell{
			{{Display: "app"}, {Display: "10.1.2.3"}, {Display: "5"}, {Display: "secret"}, {Display: "secret"}, {Display: "0", Num: 0, HasNum: true}},
			{{Display: "root"}, {Display: "unix"}, {Display: "6432"}, {Display: "secret"}, {Display: "secret"}, {Display: "1", Num: 1, HasNum: true}},
		},
	}
	applyPgbKinds(res, pgbShowClients.spec())
	for _, c := range res.Columns {
		if strings.HasPrefix(c.Name, "scram_") {
			t.Fatalf("secret column survived: %v", res.Columns)
		}
	}
	if res.SortCol != 3 || res.Columns[3].Name != "wait" {
		t.Errorf("sort column not re-pointed after the drop: %d %v", res.SortCol, res.Columns)
	}
	addPgbHostnames(res, func(ip string) (string, bool) {
		if ip == "10.1.2.3" {
			return "app1.example", true
		}
		return ip, false
	})
	if len(res.Columns) != 5 || res.Columns[2].Name != "hostname" || res.SortCol != 4 {
		t.Fatalf("hostname column placement: %v sort %d", res.Columns, res.SortCol)
	}
	if res.Rows[0][2].Display != "app1.example" || res.Rows[1][2].Display != "—" {
		t.Errorf("hostname cells: %q %q", res.Rows[0][2].Display, res.Rows[1][2].Display)
	}
	for _, row := range res.Rows {
		if len(row) != len(res.Columns) {
			t.Errorf("row/column mismatch: %d vs %d", len(row), len(res.Columns))
		}
	}
}
