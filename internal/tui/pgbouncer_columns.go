package tui

import (
	"net"
	"strings"

	"pgdu/internal/humanize"
	"pgdu/internal/pg"
)

// pgbShow enumerates the console SHOW tables the tool offers. The screen at
// levelPgBouncerShow carries one of these; everything else about the table
// (columns, kinds) comes from the live result, so a newer pgbouncer that grows
// a column just shows it.
type pgbShow int

const (
	pgbShowPools pgbShow = iota
	pgbShowStats
	pgbShowClients
	pgbShowServers
	pgbShowDatabases
	pgbShowUsers
	pgbShowConfig
	pgbShowMem
	pgbShowSockets
	pgbShowFDs
)

// pgbKind is a column-kind override keyed by column name (pgbShowSpec.kinds).
// pgbouncer reports durations as integer microseconds and sizes as bytes in
// columns whose names the generic name heuristic cannot decode.
type pgbKind int

const (
	pgbKindUS    pgbKind = iota // microseconds → DiagDuration (ms)
	pgbKindSecs                 // whole seconds → DiagDuration (ms)
	pgbKindBytes                // raw bytes → DiagBytes
	pgbKindCount                // large cumulative counter → DiagCount
)

type pgbShowSpec struct {
	show    pgbShow
	what    string // SHOW argument, lower-case
	title   string
	desc    string
	sortCol string   // default sort column (descending); "" → first column ascending
	hidden  []string // columns off by default (C toggles them back on)
	kinds   map[string]pgbKind
}

// pgbShowRegistry is the overview menu, in display order.
func pgbShowRegistry() []pgbShowSpec {
	return []pgbShowSpec{
		{
			show: pgbShowPools, what: "pools", title: "pools",
			desc:    "per database/user pool: clients active/waiting, servers active/idle/used/login, longest wait",
			sortCol: "cl_waiting",
			hidden:  []string{"maxwait_us", "load_balance_hosts", "cl_active_cancel_req", "cl_waiting_cancel_req", "sv_active_cancel", "sv_being_canceled"},
			kinds:   map[string]pgbKind{"maxwait": pgbKindSecs, "maxwait_us": pgbKindUS},
		},
		{
			show: pgbShowStats, what: "stats", title: "stats",
			desc:    "per database: transactions/s, queries/s, bytes in/out per second, average xact/query/wait time (totals via C)",
			sortCol: "avg_query_count",
			hidden: []string{"total_server_assignment_count", "total_xact_count", "total_query_count", "total_received", "total_sent",
				"total_xact_time", "total_query_time", "total_wait_time", "total_client_parse_count", "total_server_parse_count", "total_bind_count",
				"avg_client_parse_count", "avg_server_parse_count", "avg_bind_count", "avg_server_assignment_count"},
			kinds: map[string]pgbKind{
				"total_xact_count": pgbKindCount, "total_query_count": pgbKindCount, "total_server_assignment_count": pgbKindCount,
				"total_client_parse_count": pgbKindCount, "total_server_parse_count": pgbKindCount, "total_bind_count": pgbKindCount,
				"avg_xact_count": pgbKindCount, "avg_query_count": pgbKindCount,
				"total_received": pgbKindBytes, "total_sent": pgbKindBytes, "avg_recv": pgbKindBytes, "avg_sent": pgbKindBytes,
				"total_xact_time": pgbKindUS, "total_query_time": pgbKindUS, "total_wait_time": pgbKindUS,
				"avg_xact_time": pgbKindUS, "avg_query_time": pgbKindUS, "avg_wait_time": pgbKindUS,
			},
		},
		{
			show: pgbShowClients, what: "clients", title: "clients",
			desc:    "every client connection: user, database, state, address, wait time, linked server, application",
			sortCol: "wait",
			hidden:  []string{"ptr", "link", "tls", "wait_us", "prepared_statements", "local_addr", "local_port", "close_needed", "replication", "type", "id"},
			kinds:   map[string]pgbKind{"wait": pgbKindSecs, "wait_us": pgbKindUS},
		},
		{
			show: pgbShowServers, what: "servers", title: "servers",
			desc:    "every server connection: pool, state, backend pid, address, age of the current request",
			sortCol: "wait",
			hidden:  []string{"ptr", "link", "tls", "wait_us", "prepared_statements", "local_addr", "local_port", "close_needed", "replication", "type", "id"},
			kinds:   map[string]pgbKind{"wait": pgbKindSecs, "wait_us": pgbKindUS},
		},
		{
			show: pgbShowDatabases, what: "databases", title: "databases",
			desc: "configured databases: target host/port, pool sizes, pool mode, current vs. max connections, paused/disabled",
		},
		{
			show: pgbShowUsers, what: "users", title: "users",
			desc: "known users: per-user pool sizes and connection limits",
		},
		{
			show: pgbShowConfig, what: "config", title: "config",
			desc: "effective configuration: value, default, whether it can be changed at runtime",
		},
		{
			show: pgbShowMem, what: "mem", title: "memory",
			desc:  "internal slab allocators: object size, used, free",
			kinds: map[string]pgbKind{"memtotal": pgbKindBytes},
		},
		{
			show: pgbShowSockets, what: "sockets", title: "sockets",
			desc:   "low-level socket state for every connection (clients + servers), including buffer positions",
			hidden: []string{"ptr", "link", "tls", "wait_us", "prepared_statements", "recv_pos", "pkt_pos", "pkt_remain", "send_pos", "send_remain", "pkt_avail", "send_avail"},
			kinds:  map[string]pgbKind{"wait": pgbKindSecs, "wait_us": pgbKindUS},
		},
		{
			show: pgbShowFDs, what: "fds", title: "file descriptors",
			desc: "open file descriptors (admin only): what each fd is for",
		},
	}
}

