package pg

import (
	"context"
	"net"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"pgdu/internal/pgbouncer"
)

// DiscoverPgBouncers is pgbouncer.Client.Discover plus the one source only pg
// can judge: whether pgdu's own connection evidently terminates at a pooler.
func (c *Client) DiscoverPgBouncers(ctx context.Context) []pgbouncer.Instance {
	viaPooler := false
	if pool, err := c.PoolFor(ctx, c.DefaultDB()); err == nil {
		viaPooler = behindProxy(ctx, pool)
	}
	return c.PgBouncer.Discover(viaPooler)
}

// behindProxy reports whether the connections in pool terminate somewhere
// other than the Postgres backend — i.e. a pooler sits in between. It compares
// the peer address of our own socket with the address the backend sees itself
// on (inet_server_addr/port): a direct connection has both equal (TCP) or both
// unix (NULL server side). Any mismatch means a proxy. Errors are treated as
// "direct" so discovery stays silent when in doubt — dialing dbname=pgbouncer
// against a real server logs `FATAL: database "pgbouncer" does not exist`,
// noise in exactly the log the analyzer reads.
func behindProxy(ctx context.Context, pool *pgxpool.Pool) bool {
	pc, err := pool.Acquire(ctx)
	if err != nil {
		return false
	}
	defer pc.Release()

	var srvAddr, srvPort *string
	if err := pc.QueryRow(ctx,
		"SELECT host(inet_server_addr()), inet_server_port()::text").Scan(&srvAddr, &srvPort); err != nil {
		return false
	}
	remote := pc.Conn().PgConn().Conn().RemoteAddr()
	tcp, isTCP := remote.(*net.TCPAddr)
	if srvAddr == nil || srvPort == nil {
		// Backend accepted us on a unix socket. Direct if we opened one too;
		// a TCP client socket ending on a unix backend socket is a proxy.
		return isTCP
	}
	if !isTCP {
		return true
	}
	srvIP := net.ParseIP(*srvAddr)
	return srvIP == nil || !srvIP.Equal(tcp.IP) || *srvPort != strconv.Itoa(tcp.Port)
}
