package pg

const (
	// sqlDataDirectory reads the cluster's data directory. It goes through
	// pg_settings rather than SHOW because a GUC flagged superuser-only is
	// simply absent from that view for an unprivileged role — no row instead
	// of an error, which is what "we cannot tell" should cost.
	sqlDataDirectory = `SELECT setting FROM pg_settings WHERE name = 'data_directory'`

	// sqlStatServerFile stats one file on the server's own filesystem. pgdu
	// uses it on PG_VERSION only, to prove that a path it can also stat
	// locally is the *same* file — i.e. that pgdu is running on the database
	// host. Needs superuser or pg_read_server_files; failure leaves the
	// weaker loopback test (cli.Config.LocalServer) to decide.
	sqlStatServerFile = `SELECT size, modification FROM pg_stat_file($1)`
)
