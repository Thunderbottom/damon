# Nomad Provider

The Nomad provider watches for job-related events in Nomad and creates secondary jobs based on the primary job's metadata. This is perfect for automating operational tasks like backups, monitoring, or creating auxiliary services for your main applications.

## Features

- **Event-Driven Job Creation**: Automatically create secondary jobs when primary jobs are registered
- **Template-Based**: Uses HCL templates to define secondary jobs 
- **ACL Integration**: Automatically creates appropriate ACL policies for secondary jobs
- **Metadata Filtering**: Filter jobs based on metadata tags
- **Cleanup Support**: Automatically deregister secondary jobs when primary jobs are removed
- **Full Job Context**: Option to include the entire job payload in template data

## How It Works

The Nomad provider operates by:

1. Listening for `JobRegistered` and `JobDeregistered` events
2. Checking if jobs have the `damon-enable = "true"` meta tag
3. Filtering jobs based on specified metadata tags
4. Rendering job and ACL templates using the job's metadata
5. Registering the resulting job and ACL policy with Nomad
6. Cleaning up the secondary job when the primary job is deregistered (if enabled)

## Configuration

Here's a sample configuration for the Nomad provider:

```toml
[provider.backup]
type = "nomad"
# Tags that must be present in the job's meta block
tags = ["backup-cron", "backup-service", "backup-vars"]
# Templates for job and ACL
job_template = "templates/postgresql-backup/job.hcl"
acl_template = "templates/postgresql-backup/acl.hcl"
# Namespace to watch for events, "*" for all namespaces
namespace = "*"
# Whether to deregister the secondary job when the primary is deregistered
deregister_job = true
# Whether to include the full job payload in template data
add_payload = true
```

You can register multiple Nomad providers with different configurations:

```toml
# PostgreSQL backup provider
[provider.pg_backup]
type = "nomad"
tags = ["pg-backup-cron", "pg-backup-service", "pg-backup-vars"]
job_template = "templates/postgresql-backup/job.hcl"
acl_template = "templates/postgresql-backup/acl.hcl"
namespace = "default"
deregister_job = true

# Redis backup provider
[provider.redis_backup]
type = "nomad"
tags = ["redis-backup-cron", "redis-backup-service"]
job_template = "templates/redis-backup/job.hcl"
acl_template = "templates/redis-backup/acl.hcl"
namespace = "default"
deregister_job = true
```

## Configuration Options

| Option | Description | Default | Required |
|--------|-------------|---------|----------|
| `type` | Must be "nomad" | | Yes |
| `tags` | Tags required in the job meta block | `[]` | No |
| `job_template` | Path to job template file | | Yes |
| `acl_template` | Path to ACL policy template file | | Yes |
| `namespace` | Namespace to monitor for jobs | `*` | No |
| `deregister_job` | Remove secondary jobs when primary is removed | `false` | No |
| `add_payload` | Include full job payload in template data | `false` | No |

## Usage

### Primary Job Configuration

For a primary job to trigger secondary job creation, it needs:

1. The `damon-enable = true` meta tag
2. All the tags specified in the provider configuration

Here's an example of a PostgreSQL service job that will trigger a backup job:

```hcl
job "postgres" {
  datacenters = ["dc1"]
  type = "service"

  meta {
    damon-enable = "true"
    backup-cron = "0 0 * * *"  # Daily backup at midnight
    backup-service = "postgres-db"
    backup-vars = "postgres-backup-vars"  # Reference to Nomad variables
  }

  group "db" {
    count = 1

    network {
      port "db" {
        to = 5432
      }
    }

    service {
      name = "postgres-db"
      port = "db"
      tags = ["db", "postgres"]
      
      check {
        type     = "tcp"
        interval = "10s"
        timeout  = "2s"
      }
    }

    task "postgres" {
      driver = "docker"
      
      config {
        image = "postgres:14"
        ports = ["db"]
      }
      
      env {
        POSTGRES_USER = "app"
        POSTGRES_PASSWORD = "password"
        POSTGRES_DB = "myapp"
      }
    }
  }
}
```

### Template System

The Nomad provider uses a template system with the `[[` and `]]` delimiters. Available template variables include:

- `.JobID`: The ID of the secondary job (automatically prefixed with "damon-")
- `.Namespace`: The namespace of the primary job
- `.Datacenters`: The datacenters of the primary job
- `.Tags`: Map of all tags defined in the primary job's meta block
- `.Payload`: Full job payload if `add_payload = true` is set

#### Job Template Example

Here's a simplified backup job template:

```hcl
job "[[ .JobID ]]" {
  datacenters = [ [[range $idx, $dc := .Datacenters]][[if $idx]], [[end]]"[[$dc]]"[[end]] ]
  namespace = "[[ .Namespace ]]"
  type = "batch"

  periodic {
    cron = "[[ index .Tags "backup-cron" ]]"
    prohibit_overlap = true
  }

  group "backup" {
    count = 1

    task "backup" {
      driver = "docker"

      config {
        image = "postgres:14"
        command = "sh"
        args = ["/local/backup-script.sh"]
      }

      # Template for database credentials
      template {
        data = <<EOH
{{ with nomadService "[[ index .Tags "backup-service" ]]" }}
{{ range . }}
DB_HOST={{ .Address }}
DB_PORT={{ .Port }}
{{ end }}
{{ end }}

{{ with nomadVar "[[ index .Tags "backup-vars" ]]" }}
POSTGRES_DB={{ .POSTGRES_DB }}
POSTGRES_USER={{ .POSTGRES_USER }}
PGPASSWORD={{ .POSTGRES_PASSWORD }}
{{ end }}
EOH
        destination = "secrets/db-credentials.env"
        env = true
      }

      # Template for the backup script
      template {
        data = <<EOH
#!/bin/sh
pg_dump -h $DB_HOST -p $DB_PORT -U $POSTGRES_USER -d $POSTGRES_DB | gzip > /backup/backup-$(date +%Y%m%d-%H%M%S).sql.gz
EOH
        destination = "local/backup-script.sh"
        perms = "0755"
      }
    }
  }
}
```

