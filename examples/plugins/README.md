# Damon Go Plugin Examples

This directory contains examples of plugins for Damon.

## What is a Plugin?

Plugins in Damon (and Go itself) are Go shared libraries (`.so` files) that implement the provider interface. This allows you to extend Damon's functionality without modifying the core codebase.

> [!WARNING]
> Using Go plugins is generally not recommended for production environments due to version compatibility issues and other limitations. Instead, consider submitting a pull request to include your provider directly in the Damon codebase. The plugin feature is provided for simplicity when you don't want to contribute to the main repository, but contributions are highly recommended.

## Example Plugin: Slack Notifier

This example plugin sends Slack notifications whenever specific Nomad events occur.

### Building the Plugin

To build the plugin:

```bash
# Ensure you use the exact same Go version used to build Damon
go build -buildmode=plugin -o slack.so ./slack/plugin.go
```

Important notes:
1. The Go version used to build the plugin MUST match the Go version used to build Damon
2. The plugin must be built on the same operating system as where Damon will run
3. The import paths in your plugin must match the import paths in Damon

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
topics = ["Job", "Deployment"]
```

## Creating Your Own Plugin

A Damon plugin must implement the following:

1. Export a `Register` function that registers your provider with Damon's registry
2. Implement the Provider interface

Here's a simple template:

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
