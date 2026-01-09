package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/v0xg/pg-idle-guard/internal/config"
	"github.com/v0xg/pg-idle-guard/internal/postgres"
	"github.com/v0xg/pg-idle-guard/internal/util"
)

// Exit codes for status command
const (
	ExitOK       = 0
	ExitWarning  = 1
	ExitCritical = 2
)

// StatusOutput represents the JSON output of the status command
type StatusOutput struct {
	Status           string                  `json:"status"` // "ok", "warning", "critical"
	Pool             PoolStatus              `json:"pool"`
	IdleTransactions []IdleTransactionStatus `json:"idle_transactions"`
	Connections      []ConnectionStatus      `json:"connections,omitempty"` // Only with --verbose
	Thresholds       ThresholdStatus         `json:"thresholds"`
}

// PoolStatus represents connection pool statistics
type PoolStatus struct {
	MaxConnections       int     `json:"max_connections"`
	TotalConnections     int     `json:"total_connections"`
	ActiveConnections    int     `json:"active_connections"`
	IdleConnections      int     `json:"idle_connections"`
	IdleInTransaction    int     `json:"idle_in_transaction"`
	AvailableConnections int     `json:"available_connections"`
	UsagePercent         float64 `json:"usage_percent"`
}

// IdleTransactionStatus represents a single idle transaction
type IdleTransactionStatus struct {
	PID         int     `json:"pid"`
	Application string  `json:"application"`
	Duration    string  `json:"duration"`
	DurationSec float64 `json:"duration_seconds"`
	Query       string  `json:"query"`
	Severity    string  `json:"severity"` // "warning", "critical", or ""
}

// ConnectionStatus represents a single connection (for verbose output)
type ConnectionStatus struct {
	PID         int    `json:"pid"`
	State       string `json:"state"`
	Application string `json:"application"`
	ClientAddr  string `json:"client_addr"`
	Duration    string `json:"duration"`
}

// ThresholdStatus shows the configured thresholds
type ThresholdStatus struct {
	IdleWarning     string `json:"idle_warning"`
	IdleCritical    string `json:"idle_critical"`
	PoolWarningPct  int    `json:"pool_warning_percent"`
	PoolCriticalPct int    `json:"pool_critical_percent"`
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show current connection pool status",
	Long: `Display the current state of PostgreSQL connections, including idle transactions.

Exit codes:
  0 - All healthy (no thresholds exceeded)
  1 - Warning threshold exceeded
  2 - Critical threshold exceeded`,
	RunE: runStatus,
}

func init() {
	statusCmd.Flags().BoolP("verbose", "v", false, "Show all connections, not just idle transactions")
	statusCmd.Flags().Bool("json", false, "Output in JSON format")
	statusCmd.Flags().BoolP("quiet", "q", false, "No output, only exit code")
}

