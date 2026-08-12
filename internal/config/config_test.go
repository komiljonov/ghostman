package config

import (
	"log/slog"
	"os"
	"strings"
	"testing"
)

const testDatabaseURL = "postgres://user:pass@localhost:5432/ghostman?sslmode=disable"

// configVars is every variable Load reads.
var configVars = []string{"DATABASE_URL", "ENV", "PORT", "LOG_LEVEL"}

// clearEnv removes the config variables for the duration of the test, so the
// results do not depend on the caller's environment. `task test` loads .env, so
// without this the tests would see the developer's local values.
func clearEnv(t *testing.T) {
	t.Helper()

	for _, key := range configVars {
		// t.Setenv registers the restore-on-cleanup; Unsetenv then makes the
		// variable genuinely absent, which t.Setenv alone cannot express.
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("unsetting %s: %v", key, err)
		}
	}
}

func TestLoadDefaults(t *testing.T) {
	clearEnv(t)
	t.Setenv("DATABASE_URL", testDatabaseURL)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Env != EnvDev {
		t.Errorf("Env = %q, want %q", cfg.Env, EnvDev)
	}

	if cfg.Port != 8080 {
		t.Errorf("Port = %d, want 8080", cfg.Port)
	}

	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "info")
	}

	if cfg.Addr() != ":8080" {
		t.Errorf("Addr() = %q, want %q", cfg.Addr(), ":8080")
	}

	if cfg.IsProd() {
		t.Error("IsProd() = true, want false")
	}
}

func TestLoadProd(t *testing.T) {
	clearEnv(t)
	t.Setenv("DATABASE_URL", testDatabaseURL)
	t.Setenv("ENV", "PROD") // case and surrounding space are normalised
	t.Setenv("LOG_LEVEL", " warn ")
	t.Setenv("PORT", "9000")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if !cfg.IsProd() {
		t.Error("IsProd() = false, want true")
	}

	if cfg.SlogLevel() != slog.LevelWarn {
		t.Errorf("SlogLevel() = %v, want %v", cfg.SlogLevel(), slog.LevelWarn)
	}

	if cfg.Addr() != ":9000" {
		t.Errorf("Addr() = %q, want %q", cfg.Addr(), ":9000")
	}
}

func TestLoadInvalid(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{
			name:    "missing database url",
			env:     map[string]string{},
			wantErr: "DATABASE_URL",
		},
		{
			name:    "unknown env",
			env:     map[string]string{"DATABASE_URL": testDatabaseURL, "ENV": "staging"},
			wantErr: "invalid ENV",
		},
		{
			name:    "port out of range",
			env:     map[string]string{"DATABASE_URL": testDatabaseURL, "PORT": "70000"},
			wantErr: "invalid PORT",
		},
		{
			name:    "unknown log level",
			env:     map[string]string{"DATABASE_URL": testDatabaseURL, "LOG_LEVEL": "verbose"},
			wantErr: "invalid LOG_LEVEL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			_, err := Load()
			if err == nil {
				t.Fatalf("Load() = nil, want error containing %q", tt.wantErr)
			}

			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Load() error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}
