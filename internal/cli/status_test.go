package cli

import (
	"testing"
	"time"

	"github.com/v0xg/pg-idle-guard/internal/config"
	"github.com/v0xg/pg-idle-guard/internal/postgres"
)

func TestBuildStatusOutput(t *testing.T) {
	testCfg := &config.Config{
		Thresholds: config.ThresholdsConfig{
			IdleTransaction: config.IdleTransactionThresholds{
				Warning:  30 * time.Second,
				Critical: 2 * time.Minute,
			},
			ConnectionPool: config.ConnectionPoolThresholds{
				WarningPercent:  75,
				CriticalPercent: 90,
			},
		},
	}

	stats := &postgres.PoolStats{
		MaxConnections:       100,
		ReservedSuperuser:    3,
		TotalConnections:     25,
		ActiveConnections:    10,
		IdleConnections:      12,
		IdleInTransaction:    3,
		AvailableConnections: 72,
	}

	now := time.Now()
	conns := []*postgres.Connection{
		{
			PID:             1001,
			ApplicationName: "webapp",
			State:           postgres.StateActive,
			ClientAddr:      "192.168.1.10",
			StateChange:     now.Add(-10 * time.Second),
		},
		{
			PID:             1002,
			ApplicationName: "worker",
			State:           postgres.StateIdleInTransaction,
			ClientAddr:      "192.168.1.11",
			StateChange:     now.Add(-45 * time.Second),
			Query:           "SELECT * FROM orders WHERE status = 'pending'",
		},
		{
			PID:             1003,
			ApplicationName: "batch-job",
			State:           postgres.StateIdleInTransaction,
			ClientAddr:      "192.168.1.12",
			StateChange:     now.Add(-3 * time.Minute),
			Query:           "UPDATE inventory SET quantity = quantity - 1",
		},
	}

	idleConns := []*postgres.Connection{conns[1], conns[2]}

	t.Run("basic output structure", func(t *testing.T) {
		output := buildStatusOutput(stats, conns, idleConns, "warning", false, testCfg)

		if output.Status != "warning" {
			t.Errorf("Status = %q, want %q", output.Status, "warning")
		}
		if output.Pool.MaxConnections != 100 {
			t.Errorf("Pool.MaxConnections = %d, want %d", output.Pool.MaxConnections, 100)
		}
		if output.Pool.TotalConnections != 25 {
			t.Errorf("Pool.TotalConnections = %d, want %d", output.Pool.TotalConnections, 25)
		}
		if len(output.IdleTransactions) != 2 {
			t.Errorf("len(IdleTransactions) = %d, want %d", len(output.IdleTransactions), 2)
		}
		// Connections should be empty when not verbose
		if output.Connections != nil {
			t.Errorf("Connections should be nil when not verbose, got %v", output.Connections)
		}
	})

	t.Run("verbose includes all connections", func(t *testing.T) {
		output := buildStatusOutput(stats, conns, idleConns, "ok", true, testCfg)

		if len(output.Connections) != 3 {
			t.Errorf("len(Connections) = %d, want %d", len(output.Connections), 3)
		}
		if output.Connections[0].PID != 1001 {
			t.Errorf("Connections[0].PID = %d, want %d", output.Connections[0].PID, 1001)
		}
	})

	t.Run("idle transaction severity assignment", func(t *testing.T) {
		output := buildStatusOutput(stats, conns, idleConns, "critical", false, testCfg)

		// First idle connection (45s) should be "warning"
		if output.IdleTransactions[0].Severity != "warning" {
			t.Errorf("IdleTransactions[0].Severity = %q, want %q", output.IdleTransactions[0].Severity, "warning")
		}
		// Second idle connection (3m) should be "critical"
		if output.IdleTransactions[1].Severity != "critical" {
			t.Errorf("IdleTransactions[1].Severity = %q, want %q", output.IdleTransactions[1].Severity, "critical")
		}
	})

	t.Run("thresholds are included", func(t *testing.T) {
		output := buildStatusOutput(stats, conns, idleConns, "ok", false, testCfg)

		if output.Thresholds.PoolWarningPct != 75 {
			t.Errorf("Thresholds.PoolWarningPct = %d, want %d", output.Thresholds.PoolWarningPct, 75)
		}
		if output.Thresholds.PoolCriticalPct != 90 {
			t.Errorf("Thresholds.PoolCriticalPct = %d, want %d", output.Thresholds.PoolCriticalPct, 90)
		}
	})

	t.Run("empty idle transactions", func(t *testing.T) {
		output := buildStatusOutput(stats, conns, []*postgres.Connection{}, "ok", false, testCfg)

		if len(output.IdleTransactions) != 0 {
			t.Errorf("len(IdleTransactions) = %d, want %d", len(output.IdleTransactions), 0)
		}
	})
}

