package pg

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// pgBouncerInfo attempts a best-effort connection to the pgbouncer admin
// console (virtual database "pgbouncer" at the same host/port) and reads
// SHOW VERSION, SHOW POOLS, and SHOW STATS_TOTALS. Returns nil when pgbouncer
// is absent, unreachable, or auth fails. The result is cached-absent after the
// first miss so subsequent refreshes don't pay the dial cost.
//
// pgbouncer's admin console requires the simple query protocol and rejects the
// "SET pg_stat_statements.track = 'none'" sent by the normal pool AfterConnect
// hook — so we open a raw pgx.Conn instead of using PoolFor.
func (c *Client) pgBouncerInfo(ctx context.Context) *PgBouncerInfo {
	c.mu.Lock()
	if c.pgbProbed && c.pgbAbsent {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	info := c.tryPgBouncer(ctx)

	c.mu.Lock()
	c.pgbProbed = true
	if info == nil {
		c.pgbAbsent = true
	}
	c.mu.Unlock()
	return info
}

func (c *Client) tryPgBouncer(ctx context.Context) *PgBouncerInfo {
	dialCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	dsn := c.cfg.BuildDSN("pgbouncer")
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil
	}
	// pgbouncer admin console only speaks the simple query protocol.
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol

	conn, err := pgx.ConnectConfig(dialCtx, cfg)
	if err != nil {
		return nil
	}
	defer func() { _ = conn.Close(ctx) }()

	// Sanity check: make sure we're actually talking to pgbouncer, not a real
	// Postgres database that happens to be named "pgbouncer".
	var version string
	if err := conn.QueryRow(ctx, "SHOW VERSION").Scan(&version); err != nil {
		return nil
	}
	if !strings.Contains(version, "PgBouncer") {
		return nil
	}

	info := &PgBouncerInfo{Version: version}

	// SHOW POOLS — one row per (database, user, pool_mode) combination.
	poolRows, err := conn.Query(ctx, "SHOW POOLS")
	if err == nil {
		colIdx := colIndexMap(poolRows.FieldDescriptions())
		for poolRows.Next() {
			vals, err := rowToStrings(poolRows)
			if err != nil {
				continue
			}
			p := PgbPool{
				Database:   strAt(vals, colIdx, "database"),
				User:       strAt(vals, colIdx, "user"),
				Mode:       strAt(vals, colIdx, "pool_mode"),
				ClActive:   intAt(vals, colIdx, "cl_active"),
				ClWaiting:  intAt(vals, colIdx, "cl_waiting"),
				SvActive:   intAt(vals, colIdx, "sv_active"),
				SvIdle:     intAt(vals, colIdx, "sv_idle"),
				MaxWaitSec: floatAt(vals, colIdx, "maxwait") + floatAt(vals, colIdx, "maxwait_us")/1e6,
			}
			// Skip the pgbouncer meta-database itself from the pool list.
			if p.Database == "pgbouncer" {
				continue
			}
			info.Pools = append(info.Pools, p)
			info.ClActive += p.ClActive
			info.ClWaiting += p.ClWaiting
			info.SvActive += p.SvActive
			info.SvIdle += p.SvIdle
			if p.MaxWaitSec > info.MaxWaitSec {
				info.MaxWaitSec = p.MaxWaitSec
			}
		}
		poolRows.Close()
	}

	return info
}

// colIndexMap builds a name→index map from pgconn FieldDescriptions so we can
// access SHOW output columns by name regardless of their position.
func colIndexMap(fds []pgconn.FieldDescription) map[string]int {
	m := make(map[string]int, len(fds))
	for i, fd := range fds {
		m[fd.Name] = i
	}
	return m
}

// rowToStrings scans the current pgx row into a []string (one element per column).
func rowToStrings(rows pgx.Rows) ([]string, error) {
	vals, err := rows.Values()
	if err != nil {
		return nil, err
	}
	out := make([]string, len(vals))
	for i, v := range vals {
		if v == nil {
			continue
		}
		out[i] = strings.TrimSpace(strings.Trim(strconv.FormatFloat(toFloat64(v), 'f', -1, 64), ".0"))
		// For non-numeric types, fall back to fmt.Sprint equivalent via type assertion.
		switch t := v.(type) {
		case string:
			out[i] = t
		case int64:
			out[i] = strconv.FormatInt(t, 10)
		case int32:
			out[i] = strconv.FormatInt(int64(t), 10)
		case float64:
			out[i] = strconv.FormatFloat(t, 'f', -1, 64)
		case float32:
			out[i] = strconv.FormatFloat(float64(t), 'f', -1, 64)
		}
	}
	return out, nil
}

func toFloat64(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case float32:
		return float64(t)
	case int64:
		return float64(t)
	case int32:
		return float64(t)
	}
	return 0
}

func strAt(vals []string, idx map[string]int, col string) string {
	i, ok := idx[col]
	if !ok || i >= len(vals) {
		return ""
	}
	return vals[i]
}

func intAt(vals []string, idx map[string]int, col string) int {
	n, _ := strconv.Atoi(strAt(vals, idx, col))
	return n
}

func floatAt(vals []string, idx map[string]int, col string) float64 {
	f, _ := strconv.ParseFloat(strAt(vals, idx, col), 64)
	return f
}
