# Deployment Guide

## Overview

pguard can be deployed in several ways depending on your infrastructure:

| Method | Best for |
|--------|----------|
| Docker | Simple deployments, VMs |
| Kubernetes | K8s environments |
| systemd | Bare metal Linux servers |
| Binary | Quick debugging sessions |

## Prerequisites

Create a PostgreSQL user for monitoring:

```sql
-- Create dedicated monitoring user (recommended)
CREATE USER pg_idle_guard WITH PASSWORD 'your-secure-password';
GRANT pg_monitor TO pg_idle_guard;

-- Or grant minimal permissions manually:
-- GRANT SELECT ON pg_stat_activity TO pg_idle_guard;
-- GRANT EXECUTE ON FUNCTION pg_terminate_backend(int) TO pg_idle_guard;
```

## Docker Deployment

```bash
# Build image
docker build -t pguard .

# Run with environment variables
docker run -d \
  --name pguard \
  -e DATABASE_URL="postgres://pg_idle_guard:password@host.docker.internal:5432/mydb" \
  -e SLACK_WEBHOOK_URL="https://hooks.slack.com/..." \
  pguard daemon

# Or use docker-compose
cd deploy
cp docker-compose.yml docker-compose.override.yml
# Edit docker-compose.override.yml with your settings
docker-compose up -d
```

## Kubernetes Deployment

```bash
# Edit the manifests
vi deploy/kubernetes.yaml
# Update:
# - DATABASE_URL in the Secret
# - SLACK_WEBHOOK_URL in the Secret
# - Any threshold settings in the ConfigMap

# Apply
kubectl apply -f deploy/kubernetes.yaml

# Check status
kubectl -n monitoring logs -f deployment/pguard
```

## systemd Deployment (Linux servers)

```bash
# Install binary
sudo curl -L https://github.com/v0xg/pg-idle-guard/releases/latest/download/pguard-linux-amd64 \
  -o /usr/local/bin/pguard
sudo chmod +x /usr/local/bin/pguard

# Create user and directories
sudo useradd -r -s /bin/false pguard
sudo mkdir -p /etc/pguard /var/lib/pguard
sudo chown pguard: /var/lib/pguard

# Create config
sudo cp examples/config.yaml /etc/pguard/config.yaml
sudo vi /etc/pguard/config.yaml
# Edit connection settings

# Create environment file for secrets
sudo tee /etc/pguard/env << EOF
DATABASE_URL=postgres://pg_idle_guard:password@localhost:5432/mydb
SLACK_WEBHOOK_URL=https://hooks.slack.com/...
EOF
sudo chmod 600 /etc/pguard/env

# Install and start service
sudo cp deploy/pguard.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable pguard
sudo systemctl start pguard

# Check status
sudo systemctl status pguard
sudo journalctl -u pguard -f
```

## AWS RDS with IAM Authentication

IAM authentication is the recommended way to connect to RDS in production:
- No passwords to manage or rotate
- Uses AWS instance roles / ECS task roles / IRSA
- Automatic credential rotation
- Audit trail via CloudTrail

### Step 1: Enable IAM Auth on RDS

```bash
# Via AWS CLI
aws rds modify-db-instance \
  --db-instance-identifier mydb \
  --enable-iam-database-authentication \
  --apply-immediately
```

### Step 2: Create Database User with IAM Role

```sql
-- Connect to your database as admin
CREATE USER pg_idle_guard WITH LOGIN;
GRANT rds_iam TO pg_idle_guard;
GRANT pg_monitor TO pg_idle_guard;
```

### Step 3: Create IAM Policy

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": "rds-db:connect",
      "Resource": "arn:aws:rds-db:us-east-1:123456789:dbuser:db-ABCDEFGH/pg_idle_guard"
    }
  ]
}
```

Attach this policy to your EC2 instance role, ECS task role, or IRSA role.

### Step 4: Configure pguard

```yaml
# config.yaml
connection:
  host: mydb.xxxxx.us-east-1.rds.amazonaws.com
  port: 5432
  database: mydb
  user: pg_idle_guard
  auth_method: iam
  aws_region: us-east-1
  sslmode: require  # Required for IAM auth
```

### Step 5: Deploy

**On EC2:**
```bash
# The instance role provides credentials automatically
pguard daemon --config /etc/pguard/config.yaml
```

**On ECS/Fargate:**
```yaml
# Task definition - the task role provides credentials
{
  "containerDefinitions": [{
    "name": "pguard",
    "image": "ghcr.io/v0xg/pguard:latest",
    "command": ["daemon", "--config", "/app/config/config.yaml"]
  }]
}
```

**On Kubernetes with IRSA:**
```yaml
# Service account with IAM role annotation
apiVersion: v1
kind: ServiceAccount
metadata:
  name: pguard
  annotations:
    eks.amazonaws.com/role-arn: arn:aws:iam::123456789:role/pguard-role
```

### Troubleshooting IAM Auth

```bash
# Test IAM credentials are available
aws sts get-caller-identity

# Test RDS IAM auth token generation
aws rds generate-db-auth-token \
  --hostname mydb.xxxxx.us-east-1.rds.amazonaws.com \
  --port 5432 \
  --username pg_idle_guard \
  --region us-east-1

# Common issues:
# - "PAM authentication failed": IAM auth not enabled on RDS
# - "Access denied": IAM policy missing or incorrect
# - "Token expired": Clock skew on instance
```

## Health Checks

The daemon exposes health endpoints when `api.enabled: true`:

```bash
# Health check (for load balancers, k8s probes)
curl http://localhost:9182/health

# JSON status
curl http://localhost:9182/status
```

## Configuration Reference

See [examples/config.yaml](../examples/config.yaml) for all options.

Key settings:

```yaml
# Thresholds
thresholds:
  idle_transaction:
    warning: 30s    # Alert after 30s idle
    critical: 2m    # Critical after 2m
  connection_pool:
    warning_percent: 75
    critical_percent: 90

# Auto-terminate stuck transactions
auto_terminate:
  enabled: true
  after: 5m         # Kill after 5 minutes
  dry_run: false    # Set true to test without killing
  exclude_apps:     # Never kill these
    - pg_dump
    - migration
```
