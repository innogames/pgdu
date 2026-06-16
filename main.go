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

func main() {
	cfg, err := cli.Parse(os.Args[1:])
	if err != nil {
		if errors.Is(err, cli.ErrHelp) {
			os.Exit(0)
		}
		if errors.Is(err, cli.ErrVersion) {
			fmt.Println("pgdu", version)
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "pgdu:", err)
		os.Exit(2)
	}

	client := pg.New(cfg)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.Ping(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "pgdu: connect:", err)
		os.Exit(1)
	}

	model := tui.NewModel(client, cfg.QueriesRefresh, cfg.SnapshotDir, prefs.Load(), cfg.Tool)
	p := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "pgdu:", err)
		os.Exit(1)
	}
}
