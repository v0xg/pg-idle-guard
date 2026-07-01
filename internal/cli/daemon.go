package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/v0xg/pg-idle-guard/internal/alerts"
	"github.com/v0xg/pg-idle-guard/internal/cloud"
	"github.com/v0xg/pg-idle-guard/internal/config"
	"github.com/v0xg/pg-idle-guard/internal/metrics"
	"github.com/v0xg/pg-idle-guard/internal/postgres"
	"github.com/v0xg/pg-idle-guard/internal/report"
	"github.com/v0xg/pg-idle-guard/internal/secrets"
	"github.com/v0xg/pg-idle-guard/internal/util"
)

var slackClient *alerts.SlackClient
var webhookClient *alerts.WebhookClient
var cloudClient *cloud.Client

// reportStore records completed leaks when report.enabled is set; nil otherwise.
var reportStore *report.Store

// ongoingLeaks is a snapshot of still-open leaks past the warning threshold,
// rebuilt by the poll loop each cycle and read by the report scheduler — the
// scheduler never touches the tracked map directly.
var (
	ongoingMu    sync.Mutex
	ongoingLeaks []report.OngoingLeak
)

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
	daemonCmd.Flags().String("cloud-url", "", "pguard Cloud server URL; enables cloud reporting (e.g. https://cloud.pguard.dev)")
	daemonCmd.Flags().String("cloud-token", "", "pguard Cloud agent token (pgc_...); falls back to PGUARD_CLOUD_TOKEN")
	daemonCmd.Flags().String("cloud-instance", "", "instance name reported to the cloud (default: hostname)")
	rootCmd.AddCommand(daemonCmd)
}

// applyCloudFlags folds --cloud-* flags and PGUARD_CLOUD_* env vars into the
// loaded config. Precedence: flag > config file > env var. Passing --cloud-url
// (or setting cloud.enabled/url in config, or PGUARD_CLOUD_URL) turns cloud
// reporting on.
func applyCloudFlags(cmd *cobra.Command) {
	if v, _ := cmd.Flags().GetString("cloud-url"); v != "" {
		cfg.Cloud.URL = v
	} else if cfg.Cloud.URL == "" {
		cfg.Cloud.URL = os.Getenv("PGUARD_CLOUD_URL")
	}
	if v, _ := cmd.Flags().GetString("cloud-token"); v != "" {
		cfg.Cloud.Token = v
	} else if cfg.Cloud.Token == "" {
		cfg.Cloud.Token = os.Getenv("PGUARD_CLOUD_TOKEN")
	}
	if v, _ := cmd.Flags().GetString("cloud-instance"); v != "" {
		cfg.Cloud.Instance = v
	}
	if cfg.Cloud.URL != "" {
		cfg.Cloud.Enabled = true
	}
}

func runDaemon(cmd *cobra.Command, args []string) error {
	applyCloudFlags(cmd)

	// Validate config
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	closeLog, err := setupLogger(&cfg.Logging)
	if err != nil {
		return err
	}
	defer closeLog()

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

	if cfg.Cloud.Enabled {
		if cfg.Cloud.Token == "" {
			return fmt.Errorf("cloud reporting enabled but no agent token configured (set --cloud-token, cloud.token, or PGUARD_CLOUD_TOKEN)")
		}
		instance := cfg.Cloud.Instance
		if instance == "" {
			host, hostErr := os.Hostname()
			if hostErr != nil || host == "" {
				return fmt.Errorf("cloud instance name not set and hostname unavailable: %w", hostErr)
			}
			instance = host
		}
		cloudClient = cloud.NewClient(cfg.Cloud.URL, cfg.Cloud.Token, instance, Version)
		slog.Info("cloud reporting enabled",
			"url", cfg.Cloud.URL,
			"instance", instance,
			"allow_kill", cfg.Cloud.AllowKill)
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

	// Cloud command poller (kill commands issued from the dashboard).
	if cloudClient != nil {
		go cloudCommandLoop(ctx, cloudClient, client)
	}

	// Leak report: event recording + weekly digest scheduler
	if cfg.Report.Enabled {
		path := cfg.Report.DataFile
		if path == "" {
			defaultPath, pathErr := report.DefaultPath()
			if pathErr != nil {
				return fmt.Errorf("resolving report data path: %w", pathErr)
			}
			path = defaultPath
		}
		reportStore = report.NewStore(path)

		day, err := report.ParseWeekday(cfg.Report.Day)
		if err != nil {
			return fmt.Errorf("invalid report.day: %w", err)
		}
		slog.Info("leak report enabled",
			"day", cfg.Report.Day,
			"time", cfg.Report.Time,
			"retention_days", cfg.Report.RetentionDays,
			"data_file", path)
		go runReportScheduler(ctx, reportStore, day)
	}

	// Main monitoring loop
	slog.Info("daemon running", "polling_interval", cfg.Polling.Interval)
	return monitorLoop(ctx, client)
}

// reportWindowDays is the fixed trailing window of the weekly digest.
// Retention (report.retention_days) is longer so `pguard report --days` can
// look further back.
const reportWindowDays = 7

// runReportScheduler sleeps until the configured weekday+time, sends the
// digest, prunes old events, and repeats. There is no catch-up: a week where
// the daemon was down at the scheduled moment is skipped.
func runReportScheduler(ctx context.Context, store *report.Store, day time.Weekday) {
	for {
		next := report.NextRun(time.Now(), day, cfg.Report.Time)
		slog.Debug("next leak report scheduled", "at", next)

		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}

		sendLeakReport(store)
	}
}

