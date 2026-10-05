// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cavenine/ara-mcp/internal/diagnostics"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// Config is the validated runtime configuration passed to the adapter.
type Config struct {
	AraURL              string        `mapstructure:"ara-url"`
	Transport           string        `mapstructure:"transport"`
	LogLevel            string        `mapstructure:"log-level"`
	Timeout             time.Duration `mapstructure:"timeout"`
	ReadRetries         int           `mapstructure:"read-retries"`
	HTTPListen          string        `mapstructure:"http-listen"`
	HTTPBearerToken     string        `mapstructure:"http-bearer-token"`
	HTTPOrigins         []string      `mapstructure:"http-origins"`
	HTTPTLSCert         string        `mapstructure:"http-tls-cert"`
	HTTPTLSKey          string        `mapstructure:"http-tls-key"`
	DiagnosticsListen   string        `mapstructure:"diagnostics-listen"`
	DiagnosticsUsername string        `mapstructure:"diagnostics-username"`
	DiagnosticsPassword string        `mapstructure:"diagnostics-password"`
	DiagnosticsTLSCert  string        `mapstructure:"diagnostics-tls-cert"`
	DiagnosticsTLSKey   string        `mapstructure:"diagnostics-tls-key"`
}

// LoadConfig resolves explicit flags, environment, optional file, and defaults.
func LoadConfig(flags *pflag.FlagSet, configFile string) (Config, error) {
	v := viper.New()
	v.SetEnvPrefix("ARA_MCP")
	v.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	v.AutomaticEnv()
	for _, key := range []string{"ara-url", "transport", "log-level", "timeout", "read-retries", "http-listen", "http-bearer-token", "http-origins", "http-tls-cert", "http-tls-key", "diagnostics-listen", "diagnostics-username", "diagnostics-password", "diagnostics-tls-cert", "diagnostics-tls-key"} {
		if err := v.BindEnv(key); err != nil {
			return Config{}, fmt.Errorf("bind %s environment variable: %w", key, err)
		}
	}
	if flags != nil {
		if err := v.BindPFlags(flags); err != nil {
			return Config{}, fmt.Errorf("bind command flags: %w", err)
		}
	}
	v.SetDefault("ara-url", "http://127.0.0.1:5555")
	v.SetDefault("transport", "stdio")
	v.SetDefault("log-level", "info")
	v.SetDefault("timeout", 10*time.Second)
	v.SetDefault("read-retries", 0)
	v.SetDefault("http-listen", "127.0.0.1:8080")
	v.SetDefault("diagnostics-listen", "")
	if configFile != "" {
		v.SetConfigFile(configFile)
		if err := v.ReadInConfig(); err != nil {
			return Config{}, fmt.Errorf("read configuration file: %w", err)
		}
	}
	var config Config
	if err := v.Unmarshal(&config); err != nil {
		return Config{}, fmt.Errorf("decode configuration: %w", err)
	}
	origins := config.HTTPOrigins
	config.HTTPOrigins = nil
	for _, value := range origins {
		for _, origin := range strings.Split(value, ",") {
			if origin = strings.TrimSpace(origin); origin != "" {
				config.HTTPOrigins = append(config.HTTPOrigins, origin)
			}
		}
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

// Validate rejects invalid settings before opening upstream or MCP transports.
func (c Config) Validate() error {
	u, err := url.Parse(c.AraURL)
	if err != nil || !u.IsAbs() || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("Ara URL must be an absolute http(s) URL without credentials, query, or fragment")
	}
	if c.Transport != "stdio" && c.Transport != "http" {
		return fmt.Errorf("transport must be stdio or http")
	}
	if c.LogLevel != "debug" && c.LogLevel != "info" && c.LogLevel != "warn" && c.LogLevel != "error" {
		return fmt.Errorf("log level must be debug, info, warn, or error")
	}
	if c.Timeout <= 0 || c.Timeout > 5*time.Minute {
		return fmt.Errorf("timeout must be greater than zero and at most 5 minutes")
	}
	if c.ReadRetries < 0 || c.ReadRetries > 2 {
		return fmt.Errorf("read retries must be between 0 and 2")
	}
	if (c.HTTPTLSCert == "") != (c.HTTPTLSKey == "") {
		return fmt.Errorf("HTTP TLS certificate and key must be configured together")
	}
	if c.Transport == "http" {
		if c.HTTPBearerToken == "" {
			return fmt.Errorf("HTTP MCP requires a bearer token")
		}
		if len(c.HTTPBearerToken) < 32 {
			return fmt.Errorf("HTTP MCP bearer token must be at least 32 characters")
		}
		if err := validateHTTPListen(c.HTTPListen, c.HTTPTLSCert); err != nil {
			return err
		}
	}
	for _, origin := range c.HTTPOrigins {
		u, err := url.Parse(origin)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("HTTP origins must be absolute http(s) origins without paths or credentials")
		}
	}
	if err := (diagnostics.Access{Listen: c.DiagnosticsListen, Username: c.DiagnosticsUsername, Password: c.DiagnosticsPassword, TLSCert: c.DiagnosticsTLSCert, TLSKey: c.DiagnosticsTLSKey}).Validate(); err != nil {
		return err
	}
	return nil
}

func validateHTTPListen(address, tlsCert string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port == "" {
		return fmt.Errorf("HTTP listen address must be host:port")
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return fmt.Errorf("HTTP listen port must be between 0 and 65535")
	}
	ip := net.ParseIP(host)
	loopback := strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback())
	if !loopback && tlsCert == "" {
		return fmt.Errorf("non-loopback HTTP MCP listener requires TLS")
	}
	return nil
}
