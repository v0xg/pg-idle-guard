package cli

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/v0xg/pg-idle-guard/internal/alerts"
	"github.com/v0xg/pg-idle-guard/internal/postgres"
	"github.com/v0xg/pg-idle-guard/internal/secrets"
	"github.com/v0xg/pg-idle-guard/internal/util"
)

var slackClient *alerts.SlackClient
var webhookClient *alerts.WebhookClient

// alertCooldown tracks last alert times to prevent spam
type alertCooldown struct {
	lastPoolWarning  time.Time
	lastPoolCritical time.Time
	// Per-PID tracking for idle transaction alerts is handled by trackedIdle.warningSent/criticalSent
}

var cooldown = &alertCooldown{}

// canSendPoolAlert checks if enough time has passed since the last pool alert
func (a *alertCooldown) canSendPoolAlert(severity string, cooldownDuration time.Duration) bool {
	now := time.Now()
	switch severity {
	case alerts.SeverityWarning:
		if now.Sub(a.lastPoolWarning) >= cooldownDuration {
			a.lastPoolWarning = now
			return true
		}
	case alerts.SeverityCritical:
		if now.Sub(a.lastPoolCritical) >= cooldownDuration {
			a.lastPoolCritical = now
			return true
		}
	}
	return false
}

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Run as a background service",
	Long: `Run pguard as a long-running daemon that continuously monitors
PostgreSQL connections and sends alerts when thresholds are exceeded.

This is the recommended mode for production deployments.`,
	RunE: runDaemon,
}

func init() {
	rootCmd.AddCommand(daemonCmd)
}

func runDaemon(cmd *cobra.Command, args []string) error {
	// Validate config
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	// Create PostgreSQL client
	client, err := postgres.NewClient(cfg)
	if err != nil {
		return fmt.Errorf("connecting to database: %w", err)
	}
	defer client.Close()

	slog.Info("pguard daemon starting")
	slog.Info("connected to PostgreSQL")
	slog.Info("configuration loaded",
		"polling_interval", cfg.Polling.Interval,
		"warning_threshold", cfg.Thresholds.IdleTransaction.Warning,
		"critical_threshold", cfg.Thresholds.IdleTransaction.Critical,
		"alert_cooldown", cfg.Alerts.Cooldown)

	if cfg.AutoTerm.Enabled {
		if cfg.AutoTerm.DryRun {
			slog.Info("auto-terminate enabled", "mode", "dry-run")
		} else {
			slog.Info("auto-terminate enabled", "after", cfg.AutoTerm.After)
		}
	}

	if cfg.Alerts.Slack.Enabled {
		webhookURL := cfg.Alerts.Slack.WebhookURL
		if webhookURL == "" {
			webhookURL = os.Getenv("SLACK_WEBHOOK_URL")
		}
		// Try to resolve from Secrets Manager if webhook_secret is configured
		if webhookURL == "" && cfg.Alerts.Slack.WebhookSecret != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			resolvedURL, resolveErr := secrets.ResolveWebhookSecret(ctx, cfg.Alerts.Slack.WebhookSecret, cfg.Connection.AWSRegion)
			cancel()
			if resolveErr != nil {
				slog.Error("failed to resolve slack webhook from secrets manager", "error", resolveErr)
			} else {
				webhookURL = resolvedURL
			}
		}
		if webhookURL != "" {
			slackClient = alerts.NewSlackClient(
				webhookURL,
				cfg.Alerts.Slack.Channel,
				cfg.Alerts.Slack.MentionUsers,
			)
			slog.Info("slack alerts enabled", "channel", cfg.Alerts.Slack.Channel)

			// Send test message
			if err := slackClient.TestConnection(); err != nil {
				slog.Warn("slack test failed", "error", err)
			}
		} else {
			slog.Warn("slack enabled but no webhook URL configured")
		}
	}

	if cfg.Alerts.Webhook.Enabled {
		url := cfg.Alerts.Webhook.URL
		if url == "" {
			url = os.Getenv("WEBHOOK_URL")
		}
		if url != "" {
			webhookClient = alerts.NewWebhookClient(
				url,
				cfg.Alerts.Webhook.Method,
				cfg.Alerts.Webhook.Headers,
			)
			slog.Info("webhook alerts enabled", "url", url, "method", cfg.Alerts.Webhook.Method)

			// Send test message
			if err := webhookClient.TestConnection(); err != nil {
				slog.Warn("webhook test failed", "error", err)
			}
		} else {
			slog.Warn("webhook enabled but no URL configured")
		}
	}

	// Handle shutdown signals
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Start HTTP server for health checks
	var httpServer *http.Server
	if cfg.API.Enabled {
		httpServer = startHTTPServer(cfg.API.Listen, client)
		slog.Info("HTTP API listening", "address", cfg.API.Listen)
	}

	go func() {
		sig := <-sigCh
		slog.Info("received shutdown signal", "signal", sig)

		// Gracefully shutdown HTTP server
		if httpServer != nil {
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer shutdownCancel()
			if err := httpServer.Shutdown(shutdownCtx); err != nil {
				slog.Error("HTTP server shutdown failed", "error", err)
			}
		}

		cancel()
	}()

	// Main monitoring loop
	slog.Info("daemon running", "polling_interval", cfg.Polling.Interval)
	return monitorLoop(ctx, client)
}