// sendLeakReport reads the trailing window, sends the digest to all
// configured channels, and prunes events past retention. Each step is
// best-effort: a failure is logged and the rest still runs.
func sendLeakReport(store *report.Store) {
	now := time.Now()

	events, err := store.ReadSince(now.AddDate(0, 0, -reportWindowDays))
	if err != nil {
		slog.Error("leak report: reading events failed", "error", err)
		events = nil
	}
	summaries := report.Aggregate(events)
	ongoing := snapshotOngoing()

	if slackClient != nil {
		if err := slackClient.LeakReportDigest(summaries, ongoing, reportWindowDays); err != nil {
			slog.Error("failed to send slack leak report", "error", err)
		}
	}
	if webhookClient != nil {
		if err := webhookClient.LeakReportDigest(summaries, ongoing, reportWindowDays); err != nil {
			slog.Error("failed to send webhook leak report", "error", err)
		}
	}

	if err := store.Prune(now.AddDate(0, 0, -cfg.Report.RetentionDays)); err != nil {
		slog.Error("leak report: pruning events failed", "error", err)
	}
}

// trackedIdle keeps state for alerting
type trackedIdle struct {
	pid          int
	appName      string
	query        string
	firstSeen    time.Time
	maxDuration  time.Duration // max observed idle duration (pg state_change-based)
	warningSent  bool
	criticalSent bool
	terminated   bool // auto-terminated by pguard; event already recorded
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
			metrics.IncPolls()
			if err := pollAndAlert(ctx, client, tracked); err != nil {
				metrics.IncPollErrors()
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

	// Push a snapshot to the cloud first, so the instance is registered
	// before any leak events reference it.
	pushCloudSnapshot(ctx, stats, conns)

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
				query:     util.TruncateQuery(conn.Query, report.MaxQueryChars),
				firstSeen: time.Now(),
			}
			tracked[conn.PID] = tc
		}
		if duration > tc.maxDuration {
			tc.maxDuration = duration
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
			pushCloudLeakEvent(ctx, conn, duration, false)
			tc.criticalSent = true
		}

		// Auto-terminate if enabled
		if cfg.AutoTerm.Enabled && duration >= cfg.AutoTerm.After && !tc.terminated {
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
						metrics.IncTerminations()
						sendTerminationAlert(conn.PID, conn.ApplicationName, duration, "auto-terminate threshold exceeded")
						pushCloudLeakEvent(ctx, conn, duration, true)
						// Record now and flag the entry; the resolved sweep
						// still sends ResolvedAlert but must not double-record.
						recordLeakEvent(tc, true)
						tc.terminated = true
					}
				}
			}
		}
	}

	sweepResolved(tracked, seenPIDs)
	updateOngoing(tracked)

	return nil
}

// sweepResolved handles PIDs that disappeared since the last poll: it sends
// the resolved alert and records a leak event for connections that had
// crossed the warning threshold (unless already recorded at termination).
func sweepResolved(tracked map[int]*trackedIdle, seenPIDs map[int]bool) {
	for pid, tc := range tracked {
		if seenPIDs[pid] {
			continue
		}
		totalDuration := time.Since(tc.firstSeen)
		slog.Info("idle transaction resolved",
			"pid", pid,
			"app", tc.appName,
			"duration", util.FormatDuration(totalDuration))
		// Send resolved alert if we had sent warning/critical alerts
		if tc.warningSent || tc.criticalSent {
			sendResolvedAlert(pid, tc.appName, totalDuration)
		}
		if tc.warningSent && !tc.terminated {
			recordLeakEvent(tc, false)
		}
		delete(tracked, pid)
	}
}

