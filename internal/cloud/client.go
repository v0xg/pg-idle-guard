// Package cloud implements a native client for the pguard Cloud Ingest API.
//
// A daemon configured with a cloud URL and agent token pushes periodic
// snapshots (pool stats + idle-in-transaction backends), reports leak events,
// long-polls for operator-issued kill commands, and reports their results.
// This replaces the need for any external bridge process: the daemon talks the
// Ingest API contract directly.
package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ingestPrefix is the fixed base path of the Ingest API on a pguard Cloud
// server. The configured cloud URL is treated as the server root.
const ingestPrefix = "/api/ingest/v1"

// Client talks to the pguard Cloud Ingest API on behalf of a single instance.
type Client struct {
	base     string // server root, e.g. https://cloud.pguard.dev (no trailing slash)
	token    string // agent token, "pgc_..."
	instance string // instance name reported to the cloud
	version  string // daemon version, sent as daemon_version

	http *http.Client
}

// NewClient builds a cloud client. baseURL is the pguard Cloud server root
// (the "/api/ingest/v1" prefix is appended internally); token is the agent
// token; instance is the stable name this daemon reports under.
func NewClient(baseURL, token, instance, version string) *Client {
	return &Client{
		base:     strings.TrimRight(baseURL, "/"),
		token:    token,
		instance: instance,
		version:  version,
		// No client-level timeout: the command long-poll holds for ~25s.
		// Per-request deadlines are supplied via context instead.
		http: &http.Client{},
	}
}

// Instance returns the instance name this client reports under.
func (c *Client) Instance() string { return c.instance }

// PoolStats mirrors the Ingest API pool object.
type PoolStats struct {
	Active   int `json:"active"`
	Idle     int `json:"idle"`
	IdleInTx int `json:"idle_in_tx"`
	MaxConns int `json:"max_conns"`
}

// IdleTx is a single idle-in-transaction backend in a snapshot.
type IdleTx struct {
	PID       int     `json:"pid"`
	App       string  `json:"app"`
	DurationS float64 `json:"duration_s"`
	Query     string  `json:"query"`
}

// Snapshot is the body of POST /snapshots.
type Snapshot struct {
	Instance      string    `json:"instance"`
	DatabaseName  string    `json:"database_name,omitempty"`
	DaemonVersion string    `json:"daemon_version,omitempty"`
	IntervalS     float64   `json:"interval_s,omitempty"`
	TS            time.Time `json:"ts"`
	Pool          PoolStats `json:"pool"`
	IdleTxs       []IdleTx  `json:"idle_txs"`
}

// LeakEvent is a single recorded idle-transaction event.
type LeakEvent struct {
	TS         time.Time `json:"ts"`
	PID        int       `json:"pid"`
	App        string    `json:"app"`
	DurationS  float64   `json:"duration_s"`
	Query      string    `json:"query"`
	Terminated bool      `json:"terminated"`
}

// Command is a single command handed down by the cloud (currently "kill").
type Command struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// KillPayload is the payload of a "kill" command.
type KillPayload struct {
	PID int `json:"pid"`
}

// PushSnapshot sends the current pool state. The instance and daemon version
// are filled in from the client if the caller left them empty.
func (c *Client) PushSnapshot(ctx context.Context, snap Snapshot) error {
	if snap.Instance == "" {
		snap.Instance = c.instance
	}
	if snap.DaemonVersion == "" {
		snap.DaemonVersion = c.version
	}
	if snap.IdleTxs == nil {
		snap.IdleTxs = []IdleTx{}
	}
	return c.do(ctx, http.MethodPost, "/snapshots", snap, nil)
}

// PushEvents records a batch of leak events. Returns the number of events the
// cloud newly recorded (duplicates are deduped server-side).
func (c *Client) PushEvents(ctx context.Context, events []LeakEvent) (int, error) {
	if len(events) == 0 {
		return 0, nil
	}
	body := struct {
		Instance string      `json:"instance"`
		Events   []LeakEvent `json:"events"`
	}{Instance: c.instance, Events: events}

	var out struct {
		Recorded int `json:"recorded"`
	}
	if err := c.do(ctx, http.MethodPost, "/events", body, &out); err != nil {
		return 0, err
	}
	return out.Recorded, nil
}

// PollCommands long-polls for pending commands for this instance. The cloud
// holds the request open for ~25s, so callers should pass a context with a
// deadline a little longer than that.
func (c *Client) PollCommands(ctx context.Context) ([]Command, error) {
	q := url.Values{}
	q.Set("instance", c.instance)
	var out struct {
		Commands []Command `json:"commands"`
	}
	if err := c.do(ctx, http.MethodGet, "/commands?"+q.Encode(), nil, &out); err != nil {
		return nil, err
	}
	return out.Commands, nil
}

// ReportResult reports the outcome of a command back to the cloud. status is
// "done" or "failed"; message is a human-readable diagnostic.
func (c *Client) ReportResult(ctx context.Context, commandID, status, message string) error {
	body := struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}{Status: status, Message: message}
	path := "/commands/" + url.PathEscape(commandID) + "/result"
	return c.do(ctx, http.MethodPost, path, body, nil)
}

// do performs a single Ingest API request. If body is non-nil it is JSON
// encoded; if out is non-nil the (JSON) response body is decoded into it.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshaling request: %w", err)
		}
		reader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.base+ingestPrefix+path, reader)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("User-Agent", "pg-idle-guard/"+c.version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{Status: resp.StatusCode, Body: readErrorBody(resp.Body)}
	}

	if out != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decoding response: %w", err)
		}
	}
	return nil
}

// APIError is returned when the cloud responds with a non-2xx status.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	if e.Body != "" {
		return fmt.Sprintf("cloud returned %d: %s", e.Status, e.Body)
	}
	return fmt.Sprintf("cloud returned %d", e.Status)
}

// readErrorBody reads a bounded snippet of an error response for diagnostics.
func readErrorBody(r io.Reader) string {
	data, _ := io.ReadAll(io.LimitReader(r, 2048))
	return strings.TrimSpace(string(data))
}
