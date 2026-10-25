# =============================================================================
# Stage 1 — Build
# =============================================================================
FROM golang:1.24-alpine AS builder

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
FROM alpine:3.20

# ca-certificates for HTTPS to Binance / Telegram
RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

# Copy binary from build stage
COPY --from=builder /build/signalbot .

# Create data directory for SQLite (can be mounted as a volume)
RUN mkdir -p /app/data

# Run as non-root user for security
RUN addgroup -S botgroup && adduser -S botuser -G botgroup
USER botuser

ENTRYPOINT ["./signalbot"]