// trackedIdle keeps state for alerting
type trackedIdle struct {
	pid          int
	appName      string
	query        string
	firstSeen    time.Time
	warningSent  bool
	criticalSent bool
}

func monitorLoop(ctx context.Context, client *postgres.Client) error {
	ticker := time.NewTicker(cfg.Polling.Interval)
	defer ticker.Stop()

	tracked := make(map[int]*trackedIdle)

	for {
		select {
		case <-ctx.Done():
			slog.Info("daemon stopped")
			return nil
		case <-ticker.C:
			if err := pollAndAlert(ctx, client, tracked); err != nil {
				slog.Error("polling failed", "error", err)
			}
		}
	}
}

func pollAndAlert(ctx context.Context, client *postgres.Client, tracked map[int]*trackedIdle) error {
	queryCtx, cancel := context.WithTimeout(ctx, cfg.Polling.Timeout)
	defer cancel()

	// Get pool stats
	stats, err := client.GetPoolStats(queryCtx)
	if err != nil {
		return err
	}

	// Check connection pool thresholds
	usagePercent := stats.UsagePercent()
	maxAvailable := stats.MaxConnections - stats.ReservedSuperuser
	if usagePercent >= float64(cfg.Thresholds.ConnectionPool.CriticalPercent) {
		slog.Error("connection pool critical",
			"usage_percent", usagePercent,
			"used", stats.TotalConnections,
			"max", maxAvailable)
		if cooldown.canSendPoolAlert(alerts.SeverityCritical, cfg.Alerts.Cooldown) {
			sendPoolAlert(alerts.SeverityCritical, stats.TotalConnections, maxAvailable, usagePercent)
		}
	} else if usagePercent >= float64(cfg.Thresholds.ConnectionPool.WarningPercent) {
		slog.Warn("connection pool warning",
			"usage_percent", usagePercent,
			"used", stats.TotalConnections,
			"max", maxAvailable)
		if cooldown.canSendPoolAlert(alerts.SeverityWarning, cfg.Alerts.Cooldown) {
			sendPoolAlert(alerts.SeverityWarning, stats.TotalConnections, maxAvailable, usagePercent)
		}
	}

	// Get idle transactions
	conns, err := client.GetIdleTransactions(queryCtx)
	if err != nil {
		return err
	}

	// Track which PIDs we see
	seenPIDs := make(map[int]bool)

	for _, conn := range conns {
		seenPIDs[conn.PID] = true
		duration := conn.IdleDuration()

		tc, exists := tracked[conn.PID]
		if !exists {
			tc = &trackedIdle{
				pid:       conn.PID,
				appName:   conn.ApplicationName,
				query:     util.TruncateQuery(conn.Query, 100),
				firstSeen: time.Now(),
			}
			tracked[conn.PID] = tc
		}

		// Check for warning threshold
		if !tc.warningSent && duration >= cfg.Thresholds.IdleTransaction.Warning {
			slog.Warn("idle transaction detected",
				"pid", conn.PID,
				"app", conn.ApplicationName,
				"duration", util.FormatDuration(duration))
			sendIdleTransactionAlert(alerts.SeverityWarning, conn.PID, conn.ApplicationName, duration, conn.Query)
			tc.warningSent = true
		}

		// Check for critical threshold
		if !tc.criticalSent && duration >= cfg.Thresholds.IdleTransaction.Critical {
			slog.Error("idle transaction critical",
				"pid", conn.PID,
				"app", conn.ApplicationName,
				"duration", util.FormatDuration(duration))
			sendIdleTransactionAlert(alerts.SeverityCritical, conn.PID, conn.ApplicationName, duration, conn.Query)
			tc.criticalSent = true
		}

		// Auto-terminate if enabled
		if cfg.AutoTerm.Enabled && duration >= cfg.AutoTerm.After {
			if shouldTerminate(conn, duration) {
				if cfg.AutoTerm.DryRun {
					slog.Info("dry-run: would terminate",
						"pid", conn.PID,
						"app", conn.ApplicationName,
						"duration", util.FormatDuration(duration))
				} else {
					slog.Warn("auto-terminating connection",
						"pid", conn.PID,
						"app", conn.ApplicationName,
						"duration", util.FormatDuration(duration))
					if success, err := client.TerminateBackend(queryCtx, conn.PID); err != nil {
						slog.Error("failed to terminate backend", "pid", conn.PID, "error", err)
					} else if success {
						sendTerminationAlert(conn.PID, conn.ApplicationName, duration, "auto-terminate threshold exceeded")
					}
				}
			}
		}
	}

	// Check for resolved transactions
	for pid, tc := range tracked {
		if !seenPIDs[pid] {
			totalDuration := time.Since(tc.firstSeen)
			slog.Info("idle transaction resolved",
				"pid", pid,
				"app", tc.appName,
				"duration", util.FormatDuration(totalDuration))
			// Send resolved alert if we had sent warning/critical alerts
			if tc.warningSent || tc.criticalSent {
				sendResolvedAlert(pid, tc.appName, totalDuration)
			}
			delete(tracked, pid)
		}
	}

	return nil
}

