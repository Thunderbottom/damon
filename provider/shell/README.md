# Shell Provider

The Shell Provider creates Nomad jobs that execute shell scripts in response to Nomad events. This provider allows you to define shell script handlers that run in Nomad jobs whenever specific events occur in your cluster.

## Features

- **Event-Driven Scripts**: Automatically create Nomad jobs to run your shell scripts when events occur
- **Scripts from File**: Define your script in a separate file for better organization and syntax highlighting
- **Shell Agnostic**: Use any shell of your choice by specifying the appropriate shebang line in your script
- **Nomad Integration**: Leverages Nomad's job scheduling and isolation capabilities
- **Periodic Schedule Support**: Optionally run scripts on a cron schedule rather than event-triggered
- **Event Filtering**: Execute scripts only for specific event types or namespaces
- **Environment Variables**: Pass event data to your scripts through environment variables

## How It Works

The Shell Provider works by:

1. Listening for Nomad events based on your configured topics and event types
2. When a matching event is received, creating a Nomad job that executes your shell script
3. The job runs in the namespace where the event occurred
4. The script is deployed as a template with executable permissions and run directly

## Configuration

Here's a complete configuration example for the Shell provider:

```toml
[provider.job_monitor]
type = "shell"
# Path to your shell script file (required)
script_path = "/etc/damon/scripts/job-monitor.sh"

# Job configuration
job_type = "batch"  # "batch" or "system"
priority = 50       # 1-100 

# Resources for the job
resources = { cpu = 200, memory = 256 }

# Optional periodic schedule (for cron jobs)
# periodic = { cron = "*/10 * * * *", prohibit_overlap = true }

# Topics to subscribe to (defaults to Job events if not specified)
topics = { Job = "*", Deployment = "*" }

# Event types to filter (omit to handle all events)
event_types = ["JobRegistered", "JobDeregistered", "DeploymentStatusUpdate"]

# Namespace filter (omit to handle events from all namespaces)
namespace = "default"

# Environment variables to pass to the script
environment = { 
  LOG_LEVEL = "info",
  NOTIFY_URL = "https://example.com/webhook"
}
```

### Configuration Options

| Option | Description | Default |
|--------|-------------|---------|
| `script_path` | Path to the shell script file (required) | (required) |
| `job_type` | Type of Nomad job to create: "batch" or "system" | "batch" |
| `priority` | Job priority (1-100) | 50 |
| `resources` | CPU (MHz) and memory (MB) resources | {cpu=100, memory=128} |
| `periodic` | Cron schedule and overlap settings | nil (event-triggered) |
| `topics` | Nomad event topics to subscribe to | {Job="*"} |
| `event_types` | Event types to filter (omit to handle all) | [] |
| `namespace` | Namespace to filter events (omit for all) | "" |
| `environment` | Environment variables to pass to scripts | {} |

## Event Data in Scripts

The provider automatically passes event information to your script through environment variables:

### Common Variables

- `EVENT_TYPE`: The event type (e.g., "JobRegistered")
- `EVENT_TOPIC`: The event topic (e.g., "Job")
- `EVENT_INDEX`: The event index
- `EVENT_NAMESPACE`: The namespace where the event occurred (only available for Job, Allocation, Deployment, Evaluation, Service, CSIPlugin, CSIVolume, and HostVolume events - extracted from the respective objects)

### Job Event Variables (when Topic is "Job")

- `JOB_ID`: The ID of the job
- `JOB_NAME`: The name of the job
- `JOB_TYPE`: The type of job (e.g., "service", "batch")
- `JOB_STATUS`: The status of the job
- `JOB_NAMESPACE`: The namespace of the job
- `JOB_DATACENTERS`: Comma-separated list of datacenters
- `JOB_META_*`: Job metadata values with keys converted to uppercase

### Deployment Event Variables (when Topic is "Deployment")

- `DEPLOYMENT_ID`: The ID of the deployment
- `DEPLOYMENT_JOB_ID`: The ID of the job being deployed
- `DEPLOYMENT_STATUS`: The status of the deployment
- `DEPLOYMENT_NAMESPACE`: The namespace of the deployment