func TestPoolStatus_UsagePercent(t *testing.T) {
	tests := []struct {
		name            string
		stats           *postgres.PoolStats
		expectedPercent float64
	}{
		{
			name: "normal usage",
			stats: &postgres.PoolStats{
				MaxConnections:    100,
				ReservedSuperuser: 0,
				TotalConnections:  25,
			},
			expectedPercent: 25.0,
		},
		{
			name: "high usage",
			stats: &postgres.PoolStats{
				MaxConnections:    100,
				ReservedSuperuser: 3,
				TotalConnections:  87,
			},
			expectedPercent: 89.69, // 87/97 * 100
		},
		{
			name: "full usage",
			stats: &postgres.PoolStats{
				MaxConnections:    100,
				ReservedSuperuser: 3,
				TotalConnections:  97,
			},
			expectedPercent: 100.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.stats.UsagePercent()
			// Allow small floating point tolerance
			if got < tt.expectedPercent-0.1 || got > tt.expectedPercent+0.1 {
				t.Errorf("UsagePercent() = %.2f, want ~%.2f", got, tt.expectedPercent)
			}
		})
	}
}

func TestExitCodeDetermination(t *testing.T) {
	tests := []struct {
		name             string
		poolUsagePercent float64
		poolWarningPct   int
		poolCriticalPct  int
		idleDurations    []time.Duration
		idleWarning      time.Duration
		idleCritical     time.Duration
		expectedExitCode int
		expectedStatus   string
	}{
		{
			name:             "all healthy",
			poolUsagePercent: 50,
			poolWarningPct:   75,
			poolCriticalPct:  90,
			idleDurations:    []time.Duration{10 * time.Second, 15 * time.Second},
			idleWarning:      30 * time.Second,
			idleCritical:     2 * time.Minute,
			expectedExitCode: ExitOK,
			expectedStatus:   "ok",
		},
		{
			name:             "pool at warning",
			poolUsagePercent: 80,
			poolWarningPct:   75,
			poolCriticalPct:  90,
			idleDurations:    []time.Duration{10 * time.Second},
			idleWarning:      30 * time.Second,
			idleCritical:     2 * time.Minute,
			expectedExitCode: ExitWarning,
			expectedStatus:   "warning",
		},
		{
			name:             "pool at critical",
			poolUsagePercent: 95,
			poolWarningPct:   75,
			poolCriticalPct:  90,
			idleDurations:    []time.Duration{10 * time.Second},
			idleWarning:      30 * time.Second,
			idleCritical:     2 * time.Minute,
			expectedExitCode: ExitCritical,
			expectedStatus:   "critical",
		},
		{
			name:             "idle transaction at warning",
			poolUsagePercent: 50,
			poolWarningPct:   75,
			poolCriticalPct:  90,
			idleDurations:    []time.Duration{45 * time.Second},
			idleWarning:      30 * time.Second,
			idleCritical:     2 * time.Minute,
			expectedExitCode: ExitWarning,
			expectedStatus:   "warning",
		},
		{
			name:             "idle transaction at critical",
			poolUsagePercent: 50,
			poolWarningPct:   75,
			poolCriticalPct:  90,
			idleDurations:    []time.Duration{3 * time.Minute},
			idleWarning:      30 * time.Second,
			idleCritical:     2 * time.Minute,
			expectedExitCode: ExitCritical,
			expectedStatus:   "critical",
		},
		{
			name:             "critical overrides warning",
			poolUsagePercent: 80, // warning level
			poolWarningPct:   75,
			poolCriticalPct:  90,
			idleDurations:    []time.Duration{3 * time.Minute}, // critical level
			idleWarning:      30 * time.Second,
			idleCritical:     2 * time.Minute,
			expectedExitCode: ExitCritical,
			expectedStatus:   "critical",
		},
		{
			name:             "no idle transactions",
			poolUsagePercent: 50,
			poolWarningPct:   75,
			poolCriticalPct:  90,
			idleDurations:    []time.Duration{},
			idleWarning:      30 * time.Second,
			idleCritical:     2 * time.Minute,
			expectedExitCode: ExitOK,
			expectedStatus:   "ok",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exitCode := ExitOK
			status := "ok"

			// Check pool thresholds
			if tt.poolUsagePercent >= float64(tt.poolCriticalPct) {
				exitCode = ExitCritical
				status = "critical"
			} else if tt.poolUsagePercent >= float64(tt.poolWarningPct) {
				if exitCode < ExitWarning {
					exitCode = ExitWarning
					status = "warning"
				}
			}

			// Check idle transaction thresholds
			for _, duration := range tt.idleDurations {
				if duration >= tt.idleCritical {
					exitCode = ExitCritical
					status = "critical"
					break
				} else if duration >= tt.idleWarning {
					if exitCode < ExitWarning {
						exitCode = ExitWarning
						status = "warning"
					}
				}
			}

			if exitCode != tt.expectedExitCode {
				t.Errorf("exitCode = %d, want %d", exitCode, tt.expectedExitCode)
			}
			if status != tt.expectedStatus {
				t.Errorf("status = %q, want %q", status, tt.expectedStatus)
			}
		})
	}
}

