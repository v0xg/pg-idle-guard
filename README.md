# pg-idle-guard

[![CI](https://github.com/v0xg/pg-idle-guard/actions/workflows/ci.yml/badge.svg)](https://github.com/v0xg/pg-idle-guard/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/v0xg/pg-idle-guard)](https://goreportcard.com/report/github.com/v0xg/pg-idle-guard)

Monitor PostgreSQL connections. Catch idle transactions before they kill your database.

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

- Monitors connections in real-time
- Alerts before pool exhaustion (Slack, webhooks, or any HTTP endpoint)
- Identifies which app/query is leaking
- Terminates stuck transactions safely
- JSON output and exit codes for scripting/CI integration

## Install

```bash
# Using go install
go install github.com/v0xg/pg-idle-guard/cmd/pguard@latest

# Or download binary from releases
# https://github.com/v0xg/pg-idle-guard/releases

# Or run with Docker
docker run -e DATABASE_URL="postgres://..." ghcr.io/v0xg/pguard daemon
```

## Production Deployment

For production, run as a daemon with alerting:

```bash
# Docker
docker run -d \
  -e DATABASE_URL="postgres://monitor:pass@db:5432/mydb" \
  -e SLACK_WEBHOOK_URL="https://hooks.slack.com/..." \
  ghcr.io/v0xg/pguard daemon

# Kubernetes
kubectl apply -f deploy/kubernetes.yaml

# systemd
sudo systemctl enable --now pguard
```

See [deploy/README.md](deploy/README.md) for detailed deployment options.

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

## Configuration

```bash
pguard configure
```

The wizard guides you through:

- Database connection (with IAM auth support for RDS)
- Credential storage (AWS Secrets Manager, Parameter Store, or environment variables)
- Alert destinations (Slack or any webhook endpoint)
- Thresholds and auto-termination rules

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
```

## Commands

```
configure          Interactive setup wizard
status             Show current connection pool state
status --json      Output as JSON (for scripting)
status -q          Quiet mode (exit code only)
watch              Real-time monitoring
kill <pid>         Terminate a specific backend
daemon             Run as background service with alerts
```

### Exit Codes

The `status` command returns meaningful exit codes:
- `0` - All healthy
- `1` - Warning threshold exceeded
- `2` - Critical threshold exceeded

```bash
# Use in CI/scripts
pguard status -q || echo "Database has problems!"

# Parse JSON output
pguard status --json | jq '.idle_transactions[] | select(.severity == "critical")'
```

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
| `idle_in_transaction_session_timeout` | Blunt. No visibility, no alerting. |
| pgBouncer | Connection pooling does not fix leaked transactions. |
| RDS Performance Insights | Not real-time, harder to action on. |
| pganalyze | Great but expensive. This is focused and free. |

## License

[Unlicense](https://unlicense.org) - Public domain

---

Built after too many 3am pages for "connection pool exhausted."
