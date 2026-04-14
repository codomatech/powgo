# Builder stage
FROM golang:1.26.2-alpine AS builder

# Install required tools for linting and security scanning
RUN apk add --no-cache git curl && \
    go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest && \
    go install github.com/securego/gosec/v2/cmd/gosec@latest && \
    go install honnef.co/go/tools/cmd/staticcheck@latest && \
    go install golang.org/x/vuln/cmd/govulncheck@latest && \
    rm -rf /root/.cache/go-build

WORKDIR /app

# Copy go.mod and go.sum to cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
ADD server.go .
ADD utils/ utils/

# Run linting and security scans
RUN go vet ./... \
    && staticcheck ./... \
    && gosec ./... \
    && govulncheck ./...

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o powgo .

# Final stage
FROM alpine:latest

# Add non-root user
RUN addgroup -g 1001 -S appgroup && adduser -u 1001 -S appuser -G appgroup

# Install CA certificates for TLS
RUN apk add --no-cache ca-certificates

WORKDIR /app

# Copy binary from builder
COPY --from=builder --chown=appuser:appgroup /app/powgo /app/powgo

# Copy config example
COPY config.json.example /app/config.json

# Switch to non-root user
USER appuser

# Expose port
EXPOSE 8080

# Entrypoint
ENTRYPOINT ["/app/powgo"]
CMD ["--config", "/app/config.json"]
