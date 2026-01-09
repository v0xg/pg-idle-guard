package postgres

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/v0xg/pg-idle-guard/internal/config"
	"github.com/v0xg/pg-idle-guard/internal/secrets"
)

// Client wraps a PostgreSQL connection pool
type Client struct {
	pool *pgxpool.Pool
	cfg  *config.Config
}

// NewClient creates a new PostgreSQL client
func NewClient(cfg *config.Config) (*Client, error) {
	connString, err := buildConnectionString(cfg)
	if err != nil {
		return nil, fmt.Errorf("building connection string: %w", err)
	}

	poolCfg, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, fmt.Errorf("parsing connection string: %w", err)
	}

	// Set a small pool size - we only need a few connections for monitoring
	poolCfg.MaxConns = 3
	poolCfg.MinConns = 1

	// Set application name so we can identify ourselves
	poolCfg.ConnConfig.RuntimeParams["application_name"] = "pguard"

	// For IAM auth, we need to refresh the token before each connection
	if cfg.Connection.AuthMethod == "iam" {
		poolCfg.BeforeConnect = func(ctx context.Context, connCfg *pgx.ConnConfig) error {
			token, tokenErr := GetRDSAuthToken(
				ctx,
				cfg.Connection.Host,
				cfg.Connection.Port,
				cfg.Connection.User,
				cfg.Connection.AWSRegion,
			)
			if tokenErr != nil {
				return fmt.Errorf("getting IAM auth token: %w", tokenErr)
			}
			connCfg.Password = token
			return nil
		}
	}

	// Use connection timeout from config for pool creation
	timeout := cfg.Connection.ConnectTimeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("creating connection pool: %w", err)
	}

	// Test the connection
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connecting to database: %w", err)
	}

	return &Client{pool: pool, cfg: cfg}, nil
}

// buildConnectionString creates a connection string based on config
func buildConnectionString(cfg *config.Config) (string, error) {
	// If URL is provided directly, use it
	if cfg.Connection.URL != "" {
		return cfg.Connection.URL, nil
	}

	// Resolve password based on auth method
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	password, err := secrets.ResolvePassword(
		ctx,
		cfg.Connection.AuthMethod,
		cfg.Connection.Password,
		cfg.Connection.PasswordSecret,
		cfg.Connection.PasswordEnv,
		cfg.Connection.AWSRegion,
	)
	if err != nil {
		return "", fmt.Errorf("resolving password: %w", err)
	}

	connStr := fmt.Sprintf(
		"host=%s port=%d dbname=%s user=%s sslmode=%s connect_timeout=%d",
		cfg.Connection.Host,
		cfg.Connection.Port,
		cfg.Connection.Database,
		cfg.Connection.User,
		cfg.Connection.SSLMode,
		int(cfg.Connection.ConnectTimeout.Seconds()),
	)

	// Only add password if not using IAM auth
	// URL-encode the password to handle special characters
	if password != "" {
		connStr += fmt.Sprintf(" password=%s", url.QueryEscape(password))
	}

	return connStr, nil
}

// Close closes the connection pool
func (c *Client) Close() {
	c.pool.Close()
}

// Ping tests the database connection
func (c *Client) Ping(ctx context.Context) error {
	return c.pool.Ping(ctx)
}

// GetConnections returns all current connections from pg_stat_activity
func (c *Client) GetConnections(ctx context.Context) ([]*Connection, error) {
	query := `
		SELECT
			pid,
			COALESCE(usename, '') as usename,
			COALESCE(application_name, '') as application_name,
			COALESCE(client_addr::text, 'local') as client_addr,
			COALESCE(client_port, 0) as client_port,
			backend_start,
			xact_start,
			query_start,
			state_change,
			COALESCE(state, 'unknown') as state,
			wait_event_type,
			wait_event,
			COALESCE(LEFT(query, 500), '') as query,
			COALESCE(backend_type, '') as backend_type
		FROM pg_stat_activity
		WHERE backend_type = 'client backend'
		  AND pid != pg_backend_pid()
		ORDER BY state_change DESC
	`

	rows, err := c.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("querying pg_stat_activity: %w", err)
	}
	defer rows.Close()

	var connections []*Connection
	for rows.Next() {
		conn := &Connection{}
		err := rows.Scan(
			&conn.PID,
			&conn.Username,
			&conn.ApplicationName,
			&conn.ClientAddr,
			&conn.ClientPort,
			&conn.BackendStart,
			&conn.XactStart,
			&conn.QueryStart,
			&conn.StateChange,
			&conn.State,
			&conn.WaitEventType,
			&conn.WaitEvent,
			&conn.Query,
			&conn.BackendType,
		)
		if err != nil {
			return nil, fmt.Errorf("scanning row: %w", err)
		}
		connections = append(connections, conn)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating rows: %w", err)
	}

	return connections, nil
}