func (s pgbShow) spec() pgbShowSpec {
	for _, sp := range pgbShowRegistry() {
		if sp.show == s {
			return sp
		}
	}
	return pgbShowSpec{show: s, what: "pools", title: "pools"}
}

// pgbVisKey namespaces the per-SHOW column-visibility set inside the shared
// diagnostic-visibility machinery (diagVis/rebuildDiagItems). The prefix is
// what defaultDiagVis keys on to seed from pgbShowSpec.hidden.
func pgbVisKey(s pgbShow) string { return pgbVisPrefix + s.spec().what }

const pgbVisPrefix = "pgbouncer/"

// pgbDefaultVis is the seed visibility map for a pgbouncer SHOW: its spec's
// hidden columns start off, everything else on. nil when nothing is hidden.
func pgbDefaultVis(key string) map[string]bool {
	what := strings.TrimPrefix(key, pgbVisPrefix)
	for _, sp := range pgbShowRegistry() {
		if sp.what != what || len(sp.hidden) == 0 {
			continue
		}
		vis := make(map[string]bool, len(sp.hidden))
		for _, name := range sp.hidden {
			vis[name] = false
		}
		return vis
	}
	return nil
}

// applyPgbKinds rewrites a SHOW result in place: secret-bearing columns are
// dropped, then explicit per-column kind overrides from the spec, the generic
// rules (a "_us" suffix is microseconds, "_bytes" is bytes), and the default
// sort column. Durations are rescaled so Num is milliseconds (what DiagDuration
// sorts and colours on) and Display is pre-formatted, since the renderer prints
// duration cells verbatim.
func applyPgbKinds(res *pg.DiagResult, spec pgbShowSpec) {
	if res == nil {
		return
	}
	dropPgbSecretColumns(res)
	for ci := range res.Columns {
		col := &res.Columns[ci]
		k, ok := spec.kinds[col.Name]
		if !ok {
			switch {
			case strings.HasSuffix(col.Name, "_us"):
				k = pgbKindUS
			case strings.HasSuffix(col.Name, "_bytes"):
				k = pgbKindBytes
			default:
				continue
			}
		}
		switch k {
		case pgbKindUS, pgbKindSecs:
			col.Kind = pg.DiagDuration
		case pgbKindBytes:
			col.Kind = pg.DiagBytes
		case pgbKindCount:
			col.Kind = pg.DiagCount
		}
		for ri := range res.Rows {
			if ci >= len(res.Rows[ri]) {
				continue
			}
			cell := &res.Rows[ri][ci]
			if !cell.HasNum {
				continue
			}
			switch k {
			case pgbKindUS:
				cell.Num /= 1000
				cell.Display = fmtAge(cell.Num)
			case pgbKindSecs:
				cell.Num *= 1000
				cell.Display = fmtAge(cell.Num)
			case pgbKindBytes:
				cell.Display = humanize.Bytes(int64(cell.Num))
			}
		}
	}
	res.BarCol = -1
	res.SortCol = -1
	if spec.sortCol != "" {
		for ci, col := range res.Columns {
			if col.Name == spec.sortCol {
				res.SortCol = ci
				break
			}
		}
	}
}

