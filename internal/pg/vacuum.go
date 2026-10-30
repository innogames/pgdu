package pg

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// VacuumTable runs VACUUM (VERBOSE, ANALYZE, SKIP_LOCKED) on t.
// SKIP_LOCKED means a table someone else holds is passed over rather than
// waited for, so the key never blocks behind another session.
func (c *Client) VacuumTable(ctx context.Context, t Table, onLine func(string)) error {
	return c.maintenanceStmt(ctx, t, "vacuum",
		"VACUUM (VERBOSE, ANALYZE, SKIP_LOCKED) "+qualifiedIdent(t.Schema, t.Name), onLine)
}

// VacuumFreezeTable is VacuumTable with FREEZE: every tuple old enough is
// frozen rather than only those past vacuum_freeze_min_age, which is what
// actually advances relfrozenxid on a table that keeps tripping the freeze
// warnings. It costs a full scan of the heap, so it is a separate key rather
// than an option on the ordinary vacuum.
func (c *Client) VacuumFreezeTable(ctx context.Context, t Table, onLine func(string)) error {
	return c.maintenanceStmt(ctx, t, "vacuum freeze",
		"VACUUM (VERBOSE, FREEZE, ANALYZE, SKIP_LOCKED) "+qualifiedIdent(t.Schema, t.Name), onLine)
}

// AnalyzeTable runs ANALYZE VERBOSE on t. Vacuum already implies it, but on a
// large table the vacuum is the expensive half and stale statistics are a
// common enough cause on their own — this refreshes the planner's picture (and
// with it every estimate pgdu derives from pg_stats) without touching a page
// more than the sample needs.
func (c *Client) AnalyzeTable(ctx context.Context, t Table, onLine func(string)) error {
	return c.maintenanceStmt(ctx, t, "analyze",
		"ANALYZE VERBOSE "+qualifiedIdent(t.Schema, t.Name), onLine)
}

// maintenanceStmt runs one long-running maintenance statement over a dedicated
// non-pool connection, so VERBOSE notices can be streamed via OnNotice without
// hogging one of the pool's 4 connections for the duration. onLine is called
// from pgx's receive loop for each output line — it must not block for long.
// Cancellation via ctx sends a Postgres cancel request so the server aborts
// promptly. op names the operation in the error.
func (c *Client) maintenanceStmt(ctx context.Context, t Table, op, stmt string, onLine func(string)) error {
	connCfg, err := pgx.ParseConfig(c.cfg.BuildDSN(t.DB))
	if err != nil {
		return fmt.Errorf("parse dsn for %q: %w", t.DB, err)
	}
	connCfg.OnNotice = func(_ *pgconn.PgConn, n *pgconn.Notice) {
		for _, ln := range NoticeLines(n) {
			onLine(ln)
		}
	}
	conn, err := pgx.ConnectConfig(ctx, connCfg)
	if err != nil {
		return fmt.Errorf("connect %q: %w", t.DB, err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	_, _ = conn.Exec(ctx, "SET pg_stat_statements.track = 'none'")
	if _, err := conn.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("%s %q.%q: %w", op, t.Schema, t.Name, err)
	}
	return nil
}

// NoticeLines flattens one PostgreSQL notice into displayable text lines.
// INFO-level messages (the normal VACUUM VERBOSE output) are returned without
// a severity prefix; other severities (WARNING for skipped-lock, etc.) keep a
// "SEVERITY: " prefix so they stand out in the pane. Detail is appended as
// indented sub-lines when present. Exported for unit tests.
func NoticeLines(n *pgconn.Notice) []string {
	msg := strings.TrimRight(n.Message, "\n")
	if msg == "" {
		return nil
	}
	prefix := ""
	if n.Severity != "INFO" {
		prefix = n.Severity + ": "
	}
	var lines []string
	for ln := range strings.SplitSeq(msg, "\n") {
		lines = append(lines, prefix+ln)
	}
	if d := strings.TrimRight(n.Detail, "\n"); d != "" {
		for ln := range strings.SplitSeq(d, "\n") {
			if strings.TrimSpace(ln) != "" {
				lines = append(lines, "  "+ln)
			}
		}
	}
	return lines
}