// recordLeakEvent appends one completed leak to the report store. Recording
// is best-effort: failures are logged and must never disrupt monitoring.
func recordLeakEvent(tc *trackedIdle, terminated bool) {
	if reportStore == nil {
		return
	}
	e := report.Event{
		Time:       time.Now(),
		PID:        tc.pid,
		App:        tc.appName,
		Duration:   tc.maxDuration,
		Query:      tc.query,
		Terminated: terminated,
	}
	if err := reportStore.Append(e); err != nil {
		slog.Error("failed to record leak event", "pid", tc.pid, "error", err)
	}
}

// updateOngoing rebuilds the snapshot of still-open leaks past the warning
// threshold, oldest first.
func updateOngoing(tracked map[int]*trackedIdle) {
	if reportStore == nil {
		return
	}
	leaks := make([]report.OngoingLeak, 0, len(tracked))
	for _, tc := range tracked {
		if tc.warningSent && !tc.terminated {
			leaks = append(leaks, report.OngoingLeak{
				PID:      tc.pid,
				App:      tc.appName,
				Duration: tc.maxDuration,
				Query:    tc.query,
			})
		}
	}
	sort.Slice(leaks, func(i, j int) bool { return leaks[i].Duration > leaks[j].Duration })

	ongoingMu.Lock()
	ongoingLeaks = leaks
	ongoingMu.Unlock()
}