// dropPgbSecretColumns removes the scram_client_key / scram_server_key columns
// pgbouncer includes in some SHOW output. Key material has no place on a
// monitoring screen or in a CSV export, so it is removed rather than hidden.
func dropPgbSecretColumns(res *pg.DiagResult) {
	keep := make([]int, 0, len(res.Columns))
	for i, c := range res.Columns {
		if !strings.HasPrefix(c.Name, "scram_") {
			keep = append(keep, i)
		}
	}
	if len(keep) == len(res.Columns) {
		return
	}
	projectPgbColumns(res, keep)
}

// projectPgbColumns keeps only the listed column indexes, in that order.
func projectPgbColumns(res *pg.DiagResult, keep []int) {
	cols := make([]pg.DiagColumn, len(keep))
	for j, i := range keep {
		cols[j] = res.Columns[i]
	}
	for ri, row := range res.Rows {
		out := make([]pg.DiagCell, len(keep))
		for j, i := range keep {
			if i < len(row) {
				out[j] = row[i]
			}
		}
		res.Rows[ri] = out
	}
	res.Columns = cols
}

// addPgbHostnames inserts a reverse-DNS "hostname" column right after "addr"
// (clients, servers, sockets), mirroring the Activity tool's hostname column.
// resolve is Client.ResolveAddr: cached per session, so only the first sight
// of an address costs a lookup. Non-IP values ("unix", empty) get "—".
func addPgbHostnames(res *pg.DiagResult, resolve func(string) (string, bool)) {
	if res == nil || resolve == nil {
		return
	}
	addr := -1
	for i, c := range res.Columns {
		if c.Name == "addr" {
			addr = i
			break
		}
	}
	if addr < 0 {
		return
	}
	cols := make([]pg.DiagColumn, 0, len(res.Columns)+1)
	cols = append(cols, res.Columns[:addr+1]...)
	cols = append(cols, pg.DiagColumn{Name: "hostname", Kind: pg.DiagText})
	cols = append(cols, res.Columns[addr+1:]...)
	for ri, row := range res.Rows {
		cell := pg.DiagCell{Display: "—"}
		if addr < len(row) {
			if ip := row[addr].Display; net.ParseIP(ip) != nil {
				if host, ok := resolve(ip); ok {
					cell.Display = host
				}
			}
		}
		out := make([]pg.DiagCell, 0, len(row)+1)
		out = append(out, row[:min(addr+1, len(row))]...)
		out = append(out, cell)
		if addr+1 < len(row) {
			out = append(out, row[addr+1:]...)
		}
		res.Rows[ri] = out
	}
	res.Columns = cols
	if res.SortCol > addr {
		res.SortCol++
	}
}

