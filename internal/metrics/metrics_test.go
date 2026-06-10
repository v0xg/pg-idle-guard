package metrics

import (
	"strings"
	"testing"
	"time"

	"github.com/v0xg/pg-idle-guard/internal/postgres"
)

func testStats() *postgres.PoolStats {
	return &postgres.PoolStats{
		MaxConnections:       100,
		ReservedSuperuser:    3,
		TotalConnections:     43,
		ActiveConnections:    23,
		IdleConnections:      12,
		IdleInTransaction:    8,
		AvailableConnections: 57,
	}
}

func idleConn(app string, age time.Duration) *postgres.Connection {
	return &postgres.Connection{
		ApplicationName: app,
		State:           postgres.StateIdleInTransaction,
		StateChange:     time.Now().Add(-age),
	}
}

func TestRenderPoolStats(t *testing.T) {
	out := Render(testStats(), nil)

	expected := []string{
		"pguard_up 1\n",
		"pguard_max_connections 100\n",
		`pguard_connections{state="active"} 23` + "\n",
		`pguard_connections{state="idle"} 12` + "\n",
		`pguard_connections{state="idle_in_transaction"} 8` + "\n",
		"pguard_connections_used 43\n",
		"pguard_connections_available 57\n",
		"# TYPE pguard_pool_usage_ratio gauge\n",
		"pguard_idle_transaction_oldest_seconds 0\n",
	}
	for _, want := range expected {
		if !strings.Contains(out, want) {
			t.Errorf("Render output missing %q\noutput:\n%s", want, out)
		}
	}
}

func TestRenderIdleTransactionsByApp(t *testing.T) {
	idle := []*postgres.Connection{
		idleConn("payment-api", 4*time.Minute),
		idleConn("payment-api", 30*time.Second),
		idleConn("user-service", 2*time.Minute),
	}
	out := Render(testStats(), idle)

	if !strings.Contains(out, `pguard_idle_transactions{application="payment-api"} 2`) {
		t.Errorf("missing payment-api series:\n%s", out)
	}
	if !strings.Contains(out, `pguard_idle_transactions{application="user-service"} 1`) {
		t.Errorf("missing user-service series:\n%s", out)
	}
	// Apps must be sorted for deterministic scrapes
	if strings.Index(out, "payment-api") > strings.Index(out, "user-service") {
		t.Errorf("application series not sorted:\n%s", out)
	}
	// Oldest is ~240s; allow scheduling slack
	if !strings.Contains(out, "pguard_idle_transaction_oldest_seconds 24") {
		t.Errorf("unexpected oldest seconds:\n%s", out)
	}
}

func TestRenderUnreachableDatabase(t *testing.T) {
	out := Render(nil, nil)

	if !strings.Contains(out, "pguard_up 0\n") {
		t.Errorf("expected pguard_up 0:\n%s", out)
	}
	if strings.Contains(out, "pguard_max_connections") {
		t.Errorf("pool gauges must be omitted when stats are unavailable:\n%s", out)
	}
	// Daemon counters must survive database outages
	if !strings.Contains(out, "pguard_polls_total") {
		t.Errorf("counters missing when database unreachable:\n%s", out)
	}
}

func TestCounters(t *testing.T) {
	before := Render(nil, nil)

	IncPolls()
	IncPollErrors()
	IncAlerts("warning")
	IncAlerts("critical")
	IncAlerts("critical")
	IncAlerts("info") // not tracked, must not panic or count
	IncTerminations()

	after := Render(nil, nil)

	checks := []struct {
		metric string
		delta  int64
	}{
		{"pguard_polls_total", 1},
		{"pguard_poll_errors_total", 1},
		{`pguard_alerts_sent_total{severity="warning"}`, 1},
		{`pguard_alerts_sent_total{severity="critical"}`, 2},
		{"pguard_terminations_total", 1},
	}
	for _, c := range checks {
		if got := metricValue(t, after, c.metric) - metricValue(t, before, c.metric); got != c.delta {
			t.Errorf("%s: delta = %d, want %d", c.metric, got, c.delta)
		}
	}
}

func TestEscapeLabelValue(t *testing.T) {
	idle := []*postgres.Connection{idleConn("app\"with\\bad\nname", time.Minute)}
	out := Render(testStats(), idle)

	want := `pguard_idle_transactions{application="app\"with\\bad\nname"} 1`
	if !strings.Contains(out, want) {
		t.Errorf("label value not escaped, want %q in:\n%s", want, out)
	}
}

// metricValue extracts the integer value of a metric line by its name (and
// labels, if any) from rendered output.
func metricValue(t *testing.T, output, metric string) int64 {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		rest, ok := strings.CutPrefix(line, metric+" ")
		if !ok {
			continue
		}
		var v int64
		for _, ch := range rest {
			if ch < '0' || ch > '9' {
				t.Fatalf("non-integer value %q for %s", rest, metric)
			}
			v = v*10 + int64(ch-'0')
		}
		return v
	}
	t.Fatalf("metric %s not found in output:\n%s", metric, output)
	return 0
}
