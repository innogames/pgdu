package tui

import (
	"fmt"
	"sort"
	"strings"

	"pgdu/internal/pgbouncer"
)

// renderPgBouncers is the instance list's empty state; with instances the
// generic table renderer draws the (sortable) list and renderPgbListHint the
// line above it.
func (m *Model) renderPgBouncers(_ *screen, height int) string {
	var b strings.Builder
	mu := styleMuted.Render
	b.WriteString("  " + mu("no pgbouncer instance found") + "\n\n")
	b.WriteString("  " + mu("Looked for running pgbouncer processes in /proc, for /etc/pgbouncer/*.ini, and checked whether pgdu's own") + "\n")
	b.WriteString("  " + mu("connection goes through a pooler. Pass --pgbouncer-target INI|SOCKETDIR|HOST[:PORT] (repeatable, or") + "\n")
	b.WriteString("  " + mu("PGDU_PGBOUNCER_TARGET comma-separated) to add an instance by hand, e.g. --pgbouncer-target /var/run/pgbouncer_1.") + "\n")
	return padInfo(&b, height)
}

// renderPgbListHint is the one-line status of the highlighted instance above
// the list: where it came from, and — the part that matters when the console
// refused us — exactly what to change.
func (m *Model) renderPgbListHint(s *screen) string {
	inst := m.pgbSelectedInstance(s)
	if inst == nil {
		return ""
	}
	mu := styleMuted.Render
	var pr pgbouncer.Probe
	if it := s.items[s.visibleIndexes()[s.cursor]]; it.pgbIdx > 0 && it.pgbIdx <= len(s.pgb.probes) {
		pr = s.pgb.probes[it.pgbIdx-1]
	}
	var parts []string
	if inst.IniPath != "" {
		parts = append(parts, mu("ini "+inst.IniPath))
	}
	if inst.IniErr != nil {
		parts = append(parts, styleErr.Render("ini unreadable: "+inst.IniErr.Error()))
	}
	switch {
	case pr.AuthErr:
		parts = append(parts, styleErr.Render("login refused: ")+mu(pgbouncer.AuthHint(*inst, pr.User)))
	case pr.Err != nil:
		parts = append(parts, styleErr.Render(oneLineErr(pr.Err)))
	default:
		parts = append(parts, mu("↵ browse  l → log analyzer"))
	}
	return "  " + strings.Join(parts, mu("  ·  "))
}

// renderPgBouncerHeader is the overview's summary block above the SHOW menu.
func (m *Model) renderPgBouncerHeader(s *screen) string {
	mu := styleMuted.Render
	var b strings.Builder
	inst := s.pgb.inst
	if inst == nil {
		return ""
	}
	label := func(k string) string { return "  " + padRight(mu(k), 14) }
	b.WriteString(label("target") + inst.TargetLabel())
	if inst.PID > 0 {
		b.WriteString(mu(fmt.Sprintf("  pid %d", inst.PID)))
	}
	if inst.IniPath != "" {
		b.WriteString(mu("  " + inst.IniPath))
	}
	b.WriteString("\n")
	if s.pgb.err != nil {
		b.WriteString(label("error") + styleErr.Render(oneLineErr(s.pgb.err)) + "\n")
		if pgbouncer.AuthHintApplies(s.pgb.err) {
			b.WriteString(label("") + mu(pgbouncer.AuthHint(*inst, m.client.PgBouncer.User())) + "\n")
		}
		return b.String()
	}
	ov := s.pgb.overview
	if ov == nil {
		return b.String()
	}
	ver := ov.Version
	if i := strings.Index(ver, " on "); i > 0 {
		ver = ver[:i]
	}
	b.WriteString(label("version") + ver)
	if inst.PoolMode != "" {
		b.WriteString(mu("  pool_mode ") + inst.PoolMode)
	}
	if st, ok := ov.State["active"]; ok {
		if st == "yes" {
			b.WriteString(mu("  active"))
		} else {
			b.WriteString("  " + styleErr.Render("not active"))
		}
	}
	if st, ok := ov.State["paused"]; ok && st == "yes" {
		b.WriteString("  " + styleErr.Render("PAUSED"))
	}
	if st, ok := ov.State["suspended"]; ok && st == "yes" {
		b.WriteString("  " + styleErr.Render("SUSPENDED"))
	}
	b.WriteString("\n")
	b.WriteString(label("pools") + pgbTotalsLine(ov.Totals) + "\n")
	if len(ov.Lists) > 0 {
		keys := []string{"databases", "users", "pools", "free_clients", "used_clients", "login_clients", "free_servers", "used_servers", "dns_names", "dns_zones", "dns_queries"}
		var parts []string
		seen := map[string]bool{}
		for _, k := range keys {
			if v, ok := ov.Lists[k]; ok {
				parts = append(parts, mu(k+" ")+fmtCount(int(v)))
				seen[k] = true
			}
		}
		var rest []string
		for k := range ov.Lists {
			if !seen[k] {
				rest = append(rest, k)
			}
		}
		sort.Strings(rest)
		for _, k := range rest {
			parts = append(parts, mu(k+" ")+fmtCount(int(ov.Lists[k])))
		}
		b.WriteString(label("lists") + strings.Join(parts, "  ") + "\n")
	}
	b.WriteString(label("refresh") + pgbRefreshLabel(m) + mu("  t cycles · l log · ? help") + "\n")
	return b.String()
}

