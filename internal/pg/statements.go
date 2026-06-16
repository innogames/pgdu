package pg

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// EnsureStatements makes sure pg_stat_statements is installed in db. Mirrors
// EnsureWALInspect: returns *MissingExtensionError when missing so the TUI can
// offer an interactive install. Note that even after CREATE EXTENSION the view
// only collects data when the library is in shared_preload_libraries (which
// needs a server restart); when it isn't, the snapshot query surfaces that as
// an ordinary error.
func (c *Client) EnsureStatements(ctx context.Context, db string) error {
	return c.ensureExtension(ctx, db, "pg_stat_statements", c.statStatementsReady)
}

// EnsureQualstats makes sure pg_qualstats is installed in db. Like
// EnsureStatements it returns *MissingExtensionError when missing; callers in
// the Top-queries view treat that as "no real parameters available" and fall
// back to synthesized literals rather than surfacing it as a failure. Note
// pg_qualstats only records data when its library is in
// shared_preload_libraries (a server restart), which CREATE EXTENSION alone
// can't arrange — so pgdu detects and uses it but does not offer to install it.
func (c *Client) EnsureQualstats(ctx context.Context, db string) error {
	return c.ensureExtension(ctx, db, "pg_qualstats", c.qualstatsReady)
}

// QualstatsPreloaded reports whether pg_qualstats is listed in
// shared_preload_libraries. That's the precondition for it to actually collect
// quals once it's CREATE EXTENSION'd: without the preload, creating the
// extension makes its views exist but they stay empty until a server restart
// loads the library. pgdu therefore only offers a one-key install when this is
// true (a plain CREATE EXTENSION is then enough); otherwise it stays in
// detect-only mode and falls back to synthesized literals.
func (c *Client) QualstatsPreloaded(ctx context.Context, db string) (bool, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return false, err
	}
	var spl string
	if err := pool.QueryRow(ctx,
		"SELECT COALESCE(current_setting('shared_preload_libraries', true), '')",
	).Scan(&spl); err != nil {
		return false, fmt.Errorf("read shared_preload_libraries in %q: %w", db, err)
	}
	for lib := range strings.SplitSeq(spl, ",") {
		if strings.TrimSpace(lib) == "pg_qualstats" {
			return true, nil
		}
	}
	return false, nil
}

// StatementSnapshot reads the current (cumulative-since-reset) pg_stat_statements
// counters for db. Callers diff two snapshots to build a window (DiffStatements).
func (c *Client) StatementSnapshot(ctx context.Context, db string) ([]QueryStat, error) {
	if err := c.EnsureStatements(ctx, db); err != nil {
		return nil, err
	}
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return nil, err
	}
	major, minor, err := c.statementsVersion(ctx, db)
	if err != nil {
		return nil, err
	}
	// A cluster pg_upgraded to PG17 can still carry a 1.6/1.7 extension whose
	// catalog lacks total_exec_time/plans/wal_*; running the query would fail
	// with an opaque "column does not exist". Detect it here and hand the TUI a
	// typed error carrying the upgrade path instead.
	if !statementsAtLeast(major, minor, statementsMinMajor, statementsMinMinor) {
		var def string
		_ = pool.QueryRow(ctx, sqlStatementsDefaultVersion).Scan(&def)
		dMaj, dMin := parseExtVersion(def)
		return nil, &OutdatedExtensionError{
			Extension: "pg_stat_statements",
			DB:        db,
			Installed: fmt.Sprintf("%d.%d", major, minor),
			Available: def,
			Required:  fmt.Sprintf("%d.%d", statementsMinMajor, statementsMinMinor),
			Updatable: def != "" && statementsAtLeast(dMaj, dMin, statementsMinMajor, statementsMinMinor),
		}
	}
	rows, err := pool.Query(ctx, statementsQuery(major, minor))
	if err != nil {
		return nil, fmt.Errorf("read pg_stat_statements in %q: %w", db, err)
	}
	defer rows.Close()
	var out []QueryStat
	for rows.Next() {
		var q QueryStat
		if err := rows.Scan(
			&q.QueryID, &q.UserID, &q.DBID, &q.Query,
			&q.Calls, &q.Rows,
			&q.TotalExecTime, &q.MinExecTime, &q.MaxExecTime, &q.MeanExecTime, &q.StddevExecTime,
			&q.Plans, &q.TotalPlanTime,
			&q.SharedBlksHit, &q.SharedBlksRead, &q.SharedBlksDirtied, &q.SharedBlksWritten,
			&q.LocalBlksHit, &q.LocalBlksRead, &q.LocalBlksDirtied, &q.LocalBlksWritten,
			&q.TempBlksRead, &q.TempBlksWritten,
			&q.SharedBlkReadTime, &q.SharedBlkWriteTime,
			&q.LocalBlkReadTime, &q.LocalBlkWriteTime,
			&q.TempBlkReadTime, &q.TempBlkWriteTime,
			&q.WALRecords, &q.WALFPI, &q.WALBytes,
		); err != nil {
			return nil, fmt.Errorf("read pg_stat_statements in %q: %w", db, err)
		}
		out = append(out, q)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read pg_stat_statements in %q: %w", db, err)
	}
	return out, nil
}

