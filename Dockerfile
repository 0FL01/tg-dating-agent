# Multi-stage build for tg-dating-agent
# Stage 1: Build
FROM golang:1.25-alpine AS builder

WORKDIR /app

# Install git for fetching dependencies
RUN apk add --no-cache git

# Copy dependency files first for better layer caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary
# CGO_ENABLED=0 for static binary
# -ldflags="-w -s" for smaller binary (strip debug info)
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-w -s" \
    -o dating-agent \
    ./cmd/dating

# Stage 2: Runtime
FROM alpine:3.20

# Create non-root user for security
RUN addgroup -g 1000 -S dating && \
    adduser -u 1000 -S dating -G dating

# Install ca-certificates for HTTPS requests
RUN apk add --no-cache ca-certificates

WORKDIR /app

# Copy binary from builder
COPY --from=builder /app/dating-agent .

# Create directory for session file (used as SESSION_PATH fallback)
RUN mkdir -p /app/data && chown -R dating:dating /app

# Switch to non-root user
USER dating

# Expose no ports - this is a userbot that connects outbound only

ENTRYPOINT ["./dating-agent"]
