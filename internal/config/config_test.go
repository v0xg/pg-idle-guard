package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Connection.Port != 5432 {
		t.Errorf("expected default port 5432, got %d", cfg.Connection.Port)
	}

	if cfg.Thresholds.IdleTransaction.Warning != 30*time.Second {
		t.Errorf("expected warning threshold 30s, got %s", cfg.Thresholds.IdleTransaction.Warning)
	}

	if cfg.Thresholds.IdleTransaction.Critical != 2*time.Minute {
		t.Errorf("expected critical threshold 2m, got %s", cfg.Thresholds.IdleTransaction.Critical)
	}
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		modify  func(*Config)
		wantErr bool
	}{
		{
			name: "valid config with host",
			modify: func(c *Config) {
				c.Connection.Host = "localhost"
			},
			wantErr: false,
		},
		{
			name: "valid config with DATABASE_URL",
			modify: func(c *Config) {
				os.Setenv("DATABASE_URL", "postgres://localhost/test")
			},
			wantErr: false,
		},
		{
			name: "invalid: no connection",
			modify: func(c *Config) {
				c.Connection.Host = ""
				c.Connection.URL = ""
				os.Unsetenv("DATABASE_URL")
			},
			wantErr: true,
		},
		{
			name: "invalid: warning >= critical",
			modify: func(c *Config) {
				c.Connection.Host = "localhost"
				c.Thresholds.IdleTransaction.Warning = 5 * time.Minute
				c.Thresholds.IdleTransaction.Critical = 2 * time.Minute
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tt.modify(cfg)
			err := cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestConfigSaveLoad(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "pg-idle-guard-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	configPath := filepath.Join(tmpDir, "config.yaml")

	// Create and save config
	cfg := DefaultConfig()
	cfg.Connection.Host = "testhost"
	cfg.Connection.Database = "testdb"

	if saveErr := cfg.Save(configPath); saveErr != nil {
		t.Fatalf("Save() error = %v", saveErr)
	}

	// Load it back
	loaded, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if loaded.Connection.Host != "testhost" {
		t.Errorf("expected host 'testhost', got '%s'", loaded.Connection.Host)
	}

	if loaded.Connection.Database != "testdb" {
		t.Errorf("expected database 'testdb', got '%s'", loaded.Connection.Database)
	}
}

func TestConnectionString(t *testing.T) {
	tests := []struct {
		name string
		cfg  *Config
		want string
	}{
		{
			name: "with URL",
			cfg: &Config{
				Connection: ConnectionConfig{
					URL: "postgres://user:pass@host/db",
				},
			},
			want: "postgres://user:pass@host/db",
		},
		{
			name: "with individual params",
			cfg: &Config{
				Connection: ConnectionConfig{
					Host:           "localhost",
					Port:           5432,
					Database:       "mydb",
					User:           "myuser",
					Password:       "mypass",
					SSLMode:        "disable",
					ConnectTimeout: 10 * time.Second,
				},
			},
			want: "host=localhost port=5432 dbname=mydb user=myuser password=mypass sslmode=disable connect_timeout=10",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear DATABASE_URL to test individual params
			os.Unsetenv("DATABASE_URL")

			got := tt.cfg.ConnectionString()
			if got != tt.want {
				t.Errorf("ConnectionString() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExpandEnvVars(t *testing.T) {
	os.Setenv("TEST_PG_HOST", "envhost")
	os.Setenv("TEST_PG_PASS", "envpass")
	defer os.Unsetenv("TEST_PG_HOST")
	defer os.Unsetenv("TEST_PG_PASS")

	cfg := DefaultConfig()
	cfg.Connection.Host = "${TEST_PG_HOST}"
	cfg.Connection.Password = "${TEST_PG_PASS}"
	cfg.expandEnvVars()

	if cfg.Connection.Host != "envhost" {
		t.Errorf("expected host 'envhost', got '%s'", cfg.Connection.Host)
	}

	if cfg.Connection.Password != "envpass" {
		t.Errorf("expected password 'envpass', got '%s'", cfg.Connection.Password)
	}
}

func TestConfigDir(t *testing.T) {
	dir, err := Dir()
	if err != nil {
		t.Fatalf("Dir() error = %v", err)
	}

	// Should end with .config/pguard
	if !strings.HasSuffix(dir, filepath.Join(".config", "pguard")) {
		t.Errorf("Dir() = %q, want suffix .config/pguard", dir)
	}

	// Should start with home directory
	home, _ := os.UserHomeDir()
	if !strings.HasPrefix(dir, home) {
		t.Errorf("Dir() = %q, want prefix %q", dir, home)
	}
}

func TestConfigPath(t *testing.T) {
	path, err := Path()
	if err != nil {
		t.Fatalf("Path() error = %v", err)
	}

	// Should end with config.yaml
	if !strings.HasSuffix(path, "config.yaml") {
		t.Errorf("Path() = %q, want suffix config.yaml", path)
	}

	// Should contain the config dir
	dir, _ := Dir()
	if !strings.HasPrefix(path, dir) {
		t.Errorf("Path() = %q, want prefix %q", path, dir)
	}
}

func TestLoadOrDefault_NoFile(t *testing.T) {
	// Test that LoadOrDefault returns default config when no file exists
	// Set HOME to a temp directory to ensure no config file exists
	tmpDir, err := os.MkdirTemp("", "pg-idle-guard-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	cfg, err := LoadOrDefault()
	if err != nil {
		t.Fatalf("LoadOrDefault() error = %v", err)
	}

	// Should have default values
	if cfg.Thresholds.IdleTransaction.Warning != 30*time.Second {
		t.Errorf("expected default warning threshold 30s, got %v", cfg.Thresholds.IdleTransaction.Warning)
	}
	if cfg.Thresholds.IdleTransaction.Critical != 2*time.Minute {
		t.Errorf("expected default critical threshold 2m, got %v", cfg.Thresholds.IdleTransaction.Critical)
	}
}
