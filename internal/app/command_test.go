// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
)

func TestCommandConfigAndVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	var received Config
	command := NewCommand(CommandOptions{
		Version: "1.2.3",
		Stdout:  &stdout,
		Stderr:  &stderr,
		Serve: func(_ context.Context, config Config) error {
			received = config
			return nil
		},
	})
	command.SetArgs([]string{"serve", "--ara-url=http://127.0.0.1:5556", "--read-retries=0"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if received.AraURL != "http://127.0.0.1:5556" || received.ReadRetries != 0 || received.Transport != "stdio" {
		t.Fatalf("serve config = %+v", received)
	}

	stdout.Reset()
	t.Setenv("ARA_MCP_LOG_LEVEL", "not-a-level")
	command = NewCommand(CommandOptions{Version: "1.2.3", Stdout: &stdout, Stderr: &stderr})
	command.SetArgs([]string{"version"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "ara-mcp 1.2.3\n" {
		t.Fatalf("version output = %q", stdout.String())
	}
}

func TestCommandHelpDoesNotResolveConfig(t *testing.T) {
	t.Setenv("ARA_MCP_LOG_LEVEL", "not-a-level")
	var stdout, stderr bytes.Buffer
	command := NewCommand(CommandOptions{Stdout: &stdout, Stderr: &stderr})
	command.SetArgs([]string{"--help"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte("Usage:")) || !bytes.Contains(stdout.Bytes(), []byte("serve")) {
		t.Fatalf("help output = %q", stdout.String())
	}
}

func TestParseLogLevel(t *testing.T) {
	for _, test := range []struct {
		name string
		text string
		want slog.Level
	}{
		{name: "debug", text: "debug", want: slog.LevelDebug},
		{name: "info", text: "info", want: slog.LevelInfo},
		{name: "warn", text: "warn", want: slog.LevelWarn},
		{name: "error", text: "error", want: slog.LevelError},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := parseLogLevel(test.text); got != test.want {
				t.Fatalf("parseLogLevel(%q) = %v, want %v", test.text, got, test.want)
			}
		})
	}
}
