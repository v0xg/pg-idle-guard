package cli

import (
	"bufio"
	"strings"
	"testing"
	"time"

	"github.com/v0xg/pg-idle-guard/internal/config"
	"github.com/v0xg/pg-idle-guard/internal/postgres"
	"github.com/v0xg/pg-idle-guard/internal/util"
)

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		want     string
	}{
		{
			name:     "seconds only",
			duration: 45 * time.Second,
			want:     "45s",
		},
		{
			name:     "minutes and seconds",
			duration: 2*time.Minute + 30*time.Second,
			want:     "2m 30s",
		},
		{
			name:     "hours and minutes",
			duration: 1*time.Hour + 15*time.Minute + 30*time.Second,
			want:     "1h 15m",
		},
		{
			name:     "zero",
			duration: 0,
			want:     "0s",
		},
		{
			name:     "exactly one minute",
			duration: 1 * time.Minute,
			want:     "1m 0s",
		},
		{
			name:     "exactly one hour",
			duration: 1 * time.Hour,
			want:     "1h 0m",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := util.FormatDuration(tt.duration)
			if got != tt.want {
				t.Errorf("FormatDuration(%v) = %q, want %q", tt.duration, got, tt.want)
			}
		})
	}
}

func TestTruncateQuery(t *testing.T) {
	tests := []struct {
		name   string
		query  string
		maxLen int
		want   string
	}{
		{
			name:   "short query unchanged",
			query:  "SELECT * FROM users",
			maxLen: 50,
			want:   "SELECT * FROM users",
		},
		{
			name:   "long query truncated",
			query:  "SELECT id, name, email, created_at, updated_at FROM users WHERE active = true",
			maxLen: 30,
			want:   "SELECT id, name, email, cre...",
		},
		{
			name:   "query with newlines normalized",
			query:  "SELECT *\nFROM users\nWHERE id = 1",
			maxLen: 50,
			want:   "SELECT * FROM users WHERE id = 1",
		},
		{
			name:   "query with extra whitespace normalized",
			query:  "SELECT   *   FROM    users",
			maxLen: 50,
			want:   "SELECT * FROM users",
		},
		{
			name:   "empty query",
			query:  "",
			maxLen: 50,
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := util.TruncateQuery(tt.query, tt.maxLen)
			if got != tt.want {
				t.Errorf("TruncateQuery(%q, %d) = %q, want %q", tt.query, tt.maxLen, got, tt.want)
			}
		})
	}
}

