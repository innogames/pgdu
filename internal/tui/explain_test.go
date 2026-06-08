package tui

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"pgdu/internal/cli"
	"pgdu/internal/pg"
)

// go test has no TTY, so lipgloss auto-detects the Ascii profile and strips all
// colour — which would make the heat-colour assertions below trivially pass.
// Force a colour profile so Render actually emits ANSI.
func TestMain(m *testing.M) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	os.Exit(m.Run())
}

// newExplainModel builds a Model wide enough that clipDetail never truncates
// the sample plans. The client is never exercised by colorizeExplain.
func newExplainModel() *Model {
	m := NewModel(pg.New(cli.Config{}), 2*time.Second, "")
	m.width = 200
	return m
}

// The user's sample analyzed plan: the users Index Scan does real work, the
// documents Index Scan is never executed.
const sampleAnalyzePlan = `Nested Loop  (cost=0.58..16.54 rows=1 width=116) (actual time=0.033..0.034 rows=0.00 loops=1)
  Output: documents.id, documents.owner_id
  Buffers: shared hit=2
  ->  Index Scan using users_country_idx on app.users  (cost=0.29..8.23 rows=1 width=8) (actual time=0.032..0.033 rows=0.00 loops=1)
        Output: users.id, users.username
        Index Cond: (users.country = 'sample'::text)
        Buffers: shared hit=2
  ->  Index Scan using documents_owner_idx on app.documents  (cost=0.29..8.30 rows=1 width=116) (never executed)
        Output: documents.id
        Index Cond: (documents.owner_id = users.id)
Execution Time: 0.103 ms`

func TestParseExplainSelfTime(t *testing.T) {
	nodes := parseExplainTree(sampleAnalyzePlan, true)
	if len(nodes) != 3 {
		t.Fatalf("expected 3 nodes (Nested Loop + 2 Index Scans), got %d", len(nodes))
	}
	// Root inclusive = 0.034*1; users scan inclusive = 0.033*1; documents never
	// executed = 0. Root self = 0.034-0.033 = ~0.001; users self = 0.033.
	root, users, docs := nodes[0], nodes[1], nodes[2]
	if docs.inclusive != 0 {
		t.Errorf("never-executed node should have 0 inclusive, got %v", docs.inclusive)
	}
	if users.self <= docs.self {
		t.Errorf("users scan self (%v) should exceed never-executed self (%v)", users.self, docs.self)
	}
	if users.self <= root.self {
		t.Errorf("users scan self (%v) should be the bottleneck, exceeding root self (%v)", users.self, root.self)
	}
}

func TestColorizeExplainHeat(t *testing.T) {
	m := newExplainModel()
	out := m.colorizeExplain(sampleAnalyzePlan, true)
	joined := strings.Join(out, "\n")
	if !strings.Contains(joined, "\x1b[") {
		t.Fatal("expected ANSI colour in a heat-graded analyzed plan")
	}

	// The bottleneck users-scan line carries colour; the never-executed
	// documents-scan line stays plain.
	var usersLine, docsLine string
	for _, l := range out {
		if strings.Contains(l, "users_country_idx") {
			usersLine = l
		}
		if strings.Contains(l, "documents_owner_idx") {
			docsLine = l
		}
	}
	if !strings.Contains(usersLine, "\x1b[") {
		t.Error("bottleneck (users scan) line should be coloured")
	}
	if strings.Contains(docsLine, "\x1b[") {
		t.Error("never-executed (documents scan) line should render plain")
	}
}

func TestColorizeExplainGenericPlan(t *testing.T) {
	// No ANALYZE: grading falls back to cost. The Seq Scan dominates the cheap
	// Limit above it, so its (cost=…) gets coloured.
	plan := `Limit  (cost=0.00..100.00 rows=10 width=4)
  ->  Seq Scan on big  (cost=0.00..90.00 rows=100000 width=4)
        Filter: (id = $1)`
	m := newExplainModel()
	out := m.colorizeExplain(plan, false)
	var seqLine string
	for _, l := range out {
		if strings.Contains(l, "Seq Scan") {
			seqLine = l
		}
	}
	if !strings.Contains(seqLine, "\x1b[") {
		t.Error("expected the dominant Seq Scan cost to be coloured in a generic plan")
	}
}

// A plan with no positive root time (everything trivial / never executed) must
// round-trip unchanged — no ANSI injected.
func TestColorizeExplainNoHeat(t *testing.T) {
	plan := `Result  (cost=0.00..0.00 rows=1 width=4) (never executed)`
	m := newExplainModel()
	out := m.colorizeExplain(plan, true)
	if strings.Contains(strings.Join(out, "\n"), "\x1b[") {
		t.Error("a zero-time plan should render with no colour")
	}
}
