package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"

	"pgdu/internal/cli"
	"pgdu/internal/pg"
	"pgdu/internal/prefs"
	"pgdu/internal/tui"
)

// version is overwritten at release time via -ldflags "-X main.version=…"
// (see .goreleaser.yaml). It stays "dev" for plain `go build` / `make build`.
var version = "dev"

func main() { os.Exit(run()) }

// run holds every defer so an early exit still closes the pool and cancels the
// connect timeout; main only turns its result into the process exit code.
func run() int {
	cfg, err := cli.Parse(os.Args[1:])
	if err != nil {
		if errors.Is(err, cli.ErrHelp) {
			return 0
		}
		if errors.Is(err, cli.ErrVersion) {
			fmt.Println("pgdu", version)
			return 0
		}
		fmt.Fprintln(os.Stderr, "pgdu:", err)
		return 2
	}

	client := pg.New(cfg)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.Ping(ctx); err != nil {
		// The pgbouncer tool only needs Postgres for one discovery heuristic, so
		// a pooler-only host (or a down server) must not keep it from opening.
		if cfg.Tool != "pgbouncer" {
			fmt.Fprintln(os.Stderr, "pgdu: connect:", err)
			return 1
		}
		fmt.Fprintln(os.Stderr, "pgdu: warning: postgres unreachable, continuing with the pgbouncer tool only:", err)
	}

	if cfg.Sample {
		return runSample(cfg, client)
	}

	model := tui.NewModel(client, cfg.SnapshotDir, prefs.Load(), cfg.Tool, cfg.LogFile)
	p := tea.NewProgram(model)
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "pgdu:", err)
		return 1
	}
	return 0
}

// sampleTimeout bounds one --sample capture. The connect context above is a
// hard 10 s and CaptureSizes stats every fork of every relation, so a cluster
// with tens of thousands of relations and a cold page cache needs its own, much
// larger budget — the same exemption REINDEX, VACUUM and log loading take from
// the TUI's 30 s query() wrapper.
const sampleTimeout = 10 * time.Minute

// runSample is the cron path of the growth tool: one capture, one file, a
// best-effort prune, no TUI. It returns run's exit code rather than exiting, so
// run's deferred Close still releases the pool.
//
// Only the sample's path goes to stdout, so `path=$(pgdu --sample)` works;
// warnings and the prune count go to stderr. A failed prune does not fail the
// run: the sample is already safely written, and the next invocation retries.
func runSample(cfg cli.Config, client *pg.Client) int {
	ctx, cancel := context.WithTimeout(context.Background(), sampleTimeout)
	defer cancel()

	sample, err := client.CaptureSizes(ctx, cfg.Database, cfg.SampleMinBytes)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pgdu: sample:", err)
		return 1
	}
	path, err := pg.SaveSizeSample(cfg.SnapshotDir, sample)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pgdu: sample:", err)
		return 1
	}
	fmt.Println(path)

	if cfg.SampleRetention > 0 {
		n, err := pg.PruneSizeSamples(cfg.SnapshotDir, sample.Target, sample.Database,
			time.Now(), pg.RetentionFor(cfg.SampleRetention))
		switch {
		case err != nil:
			fmt.Fprintln(os.Stderr, "pgdu: warning: prune:", err)
		case n > 0:
			fmt.Fprintf(os.Stderr, "pgdu: pruned %d old size sample(s)\n", n)
		}
	}
	return 0
}
