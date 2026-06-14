package pg

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// VacuumTable runs VACUUM (VERBOSE, ANALYZE, SKIP_LOCKED) on t over a dedicated
// non-pool connection so VERBOSE notices can be streamed via OnNotice without
// hogging one of the pool's 4 connections for the duration of the vacuum.
// onLine is called from pgx's receive loop for each output line — it must not
// block for long. Cancellation via ctx sends a Postgres cancel request so the
// server aborts the operation promptly.
func (c *Client) VacuumTable(ctx context.Context, t Table, onLine func(string)) error {
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
	stmt := fmt.Sprintf("VACUUM (VERBOSE, ANALYZE, SKIP_LOCKED) %q.%q", t.Schema, t.Name)
	if _, err := conn.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("vacuum %q.%q: %w", t.Schema, t.Name, err)
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
