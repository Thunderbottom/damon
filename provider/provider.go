package provider

import (
	"context"
	"log/slog"

	"github.com/knadh/koanf/v2"
	"github.com/thunderbottom/damon/internal/interfaces"
)

// Config holds the configuration for creating a provider instance
type Config struct {
	// Name is the unique identifier for this provider
	Name string

	// Logger for provider-specific logging
	Logger *slog.Logger

	// Client is the Nomad API client
	Client interfaces.NomadClient

	// Koanf contains provider-specific configuration
	Koanf *koanf.Koanf

	// Cache client for persistent storage
	Cache interfaces.CacheClient

	// Context for provider operations
	Context context.Context
}

// UnknownProviderError is returned when attempting to create an unknown provider
type UnknownProviderError struct {
	ProviderType string
}

func (e *UnknownProviderError) Error() string {
	return "unknown provider type: " + e.ProviderType
}
