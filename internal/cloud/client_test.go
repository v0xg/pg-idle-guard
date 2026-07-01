package cloud

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// newTestClient returns a client pointed at srv with a fixed token/instance.
func newTestClient(srv *httptest.Server) *Client {
	return NewClient(srv.URL, "pgc_test", "api-prod", "test")
}

func TestPushSnapshot(t *testing.T) {
	var gotAuth, gotPath, gotUA string
	var body map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		gotUA = r.Header.Get("User-Agent")
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := newTestClient(srv)
	err := c.PushSnapshot(context.Background(), Snapshot{
		TS:   time.Unix(0, 0).UTC(),
		Pool: PoolStats{Active: 1, Idle: 2, IdleInTx: 3, MaxConns: 100},
		IdleTxs: []IdleTx{
			{PID: 42, App: "worker", DurationS: 12.5, Query: "SELECT 1"},
		},
	})
	if err != nil {
		t.Fatalf("PushSnapshot: %v", err)
	}

	if gotAuth != "Bearer pgc_test" {
		t.Errorf("auth header = %q, want Bearer pgc_test", gotAuth)
	}
	if gotPath != "/api/ingest/v1/snapshots" {
		t.Errorf("path = %q, want /api/ingest/v1/snapshots", gotPath)
	}
	if gotUA != "pg-idle-guard/test" {
		t.Errorf("user-agent = %q", gotUA)
	}
	// Instance and daemon version are filled in from the client.
	if body["instance"] != "api-prod" {
		t.Errorf("instance = %v, want api-prod", body["instance"])
	}
	if body["daemon_version"] != "test" {
		t.Errorf("daemon_version = %v, want test", body["daemon_version"])
	}
	pool, ok := body["pool"].(map[string]any)
	if !ok {
		t.Fatalf("pool not an object: %v", body["pool"])
	}
	if mc, _ := pool["max_conns"].(float64); mc != 100 {
		t.Errorf("pool.max_conns = %v, want 100", pool["max_conns"])
	}
}

func TestPushSnapshotFillsEmptyIdleTxs(t *testing.T) {
	var raw map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &raw)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	if err := newTestClient(srv).PushSnapshot(context.Background(), Snapshot{}); err != nil {
		t.Fatalf("PushSnapshot: %v", err)
	}
	// idle_txs must serialize as [] (not null) so the cloud never sees a nil.
	if got := string(raw["idle_txs"]); got != "[]" {
		t.Errorf("idle_txs = %s, want []", got)
	}
}

func TestPushEvents(t *testing.T) {
	var body struct {
		Instance string      `json:"instance"`
		Events   []LeakEvent `json:"events"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"recorded":2}`))
	}))
	defer srv.Close()

	recorded, err := newTestClient(srv).PushEvents(context.Background(), []LeakEvent{
		{PID: 1, App: "a", Terminated: true},
		{PID: 2, App: "b"},
	})
	if err != nil {
		t.Fatalf("PushEvents: %v", err)
	}
	if recorded != 2 {
		t.Errorf("recorded = %d, want 2", recorded)
	}
	if body.Instance != "api-prod" || len(body.Events) != 2 {
		t.Errorf("unexpected body: %+v", body)
	}
	if !body.Events[0].Terminated {
		t.Errorf("event 0 should be terminated")
	}
}

func TestPushEventsEmptyIsNoop(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	n, err := newTestClient(srv).PushEvents(context.Background(), nil)
	if err != nil || n != 0 {
		t.Fatalf("PushEvents(nil) = %d, %v", n, err)
	}
	if called {
		t.Errorf("empty batch should not hit the network")
	}
}

func TestPollCommands(t *testing.T) {
	var gotInstance string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotInstance = r.URL.Query().Get("instance")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"commands":[{"id":"c1","type":"kill","payload":{"pid":99}}]}`))
	}))
	defer srv.Close()

	cmds, err := newTestClient(srv).PollCommands(context.Background())
	if err != nil {
		t.Fatalf("PollCommands: %v", err)
	}
	if gotInstance != "api-prod" {
		t.Errorf("instance query = %q, want api-prod", gotInstance)
	}
	if len(cmds) != 1 || cmds[0].ID != "c1" || cmds[0].Type != "kill" {
		t.Fatalf("unexpected commands: %+v", cmds)
	}
	var p KillPayload
	if err := json.Unmarshal(cmds[0].Payload, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if p.PID != 99 {
		t.Errorf("pid = %d, want 99", p.PID)
	}
}

func TestReportResult(t *testing.T) {
	var gotPath string
	var body map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	err := newTestClient(srv).ReportResult(context.Background(), "cmd-123", "done", "terminated backend 99")
	if err != nil {
		t.Fatalf("ReportResult: %v", err)
	}
	if gotPath != "/api/ingest/v1/commands/cmd-123/result" {
		t.Errorf("path = %q", gotPath)
	}
	if body["status"] != "done" || body["message"] != "terminated backend 99" {
		t.Errorf("unexpected body: %+v", body)
	}
}

func TestAPIErrorSurfaced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"unauthenticated","message":"bad token"}}`))
	}))
	defer srv.Close()

	err := newTestClient(srv).PushSnapshot(context.Background(), Snapshot{})
	if err == nil {
		t.Fatal("expected error on 401")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("error type = %T, want *APIError", err)
	}
	if apiErr.Status != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", apiErr.Status)
	}
}

func TestBaseURLTrailingSlashTrimmed(t *testing.T) {
	c := NewClient("https://cloud.example.com/", "pgc_x", "inst", "v1")
	if c.base != "https://cloud.example.com" {
		t.Errorf("base = %q, want no trailing slash", c.base)
	}
}
