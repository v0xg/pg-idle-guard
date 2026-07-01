// Package metrics exposes pguard state in the Prometheus text exposition
// format (version 0.0.4). The format is hand-rendered to keep pguard free of
// the prometheus/client_golang dependency tree.
package metrics

import (
	"fmt"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/v0xg/pg-idle-guard/internal/postgres"
)

// ContentType is the Content-Type header for the Prometheus text format.
const ContentType = "text/plain; version=0.0.4; charset=utf-8"

// Daemon activity counters, cumulative since process start. They are updated
// by the monitoring loop and read by the /metrics handler, hence atomics.
var (
	pollsTotal          atomic.Int64
	pollErrorsTotal     atomic.Int64
	alertsWarningTotal  atomic.Int64
	alertsCriticalTotal atomic.Int64
	terminationsTotal   atomic.Int64
)

// IncPolls records one iteration of the monitoring loop.
func IncPolls() { pollsTotal.Add(1) }

// IncPollErrors records a failed iteration of the monitoring loop.
func IncPollErrors() { pollErrorsTotal.Add(1) }

// IncAlerts records a dispatched alert. Severity values other than
// "warning" and "critical" are ignored.
func IncAlerts(severity string) {
	switch severity {
	case "warning":
		alertsWarningTotal.Add(1)
	case "critical":
		alertsCriticalTotal.Add(1)
	}
}

// IncTerminations records an auto-terminated backend.
func IncTerminations() { terminationsTotal.Add(1) }

// Render produces the full /metrics payload. stats may be nil when the
// database is unreachable; pguard_up reports 0 and only daemon counters are
// emitted so Prometheus can still alert on scrape data.
func Render(stats *postgres.PoolStats, idle []*postgres.Connection) string {
	var b strings.Builder

	up := 0
	if stats != nil {
		up = 1
	}
	writeMetric(&b, "pguard_up", "gauge", "Whether the last query of PostgreSQL succeeded.")
	fmt.Fprintf(&b, "pguard_up %d\n", up)

	if stats != nil {
		writeMetric(&b, "pguard_max_connections", "gauge", "PostgreSQL max_connections setting.")
		fmt.Fprintf(&b, "pguard_max_connections %d\n", stats.MaxConnections)

		writeMetric(&b, "pguard_connections", "gauge", "Current backend connections by state.")
		fmt.Fprintf(&b, "pguard_connections{state=\"active\"} %d\n", stats.ActiveConnections)
		fmt.Fprintf(&b, "pguard_connections{state=\"idle\"} %d\n", stats.IdleConnections)
		fmt.Fprintf(&b, "pguard_connections{state=\"idle_in_transaction\"} %d\n", stats.IdleInTransaction)

		writeMetric(&b, "pguard_connections_used", "gauge", "Total backend connections currently open.")
		fmt.Fprintf(&b, "pguard_connections_used %d\n", stats.TotalConnections)

		writeMetric(&b, "pguard_connections_available", "gauge", "Connection slots still available to non-superusers.")
		fmt.Fprintf(&b, "pguard_connections_available %d\n", stats.AvailableConnections)

		writeMetric(&b, "pguard_pool_usage_ratio", "gauge", "Fraction of the non-reserved connection pool in use (0-1).")
		fmt.Fprintf(&b, "pguard_pool_usage_ratio %g\n", stats.UsagePercent()/100)

		writeIdleTransactions(&b, idle)
	}

	writeMetric(&b, "pguard_polls_total", "counter", "Monitoring loop iterations since the daemon started.")
	fmt.Fprintf(&b, "pguard_polls_total %d\n", pollsTotal.Load())

	writeMetric(&b, "pguard_poll_errors_total", "counter", "Monitoring loop iterations that failed since the daemon started.")
	fmt.Fprintf(&b, "pguard_poll_errors_total %d\n", pollErrorsTotal.Load())

	writeMetric(&b, "pguard_alerts_sent_total", "counter", "Alerts dispatched since the daemon started, by severity.")
	fmt.Fprintf(&b, "pguard_alerts_sent_total{severity=\"warning\"} %d\n", alertsWarningTotal.Load())
	fmt.Fprintf(&b, "pguard_alerts_sent_total{severity=\"critical\"} %d\n", alertsCriticalTotal.Load())

	writeMetric(&b, "pguard_terminations_total", "counter", "Backends auto-terminated since the daemon started.")
	fmt.Fprintf(&b, "pguard_terminations_total %d\n", terminationsTotal.Load())

	return b.String()
}

// writeIdleTransactions emits idle-in-transaction gauges, broken down by
// application_name so leaks can be attributed to a service.
func writeIdleTransactions(b *strings.Builder, idle []*postgres.Connection) {
	byApp := make(map[string]int)
	var oldest float64
	for _, conn := range idle {
		byApp[conn.ApplicationName]++
		if age := conn.IdleDuration().Seconds(); age > oldest {
			oldest = age
		}
	}

	apps := make([]string, 0, len(byApp))
	for app := range byApp {
		apps = append(apps, app)
	}
	sort.Strings(apps)

	writeMetric(b, "pguard_idle_transactions", "gauge", "Idle-in-transaction connections by application.")
	for _, app := range apps {
		fmt.Fprintf(b, "pguard_idle_transactions{application=\"%s\"} %d\n", escapeLabelValue(app), byApp[app])
	}

	writeMetric(b, "pguard_idle_transaction_oldest_seconds", "gauge", "Age of the oldest idle-in-transaction connection.")
	fmt.Fprintf(b, "pguard_idle_transaction_oldest_seconds %g\n", oldest)
}

func writeMetric(b *strings.Builder, name, typ, help string) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
}

var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

func escapeLabelValue(v string) string {
	return labelEscaper.Replace(v)
}
