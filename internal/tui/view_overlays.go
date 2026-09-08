package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// hasInfoOverlay reports whether the current level has a ? reference overlay.
// Used both to gate the modal key/wheel handling and to pick the View case.
// Kept in sync with the level set the ? key toggles in handleKey.
func (m *Model) hasInfoOverlay(s *screen) bool {
	switch s.level {
	case levelBufferTables, levelBufferDetail, levelShmem,
		levelHeapPages, levelHeapTuples,
		levelIndexPages, levelIndexTuples,
		levelWAL, levelWALRecords, levelWALBlocks, levelWALRelBlocks, levelWALBlockDetail,
		levelStatements, levelStatementDetail, levelStatementSamples, levelStatementResult, levelSnapshots,
		levelMaintenance, levelSettings,
		levelActivity, levelTableStats, levelWaitProfile,
		levelDiagnostics, levelDiagnosticResult,
		levelDescribe,
		levelLogFiles, levelLogs, levelLogGroup, levelLogEntry,
		levelPgBouncers, levelPgBouncer, levelPgBouncerShow:
		return true
	}
	return false
}

// renderInfoOverlay returns the full (unscrolled) ? reference body for the
// current level; View runs it through scrollWindow. Only called when
// hasInfoOverlay(s) is true.
func (m *Model) renderInfoOverlay(s *screen, height int) string {
	switch s.level {
	case levelBufferTables:
		return m.renderBufferInfo(height)
	case levelBufferDetail:
		return m.renderBufferDetailInfo(height)
	case levelShmem:
		return m.renderShmemInfo(height)
	case levelHeapPages:
		return m.renderHeapPagesInfo(height)
	case levelHeapTuples:
		return m.renderHeapTuplesInfo(height)
	case levelIndexPages:
		switch s.pages.index.AccessMethod {
		case "gist":
			return m.renderGistInfo(height, false)
		case "brin":
			return m.renderBrinInfo(height, false)
		case "gin":
			return m.renderGinInfo(height, false)
		}
		return m.renderIndexPagesInfo(height)
	case levelIndexTuples:
		switch s.pages.index.AccessMethod {
		case "gist":
			return m.renderGistInfo(height, true)
		case "brin":
			return m.renderBrinInfo(height, true)
		case "gin":
			return m.renderGinInfo(height, true)
		}
		return m.renderIndexTuplesInfo(height)
	case levelWAL:
		return m.renderWALInfo(height)
	case levelWALRecords:
		return m.renderWALRecordsInfo(height)
	case levelWALBlocks, levelWALRelBlocks:
		return m.renderWALBlocksInfo(height)
	case levelWALBlockDetail:
		return m.renderWALBlockDetailInfo(height)
	case levelStatements, levelStatementDetail, levelStatementSamples, levelStatementResult, levelSnapshots:
		return m.renderStatementsInfo(height)
	case levelMaintenance, levelSettings:
		return m.renderMaintenanceInfo(height)
	case levelActivity:
		return m.renderActivityInfo(height)
	case levelTableStats:
		return m.renderTableStatsInfo(height)
	case levelWaitProfile:
		return m.renderWaitProfileInfo(height)
	case levelDiagnostics, levelDiagnosticResult:
		return m.renderDiagnosticInfo(s, height)
	case levelDescribe:
		return m.renderDescribeInfo(s, height)
	case levelLogFiles, levelLogs, levelLogGroup, levelLogEntry:
		return m.renderLogsInfo(height)
	case levelPgBouncers, levelPgBouncer, levelPgBouncerShow:
		return m.renderPgBouncerInfo(height)
	}
	return ""
}

// colCfgRow is one checkbox row of a column-config overlay. note is the badge
// appended to an unavailable row (e.g. "track_planning off"); desc may be
// empty for pickers over dynamic column sets with no descriptions.
type colCfgRow struct {
	name, desc                 string
	on, mandatory, unavailable bool
	note                       string
}

