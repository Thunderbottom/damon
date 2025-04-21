# Damon - Nomad Events Operator

Damon is a Nomad Events Operator that listens to events from your Nomad cluster and provides functionality based on configurable providers.

## Providers

Damon comes with the following built-in providers:

- [DNS Provider](./provider/dns/README.md) - Creates a DNS server for service discovery
- [Nomad Provider](./provider/nomad/README.md) - Automatically creates secondary jobs based on primary job metadata

### Extending Damon

Damon supports loading custom providers as go plugins. Although go plugins are not really recommended for production usage, you may load your own plugins into damon at runtime. For more details, check the [example plugin](./examples/plugins). I am open to accepting new providers as a contribution to Damon instead of using go plugins. Feel free to submit a Pull Request!

## Installation

### Binary Installation

Download the latest release from the [GitHub Releases](https://github.com/thunderbottom/damon/releases) page.

```bash
# Download the binary (replace with the latest version under releases)
$ curl -L -o damon https://github.com/thunderbottom/damon/releases/download/v0.1.0/damon_0.1.0_linux_amd64

# Make it executable
$ chmod +x damon

# Move to a directory in your PATH
$ mv damon /usr/local/bin/
```

### Docker Installation

You can also run Damon using Docker:

```yaml
# docker-compose.yml
version: '3'

services:
  damon:
    image: thunderbottom/damon:latest
    volumes:
      - ./config.toml:/app/config.toml
    environment:
      - NOMAD_ADDR=http://host.docker.internal:4646
      - NOMAD_TOKEN=${NOMAD_TOKEN}
    restart: unless-stopped
    depends_on:
      - valkey

  valkey:
    image: valkey/valkey:latest
    ports:
      - "6379:6379"
    volumes:
      - valkey-data:/data
    restart: unless-stopped

volumes:
  valkey-data:
```

Start with:

```bash
docker-compose up -d
```

## Configuration

Damon uses a TOML configuration file. Create a `config.toml` file with the following structure:

```toml
[app]
log_level = "INFO"  # Can be DEBUG, INFO, ERROR

[cache]
address = ["localhost:6379"]
username = ""
password = ""
client_name = ""
commit_interval = "10s"

# Provider configurations
[provider.backup]
type = "nomad"
tags = ["backup-cron", "backup-db-service", "backup-variables"]
job_template = "templates/postgresql-backup/job.hcl"
acl_template = "templates/postgresql-backup/acl.hcl"
namespace = "*"
deregister_job = true

# DNS provider configuration
[provider.dns]
type = "dns"
namespace = "*"
tags = []
listen_addr = ":5353"
```

A sample configuration file is available at [config.sample.toml](./config.sample.toml).

## Environment Variables

All configuration options can also be provided via environment variables with the `DAMON_` prefix:

```bash
DAMON_APP__LOG_LEVEL=DEBUG
DAMON_CACHE__ADDRESS=valkey:6379
DAMON_CACHE__PASSWORD=secret
```

## Building from Source

```bash
git clone https://github.com/thunderbottom/damon.git
cd damon
make build
```

## Contributing

Contributions in any form are welcome! Please feel free to submit a Pull Request.
