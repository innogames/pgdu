package pg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"pgdu/internal/procfs"
)

// DiskSpace is the capacity of the filesystem holding the server's data
// directory. Postgres exposes no free-space function, so this is a host read:
// it exists only when pgdu runs on the database host itself and can stat the
// data directory (see Client.DiskSpace).
type DiskSpace struct {
	procfs.FS

	// Path is the data directory the figures are about. A tablespace, or a
	// pg_wal symlinked elsewhere, lives on a filesystem this does not cover —
	// which is why every caller presents it as the data directory's disk and
	// not as "the server's".
	Path string
	At   time.Time
}

// UsedPct is how full the filesystem is, counting the root-reserved blocks as
// used — the number df prints.
func (d *DiskSpace) UsedPct() float64 {
	if d == nil || d.Total <= 0 {
		return 0
	}
	return float64(d.Used()) / float64(d.Total) * 100
}

// ErrDiskSpaceUnknown is the error every "we cannot tell from here" path wraps:
// an unprivileged connection, a remote host, a platform without statfs. It is
// a normal outcome, not a failure to report — callers suppress their disk
// figures and say nothing.
var ErrDiskSpaceUnknown = errors.New("disk space not readable from here")

// DiskSpace reports the free space on the filesystem holding the server's data
// directory, or an error wrapping ErrDiskSpaceUnknown when pgdu cannot
// establish that it is looking at the server's own disk.
//
// Two things must hold. The data directory has to be readable from the catalog
// (superuser or pg_read_all_settings), and the path has to be the server's own
// rather than a same-named directory on whatever host pgdu runs on. The strong
// test for the second is stat'ing PG_VERSION on both sides and comparing size
// and mtime; where pg_stat_file is not granted, a loopback or Unix-socket
// connection is accepted instead, since such a connection cannot reach another
// machine.
func (c *Client) DiskSpace(ctx context.Context, db string) (*DiskSpace, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return nil, err
	}
	var dir string
	if err := pool.QueryRow(ctx, sqlDataDirectory).Scan(&dir); err != nil {
		return nil, fmt.Errorf("read data_directory in %q: %w", db, ErrDiskSpaceUnknown)
	}
	if dir == "" {
		return nil, ErrDiskSpaceUnknown
	}

	local, err := os.Stat(filepath.Join(dir, "PG_VERSION"))
	if err != nil {
		return nil, fmt.Errorf("stat %s locally: %w", dir, ErrDiskSpaceUnknown)
	}
	if !c.sameHost(ctx, pool, dir, local) {
		return nil, fmt.Errorf("%s exists here too but is not the server's: %w", dir, ErrDiskSpaceUnknown)
	}

	fs, ok := procfs.Statfs(dir)
	if !ok || fs.Total <= 0 {
		return nil, fmt.Errorf("statfs %s: %w", dir, ErrDiskSpaceUnknown)
	}
	return &DiskSpace{Path: dir, FS: fs, At: time.Now()}, nil
}

// sameHost decides whether the PG_VERSION pgdu just stat'ed locally is the one
// the server reads. Size and mtime to the second are enough: the file is
// written once at initdb and never touched again, so two clusters agreeing on
// both are the same cluster, and a copy that preserved neither would have to
// be a deliberate forgery of a three-byte file.
func (c *Client) sameHost(ctx context.Context, pool *pgxpool.Pool, dir string, local os.FileInfo) bool {
	var size int64
	var mod time.Time
	err := pool.QueryRow(ctx, sqlStatServerFile, filepath.Join(dir, "PG_VERSION")).Scan(&size, &mod)
	if err != nil {
		// pg_stat_file is not granted: fall back to the connection itself.
		return c.cfg.LocalServer()
	}
	return size == local.Size() && mod.Truncate(time.Second).Equal(local.ModTime().Truncate(time.Second))
}
