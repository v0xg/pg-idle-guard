package alerts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/v0xg/pg-idle-guard/internal/util"
)

func TestSlackClient_IdleTransactionAlert(t *testing.T) {
	var received SlackMessage

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewSlackClient(server.URL, "#test-channel", []string{"@oncall"})

	err := client.IdleTransactionAlert(
		SeverityCritical,
		12345,
		"payment-api",
		5*time.Minute,
		"UPDATE accounts SET balance = balance + 100",
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if received.Channel != "#test-channel" {
		t.Errorf("expected channel #test-channel, got %s", received.Channel)
	}

	if len(received.Attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(received.Attachments))
	}

	att := received.Attachments[0]
	if att.Color != "#FF0000" {
		t.Errorf("expected red color for critical, got %s", att.Color)
	}

	// Check that mention was included for critical
	if received.Text != "@oncall " {
		t.Errorf("expected mention text '@oncall ', got '%s'", received.Text)
	}
}

func TestSlackClient_ConnectionPoolAlert(t *testing.T) {
	var received SlackMessage

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("failed to decode request: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewSlackClient(server.URL, "#alerts", nil)

	err := client.ConnectionPoolAlert(SeverityWarning, 75, 100, 75.0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if received.Attachments[0].Color != "#FFA500" {
		t.Errorf("expected orange color for warning, got %s", received.Attachments[0].Color)
	}
}

func TestSlackClient_TerminationAlert(t *testing.T) {
	var received SlackMessage

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("failed to decode request: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewSlackClient(server.URL, "#alerts", nil)

	err := client.TerminationAlert(12345, "stuck-app", 10*time.Minute, "exceeded critical threshold")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(received.Attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(received.Attachments))
	}

	att := received.Attachments[0]
	if att.Title != "Connection Terminated" {
		t.Errorf("expected title 'Connection Terminated', got %s", att.Title)
	}

	// Verify fields contain expected data
	foundPID := false
	foundApp := false
	for _, field := range att.Fields {
		if field.Title == "PID" && field.Value == "12345" {
			foundPID = true
		}
		if field.Title == "Application" && field.Value == "stuck-app" {
			foundApp = true
		}
	}
	if !foundPID {
		t.Error("expected PID field with value 12345")
	}
	if !foundApp {
		t.Error("expected Application field with value stuck-app")
	}
}

func TestSlackClient_ResolvedAlert(t *testing.T) {
	var received SlackMessage

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("failed to decode request: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewSlackClient(server.URL, "#alerts", nil)

	err := client.ResolvedAlert(54321, "recovered-app", 3*time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(received.Attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(received.Attachments))
	}

	att := received.Attachments[0]
	if att.Title != "Idle Transaction Resolved" {
		t.Errorf("expected title 'Idle Transaction Resolved', got %s", att.Title)
	}

	// Resolved should be green
	if att.Color != "#00FF00" {
		t.Errorf("expected green color for resolved, got %s", att.Color)
	}
}

func TestSlackClient_FailedWebhook(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewSlackClient(server.URL, "#test", nil)

	err := client.TestConnection()
	if err == nil {
		t.Error("expected error for failed webhook")
	}
}

func TestSlackClient_EmptyWebhook(t *testing.T) {
	client := NewSlackClient("", "#test", nil)

	err := client.TestConnection()
	if err == nil {
		t.Error("expected error for empty webhook URL")
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"short", 10, "short"},
		{"this is a very long string", 10, "this is..."},
		{"exactly10!", 10, "exactly10!"},
	}

	for _, tt := range tests {
		got := util.Truncate(tt.input, tt.maxLen)
		if got != tt.want {
			t.Errorf("Truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
		}
	}
}