#### ACL Template Example

The ACL template defines the permissions for the secondary job:

```hcl
namespace "[[ .Namespace ]]" {
  policy = "read"
  
  # Access to variables
  variables {
    path "[[ index .Tags "backup-vars" ]]" {
      capabilities = ["read"]
    }
  }
}

# Read access to services for service discovery
service {
  policy = "read"
}
```

### Advanced Template Features

The template system supports more advanced features like:

- Conditionals: `[[if condition]]...[[end]]`
- Loops: `[[range items]]...[[end]]`
- Variable access: `index .Tags "key-name"`
- Using primary job properties with `add_payload = true`:

```hcl
[[if and .Payload .Payload.job]]
# Use job priority from original job if available
[[if .Payload.job.Priority]]
priority = [[ .Payload.job.Priority ]]
[[end]]

# Copy constraints from original job
[[if .Payload.job.Constraints]]
[[range $idx, $constraint := .Payload.job.Constraints]]
constraint {
  attribute = "[[ $constraint.LTarget ]]"
  operator  = "[[ $constraint.Operand ]]"
  value     = "[[ $constraint.RTarget ]]"
}
[[end]]
[[end]]
[[end]]
```

## Practical Examples

### Database Backup System

Here's a complete example of a PostgreSQL deployment with automated backups:

#### 1. Configure the Nomad provider

```toml
[provider.pg_backup]
type = "nomad"
tags = ["backup-cron", "backup-service", "backup-vars"]
job_template = "templates/postgresql-backup/job.hcl"
acl_template = "templates/postgresql-backup/acl.hcl"
namespace = "*"
deregister_job = true
```

#### 2. Create Nomad variables for backup credentials

```bash
nomad var put postgres-backup-vars \
  POSTGRES_USER=app \
  POSTGRES_PASSWORD=secret \
  POSTGRES_DB=myapp \
  S3_BUCKET=my-backups
```

#### 3. Deploy a PostgreSQL job with the appropriate meta tags

```hcl
job "postgres" {
  datacenters = ["dc1"]
  type = "service"

  meta {
    damon-enable = "true"
    backup-cron = "0 3 * * *"  # Daily backup at 3 AM
    backup-service = "postgres-db"
    backup-vars = "postgres-backup-vars"
  }

  # Rest of the PostgreSQL job definition...
}
```

#### 4. Damon automatically creates a backup job

The provider will:
1. Detect the PostgreSQL job registration
2. Render the backup job template with the provided meta tags
3. Create an appropriate ACL policy for the backup job
4. Register the backup job in Nomad

#### 5. Automatic cleanup when the database is removed

If `deregister_job = true` is set and the PostgreSQL job is deregistered, Damon will automatically:
1. Detect the deregistration event
2. Delete the backup job from Nomad
3. Remove the associated ACL policy

### Monitoring Job Example

You could also create a provider that automatically deploys monitoring jobs:

```toml
[provider.monitoring]
type = "nomad"
tags = ["monitoring-scrape-interval", "monitoring-port", "monitoring-path"]
job_template = "templates/prometheus-exporter/job.hcl"
acl_template = "templates/prometheus-exporter/acl.hcl"
namespace = "*"
deregister_job = true
```

Then add monitoring meta tags to your applications:

```hcl
meta {
  damon-enable = "true"
  monitoring-scrape-interval = "15s"
  monitoring-port = "8080"
  monitoring-path = "/metrics"
}
```

## ACL Policy Best Practices

When creating ACL templates for secondary jobs, follow these principles:

- Grant only the permissions needed for the specific job
- Use namespace restrictions to isolate jobs
- Limit variable access to only what's required

Example of a secure ACL template:

```hcl
namespace "[[ .Namespace ]]" {
  policy = "read"
  
  # Restrict to specific job only
  job "[[ .JobID ]]" {
    policy = "write"
  }
  
  # Limit variable access
  variables {
    path "[[ index .Tags "backup-vars" ]]" {
      capabilities = ["read"]
    }
  }
}

# Minimal service access for discovery
service {
  policy = "read"
}
```

## Troubleshooting

### Secondary Job Not Being Created

1. Verify that the primary job has `damon-enable = true` in its meta block
2. Check that all required tags are present in the meta block
3. Inspect the Damon logs for template rendering errors
4. Verify that the job and ACL templates exist at the specified paths

### Secondary Job Creation Fails

1. Check template syntax for errors
2. Verify that variables referenced in templates are available
3. Ensure Damon has the necessary permissions to create jobs and ACL policies
4. Inspect the Damon logs for detailed error messages

### Secondary Job Not Cleaning Up

1. Verify that `deregister_job = true` is set in the provider configuration
2. Check that the job was properly registered in the cache
3. Ensure Damon has permissions to deregister jobs
4. Inspect the Damon logs for deregistration errors
