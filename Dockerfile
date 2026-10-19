# Multi-stage build for ProjectionRoom (production)
# Stage 1: Build
FROM golang:1.23-alpine AS builder

RUN apk add --no-cache git

WORKDIR /build

# Copy go mod files first (for caching)
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build with tags
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o projectionroom -tags wireinject ./cmd

# Stage 2: Runtime
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

# Copy binary from builder
COPY --from=builder /build/projectionroom .

# Copy client dist (if exists)
COPY --from=builder /build/client/dist ./client/dist

# Expose port
EXPOSE 8080

# Set environment variables
ENV TZ=Asia/Shanghai
ENV PR_SERVE_STATIC=true

# Run as non-root user
RUN adduser -D appuser && chown -R appuser:appuser /app
USER appuser

CMD ["/app/projectionroom"]
