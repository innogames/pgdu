package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// hasInfoOverlay reports whether the current level has a ? reference overlay.
// Used both to gate the modal key/wheel handling and to pick the View case.
// Kept in sync with the level set the ? key toggles in handleKey.
func (m *Model) hasInfoOverlay(s *screen) bool {
	switch s.level {
	case levelBufferTables, levelBufferDetail,
		levelHeapPages, levelHeapTuples,
		levelIndexPages, levelIndexTuples,
		levelWAL, levelWALRecords, levelWALBlocks,
		levelStatements, levelStatementDetail, levelStatementSamples, levelStatementResult, levelSnapshots,
		levelMaintenance, levelSettings,
		levelActivity:
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
	case levelHeapPages:
		return m.renderHeapPagesInfo(height)
	case levelHeapTuples:
		return m.renderHeapTuplesInfo(height)
	case levelIndexPages:
		return m.renderIndexPagesInfo(height)
	case levelIndexTuples:
		return m.renderIndexTuplesInfo(height)
	case levelWAL:
		return m.renderWALInfo(height)
	case levelWALRecords:
		return m.renderWALRecordsInfo(height)
	case levelWALBlocks:
		return m.renderWALBlocksInfo(height)
	case levelStatements, levelStatementDetail, levelStatementSamples, levelStatementResult, levelSnapshots:
		return m.renderStatementsInfo(height)
	case levelMaintenance, levelSettings:
		return m.renderMaintenanceInfo(height)
	case levelActivity:
		return m.renderActivityInfo(height)
	}
	return ""
}

// renderLegend returns a one-line colour legend for the current level so
// the user can decode the bar colours without guessing. Returns "" on
// levels whose bars are monochrome (no legend needed).
func renderLegend(s *screen) string {
	swatch := func(style lipgloss.Style, label string) string {
		return style.Render("▇") + " " + styleMuted.Render(label)
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
			swatch(styleIndexSeg, "btree index") + sep +
			swatch(styleToastSeg, "toast")
	case levelIndexPages:
		return "  " + swatch(styleIndexSeg, "live") + sep +
			swatch(styleBloat, "dead") + sep +
			styleMuted.Render("░ free")
	case levelWAL, levelWALRecords:
		return "  " + swatch(styleBar, "record bytes") + sep +
			swatch(styleBarAlt, "FPI bytes (full-page images)")
	case levelWALBlocks:
		return "  " + swatch(styleBarAlt, "FPI bytes") + sep +
			styleMuted.Render("░ no full-page image")
	case levelIndexTuples:
		// Three kinds of bt_page_items rows the user will run into on a
		// modern leaf page: regular entries (pointing at a heap row, so
		// the decoded key resolves and ENTER drills); the high-key
		// pivot at the start of the page (a structural separator, not a
		// row); and posting-list tuples (PG 13+ dedup — one entry packs
		// many heap tids for the same key). The latter two have no
		// single heap row to project, so they show their raw hex data.
		return "  " + styleLPNormal.Render("●") + " " + styleMuted.Render("leaf entry → heap row") + sep +
			styleHeapToastTag.Render("pivot") + " " + styleMuted.Render("high-key separator") + sep +
			styleHeapHot.Render("posting") + " " + styleMuted.Render("packed heap-tid list")
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
	case s.reindexing != "":
		return "  " + styleMuted.Render(m.spinner.View()+" REINDEX INDEX CONCURRENTLY "+s.reindexing+"…")
	case s.pendingReindex != "":
		return "  " + styleSelected.Render("confirm: ") +
			styleMuted.Render("REINDEX INDEX CONCURRENTLY "+s.pendingReindex+" — press ") +
			styleBadge.Render("y") +
			styleMuted.Render(" to run, ") +
			styleBadge.Render("n") +
			styleMuted.Render(" (or any other key) to cancel")
	case s.reindexErr != nil:
		return "  " + styleErr.Render("reindex failed: "+s.reindexErr.Error())
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
	rendered := strings.Count(b.String(), "\n")
	for i := rendered; i < height; i++ {
		b.WriteString("\n")
	}
	return b.String()
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
	rendered := strings.Count(b.String(), "\n")
	for i := rendered; i < height; i++ {
		b.WriteString("\n")
	}
	return b.String()
}
