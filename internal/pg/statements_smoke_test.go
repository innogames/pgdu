package pg

import (
	"context"
	"os"
	"testing"

	"pgdu/internal/cli"
)

// Smoke test for the top-queries client methods against a live local server
// (peer auth over the unix socket). Run with:
//
//	PGDU_SMOKE_DB=matze go test ./internal/pg -run TestStatementsSmoke -v
func TestStatementsSmoke(t *testing.T) {
	db := os.Getenv("PGDU_SMOKE_DB")
	if db == "" {
		t.Skip("PGDU_SMOKE_DB not set")
	}
	cfg := cli.Config{User: os.Getenv("USER"), Database: db, SSLMode: "disable"}
	c := New(cfg)
	defer c.Close()
	ctx := context.Background()

	if err := c.EnsureStatements(ctx, db); err != nil {
		t.Fatalf("EnsureStatements: %v", err)
	}

	snap, err := c.StatementSnapshot(ctx, db)
	if err != nil {
		t.Fatalf("StatementSnapshot: %v", err)
	}
	t.Logf("snapshot rows: %d", len(snap))
	if len(snap) == 0 {
		t.Skip("no statements recorded yet")
	}

	// Find a parametrized SELECT to exercise inference + explain.
	var target *QueryStat
	for i := range snap {
		q := snap[i].Query
		if len(q) > 6 && (q[:6] == "SELECT" || q[:6] == "select") {
			target = &snap[i]
			break
		}
	}
	if target == nil {
		target = &snap[0]
	}
	t.Logf("target query: %s", target.Query)

	params, err := c.InferParams(ctx, db, target.Query)
	if err != nil {
		t.Logf("InferParams (non-fatal): %v", err)
	} else {
		t.Logf("inferred %d params: %+v", len(params), params)
		t.Logf("sample call: %s", BuildSampleCall(target.Query, params))
	}

	plan, err := c.ExplainGeneric(ctx, db, target.Query)
	if err != nil {
		t.Logf("ExplainGeneric (non-fatal — some queries can't be generic-planned): %v", err)
	} else {
		t.Logf("plan:\n%s", plan)
	}

	// Real parameters via pg_qualstats — only exercised when the extension is
	// installed (it needs shared_preload_libraries + a restart). Skipped cleanly
	// otherwise so the smoke test still passes on a plain server.
	if err := c.EnsureQualstats(ctx, db); err != nil {
		t.Logf("pg_qualstats not available (non-fatal): %v", err)
		return
	}
	example, err := c.QualstatsExampleQuery(ctx, db, target.QueryID)
	if err != nil {
		t.Logf("QualstatsExampleQuery (non-fatal): %v", err)
	} else if example == "" {
		t.Logf("pg_qualstats has no captured example for queryid %d yet", target.QueryID)
	} else {
		t.Logf("real example query: %s", example)
		if p, err := c.ExplainLiteral(ctx, db, example); err != nil {
			t.Logf("ExplainLiteral (non-fatal): %v", err)
		} else {
			t.Logf("real plan:\n%s", p)
		}
	}
	samples, err := c.QualstatsSamples(ctx, db, target.QueryID)
	if err != nil {
		t.Logf("QualstatsSamples (non-fatal): %v", err)
	} else {
		t.Logf("captured %d real values for queryid %d", len(samples), target.QueryID)
		for _, s := range samples {
			t.Logf("  %s.%s %s %s  (%d×)", s.Relation, s.Column, s.Operator, s.ConstValue, s.Occurrences)
		}
	}
}
