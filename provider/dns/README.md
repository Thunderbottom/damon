# DNS Provider

The DNS Provider creates a lightweight DNS server that automatically registers Nomad services and makes them discoverable through DNS queries. This enables service discovery across your Nomad cluster without requiring additional infrastructure.

## Features

- **Automatic Service Registration**: Monitors Nomad service registration events and creates DNS records
- **DNS Query Support**: Responds to A, SRV, and NS queries for registered services
- **Service Filtering**: Ability to filter services by tags
- **Namespace and Datacenter Awareness**: Query services across namespaces and datacenters
- **Periodic Refresh**: Automatically syncs with Nomad services to maintain consistency

## How It Works

The DNS Provider works by:

1. Listening for Nomad service registration and deregistration events
2. Storing service information in the cache with the service name as key
3. Running a DNS server that responds to queries for registered services
4. Periodically refreshing the service catalog to maintain consistency

## Configuration

Here's a sample configuration for the DNS provider:

```toml
# Basic configuration
[provider.dns]
type = "dns"
namespace = "*"                  # Namespace to watch for services, "*" for all namespaces
listen_addr = ":5353"            # Address for the DNS server to listen on
ttl = 30                         # TTL for DNS records in seconds (optional, default: 30)
tags = ["production", "public"]  # Only register services with these tags (optional)
```

You can register multiple DNS providers with different configurations:

```toml
# Listen on different addresses for different service filters
[provider.internal_dns]
type = "dns"
namespace = "*"
listen_addr = ":5353"
tags = ["internal"]

[provider.external_dns]
type = "dns"
namespace = "production"
listen_addr = ":5354"
tags = ["external"]
```

## DNS Query Formats

The DNS provider supports multiple query formats for different use cases:

| Query Format | Example | Description |
|--------------|---------|-------------|
| `servicename` | `postgres-db` | Basic A record lookup, returns IP address |
| `servicename.namespace` | `postgres-db.production` | Service in specific namespace |
| `servicename.namespace.datacenter.service` | `postgres-db.production.dc1.service` | Fully qualified service name |
| `_servicename._tcp.service` | `_postgres-db._tcp.service` | SRV record with port information |

All query formats are case-insensitive.

## Usage

### Service Registration in Nomad

Services are automatically registered when they appear in Nomad. To ensure a service is included in DNS, make sure it has the right tags if you've configured tag filtering:

```hcl
job "web-app" {
  group "app" {
    network {
      port "http" {
        to = 8080
      }
    }

    service {
      name = "webapp"
      port = "http"
      tags = ["production", "public"]  # Tags for filtering by the DNS provider
    }
  }
}
```

### Example DNS Queries

#### A Record Query

For direct IP address lookups:

```bash
# Basic service lookup
dig @localhost -p 5353 webapp

# Service in specific namespace
dig @localhost -p 5353 webapp.default
```

Response:
```
;; ANSWER SECTION:
webapp.      30    IN    A    10.0.0.123
```

#### SRV Record Query

For service discovery with port information:

```bash
dig @localhost -p 5353 _webapp._tcp.service SRV
```

Response:
```
;; ANSWER SECTION:
_webapp._tcp.service. 30 IN SRV 10 10 8080 webapp.default.dc1.service.

;; ADDITIONAL SECTION:
webapp.default.dc1.service. 30 IN A 10.0.0.123
```

The SRV record includes:
- Target hostname: `<service-name>.<namespace>.svc.<datacenter>.`
- Port information from the Nomad service registration
- Priority and weight values (useful for load balancing)

### Using with Applications

To use this DNS server for service discovery in your applications, you need to configure them to use the Damon DNS server for resolution.

#### Configuring Nginx

Here's an example of using the DNS server with Nginx for service discovery:

```hcl
template {
  data = <<EOF
server {
    listen 80;
    
    location / {
        root /usr/share/nginx/html;
        index index.html;
    }
    
    location /api {
        # Use Damon's DNS server for resolution
        resolver 127.0.0.1:5353;
        set $api_backend "api.service";
        proxy_pass http://$api_backend:8000;
    }
}
EOF
  destination = "local/nginx.conf"
}
```

Note the `resolver` directive that tells Nginx to use the Damon DNS server on port 5353, and the use of a variable for the backend to ensure DNS resolution happens at request time.

