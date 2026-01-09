package alerts

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewWebhookClient(t *testing.T) {
	tests := []struct {
		name           string
		url            string
		method         string
		headers        map[string]string
		expectedMethod string
	}{
		{
			name:           "defaults to POST",
			url:            "https://example.com/webhook",
			method:         "",
			headers:        nil,
			expectedMethod: "POST",
		},
		{
			name:           "uses provided method",
			url:            "https://example.com/webhook",
			method:         "PUT",
			headers:        nil,
			expectedMethod: "PUT",
		},
		{
			name:   "with custom headers",
			url:    "https://example.com/webhook",
			method: "POST",
			headers: map[string]string{
				"Authorization": "Bearer token123",
			},
			expectedMethod: "POST",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewWebhookClient(tt.url, tt.method, tt.headers)
			if client.URL != tt.url {
				t.Errorf("URL = %q, want %q", client.URL, tt.url)
			}
			if client.Method != tt.expectedMethod {
				t.Errorf("Method = %q, want %q", client.Method, tt.expectedMethod)
			}
			if client.HTTPClient == nil {
				t.Error("HTTPClient should not be nil")
			}
		})
	}
}

func TestWebhookClient_IdleTransactionAlert(t *testing.T) {
	var receivedPayload WebhookPayload
	var receivedHeaders http.Header

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &receivedPayload); err != nil {
			t.Errorf("failed to unmarshal payload: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewWebhookClient(server.URL, "POST", map[string]string{
		"X-Custom-Header": "test-value",
	})

	err := client.IdleTransactionAlert(SeverityWarning, 12345, "test-app", 45*time.Second, "SELECT * FROM users")
	if err != nil {
		t.Fatalf("IdleTransactionAlert() error = %v", err)
	}

	// Verify payload
	if receivedPayload.Event != "idle_transaction" {
		t.Errorf("Event = %q, want %q", receivedPayload.Event, "idle_transaction")
	}
	if receivedPayload.Severity != SeverityWarning {
		t.Errorf("Severity = %q, want %q", receivedPayload.Severity, SeverityWarning)
	}
	if pid, ok := receivedPayload.Data["pid"].(float64); !ok || pid != 12345 {
		t.Errorf("pid = %v, want 12345", receivedPayload.Data["pid"])
	}
	if app, ok := receivedPayload.Data["application"].(string); !ok || app != "test-app" {
		t.Errorf("application = %v, want test-app", receivedPayload.Data["application"])
	}

	// Verify headers
	if receivedHeaders.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q, want %q", receivedHeaders.Get("Content-Type"), "application/json")
	}
	if receivedHeaders.Get("X-Custom-Header") != "test-value" {
		t.Errorf("X-Custom-Header = %q, want %q", receivedHeaders.Get("X-Custom-Header"), "test-value")
	}
	if receivedHeaders.Get("User-Agent") != "pguard" {
		t.Errorf("User-Agent = %q, want %q", receivedHeaders.Get("User-Agent"), "pguard")
	}
}

