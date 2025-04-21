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

# Copy binary and sample configuration
COPY damon /app/
COPY config.sample.toml /app/config.toml

# Switch to non-root user
USER damon

# Command to run
ENTRYPOINT ["/app/damon"]
