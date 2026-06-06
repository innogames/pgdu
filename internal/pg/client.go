package pg

import (
	"context"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"pgdu/internal/cli"
)

// Client owns one pgxpool.Pool per database, opened lazily as the user
// drills into different databases. All pools are closed by Close.
type Client struct {
	cfg cli.Config

	mu    sync.Mutex
	pools map[string]*pgxpool.Pool

	// BloatMode is set on first call to ProbeBloat and cached thereafter.
	bloatProbed map[string]BloatMode

	// True once pg_buffercache is known to be installed in a given database.
	bufCacheReady map[string]bool

	// True once pageinspect is known to be installed in a given database.
	pageInspectReady map[string]bool

	// True once pg_walinspect is known to be installed in a given database.
	walInspectReady map[string]bool

	// True once pg_stat_statements is known to be installed in a given database.
	statStatementsReady map[string]bool

	// True once pg_qualstats is known to be installed in a given database. Used
	// to source real EXPLAIN parameters (real captured constants) for the
	// Top-queries view, falling back to synthesized literals when absent.
	qualstatsReady map[string]bool

	// Cached pg_stat_statements.track_planning per database (it only changes on
	// a config reload, so one read per session is enough — and avoids the
	// per-refresh query showing up as noise on unprivileged connections).
	trackPlanning      map[string]bool
	trackPlanningKnown map[string]bool

	// Cached pg_stat_statements extension version [major, minor] per database. It
	// selects the right I/O-timing column names (the 1.11 rename), so it must be
	// known before the first snapshot query runs.
	statStatementsVer      map[string][2]int
	statStatementsVerKnown map[string]bool
}

type BloatMode int

const (
	BloatUnknown  BloatMode = iota
	BloatExact              // pgstattuple_approx available
	BloatEstimate           // statistics-only fallback
)

func New(cfg cli.Config) *Client {
	return &Client{
		cfg:                    cfg,
		pools:                  map[string]*pgxpool.Pool{},
		bloatProbed:            map[string]BloatMode{},
		bufCacheReady:          map[string]bool{},
		pageInspectReady:       map[string]bool{},
		walInspectReady:        map[string]bool{},
		statStatementsReady:    map[string]bool{},
		qualstatsReady:         map[string]bool{},
		trackPlanning:          map[string]bool{},
		trackPlanningKnown:     map[string]bool{},
		statStatementsVer:      map[string][2]int{},
		statStatementsVerKnown: map[string]bool{},
	}
}

// PoolFor returns (or creates) a pool against the named database.
func (c *Client) PoolFor(ctx context.Context, db string) (*pgxpool.Pool, error) {
	if db == "" {
		db = c.cfg.Database
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if p, ok := c.pools[db]; ok {
		return p, nil
	}
	dsn := c.cfg.BuildDSN(db)
	pcfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn for %q: %w", db, err)
	}
	pcfg.MaxConns = 4
	pcfg.MinConns = 0
	// Make pgdu invisible to pg_stat_statements: without this, pgdu's own
	// polling (the 2 s snapshot) and EXPLAIN queries get recorded and show up
	// in — and pollute — its own Top-queries tool. The GUC is superuser-only,
	// so this is best-effort; the snapshot query also filters pgdu's footprints
	// out as a fallback for unprivileged connections.
	pcfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, _ = conn.Exec(ctx, "SET pg_stat_statements.track = 'none'")
		return nil
	}
	p, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, fmt.Errorf("connect %q: %w", db, err)
	}
	c.pools[db] = p
	return p, nil
}

// Ping opens the default-DB pool and verifies connectivity, so connection
// problems surface before the TUI takes over the screen.
func (c *Client) Ping(ctx context.Context) error {
	p, err := c.PoolFor(ctx, c.cfg.Database)
	if err != nil {
		return err
	}
	return p.Ping(ctx)
}

func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, p := range c.pools {
		p.Close()
	}
	c.pools = nil
}

// DefaultDB is the initial database the user pointed pgdu at.
func (c *Client) DefaultDB() string { return c.cfg.Database }
func (c *Client) Target() string    { return c.cfg.Target() }

// ensureExtension verifies that ext is installed in db, caching a positive
// result in ready so the probe runs at most once per database. A missing
// extension is reported as *MissingExtensionError so the TUI can offer an
// interactive install instead of failing with an opaque error. Auto-running
// CREATE EXTENSION here would silently mask permission problems, so we don't.
func (c *Client) ensureExtension(ctx context.Context, db, ext string, ready map[string]bool) error {
	c.mu.Lock()
	if ready[db] {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	st, err := c.ProbeExtension(ctx, db, ext)
	if err != nil {
		return err
	}
	if !st.Installed {
		return &MissingExtensionError{Extension: ext, DB: db, Installable: st.Available}
	}
	c.mu.Lock()
	ready[db] = true
	c.mu.Unlock()
	return nil
}
