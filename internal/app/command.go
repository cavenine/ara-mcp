// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

// CommandOptions injects build metadata, output streams, and the runtime entrypoint.
type CommandOptions struct {
	Version string
	Stdout  io.Writer
	Stderr  io.Writer
	Serve   func(context.Context, Config) error
}

// NewCommand creates an isolated Cobra command tree for one execution.
func NewCommand(options CommandOptions) *cobra.Command {
	version := options.Version
	if version == "" {
		version = "dev"
	}
	root := &cobra.Command{
		Use:               "ara-mcp",
		Short:             "MCP adapter for OpenAstro Ara",
		Args:              cobra.NoArgs,
		SilenceErrors:     true,
		SilenceUsage:      true,
		CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
	}
	if options.Stdout != nil {
		root.SetOut(options.Stdout)
	}
	if options.Stderr != nil {
		root.SetErr(options.Stderr)
	}
	flags := root.PersistentFlags()
	flags.String("config", "", "configuration file (optional)")
	flags.String("ara-url", "", "Ara server base URL")
	flags.String("transport", "", "MCP transport: stdio or http")
	flags.String("log-level", "", "log level: debug, info, warn, or error")
	flags.Duration("timeout", 0, "Ara request timeout")
	flags.Int("read-retries", 0, "maximum retries for Ara GET requests")
	flags.String("http-listen", "", "HTTP MCP listen address")
	flags.StringSlice("http-origins", nil, "additional trusted MCP HTTP origins")
	flags.String("http-tls-cert", "", "HTTP MCP TLS certificate file")
	flags.String("http-tls-key", "", "HTTP MCP TLS private key file")
	flags.String("diagnostics-listen", "", "optional diagnostics HTTP listen address")
	flags.String("diagnostics-tls-cert", "", "diagnostics TLS certificate file")
	flags.String("diagnostics-tls-key", "", "diagnostics TLS private key file")

	var config Config
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		if cmd.Name() == "version" {
			return nil
		}
		path, err := cmd.Flags().GetString("config")
		if err != nil {
			return fmt.Errorf("read config flag: %w", err)
		}
		config, err = LoadConfig(cmd.Flags(), path)
		return err
	}
	serve := &cobra.Command{
		Use:   "serve",
		Short: "serve MCP over the configured transport",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if options.Serve == nil {
				return fmt.Errorf("serve runtime is not configured")
			}
			return options.Serve(cmd.Context(), config)
		},
	}
	versionCommand := &cobra.Command{
		Use:   "version",
		Short: "print version information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "ara-mcp %s\n", version)
			return err
		},
	}
	root.AddCommand(serve, versionCommand)
	return root
}
