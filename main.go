package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

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

	model := tui.NewModel(client, cfg.QueriesRefresh, cfg.SnapshotDir, prefs.Load(), cfg.Tool, cfg.LogFile)
	p := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "pgdu:", err)
		return 1
	}
	return 0
}
