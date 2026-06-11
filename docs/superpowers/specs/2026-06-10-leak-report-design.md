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
- **Retention:** events are kept for `report.retention_days` (default 30). The weekly
  digest window stays fixed at 7 days; retention exists so `pguard report --days N` can
  look further back without the prune silently truncating the answer.
- **Ongoing leaks are first-class:** leaks that have not yet resolved (the worst
  offenders) appear in a dedicated "ongoing" section of both the digest and the CLI —
  they are *not* written to the event store (events record completed leaks only), so
  there is no double-counting when they eventually resolve.

## Components

### 1. `internal/report` package (new)

**`Event`** — one recorded leak:

```go
// MaxQueryChars is the single truncation limit for query text captured for
// reporting. Applied once, at capture time in daemon.go (the existing
// trackedIdle capture moves from its hardcoded 100 to this constant).
const MaxQueryChars = 200

type Event struct {
    Time       time.Time     `json:"ts"`         // when the event was recorded (resolution/termination)
    PID        int           `json:"pid"`
    App        string        `json:"app"`        // application_name; empty string allowed
    Duration   time.Duration `json:"duration_s"` // max observed idle duration (from pg state_change)
    Query      string        `json:"query"`      // truncated to MaxQueryChars
    Terminated bool          `json:"terminated"` // true if pguard auto-terminated it
}
```

`Duration` is serialized as **fractional seconds** via custom `MarshalJSON`/`UnmarshalJSON`
(key `duration_s`), not Go's default nanosecond integer — the JSONL file and `--json`
output are consumed by jq/spreadsheets, and changing the format after shipping is a
migration.

**`OngoingLeak`** — a leak that is still open (read live, never stored):

```go
type OngoingLeak struct {
    PID      int
    App      string
    Duration time.Duration // current idle duration (state_change-based)
    Query    string        // truncated to MaxQueryChars
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
  `Event` with `Terminated: true` and set a new `terminated bool` on the `trackedIdle`
  entry. The PID is **not** deleted from `tracked`: the next resolved sweep still fires
  the existing `ResolvedAlert` (preserving current alert semantics for webhook/runbook
  consumers) but skips appending a second event when `tc.terminated` is set.
- Appends are best-effort: on error, `slog.Error` and continue; event recording must
  never disrupt monitoring.
- Event recording is active whenever `report.enabled: true`.
- **Ongoing snapshot:** at the end of every poll, the loop rebuilds a
  `[]report.OngoingLeak` from `tracked` entries with `warningSent` true (and not
  `terminated`), stored in a mutex-guarded variable. The scheduler goroutine reads this
  snapshot at digest time — it never touches the `tracked` map directly, so the map
  stays single-goroutine.

**Scheduler:** a goroutine started by `runDaemon` when `report.enabled`:

```
for {
    next := report.NextRun(time.Now(), cfg.Report.Day, cfg.Report.Time)
    sleep until next (or ctx.Done)
    events := store.ReadSince(now - 7d)
    summaries := report.Aggregate(events)
    ongoing := read the mutex-guarded ongoing snapshot
    send digest to slackClient and webhookClient (each nil-checked, errors logged)
    store.Prune(now - cfg.Report.RetentionDays)
}
```

- Digest window is fixed at the trailing 7 days; prune uses `retention_days` so older
  history stays available to `pguard report --days`.
- An empty week with no ongoing leaks still sends a short "no idle transaction leaks
  this week" digest (confirms the reporting pipeline is alive). A week with zero
  *completed* events but open ongoing leaks must **not** claim "no leaks" — the ongoing
  section leads in that case.
- No catch-up: if the daemon was down at the scheduled moment, that week is skipped.

### 3. Config (`internal/config`)

```yaml
report:
  enabled: false      # default off, consistent with project conventions
  day: monday         # weekday name, case-insensitive
  time: "09:00"       # 24h HH:MM, local time
  data_file: ""       # empty = default XDG state path
  retention_days: 30  # how long events are kept; must be >= 7 (the digest window)
