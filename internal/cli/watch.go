package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/v0xg/pg-idle-guard/internal/postgres"
	"github.com/v0xg/pg-idle-guard/internal/util"
)

var watchCmd = &cobra.Command{
	Use:   "watch",
	Short: "Monitor connections in real-time",
	Long:  `Watch PostgreSQL connections in real-time and report changes as they happen.`,
	RunE:  runWatch,
}

func init() {
	watchCmd.Flags().DurationP("interval", "i", 5*time.Second, "Polling interval")
}

// trackedConnection keeps state about connections we're watching
type trackedConnection struct {
	pid          int
	appName      string
	query        string
	firstSeen    time.Time
	warningSent  bool
	criticalSent bool
}

func runWatch(cmd *cobra.Command, args []string) error {
	interval, _ := cmd.Flags().GetDuration("interval")

	// Create PostgreSQL client
	client, err := postgres.NewClient(cfg)
	if err != nil {
		return fmt.Errorf("connecting to database: %w", err)
	}
	defer client.Close()

	// Handle graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\nStopping...")
		cancel()
	}()

	fmt.Println("Watching PostgreSQL connections... (Ctrl+C to stop)")
	fmt.Printf("Refresh: %s | Thresholds: warn=%s, crit=%s\n",
		interval,
		cfg.Thresholds.IdleTransaction.Warning,
		cfg.Thresholds.IdleTransaction.Critical,
	)
	fmt.Println()

	// Track connections we've seen
	tracked := make(map[int]*trackedConnection)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Run immediately, then on tick
	if err := pollOnce(ctx, client, tracked); err != nil {
		logEvent("ERROR", err.Error())
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := pollOnce(ctx, client, tracked); err != nil {
				logEvent("ERROR", err.Error())
			}
		}
	}
}

func pollOnce(ctx context.Context, client *postgres.Client, tracked map[int]*trackedConnection) error {
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Get pool stats
	stats, err := client.GetPoolStats(queryCtx)
	if err != nil {
		return err
	}

	// Get idle transactions
	conns, err := client.GetIdleTransactions(queryCtx)
	if err != nil {
		return err
	}

	// Track which PIDs we see this round
	seenPIDs := make(map[int]bool)

	for _, conn := range conns {
		seenPIDs[conn.PID] = true
		duration := conn.IdleDuration()

		tc, exists := tracked[conn.PID]
		if !exists {
			// New idle transaction
			tc = &trackedConnection{
				pid:       conn.PID,
				appName:   conn.ApplicationName,
				query:     util.TruncateQuery(conn.Query, 60),
				firstSeen: time.Now(),
			}
			tracked[conn.PID] = tc

			if duration >= cfg.Thresholds.IdleTransaction.Warning {
				logEvent("WARN", fmt.Sprintf("New idle transaction: PID %d (%s) idle for %s",
					conn.PID, conn.ApplicationName, util.FormatDuration(duration)))
				logEvent("    ", fmt.Sprintf("Query: %s", tc.query))
				tc.warningSent = true
			}
		}

		// Check for threshold crossings
		if !tc.warningSent && duration >= cfg.Thresholds.IdleTransaction.Warning {
			logEvent("WARN", fmt.Sprintf("PID %d (%s) idle for %s",
				conn.PID, conn.ApplicationName, util.FormatDuration(duration)))
			tc.warningSent = true
		}

		if !tc.criticalSent && duration >= cfg.Thresholds.IdleTransaction.Critical {
			logEvent("CRIT", fmt.Sprintf("PID %d (%s) idle for %s",
				conn.PID, conn.ApplicationName, util.FormatDuration(duration)))
			tc.criticalSent = true
		}
	}

	// Check for resolved transactions
	for pid, tc := range tracked {
		if !seenPIDs[pid] {
			totalDuration := time.Since(tc.firstSeen)
			logEvent("OK", fmt.Sprintf("Resolved: PID %d (%s) - was idle for %s",
				pid, tc.appName, util.FormatDuration(totalDuration)))
			delete(tracked, pid)
		}
	}

	// Check connection pool pressure
	usagePercent := stats.UsagePercent()
	if usagePercent >= float64(cfg.Thresholds.ConnectionPool.CriticalPercent) {
		logEvent("CRIT", fmt.Sprintf("Connection pressure: %d/%d (%.0f%%) - approaching limit!",
			stats.TotalConnections, stats.MaxConnections-stats.ReservedSuperuser, usagePercent))
	} else if usagePercent >= float64(cfg.Thresholds.ConnectionPool.WarningPercent) {
		logEvent("WARN", fmt.Sprintf("Connection pressure: %d/%d (%.0f%%)",
			stats.TotalConnections, stats.MaxConnections-stats.ReservedSuperuser, usagePercent))
	}

	return nil
}

func logEvent(level, message string) {
	timestamp := time.Now().Format("15:04:05")

	var prefix string
	switch level {
	case "WARN":
		prefix = "[!]"
	case "CRIT":
		prefix = "[X]"
	case "OK":
		prefix = "[+]"
	case "ERROR":
		prefix = "[E]"
	case "INFO":
		prefix = "[i]"
	default:
		prefix = "   "
	}

	// Handle multi-line messages (indent continuation lines)
	lines := strings.Split(message, "\n")
	for i, line := range lines {
		if i == 0 {
			fmt.Printf("%s %s %s\n", timestamp, prefix, line)
		} else {
			fmt.Printf("%s     %s\n", strings.Repeat(" ", len(timestamp)), line)
		}
	}
}
