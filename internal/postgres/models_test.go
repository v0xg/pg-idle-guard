package postgres

import (
	"testing"
	"time"
)

func TestConnectionIdleDuration(t *testing.T) {
	conn := &Connection{
		StateChange: time.Now().Add(-2 * time.Minute),
	}

	duration := conn.IdleDuration()

	// Should be approximately 2 minutes
	if duration < 119*time.Second || duration > 121*time.Second {
		t.Errorf("expected ~2m duration, got %s", duration)
	}
}

func TestConnectionTransactionDuration(t *testing.T) {
	t.Run("with transaction", func(t *testing.T) {
		xactStart := time.Now().Add(-5 * time.Minute)
		conn := &Connection{
			XactStart: &xactStart,
		}

		duration := conn.TransactionDuration()
		if duration < 299*time.Second || duration > 301*time.Second {
			t.Errorf("expected ~5m duration, got %s", duration)
		}
	})

	t.Run("without transaction", func(t *testing.T) {
		conn := &Connection{
			XactStart: nil,
		}

		duration := conn.TransactionDuration()
		if duration != 0 {
			t.Errorf("expected 0 duration without transaction, got %s", duration)
		}
	})
}

func TestConnectionIsIdleInTransaction(t *testing.T) {
	tests := []struct {
		state ConnectionState
		want  bool
	}{
		{StateActive, false},
		{StateIdle, false},
		{StateIdleInTransaction, true},
		{StateIdleInTransactionAborted, true},
		{StateFastpath, false},
		{StateDisabled, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			conn := &Connection{State: tt.state}
			if got := conn.IsIdleInTransaction(); got != tt.want {
				t.Errorf("IsIdleInTransaction() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPoolStatsUsagePercent(t *testing.T) {
	tests := []struct {
		name  string
		stats PoolStats
		want  float64
	}{
		{
			name: "50% usage",
			stats: PoolStats{
				MaxConnections:    100,
				ReservedSuperuser: 0,
				TotalConnections:  50,
			},
			want: 50.0,
		},
		{
			name: "with reserved connections",
			stats: PoolStats{
				MaxConnections:    100,
				ReservedSuperuser: 3,
				TotalConnections:  97,
			},
			want: 100.0, // 97 / (100-3) = 100%
		},
		{
			name: "empty pool",
			stats: PoolStats{
				MaxConnections:    100,
				ReservedSuperuser: 3,
				TotalConnections:  0,
			},
			want: 0.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.stats.UsagePercent()
			if got != tt.want {
				t.Errorf("UsagePercent() = %v, want %v", got, tt.want)
			}
		})
	}
}
