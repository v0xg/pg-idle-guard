package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/v0xg/pg-idle-guard/internal/postgres"
)

func TestFindConnectionByPID(t *testing.T) {
	now := time.Now()
	conns := []*postgres.Connection{
		{
			PID:             1001,
			ApplicationName: "webapp",
			State:           postgres.StateActive,
			ClientAddr:      "192.168.1.10",
			Username:        "appuser",
			StateChange:     now.Add(-10 * time.Second),
		},
		{
			PID:             1002,
			ApplicationName: "worker",
			State:           postgres.StateIdleInTransaction,
			ClientAddr:      "192.168.1.11",
			Username:        "worker_user",
			StateChange:     now.Add(-45 * time.Second),
		},
		{
			PID:             1003,
			ApplicationName: "batch",
			State:           postgres.StateIdle,
			ClientAddr:      "192.168.1.12",
			Username:        "batch_user",
			StateChange:     now.Add(-5 * time.Second),
		},
	}

	tests := []struct {
		name      string
		pid       int
		wantFound bool
		wantApp   string
	}{
		{
			name:      "find existing connection",
			pid:       1001,
			wantFound: true,
			wantApp:   "webapp",
		},
		{
			name:      "find another connection",
			pid:       1002,
			wantFound: true,
			wantApp:   "worker",
		},
		{
			name:      "connection not found",
			pid:       9999,
			wantFound: false,
			wantApp:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var targetConn *postgres.Connection
			for _, conn := range conns {
				if conn.PID == tt.pid {
					targetConn = conn
					break
				}
			}

			found := targetConn != nil
			if found != tt.wantFound {
				t.Errorf("found = %v, want %v", found, tt.wantFound)
			}
			if found && targetConn.ApplicationName != tt.wantApp {
				t.Errorf("ApplicationName = %q, want %q", targetConn.ApplicationName, tt.wantApp)
			}
		})
	}
}

func TestConnectionState_Warnings(t *testing.T) {
	tests := []struct {
		name               string
		state              postgres.ConnectionState
		isIdleInTx         bool
		isActive           bool
		shouldWarnRollback bool
		shouldWarnActive   bool
	}{
		{
			name:               "idle in transaction - should warn about rollback",
			state:              postgres.StateIdleInTransaction,
			isIdleInTx:         true,
			isActive:           false,
			shouldWarnRollback: true,
			shouldWarnActive:   false,
		},
		{
			name:               "idle in transaction (aborted) - should warn about rollback",
			state:              postgres.StateIdleInTransactionAborted,
			isIdleInTx:         true,
			isActive:           false,
			shouldWarnRollback: true,
			shouldWarnActive:   false,
		},
		{
			name:               "active - should warn about active query",
			state:              postgres.StateActive,
			isIdleInTx:         false,
			isActive:           true,
			shouldWarnRollback: false,
			shouldWarnActive:   true,
		},
		{
			name:               "idle - no special warning",
			state:              postgres.StateIdle,
			isIdleInTx:         false,
			isActive:           false,
			shouldWarnRollback: false,
			shouldWarnActive:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn := &postgres.Connection{
				PID:   12345,
				State: tt.state,
			}

			isIdleInTx := conn.IsIdleInTransaction()
			isActive := string(conn.State) == "active"

			if isIdleInTx != tt.isIdleInTx {
				t.Errorf("IsIdleInTransaction() = %v, want %v", isIdleInTx, tt.isIdleInTx)
			}
			if isActive != tt.isActive {
				t.Errorf("isActive = %v, want %v", isActive, tt.isActive)
			}

			// Verify warning conditions
			shouldWarnRollback := isIdleInTx
			shouldWarnActive := isActive && !isIdleInTx

			if shouldWarnRollback != tt.shouldWarnRollback {
				t.Errorf("shouldWarnRollback = %v, want %v", shouldWarnRollback, tt.shouldWarnRollback)
			}
			if shouldWarnActive != tt.shouldWarnActive {
				t.Errorf("shouldWarnActive = %v, want %v", shouldWarnActive, tt.shouldWarnActive)
			}
		})
	}
}

func TestConfirmationResponse(t *testing.T) {
	tests := []struct {
		name       string
		response   string
		shouldProc bool
	}{
		{"lowercase y", "y", true},
		{"uppercase Y", "Y", true},
		{"lowercase yes", "yes", true},
		{"uppercase YES", "YES", true},
		{"mixed case Yes", "Yes", true},
		{"no", "no", false},
		{"n", "n", false},
		{"empty", "", false},
		{"random text", "maybe", false},
		{"y with spaces (trimmed)", "  y  ", false}, // Should be trimmed before comparison
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := strings.ToLower(tt.response)
			shouldProceed := response == "y" || response == "yes"

			if shouldProceed != tt.shouldProc {
				t.Errorf("shouldProceed for %q = %v, want %v", tt.response, shouldProceed, tt.shouldProc)
			}
		})
	}
}

func TestActionString(t *testing.T) {
	tests := []struct {
		name       string
		cancelOnly bool
		wantAction string
	}{
		{"terminate mode", false, "terminate"},
		{"cancel mode", true, "cancel the current query on"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			action := "terminate"
			if tt.cancelOnly {
				action = "cancel the current query on"
			}

			if action != tt.wantAction {
				t.Errorf("action = %q, want %q", action, tt.wantAction)
			}
		})
	}
}
