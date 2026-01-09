package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds all configuration for pguard
type Config struct {
	Connection ConnectionConfig `yaml:"connection"`
	Thresholds ThresholdsConfig `yaml:"thresholds"`
	Polling    PollingConfig    `yaml:"polling"`
	Alerts     AlertsConfig     `yaml:"alerts"`
	AutoTerm   AutoTermConfig   `yaml:"auto_terminate"`
	API        APIConfig        `yaml:"api"`
	Logging    LoggingConfig    `yaml:"logging"`
}

type ConnectionConfig struct {
	// Connection string (alternative to individual fields)
	URL string `yaml:"url"`

	// Individual connection parameters
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Database string `yaml:"database"`
	User     string `yaml:"user"`

	// Authentication
	AuthMethod     string `yaml:"auth_method"` // "password", "iam", "secrets_manager", "parameter_store", "env"
	Password       string `yaml:"password"`
	PasswordSecret string `yaml:"password_secret"` // ARN or parameter name
	PasswordEnv    string `yaml:"password_env"`    // Environment variable name

	// AWS settings (for IAM auth)
	AWSRegion string `yaml:"aws_region"`

	// SSL
	SSLMode string `yaml:"sslmode"`

	// Timeouts
	ConnectTimeout time.Duration `yaml:"connect_timeout"`
}

type ThresholdsConfig struct {
	IdleTransaction IdleTransactionThresholds `yaml:"idle_transaction"`
	ConnectionPool  ConnectionPoolThresholds  `yaml:"connection_pool"`
}

type IdleTransactionThresholds struct {
	Warning  time.Duration `yaml:"warning"`
	Critical time.Duration `yaml:"critical"`
}

type ConnectionPoolThresholds struct {
	WarningPercent  int `yaml:"warning_percent"`
	CriticalPercent int `yaml:"critical_percent"`
}

type PollingConfig struct {
	Interval time.Duration `yaml:"interval"`
	Timeout  time.Duration `yaml:"timeout"`
}

type AlertsConfig struct {
	Cooldown time.Duration `yaml:"cooldown"`
	Slack    SlackConfig   `yaml:"slack"`
	Webhook  WebhookConfig `yaml:"webhook"`
}

type WebhookConfig struct {
	Enabled  bool              `yaml:"enabled"`
	URL      string            `yaml:"url"`
	Method   string            `yaml:"method"` // POST (default) or GET
	Headers  map[string]string `yaml:"headers"`
	Template string            `yaml:"template"` // Optional custom JSON template
}

type SlackConfig struct {
	Enabled       bool     `yaml:"enabled"`
	WebhookURL    string   `yaml:"webhook_url"`
	WebhookSecret string   `yaml:"webhook_secret"` // ARN for secrets manager
	Channel       string   `yaml:"channel"`
	MentionUsers  []string `yaml:"mention_users"`
}

type AutoTermConfig struct {
	Enabled       bool           `yaml:"enabled"`
	After         time.Duration  `yaml:"after"`
	DryRun        bool           `yaml:"dry_run"`
	ExcludeApps   []string       `yaml:"exclude_apps"`
	ExcludeIPs    []string       `yaml:"exclude_ips"`
	ProtectedApps []ProtectedApp `yaml:"protected_apps"`
}

type ProtectedApp struct {
	Name                string        `yaml:"name"`
	MinIdleDuration     time.Duration `yaml:"min_idle_duration"`
	RequireConfirmation bool          `yaml:"require_confirmation"`
}

type APIConfig struct {
	Enabled bool   `yaml:"enabled"`
	Listen  string `yaml:"listen"`
}

type LoggingConfig struct {
	Level  string `yaml:"level"`  // debug, info, warn, error
	Format string `yaml:"format"` // json, text
	Output string `yaml:"output"` // stderr, stdout, file path
}