// GetPoolStats returns aggregate statistics about the connection pool
func (c *Client) GetPoolStats(ctx context.Context) (*PoolStats, error) {
	stats := &PoolStats{}

	// Get max_connections
	err := c.pool.QueryRow(ctx, `
		SELECT setting::int FROM pg_settings WHERE name = 'max_connections'
	`).Scan(&stats.MaxConnections)
	if err != nil {
		return nil, fmt.Errorf("getting max_connections: %w", err)
	}

	// Get superuser_reserved_connections
	err = c.pool.QueryRow(ctx, `
		SELECT setting::int FROM pg_settings WHERE name = 'superuser_reserved_connections'
	`).Scan(&stats.ReservedSuperuser)
	if err != nil {
		return nil, fmt.Errorf("getting superuser_reserved_connections: %w", err)
	}

	// Get counts by state
	rows, err := c.pool.Query(ctx, `
		SELECT 
			COALESCE(state, 'unknown') as state,
			COUNT(*) as count
		FROM pg_stat_activity
		WHERE backend_type = 'client backend'
		  AND pid != pg_backend_pid()
		GROUP BY state
	`)
	if err != nil {
		return nil, fmt.Errorf("getting connection counts: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var state string
		var count int
		if err := rows.Scan(&state, &count); err != nil {
			return nil, fmt.Errorf("scanning state count: %w", err)
		}

		stats.TotalConnections += count

		switch ConnectionState(state) {
		case StateActive:
			stats.ActiveConnections = count
		case StateIdle:
			stats.IdleConnections = count
		case StateIdleInTransaction, StateIdleInTransactionAborted:
			stats.IdleInTransaction += count
		}
	}

	stats.AvailableConnections = stats.MaxConnections - stats.ReservedSuperuser - stats.TotalConnections

	return stats, nil
}

// GetServerInfo returns information about the PostgreSQL server
func (c *Client) GetServerInfo(ctx context.Context) (*ServerInfo, error) {
	info := &ServerInfo{}

	err := c.pool.QueryRow(ctx, `
		SELECT 
			version(),
			pg_postmaster_start_time(),
			setting::int
		FROM pg_settings
		WHERE name = 'max_connections'
	`).Scan(&info.Version, &info.ServerStart, &info.MaxConnections)
	if err != nil {
		return nil, fmt.Errorf("getting server info: %w", err)
	}

	return info, nil
}

// TerminateBackend terminates a backend by PID
func (c *Client) TerminateBackend(ctx context.Context, pid int) (bool, error) {
	var success bool
	err := c.pool.QueryRow(ctx, `SELECT pg_terminate_backend($1)`, pid).Scan(&success)
	if err != nil {
		return false, fmt.Errorf("terminating backend %d: %w", pid, err)
	}
	return success, nil
}

// CancelBackend cancels the current query on a backend (less destructive than terminate)
func (c *Client) CancelBackend(ctx context.Context, pid int) (bool, error) {
	var success bool
	err := c.pool.QueryRow(ctx, `SELECT pg_cancel_backend($1)`, pid).Scan(&success)
	if err != nil {
		return false, fmt.Errorf("canceling backend %d: %w", pid, err)
	}
	return success, nil
}

// GetIdleTransactions returns connections that are idle in transaction
func (c *Client) GetIdleTransactions(ctx context.Context) ([]*Connection, error) {
	conns, err := c.GetConnections(ctx)
	if err != nil {
		return nil, err
	}

	var idle []*Connection
	for _, conn := range conns {
		if conn.IsIdleInTransaction() {
			idle = append(idle, conn)
		}
	}

	return idle, nil
}

// TestConnection tests if we can connect and query the database
func TestConnection(connString string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, connString)
	if err != nil {
		return fmt.Errorf("connecting: %w", err)
	}
	defer conn.Close(ctx)

	var result int
	err = conn.QueryRow(ctx, "SELECT 1").Scan(&result)
	if err != nil {
		return fmt.Errorf("querying: %w", err)
	}

	return nil
}

// TestConnectionWithConfig tests connection using full config (supports IAM auth)
func TestConnectionWithConfig(cfg *config.Config) error {
	client, err := NewClient(cfg)
	if err != nil {
		return err
	}
	defer client.Close()
	return nil
}
