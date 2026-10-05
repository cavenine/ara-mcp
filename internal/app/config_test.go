// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/spf13/pflag"
)

func TestLoadConfigPrecedenceAndExplicitZero(t *testing.T) {
	t.Setenv("ARA_MCP_ARA_URL", "http://env.example")
	t.Setenv("ARA_MCP_READ_RETRIES", "2")
	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	flags.String("ara-url", "", "")
	flags.String("transport", "", "")
	flags.String("log-level", "", "")
	flags.Duration("timeout", 0, "")
	flags.Int("read-retries", 0, "")
	if err := flags.Parse([]string{"--ara-url=http://flag.example", "--read-retries=0"}); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(flags, "")
	if err != nil {
		t.Fatal(err)
	}
	if config.AraURL != "http://flag.example" || config.ReadRetries != 0 {
		t.Fatalf("explicit flag values lost: %+v", config)
	}
}

func TestLoadConfigEnvironmentFileAndDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("ara-url: http://file.example\ntransport: http\nhttp-bearer-token: 0123456789abcdef0123456789abcdef\nlog-level: warn\ntimeout: 5s\nread-retries: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARA_MCP_ARA_URL", "http://env.example")
	t.Setenv("ARA_MCP_LOG_LEVEL", "debug")
	t.Setenv("ARA_MCP_READ_RETRIES", "2")
	t.Setenv("ARA_MCP_HTTP_ORIGINS", "https://agent.example,https://other.example")
	config, err := LoadConfig(pflag.NewFlagSet("test", pflag.ContinueOnError), path)
	if err != nil {
		t.Fatal(err)
	}
	if config.AraURL != "http://env.example" || config.Transport != "http" || config.HTTPBearerToken != "0123456789abcdef0123456789abcdef" || config.LogLevel != "debug" || config.Timeout.String() != "5s" || config.ReadRetries != 2 || !slices.Equal(config.HTTPOrigins, []string{"https://agent.example", "https://other.example"}) {
		t.Fatalf("environment/file precedence = %+v", config)
	}

	t.Setenv("ARA_MCP_ARA_URL", "")
	t.Setenv("ARA_MCP_LOG_LEVEL", "")
	t.Setenv("ARA_MCP_READ_RETRIES", "")
	defaults, err := LoadConfig(pflag.NewFlagSet("test", pflag.ContinueOnError), "")
	if err != nil {
		t.Fatal(err)
	}
	if defaults.AraURL != "http://127.0.0.1:5555" || defaults.Transport != "stdio" || defaults.LogLevel != "info" || defaults.Timeout.String() != "10s" || defaults.ReadRetries != 0 {
		t.Fatalf("defaults = %+v", defaults)
	}
}

func TestLoadConfigRejectsExplicitMissingFile(t *testing.T) {
	if _, err := LoadConfig(pflag.NewFlagSet("test", pflag.ContinueOnError), filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("missing explicitly selected config file succeeded")
	}
}

func TestLoadConfigRejectsInvalidValues(t *testing.T) {
	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	flags.String("log-level", "", "")
	if err := flags.Parse([]string{"--log-level=trace"}); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(flags, ""); err == nil {
		t.Fatal("invalid log level was accepted")
	}
}

func TestConfigRequiresAuthenticatedHTTPListener(t *testing.T) {
	config := Config{AraURL: "http://127.0.0.1:5555", Transport: "http", LogLevel: "info", Timeout: 10 * time.Second}
	if err := config.Validate(); err == nil {
		t.Fatal("HTTP transport without listener or bearer token was accepted")
	}
	config.HTTPBearerToken = "0123456789abcdef0123456789abcdef"
	config.HTTPListen = "0.0.0.0:8080"
	if err := config.Validate(); err == nil {
		t.Fatal("network-facing HTTP listener without TLS was accepted")
	}
	config.HTTPTLSCert, config.HTTPTLSKey = "cert.pem", "key.pem"
	if err := config.Validate(); err != nil {
		t.Fatalf("authenticated TLS listener rejected: %v", err)
	}
}

func TestLoadConfigDiagnosticsAccess(t *testing.T) {
	tests := []struct {
		name      string
		config    string
		wantError bool
	}{
		{name: "loopback listener", config: "diagnostics-listen: 127.0.0.1:9090\n"},
		{name: "remote requires authentication and TLS", config: "diagnostics-listen: 0.0.0.0:9090\n", wantError: true},
		{name: "remote with authentication and TLS", config: "diagnostics-listen: 0.0.0.0:9090\ndiagnostics-username: operator\ndiagnostics-password: secret\ndiagnostics-tls-cert: cert.pem\ndiagnostics-tls-key: key.pem\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(test.config), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadConfig(pflag.NewFlagSet("test", pflag.ContinueOnError), path)
			if (err != nil) != test.wantError {
				t.Fatalf("LoadConfig error = %v, wantError %t", err, test.wantError)
			}
		})
	}
}