func shouldTerminate(conn *postgres.Connection, duration time.Duration) bool {
	// Check exclusion list
	for _, excluded := range cfg.AutoTerm.ExcludeApps {
		if conn.ApplicationName == excluded {
			return false
		}
	}

	// Check excluded IPs
	for _, excludedIP := range cfg.AutoTerm.ExcludeIPs {
		if conn.ClientAddr == excludedIP {
			return false
		}
	}

	// Check protected apps with custom thresholds
	for _, protected := range cfg.AutoTerm.ProtectedApps {
		if conn.ApplicationName == protected.Name {
			// If RequireConfirmation is set, never auto-terminate (requires manual intervention)
			if protected.RequireConfirmation {
				slog.Debug("skipping protected app requiring confirmation",
					"pid", conn.PID,
					"app", conn.ApplicationName)
				return false
			}
			// Only terminate if duration exceeds the app-specific threshold
			if duration < protected.MinIdleDuration {
				slog.Debug("protected app under threshold",
					"pid", conn.PID,
					"app", conn.ApplicationName,
					"duration", util.FormatDuration(duration),
					"threshold", util.FormatDuration(protected.MinIdleDuration))
				return false
			}
			// Duration exceeds protected app threshold, allow termination
			slog.Info("protected app exceeded custom threshold",
				"pid", conn.PID,
				"app", conn.ApplicationName,
				"duration", util.FormatDuration(duration),
				"threshold", util.FormatDuration(protected.MinIdleDuration))
			return true
		}
	}

	return true
}

// Alert helper functions - send to all configured channels

func sendPoolAlert(severity string, used, maxConns int, percent float64) {
	if slackClient != nil {
		if err := slackClient.ConnectionPoolAlert(severity, used, maxConns, percent); err != nil {
			slog.Error("failed to send slack alert", "error", err)
		}
	}
	if webhookClient != nil {
		if err := webhookClient.ConnectionPoolAlert(severity, used, maxConns, percent); err != nil {
			slog.Error("failed to send webhook alert", "error", err)
		}
	}
}

func sendIdleTransactionAlert(severity string, pid int, appName string, duration time.Duration, query string) {
	if slackClient != nil {
		if err := slackClient.IdleTransactionAlert(severity, pid, appName, duration, query); err != nil {
			slog.Error("failed to send slack alert", "error", err)
		}
	}
	if webhookClient != nil {
		if err := webhookClient.IdleTransactionAlert(severity, pid, appName, duration, query); err != nil {
			slog.Error("failed to send webhook alert", "error", err)
		}
	}
}

func sendTerminationAlert(pid int, appName string, duration time.Duration, reason string) {
	if slackClient != nil {
		if err := slackClient.TerminationAlert(pid, appName, duration, reason); err != nil {
			slog.Error("failed to send slack alert", "error", err)
		}
	}
	if webhookClient != nil {
		if err := webhookClient.TerminationAlert(pid, appName, duration, reason); err != nil {
			slog.Error("failed to send webhook alert", "error", err)
		}
	}
}

func sendResolvedAlert(pid int, appName string, duration time.Duration) {
	if slackClient != nil {
		if err := slackClient.ResolvedAlert(pid, appName, duration); err != nil {
			slog.Error("failed to send slack alert", "error", err)
		}
	}
	if webhookClient != nil {
		if err := webhookClient.ResolvedAlert(pid, appName, duration); err != nil {
			slog.Error("failed to send webhook alert", "error", err)
		}
	}
}

func startHTTPServer(listen string, client *postgres.Client) *http.Server {
	mux := http.NewServeMux()

	// Health check
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		if err := client.Ping(ctx); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, "unhealthy: %v", err)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})

	// Status endpoint
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		stats, err := client.GetPoolStats(ctx)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, "error: %v", err)
			return
		}

		idle, _ := client.GetIdleTransactions(ctx)

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"max_connections":%d,"total":%d,"active":%d,"idle":%d,"idle_in_transaction":%d,"available":%d,"idle_transactions_count":%d}`,
			stats.MaxConnections,
			stats.TotalConnections,
			stats.ActiveConnections,
			stats.IdleConnections,
			stats.IdleInTransaction,
			stats.AvailableConnections,
			len(idle),
		)
	})

	server := &http.Server{
		Addr:         listen,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("HTTP server error", "error", err)
		}
	}()

	return server
}
