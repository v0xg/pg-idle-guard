#!/bin/bash
# simulate-idle.sh
# Creates idle transactions for testing pg-idle-guard

CONNSTR="postgres://testuser:testpass@localhost:5432/testdb"

echo "Creating idle transactions..."
echo "Press Ctrl+C to stop and rollback all"
echo ""

# Function to create an idle transaction
create_idle() {
    local name=$1
    local query=$2

    echo "[$name] Starting idle transaction..."

    # Use process substitution: echo SQL, then sleep keeps fd open = psql waits = idle in transaction
    psql "$CONNSTR" -f <(echo "SET application_name = '$name'; BEGIN; $query;"; sleep 300) &
}

# Create some test data first
psql "$CONNSTR" -c "
    CREATE TABLE IF NOT EXISTS accounts (
        id SERIAL PRIMARY KEY,
        name TEXT,
        balance NUMERIC DEFAULT 0
    );
    INSERT INTO accounts (name, balance) 
    SELECT 'user_' || i, random() * 1000 
    FROM generate_series(1, 100) i
    ON CONFLICT DO NOTHING;
" 2>/dev/null

echo "Test table ready."
echo ""

# Simulate different scenarios
create_idle "payment-api" "UPDATE accounts SET balance = balance + 100 WHERE id = 1"
sleep 1
create_idle "payment-api" "SELECT * FROM accounts WHERE id = 2 FOR UPDATE"
sleep 1
create_idle "user-service" "INSERT INTO accounts (name, balance) VALUES ('pending', 0)"
sleep 1
create_idle "order-service" "UPDATE accounts SET balance = balance - 50 WHERE id = 3"

echo ""
echo "Created 4 idle transactions."
echo "Now run: pg-idle-guard status"
echo "Or:      pg-idle-guard watch"
echo ""
echo "Waiting... (Ctrl+C to cleanup)"

# Wait for interrupt
trap "echo 'Cleaning up...'; kill $(jobs -p) 2>/dev/null; exit" INT
wait