func TestWebhookClient_ConnectionPoolAlert(t *testing.T) {
	var receivedPayload WebhookPayload

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &receivedPayload); err != nil {
			t.Errorf("failed to unmarshal payload: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewWebhookClient(server.URL, "POST", nil)
	err := client.ConnectionPoolAlert(SeverityCritical, 90, 100, 90.0)
	if err != nil {
		t.Fatalf("ConnectionPoolAlert() error = %v", err)
	}

	if receivedPayload.Event != "connection_pool" {
		t.Errorf("Event = %q, want %q", receivedPayload.Event, "connection_pool")
	}
	if receivedPayload.Severity != SeverityCritical {
		t.Errorf("Severity = %q, want %q", receivedPayload.Severity, SeverityCritical)
	}
	if used, ok := receivedPayload.Data["used_connections"].(float64); !ok || used != 90 {
		t.Errorf("used_connections = %v, want 90", receivedPayload.Data["used_connections"])
	}
	if maxVal, ok := receivedPayload.Data["max_connections"].(float64); !ok || maxVal != 100 {
		t.Errorf("max_connections = %v, want 100", receivedPayload.Data["max_connections"])
	}
	if avail, ok := receivedPayload.Data["available_connections"].(float64); !ok || avail != 10 {
		t.Errorf("available_connections = %v, want 10", receivedPayload.Data["available_connections"])
	}
}

func TestWebhookClient_TerminationAlert(t *testing.T) {
	var receivedPayload WebhookPayload

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &receivedPayload); err != nil {
			t.Errorf("failed to unmarshal payload: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewWebhookClient(server.URL, "POST", nil)
	err := client.TerminationAlert(54321, "terminated-app", 5*time.Minute, "auto-terminate threshold exceeded")
	if err != nil {
		t.Fatalf("TerminationAlert() error = %v", err)
	}

	if receivedPayload.Event != "connection_terminated" {
		t.Errorf("Event = %q, want %q", receivedPayload.Event, "connection_terminated")
	}
	if receivedPayload.Severity != SeverityInfo {
		t.Errorf("Severity = %q, want %q", receivedPayload.Severity, SeverityInfo)
	}
	if pid, ok := receivedPayload.Data["pid"].(float64); !ok || pid != 54321 {
		t.Errorf("pid = %v, want 54321", receivedPayload.Data["pid"])
	}
	if reason, ok := receivedPayload.Data["reason"].(string); !ok || reason != "auto-terminate threshold exceeded" {
		t.Errorf("reason = %v, want 'auto-terminate threshold exceeded'", receivedPayload.Data["reason"])
	}
}

func TestWebhookClient_ResolvedAlert(t *testing.T) {
	var receivedPayload WebhookPayload

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &receivedPayload); err != nil {
			t.Errorf("failed to unmarshal payload: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewWebhookClient(server.URL, "POST", nil)
	err := client.ResolvedAlert(99999, "resolved-app", 3*time.Minute)
	if err != nil {
		t.Fatalf("ResolvedAlert() error = %v", err)
	}

	if receivedPayload.Event != "idle_transaction_resolved" {
		t.Errorf("Event = %q, want %q", receivedPayload.Event, "idle_transaction_resolved")
	}
	if receivedPayload.Severity != SeverityResolved {
		t.Errorf("Severity = %q, want %q", receivedPayload.Severity, SeverityResolved)
	}
}

func TestWebhookClient_TestConnection(t *testing.T) {
	var receivedPayload WebhookPayload

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &receivedPayload); err != nil {
			t.Errorf("failed to unmarshal payload: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewWebhookClient(server.URL, "POST", nil)
	err := client.TestConnection()
	if err != nil {
		t.Fatalf("TestConnection() error = %v", err)
	}

	if receivedPayload.Event != "test" {
		t.Errorf("Event = %q, want %q", receivedPayload.Event, "test")
	}
	if msg, ok := receivedPayload.Data["message"].(string); !ok || msg != "pguard webhook configured successfully" {
		t.Errorf("message = %v, want 'pguard webhook configured successfully'", receivedPayload.Data["message"])
	}
}

func TestWebhookClient_ErrorHandling(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		wantErr    bool
	}{
		{"success 200", 200, false},
		{"success 201", 201, false},
		{"success 204", 204, false},
		{"error 400", 400, true},
		{"error 401", 401, true},
		{"error 403", 403, true},
		{"error 404", 404, true},
		{"error 500", 500, true},
		{"error 502", 502, true},
		{"error 503", 503, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
			}))
			defer server.Close()

			client := NewWebhookClient(server.URL, "POST", nil)
			err := client.TestConnection()

			if (err != nil) != tt.wantErr {
				t.Errorf("TestConnection() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestWebhookClient_EmptyURL(t *testing.T) {
	client := NewWebhookClient("", "POST", nil)
	err := client.TestConnection()
	if err == nil {
		t.Error("expected error for empty URL")
	}
}

func TestWebhookClient_GETMethod(t *testing.T) {
	var receivedMethod string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewWebhookClient(server.URL, "GET", nil)
	err := client.TestConnection()
	if err != nil {
		t.Fatalf("TestConnection() error = %v", err)
	}

	if receivedMethod != "GET" {
		t.Errorf("Method = %q, want %q", receivedMethod, "GET")
	}
}
