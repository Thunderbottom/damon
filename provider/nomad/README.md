# Nomad Provider

The Nomad provider watches for job-related events in Nomad and can create secondary jobs based on the primary job's metadata. This is useful for automating operational tasks like backups, monitoring, or creating auxiliary services for your main applications.

## Features

- **Event-Driven Job Creation**: Automatically create secondary jobs when primary jobs are registered
- **Template-Based**: Uses HCL templates to define secondary jobs
- **ACL Integration**: Automatically creates appropriate ACL policies for secondary jobs
- **Metadata Filtering**: Filter jobs based on metadata tags
- **Cleanup Support**: Automatically deregister secondary jobs when primary jobs are removed

## How It Works

The Nomad provider works by:

1. Listening for `JobRegistered` and `JobDeregistered` events
2. Filtering jobs based on specified metadata tags
3. When a matching job is registered, rendering job and ACL templates using the job's metadata
4. Registering the resulting job and ACL policy with Nomad
5. Optionally cleaning up the secondary job when the primary job is deregistered

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
    pg-backup-cron = "0 0 * * *"  # Daily backup at midnight
    pg-backup-service = "postgres-db"
    pg-backup-vars = "nomad/jobs/postgres"  # Reference to this job's own variables
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

### Job Template

Your job template should use the `[[` and `]]` delimiters for template variables. Available template variables include:

- `.JobID`: The ID of the secondary job (automatically prefixed with "damon-")
- `.Namespace`: The namespace of the primary job
- `.Datacenters`: The datacenters of the primary job
- `.Tags`: Map of all tags defined in the primary job's meta block
- `.Payload`: Full job payload if `add_payload = true` is set

Here's a simplified backup job template example:

```hcl
job "[[ .JobID ]]" {
  datacenters = [ [[range $idx, $dc := .Datacenters]][[if $idx]], [[end]]"[[$dc]]"[[end]] ]
  namespace = "[[ .Namespace ]]"
  type = "batch"

  periodic {
    cron = "[[ index .Tags "pg-backup-cron" ]]"
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

      template {
        data = <<EOH
#!/bin/sh
pg_dump -h [[ index .Tags "pg-backup-service" ]] -U $POSTGRES_USER -d $POSTGRES_DB | gzip > /backup/backup-$(date +%Y%m%d-%H%M%S).sql.gz
EOH
        destination = "local/backup-script.sh"
        perms = "0755"
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

### ACL Template

The ACL template defines the permissions for the secondary job. Here's a simple example:

```hcl
namespace "[[ .Namespace ]]" {
  policy = "read"
  
  # Access to the specific job
  job "[[ .JobID ]]" {
    policy = "write"
  }
}

# Read access to services for service discovery
service {
  policy = "read"
}
```

## Example: PostgreSQL Backup System

Here's a complete example of a PostgreSQL deployment with automated backups:

### Primary Job: PostgreSQL Service

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
        volumes = [
          "/data/postgres:/var/lib/postgresql/data",
          "/data/backups:/backups"
        ]
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

### Nomad Variables

You can use either dedicated variables for backups or reference the primary job's variables:

```bash
# Option 1: Create dedicated variables
nomad var put postgres-backup-vars POSTGRES_USER=app POSTGRES_PASSWORD=password POSTGRES_DB=myapp S3_BUCKET=my-backups

# Option 2: Use the original job's variables (referenced by backup-vars = "nomad/jobs/postgres" in the job meta)
# No additional action needed as the job template will access the primary job's variables
```

### Secondary Job Template

Create a template for PostgreSQL backups (saved as `templates/postgresql-backup/job.hcl`):

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
        volumes = [
          "/data/backups:/backups"
        ]
      }

      # Template for database credentials
      template {
        data = <<EOH
{{ with nomadService "[[ index .Tags "backup-service" ]]" "provider=nomad" }}
{{ range . }}
DB_HOST={{ .Address }}
DB_PORT={{ .Port }}
{{ end }}
{{ end }}

{{ with nomadVar "[[ index .Tags "backup-vars" ]]" }}
POSTGRES_DB={{ .POSTGRES_DB }}
POSTGRES_USER={{ .POSTGRES_USER }}
PGPASSWORD={{ .POSTGRES_PASSWORD }}
S3_BUCKET={{ .S3_BUCKET }}
{{ end }}
EOH
        destination = "secrets/db-credentials.env"
        env = true
      }

      # Template for the backup script
      template {
        data = <<EOH
#!/bin/sh
set -e

echo "Starting PostgreSQL backup at $(date)"

# Generate backup filename with timestamp
TIMESTAMP=$(date +%Y%m%d-%H%M%S)
BACKUP_FILE="backup-${TIMESTAMP}.sql.gz"

echo "Backing up database $POSTGRES_DB from $DB_HOST:$DB_PORT"

# Create the backup
pg_dump -h $DB_HOST -p $DB_PORT -U $POSTGRES_USER -d $POSTGRES_DB | gzip > /backups/$BACKUP_FILE

echo "Backup completed successfully at $(date)"
EOH
        destination = "local/backup-script.sh"
        perms = "0755"
      }
    }
  }
}
```

### ACL Template

Create an ACL template (saved as `templates/postgresql-backup/acl.hcl`):

```hcl
namespace "[[ .Namespace ]]" {
  policy = "read"
  
  # Write access to the specific job
  job "[[ .JobID ]]" {
    policy = "write"
  }
  
  # Read access to variables
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