// DefaultConfig returns a config with sensible defaults
func DefaultConfig() *Config {
	return &Config{
		Connection: ConnectionConfig{
			Port:           5432,
			SSLMode:        "prefer",
			ConnectTimeout: 10 * time.Second,
			AuthMethod:     "password",
		},
		Thresholds: ThresholdsConfig{
			IdleTransaction: IdleTransactionThresholds{
				Warning:  30 * time.Second,
				Critical: 2 * time.Minute,
			},
			ConnectionPool: ConnectionPoolThresholds{
				WarningPercent:  75,
				CriticalPercent: 90,
			},
		},
		Polling: PollingConfig{
			Interval: 5 * time.Second,
			Timeout:  5 * time.Second,
		},
		Alerts: AlertsConfig{
			Cooldown: 5 * time.Minute,
		},
		AutoTerm: AutoTermConfig{
			Enabled:     false,
			After:       5 * time.Minute,
			DryRun:      true,
			ExcludeApps: []string{"pguard", "pg_dump"},
		},
		API: APIConfig{
			Enabled: false,
			Listen:  "127.0.0.1:9182",
		},
		Logging: LoggingConfig{
			Level:  "info",
			Format: "text",
			Output: "stderr",
		},
	}
}

// Dir returns the directory where config files are stored
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "pguard"), nil
}

// Path returns the default config file path
func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.yaml"), nil
}

// Load reads config from the given path
func Load(path string) (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	// Expand environment variables in certain fields
	cfg.expandEnvVars()

	return cfg, nil
}

// LoadOrDefault attempts to load config from default path, returns default config if not found
func LoadOrDefault() (*Config, error) {
	path, err := Path()
	if err != nil {
		return DefaultConfig(), nil
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		return DefaultConfig(), nil
	}

	return Load(path)
}

// Save writes config to the given path
func (c *Config) Save(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("writing config file: %w", err)
	}

	return nil
}

// expandEnvVars expands environment variables in config values
func (c *Config) expandEnvVars() {
	c.Connection.URL = os.ExpandEnv(c.Connection.URL)
	c.Connection.Host = os.ExpandEnv(c.Connection.Host)
	c.Connection.Password = os.ExpandEnv(c.Connection.Password)
	c.Connection.User = os.ExpandEnv(c.Connection.User)
	c.Connection.Database = os.ExpandEnv(c.Connection.Database)
	c.Alerts.Slack.WebhookURL = os.ExpandEnv(c.Alerts.Slack.WebhookURL)
	c.Alerts.Webhook.URL = os.ExpandEnv(c.Alerts.Webhook.URL)
}

// ConnectionString builds a PostgreSQL connection string from config
func (c *Config) ConnectionString() string {
	if c.Connection.URL != "" {
		return c.Connection.URL
	}

	// Check for DATABASE_URL environment variable
	if url := os.Getenv("DATABASE_URL"); url != "" {
		return url
	}

	// Build connection string from individual parameters
	return fmt.Sprintf(
		"host=%s port=%d dbname=%s user=%s password=%s sslmode=%s connect_timeout=%d",
		c.Connection.Host,
		c.Connection.Port,
		c.Connection.Database,
		c.Connection.User,
		c.Connection.Password,
		c.Connection.SSLMode,
		int(c.Connection.ConnectTimeout.Seconds()),
	)
}

// Validate checks if the configuration is valid
func (c *Config) Validate() error {
	if c.Connection.URL == "" && c.Connection.Host == "" {
		if os.Getenv("DATABASE_URL") == "" {
			return fmt.Errorf("no database connection configured: set connection.url, connection.host, or DATABASE_URL")
		}
	}

	if c.Thresholds.IdleTransaction.Warning >= c.Thresholds.IdleTransaction.Critical {
		return fmt.Errorf("idle_transaction.warning must be less than critical")
	}

	if c.Thresholds.ConnectionPool.WarningPercent >= c.Thresholds.ConnectionPool.CriticalPercent {
		return fmt.Errorf("connection_pool.warning_percent must be less than critical_percent")
	}

	return nil
}
