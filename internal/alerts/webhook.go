package alerts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/v0xg/pg-idle-guard/internal/util"
)

// WebhookClient sends alerts to a generic HTTP endpoint
type WebhookClient struct {
	URL        string
	Method     string
	Headers    map[string]string
	HTTPClient *http.Client
}

// NewWebhookClient creates a new webhook client
func NewWebhookClient(url, method string, headers map[string]string) *WebhookClient {
	if method == "" {
		method = "POST"
	}
	return &WebhookClient{
		URL:     url,
		Method:  method,
		Headers: headers,
		HTTPClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// WebhookPayload is the standard payload sent to webhooks
type WebhookPayload struct {
	Event     string                 `json:"event"`
	Severity  string                 `json:"severity"`
	Timestamp string                 `json:"timestamp"`
	Data      map[string]interface{} `json:"data"`
}

// IdleTransactionAlert sends an alert about an idle transaction
func (w *WebhookClient) IdleTransactionAlert(severity string, pid int, appName string, duration time.Duration, query string) error {
	payload := WebhookPayload{
		Event:     "idle_transaction",
		Severity:  severity,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Data: map[string]interface{}{
			"pid":              pid,
			"application":      appName,
			"duration_seconds": duration.Seconds(),
			"duration_human":   duration.Round(time.Second).String(),
			"query":            util.Truncate(query, 500),
		},
	}
	return w.send(payload)
}

// ConnectionPoolAlert sends an alert about connection pool pressure
func (w *WebhookClient) ConnectionPoolAlert(severity string, used, maxConns int, percent float64) error {
	payload := WebhookPayload{
		Event:     "connection_pool",
		Severity:  severity,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Data: map[string]interface{}{
			"used_connections":      used,
			"max_connections":       maxConns,
			"available_connections": maxConns - used,
			"usage_percent":         percent,
		},
	}
	return w.send(payload)
}

// TerminationAlert sends an alert when a connection is terminated
func (w *WebhookClient) TerminationAlert(pid int, appName string, duration time.Duration, reason string) error {
	payload := WebhookPayload{
		Event:     "connection_terminated",
		Severity:  SeverityInfo,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Data: map[string]interface{}{
			"pid":              pid,
			"application":      appName,
			"duration_seconds": duration.Seconds(),
			"duration_human":   duration.Round(time.Second).String(),
			"reason":           reason,
		},
	}
	return w.send(payload)
}

// ResolvedAlert sends an alert when an idle transaction resolves
func (w *WebhookClient) ResolvedAlert(pid int, appName string, duration time.Duration) error {
	payload := WebhookPayload{
		Event:     "idle_transaction_resolved",
		Severity:  SeverityResolved,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Data: map[string]interface{}{
			"pid":              pid,
			"application":      appName,
			"duration_seconds": duration.Seconds(),
			"duration_human":   duration.Round(time.Second).String(),
		},
	}
	return w.send(payload)
}

// TestConnection sends a test message to verify the webhook works
func (w *WebhookClient) TestConnection() error {
	payload := WebhookPayload{
		Event:     "test",
		Severity:  SeverityInfo,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Data: map[string]interface{}{
			"message": "pguard webhook configured successfully",
		},
	}
	return w.send(payload)
}

// send posts a payload to the webhook URL
func (w *WebhookClient) send(payload WebhookPayload) error {
	if w.URL == "" {
		return fmt.Errorf("webhook URL not configured")
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshaling payload: %w", err)
	}

	req, err := http.NewRequest(w.Method, w.URL, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "pguard")

	// Add custom headers
	for key, value := range w.Headers {
		req.Header.Set(key, value)
	}

	resp, err := w.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned status %d", resp.StatusCode)
	}

	return nil
}