```

`Validate()` additions (only when `report.enabled`): `day` parses to a weekday,
`time` parses as `15:04`, `retention_days >= 7`.

### 4. Alerts (`internal/alerts`)

New method on both clients, following the existing interface pattern:

- `SlackClient.LeakReportDigest(summaries []report.AppSummary, ongoing []report.OngoingLeak, windowDays int) error` —
  one attachment, info color, title "Weekly Leak Report", one line per app:
  `payment-api — 47 leaks, median 4m, max 32m, 3 terminated` plus `top query:` text.
  Apps beyond the top 10 are summarized as "…and N more". If `ongoing` is non-empty, an
  **"Ongoing"** section follows (or leads, when there are no completed events):
  `3 leaks still open — oldest 18d (payment-api, pid 4711)`, one line per leak, capped
  at 5 with "…and N more". No @-mentions (it's a digest, not an incident).
- `WebhookClient.LeakReportDigest(...)` — JSON payload:
  `{"type":"leak_report","window_days":7,"generated_at":...,"apps":[...],"ongoing":[...]}`.
  Durations in the payload are fractional seconds, matching the JSONL format.

To avoid an import cycle (`alerts` → `report` while daemon uses both), `alerts` imports
`report` for the `AppSummary` type; `report` imports nothing from `alerts`.

### 5. CLI: `pguard report` (`internal/cli/report.go`)

- Reads the event file directly (works without a running daemon), aggregates, prints.
- **Ongoing section from a live query:** like `status`, it creates a `postgres.Client`
  and lists current idle-in-transaction sessions past the warning threshold (the daemon's
  snapshot is daemon-local; the CLI gets fresher data straight from `pg_stat_activity`).
  If the DB is unreachable, print the historical report with a note that ongoing leaks
  couldn't be fetched.
- Flags: `--days N` (default 7), `--json`. If `N > retention_days`, print
  `note: history is retained for 30 days; showing what's available` in the header rather
  than silently presenting a truncated window as N days.
- Table output mirrors `status` style: APP / LEAKS / MEDIAN / MAX / TERMINATED / TOP QUERY,
  followed by the ongoing table (PID / APP / IDLE FOR / QUERY).
- `--json` durations are fractional seconds, same as the JSONL format.
- Exit code 0 for informational outcomes, including a missing event file; an *unreadable*
  file (EACCES) is an error and exits 1. If the event file doesn't exist, the hint must
  cover both causes: `report.enabled: true` must be set on the daemon, **and** this
  command must run where the daemon writes its state (same host, or with the state
  directory volume-mounted when the daemon runs in Docker).
- Added to the forbidigo allowlist alongside the other CLI output files.

**Deployment note:** the event file is state on the daemon's filesystem — unlike
`status`/`kill`/`watch`, history is not in Postgres. Docker deployments must mount a
volume at the state directory or history is lost on container restart; document this
next to the existing Docker instructions.

## Error handling

| Failure | Behavior |
|---|---|
| Append fails (disk full, perms) | `slog.Error`, monitoring continues |
| Malformed JSONL line on read | skipped, debug log |
| Digest send fails | `slog.Error` per channel (existing pattern); prune still runs |
| Prune fails | `slog.Error`; file keeps growing until next successful prune (bounded by weekly retry) |
| Event file missing on `pguard report` | friendly hint (enable on daemon / run on daemon host), exit 0 |
| Event file unreadable (EACCES) on `pguard report` | error message naming the path and suggesting the daemon's user owns it (file is 0600), exit 1 |
| DB unreachable for ongoing section on `pguard report` | historical report prints with a "couldn't fetch ongoing leaks" note |

## Testing

- `internal/report`: store round-trip, append-then-read-since filtering, prune keeps/drops
  correctly, malformed-line tolerance, missing-file behavior; `Event` JSON round-trip
  asserting `duration_s` is fractional seconds (guards the wire format); `Aggregate`
  median (odd/even counts), top-query selection, sort order, empty input; `NextRun`
  across week boundaries, same-day before/after the slot, DST-adjacent dates.
- `internal/cli`: resolved sweep still sends `ResolvedAlert` for terminated PIDs and does
  not double-record their events; queries captured for events are truncated to
  `report.MaxQueryChars` (not the old 100).
- `internal/config`: report block defaults, validation of bad day/time,
  `retention_days < 7` rejected.
- `internal/alerts`: digest payload shape for both clients including the `ongoing` array
  and ongoing-only weeks (existing test patterns with httptest servers).

## Out of scope

- Multi-database / fleet aggregation.
- Query normalization/fingerprinting (grouping is by exact truncated query text — for
  apps that inline literals instead of using parameters, counts fragment and `TopQuery`
  degrades; the digest copy should say "a top query" rather than promise *the* culprit).
- Configurable digest window or non-weekly schedules (`retention_days` is configurable;
  the digest window is not).
- Historical trend comparison ("up 20% from last week").
