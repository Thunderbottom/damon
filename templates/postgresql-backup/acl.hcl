# ACL Policy for PostgreSQL backup jobs
# Provides read-only access to required services and variables

# Namespace-specific permissions
namespace "[[ .Namespace ]]" {
  # Read-only access to jobs in this namespace
  policy = "read"
  
  # Read-only access to variables referenced in backup-vars meta tag
  variables {
    # Use a simple space-separated list of variable paths
    path "[[index .Tags "backup-vars"]]" {
      capabilities = ["read"]
    }
  }
}

# Allow read access to services for service discovery
service {
  policy = "read"
}

# Allow read access to nodes for service discovery
node {
  policy = "read"
}

# Read-only access to agent information
agent {
  policy = "read"
}

# Basic operator read access
operator {
  policy = "read"
}
