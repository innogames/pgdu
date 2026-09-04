package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"pgdu/internal/humanize"
	"pgdu/internal/pg"
	"pgdu/internal/pglog"
)

// Column widths of the groups pane (shared with barReserve in layout.go).
const (
	logCountColW = 6  // "12.3k"
	logSevColW   = 7  // "WARNING"
	logSpanColW  = 13 // "00:15→07:18" or "09-01→09-02"
)

// logSevStyle colours a severity tag: errors and worse red, warnings yellow,
// LOG muted, the debug family dimmer still.
func logSevStyle(sev pglog.Severity) lipgloss.Style {
	switch {
	case sev >= pglog.SevError:
		return lipgloss.NewStyle().Foreground(colorError)
	case sev == pglog.SevWarning:
		return lipgloss.NewStyle().Foreground(colorAccent)
	case sev == pglog.SevLog:
		return lipgloss.NewStyle().Foreground(colorMuted)
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
}

// logSevStyleByName is the timeline-cell variant (the generic renderer only has
// the display text).
func logSevStyleByName(name string) (lipgloss.Style, bool) {
	switch name {
	case "ERROR", "FATAL", "PANIC":
		return lipgloss.NewStyle().Foreground(colorError), true
	case "WARNING":
		return lipgloss.NewStyle().Foreground(colorAccent), true
	case "LOG":
		return lipgloss.NewStyle().Foreground(colorMuted), true
	case "NOTICE", "INFO", "DEBUG", "NOISE":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("240")), true
	}
	return lipgloss.Style{}, false
}

func logGlyph(sev pglog.Severity) string {
	switch {
	case sev >= pglog.SevError:
		return logSevStyle(sev).Render("✗")
	case sev == pglog.SevWarning:
		return logSevStyle(sev).Render("▲")
	}
	return logSevStyle(sev).Render("●")
}

func logCatStyle(c pglog.Category) lipgloss.Style {
	switch c {
	case pglog.CatError:
		return lipgloss.NewStyle().Foreground(colorError)
	case pglog.CatWarning, pglog.CatLock:
		return lipgloss.NewStyle().Foreground(colorAccent)
	case pglog.CatTempFile:
		return lipgloss.NewStyle().Foreground(colorBloat)
	case pglog.CatSlowQuery, pglog.CatStatement:
		return lipgloss.NewStyle().Foreground(colorBar)
	}
	return lipgloss.NewStyle().Foreground(colorMuted)
}

// fmtCount renders an entry count compactly ("12.3k") for the count column.
func fmtCount(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 10_000:
		return fmt.Sprintf("%.0fk", float64(n)/1e3)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return strconv.Itoa(n)
}

// fmtSpan renders a first→last pair: clock times inside one day, dates beyond.
func fmtSpan(first, last time.Time) string {
	if first.IsZero() {
		return "—"
	}
	if last.Sub(first) > 24*time.Hour || first.YearDay() != last.YearDay() {
		return first.Format("01-02") + "→" + last.Format("01-02")
	}
	if first.Equal(last) {
		return first.Format("15:04:05")
	}
	return first.Format("15:04") + "→" + last.Format("15:04")
}

