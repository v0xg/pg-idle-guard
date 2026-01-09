.PHONY: all build install run test coverage lint fmt tidy clean release dev test-db-up test-db-down test-run test-idle help

BINARY_NAME=pguard
VERSION?=dev
COMMIT?=$(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
DATE?=$(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
BUILD_DIR=./build
CMD_DIR=./cmd/pguard

# Build flags - inject version info into internal/cli package
LDFLAGS=-ldflags "-s -w \
	-X github.com/v0xg/pg-idle-guard/internal/cli.Version=$(VERSION) \
	-X github.com/v0xg/pg-idle-guard/internal/cli.Commit=$(COMMIT) \
	-X github.com/v0xg/pg-idle-guard/internal/cli.Date=$(DATE)"

# Default target
all: build

# Build the binary
build:
	@echo "Building $(BINARY_NAME)..."
	@mkdir -p $(BUILD_DIR)
	go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) $(CMD_DIR)
	@echo "Built: $(BUILD_DIR)/$(BINARY_NAME)"

# Install to GOPATH/bin
install:
	@echo "Installing $(BINARY_NAME)..."
	go install $(LDFLAGS) $(CMD_DIR)
	@echo "Installed to $(shell go env GOPATH)/bin/$(BINARY_NAME)"

# Run without building
run:
	go run $(CMD_DIR) $(ARGS)

# Run tests
test:
	go test -v ./...

# Run tests with coverage
coverage:
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

# Lint code
GOLANGCI_LINT=$(shell go env GOPATH)/bin/golangci-lint
lint:
	@test -f $(GOLANGCI_LINT) || (echo "Installing golangci-lint..." && go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest)
	$(GOLANGCI_LINT) run

# Format code
fmt:
	go fmt ./...

# Tidy dependencies
tidy:
	go mod tidy

# Clean build artifacts
clean:
	@echo "Cleaning..."
	@rm -rf $(BUILD_DIR)
	@rm -f coverage.out coverage.html
	@echo "Done"

# Build for multiple platforms
release:
	@echo "Building releases..."
	@mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64 $(CMD_DIR)
	GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux-arm64 $(CMD_DIR)
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-amd64 $(CMD_DIR)
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-arm64 $(CMD_DIR)
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-windows-amd64.exe $(CMD_DIR)
	@echo "Releases built in $(BUILD_DIR)/"

# Development: watch and rebuild
dev:
	@which air > /dev/null || (echo "Installing air..." && go install github.com/cosmtrek/air@latest)
	air

# Start test PostgreSQL
test-db-up:
	docker compose -f test/docker-compose.yml up -d
	@echo "Waiting for PostgreSQL..."
	@sleep 2
	@echo "Ready. Connection: postgres://testuser:testpass@localhost:5432/testdb"

# Stop test PostgreSQL
test-db-down:
	docker compose -f test/docker-compose.yml down

# Run with test config
test-run: build
	./build/$(BINARY_NAME) --config test/config.yaml status

# Create idle transactions for testing
test-idle:
	@chmod +x test/simulate-idle.sh
	./test/simulate-idle.sh

# Show help
help:
	@echo "Available targets:"
	@echo "  build       - Build the binary"
	@echo "  install     - Install to GOPATH/bin"
	@echo "  run         - Run without building (use ARGS= to pass arguments)"
	@echo "  test        - Run unit tests"
	@echo "  coverage    - Run tests with coverage report"
	@echo "  lint        - Run linter"
	@echo "  fmt         - Format code"
	@echo "  tidy        - Tidy dependencies"
	@echo "  clean       - Remove build artifacts"
	@echo "  release     - Build for multiple platforms"
	@echo "  dev         - Watch and rebuild on changes"
	@echo ""
	@echo "Integration testing:"
	@echo "  test-db-up  - Start PostgreSQL in Docker"
	@echo "  test-db-down - Stop PostgreSQL"
	@echo "  test-run    - Run status against test DB"
	@echo "  test-idle   - Create idle transactions for testing"
