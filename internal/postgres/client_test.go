package postgres

import (
	"strings"
	"testing"
	"time"

	"github.com/v0xg/pg-idle-guard/internal/config"
)

func TestBuildConnectionString(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *config.Config
		wantHost string
		wantPort string
		wantDB   string
		wantUser string
		wantSSL  string
	}{
		{
			name: "basic connection",
			cfg: &config.Config{
				Connection: config.ConnectionConfig{
					Host:           "localhost",
					Port:           5432,
					Database:       "testdb",
					User:           "testuser",
					SSLMode:        "disable",
					ConnectTimeout: 10 * time.Second,
				},
			},
			wantHost: "host=localhost",
			wantPort: "port=5432",
			wantDB:   "dbname=testdb",
			wantUser: "user=testuser",
			wantSSL:  "sslmode=disable",
		},
		{
			name: "custom port",
			cfg: &config.Config{
				Connection: config.ConnectionConfig{
					Host:           "db.example.com",
					Port:           5433,
					Database:       "prod",
					User:           "admin",
					SSLMode:        "require",
					ConnectTimeout: 30 * time.Second,
				},
			},
			wantHost: "host=db.example.com",
			wantPort: "port=5433",
			wantDB:   "dbname=prod",
			wantUser: "user=admin",
			wantSSL:  "sslmode=require",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			connStr, err := buildConnectionString(tt.cfg)
			if err != nil {
				t.Fatalf("buildConnectionString() error = %v", err)
			}

			if !strings.Contains(connStr, tt.wantHost) {
				t.Errorf("connection string missing host: got %s, want %s", connStr, tt.wantHost)
			}
			if !strings.Contains(connStr, tt.wantPort) {
				t.Errorf("connection string missing port: got %s, want %s", connStr, tt.wantPort)
			}
			if !strings.Contains(connStr, tt.wantDB) {
				t.Errorf("connection string missing dbname: got %s, want %s", connStr, tt.wantDB)
			}
			if !strings.Contains(connStr, tt.wantUser) {
				t.Errorf("connection string missing user: got %s, want %s", connStr, tt.wantUser)
			}
			if !strings.Contains(connStr, tt.wantSSL) {
				t.Errorf("connection string missing sslmode: got %s, want %s", connStr, tt.wantSSL)
			}
		})
	}
}

func TestBuildConnectionString_URL(t *testing.T) {
	cfg := &config.Config{
		Connection: config.ConnectionConfig{
			URL: "postgres://user:pass@localhost:5432/mydb?sslmode=disable",
		},
	}

	connStr, err := buildConnectionString(cfg)
	if err != nil {
		t.Fatalf("buildConnectionString() error = %v", err)
	}

	if connStr != cfg.Connection.URL {
		t.Errorf("expected URL to be returned as-is, got %s", connStr)
	}
}

func TestBuildConnectionString_PasswordEscaping(t *testing.T) {
	tests := []struct {
		name         string
		password     string
		wantContains string
	}{
		{
			name:         "simple password",
			password:     "secret123",
			wantContains: "password=secret123",
		},
		{
			name:         "password with spaces",
			password:     "my secret",
			wantContains: "password=my+secret",
		},
		{
			name:         "password with special chars",
			password:     "p@ss!word#123",
			wantContains: "password=p%40ss%21word%23123",
		},
		{
			name:         "password with equals sign",
			password:     "pass=word",
			wantContains: "password=pass%3Dword",
		},
		{
			name:         "password with ampersand",
			password:     "pass&word",
			wantContains: "password=pass%26word",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				Connection: config.ConnectionConfig{
					Host:           "localhost",
					Port:           5432,
					Database:       "testdb",
					User:           "testuser",
					Password:       tt.password,
					SSLMode:        "disable",
					ConnectTimeout: 10 * time.Second,
					AuthMethod:     "password",
				},
			}

			connStr, err := buildConnectionString(cfg)
			if err != nil {
				t.Fatalf("buildConnectionString() error = %v", err)
			}

			if !strings.Contains(connStr, tt.wantContains) {
				t.Errorf("connection string = %s, want to contain %s", connStr, tt.wantContains)
			}
		})
	}
}

func TestBuildConnectionString_IAMAuth(t *testing.T) {
	cfg := &config.Config{
		Connection: config.ConnectionConfig{
			Host:           "mydb.cluster.us-east-1.rds.amazonaws.com",
			Port:           5432,
			Database:       "mydb",
			User:           "iam_user",
			SSLMode:        "require",
			AuthMethod:     "iam",
			AWSRegion:      "us-east-1",
			ConnectTimeout: 10 * time.Second,
		},
	}

	connStr, err := buildConnectionString(cfg)
	if err != nil {
		t.Fatalf("buildConnectionString() error = %v", err)
	}

	// For IAM auth, password should not be included in connection string
	if strings.Contains(connStr, "password=") {
		t.Error("IAM auth connection string should not contain password")
	}

	// Other fields should be present
	if !strings.Contains(connStr, "host=mydb.cluster.us-east-1.rds.amazonaws.com") {
		t.Error("connection string missing host")
	}
	if !strings.Contains(connStr, "sslmode=require") {
		t.Error("IAM auth should use SSL")
	}
}

func TestBuildConnectionString_EmptyPassword(t *testing.T) {
	cfg := &config.Config{
		Connection: config.ConnectionConfig{
			Host:           "localhost",
			Port:           5432,
			Database:       "testdb",
			User:           "testuser",
			Password:       "",
			SSLMode:        "disable",
			ConnectTimeout: 10 * time.Second,
		},
	}

	connStr, err := buildConnectionString(cfg)
	if err != nil {
		t.Fatalf("buildConnectionString() error = %v", err)
	}

	if strings.Contains(connStr, "password=") {
		t.Error("empty password should not appear in connection string")
	}
}

func TestBuildConnectionString_ConnectTimeout(t *testing.T) {
	cfg := &config.Config{
		Connection: config.ConnectionConfig{
			Host:           "localhost",
			Port:           5432,
			Database:       "testdb",
			User:           "testuser",
			SSLMode:        "disable",
			ConnectTimeout: 30 * time.Second,
		},
	}

	connStr, err := buildConnectionString(cfg)
	if err != nil {
		t.Fatalf("buildConnectionString() error = %v", err)
	}

	if !strings.Contains(connStr, "connect_timeout=30") {
		t.Errorf("connection string should contain connect_timeout=30, got %s", connStr)
	}
}