func snapshotOngoing() []report.OngoingLeak {
	ongoingMu.Lock()
	defer ongoingMu.Unlock()
	return slices.Clone(ongoingLeaks)
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
	metrics.IncAlerts(severity)
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
	metrics.IncAlerts(severity)
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

// pushCloudSnapshot reports the current pool state and idle-in-transaction
// backends to pguard Cloud. Best-effort: failures are logged, never fatal.
func pushCloudSnapshot(ctx context.Context, stats *postgres.PoolStats, conns []*postgres.Connection) {
	if cloudClient == nil {
		return
	}

	idleTxs := make([]cloud.IdleTx, 0, len(conns))
	for _, conn := range conns {
		idleTxs = append(idleTxs, cloud.IdleTx{
			PID:       conn.PID,
			App:       conn.ApplicationName,
			DurationS: conn.IdleDuration().Seconds(),
			Query:     util.Truncate(conn.Query, 500),
		})
	}

	snap := cloud.Snapshot{
		DatabaseName: cfg.Connection.Database,
		IntervalS:    cfg.Polling.Interval.Seconds(),
		TS:           time.Now().UTC(),
		Pool: cloud.PoolStats{
			Active:   stats.ActiveConnections,
			Idle:     stats.IdleConnections,
			IdleInTx: stats.IdleInTransaction,
			MaxConns: stats.MaxConnections,
		},
		IdleTxs: idleTxs,
	}

	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := cloudClient.PushSnapshot(reqCtx, snap); err != nil {
		slog.Warn("cloud snapshot push failed", "error", err)
	}
}

// pushCloudLeakEvent records a single leak event (a long-lived idle
// transaction, optionally terminated) with the cloud. Best-effort.
func pushCloudLeakEvent(ctx context.Context, conn *postgres.Connection, duration time.Duration, terminated bool) {
	if cloudClient == nil {
		return
	}
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err := cloudClient.PushEvents(reqCtx, []cloud.LeakEvent{{
		TS:         time.Now().UTC(),
		PID:        conn.PID,
		App:        conn.ApplicationName,
		DurationS:  duration.Seconds(),
		Query:      util.Truncate(conn.Query, 500),
		Terminated: terminated,
	}})
	if err != nil {
		slog.Warn("cloud leak event push failed", "pid", conn.PID, "error", err)
	}
}

// cloudCommandLoop long-polls the cloud for commands (kill requests issued from
// the dashboard) and executes them, reporting each result back. It runs until
// the context is canceled.
func cloudCommandLoop(ctx context.Context, cc *cloud.Client, pg *postgres.Client) {
	slog.Info("cloud command poller started", "instance", cc.Instance())
	const backoff = 5 * time.Second

	for {
		if ctx.Err() != nil {
			return
		}

		// The cloud holds the long-poll for ~25s; allow a little longer.
		pollCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
		cmds, err := cc.PollCommands(pollCtx)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// Until the first snapshot registers this instance the cloud
			// answers 422 "unknown instance"; that is expected startup
			// state, not a failure worth alarming on.
			var apiErr *cloud.APIError
			if errors.As(err, &apiErr) && apiErr.Status == http.StatusUnprocessableEntity {
				slog.Debug("cloud command poll: instance not registered yet", "error", err)
			} else {
				slog.Warn("cloud command poll failed", "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			continue
		}

		for _, cmd := range cmds {
			handleCloudCommand(ctx, cc, pg, cmd)
		}
	}
}

// handleCloudCommand executes a single command and reports its result. Only
// "kill" is supported; anything else is reported as failed so the dashboard
// clearly shows the command was not carried out.
func handleCloudCommand(ctx context.Context, cc *cloud.Client, pg *postgres.Client, cmd cloud.Command) {
	report := func(status, message string) {
		reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := cc.ReportResult(reqCtx, cmd.ID, status, message); err != nil {
			slog.Error("cloud command result report failed", "id", cmd.ID, "error", err)
		}
	}

	if cmd.Type != "kill" {
		slog.Warn("unsupported cloud command", "id", cmd.ID, "type", cmd.Type)
		report("failed", fmt.Sprintf("unsupported command type %q", cmd.Type))
		return
	}

	var payload cloud.KillPayload
	if err := json.Unmarshal(cmd.Payload, &payload); err != nil || payload.PID <= 0 {
		slog.Warn("invalid kill command payload", "id", cmd.ID)
		report("failed", "invalid kill payload: expected a positive pid")
		return
	}

	if !cfg.Cloud.AllowKill {
		slog.Warn("refusing cloud kill command; allow_kill is disabled", "id", cmd.ID, "pid", payload.PID)
		report("failed", "remote termination is disabled on this daemon (cloud.allow_kill=false)")
		return
	}

	slog.Warn("executing cloud kill command", "id", cmd.ID, "pid", payload.PID)
	killCtx, cancel := context.WithTimeout(ctx, cfg.Polling.Timeout)
	defer cancel()
	success, err := pg.TerminateBackend(killCtx, payload.PID)
	switch {
	case err != nil:
		slog.Error("cloud kill failed", "id", cmd.ID, "pid", payload.PID, "error", err)
		report("failed", fmt.Sprintf("pg_terminate_backend(%d) failed: %v", payload.PID, err))
	case !success:
		slog.Info("cloud kill: backend not found", "id", cmd.ID, "pid", payload.PID)
		report("failed", fmt.Sprintf("backend %d not found (already gone)", payload.PID))
	default:
		slog.Info("cloud kill succeeded", "id", cmd.ID, "pid", payload.PID)
		report("done", fmt.Sprintf("terminated backend %d", payload.PID))
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

	// Prometheus metrics endpoint
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		// On query failure, still serve daemon counters with pguard_up 0 so
		// Prometheus can alert on database unreachability.
		stats, err := client.GetPoolStats(ctx)
		if err != nil {
			slog.Error("metrics: pool stats query failed", "error", err)
			stats = nil
		}
		var idle []*postgres.Connection
		if stats != nil {
			if idle, err = client.GetIdleTransactions(ctx); err != nil {
				slog.Error("metrics: idle transactions query failed", "error", err)
				stats = nil
			}
		}

		w.Header().Set("Content-Type", metrics.ContentType)
		fmt.Fprint(w, metrics.Render(stats, idle))
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

// setupLogger configures slog based on cfg and returns a cleanup function that
// closes the log file if one was opened.
func setupLogger(cfg *config.LoggingConfig) (func(), error) {
	var w io.Writer
	cleanup := func() {}

	switch cfg.Output {
	case "stdout":
		w = os.Stdout
	case "", "stderr":
		w = os.Stderr
	default:
		f, err := os.OpenFile(cfg.Output, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, fmt.Errorf("opening log file %s: %w", cfg.Output, err)
		}
		w = f
		cleanup = func() { f.Close() }
	}

	var level slog.Level
	switch strings.ToLower(cfg.Level) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if strings.EqualFold(cfg.Format, "json") {
		handler = slog.NewJSONHandler(w, opts)
	} else {
		handler = slog.NewTextHandler(w, opts)
	}

	slog.SetDefault(slog.New(handler))
	return cleanup, nil
}