// oneLineErr flattens a multi-line error (pgx lists every dial attempt on its
// own indented line) so it fits a header row.
func oneLineErr(err error) string {
	var parts []string
	for ln := range strings.SplitSeq(err.Error(), "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			parts = append(parts, ln)
		}
	}
	return strings.Join(parts, " · ")
}

// pgbTotalsLine renders pool totals with a red longest wait when clients are
// queuing — the one number that says the pool is undersized or its servers
// are stuck.
func pgbTotalsLine(t pgbouncer.PoolTotals) string {
	mu := styleMuted.Render
	waiting := fmt.Sprintf("%d waiting", t.ClWaiting)
	wait := mu("max wait 0")
	if t.ClWaiting > 0 || t.MaxWaitSec > 0 {
		waiting = styleErr.Render(waiting)
		wait = styleErr.Render("max wait " + fmtSecsDuration(t.MaxWaitSec))
	}
	return fmt.Sprintf("%s  cl %d active  %s  sv %d active  %d idle  %s",
		mu(fmt.Sprintf("%d pools", t.Pools)), t.ClActive, waiting, t.SvActive, t.SvIdle, wait)
}

func pgbRefreshLabel(m *Model) string {
	if m.pgbRefresh <= 0 {
		return "off"
	}
	return m.pgbRefresh.String()
}

// renderPgbShowHeader is the one-line banner above a SHOW table.
func (m *Model) renderPgbShowHeader(s *screen) string {
	mu := styleMuted.Render
	if s.pgb.inst == nil {
		return ""
	}
	spec := s.pgb.show.spec()
	line := "  " + s.pgb.inst.Name + mu(" · SHOW "+strings.ToUpper(spec.what))
	if s.pgb.err != nil {
		return line + "  " + styleErr.Render(oneLineErr(s.pgb.err))
	}
	if s.diagResult != nil {
		line += mu(fmt.Sprintf(" · %d rows", len(s.diagResult.Rows)))
	}
	line += mu(" · refresh "+pgbRefreshLabel(m)) + mu("  t cycles · C columns · e csv")
	if parent := m.findLevel(levelPgBouncer); parent != nil && parent.pgb.overview != nil && parent.pgb.inst != nil &&
		parent.pgb.inst.Key() == s.pgb.inst.Key() {
		line += "\n  " + pgbTotalsLine(parent.pgb.overview.Totals)
	}
	return line
}

// renderPgBouncerMenu draws the overview's SHOW menu (tool-picker style).
func (m *Model) renderPgBouncerMenu(s *screen, height int) string {
	if s.pgb.err != nil && len(s.items) == 0 {
		var b strings.Builder
		for range height {
			b.WriteString("\n")
		}
		return b.String()
	}
	return m.renderToolPicker(s, height)
}