#### Using in Docker/Nomad

When using in Docker or Nomad, you'll need to:

1. Ensure the container can reach the Damon DNS server (typically on the host)
2. Configure the application to use the DNS server for resolution
3. For Docker networks, you may need to use `--dns` flag or edit `/etc/resolv.conf`

### Adding to /etc/resolv.conf

To use this DNS server for service resolution across your infrastructure, add it to your `/etc/resolv.conf`:

```
nameserver 127.0.0.1
```

If running on a specific port (e.g., 5353), you'll need to set up a DNS forwarder like `dnsmasq` or `systemd-resolved`.

Example dnsmasq configuration to forward service queries to Damon:
```
server=/service/127.0.0.1#5353
```

## Example: Web Application with Service Discovery

Here's a more complete example of deploying a web service and an API service with DNS-based service discovery:

```hcl
job "web-stack" {
  datacenters = ["dc1"]
  type = "service"

  group "frontend" {
    count = 2

    network {
      port "http" {
        to = 80
      }
    }

    service {
      name = "frontend"
      tags = ["web", "public"]
      port = "http"
      check {
        type     = "http"
        path     = "/"
        interval = "10s"
        timeout  = "2s"
      }
    }

    task "nginx" {
      driver = "docker"
      
      config {
        image = "nginx:latest"
        ports = ["http"]
        volumes = [
          "local/nginx.conf:/etc/nginx/conf.d/default.conf"
        ]
      }
      
      template {
        data = <<EOF
server {
    listen 80;
    
    location / {
        root /usr/share/nginx/html;
        index index.html;
    }
    
    location /api {
        # Use Damon's DNS server for resolution
        resolver 127.0.0.1:5353;
        set $api_backend "api.service";
        proxy_pass http://$api_backend:8000;
    }
}
EOF
        destination = "local/nginx.conf"
      }
    }
  }

  group "api" {
    count = 3

    network {
      port "api" {
        to = 8000
      }
    }

    service {
      name = "api"
      tags = ["api", "internal"]
      port = "api"
      check {
        type     = "http"
        path     = "/health"
        interval = "10s"
        timeout  = "2s"
      }
    }

    task "api-server" {
      driver = "docker"
      
      config {
        image = "python:3.9-alpine"
        ports = ["api"]
        command = "python"
        args = ["-m", "http.server", "8000"]
      }
    }
  }
}
```

With this setup and the DNS provider configured:

1. The frontend service will be accessible via DNS as `frontend.service`
2. The API service will be accessible via DNS as `api.service`
3. The frontend's Nginx configuration will resolve the API service using Damon's DNS server

## Advanced Features

### Multi-Instance Services

When multiple instances of a service are registered with the same name, the DNS provider will return all IP addresses in response to A record queries. This enables simple round-robin load balancing at the DNS level.

### Namespace and Datacenter Spanning

The DNS provider can discover services across:
- Multiple namespaces if configured with `namespace = "*"`
- Multiple datacenters if your Nomad cluster spans datacenters

This allows for sophisticated service discovery patterns across your infrastructure.

### Health Check Integration

The DNS provider only registers services that have passed their health checks, ensuring that your applications only discover healthy service instances.

## Troubleshooting

### Service Not Appearing in DNS

1. Verify the service is registered in Nomad: `nomad service list`
2. Check if the service has the required tags (if configured)
3. Inspect the Damon logs for errors or warnings
4. Try manually refreshing the service catalog by restarting Damon

### DNS Server Not Responding

1. Check the `listen_addr` configuration
2. Ensure no other service is using the same port
3. Verify that your firewall allows traffic on the configured port
4. Test using `dig` or `nslookup` to query the DNS server directly

### Testing DNS Resolution

To test if your DNS server is working correctly:

```bash
# Test A record resolution
dig @localhost -p 5353 myservice

# Test SRV record resolution
dig @localhost -p 5353 _myservice._tcp.service SRV

# Check if the DNS server is responding
dig @localhost -p 5353 version.bind TXT CH
```

### Debugging DNS in Applications

If your application isn't resolving services correctly:

1. Check that your application is configured to use the correct DNS server
2. Verify the resolver configuration (e.g., in Nginx)
3. Try using a DNS lookup utility within the container/environment
4. Inspect the Damon logs for incoming DNS requests when your application makes a request
