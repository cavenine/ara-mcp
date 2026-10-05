// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/cavenine/ara-mcp/internal/app"
)

var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	command := app.NewCommand(app.CommandOptions{
		Version: version + " (" + commit + ")",
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Serve: func(ctx context.Context, config app.Config) error {
			return app.Serve(ctx, config, version, os.Stderr)
		},
	})
	if err := command.ExecuteContext(ctx); err != nil {
		logger.Error("command failed", "service", "ara-mcp", "version", version, "component", "process", "event", "command_failed", "error", err.Error())
		os.Exit(1)
	}
}
