package pg

import (
	"context"
	"fmt"
	"sync"

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
}

type BloatMode int

const (
	BloatUnknown  BloatMode = iota
	BloatExact              // pgstattuple_approx available
	BloatEstimate           // statistics-only fallback
)

func New(cfg cli.Config) *Client {
	return &Client{
		cfg:           cfg,
		pools:         map[string]*pgxpool.Pool{},
		bloatProbed:   map[string]BloatMode{},
		bufCacheReady: map[string]bool{},
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