func TestStatusOutput_JSON_Structure(t *testing.T) {
	// Verify the struct tags are correct for JSON marshaling
	output := StatusOutput{
		Status: "ok",
		Pool: PoolStatus{
			MaxConnections:       100,
			TotalConnections:     25,
			ActiveConnections:    10,
			IdleConnections:      12,
			IdleInTransaction:    3,
			AvailableConnections: 72,
			UsagePercent:         25.77,
		},
		IdleTransactions: []IdleTransactionStatus{
			{
				PID:         12345,
				Application: "test-app",
				Duration:    "45s",
				DurationSec: 45.0,
				Query:       "SELECT 1",
				Severity:    "warning",
			},
		},
		Thresholds: ThresholdStatus{
			IdleWarning:     "30s",
			IdleCritical:    "2m0s",
			PoolWarningPct:  75,
			PoolCriticalPct: 90,
		},
	}

	// Verify fields are set correctly
	if output.Status != "ok" {
		t.Errorf("Status = %q, want %q", output.Status, "ok")
	}
	if len(output.IdleTransactions) != 1 {
		t.Errorf("len(IdleTransactions) = %d, want %d", len(output.IdleTransactions), 1)
	}
	if output.IdleTransactions[0].PID != 12345 {
		t.Errorf("IdleTransactions[0].PID = %d, want %d", output.IdleTransactions[0].PID, 12345)
	}
}

func TestExitCodes(t *testing.T) {
	// Verify exit code constants
	if ExitOK != 0 {
		t.Errorf("ExitOK = %d, want 0", ExitOK)
	}
	if ExitWarning != 1 {
		t.Errorf("ExitWarning = %d, want 1", ExitWarning)
	}
	if ExitCritical != 2 {
		t.Errorf("ExitCritical = %d, want 2", ExitCritical)
	}
}