// StatementsInfo returns the last time pg_stat_statements counters were reset
// for db (pg_stat_statements_info, PG14+). Best-effort: a zero time with nil
// error means the view exists but has never recorded a reset, or the read was
// not permitted — callers use it only to warn about an invalidated baseline.
func (c *Client) StatementsInfo(ctx context.Context, db string) (time.Time, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return time.Time{}, err
	}
	var reset *time.Time
	if err := pool.QueryRow(ctx, sqlStatementsInfo).Scan(&reset); err != nil {
		return time.Time{}, fmt.Errorf("read pg_stat_statements_info in %q: %w", db, err)
	}
	if reset == nil {
		return time.Time{}, nil
	}
	return *reset, nil
}

// statementsVersion returns the installed pg_stat_statements extension version as
// (major, minor), cached per database (it only changes on ALTER EXTENSION, so one
// read per session is enough). The version drives which I/O-timing column names
// statementsQuery selects — on a pg_upgraded PG17 cluster the extension can still
// be 1.10, which lacks the 1.11 shared_blk_*_time / local_blk_*_time columns. An
// unparseable version is treated as the newest (current) layout.
func (c *Client) statementsVersion(ctx context.Context, db string) (int, int, error) {
	c.mu.Lock()
	if c.statStatementsVerKnown[db] {
		v := c.statStatementsVer[db]
		c.mu.Unlock()
		return v[0], v[1], nil
	}
	c.mu.Unlock()

	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return 0, 0, err
	}
	var ver string
	if err := pool.QueryRow(ctx, sqlStatementsVersion).Scan(&ver); err != nil {
		return 0, 0, fmt.Errorf("read pg_stat_statements version in %q: %w", db, err)
	}
	major, minor := parseExtVersion(ver)

	c.mu.Lock()
	c.statStatementsVer[db] = [2]int{major, minor}
	c.statStatementsVerKnown[db] = true
	c.mu.Unlock()
	return major, minor, nil
}

// parseExtVersion parses an extension version like "1.11" into (1, 11). A version
// it can't parse (empty / unexpected shape) defaults to a high number so callers
// assume the newest column layout rather than the legacy one.
func parseExtVersion(v string) (int, int) {
	parts := strings.SplitN(strings.TrimSpace(v), ".", 3)
	major, err1 := strconv.Atoi(parts[0])
	if err1 != nil {
		return 999, 0
	}
	minor := 0
	if len(parts) > 1 {
		if m, err2 := strconv.Atoi(parts[1]); err2 == nil {
			minor = m
		}
	}
	return major, minor
}

// TrackPlanning reports whether pg_stat_statements.track_planning is on. It is
// off by default, in which case total_plan_time is always 0 — the Top-queries
// view shows the plan_ms column as "not collected" rather than a misleading 0.
func (c *Client) TrackPlanning(ctx context.Context, db string) (bool, error) {
	c.mu.Lock()
	if c.trackPlanningKnown[db] {
		v := c.trackPlanning[db]
		c.mu.Unlock()
		return v, nil
	}
	c.mu.Unlock()

	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return false, err
	}
	var v string
	if err := pool.QueryRow(ctx,
		"SELECT COALESCE(current_setting('pg_stat_statements.track_planning', true), 'off')",
	).Scan(&v); err != nil {
		return false, fmt.Errorf("read track_planning in %q: %w", db, err)
	}
	c.mu.Lock()
	c.trackPlanning[db] = v == "on"
	c.trackPlanningKnown[db] = true
	c.mu.Unlock()
	return v == "on", nil
}