// renderLogHeader is the block above every log level: source + window +
// prefix, the severity counts and view badges, and one sparkline per severity
// class. On the group/entry levels a group summary line takes the badges' place.
func (m *Model) renderLogHeader(s *screen) string {
	mu := styleMuted.Render
	logs := m.findLevel(levelLogs)
	if logs == nil {
		return ""
	}
	r := logs.log.report
	var b strings.Builder
	if r == nil {
		if logs.log.err != nil && logs.log.src != nil {
			b.WriteString("  " + mu(logSourceLabel(logs.log.src.Info())) + "\n")
		}
		return strings.TrimRight(b.String(), "\n")
	}

	// Line 1: file · kind · window · covered range · prefix.
	l1 := []string{logSourceLabel(r.Source), logWindowLabel(r.Window)}
	if !r.Window.From.IsZero() {
		span := r.Window.To.Sub(r.Window.From)
		l1 = append(l1, fmt.Sprintf("%s → %s (%s)", r.Window.From.Format("01-02 15:04:05"), r.Window.To.Format("15:04:05"), fmtDuration(span)))
	}
	prefix := "prefix " + r.Prefix
	if r.Format != pglog.FormatStderr {
		prefix = r.Format.String()
	}
	if r.PrefixDetected {
		prefix += " " + styleBadge.Render("detected")
	}
	l1 = append(l1, prefix)
	b.WriteString("  " + mu(strings.Join(l1, "  ·  ")) + "\n")

	// Line 2: counts + badges.
	count := func(n int, name string, st lipgloss.Style) string {
		if n == 0 {
			return mu(fmt.Sprintf("%d %s", n, name))
		}
		return st.Render(fmt.Sprintf("%d %s", n, name))
	}
	total := len(r.Entries)
	errs := r.BySeverity[pglog.SevError]
	fatal := r.BySeverity[pglog.SevFatal] + r.BySeverity[pglog.SevPanic]
	parts := []string{
		fmt.Sprintf("%d entries", total),
		count(errs, "errors", logSevStyle(pglog.SevError)),
	}
	if fatal > 0 {
		parts = append(parts, count(fatal, "fatal", logSevStyle(pglog.SevFatal)))
	}
	parts = append(parts, count(r.BySeverity[pglog.SevWarning], "warnings", logSevStyle(pglog.SevWarning)))
	if r.Format == pglog.FormatPgBouncer {
		// A pooler log has no locks/temp files/checkpoints; its volume is
		// connection churn.
		parts = append(parts, mu(fmt.Sprintf("%d conn", r.ByCategory[pglog.CatConnection])))
	} else {
		parts = append(parts,
			count(r.ByCategory[pglog.CatLock], "locks", logCatStyle(pglog.CatLock)),
			count(r.ByCategory[pglog.CatTempFile], "temp files", logCatStyle(pglog.CatTempFile)),
			count(r.ByCategory[pglog.CatSlowQuery], "slow", logCatStyle(pglog.CatSlowQuery)),
			mu(fmt.Sprintf("%d ckpt", r.ByCategory[pglog.CatCheckpoint])),
		)
	}
	if n := r.ByCategory[pglog.CatStatement]; n > 0 {
		parts = append(parts, count(n, "stmts", logCatStyle(pglog.CatStatement)))
	}
	if n := r.ByCategory[pglog.CatAutovacuum]; n > 0 {
		parts = append(parts, mu(fmt.Sprintf("%d autovac", n)))
	}
	if n := r.ByCategory[pglog.CatConnection]; n > 0 && r.Format != pglog.FormatPgBouncer {
		parts = append(parts, mu(fmt.Sprintf("%d conn", n)))
	}
	if r.Unparsed > 0 {
		parts = append(parts, styleErr.Render(fmt.Sprintf("%d unparsed lines", r.Unparsed)))
	}
	line2 := "  " + strings.Join(parts, mu("  ·  "))

	var badges []string
	if s.level == levelLogs {
		if logs.log.view.table() {
			badges = append(badges, styleBadge.Render(logs.log.view.label()))
		} else {
			badges = append(badges, styleBadge.Render(logs.log.groupBy.label()))
		}
	}
	if m.logRefresh > 0 {
		badges = append(badges, styleSelected.Render(fmt.Sprintf("⟳ %s", m.logRefresh)))
	} else {
		badges = append(badges, mu("⟳ off"))
	}
	b.WriteString(truncateToWidth(line2+"   "+strings.Join(badges, " "), m.width) + "\n")

	// Line 3+: sparklines (skipped when there is no time axis or the terminal
	// is too narrow to be legible).
	if len(r.Hist.Counts) > 1 && m.width >= 40 {
		w := min(len(r.Hist.Counts), m.width-24)
		row := func(label string, series []int, st lipgloss.Style) {
			vals := make([]float64, len(series))
			nonzero := false
			for i, n := range series {
				vals[i] = float64(n)
				if n > 0 {
					nonzero = true
				}
			}
			if !nonzero {
				return
			}
			b.WriteString("  " + mu(padRight(label, 8)) + st.Render(sparkline(vals, w, 0)) + "\n")
		}
		labelW := 8
		if s.level == levelLogs && logs.log.view == logViewStats && len(r.Pooler.Counts) == len(r.Hist.Counts) {
			// The stats pane swaps the severity rows for the pooler metrics on
			// the same axis: throughput in the bar colour, latencies in the
			// accent so the two families read apart. The peak sits at the end.
			labelW = 10
			for _, met := range pglog.PoolerMetrics {
				vals := r.Pooler.Series(met)
				peak := 0.0
				for _, v := range vals {
					peak = max(peak, v)
				}
				if peak == 0 {
					continue
				}
				st := lipgloss.NewStyle().Foreground(colorBar)
				if met >= pglog.PoolerXactUs {
					st = lipgloss.NewStyle().Foreground(colorAccent)
				}
				b.WriteString("  " + mu(padRight(met.Label(), labelW)) + st.Render(sparkline(vals, w, 0)) + mu("  peak "+fmtPoolerMetric(met, peak)) + "\n")
			}
		} else {
			errSeries := r.Hist.Series(pglog.SevError)
			for i, n := range r.Hist.Series(pglog.SevFatal) {
				errSeries[i] += n
			}
			for i, n := range r.Hist.Series(pglog.SevPanic) {
				errSeries[i] += n
			}
			row("errors", errSeries, logSevStyle(pglog.SevError))
			row("warnings", r.Hist.Series(pglog.SevWarning), logSevStyle(pglog.SevWarning))
			row("all", r.Hist.Total(), lipgloss.NewStyle().Foreground(colorBar))
		}
		b.WriteString("  " + mu(fmt.Sprintf("%-*s%s per cell, %s → %s", labelW, "", r.Hist.Bucket, r.Hist.Start.Format("15:04"), r.Window.To.Format("15:04"))) + "\n")
	}

	if s.level == levelLogGroup && s.log.group != nil {
		b.WriteString(m.renderLogGroupSummary(s.log.group) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// fmtPoolerMetric renders a pooler metric the way its table column does.
func fmtPoolerMetric(m pglog.PoolerMetric, v float64) string {
	switch m {
	case pglog.PoolerIn, pglog.PoolerOut:
		return humanize.Bytes(int64(v)) + "/s"
	case pglog.PoolerXactUs, pglog.PoolerQueryUs, pglog.PoolerWaitUs:
		return fmtMicros(int64(v))
	}
	return strconv.FormatInt(int64(v), 10)
}

// renderLogGroupSummary is the one-line stats strip for a group.
func (m *Model) renderLogGroupSummary(g *pglog.Group) string {
	mu := styleMuted.Render
	parts := []string{
		logGlyph(g.Severity) + " " + logSevStyle(g.Severity).Render(g.Severity.String()),
		logCatStyle(g.Category).Render(g.Category.Label()),
		fmt.Sprintf("%d entries", g.Count),
		"first " + g.First.Format("01-02 15:04:05"),
		"last " + g.Last.Format("01-02 15:04:05"),
	}
	parts = append(parts, logGroupStats(g)...)
	if g.Plans > 0 {
		parts = append(parts, logPlanBadge(g.Plans)+mu(" — ▤ rows carry one; Enter shows it"))
	}
	if len(g.Samples) < g.Count {
		parts = append(parts, mu(fmt.Sprintf("showing the last %d", len(g.Samples))))
	}
	return "  " + strings.Join(parts, mu("  ·  "))
}

// logGroupStats returns the category-specific figures for a group.
func logGroupStats(g *pglog.Group) []string {
	var out []string
	switch {
	case g.Slow != nil:
		out = append(out,
			"avg "+durationStyle(g.Slow.AvgMs(g.Count)).Render(fmtAge(g.Slow.AvgMs(g.Count))),
			"p95 "+durationStyle(g.Slow.P95Ms).Render(fmtAge(g.Slow.P95Ms)),
			"max "+durationStyle(g.Slow.MaxMs).Render(fmtAge(g.Slow.MaxMs)),
			"total "+fmtAge(g.Slow.SumMs))
	case g.Checkpoint != nil && g.Checkpoint.Complete > 0:
		c := g.Checkpoint
		n := float64(c.Complete)
		out = append(out,
			fmt.Sprintf("avg write %.0fs", c.SumWrite/n),
			fmt.Sprintf("avg total %.0fs", c.SumTotal/n),
			fmt.Sprintf("max %.0fs", c.MaxTotal),
			fmt.Sprintf("avg %s buffers", fmtCount(int(float64(c.SumBuffers)/n))))
	case g.Temp != nil:
		out = append(out, "total "+humanize.Bytes(g.Temp.TotalBytes), "max "+humanize.Bytes(g.Temp.MaxBytes))
	case g.Autovac != nil:
		tables := sortedAutovacTables(g.Autovac)
		top := strings.Join(tables[:min(3, len(tables))], ", ")
		out = append(out, fmt.Sprintf("%d table(s): %s", len(tables), top))
	}
	return out
}

// renderLogGroups is the default pane: section headers with the aggregated
// rows beneath, each with a count bar scaled to its section's top group.
func (m *Model) renderLogGroups(s *screen, height int) string {
	var b strings.Builder
	mu := styleMuted.Render
	if s.log.err != nil {
		b.WriteString(styleErr.Render("  error: "+s.log.err.Error()) + "\n")
		b.WriteString("  " + mu("space reloads · esc picks another file · ? explains the privileges server-side reads need") + "\n")
		return padInfo(&b, height)
	}
	if s.log.report == nil {
		for range height {
			b.WriteString("\n")
		}
		return b.String()
	}
	vis := s.visibleIndexes()
	if len(vis) == 0 {
		msg := "(nothing to show"
		if s.filter != "" {
			msg += " — esc clears the filter"
		}
		b.WriteString("  " + mu(msg+")") + "\n")
		for i := 1; i < height; i++ {
			b.WriteString("\n")
		}
		return b.String()
	}

	// Section maxima drive the per-row bars: the biggest group in each section
	// fills the bar, so a 13-entry error is as visible as a 1000-entry slow query.
	sectionMax := map[int]int{}
	sec := -1
	for _, idx := range vis {
		it := s.items[idx]
		if _, ok := it.data.(logSection); ok {
			sec = idx
			continue
		}
		if g, ok := it.data.(*pglog.Group); ok && g.Count > sectionMax[sec] {
			sectionMax[sec] = g.Count
		}
	}

	barW := m.barWidth(s)
	b.WriteString(renderLogGroupsHeader(s.sort, s.sortDesc, barW) + "\n")
	rowsH := max(height-1, 0)
	if rowsH > 0 {
		s.offset, _ = viewportRange(s.cursor, s.offset, rowsH, len(vis))
	}
	end := min(s.offset+rowsH, len(vis))
	// The section a row belongs to is the last header at or before it.
	secOf := func(vi int) int {
		for j := vi; j >= 0; j-- {
			if _, ok := s.items[vis[j]].data.(logSection); ok {
				return vis[j]
			}
		}
		return -1
	}
	for vi := s.offset; vi < end; vi++ {
		it := s.items[vis[vi]]
		selected := vi == s.cursor
		if hdr, ok := it.data.(logSection); ok {
			label := fmt.Sprintf(" %s  %d group(s) · %s entries ", hdr.title, hdr.groups, fmtCount(hdr.entries))
			rule := strings.Repeat("─", max(m.width-displayWidth(label)-4, 0))
			b.WriteString(truncateToWidth("  "+styleSelected.Render(hdr.title)+mu(label[len(hdr.title)+1:])+mu(rule), m.width) + "\n")
			continue
		}
		g, ok := it.data.(*pglog.Group)
		if !ok {
			b.WriteString("\n")
			continue
		}
		cursor := "  "
		if selected {
			cursor = styleSelected.Render("▶ ")
		}
		mx := sectionMax[secOf(vi)]
		cells := 0
		if mx > 0 {
			cells = max(g.Count*barW/mx, 1)
		}
		bar := paintBar(barW, barSegment{cells: cells, style: logCatStyle(g.Category)})
		count := padLeft(fmtCount(g.Count), logCountColW)
		sev := padRight(g.Severity.String(), logSevColW)
		span := padRight(fmtSpan(g.First, g.Last), logSpanColW)
		title := g.Title
		if g.Plans > 0 {
			title = logPlanBadge(g.Plans) + " " + title
		}
		stats := strings.Join(logGroupStats(g), mu(" · "))
		if selected {
			count = styleSelected.Render(count)
			sev = styleSelected.Render(sev)
			title = styleSelected.Render(title)
		} else {
			sev = logSevStyle(g.Severity).Render(sev)
			span = mu(span)
		}
		line := cursor + bar + " " + count + "  " + sev + "  " + span + "  " + title
		if stats != "" {
			line += "  " + mu("[") + stats + mu("]")
		}
		b.WriteString(truncateToWidth(line, m.width) + "\n")
	}
	for i := end - s.offset; i < rowsH; i++ {
		b.WriteString("\n")
	}
	return b.String()
}

// renderLogGroupsHeader labels the groups pane's columns; the layout mirrors
// the row assembly below (cursor, bar, count, severity, span, message).
func renderLogGroupsHeader(sort sortMode, sortDesc bool, barW int) string {
	// Cursor (2) + bracketed bar (barW+2) + one space, as in the rows.
	line := strings.Repeat(" ", 2+barW+2+1) +
		padLeft(sortMark("count", sort == sortByCount, sortDesc), logCountColW) + "  " +
		padRight("level", logSevColW) + "  " +
		padRight(sortMark("seen", sort == sortByLast, sortDesc), logSpanColW) + "  " +
		sortMark("message", sort == sortByName, sortDesc) + "  [stats]"
	return styleMuted.Render(line)
}

// logPlanBadge marks slow-query rows that carry an auto_explain plan; n > 1
// shows how many of the group's entries have one.
func logPlanBadge(n int) string {
	if n > 1 {
		return styleBadge.Render(fmt.Sprintf("▤ plan ×%d", n))
	}
	return styleBadge.Render("▤ plan")
}

// renderLogFiles is the picker's empty state; with candidates the generic
// table renderer draws the (sortable) list.
func (m *Model) renderLogFiles(_ *screen, height int) string {
	var b strings.Builder
	mu := styleMuted.Render
	b.WriteString("  " + mu("no readable log found") + "\n\n")
	b.WriteString("  " + mu("pg_current_logfile() is empty (logging_collector off) and nothing matched /var/log/postgresql/postgresql-*.log*.") + "\n")
	b.WriteString("  " + mu("Pass --log-file PATH (or PGDU_LOG_FILE) to analyze a specific file; reading the server's log directory") + "\n")
	b.WriteString("  " + mu("remotely needs pg_read_server_files (pg_ls_logdir needs pg_monitor and logging_collector = on).") + "\n")
	return padInfo(&b, height)
}

// renderLogGroup lists the sampled entries behind one group, newest first.
func (m *Model) renderLogGroup(s *screen, height int) string {
	var b strings.Builder
	mu := styleMuted.Render
	vis := s.visibleIndexes()
	if len(vis) == 0 {
		b.WriteString("  " + mu("(no entries)") + "\n")
		return padInfo(&b, height)
	}
	rowsH := height
	if rowsH > 0 {
		s.offset, _ = viewportRange(s.cursor, s.offset, rowsH, len(vis))
	}
	end := min(s.offset+rowsH, len(vis))
	for vi := s.offset; vi < end; vi++ {
		it := s.items[vis[vi]]
		e := s.logEntryOf(it)
		if e == nil {
			b.WriteString("\n")
			continue
		}
		selected := vi == s.cursor
		cursor := "  "
		if selected {
			cursor = styleSelected.Render("▶ ")
		}
		ts := "—"
		if !e.Time.IsZero() {
			ts = e.Time.Format("01-02 15:04:05")
		}
		who := ""
		if e.PID != 0 {
			who = fmt.Sprintf("pid %d", e.PID)
		}
		if len(e.User) > 0 {
			who += " " + string(e.User)
			if len(e.DB) > 0 {
				who += "@" + string(e.DB)
			} else if len(e.Host) > 0 {
				who += "@" + string(e.Host)
			}
		}
		dur := ""
		if e.Category == pglog.CatSlowQuery {
			dur = durationStyle(e.DurationMs).Render(padLeft(fmtAge(e.DurationMs), 7)) + "  "
		}
		if len(e.Plan) > 0 {
			dur += styleBadge.Render("▤") + " "
		}
		msg := it.name
		if len(e.SQL) > 0 {
			msg = collapseWS(string(e.SQL), 300)
		}
		extra := ""
		if len(e.Detail) > 0 {
			extra = "  " + mu("DETAIL: "+collapseWS(string(e.Detail), 120))
		}
		if selected {
			msg = styleSelected.Render(msg)
		}
		line := cursor + mu(ts) + "  " + padRight(who, 28) + "  " + dur + msg + extra
		b.WriteString(truncateToWidth(line, m.width) + "\n")
	}
	for i := end - s.offset; i < rowsH; i++ {
		b.WriteString("\n")
	}
	return b.String()
}

// renderLogEntry shows one full record: prefix fields, then each text field
// with SQL highlighted. Scrolls through s.offset like the statement detail.
func (m *Model) renderLogEntry(s *screen, height int) string {
	e := s.log.entry
	if e == nil {
		return strings.Repeat("\n", max(height, 0))
	}
	mu := styleMuted.Render
	var b strings.Builder
	width := max(m.width-4, 20)

	field := func(k, v string) {
		if v != "" {
			b.WriteString("  " + mu(padRight(k, 10)) + v + "\n")
		}
	}
	b.WriteString("\n")
	ts := ""
	if !e.Time.IsZero() {
		ts = e.Time.Format("2006-01-02 15:04:05 MST")
	}
	field("time", ts)
	field("severity", logSevStyle(e.Severity).Render(e.Severity.String())+mu("  ·  "+e.Category.Label()))
	if e.PID != 0 {
		pid := strconv.Itoa(int(e.PID))
		if e.Line > 0 {
			pid += fmt.Sprintf("  (session line %d)", e.Line)
		}
		field("pid", pid)
	}
	field("session", string(e.Session))
	field("user", string(e.User))
	field("database", string(e.DB))
	field("host", string(e.Host))
	field("app", string(e.App))
	field("sqlstate", string(e.SQLState))
	switch {
	case e.Category == pglog.CatSlowQuery:
		field("duration", durationStyle(e.DurationMs).Render(fmtAge(e.DurationMs)))
	case e.LockWaitMs > 0:
		field("waited", fmtAge(e.LockWaitMs))
	case e.TempBytes > 0:
		field("temp file", humanize.Bytes(e.TempBytes))
	case e.Checkpoint != nil && !e.Checkpoint.Starting:
		c := e.Checkpoint
		field("checkpoint", fmt.Sprintf("%s buffers (%.1f%%) · write %.1fs sync %.2fs total %.1fs · WAL +%d −%d ↻%d · distance %s",
			fmtCount(int(c.Buffers)), c.BuffersPct, c.WriteSec, c.SyncSec, c.TotalSec, c.WALAdded, c.WALRemoved, c.WALRecycle, humanize.Bytes(c.DistanceKB*1024)))
	}
	if sql := e.SQL; len(sql) > 0 || len(e.Statement) > 0 {
		if len(sql) == 0 {
			sql = e.Statement
		}
		if tbl := pg.MainTable(string(sql)); tbl != "" {
			field("table", tbl+mu("  ·  d describes it"))
		} else if idx := pg.MainIndex(string(sql)); idx != "" {
			field("index", idx+mu("  ·  d describes it"))
		}
	}
	if e.Orphan {
		field("note", styleErr.Render("primary line lies before the window start — only its attachments are visible"))
	}

	section := func(title string, body []byte, sql bool) {
		if len(body) == 0 {
			return
		}
		b.WriteString("\n  " + styleHeader.Render(" "+title+" ") + "\n")
		text := strings.TrimLeft(string(body), "\n")
		if sql {
			for _, l := range highlightSQL(text, width) {
				b.WriteString("  " + l + "\n")
			}
			return
		}
		for _, l := range wrapPlain(dedent(text), width) {
			b.WriteString("  " + l + "\n")
		}
	}
	// planSection is the PLAN body with the same heat grading as the top-queries
	// EXPLAIN pane: each source line is wrapped plain first (wrapPlain is
	// rune-based, so no ANSI may enter it) and only then painted per segment —
	// paintExplainLine is a no-op on segments without the metric block.
	planSection := func(body []byte) {
		if len(body) == 0 {
			return
		}
		b.WriteString("\n  " + styleHeader.Render(" PLAN (auto_explain) ") + "\n")
		plan := dedent(strings.TrimLeft(string(body), "\n"))
		analyze := explainHasTiming(plan)
		decided := explainDecisions(plan, analyze)
		for i, line := range strings.Split(plan, "\n") {
			d, ok := decided[i]
			for _, seg := range wrapPlain(line, width) {
				if ok {
					seg = paintExplainLine(seg, d.style, d.boldName, analyze, d.selfPct)
				}
				b.WriteString("  " + seg + "\n")
			}
		}
	}
	if len(e.SQL) > 0 {
		// Keep only the lead-in ("duration: N ms" / "execute <unnamed>:") —
		// the SQL itself gets its own highlighted section.
		head := e.FirstLine()
		if i := strings.Index(head, "ms  "); i >= 0 {
			head = head[:i+2]
		} else if i := strings.Index(head, string(e.SQL[:min(len(e.SQL), 20)])); i > 0 {
			head = strings.TrimRight(head[:i], " ")
		}
		section("MESSAGE", []byte(head), false)
		section("STATEMENT", e.SQL, true)
		planSection(e.Plan)
	} else {
		section("MESSAGE", e.Message, false)
	}
	section("DETAIL", e.Detail, false)
	section("HINT", e.Hint, false)
	section("CONTEXT", e.Context, strings.HasPrefix(string(e.Context), "SQL statement"))
	section("STATEMENT", e.Statement, true)
	section("QUERY", e.Query, true)
	section("LOCATION", e.Location, false)

	return scrollWindow(b.String(), &s.offset, height)
}

// dedent strips the common leading tab indentation PostgreSQL adds to
// continuation lines, keeping relative indentation.
func dedent(text string) string {
	lines := strings.Split(text, "\n")
	common := -1
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		n := 0
		for n < len(l) && l[n] == '\t' {
			n++
		}
		if common < 0 || n < common {
			common = n
		}
	}
	if common <= 0 {
		return text
	}
	for i, l := range lines {
		if len(l) >= common {
			lines[i] = l[common:]
		}
	}
	return strings.Join(lines, "\n")
}

// wrapPlain hard-wraps text to width runes, preferring a space to break at
// and preserving existing line breaks.
func wrapPlain(text string, width int) []string {
	var out []string
	for l := range strings.SplitSeq(text, "\n") {
		r := []rune(strings.ReplaceAll(l, "\t", "    "))
		for len(r) > width {
			cut := width
			for i := width; i > width/2; i-- {
				if r[i] == ' ' {
					cut = i
					break
				}
			}
			out = append(out, string(r[:cut]))
			r = r[cut:]
			for len(r) > 0 && r[0] == ' ' {
				r = r[1:]
			}
		}
		out = append(out, string(r))
	}
	return out
}

// renderLogColumnConfig is the C picker over the timeline columns.
func (m *Model) renderLogColumnConfig(v logView, height int) string {
	return logSpec(v).renderConfig(m, m.logTableFor(v), logCtx{}, height)
}

// renderLogsInfo is the ? reference for the log analyzer.
func (m *Model) renderLogsInfo(height int) string {
	mu := styleMuted.Render
	badge := func(s string) string { return styleBadge.Render(s) }
	var b strings.Builder
	infoHeader(&b, "Log analyzer reference")

	b.WriteString("  " + styleHeader.Render(" what it reads ") + "\n")
	b.WriteString("  " + mu("The server log, parsed with log_line_prefix (taken from the server, or detected from the file when that") + "\n")
	b.WriteString("  " + mu("does not match — rotated logs written under an older prefix, files copied from elsewhere). csvlog and jsonlog") + "\n")
	b.WriteString("  " + mu("are recognised too, as is pgbouncer's own log (\"%m [%p] LOG message\": socket events group under conn; the") + "\n")
	b.WriteString("  " + mu("periodic stats lines get their own pooler stats pane, one row per stats_period, cells coloured relative to the") + "\n")
	b.WriteString("  " + mu("column's max). Only the tail window is read (w widens it); the header shows the covered range.") + "\n")
	b.WriteString("  " + mu("DETAIL / HINT / STATEMENT / CONTEXT lines are attached to their primary line, so a group's entry shows all of them.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" where the file comes from ") + "\n")
	b.WriteString("  " + mu("--log-file PATH (or PGDU_LOG_FILE) · pg_current_logfile() when logging_collector is on · the Debian/Ubuntu") + "\n")
	b.WriteString("  " + mu("/var/log/postgresql/postgresql-*.log* files incl. rotated .1 / .2.gz · pgbouncer*.log* and /var/log/pgbouncer ·") + "\n")
	b.WriteString("  " + mu("the server's log directory over the connection") + "\n")
	b.WriteString("  " + mu("(pg_ls_logdir needs pg_monitor; reading any server file needs pg_read_server_files or superuser).") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" categories ") + "\n")
	cats := []struct {
		c pglog.Category
		d string
	}{
		{pglog.CatError, "ERROR / FATAL / PANIC, grouped by normalized message (identifiers kept, literals and numbers folded)"},
		{pglog.CatWarning, "WARNING lines"},
		{pglog.CatLock, "lock waits (log_lock_waits) and deadlocks"},
		{pglog.CatTempFile, "temporary file spills (log_temp_files), grouped by the statement in CONTEXT/STATEMENT"},
		{pglog.CatReplication, "recovery, streaming, archiving, startup/shutdown"},
		{pglog.CatOther, "everything else"},
		{pglog.CatSlowQuery, "duration: lines (log_min_duration_statement), grouped by normalized SQL — /* comments */ kept; auto_explain plans fold into their statement (▤)"},
		{pglog.CatStatement, "statement: / execute lines (log_statement), grouped by normalized SQL like slow queries"},
		{pglog.CatCheckpoint, "checkpoint / restartpoint starting and complete"},
		{pglog.CatAutovacuum, "automatic vacuum / analyze of table"},
		{pglog.CatConnection, "connection received / authorized / disconnection"},
	}
	for _, c := range cats {
		b.WriteString("  " + logCatStyle(c.c).Render(padRight(c.c.Label(), 14)) + mu(c.d) + "\n")
	}
	b.WriteString("\n")

	b.WriteString("  " + styleHeader.Render(" keys ") + "\n")
	keys := []struct{ k, d string }{
		{"↵", "groups pane: open the group's entries · entry rows: the full record (message, DETAIL, STATEMENT highlighted)"},
		{"tab", "cycle the panes: aggregated groups → chronological timeline → slow queries by duration → pooler stats (pgbouncer logs; tables sortable, C picks columns)"},
		{"m", "section mode: by category ⇄ flat"},
		{"j", "on an entry or a group's rows: jump to that line in the timeline"},
		{"d", "describe the main table of the statement behind the row (slow query / log_statement SQL or an error's STATEMENT)"},
		{"←/→ r", "sort groups by count / last seen / title (timeline: by column)"},
		{"/", "substring search over titles (timeline: over every visible cell)"},
		{"w", "widen the tail window: 32 → 64 → 128 → 256 → 512 MiB → whole file"},
		{"t", "live tail cadence: off → 5s → 15s → 60s (incremental re-read; a rotation reloads)"},
		{"space", "reload now"},
		{"esc", "back — to the group, the overview, then the file picker (rotated / .gz files)"},
		{"e", "export the current pane as CSV"},
	}
	for _, k := range keys {
		b.WriteString("  " + badge(padRight(k.k, 7)) + mu(k.d) + "\n")
	}
	b.WriteString("\n  " + styleHeader.Render(" timeline columns ") + "\n")
	for _, d := range logColumnRegistry() {
		b.WriteString("  " + padRight(d.name, 10) + mu(d.desc) + "\n")
	}
	b.WriteString("\n  " + styleHeader.Render(" pooler stats columns ") + "\n")
	for _, d := range logStatsColumnRegistry()[1:] {
		b.WriteString("  " + padRight(d.name, 11) + mu(d.desc) + "\n")
	}
	return padInfo(&b, height)
}

// sortedAutovacTables lists a group's per-table counts, busiest first (used by
// the group summary when the group is autovacuum).
func sortedAutovacTables(av map[string]int) []string {
	type kv struct {
		k string
		v int
	}
	kvs := make([]kv, 0, len(av))
	for k, v := range av {
		kvs = append(kvs, kv{k, v})
	}
	sort.Slice(kvs, func(i, j int) bool { return kvs[i].v > kvs[j].v || kvs[i].v == kvs[j].v && kvs[i].k < kvs[j].k })
	out := make([]string, len(kvs))
	for i, e := range kvs {
		out[i] = fmt.Sprintf("%s ×%d", e.k, e.v)
	}
	return out
}
