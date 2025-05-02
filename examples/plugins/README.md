# Damon Go Plugin Examples

This directory contains examples of plugins for Damon that extend its functionality.

## What is a Plugin?

Plugins in Damon are Go shared libraries (`.so` files) that implement the provider interface. They allow you to extend Damon's functionality without modifying the core codebase.

> [!WARNING]
> Using Go plugins has limitations for production environments due to version compatibility issues. For production use, consider submitting a pull request to include your provider directly in the Damon codebase. The plugin feature is provided for development and testing when you don't want to modify the main repository.

## Plugin Compatibility Requirements

For plugins to work properly with Damon:

1. The Go version used to build the plugin MUST match the Go version used to build Damon
2. The plugin must be built on the same operating system as where Damon will run
3. The import paths in your plugin must match the import paths in Damon

If these requirements aren't met, you'll receive errors when Damon tries to load the plugin.

## Example Plugin: Slack Notifier

This example plugin sends Slack notifications whenever specific Nomad events occur.

### Building the Plugin

To build the plugin:

```bash
# Ensure you use the exact same Go version used to build Damon
go build -buildmode=plugin -o slack.so ./slack/plugin.go
```

### Using the Plugin

1. Place the compiled `.so` file in a directory
2. Configure Damon to look for plugins in that directory:

```toml
[plugins]
directory = "/path/to/plugins"
```

3. Start Damon, and it will automatically load and register your plugin

### Plugin Configuration

Once loaded, configure the plugin like any other provider:

```toml
[provider.slack]
type = "slack"  # This must match what your plugin registers as
webhook_url = "https://hooks.slack.com/services/T00000000/B00000000/XXXXXXXXXXXXXXXXXXXXXXXX"
channel = "#nomad-events"
username = "Damon Bot"
topics = { "Job" = "*", "Deployment" = "*" }
event_types = ["JobRegistered", "DeploymentFailed"]  # Optional filter
```

## Creating Your Own Plugin

A Damon plugin must implement the following:

1. Export a `Register` function that registers your provider with Damon's registry
2. Implement the Provider interface

### Provider Interface

All providers must implement this interface:

```go
type Provider interface {
    // Name returns the name of the provider
    Name() string

    // OnEvent executes the provider logic on an event stream event
    OnEvent(event *api.Event)

    // Topics returns the topics required by the event stream provider
    Topics() map[api.Topic][]string

    // Close performs any cleanup needed when shutting down
    Close() error
}
```

### Basic Plugin Template

Here's a simple template to get you started:

```go
package main

import (
	"context"
	"log/slog"

	"github.com/hashicorp/nomad/api"
	"github.com/knadh/koanf/v2"
	"github.com/thunderbottom/damon/internal/interfaces"
	"github.com/thunderbottom/damon/provider"
)

// Register is the entry point for the plugin
func Register(registry *provider.Registry) error {
	return registry.Register("yourprovider", New)
}

// YourProvider implements the Provider interface
type YourProvider struct {
	// Your provider fields here
	logger *slog.Logger
	name   string
}

// New creates a new instance of your provider
func New(
	ctx context.Context,
	logger *slog.Logger,
	client interfaces.NomadClient,
	cache interfaces.CacheClient,
	config *koanf.Koanf,
) (interfaces.Provider, error) {
	// Initialize your provider
	return &YourProvider{
		logger: logger,
		name:   config.String("name"),
	}, nil
}

// Name returns the provider name
func (p *YourProvider) Name() string {
	return p.name
}

// OnEvent handles Nomad events
func (p *YourProvider) OnEvent(event *api.Event) {
	// Handle the event
}

// Topics returns what event topics to subscribe to
func (p *YourProvider) Topics() map[api.Topic][]string {
	return map[api.Topic][]string{
		api.TopicJob: {"*"},
	}
}

// Close performs cleanup
func (p *YourProvider) Close() error {
	return nil
}
```

## Example Plugin Implementation: Slack Provider

The included Slack provider is a real-world example that sends Nomad event notifications to Slack channels.

### Key Features

The Slack provider demonstrates several important plugin concepts:

- Reading configuration values from TOML
- Processing different Nomad event types
- Formatting structured messages for an external API
- Error handling and logging

### Code Structure

A well-organized provider typically has:

1. **Configuration Structure**: For provider-specific settings
2. **Event Handlers**: Methods to process different types of events
3. **External API Integration**: Code to call external services
4. **Helper Methods**: Utility functions for common tasks

### Implementing External Integrations

When creating plugins that integrate with external systems:

1. Use appropriate error handling for network operations
2. Consider retry logic for transient failures
3. Implement proper authentication with API keys or tokens
4. Format messages appropriately for the target system

## Other Plugin Ideas

Here are some ideas for custom plugins you could build:

### Email Notification Provider

Send email alerts for important Nomad events:

```go
// EmailProvider sends email notifications for Nomad events
type EmailProvider struct {
    logger    *slog.Logger
    name      string
    smtpConfig *SMTPConfig
    topics    map[api.Topic][]string
}
```

### Metrics Provider

Send Nomad event metrics to monitoring systems:

```go
// MetricsProvider sends event metrics to Prometheus, StatsD, etc.
type MetricsProvider struct {
    logger *slog.Logger
    name   string
    client MetricsClient
}
```

### Webhook Provider

Forward Nomad events to arbitrary HTTP endpoints:

```go
// WebhookProvider forwards events to configured HTTP endpoints
type WebhookProvider struct {
    logger   *slog.Logger
    name     string
    endpoints []string
    client   *http.Client
}
```

## Best Practices

When developing plugins for Damon:

1. **Validate Configuration**: Check all required configuration values at startup
2. **Handle Errors Gracefully**: Log errors but try to continue operating
3. **Clean Up Resources**: Properly close connections in the `Close()` method
4. **Maintain Compatibility**: Follow Damon's versioning for interface changes
5. **Efficient Event Processing**: Only process events your plugin cares about
6. **Proper Logging**: Use structured logging with appropriate log levels
7. **Security**: Handle credentials securely, never log sensitive information

## Debugging Plugins

If you're having trouble with a plugin:

1. Build with debug information: `go build -buildmode=plugin -gcflags="all=-N -l" -o myplugin.so`
2. Run Damon with higher log level: `DAMON_APP__LOG_LEVEL=DEBUG ./damon`
3. Check for version mismatches between Go used to build Damon and the plugin
4. Verify the plugin is in the correct directory as specified in config
5. Test your plugin's logic separately before integration with Damon

## Contributing Plugins

If you've developed a useful plugin, consider contributing it to the main Damon repository:

1. Create a new provider directory in `provider/yourprovider/`
2. Implement the provider interface fully
3. Add tests in `provider/yourprovider/yourprovider_test.go`
4. Add documentation in `provider/yourprovider/README.md`
5. Submit a pull request to the Damon repository