### Service Event Variables (when Topic is "Service")

- `SERVICE_ID`: The ID of the service
- `SERVICE_NAME`: The name of the service
- `SERVICE_ADDRESS`: The service address
- `SERVICE_PORT`: The service port
- `SERVICE_NAMESPACE`: The namespace of the service
- `SERVICE_TAGS`: Comma-separated list of service tags

### Other event types have similar variables specific to their content.

## Using Different Shells

Since the script is saved to a file and executed directly (rather than passed to `/bin/sh -c`), you can use any shell by including the appropriate shebang line at the top of your script:

```bash
#!/bin/bash
# Use Bash features
echo "Running in Bash"
```

```fish
#!/usr/bin/env fish
# Use Fish features
echo "Running in Fish shell"
```

```zsh
#!/usr/bin/env zsh
# Use Zsh features
echo "Running in Zsh"
```

Make sure the shell you specify is available on the Nomad clients where your job will run.

## Example Script: Job Notification

Here's an example shell script that sends notifications when jobs are registered:

```bash
#!/bin/bash
# job-notify.sh
# This script sends notifications for job events

echo "Processing event: $EVENT_TYPE for topic $EVENT_TOPIC"

if [ "$EVENT_TOPIC" = "Job" ]; then
  echo "Job: $JOB_ID ($JOB_NAMESPACE)"
  echo "Status: $JOB_STATUS"
  
  # Send notification to webhook
  if [ "$EVENT_TYPE" = "JobRegistered" ]; then
    curl -s -X POST \
      -H "Content-Type: application/json" \
      -d "{\"text\":\"Job '$JOB_ID' registered with status '$JOB_STATUS'\"}" \
      "$WEBHOOK_URL"
  elif [ "$EVENT_TYPE" = "JobDeregistered" ]; then
    curl -s -X POST \
      -H "Content-Type: application/json" \
      -d "{\"text\":\"Job '$JOB_ID' deregistered\"}" \
      "$WEBHOOK_URL"
  fi
fi

# Exit successfully
exit 0
```

## Example Use Cases

### Job Status Notifications

Create shell scripts that send notifications when jobs are registered or deregistered:

```toml
[provider.job_notifications]
type = "shell"
script_path = "/etc/damon/scripts/job-notify.sh"
topics = { Job = "*" }
event_types = ["JobRegistered", "JobDeregistered"]
environment = { WEBHOOK_URL = "https://hooks.slack.com/services/T00000/B00000/XXXXX" }
```

### Deployment Monitoring

Monitor deployments and track their progress:

```toml
[provider.deployment_tracker]
type = "shell"
script_path = "/etc/damon/scripts/deployment-tracker.sh"
topics = { Deployment = "*" }
event_types = ["DeploymentStatusUpdate"]
environment = { NOTIFY_URL = "https://example.com/notify" }
```

### Periodic System Cleanup

Use the periodic feature to run cleanup operations on a schedule:

```toml
[provider.cleanup]
type = "shell"
script_path = "/etc/damon/scripts/cleanup.sh"
job_type = "batch"
periodic = { cron = "0 2 * * *", prohibit_overlap = true }  # Run at 2 AM daily
```

## Security Considerations

1. **Isolation**: Scripts run in the context of a Nomad job, providing isolation from other jobs.
2. **Resource Limits**: Configure appropriate CPU and memory limits to prevent resource exhaustion.
3. **Access Control**: Scripts run with the permissions of the Nomad job, so follow Nomad's security best practices.
4. **Secrets Handling**: Use Nomad's vault integration for sensitive information instead of hardcoding in scripts.

## Troubleshooting

### Job Creation Failed

1. Check Damon logs for detailed error messages about job creation failures
2. Verify your shell script syntax
3. Ensure your Nomad cluster has available resources for the job
4. Check that Damon has permissions to create jobs in the target namespace

### Script Execution Issues

1. Check the Nomad UI or CLI for job status and logs
2. Verify that your script has the correct shebang line for the shell you want to use
3. Ensure the shell specified in your shebang line is available on the Nomad client
4. Check for syntax errors in your shell script
5. Ensure environment variables are correctly referenced in your script