// pgbInstanceColumns is the instance list's schema; pgbInstanceItems keeps
// its cells parallel.
func pgbInstanceColumns() []pg.DiagColumn {
	return []pg.DiagColumn{
		{Name: "instance", Kind: pg.DiagText},
		{Name: "state", Kind: pg.DiagText},
		{Name: "version", Kind: pg.DiagText},
		{Name: "pid", Kind: pg.DiagInt},
		{Name: "target", Kind: pg.DiagText},
		{Name: "mode", Kind: pg.DiagText},
		{Name: "cl_active", Kind: pg.DiagInt},
		{Name: "cl_waiting", Kind: pg.DiagInt},
		{Name: "sv_active", Kind: pg.DiagInt},
		{Name: "sv_idle", Kind: pg.DiagInt},
		{Name: "maxwait", Kind: pg.DiagDuration},
		{Name: "pools", Kind: pg.DiagInt},
		{Name: "logfile", Kind: pg.DiagText},
		{Name: "via", Kind: pg.DiagText},
	}
}

// pgbInstanceState is the one-word health of an instance for the list.
func pgbInstanceState(inst pg.PgBouncerInstance, pr pg.PgBouncerProbe) string {
	switch {
	case pr.Err == nil:
		return "running"
	case pr.AuthErr:
		return "auth denied"
	case inst.PID == 0:
		return "stopped"
	}
	return "unreachable"
}

// pgbInstanceItems renders instances as generic-table rows; pgbIdx points back
// at the instance for Enter and l.
func pgbInstanceItems(insts []pg.PgBouncerInstance, probes []pg.PgBouncerProbe) []item {
	items := make([]item, 0, len(insts))
	num := func(n int) pg.DiagCell {
		return pg.DiagCell{Display: fmtCount(n), Num: float64(n), HasNum: true}
	}
	for i, inst := range insts {
		var pr pg.PgBouncerProbe
		if i < len(probes) {
			pr = probes[i]
		}
		version := pg.DiagCell{Display: "—"}
		if pr.Version != "" {
			version = pg.DiagCell{Display: strings.TrimPrefix(pr.Version, "PgBouncer ")}
		}
		pid := pg.DiagCell{Display: "—"}
		if inst.PID > 0 {
			pid = pg.DiagCell{Display: fmtCount(inst.PID), Num: float64(inst.PID), HasNum: true}
		}
		mode := pg.DiagCell{Display: inst.PoolMode}
		if mode.Display == "" {
			mode.Display = "—"
		}
		var clA, clW, svA, svI, pools, wait pg.DiagCell
		if pr.Err == nil {
			t := pr.Totals
			clA, clW, svA, svI, pools = num(t.ClActive), num(t.ClWaiting), num(t.SvActive), num(t.SvIdle), num(t.Pools)
			ms := t.MaxWaitSec * 1000
			wait = pg.DiagCell{Display: fmtAge(ms), Num: ms, HasNum: true}
			if ms == 0 {
				wait.Display = "0"
			}
		} else {
			dash := pg.DiagCell{Display: "—"}
			clA, clW, svA, svI, pools, wait = dash, dash, dash, dash, dash, dash
		}
		logfile := pg.DiagCell{Display: inst.Logfile}
		if logfile.Display == "" {
			logfile.Display = "—"
		}
		cells := []pg.DiagCell{
			{Display: inst.Name}, {Display: pgbInstanceState(inst, pr)}, version, pid,
			{Display: inst.TargetLabel()}, mode, clA, clW, svA, svI, wait, pools, logfile, {Display: inst.Reason},
		}
		parts := make([]string, len(cells))
		for j, cell := range cells {
			parts[j] = cell.Display
		}
		items = append(items, item{name: strings.Join(parts, " "), hasChildren: true, data: cells, pgbIdx: i + 1})
	}
	return items
}

// pgbLogRow marks the "open log" row of the overview menu.
type pgbLogRow struct{}

// pgbMenuItems builds the overview menu: one row per SHOW plus the logfile.
func pgbMenuItems(inst pg.PgBouncerInstance) []item {
	reg := pgbShowRegistry()
	items := make([]item, 0, len(reg)+1)
	for _, sp := range reg {
		items = append(items, item{name: sp.title, detail: sp.desc, hasChildren: true, data: sp.show})
	}
	if inst.Logfile != "" {
		items = append(items, item{name: "log", detail: "open " + inst.Logfile + " in the log analyzer", hasChildren: true, data: pgbLogRow{}})
	}
	return items
}
