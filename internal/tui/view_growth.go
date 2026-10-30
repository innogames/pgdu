package tui

import (
	"strings"
)

// renderGrowthColumnConfig draws the htop-style column picker for the growth
// table (C on levelGrowth). One picker serves every scope, so the checkbox
// list stays stable as Tab cycles.
func (m *Model) renderGrowthColumnConfig(s *screen, height int) string {
	ctx := growthCtx{view: s.growth.view, window: s.growth.duration()}
	return growthSpec.renderConfig(m, &m.growthTable, ctx, height)
}

// renderGrowthInfo is the ? reference for the size-history tool: what a sample
// and a window are, what each tag means, and — just as important — what pgdu
// will not guess. The column glossary under it is built from the same registry
// the C picker uses, so the two never drift.
func (m *Model) renderGrowthInfo(height int) string {
	mu := styleMuted.Render
	var b strings.Builder

	infoHeader(&b, "size history reference")
	b.WriteString("  " + mu("A ") + styleSelected.Render("sample") + mu(" is a point-in-time capture of every relation's size in one") + "\n")
	b.WriteString("  " + mu("database. Samples live in the snapshot directory next to the query snapshots;") + "\n")
	b.WriteString("  " + mu("the two are different kinds and neither browser lists the other's files.") + "\n\n")

	b.WriteString("  " + mu("A ") + styleSelected.Render("window") + mu(" is two samples. Δ = end − base; ") + styleSelected.Render("per_day") +
		mu(" normalises Δ by the") + "\n")
	b.WriteString("  " + mu("window's wall-clock length, so windows of different lengths compare directly.") + "\n")
	b.WriteString("  " + mu("A window shorter than a minute is refused rather than reported as noise.") + "\n\n")

	b.WriteString("  " + mu("Rows are identified by ") + styleSelected.Render("(database, schema, relname)") +
		mu(", not by OID, which") + "\n")
	b.WriteString("  " + mu("survives pg_upgrade but not a dump/restore. The consequences are tagged, never") + "\n")
	b.WriteString("  " + mu("silently corrected:") + "\n")
	b.WriteString("    " + styleSelected.Render("new") + mu("        absent from the baseline; carries its whole size as the delta,") + "\n")
	b.WriteString("    " + mu("           because a fresh 40 GB partition really is growth") + "\n")
	b.WriteString("    " + styleSelected.Render("gone") + mu("       dropped; negative delta, hidden until ") + styleBadge.Render("v") + "\n")
	b.WriteString("    " + styleSelected.Render("rewritten") + mu("  relfilenode changed — VACUUM FULL / CLUSTER / REINDEX / a") + "\n")
	b.WriteString("    " + mu("           rewriting ALTER — with the delta unmodified, because the") + "\n")
	b.WriteString("    " + mu("           shrink is real disk") + "\n")
	b.WriteString("  " + mu("A rename therefore reads as one ") + styleSelected.Render("gone") + mu(" plus one ") +
		styleSelected.Render("new") + mu(". A trailing ") + styleSelected.Render("?") +
		mu(" on a tag means the") + "\n")
	b.WriteString("  " + mu("other sample had a size floor, so the relation may simply have been under it.") + "\n\n")

	b.WriteString("  " + styleSelected.Render("Δ%") + mu(" is undefined against a zero baseline: it reads \"new\", carries no number") + "\n")
	b.WriteString("  " + mu("and sorts last. Sizes are measured, so they are always comparable; row counts") + "\n")
	b.WriteString("  " + mu("are counters, so they read \"—\" when the window straddles a statistics reset.") + "\n\n")

	b.WriteString("  " + mu("Every change column carries an explicit sign and is coloured by ") +
		styleSelected.Render("direction") + mu(", not") + "\n")
	b.WriteString("  " + mu("by judgement — growth is not inherently bad:") + "\n")
	b.WriteString("    " + deltaStyle(-1, 1).Render("-4.52 MB") + mu("   a shrink: disk handed back") + "\n")
	b.WriteString("    " + deltaStyle(1, 100).Render("+4.52 MB") + mu("   growth, graded against the largest change in its column,") + "\n")
	b.WriteString("    " + deltaStyle(70, 100).Render("+452 MB") + mu("    so whatever is driving the table stands out") + "\n")
	b.WriteString("    " + deltaStyle(0, 1).Render("0 B") + mu("        no change at all") + "\n")
	b.WriteString("  " + styleSelected.Render("Δheap") + mu(" / ") + styleSelected.Render("Δidx") + mu(" / ") +
		styleSelected.Render("Δtoast") + mu(" split the change by fork: growth that is all index on an") + "\n")
	b.WriteString("  " + mu("unchanged heap is index bloat, not data, and wants a REINDEX rather than a plan.") + "\n\n")

	b.WriteString("  " + styleBadge.Render("tab") + mu(" cycles the scope — relations → by schema → by database → partition") + "\n")
	b.WriteString("  " + mu("      trees — over the same rows; ") + styleBadge.Render("↵") +
		mu(" narrows to one row's members, ") + styleBadge.Render("esc") + mu(" widens,") + "\n")
	b.WriteString("  " + mu("      then pops. ") + styleBadge.Render("↵") + mu(" on a relation opens its disk parts.") + "\n")
	b.WriteString("  " + styleBadge.Render("S") + mu("   captures a sample now (minutes on a large cluster). ") +
		styleBadge.Render("L") + mu(" reopens the") + "\n")
	b.WriteString("  " + mu("      sample browser · ") + styleBadge.Render("D") + mu(" deletes a sample · ") +
		styleBadge.Render("C") + mu(" picks columns.") + "\n")
	b.WriteString("  " + styleSelected.Render("pgdu --sample") + mu(" from cron is how a real history accumulates — pgdu has no") + "\n")
	b.WriteString("  " + mu("      daemon, so nothing is captured while nobody is looking.") + "\n\n")

	reg := growthColumnRegistry()
	nameW := 0
	for _, d := range reg {
		if n := len([]rune(d.name)); n > nameW {
			nameW = n
		}
	}
	for _, d := range reg {
		b.WriteString("  " + styleSelected.Render(padRight(d.name, nameW)) + "  " + mu(d.desc) + "\n")
	}
	return padInfo(&b, height)
}
