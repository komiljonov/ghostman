// Package config loads and validates all runtime configuration from environment
// variables. Environment is the only configuration source: no files, no flags.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/caarlos0/env/v11"
)

// Environment names understood by the server.
const (
	EnvDev  = "dev"
	EnvProd = "prod"
)

// Config holds every knob the server understands. Field tags map one-to-one to
// the variables documented in .env.example.
type Config struct {
	// Env selects behaviour that differs between local development and
	// deployment, most visibly the log format ("dev" or "prod").
	Env string `env:"ENV" envDefault:"dev"`

	// Port is the TCP port the HTTP server listens on.
	Port int `env:"PORT" envDefault:"8080"`

	// DatabaseURL is the PostgreSQL connection string used by pgxpool.
	DatabaseURL string `env:"DATABASE_URL,required"`

	// LogLevel is one of debug, info, warn, error.
	LogLevel string `env:"LOG_LEVEL" envDefault:"info"`
}

// Load reads the process environment into a Config and validates it.
func Load() (Config, error) {
	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse environment: %w", err)
	}

	cfg.Env = strings.ToLower(strings.TrimSpace(cfg.Env))
	cfg.LogLevel = strings.ToLower(strings.TrimSpace(cfg.LogLevel))

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func (c Config) validate() error {
	// env's "required" only checks that the variable exists; an exported but
	// empty value has to be caught here.
	if strings.TrimSpace(c.DatabaseURL) == "" {
		return errors.New("DATABASE_URL must not be empty")
	}

	switch c.Env {
	case EnvDev, EnvProd:
	default:
		return fmt.Errorf("invalid ENV %q: want %q or %q", c.Env, EnvDev, EnvProd)
	}

	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("invalid PORT %d: want 1-65535", c.Port)
	}

	if _, err := parseLevel(c.LogLevel); err != nil {
		return err
	}

	return nil
}

// IsProd reports whether the server is running in its production profile.
func (c Config) IsProd() bool { return c.Env == EnvProd }

// Addr is the listen address for the HTTP server.
func (c Config) Addr() string { return fmt.Sprintf(":%d", c.Port) }

// SlogLevel converts LogLevel into a slog level. Validation happens in Load, so
// an unknown value here degrades to info rather than failing.
func (c Config) SlogLevel() slog.Level {
	level, err := parseLevel(c.LogLevel)
	if err != nil {
		return slog.LevelInfo
	}
	return level
}

func parseLevel(name string) (slog.Level, error) {
	switch name {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid LOG_LEVEL %q: want debug, info, warn or error", name)
	}
}
