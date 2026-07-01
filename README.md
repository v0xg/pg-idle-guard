# pg-idle-guard

[![CI](https://github.com/v0xg/pg-idle-guard/actions/workflows/ci.yml/badge.svg)](https://github.com/v0xg/pg-idle-guard/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/v0xg/pg-idle-guard)](https://goreportcard.com/report/github.com/v0xg/pg-idle-guard)

Monitor PostgreSQL connections. Catch idle transactions before they kill your database. Know which service to fix.

```
$ pguard status

Connection Pool (max: 100)
--------------------------------------------
Active:               23
Idle:                 12
Idle in transaction:   8  [!]
Available:            57

Idle Transactions
--------------------------------------------
PID     Age      Application     Query                          
18234   4m 23s   payment-api     UPDATE accounts SET balance... [CRIT]
18456   2m 11s   user-service    SELECT * FROM transactions...  [CRIT]
19012   45s      order-service   SELECT * FROM orders WHERE...  [WARN]
```

## The Problem

A transaction opens, something throws, the connection never closes. It sits "idle in transaction" holding locks and consuming a slot from your limited connection pool. Multiply by traffic:

```
FATAL: too many connections for role "myapp"
```

You scramble to query `pg_stat_activity` and blindly kill connections. Again.

## What This Does

Postgres can already kill idle transactions (`idle_in_transaction_session_timeout`).
What it can't tell you is which service keeps creating them. pguard does both:

- Names the leaker: every idle transaction with its `application_name`, query, and age
- Alerts before pool exhaustion — Slack, any webhook, or your Prometheus/Grafana stack
- Tracks every leak and sends a weekly per-app report: counts, median/max age, top query
- Terminates stuck transactions safely (opt-in, dry-run by default, exclude lists for migrations)
- Scripts cleanly: JSON output and meaningful exit codes for CI

## Install

```bash
# Using go install
go install github.com/v0xg/pg-idle-guard/cmd/pguard@latest

# Or download binary from releases
# https://github.com/v0xg/pg-idle-guard/releases

# Or run with Docker
docker run -e DATABASE_URL="postgres://..." ghcr.io/v0xg/pg-idle-guard daemon
```

## Quick Start

```bash
# Interactive setup (recommended)
pguard configure

# Or set connection string directly
export DATABASE_URL="postgres://user:pass@localhost:5432/mydb"

# Check current status
pguard status

# Watch in real-time
pguard watch

# Run as daemon with alerting
pguard daemon
```

## Production Deployment

For production, run as a daemon with alerting:

```bash
# Docker
docker run -d \
  -e DATABASE_URL="postgres://monitor:pass@db:5432/mydb" \
  -e SLACK_WEBHOOK_URL="https://hooks.slack.com/..." \
  ghcr.io/v0xg/pg-idle-guard daemon

# Kubernetes
kubectl apply -f deploy/kubernetes.yaml

# systemd
sudo systemctl enable --now pguard
```

See [deploy/README.md](deploy/README.md) for detailed deployment options.

## Configuration

```bash
pguard configure
```

The wizard guides you through:

- Database connection (with IAM auth support for RDS)
- Credential storage (AWS Secrets Manager, Parameter Store, or environment variables)
- Alert destinations (Slack or any webhook endpoint)
- Alert cooldowns and auto-termination rules
- HTTP API toggle (`/health`, `/status`, Prometheus `/metrics`) and listen address
- Logging level, format, and output

Config is stored in `~/.config/pguard/config.yaml`. Secrets stay in your chosen secret manager, never in plain text.

### Example Config

```yaml
connection:
  host: mydb.rds.amazonaws.com
  database: production
  user: monitoring
  auth_method: iam  # Uses AWS IAM, no password needed

thresholds:
  idle_transaction:
    warning: 30s
    critical: 2m
  connection_pool:
    warning_percent: 75
    critical_percent: 90

alerts:
  cooldown: 5m  # Prevent alert spam
  slack:
    enabled: true
    webhook_url: ${SLACK_WEBHOOK_URL}
    channel: "#alerts-db"
  # Or use any HTTP endpoint (Discord, Mattermost, custom)
  webhook:
    enabled: true
    url: "https://your-service.com/alerts"
    headers:
      Authorization: "Bearer ${WEBHOOK_TOKEN}"

auto_terminate:
  enabled: true
  after: 5m
  exclude_apps: [migration-runner, pg_dump]

report:
  enabled: true       # record leaks + send a weekly per-app digest
  day: monday         # weekday name, case-insensitive
  time: "09:00"       # 24h HH:MM, local time
  data_file: ""       # empty = ~/.local/state/pguard/events.jsonl
  retention_days: 30  # how long events are kept (minimum 7)
```

## Commands

```
configure          Interactive setup wizard
status             Show current connection pool state
status --json      Output as JSON (for scripting)
status -q          Quiet mode (exit code only: 0 healthy, 1 warning, 2 critical)
watch              Real-time monitoring
kill <pid>         Terminate a specific backend
daemon             Run as background service with alerts
report             Aggregated leaks per application over a trailing window
report --days 14   Widen the window (up to retention_days)
report --json      Output as JSON (durations in fractional seconds)
```

## Leak Report

Killing a PID treats the symptom. The leak report tells you which service to fix:

```
$ pguard report

Leak Report — last 7 days
--------------------------------------------------------------------------------
APP            LEAKS  MEDIAN  MAX      TERMINATED  TOP QUERY
payment-api    47     4m 12s  18m 40s  9           UPDATE accounts SET balance = balance...
user-service   12     1m 3s   7m 55s   0           SELECT * FROM transactions WHERE user...
order-service  3      52s     2m 30s   0           SELECT * FROM orders WHERE id = $1

Ongoing — 1 still open
--------------------------------------------------------------------------------
PID    APP          IDLE FOR  QUERY
18234  payment-api  6m 2s     UPDATE accounts SET balance = balance...
```

With `report.enabled: true`, the daemon records every idle transaction that
crosses the warning threshold and sends this same summary to Slack or your
webhook once a week (`report.day` / `report.time`). Still-open leaks are
fetched live into the "ongoing" section, so the worst offenders are never
hidden. After a week, the conversation changes from "who killed my
connection?" to "payment-api leaks after every deploy — here's the query."

The event file lives on the daemon's filesystem — unlike `status` and `kill`,
this history is not stored in Postgres. Two consequences:

- **Docker:** mount a volume at the state directory or history is lost on
  container restart: `-v pguard-state:/home/appuser/.local/state/pguard`
  (the image runs as `appuser`). On Kubernetes, the bundled manifest already
  mounts a `data` volume at `/app/data` — set `report.data_file:
  /app/data/events.jsonl` to use it.
- **`pguard report`** must run where the daemon writes its state (same host,
  or with that volume mounted).

## Prometheus

With the HTTP API enabled, the daemon serves Prometheus metrics at `/metrics` —
point a scrape job at it and graph/alert in your existing Grafana stack:

```
pguard_connections{state="idle_in_transaction"} 8
pguard_pool_usage_ratio 0.44
pguard_idle_transactions{application="payment-api"} 2
pguard_idle_transaction_oldest_seconds 263
pguard_terminations_total 5
```

See [deploy/README.md](deploy/README.md) for scrape config and the full metric list.

## AWS RDS

pguard works well with RDS:

```bash
# Use IAM authentication (recommended)
pguard configure
# Select: AWS IAM Authentication

# Your RDS user needs the rds_iam role:
# GRANT rds_iam TO monitoring_user;
```

Credentials are fetched automatically using your AWS credentials (environment, instance profile, etc).

## Why Not Just Use X?

| Alternative | Gap |
|-------------|-----|
| `idle_in_transaction_session_timeout` | Blunt. Kills leaks but can't tell you which service keeps creating them. |
| pgBouncer | Connection pooling does not fix leaked transactions. |
| postgres_exporter + Grafana | Graphs the counts. No query attribution out of the box, no termination, no leak report. |
| RDS Performance Insights | Not real-time, harder to action on. |
| pganalyze | Great but expensive. This is focused and free. |

## License

[Unlicense](https://unlicense.org) - Public domain

---

Built after too many 3am pages for "connection pool exhausted."
