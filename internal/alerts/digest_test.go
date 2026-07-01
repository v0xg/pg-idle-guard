package alerts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/v0xg/pg-idle-guard/internal/report"
)

func TestSlackClient_LeakReportDigest(t *testing.T) {
	var received SlackMessage

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("failed to decode request: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewSlackClient(server.URL, "#alerts", []string{"@oncall"})

	summaries := []report.AppSummary{
		{App: "payment-api", Count: 47, MedianDuration: 4 * time.Minute, MaxDuration: 32 * time.Minute, TerminatedCount: 3, TopQuery: "UPDATE accounts"},
		{App: "", Count: 2, MedianDuration: time.Minute, MaxDuration: 2 * time.Minute, TopQuery: "COMMIT"},
	}
	ongoing := []report.OngoingLeak{
		{PID: 4711, App: "batch-job", Duration: 18 * time.Hour, Query: "DELETE FROM x"},
	}

	if err := client.LeakReportDigest(summaries, ongoing, 7); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(received.Attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(received.Attachments))
	}
	att := received.Attachments[0]

	if att.Color != severityColors[SeverityInfo] {
		t.Errorf("expected info color, got %s", att.Color)
	}
	if !strings.Contains(att.Title, "Weekly Leak Report") {
		t.Errorf("unexpected title %q", att.Title)
	}
	if received.Text != "" {
		t.Errorf("digest must not @-mention anyone, got text %q", received.Text)
	}
	for _, want := range []string{
		"payment-api", "47 leaks", "median 4m 0s", "max 32m 0s", "3 terminated",
		"UPDATE accounts", "(unknown)", "*Ongoing* — 1 still open", "pid 4711",
	} {
		if !strings.Contains(att.Text, want) {
			t.Errorf("digest text missing %q:\n%s", want, att.Text)
		}
	}
}

func TestSlackClient_LeakReportDigest_Empty(t *testing.T) {
	var received SlackMessage

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("failed to decode request: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewSlackClient(server.URL, "#alerts", nil)

	if err := client.LeakReportDigest(nil, nil, 7); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(received.Attachments[0].Text, "No idle transaction leaks") {
		t.Errorf("empty week should send the pipeline-alive message, got %q", received.Attachments[0].Text)
	}
}

func TestSlackClient_LeakReportDigest_OngoingOnly(t *testing.T) {
	var received SlackMessage

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("failed to decode request: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewSlackClient(server.URL, "#alerts", nil)

	ongoing := []report.OngoingLeak{{PID: 1, App: "stuck-app", Duration: 3 * time.Hour}}
	if err := client.LeakReportDigest(nil, ongoing, 7); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	text := received.Attachments[0].Text
	if strings.Contains(text, "No idle transaction leaks") {
		t.Errorf("week with ongoing leaks must not claim no leaks:\n%s", text)
	}
	if !strings.HasPrefix(text, "*Ongoing*") {
		t.Errorf("ongoing section should lead when there are no completed events:\n%s", text)
	}
}

func TestSlackClient_LeakReportDigest_Caps(t *testing.T) {
	var received SlackMessage

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("failed to decode request: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewSlackClient(server.URL, "#alerts", nil)

	summaries := make([]report.AppSummary, 13)
	for i := range summaries {
		summaries[i] = report.AppSummary{App: "app", Count: 13 - i}
	}
	ongoing := make([]report.OngoingLeak, 8)
	for i := range ongoing {
		ongoing[i] = report.OngoingLeak{PID: i, App: "app"}
	}

	if err := client.LeakReportDigest(summaries, ongoing, 7); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	text := received.Attachments[0].Text
	if !strings.Contains(text, "…and 3 more") {
		t.Errorf("expected app overflow line:\n%s", text)
	}
	if strings.Count(text, "…and 3 more") != 2 { // 13-10 apps and 8-5 ongoing both overflow by 3
		t.Errorf("expected ongoing overflow line too:\n%s", text)
	}
}

func TestWebhookClient_LeakReportDigest(t *testing.T) {
	var received WebhookPayload

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("failed to decode request: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewWebhookClient(server.URL, "POST", nil)

	summaries := []report.AppSummary{
		{App: "payment-api", Count: 47, MedianDuration: 4 * time.Minute, MaxDuration: 32 * time.Minute, TerminatedCount: 3, TopQuery: "UPDATE accounts"},
	}
	ongoing := []report.OngoingLeak{
		{PID: 4711, App: "batch-job", Duration: 90 * time.Second, Query: "DELETE FROM x"},
	}

	if err := client.LeakReportDigest(summaries, ongoing, 7); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if received.Event != "leak_report" {
		t.Errorf("event = %q, want leak_report", received.Event)
	}
	if received.Severity != SeverityInfo {
		t.Errorf("severity = %q, want info", received.Severity)
	}
	if received.Data["window_days"] != 7.0 {
		t.Errorf("window_days = %v, want 7", received.Data["window_days"])
	}

	apps, ok := received.Data["apps"].([]interface{})
	if !ok || len(apps) != 1 {
		t.Fatalf("apps = %v, want 1 entry", received.Data["apps"])
	}
	app, ok := apps[0].(map[string]interface{})
	if !ok {
		t.Fatalf("app entry not an object: %v", apps[0])
	}
	if app["median_duration_s"] != 240.0 {
		t.Errorf("median_duration_s = %v, want 240 (fractional seconds)", app["median_duration_s"])
	}

	ong, ok := received.Data["ongoing"].([]interface{})
	if !ok || len(ong) != 1 {
		t.Fatalf("ongoing = %v, want 1 entry", received.Data["ongoing"])
	}
	leak, ok := ong[0].(map[string]interface{})
	if !ok {
		t.Fatalf("ongoing entry not an object: %v", ong[0])
	}
	if leak["duration_s"] != 90.0 {
		t.Errorf("ongoing duration_s = %v, want 90", leak["duration_s"])
	}
}

func TestWebhookClient_LeakReportDigest_EmptyArrays(t *testing.T) {
	var raw map[string]json.RawMessage

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Data map[string]json.RawMessage `json:"data"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("failed to decode request: %v", err)
		}
		raw = payload.Data
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewWebhookClient(server.URL, "POST", nil)
	if err := client.LeakReportDigest(nil, nil, 7); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// nil slices must serialize as [] not null, for consumer friendliness.
	if string(raw["apps"]) != "[]" {
		t.Errorf("apps = %s, want []", raw["apps"])
	}
	if string(raw["ongoing"]) != "[]" {
		t.Errorf("ongoing = %s, want []", raw["ongoing"])
	}
}
