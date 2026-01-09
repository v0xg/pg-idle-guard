package alerts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/v0xg/pg-idle-guard/internal/util"
)

// SlackClient sends alerts to Slack
type SlackClient struct {
	WebhookURL string
	Channel    string
	Mentions   []string
	HTTPClient *http.Client
}

// NewSlackClient creates a new Slack client
func NewSlackClient(webhookURL, channel string, mentions []string) *SlackClient {
	return &SlackClient{
		WebhookURL: webhookURL,
		Channel:    channel,
		Mentions:   mentions,
		HTTPClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// SlackMessage represents a Slack webhook message
type SlackMessage struct {
	Channel     string            `json:"channel,omitempty"`
	Text        string            `json:"text,omitempty"`
	Attachments []SlackAttachment `json:"attachments,omitempty"`
}

// SlackAttachment represents a Slack message attachment
type SlackAttachment struct {
	Color      string       `json:"color,omitempty"`
	Title      string       `json:"title,omitempty"`
	Text       string       `json:"text,omitempty"`
	Fields     []SlackField `json:"fields,omitempty"`
	Footer     string       `json:"footer,omitempty"`
	FooterIcon string       `json:"footer_icon,omitempty"`
	Timestamp  int64        `json:"ts,omitempty"`
}

// SlackField represents a field in a Slack attachment
type SlackField struct {
	Title string `json:"title"`
	Value string `json:"value"`
	Short bool   `json:"short,omitempty"`
}

// Alert severity levels
const (
	SeverityWarning  = "warning"
	SeverityCritical = "critical"
	SeverityInfo     = "info"
	SeverityResolved = "resolved"
)

// Color codes for Slack
var severityColors = map[string]string{
	SeverityWarning:  "#FFA500", // Orange
	SeverityCritical: "#FF0000", // Red
	SeverityInfo:     "#0000FF", // Blue
	SeverityResolved: "#00FF00", // Green
}

// buildMentionText creates mention text for critical alerts
func (s *SlackClient) buildMentionText(severity string) string {
	if severity != SeverityCritical || len(s.Mentions) == 0 {
		return ""
	}
	mentionText := ""
	for _, m := range s.Mentions {
		mentionText += m + " "
	}
	return mentionText
}

// IdleTransactionAlert sends an alert about an idle transaction
func (s *SlackClient) IdleTransactionAlert(severity string, pid int, appName string, duration time.Duration, query string) error {
	color := severityColors[severity]
	if color == "" {
		color = "#808080"
	}

	msg := SlackMessage{
		Channel: s.Channel,
		Text:    s.buildMentionText(severity),
		Attachments: []SlackAttachment{
			{
				Color: color,
				Title: fmt.Sprintf("Idle Transaction [%s]", severity),
				Fields: []SlackField{
					{Title: "Application", Value: appName, Short: true},
					{Title: "PID", Value: fmt.Sprintf("%d", pid), Short: true},
					{Title: "Idle Duration", Value: duration.Round(time.Second).String(), Short: true},
					{Title: "Severity", Value: severity, Short: true},
					{Title: "Query", Value: util.Truncate(query, 200)},
				},
				Footer:    "pguard",
				Timestamp: time.Now().Unix(),
			},
		},
	}

	return s.send(msg)
}

// ConnectionPoolAlert sends an alert about connection pool pressure
func (s *SlackClient) ConnectionPoolAlert(severity string, used, maxConns int, percent float64) error {
	color := severityColors[severity]
	if color == "" {
		color = "#808080"
	}

	msg := SlackMessage{
		Channel: s.Channel,
		Text:    s.buildMentionText(severity),
		Attachments: []SlackAttachment{
			{
				Color: color,
				Title: fmt.Sprintf("Connection Pool [%s]", severity),
				Fields: []SlackField{
					{Title: "Usage", Value: fmt.Sprintf("%.0f%%", percent), Short: true},
					{Title: "Connections", Value: fmt.Sprintf("%d / %d", used, maxConns), Short: true},
					{Title: "Available", Value: fmt.Sprintf("%d", maxConns-used), Short: true},
					{Title: "Severity", Value: severity, Short: true},
				},
				Footer:    "pguard",
				Timestamp: time.Now().Unix(),
			},
		},
	}

	return s.send(msg)
}

// TerminationAlert sends an alert when a connection is terminated
func (s *SlackClient) TerminationAlert(pid int, appName string, duration time.Duration, reason string) error {
	msg := SlackMessage{
		Channel: s.Channel,
		Attachments: []SlackAttachment{
			{
				Color: severityColors[SeverityInfo],
				Title: "Connection Terminated",
				Fields: []SlackField{
					{Title: "Application", Value: appName, Short: true},
					{Title: "PID", Value: fmt.Sprintf("%d", pid), Short: true},
					{Title: "Was Idle For", Value: duration.Round(time.Second).String(), Short: true},
					{Title: "Reason", Value: reason, Short: true},
				},
				Footer:    "pguard",
				Timestamp: time.Now().Unix(),
			},
		},
	}

	return s.send(msg)
}

// ResolvedAlert sends an alert when an idle transaction resolves
func (s *SlackClient) ResolvedAlert(pid int, appName string, duration time.Duration) error {
	msg := SlackMessage{
		Channel: s.Channel,
		Attachments: []SlackAttachment{
			{
				Color: severityColors[SeverityResolved],
				Title: "Idle Transaction Resolved",
				Fields: []SlackField{
					{Title: "Application", Value: appName, Short: true},
					{Title: "PID", Value: fmt.Sprintf("%d", pid), Short: true},
					{Title: "Total Duration", Value: duration.Round(time.Second).String(), Short: true},
				},
				Footer:    "pguard",
				Timestamp: time.Now().Unix(),
			},
		},
	}

	return s.send(msg)
}

// TestConnection sends a test message to verify the webhook works
func (s *SlackClient) TestConnection() error {
	msg := SlackMessage{
		Channel: s.Channel,
		Attachments: []SlackAttachment{
			{
				Color:     "#00FF00",
				Title:     "pguard Connected",
				Text:      "Slack alerts are configured correctly.",
				Footer:    "pguard",
				Timestamp: time.Now().Unix(),
			},
		},
	}

	return s.send(msg)
}

// send posts a message to the Slack webhook
func (s *SlackClient) send(msg SlackMessage) error {
	if s.WebhookURL == "" {
		return fmt.Errorf("slack webhook URL not configured")
	}

	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshaling message: %w", err)
	}

	resp, err := s.HTTPClient.Post(s.WebhookURL, "application/json", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("slack returned status %d", resp.StatusCode)
	}

	return nil
}