// renderPgBouncerInfo is the ? reference for every pgbouncer level.
func (m *Model) renderPgBouncerInfo(height int) string {
	var b strings.Builder
	mu := styleMuted.Render
	infoHeader(&b, "pgbouncer")

	b.WriteString("  " + styleHeader.Render(" where instances come from ") + "\n")
	b.WriteString("    " + mu("Running pgbouncer processes in /proc (the ini on their command line is parsed), /etc/pgbouncer/*.ini,") + "\n")
	b.WriteString("    " + mu("pgdu's own connection when it evidently ends at a pooler, and anything given with --pgbouncer-target.") + "\n")
	b.WriteString("    " + mu("The same instance seen twice is merged; 'via' says which source found it first.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" why the unix socket, not TCP ") + "\n")
	b.WriteString("    " + mu("Several instances on one host usually share one TCP port through so_reuseport: the kernel hands each new") + "\n")
	b.WriteString("    " + mu("TCP connection to *any* of them, so a TCP console connection reads a random instance. Each instance's") + "\n")
	b.WriteString("    " + mu("unix_socket_dir is its own, so the tool connects there whenever the socket exists and only falls back to") + "\n")
	b.WriteString("    " + mu("listen_addr (127.0.0.1 for '*') otherwise — a TCP target is therefore marked and may not be the one you mean.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" logging in ") + "\n")
	b.WriteString("    " + mu("The console is the virtual database \"pgbouncer\". Only admin_users and stats_users may log in; stats_users") + "\n")
	b.WriteString("    " + mu("are read-only and enough for everything here. The login is -U (or --pgbouncer-user) and must have a password") + "\n")
	b.WriteString("    " + mu("in auth_file: PGDU_PGBOUNCER_PASSWORD, PGPASSWORD, or ~/.pgpass keyed by the socket directory (as libpq does)") + "\n")
	b.WriteString("    " + mu("or by localhost. 'auth denied' rows show the exact ini change. One connection per instance is kept open:") + "\n")
	b.WriteString("    " + mu("pgbouncer logs every console login/logout, and console connections hold no server slot.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" pools ") + "\n")
	b.WriteString("    " + mu("cl_active   clients linked to a server (or idle in session mode)   cl_waiting  clients queued for a server") + "\n")
	b.WriteString("    " + mu("sv_active   servers linked to a client        sv_idle  pooled, ready       sv_used  idle > server_check_delay") + "\n")
	b.WriteString("    " + mu("sv_login    servers still connecting          maxwait  longest queue time of the oldest waiting client") + "\n")
	b.WriteString("    " + mu("Brief queueing is how transaction pooling works; a wait of a second means the pool is too small, the servers") + "\n")
	b.WriteString("    " + mu("are stuck, or max_db_connections/max_user_connections cap it. Triage flags waits ≥ 1s (warn) / 10s (crit).") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" stats ") + "\n")
	b.WriteString("    " + mu("avg_* columns are per-second rates and mean durations over pgbouncer's stats_period (default 60s), computed") + "\n")
	b.WriteString("    " + mu("by pgbouncer itself; total_* are cumulative since start and hidden by default (C shows them). avg_wait_time") + "\n")
	b.WriteString("    " + mu("is the mean time a client spent waiting for a server — the pooler's own contribution to latency.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" clients / servers ") + "\n")
	b.WriteString("    " + mu("state: active (linked), waiting (queued), idle (session mode, unlinked), used/tested/login (servers).") + "\n")
	b.WriteString("    " + mu("wait is how long the current request has been in that state; link points at the peer socket; remote_pid") + "\n")
	b.WriteString("    " + mu("on a server row is the Postgres backend pid — the same pid the Activity tool shows. hostname is the") + "\n")
	b.WriteString("    " + mu("reverse-DNS name of addr (cached per session, like the Activity tool's); scram key columns are never shown.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" keys ") + "\n")
	b.WriteString("    " + mu("↵ open the highlighted instance / SHOW table   l → log analyzer (the instance's logfile)   t refresh cadence   C columns (SHOW tables)") + "\n")
	b.WriteString("    " + mu("←/→ sort   r reverse   / filter   e export csv   space reload   ? this help") + "\n")
	b.WriteString("    " + mu("The tool is read-only: it never issues RELOAD, PAUSE, RESUME or KILL.") + "\n")
	return padInfo(&b, height)
}
