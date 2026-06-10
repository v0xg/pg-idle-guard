# Leak Report — Design

*2026-06-10*

## Summary

Add a "leak report" to pguard: per-`application_name` aggregation of idle-in-transaction
leaks over a trailing window, delivered as a scheduled Slack/webhook digest from the daemon
and on demand via a new `pguard report` CLI command.

Motivation (from PRODUCT_TEARDOWN.md): killing PIDs treats symptoms; telling the team
*which service to fix* ("payment-api leaked 47 idle transactions this week, median age 4m,
top query: `UPDATE accounts...`") is the attribution value nothing free provides, and it
justifies keeping pguard installed after the immediate leak is fixed.

## Decisions made

- **Storage:** one single append-only JSONL file (not a file per event, not SQLite,
  not in-memory). Zero new dependencies; survives daemon restarts.
- **Delivery:** scheduled digest from the daemon **plus** on-demand `pguard report` CLI.
- **Leak criteria:** an idle transaction becomes a reportable event only if it crossed the
  existing `thresholds.idle_transaction.warning` threshold. No new threshold knob.

## Components

### 1. `internal/report` package (new)

**`Event`** — one recorded leak:

```go
type Event struct {
    Time       time.Time     `json:"ts"`         // when the event was recorded (resolution/termination)
    PID        int           `json:"pid"`
    App        string        `json:"app"`        // application_name; empty string allowed
    Duration   time.Duration `json:"duration"`   // max observed idle duration (from pg state_change)
    Query      string        `json:"query"`      // truncated to 200 chars
    Terminated bool          `json:"terminated"` // true if pguard auto-terminated it
}
```

**`Store`** — wraps the JSONL file:

- `NewStore(path string) *Store`
- `Append(e Event) error` — opens in append mode, writes one JSON line, closes.
- `ReadSince(t time.Time) ([]Event, error)` — full-file scan; skips malformed lines
  (logs at debug level); missing file returns an empty slice, not an error.
- `Prune(olderThan time.Time) error` — rewrites the file keeping only newer events
  (write to temp file in same dir, then rename).

Default path: `$XDG_STATE_HOME/pguard/events.jsonl`, falling back to
`~/.local/state/pguard/events.jsonl`. Directory created on first append (0700, file 0600).
A `Store` is used by a single daemon process; no cross-process locking (consistent with
the existing single-daemon-per-database model). Within the process, all `Store` methods
take an internal mutex — the poll loop appends while the scheduler goroutine
reads/prunes, and the temp-file-plus-rename in `Prune` must not race an `Append`.

**`Aggregate(events []Event) []AppSummary`** — groups by `App`:

```go
type AppSummary struct {
    App            string
    Count          int
    MedianDuration time.Duration
    MaxDuration    time.Duration
    TerminatedCount int
    TopQuery       string // most frequent truncated query text; ties broken by first seen
}
```

Sorted by `Count` descending, then `App` ascending for stable output. Empty `App` is
rendered as `(unknown)` at display time but grouped under its real key.

**`NextRun(now time.Time, day time.Weekday, hhmm string) time.Time`** — next occurrence
of the configured weekday+time in local time; if now is exactly past this week's slot,
returns next week's.

### 2. Daemon integration (`internal/cli/daemon.go`)

- `trackedIdle` gains `maxDuration time.Duration`, updated every poll from
  `conn.IdleDuration()` (Postgres `state_change`-based, more accurate than
  `time.Since(firstSeen)` which only measures pguard's observation window).
- **Record on resolution:** in the existing resolved-PID sweep, if `tc.warningSent` is
  true, append an `Event` with `Terminated: false`.
- **Record on auto-termination:** after a successful `TerminateBackend`, append an
  `Event` with `Terminated: true` and delete the PID from `tracked` immediately (so the
  resolved sweep doesn't double-record it).
- Appends are best-effort: on error, `slog.Error` and continue; event recording must
  never disrupt monitoring.
- Event recording is active whenever `report.enabled: true`.

**Scheduler:** a goroutine started by `runDaemon` when `report.enabled`:

```
for {
    next := report.NextRun(time.Now(), cfg.Report.Day, cfg.Report.Time)
    sleep until next (or ctx.Done)
    events := store.ReadSince(now - 7d)
    summaries := report.Aggregate(events)
    send digest to slackClient and webhookClient (each nil-checked, errors logged)
    store.Prune(now - 7d)
}
```

- Window is fixed at the trailing 7 days.
- An empty week still sends a short "no idle transaction leaks this week" digest
  (confirms the reporting pipeline is alive).
- No catch-up: if the daemon was down at the scheduled moment, that week is skipped.

### 3. Config (`internal/config`)

```yaml
report:
  enabled: false      # default off, consistent with project conventions
  day: monday         # weekday name, case-insensitive
  time: "09:00"       # 24h HH:MM, local time
  data_file: ""       # empty = default XDG state path
```

`Validate()` additions (only when `report.enabled`): `day` parses to a weekday,
`time` parses as `15:04`.

### 4. Alerts (`internal/alerts`)

New method on both clients, following the existing interface pattern:

- `SlackClient.LeakReportDigest(summaries []report.AppSummary, windowDays int) error` —
  one attachment, info color, title "Weekly Leak Report", one line per app:
  `payment-api — 47 leaks, median 4m, max 32m, 3 terminated` plus `top query:` text.
  Apps beyond the top 10 are summarized as "…and N more". No @-mentions (it's a digest,
  not an incident).
- `WebhookClient.LeakReportDigest(...)` — JSON payload:
  `{"type":"leak_report","window_days":7,"generated_at":...,"apps":[...]}`.

To avoid an import cycle (`alerts` → `report` while daemon uses both), `alerts` imports
`report` for the `AppSummary` type; `report` imports nothing from `alerts`.

### 5. CLI: `pguard report` (`internal/cli/report.go`)

- Reads the event file directly (works without a running daemon), aggregates, prints.
- Flags: `--days N` (default 7), `--json`.
- Table output mirrors `status` style: APP / LEAKS / MEDIAN / MAX / TERMINATED / TOP QUERY.
- Exit code 0 always (informational). If the event file doesn't exist, prints a hint that
  `report.enabled: true` must be set on the daemon.
- Added to the forbidigo allowlist alongside the other CLI output files.

## Error handling

| Failure | Behavior |
|---|---|
| Append fails (disk full, perms) | `slog.Error`, monitoring continues |
| Malformed JSONL line on read | skipped, debug log |
| Digest send fails | `slog.Error` per channel (existing pattern); prune still runs |
| Prune fails | `slog.Error`; file keeps growing until next successful prune (bounded by weekly retry) |
| Event file missing on `pguard report` | friendly hint, exit 0 |

## Testing

- `internal/report`: store round-trip, append-then-read-since filtering, prune keeps/drops
  correctly, malformed-line tolerance, missing-file behavior; `Aggregate` median (odd/even
  counts), top-query selection, sort order, empty input; `NextRun` across week boundaries,
  same-day before/after the slot, DST-adjacent dates.
- `internal/config`: report block defaults, validation of bad day/time.
- `internal/alerts`: digest payload shape for both clients (existing test patterns with
  httptest servers).

## Out of scope

- Multi-database / fleet aggregation.
- Query normalization/fingerprinting (grouping is by exact truncated query text).
- Configurable report window or non-weekly schedules.
- Historical trend comparison ("up 20% from last week").
