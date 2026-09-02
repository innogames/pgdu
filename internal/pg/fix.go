package pg

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// RunFix executes a suggested-fix script (Diagnostic.Fix output) in db,
// statement by statement, streaming progress to onLine: a "▶ <statement>"
// marker before each, every server notice it raises (VACUUM VERBOSE, REINDEX
// warnings), and a "✓ <command tag> · <elapsed>" line after. It stops at the
// first failure and returns that error, with the statement that failed.
//
// Statements run one at a time over a dedicated connection rather than as one
// multi-statement Exec: a simple-protocol batch is wrapped in an implicit
// transaction block, which VACUUM, REINDEX CONCURRENTLY and DROP INDEX
// CONCURRENTLY all refuse. A dedicated connection also lets a leading
// SET lock_timeout apply to the ALTER that follows, keeps notices flowing via
// OnNotice, and — like VacuumTable — keeps a long VACUUM off the pool's few
// connections. onLine is called from pgx's receive loop and must not block.
func (c *Client) RunFix(ctx context.Context, db, script string, onLine func(string)) error {
	stmts := SplitSQLStatements(script)
	if len(stmts) == 0 {
		return fmt.Errorf("fix in %q: no executable statement", db)
	}
	connCfg, err := pgx.ParseConfig(c.cfg.BuildDSN(db))
	if err != nil {
		return fmt.Errorf("parse dsn for %q: %w", db, err)
	}
	connCfg.OnNotice = func(_ *pgconn.PgConn, n *pgconn.Notice) {
		for _, ln := range NoticeLines(n) {
			onLine(ln)
		}
	}
	conn, err := pgx.ConnectConfig(ctx, connCfg)
	if err != nil {
		return fmt.Errorf("connect %q: %w", db, err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	// Keep the maintenance statements themselves out of the top-queries table.
	_, _ = conn.Exec(ctx, "SET pg_stat_statements.track = 'none'")

	for _, stmt := range stmts {
		onLine("▶ " + stmt)
		start := time.Now()
		tag, err := conn.Exec(ctx, stmt)
		if err != nil {
			return fmt.Errorf("fix in %q: %s: %w", db, firstLine(stmt), err)
		}
		onLine(fmt.Sprintf("✓ %s · %s", tag.String(), time.Since(start).Round(time.Millisecond)))
	}
	return nil
}

// SplitSQLStatements breaks a fix script into its executable statements: the
// text between top-level semicolons, with -- comments (the builders' advice
// lines) removed. A semicolon inside a quoted identifier or string literal is
// content, not a separator, so a table called "a;b" survives. Trailing
// whitespace and the terminating semicolon are dropped; empty statements
// (comment-only scripts, doubled semicolons) are skipped. Exported for tests.
func SplitSQLStatements(script string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			out = append(out, s)
		}
		cur.Reset()
	}
	var quote byte // active quote char (' or "), 0 outside quotes
	for i := 0; i < len(script); i++ {
		c := script[i]
		switch {
		case quote != 0:
			cur.WriteByte(c)
			if c == quote {
				// A doubled quote is an escaped quote inside the literal.
				if i+1 < len(script) && script[i+1] == quote {
					cur.WriteByte(quote)
					i++
				} else {
					quote = 0
				}
			}
		case c == '\'' || c == '"':
			quote = c
			cur.WriteByte(c)
		case c == '-' && i+1 < len(script) && script[i+1] == '-':
			for i < len(script) && script[i] != '\n' {
				i++
			}
			cur.WriteByte('\n')
		case c == ';':
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return out
}

// firstLine trims an error subject to its first line so the wrapped error
// stays one line in the TUI status row.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
