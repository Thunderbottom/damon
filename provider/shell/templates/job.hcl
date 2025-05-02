job "{{.JobName}}" {
  datacenters = ["dc1"]
  type = "{{.JobType}}"
  namespace = "{{.Namespace}}"

  priority = {{.Priority}}

  {{if .Periodic}}
  periodic {
    cron = "{{.Periodic.Cron}}"
    prohibit_overlap = {{.Periodic.ProhibitOverlap}}
  }
  {{end}}

  group "shell" {
    count = 1

    task "script" {
      driver = "exec"

      config {
        command = "${NOMAD_TASK_DIR}/script.sh"
      }

      # The script content as a template
      template {
        data = <<EOH
{{.ShellScript}}
EOH
        destination = "${NOMAD_TASK_DIR}/script.sh"
        perms = "0755"  # Make the script executable
      }

      # Resource limits
      resources {
        cpu    = {{.Resources.CPU}}
        memory = {{.Resources.Memory}}
      }
    }
  }
}
