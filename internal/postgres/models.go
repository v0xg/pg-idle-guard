package postgres

import (
	"time"
)

// ConnectionState represents the state of a PostgreSQL connection
type ConnectionState string

const (
	StateActive                   ConnectionState = "active"
	StateIdle                     ConnectionState = "idle"
	StateIdleInTransaction        ConnectionState = "idle in transaction"
	StateIdleInTransactionAborted ConnectionState = "idle in transaction (aborted)"
	StateFastpath                 ConnectionState = "fastpath function call"
	StateDisabled                 ConnectionState = "disabled"
)

// Connection represents a single database connection from pg_stat_activity
type Connection struct {
	PID             int
	Username        string
	ApplicationName string
	ClientAddr      string
	ClientPort      int
	BackendStart    time.Time
	XactStart       *time.Time // Transaction start time (nil if no transaction)
	QueryStart      *time.Time
	StateChange     time.Time
	State           ConnectionState
	WaitEventType   *string
	WaitEvent       *string
	Query           string
	BackendType     string
}

// IdleDuration returns how long the connection has been in its current state
func (c *Connection) IdleDuration() time.Duration {
	return time.Since(c.StateChange)
}

// TransactionDuration returns how long the current transaction has been open
func (c *Connection) TransactionDuration() time.Duration {
	if c.XactStart == nil {
		return 0
	}
	return time.Since(*c.XactStart)
}

// IsIdleInTransaction returns true if the connection is idle in a transaction
func (c *Connection) IsIdleInTransaction() bool {
	return c.State == StateIdleInTransaction || c.State == StateIdleInTransactionAborted
}

// PoolStats contains aggregate statistics about the connection pool
type PoolStats struct {
	MaxConnections       int
	ReservedSuperuser    int
	TotalConnections     int
	ActiveConnections    int
	IdleConnections      int
	IdleInTransaction    int
	AvailableConnections int
}

// UsagePercent returns the percentage of connections in use
func (p *PoolStats) UsagePercent() float64 {
	available := p.MaxConnections - p.ReservedSuperuser
	if available <= 0 {
		return 100
	}
	return float64(p.TotalConnections) / float64(available) * 100
}

// ServerInfo contains basic information about the PostgreSQL server
type ServerInfo struct {
	Version        string
	ServerStart    time.Time
	MaxConnections int
}
