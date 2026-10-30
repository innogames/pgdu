package pg

import (
	"context"
	"fmt"
	"strconv"
)

// Server-version thresholds, in the server_version_num encoding
// (major*10000 + minor). pgdu's floor is PG 17, so these only ever gate
// features newer than that.
const (
	pgVer18 = 180000
	pgVer19 = 190000
)

// serverVersion returns server_version_num, cached for the session. Unlike the
// per-database extension caches around it, this is one value for the whole
// client: every pool talks to the same server, and the version can only change
// across a restart we would not survive anyway.
//
// Most version differences are handled in SQL instead — a reduced column list,
// or to_jsonb(row) ->> 'col' so a column missing on an older server reads NULL.
// This probe is for the cases those can't cover: a function or view that does
// not exist at all, where the whole statement would fail.
func (c *Client) serverVersion(ctx context.Context, db string) (int, error) {
	c.mu.Lock()
	if c.serverVerKnown {
		v := c.serverVer
		c.mu.Unlock()
		return v, nil
	}
	c.mu.Unlock()

	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return 0, err
	}
	var ver int
	if err := pool.QueryRow(ctx, sqlServerVersionNum).Scan(&ver); err != nil {
		return 0, fmt.Errorf("read server version in %q: %w", db, err)
	}

	c.mu.Lock()
	c.serverVer = ver
	c.serverVerKnown = true
	c.mu.Unlock()
	return ver, nil
}

// serverAtLeast reports whether the server is at least want. A failed probe
// answers false, so a feature gated on it stays off rather than issuing SQL
// that would error — the same degrade-to-absent shape as a missing extension.
func (c *Client) serverAtLeast(ctx context.Context, db string, want int) bool {
	ver, err := c.serverVersion(ctx, db)
	return err == nil && ver >= want
}

// majorVersionString renders a server_version_num as the major version people
// actually say ("18"), with the minor appended only when it is set — a
// threshold is always a bare major, a live server usually is not.
func majorVersionString(ver int) string {
	major, minor := ver/10000, ver%10000
	if minor == 0 {
		return strconv.Itoa(major)
	}
	return strconv.Itoa(major) + "." + strconv.Itoa(minor)
}