func TestShouldTerminate(t *testing.T) {
	// Save original config and restore after test
	originalCfg := cfg
	defer func() { cfg = originalCfg }()

	tests := []struct {
		name          string
		conn          *postgres.Connection
		duration      time.Duration
		excludeApps   []string
		excludeIPs    []string
		protectedApps []config.ProtectedApp
		want          bool
	}{
		{
			name: "no exclusions - should terminate",
			conn: &postgres.Connection{
				ApplicationName: "myapp",
				ClientAddr:      "192.168.1.100",
			},
			duration:    5 * time.Minute,
			excludeApps: []string{},
			excludeIPs:  []string{},
			want:        true,
		},
		{
			name: "excluded by app name",
			conn: &postgres.Connection{
				ApplicationName: "pg_dump",
				ClientAddr:      "192.168.1.100",
			},
			duration:    5 * time.Minute,
			excludeApps: []string{"pg_dump", "migration-runner"},
			excludeIPs:  []string{},
			want:        false,
		},
		{
			name: "excluded by IP",
			conn: &postgres.Connection{
				ApplicationName: "myapp",
				ClientAddr:      "10.0.0.1",
			},
			duration:    5 * time.Minute,
			excludeApps: []string{},
			excludeIPs:  []string{"10.0.0.1", "10.0.0.2"},
			want:        false,
		},
		{
			name: "not in exclusion lists",
			conn: &postgres.Connection{
				ApplicationName: "webapp",
				ClientAddr:      "192.168.1.50",
			},
			duration:    5 * time.Minute,
			excludeApps: []string{"pg_dump", "backup-agent"},
			excludeIPs:  []string{"10.0.0.1"},
			want:        true,
		},
		{
			name: "pguard excluded",
			conn: &postgres.Connection{
				ApplicationName: "pguard",
				ClientAddr:      "127.0.0.1",
			},
			duration:    5 * time.Minute,
			excludeApps: []string{"pguard"},
			excludeIPs:  []string{},
			want:        false,
		},
		{
			name: "protected app - under custom threshold",
			conn: &postgres.Connection{
				ApplicationName: "critical-service",
				ClientAddr:      "192.168.1.100",
			},
			duration:    3 * time.Minute,
			excludeApps: []string{},
			excludeIPs:  []string{},
			protectedApps: []config.ProtectedApp{
				{Name: "critical-service", MinIdleDuration: 10 * time.Minute},
			},
			want: false,
		},
		{
			name: "protected app - over custom threshold",
			conn: &postgres.Connection{
				ApplicationName: "critical-service",
				ClientAddr:      "192.168.1.100",
			},
			duration:    15 * time.Minute,
			excludeApps: []string{},
			excludeIPs:  []string{},
			protectedApps: []config.ProtectedApp{
				{Name: "critical-service", MinIdleDuration: 10 * time.Minute},
			},
			want: true,
		},
		{
			name: "protected app - requires confirmation never auto-terminates",
			conn: &postgres.Connection{
				ApplicationName: "important-service",
				ClientAddr:      "192.168.1.100",
			},
			duration:    1 * time.Hour,
			excludeApps: []string{},
			excludeIPs:  []string{},
			protectedApps: []config.ProtectedApp{
				{Name: "important-service", MinIdleDuration: 5 * time.Minute, RequireConfirmation: true},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg = &config.Config{
				AutoTerm: config.AutoTermConfig{
					ExcludeApps:   tt.excludeApps,
					ExcludeIPs:    tt.excludeIPs,
					ProtectedApps: tt.protectedApps,
				},
			}

			got := shouldTerminate(tt.conn, tt.duration)
			if got != tt.want {
				t.Errorf("shouldTerminate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetSeverity(t *testing.T) {
	testCfg := &config.Config{
		Thresholds: config.ThresholdsConfig{
			IdleTransaction: config.IdleTransactionThresholds{
				Warning:  30 * time.Second,
				Critical: 2 * time.Minute,
			},
		},
	}

	tests := []struct {
		name     string
		duration time.Duration
		want     string
	}{
		{
			name:     "below warning",
			duration: 15 * time.Second,
			want:     "",
		},
		{
			name:     "at warning threshold",
			duration: 30 * time.Second,
			want:     "[WARN]",
		},
		{
			name:     "between warning and critical",
			duration: 1 * time.Minute,
			want:     "[WARN]",
		},
		{
			name:     "at critical threshold",
			duration: 2 * time.Minute,
			want:     "[CRIT]",
		},
		{
			name:     "above critical",
			duration: 5 * time.Minute,
			want:     "[CRIT]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getSeverity(tt.duration, testCfg)
			if got != tt.want {
				t.Errorf("getSeverity(%v) = %q, want %q", tt.duration, got, tt.want)
			}
		})
	}
}

func TestTrackedIdle(t *testing.T) {
	now := time.Now()
	firstSeen := now.Add(-5 * time.Minute)
	tc := &trackedIdle{
		pid:          12345,
		appName:      "testapp",
		query:        "SELECT * FROM test",
		firstSeen:    firstSeen,
		warningSent:  false,
		criticalSent: false,
	}

	// Verify struct fields
	if tc.pid != 12345 {
		t.Errorf("pid = %d, want 12345", tc.pid)
	}
	if tc.appName != "testapp" {
		t.Errorf("appName = %s, want testapp", tc.appName)
	}
	if tc.query != "SELECT * FROM test" {
		t.Errorf("query = %s, want SELECT * FROM test", tc.query)
	}
	if tc.firstSeen != firstSeen {
		t.Errorf("firstSeen = %v, want %v", tc.firstSeen, firstSeen)
	}
	if tc.warningSent != false {
		t.Error("warningSent should be false initially")
	}
	if tc.criticalSent != false {
		t.Error("criticalSent should be false initially")
	}

	// Simulate sending warning
	tc.warningSent = true
	if !tc.warningSent {
		t.Error("warningSent should be true after setting")
	}
}

func TestReadLine(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:    "simple input",
			input:   "hello\n",
			want:    "hello",
			wantErr: false,
		},
		{
			name:    "input with spaces",
			input:   "  hello world  \n",
			want:    "hello world",
			wantErr: false,
		},
		{
			name:    "empty input",
			input:   "\n",
			want:    "",
			wantErr: false,
		},
		{
			name:    "no newline (EOF)",
			input:   "incomplete",
			want:    "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := bufio.NewReader(strings.NewReader(tt.input))
			got, err := readLine(reader)

			if (err != nil) != tt.wantErr {
				t.Errorf("readLine() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && got != tt.want {
				t.Errorf("readLine() = %q, want %q", got, tt.want)
			}
		})
	}
}