// renderColCfgOverlay draws the htop-style column picker shared by the
// top-queries, activity, table-overview and diagnostic-result C overlays: the
// badge help line, a subtitle, and one checkbox row per column with the cursor
// row highlighted.
func (m *Model) renderColCfgOverlay(subtitle string, rows []colCfgRow, cursor, height int) string {
	mu := styleMuted.Render
	var b strings.Builder

	b.WriteString("\n")
	b.WriteString("  " + styleSelected.Render("configure columns") + mu("  ·  ") +
		styleBadge.Render("space") + mu(" toggles · ") +
		styleBadge.Render("↑/↓") + mu(" move · ") +
		styleBadge.Render("r") + mu(" reset · ") +
		styleBadge.Render("C") + mu(" or ") + styleBadge.Render("esc") + mu(" to close") + "\n")
	b.WriteString("  " + mu(subtitle) + "\n\n")

	nameW := 0
	for _, r := range rows {
		if n := len(r.name); n > nameW {
			nameW = n
		}
	}
	for i, r := range rows {
		box := "[ ]"
		switch {
		case r.unavailable:
			box = "[·]"
		case r.on:
			box = "[x]"
		}
		cur := "  "
		if i == cursor {
			cur = lipgloss.NewStyle().Foreground(colorAccent).Render("▶ ")
		}
		label := box + "  " + padRight(r.name, nameW)
		var rendered string
		switch {
		case r.unavailable:
			rendered = mu(label+"  "+r.desc) + "  " + styleBadge.Render(r.note)
		case i == cursor:
			rendered = styleSelected.Render(label)
			if r.desc != "" {
				rendered += "  " + mu(r.desc)
			}
		default:
			rendered = label
			if r.desc != "" {
				rendered += "  " + mu(r.desc)
			}
		}
		if r.mandatory {
			rendered += mu("  (always shown)")
		}
		b.WriteString(cur + rendered + "\n")
	}
	return padInfo(&b, height)
}

// confirmBanner renders the shared one-line two-step confirmation prompt
// ("confirm: <action> — press [y] to run, [n] … to cancel") used by the
// reindex, vacuum and stats-reset flows.
func confirmBanner(action string) string {
	return "  " + styleSelected.Render("confirm: ") +
		styleMuted.Render(action+" — press ") +
		styleBadge.Render("y") +
		styleMuted.Render(" to run, ") +
		styleBadge.Render("n") +
		styleMuted.Render(" (or any other key) to cancel")
}

