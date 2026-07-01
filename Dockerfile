# Build stage
FROM golang:1.24-alpine AS builder

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown

RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w \
    -X github.com/v0xg/pg-idle-guard/internal/cli.Version=${VERSION} \
    -X github.com/v0xg/pg-idle-guard/internal/cli.Commit=${COMMIT} \
    -X github.com/v0xg/pg-idle-guard/internal/cli.Date=${DATE}" \
    -o pguard ./cmd/pguard

# Runtime stage
FROM alpine:3.19

RUN apk --no-cache add ca-certificates

WORKDIR /app

COPY --from=builder /build/pguard /usr/local/bin/pguard

# Non-root user. The state dir must exist with appuser ownership so a named
# volume mounted there (for leak report history) inherits writable ownership.
RUN adduser -D -u 1000 appuser \
    && mkdir -p /home/appuser/.local/state/pguard \
    && chown -R appuser /home/appuser/.local/state
USER appuser

ENTRYPOINT ["pguard"]
CMD ["daemon"]
