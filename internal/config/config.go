package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/knadh/koanf/parsers/toml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/posflag"
	"github.com/knadh/koanf/v2"
	flag "github.com/spf13/pflag"
)

// Config holds the application configuration
type Config struct {
	Koanf *koanf.Koanf
}

var defaultValues = map[string]any{
	"app.log_level":         "INFO",
	"cache.commit_interval": "10s",
	"provider.*.namespace":  "*",
}

// Load loads configuration from file and environment
func Load() (*Config, error) {
	ko := koanf.New(".")
	f := flag.NewFlagSet("config", flag.ContinueOnError)

	// Configure command line flags
	f.String("config", "config.toml", "path to the configuration file")
	if err := f.Parse(os.Args[1:]); err != nil {
		return nil, fmt.Errorf("error parsing flags: %w", err)
	}

	// Load command line flags
	if err := ko.Load(posflag.Provider(f, ".", ko), nil); err != nil {
		return nil, fmt.Errorf("error loading flags: %w", err)
	}

	// Load configuration file
	configPath := ko.String("config")
	if err := ko.Load(file.Provider(configPath), toml.Parser()); err != nil {
		return nil, fmt.Errorf("config file error: %w", err)
	}

	// Simplified env var loading
	envPrefix := "DAMON_"
	if err := ko.Load(env.Provider(envPrefix, ".", func(s string) string {
		return strings.ToLower(strings.Replace(
			strings.TrimPrefix(s, envPrefix),
			"__",
			".",
			-1))
	}), nil); err != nil {
		return nil, fmt.Errorf("env config error: %w", err)
	}

	return validateConfig(ko)
}

// applyDefaults sets default values for configuration options
func applyDefaults(ko *koanf.Koanf) {
	for key, value := range defaultValues {
		if strings.Contains(key, "*") {
			// Handle wildcard defaults for providers
			for _, provider := range ko.MapKeys("provider") {
				specificKey := strings.Replace(key, "*", provider, 1)
				if !ko.Exists(specificKey) {
					ko.Set(specificKey, value)
				}
			}
		} else if !ko.Exists(key) {
			ko.Set(key, value)
		}
	}
}

// validateConfig checks if the configuration is valid
func validateConfig(ko *koanf.Koanf) (*Config, error) {
	applyDefaults(ko)

	cfg := &Config{Koanf: ko}

	var violations []string
	// Required field checks
	if len(ko.MapKeys("provider")) == 0 {
		violations = append(violations, "no providers configured")
	}

	if len(violations) > 0 {
		return nil, fmt.Errorf("config validation failed: %s", strings.Join(violations, "; "))
	}

	return cfg, nil
}

// NewLogger creates a configured logger
func (c *Config) NewLogger() *slog.Logger {
	level := c.Koanf.MustString("app.log_level")
	opts := &slog.HandlerOptions{}

	switch level {
	case "DEBUG":
		opts.Level = slog.LevelDebug
	case "INFO":
		opts.Level = slog.LevelInfo
	case "ERROR":
		opts.Level = slog.LevelError
	default:
		fmt.Fprintf(os.Stderr, "undefined log level: %s\n", level)
		os.Exit(1)
	}

	return slog.New(slog.NewTextHandler(os.Stdout, opts))
}

// Validate ensures the configuration has all required values
func (c *Config) Validate() error {
	var errors []string

	// Check for required fields
	if len(c.Koanf.MapKeys("provider")) == 0 {
		errors = append(errors, "no providers configured")
	}

	if len(c.CacheAddress()) == 0 {
		errors = append(errors, "cache address not configured")
	}

	if c.GetCommitInterval() == 0 {
		errors = append(errors, "commit interval not configured or invalid")
	}

	// Validate each provider
	for _, provider := range c.Koanf.MapKeys("provider") {
		pType := c.Koanf.String(fmt.Sprintf("provider.%s.type", provider))
		if pType == "" {
			errors = append(errors, fmt.Sprintf("provider %s has no type", provider))
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("configuration validation failed: %s", strings.Join(errors, "; "))
	}

	return nil
}

// GetCommitInterval returns the commit interval duration
func (c *Config) GetCommitInterval() time.Duration {
	return c.Koanf.Duration("cache.commit_interval")
}

// CacheAddress returns the cache address
func (c *Config) CacheAddress() []string {
	return c.Koanf.Strings("cache.address")
}

// CacheUsername returns the cache username
func (c *Config) CacheUsername() string {
	return c.Koanf.String("cache.username")
}

// CachePassword returns the cache password
func (c *Config) CachePassword() string {
	return c.Koanf.String("cache.password")
}

// CacheClientName returns the cache client name
func (c *Config) CacheClientName() string {
	return c.Koanf.String("cache.client_name")
}