// renderLegend returns a one-line colour legend for the current level so
// the user can decode the bar colours without guessing. Returns "" on
// levels whose bars are monochrome (no legend needed).
func renderLegend(s *screen) string {
	swatch := func(style lipgloss.Style, label string) string {
		return swatch(style) + " " + styleMuted.Render(label)
	}
	sep := styleMuted.Render("  ·  ")
	switch s.level {
	case levelTables:
		// Page-inspector tables show a solid heap-only bar; the segmented
		// legend would mislead, so suppress it on that flow.
		if s.tool == toolPageInspect {
			return ""
		}
		return "  " + swatch(styleHeapSeg, "heap") + sep +
			swatch(styleIndexSeg, "index") + sep +
			swatch(styleToastSeg, "toast")
	case levelParts:
		return "  " + swatch(styleBar, "size") + sep +
			swatch(styleBloat, "bloat")
	case levelHeapPages:
		return "  " + swatch(styleHeapSeg, "live") + sep +
			swatch(styleBloat, "dead") + sep +
			styleMuted.Render("░ free") + sep +
			styleHeapHot.Render("H") + " " + styleMuted.Render("hot-updated") + sep +
			styleHeapToastTag.Render("T") + " " + styleMuted.Render("has-external")
	case levelHeapTuples:
		return "  " + styleLPNormal.Render("●") + " " + styleMuted.Render("normal") + sep +
			styleLPRedirect.Render("●") + " " + styleMuted.Render("redirect") + sep +
			styleLPDead.Render("●") + " " + styleMuted.Render("dead") + sep +
			styleLPUnused.Render("●") + " " + styleMuted.Render("unused")
	case levelRelations:
		return "  " + swatch(styleHeapSeg, "table") + sep +
			swatch(styleIndexSeg, "btree") + sep +
			swatch(styleGistSeg, "gist") + sep +
			swatch(styleBrinSeg, "brin") + sep +
			swatch(styleGinSeg, "gin") + sep +
			swatch(styleToastSeg, "toast")
	case levelIndexPages:
		switch s.pages.index.AccessMethod {
		case "gist":
			return "  " + swatch(styleGistSeg, "used") + sep +
				styleMuted.Render("░ free") + sep +
				styleMuted.Render("leaf") + sep +
				styleBarAlt.Render("intr") + sep +
				styleBloat.Render("del")
		case "brin":
			return "  " + swatch(styleBrinSeg, "used") + sep +
				styleMuted.Render("░ free") + sep +
				styleMuted.Render("regular") + sep +
				styleBarAlt.Render("meta/revmap")
		case "gin":
			return "  " + swatch(styleGinSeg, "used") + sep +
				styleMuted.Render("░ free") + sep +
				styleMuted.Render("data-leaf (drillable)") + sep +
				styleBarAlt.Render("entry/meta")
		}
		return "  " + swatch(styleIndexSeg, "live") + sep +
			swatch(styleBloat, "dead") + sep +
			styleMuted.Render("░ free")
	case levelWAL:
		return "  " + swatch(styleBar, "record bytes") + sep +
			swatch(styleBarAlt, "FPI bytes (full-page images)") + sep +
			styleBadge.Render("↵") + styleMuted.Render(" records of a rmgr · block refs of a relation")
	case levelWALRecords:
		return "  " + swatch(styleBar, "record bytes") + sep +
			swatch(styleBarAlt, "FPI bytes (full-page images)")
	case levelWALBlocks, levelWALRelBlocks:
		return "  " + swatch(styleBarAlt, "FPI bytes") + sep +
			styleMuted.Render("░ no full-page image") + sep +
			styleBadge.Render("↵") + styleMuted.Render(" payload: tuple bytes / page image")
	case levelWALBlockDetail:
		return "  " + styleSelected.Render("◀") + styleMuted.Render(" line pointer this record touched") + sep +
			styleMuted.Render("values decoded from raw bytes; NULL = absent in the tuple")
	case levelIndexTuples:
		switch s.pages.index.AccessMethod {
		case "gist":
			return "  " + styleLPNormal.Render("●") + " " + styleMuted.Render("leaf → heap row") + sep +
				styleGistSeg.Render("→ blk") + " " + styleMuted.Render("downlink") + sep +
				styleBloat.Render("dead") + " " + styleMuted.Render("dead entry") + sep +
				styleMuted.Render("keys = opclass-decoded")
		case "brin":
			return "  " + styleIndexSeg.Render("block range") + " " + styleMuted.Render("summarised heap blocks") + sep +
				styleBadge.Render("N") + styleMuted.Render(" has-nulls") + sep +
				styleHeapToastTag.Render("P") + styleMuted.Render(" placeholder") + sep +
				styleMuted.Render("E empty") + sep +
				styleMuted.Render("↵ → heap pages of range")
		case "gin":
			return "  " + styleMuted.Render("each row = one posting-list segment (compressed heap tids)") + sep +
				styleMuted.Render("entry-tree pages aren't itemizable via pageinspect")
		}
		// Three kinds of bt_page_items rows the user will run into on a
		// modern leaf page: regular entries (pointing at a heap row, so
		// the decoded key resolves and ENTER drills); the high-key
		// pivot at the start of the page (a structural separator, not a
		// row); and posting-list tuples (PG 13+ dedup — one entry packs
		// many heap tids for the same key). The latter two have no
		// single heap row to project, so they show their raw hex data.
		return "  " + styleLPNormal.Render("●") + " " + styleMuted.Render("leaf → heap row") + sep +
			styleIndexSeg.Render("→ blk") + " " + styleMuted.Render("downlink") + sep +
			styleHeapToastTag.Render("pivot") + "/" + styleHeapToastTag.Render("high key") + " " + styleMuted.Render("page bound") + sep +
			styleHeapHot.Render("posting ×N") + " " + styleMuted.Render("packed tids, ↵ unfolds") + sep +
			styleHeapHot.Render("▸off") + " " + styleMuted.Render("HOT hop")
	}
	return ""
}

// renderReindexBanner renders the one-line status for the per-row REINDEX
// flow on the parts level: pending confirmation, in-flight progress, or the
// last failure. Returns "" when there's nothing to show.
func (m *Model) renderReindexBanner(s *screen) string {
	if s.level != levelParts {
		return ""
	}
	switch {
	case s.reindex.running != "":
		mu := styleMuted.Render
		line := "  " + m.spinner.View() + " " +
			styleSelected.Render("REINDEX") + mu(" CONCURRENTLY "+s.reindex.running)
		// Live progress, once pg_stat_progress_create_index starts reporting.
		// The bar is the overall composite (reindexPctMax, monotonic across
		// phases), not the current phase's own resetting counters.
		if p := s.reindex.prog; p != nil {
			if p.Phase != "" {
				label := p.Phase
				if p.Waiting() && p.LockersTotal > 0 {
					label += fmt.Sprintf(" (%d/%d)", p.LockersDone, p.LockersTotal)
				}
				line += mu("  ·  " + label)
			}
			const barW = 48
			pct := s.reindex.pctMax
			filled := min(int(float64(barW)*pct/100), barW)
			line += "  " + paintBar(barW, barSegment{cells: filled, style: styleBar}) +
				mu(fmt.Sprintf(" %.0f%%", pct))
		}
		return line
	case s.reindex.pending != "":
		return confirmBanner("REINDEX INDEX CONCURRENTLY " + s.reindex.pending)
	case s.reindex.err != nil:
		return "  " + styleErr.Render("reindex failed: "+s.reindex.err.Error())
	}
	return ""
}

