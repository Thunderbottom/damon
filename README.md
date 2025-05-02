# Damon - Nomad Automation Helper

> Automate routine Nomad tasks by responding to cluster events - simplify backups, enable service discovery, and manage job lifecycles

Damon is a tool that listens for events in your HashiCorp Nomad cluster and performs useful automated actions in response. It's like having a helpful assistant that watches your Nomad cluster and reacts to changes.

## Key Features

- **Automated Backups**: Create scheduled backups of databases when they're registered
- **Service Discovery**: Make your Nomad services discoverable through DNS
- **Event Notifications**: Get Slack alerts for important cluster events
- **Extensible**: Add custom automation through plugins

## How It Works

Damon connects to your Nomad cluster's event stream and watches for specific events (like job registrations, service changes, etc.). When it detects relevant events, it triggers actions through "providers" that handle different types of automation.

For example, when a PostgreSQL database is registered in Nomad, Damon can automatically create a backup job for it.

## Providers

Damon comes with the following built-in providers:

- [DNS Provider](./provider/dns/README.md) - Creates a DNS server for service discovery
- [Nomad Provider](./provider/nomad/README.md) - Automatically creates secondary jobs based on primary job metadata
- [Shell Provider](./provider/shell/README.md) - Execute shell scripts as Nomad jobs based on user-specified Nomad events

### Extending Damon

Damon supports loading custom providers as go plugins. Although go plugins are not really recommended for production usage, you may load your own plugins into Damon at runtime. For more details, check the [example plugin](./examples/plugins). I am open to accepting new providers as a contribution to Damon instead of using go plugins. Feel free to submit a Pull Request!

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

Double underscores `__` are used to separate configuration sections.

## Common Usage Patterns

### Setting Up Database Backups

1. Create a template for your backup job (e.g., `templates/postgresql-backup/job.hcl`)

2. Configure the Nomad provider:
```toml
[provider.pg_backup]
type = "nomad"
tags = ["pg-backup-cron", "pg-backup-service", "pg-backup-vars"]
job_template = "templates/postgresql-backup/job.hcl" 
acl_template = "templates/postgresql-backup/acl.hcl"
namespace = "*"
deregister_job = true
```

3. Add required metadata to your PostgreSQL job:
```hcl
meta {
  damon-enable = "true"
  pg-backup-cron = "0 3 * * *"  # Daily at 3am
  pg-backup-service = "postgres-db"
  pg-backup-vars = "postgres-backup-vars"
}
```

4. Store backup credentials in Nomad variables:
```bash
nomad var put postgres-backup-vars \
  POSTGRES_USER=app \
  POSTGRES_PASSWORD=secret \
  POSTGRES_DB=mydatabase \
  S3_BUCKET=my-backups
```

5. Deploy your PostgreSQL job, and Damon will automatically create the backup job

### Setting Up DNS Service Discovery

1. Configure the DNS provider:
```toml
[provider.dns]
type = "dns"
namespace = "*"
listen_addr = ":5353"
```

2. Configure your application to use Damon for DNS resolution:
```hcl
template {
  data = <<EOF
server {
    listen 80;
    
    location /api {
        resolver 127.0.0.1:5353;
        set $api_backend "api-service";
        proxy_pass http://$api_backend:8000;
    }
}
EOF
  destination = "local/nginx.conf"
}
```

3. Or configure system-wide DNS resolution with dnsmasq:
```
# /etc/dnsmasq.conf
server=/service/127.0.0.1#5353
```

## Troubleshooting

### Common Issues

**Problem**: Damon isn't creating secondary jobs
- Check if `damon-enable = "true"` is in your job's meta block
- Verify all required tags are present
- Check Damon's logs for template errors

**Problem**: DNS provider not resolving services
- Check the `listen_addr` configuration
- Ensure no firewall is blocking the DNS port
- Test DNS resolution with `dig @localhost -p 5353 service-name`

**Problem**: Can't connect to the cache
- Verify your Redis/Valkey server is running
- Check connection details in the configuration

## Building from Source

```bash
git clone https://github.com/thunderbottom/damon.git
cd damon
make build
```

## Contributing

Contributions in any form are welcome! Please feel free to submit a Pull Request.
