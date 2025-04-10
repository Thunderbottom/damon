job "[[ .JobID ]]" {
  datacenters = [ [[range $idx, $dc := .Datacenters]][[if $idx]], [[end]]"[[$dc]]"[[end]] ]
  namespace = "[[ .Namespace ]]"
  type = "batch"

  periodic {
    cron = "[[ index .Tags "backup-cron" ]]"
    prohibit_overlap = true
  }

  [[if and .Payload .Payload.job]]
  # Utilizing data from the job payload
  [[if .Payload.job.Priority]]
  priority = [[ .Payload.job.Priority ]]
  [[end]]

  # Add additional constraints if they exist in the source job
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

  group "backup" {
    count = 1

    [[if and .Payload .Payload.job]]
    # Copy resources/restart policy from the source job if available
    [[if .Payload.job.TaskGroups]]
    [[if index .Payload.job.TaskGroups 0]]
    [[if (index .Payload.job.TaskGroups 0).RestartPolicy]]
    restart {
      attempts = [[ (index .Payload.job.TaskGroups 0).RestartPolicy.Attempts ]]
      interval = "[[ (index .Payload.job.TaskGroups 0).RestartPolicy.Interval ]]"
      delay    = "[[ (index .Payload.job.TaskGroups 0).RestartPolicy.Delay ]]"
      mode     = "[[ (index .Payload.job.TaskGroups 0).RestartPolicy.Mode ]]"
    }
    [[end]]
    [[end]]
    [[end]]
    [[end]]

    task "backup" {
      driver = "docker"

      config {
        image   = "postgres:latest"
        command = "sh"
        args    = ["/local/backup-script.sh"]
        
        # Mount the necessary AWS credentials for S3 access
        mount {
          type     = "bind"
          source   = "local/backup-script.sh"
          target   = "/local/backup-script.sh"
          readonly = true
        }
      }

      # Resource allocation
      resources {
        cpu    = 500
        memory = 512
      }

      # Template for S3 backup configuration
      template {
        data = <<EOH
{{ with nomadVar "[[ index .Tags "backup-vars" ]]" }}
S3_BUCKET={{ .S3_BUCKET | default "example" }}
{{ end }}

# Extract parent job name and namespace from the job ID
# The JobID format is typically "{parentJobName}"
PARENT_JOB_NAME="[[ trimPrefix .JobID (index .Tags "job-prefix" | default "damon-") ]]"
PARENT_JOB_NAMESPACE="[[ .Namespace ]]"
EOH
        destination = "local/s3-config.env"
        env         = true
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
        env         = true
      }

      # Template for the backup script
      template {
        data = <<EOH
#!/bin/bash
set -e

echo "Starting PostgreSQL backup at $(date)"

# Install AWS CLI
echo "Installing AWS CLI..."
apt-get update && apt-get install -y curl unzip
curl "https://awscli.amazonaws.com/awscli-exe-linux-x86_64.zip" -o "awscliv2.zip"
unzip awscliv2.zip
./aws/install
rm -rf awscliv2.zip aws

# Generate backup filename with timestamp
TIMESTAMP=$(date +%Y%m%d-%H%M%S)
S3_PATH="${PARENT_JOB_NAME}-${PARENT_JOB_NAMESPACE}"
BACKUP_FILE="backup-${TIMESTAMP}.sql.gz"

echo "Backing up database $POSTGRES_DB from $DB_HOST:$DB_PORT"
echo "Parent job: $PARENT_JOB_NAME in namespace: $PARENT_JOB_NAMESPACE"

# Create the backup 
# Note: PGPASSWORD is set as an environment variable via the db-credentials.env template
pg_dump -h $DB_HOST -p $DB_PORT -U $POSTGRES_USER -d $POSTGRES_DB | gzip > /tmp/$BACKUP_FILE

# Upload to S3
echo "Uploading backup to s3://$S3_BUCKET/$S3_PATH/$BACKUP_FILE"
aws s3 cp /tmp/$BACKUP_FILE s3://$S3_BUCKET/$S3_PATH/$BACKUP_FILE

# Delete the local file
rm /tmp/$BACKUP_FILE

echo "Backup completed successfully at $(date)"
EOH
        destination = "local/backup-script.sh"
        perms       = "0755"
      }
    }
  }
}
