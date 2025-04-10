FROM golang:1.21-alpine AS builder

# Set up build directory
WORKDIR /build

# Install build dependencies
RUN apk add --no-cache git make

# Copy go module files first for better layer caching
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 make build

# Create the final small image
FROM alpine:3.18

# Add certificates and timezone data
RUN apk add --no-cache ca-certificates tzdata

# Create non-root user
RUN addgroup -g 1000 damon && \
    adduser -u 1000 -G damon -s /bin/sh -D damon

# Create necessary directories
RUN mkdir -p /app/templates && \
    chown -R damon:damon /app

# Set working directory
WORKDIR /app

# Copy binary from builder stage
COPY --from=builder /build/damon /app/
COPY --from=builder /build/templates /app/templates/
COPY --from=builder /build/config.sample.toml /app/config.toml

# Switch to non-root user
USER damon

# Command to run
ENTRYPOINT ["/app/damon"]
