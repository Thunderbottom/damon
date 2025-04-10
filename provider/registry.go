package provider

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"plugin"
	"strings"
	"sync"

	"github.com/knadh/koanf/v2"
	"github.com/thunderbottom/damon/internal/interfaces"
)

// Registry manages provider registration and creation
type Registry struct {
	mu        sync.RWMutex
	factories map[string]interfaces.ProviderFactory
	logger    *slog.Logger
}

// NewRegistry creates a new provider registry
func NewRegistry(logger *slog.Logger) *Registry {
	if logger == nil {
		logger = slog.Default()
	}

	return &Registry{
		factories: make(map[string]interfaces.ProviderFactory),
		logger:    logger,
	}
}

// Register adds a provider factory to the registry
func (r *Registry) Register(name string, factory interfaces.ProviderFactory) error {
	switch {
	case name == "":
		return fmt.Errorf("provider name cannot be empty")
	case factory == nil:
		return fmt.Errorf("factory function for provider '%s' cannot be nil", name)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// Check if provider already exists
	if _, exists := r.factories[name]; exists {
		return fmt.Errorf("provider type '%s' is already registered", name)
	}

	r.factories[name] = factory
	r.logger.Debug("registered provider type", "provider", name)
	return nil
}

// Create instantiates a provider by type
func (r *Registry) Create(
	ctx context.Context,
	providerType string,
	name string,
	logger *slog.Logger,
	client interfaces.NomadClient,
	cache interfaces.CacheClient,
	config *koanf.Koanf,
) (interfaces.Provider, error) {
	r.mu.RLock()
	factory, exists := r.factories[providerType]
	r.mu.RUnlock()

	if !exists {
		// List available providers for better error message
		availableTypes := r.Types()
		return nil, fmt.Errorf("unknown provider type: '%s'. Available types: %s",
			providerType, strings.Join(availableTypes, ", "))
	}

	provider, err := factory(ctx, logger, client, cache, config)
	if err != nil {
		return nil, fmt.Errorf("failed to create provider '%s' of type '%s': %w",
			name, providerType, err)
	}

	return provider, nil
}

// Types returns a list of all registered provider types
func (r *Registry) Types() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	types := make([]string, 0, len(r.factories))
	for t := range r.factories {
		types = append(types, t)
	}
	return types
}

// LoadPlugins searches for and loads provider plugins from the given directory
func (r *Registry) LoadPlugins(pluginDir string) error {
	_, err := os.Stat(pluginDir)
	if os.IsNotExist(err) {
		r.logger.Info("plugin directory does not exist, skipping plugin loading",
			"directory", pluginDir)
		return nil
	}
	if err != nil {
		return fmt.Errorf("error accessing plugin directory: %w", err)
	}

	files, err := os.ReadDir(pluginDir)
	if err != nil {
		return fmt.Errorf("error reading plugin directory: %w", err)
	}

	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".so") {
			continue
		}

		pluginPath := filepath.Join(pluginDir, file.Name())
		r.logger.Debug("loading plugin", "path", pluginPath)

		// Load plugin
		p, err := plugin.Open(pluginPath)
		if err != nil {
			r.logger.Error("failed to load plugin",
				"path", pluginPath,
				"error", err)
			continue
		}

		// Look for Register symbol
		registerSym, err := p.Lookup("Register")
		if err != nil {
			r.logger.Error("plugin does not export Register function",
				"path", pluginPath,
				"error", err)
			continue
		}

		// Call Register function
		register, ok := registerSym.(func(*Registry) error)
		if !ok {
			r.logger.Error("plugin Register symbol has wrong type",
				"path", pluginPath)
			continue
		}

		if err := register(r); err != nil {
			r.logger.Error("plugin registration failed",
				"path", pluginPath,
				"error", err)
			continue
		}

		r.logger.Info("successfully loaded plugin", "path", pluginPath)
	}

	return nil
}

// Global registry instance
var (
	defaultRegistry *Registry
	once            sync.Once
)

// GetRegistry returns the default registry, creating it if necessary
func GetRegistry() *Registry {
	once.Do(func() {
		defaultRegistry = NewRegistry(slog.Default())
	})
	return defaultRegistry
}

// RegisterProvider adds a provider factory to the default registry
func RegisterProvider(name string, factory interfaces.ProviderFactory) error {
	return GetRegistry().Register(name, factory)
}

// Create is a convenience function that uses the default registry
func Create(
	ctx context.Context,
	providerType string,
	name string,
	logger *slog.Logger,
	client interfaces.NomadClient,
	cache interfaces.CacheClient,
	config *koanf.Koanf,
) (interfaces.Provider, error) {
	return GetRegistry().Create(ctx, providerType, name, logger, client, cache, config)
}

// Initialize is a convenience function to initialize all common providers
func Initialize(logger *slog.Logger) {
	registry := GetRegistry()
	if logger != nil {
		registry.logger = logger
	}

	// The init functions of imported packages will register their providers
	// This function can also explicitly register built-in providers if needed
	logger.Debug("provider registry initialized", "count", len(registry.Types()))
}