// renderExtHint renders a single muted line above the list, suggesting an
// optional extension. Pressing `i` triggers the install.
func (m *Model) renderExtHint(s *screen) string {
	p := s.extPrompt
	if p == nil {
		return ""
	}
	if s.installing {
		return "  " + styleMuted.Render(m.spinner.View()+" installing "+p.name+"…")
	}
	if p.err != nil {
		return "  " + styleErr.Render("install "+p.name+" failed: "+p.err.Error()) + "  " +
			styleMuted.Render("(press i to retry)")
	}
	if !p.installable {
		return "  " + styleMuted.Render("hint: "+p.reason+" — "+p.name+" not available on this server")
	}
	return "  " + styleMuted.Render("hint: "+p.reason+" — press ") +
		styleBadge.Render("i") + styleMuted.Render(" to install "+p.name)
}

// renderExtPrompt renders the blocking "install this extension?" screen.
// Called instead of the list when extPrompt.blocking is set.
func (m *Model) renderExtPrompt(s *screen, height int) string {
	p := s.extPrompt
	if p.upgrade {
		return m.renderUpgradePrompt(s, height)
	}
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString("  " + styleSelected.Render("Extension required") + "\n\n")
	b.WriteString("  " + p.reason + "\n")
	b.WriteString("  " + styleMuted.Render("missing: "+p.name+" in database "+p.db) + "\n\n")
	switch {
	case s.installing:
		b.WriteString("  " + m.spinner.View() + " installing " + p.name + "…\n")
	case p.err != nil:
		b.WriteString("  " + styleErr.Render("install failed: "+p.err.Error()) + "\n")
		b.WriteString("  " + styleMuted.Render("press ") + styleBadge.Render("i") +
			styleMuted.Render(" to retry, or ") + styleBadge.Render("←") +
			styleMuted.Render(" to back out") + "\n")
	case p.installable:
		b.WriteString("  press " + styleBadge.Render("i") +
			" to run " + styleMuted.Render("CREATE EXTENSION "+p.name) + "\n")
		b.WriteString("  " + styleMuted.Render("(requires database-owner or superuser privileges)") + "\n")
	default:
		b.WriteString("  " + styleErr.Render(p.name+" is not available on this server — ask the DBA to install it") + "\n")
	}
	return padInfo(&b, height)
}

// renderUpgradePrompt renders the blocking "extension outdated" screen for the
// upgrade variant of extPrompt: it states the installed and available versions
// (the "what is installed / what is possible" note) and offers the ALTER
// EXTENSION UPDATE that lifts it. Shown instead of an opaque "column does not
// exist" error when a pg_upgraded cluster still carries an old extension.
func (m *Model) renderUpgradePrompt(s *screen, height int) string {
	p := s.extPrompt
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString("  " + styleSelected.Render("Extension outdated") + "\n\n")
	b.WriteString("  " + p.reason + "\n")
	b.WriteString("  " + styleMuted.Render(p.name+" in database "+p.db+" is too old for this view") + "\n")
	b.WriteString("  " + styleMuted.Render("installed ") + styleBadge.Render(p.installed) +
		styleMuted.Render(" · available ") + styleBadge.Render(p.available) +
		styleMuted.Render(" · pgdu needs ≥ ") + styleBadge.Render(p.required) + "\n\n")
	switch {
	case s.installing:
		b.WriteString("  " + m.spinner.View() + " upgrading " + p.name + "…\n")
	case p.err != nil:
		b.WriteString("  " + styleErr.Render("upgrade failed: "+p.err.Error()) + "\n")
		b.WriteString("  " + styleMuted.Render("press ") + styleBadge.Render("i") +
			styleMuted.Render(" to retry, or ") + styleBadge.Render("←") +
			styleMuted.Render(" to back out") + "\n")
		b.WriteString("  " + styleMuted.Render("(ALTER EXTENSION requires extension-owner or superuser privileges)") + "\n")
	case p.installable:
		b.WriteString("  press " + styleBadge.Render("i") +
			" to run " + styleMuted.Render("ALTER EXTENSION "+p.name+" UPDATE") + "\n")
		b.WriteString("  " + styleMuted.Render("(requires extension-owner or superuser privileges)") + "\n")
	default:
		b.WriteString("  " + styleErr.Render("the server's own "+p.name+" ("+p.available+
			") is older than pgdu needs — upgrade PostgreSQL / the extension package") + "\n")
	}
	return padInfo(&b, height)
}
