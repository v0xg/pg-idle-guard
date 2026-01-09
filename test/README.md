# Testing pg-idle-guard

## Quick Start

```bash
# 1. Start PostgreSQL
docker-compose up -d

# 2. Set connection (or run pg-idle-guard configure)
export DATABASE_URL="postgres://testuser:testpass@localhost:5432/testdb"

# 3. Verify connection
pg-idle-guard status

# 4. In another terminal, create idle transactions
./simulate-idle.sh

# 5. Watch them appear
pg-idle-guard watch

# 6. Try killing one
pg-idle-guard status   # note a PID
pg-idle-guard kill <PID>

# 7. Cleanup
docker-compose down
```

## Manual Testing

If you don't want to use the script, manually create an idle transaction:

```bash
# Terminal 1: Start a transaction and leave it open
psql "postgres://testuser:testpass@localhost:5432/testdb" <<EOF
SET application_name = 'test-app';
BEGIN;
SELECT * FROM pg_stat_activity WHERE pid = pg_backend_pid();
-- Don't type anything else, just leave it hanging
EOF

# Terminal 2: Watch it
pg-idle-guard watch
```

## Testing Connection Pool Pressure

```bash
# Create many connections to test pool warnings
for i in {1..15}; do
    psql "postgres://testuser:testpass@localhost:5432/testdb" \
        -c "SET application_name = 'load-test-$i'; SELECT pg_sleep(60);" &
done

# Watch pool usage increase
pg-idle-guard status
```

## Testing Thresholds

Edit your config to use shorter thresholds for testing:

```yaml
thresholds:
  idle_transaction:
    warning: 5s
    critical: 15s
```

Then create an idle transaction and watch the severity change.
