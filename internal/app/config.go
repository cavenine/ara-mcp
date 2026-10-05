// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// Config is the validated runtime configuration passed to the adapter.
type Config struct {
	AraURL      string        `mapstructure:"ara-url"`
	Transport   string        `mapstructure:"transport"`
	LogLevel    string        `mapstructure:"log-level"`
	Timeout     time.Duration `mapstructure:"timeout"`
	ReadRetries int           `mapstructure:"read-retries"`
}

// LoadConfig resolves explicit flags, environment, optional file, and defaults.
func LoadConfig(flags *pflag.FlagSet, configFile string) (Config, error) {
	v := viper.New()
	v.SetEnvPrefix("ARA_MCP")
	v.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	v.AutomaticEnv()
	for _, key := range []string{"ara-url", "transport", "log-level", "timeout", "read-retries"} {
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
	return nil
}