func runStatus(cmd *cobra.Command, args []string) error {
	verbose, _ := cmd.Flags().GetBool("verbose")
	jsonOutput, _ := cmd.Flags().GetBool("json")
	quiet, _ := cmd.Flags().GetBool("quiet")

	// Create PostgreSQL client
	client, err := postgres.NewClient(cfg)
	if err != nil {
		return fmt.Errorf("connecting to database: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)

	// Get pool stats
	stats, err := client.GetPoolStats(ctx)
	if err != nil {
		cancel()
		client.Close()
		return fmt.Errorf("getting pool stats: %w", err)
	}

	// Get all connections
	conns, err := client.GetConnections(ctx)
	if err != nil {
		cancel()
		client.Close()
		return fmt.Errorf("getting connections: %w", err)
	}

	// Build idle transactions list
	var idleConns []*postgres.Connection
	for _, conn := range conns {
		if conn.IsIdleInTransaction() {
			idleConns = append(idleConns, conn)
		}
	}

	// Determine overall status and exit code
	exitCode := ExitOK
	overallStatus := "ok"
	usagePercent := stats.UsagePercent()

	// Check pool thresholds
	if usagePercent >= float64(cfg.Thresholds.ConnectionPool.CriticalPercent) {
		exitCode = ExitCritical
		overallStatus = "critical"
	} else if usagePercent >= float64(cfg.Thresholds.ConnectionPool.WarningPercent) {
		if exitCode < ExitWarning {
			exitCode = ExitWarning
			overallStatus = "warning"
		}
	}

	// Check idle transaction thresholds
	for _, conn := range idleConns {
		duration := conn.IdleDuration()
		if duration >= cfg.Thresholds.IdleTransaction.Critical {
			exitCode = ExitCritical
			overallStatus = "critical"
			break // Can't get worse than critical
		} else if duration >= cfg.Thresholds.IdleTransaction.Warning {
			if exitCode < ExitWarning {
				exitCode = ExitWarning
				overallStatus = "warning"
			}
		}
	}

	// Quiet mode: just exit with code
	if quiet {
		cancel()
		client.Close()
		os.Exit(exitCode)
	}

	// JSON output mode
	if jsonOutput {
		output := buildStatusOutput(stats, conns, idleConns, overallStatus, verbose, cfg)
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			cancel()
			client.Close()
			return fmt.Errorf("marshaling JSON: %w", err)
		}
		fmt.Println(string(data))
		cancel()
		client.Close()
		os.Exit(exitCode)
	}

	// Human-readable output
	printHumanStatus(stats, conns, idleConns, usagePercent, verbose, cfg)

	cancel()
	client.Close()
	os.Exit(exitCode)
	return nil // unreachable but satisfies compiler
}

func buildStatusOutput(stats *postgres.PoolStats, conns, idleConns []*postgres.Connection, status string, verbose bool, cfg *config.Config) StatusOutput {
	output := StatusOutput{
		Status: status,
		Pool: PoolStatus{
			MaxConnections:       stats.MaxConnections,
			TotalConnections:     stats.TotalConnections,
			ActiveConnections:    stats.ActiveConnections,
			IdleConnections:      stats.IdleConnections,
			IdleInTransaction:    stats.IdleInTransaction,
			AvailableConnections: stats.AvailableConnections,
			UsagePercent:         stats.UsagePercent(),
		},
		Thresholds: ThresholdStatus{
			IdleWarning:     cfg.Thresholds.IdleTransaction.Warning.String(),
			IdleCritical:    cfg.Thresholds.IdleTransaction.Critical.String(),
			PoolWarningPct:  cfg.Thresholds.ConnectionPool.WarningPercent,
			PoolCriticalPct: cfg.Thresholds.ConnectionPool.CriticalPercent,
		},
	}

	// Build idle transactions list
	output.IdleTransactions = make([]IdleTransactionStatus, 0, len(idleConns))
	for _, conn := range idleConns {
		duration := conn.IdleDuration()
		severity := ""
		if duration >= cfg.Thresholds.IdleTransaction.Critical {
			severity = "critical"
		} else if duration >= cfg.Thresholds.IdleTransaction.Warning {
			severity = "warning"
		}

		output.IdleTransactions = append(output.IdleTransactions, IdleTransactionStatus{
			PID:         conn.PID,
			Application: conn.ApplicationName,
			Duration:    util.FormatDuration(duration),
			DurationSec: duration.Seconds(),
			Query:       util.TruncateQuery(conn.Query, 200),
			Severity:    severity,
		})
	}

	// Add all connections if verbose
	if verbose {
		output.Connections = make([]ConnectionStatus, 0, len(conns))
		for _, conn := range conns {
			output.Connections = append(output.Connections, ConnectionStatus{
				PID:         conn.PID,
				State:       string(conn.State),
				Application: conn.ApplicationName,
				ClientAddr:  conn.ClientAddr,
				Duration:    util.FormatDuration(conn.IdleDuration()),
			})
		}
	}

	return output
}

func printHumanStatus(stats *postgres.PoolStats, conns, idleConns []*postgres.Connection, usagePercent float64, verbose bool, cfg *config.Config) {
	// Print pool status
	fmt.Println()
	fmt.Printf("Connection Pool (max: %d)\n", stats.MaxConnections)
	fmt.Println(strings.Repeat("-", 44))

	fmt.Printf("Active:               %3d\n", stats.ActiveConnections)
	fmt.Printf("Idle:                 %3d\n", stats.IdleConnections)

	idleIndicator := ""
	if stats.IdleInTransaction > 0 {
		idleIndicator = "  [!]"
	}
	fmt.Printf("Idle in transaction:  %3d%s\n", stats.IdleInTransaction, idleIndicator)
	fmt.Printf("Available:            %3d\n", stats.AvailableConnections)

	// Usage percentage with indicator
	usageIndicator := ""
	if usagePercent >= float64(cfg.Thresholds.ConnectionPool.CriticalPercent) {
		usageIndicator = " [CRIT]"
	} else if usagePercent >= float64(cfg.Thresholds.ConnectionPool.WarningPercent) {
		usageIndicator = " [WARN]"
	}
	fmt.Printf("\nUsage: %.1f%% (%d/%d)%s\n", usagePercent, stats.TotalConnections, stats.MaxConnections-stats.ReservedSuperuser, usageIndicator)

	// Print idle transactions
	if len(idleConns) > 0 {
		fmt.Println()
		fmt.Println("Idle Transactions")
		fmt.Println(strings.Repeat("-", 80))

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "PID\tAge\tApplication\tQuery")

		for _, conn := range idleConns {
			duration := conn.IdleDuration()
			severity := getSeverity(duration, cfg)

			query := util.TruncateQuery(conn.Query, 40)

			fmt.Fprintf(w, "%d\t%s\t%s\t%s %s\n",
				conn.PID,
				util.FormatDuration(duration),
				util.Truncate(conn.ApplicationName, 15),
				query,
				severity,
			)
		}
		w.Flush()
	} else {
		fmt.Println()
		fmt.Println("No idle transactions.")
	}

	// Show all connections if verbose
	if verbose && len(conns) > 0 {
		fmt.Println()
		fmt.Println("All Connections")
		fmt.Println(strings.Repeat("-", 80))

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "PID\tState\tApplication\tClient\tAge")

		for _, conn := range conns {
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\n",
				conn.PID,
				conn.State,
				util.Truncate(conn.ApplicationName, 15),
				conn.ClientAddr,
				util.FormatDuration(conn.IdleDuration()),
			)
		}
		w.Flush()
	}

	fmt.Println()
}

func getSeverity(duration time.Duration, cfg *config.Config) string {
	if duration >= cfg.Thresholds.IdleTransaction.Critical {
		return "[CRIT]"
	}
	if duration >= cfg.Thresholds.IdleTransaction.Warning {
		return "[WARN]"
	}
	return ""
}
