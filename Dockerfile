# =============================================================================
# Stage 1 — Build
# =============================================================================
FROM golang:1.27-alpine AS builder

WORKDIR /build

# Download dependencies first (better layer caching)
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build a statically linked binary
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-w -s" -o signalbot ./cmd/main.go

# =============================================================================
# Stage 2 — Runtime (minimal image)
# =============================================================================
FROM alpine:latest

# ca-certificates for HTTPS to Binance / Telegram
RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

# Run as non-root user for security
RUN addgroup -S botgroup && adduser -S botuser -G botgroup

# Create data directory for SQLite (can be mounted as a volume)
RUN mkdir -p /app/data && chown -R botuser:botgroup /app/data

# Copy binary from build stage
COPY --from=builder --chown=botuser:botgroup /build/signalbot .

USER botuser

ENTRYPOINT ["./signalbot"]
