package core

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/hashicorp/nomad/api"
	"github.com/thunderbottom/damon/internal/config"
	"github.com/thunderbottom/damon/internal/interfaces"
	"github.com/thunderbottom/damon/internal/stream"
	"github.com/thunderbottom/damon/provider"
	"github.com/valkey-io/valkey-go"
)

// Core represents the main application engine
type Core struct {
	logger       *slog.Logger
	client       interfaces.NomadClient
	config       *config.Config
	streamMgr    interfaces.StreamManager
	cache        interfaces.CacheClient
	rawCache     valkey.Client
	doneOnce     sync.Once
	commitTicker *time.Ticker
}

// nomadClientAdapter adapts api.Client to interfaces.NomadClient
type nomadClientAdapter struct {
	client *api.Client
}

// NewNomadClientAdapter returns a new Nomad client adapter
func NewNomadClientAdapter(client *api.Client) interfaces.NomadClient {
	return &nomadClientAdapter{client: client}
}

// Address returns the Nomad client address
func (a *nomadClientAdapter) Address() string {
	return a.client.Address()
}

// Jobs returns a new Nomad client job adapter
func (a *nomadClientAdapter) Jobs() interfaces.JobsAPI {
	return &jobsAPIAdapter{a.client.Jobs()}
}

// EventStream returns a new Nomad client Events Stream adapter
func (a *nomadClientAdapter) EventStream() interfaces.EventStreamAPI {
	return &eventStreamAPIAdapter{a.client.EventStream()}
}

// ACLPolicies returns a new Nomad ACL policies adapter
func (a *nomadClientAdapter) ACLPolicies() interfaces.ACLPoliciesAPI {
	return &aclPoliciesAPIAdapter{a.client.ACLPolicies()}
}

// Services returns a new Nomad Services adapter
func (a *nomadClientAdapter) Services() interfaces.ServicesAPI {
	return &servicesAPIAdapter{a.client.Services()}
}

// Individual API adapters
type jobsAPIAdapter struct {
	jobs *api.Jobs
}

func (a *jobsAPIAdapter) List(q *api.QueryOptions) ([]*api.JobListStub, *api.QueryMeta, error) {
	return a.jobs.List(q)
}

func (a *jobsAPIAdapter) Register(job *api.Job, q *api.WriteOptions) (*api.JobRegisterResponse, *api.WriteMeta, error) {
	return a.jobs.Register(job, q)
}

func (a *jobsAPIAdapter) Deregister(jobID string, purge bool, q *api.WriteOptions) (string, *api.WriteMeta, error) {
	return a.jobs.Deregister(jobID, purge, q)
}

type eventStreamAPIAdapter struct {
	es *api.EventStream
}

func (a *eventStreamAPIAdapter) Stream(ctx context.Context, topics map[api.Topic][]string, index uint64, q *api.QueryOptions) (<-chan *api.Events, error) {
	return a.es.Stream(ctx, topics, index, q)
}

type aclPoliciesAPIAdapter struct {
	acl *api.ACLPolicies
}

func (a *aclPoliciesAPIAdapter) Upsert(policy *api.ACLPolicy, q *api.WriteOptions) (*api.ACLPolicy, error) {
	_, err := a.acl.Upsert(policy, q)
	if err != nil {
		return nil, err
	}
	return policy, nil
}

func (a *aclPoliciesAPIAdapter) Delete(policyName string, q *api.WriteOptions) error {
	_, err := a.acl.Delete(policyName, q)
	return err
}

type servicesAPIAdapter struct {
	services *api.Services
}

func (a *servicesAPIAdapter) List(q *api.QueryOptions) ([]*api.ServiceRegistrationListStub, *api.QueryMeta, error) {
	return a.services.List(q)
}

func (a *servicesAPIAdapter) Get(serviceID string, q *api.QueryOptions) ([]*api.ServiceRegistration, *api.QueryMeta, error) {
	return a.services.Get(serviceID, q)
}

// New creates a new Core instance with all initialized components
func New(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*Core, error) {
	// Initialize Nomad client
	apiClient, err := api.NewClient(api.DefaultConfig())
	if err != nil {
		return nil, fmt.Errorf("failed to initialize nomad client: %w", err)
	}

	// Wrap with adapter
	client := NewNomadClientAdapter(apiClient)
	logger.Info("initialized nomad client", "cluster", client.Address())

	// Initialize stream manager
	streamMgr := stream.NewManager(logger)

	core := &Core{
		logger:    logger,
		client:    client,
		config:    cfg,
		streamMgr: streamMgr,
	}

	// Initialize cache
	if err := core.initCache(ctx); err != nil {
		return nil, err
	}

	// Initialize providers and streams
	if err := core.initProviders(ctx); err != nil {
		return nil, err
	}

	return core, nil
}

// initCache initializes the Redis cache client
func (c *Core) initCache(ctx context.Context) error {
	cache, err := valkey.NewClient(valkey.ClientOption{
		InitAddress: c.config.CacheAddress(),
		Username:    c.config.CacheUsername(),
		Password:    c.config.CachePassword(),
		ClientName:  c.config.CacheClientName(),
	})
	if err != nil {
		return fmt.Errorf("failed to initialize cache: %w", err)
	}

	// Test cache connection
	_, err = cache.Do(ctx, cache.B().Ping().Build()).ToString()
	if err != nil {
		return fmt.Errorf("failed to connect to cache: %w", err)
	}

	// Store both the adapted interface and the raw client
	c.cache = interfaces.NewValkeyClientAdapter(cache)
	c.rawCache = cache
	c.logger.Info("initialized cache connection")
	return nil
}

// Run starts all providers and handles graceful shutdown
func (c *Core) Run(ctx context.Context) error {
	// Start the commit ticker
	commitInterval := c.config.GetCommitInterval()
	c.startCommitTicker(ctx, commitInterval)

	// Run all streams
	err := c.streamMgr.Consume(ctx)
	if err != nil && err != context.Canceled {
		return fmt.Errorf("error consuming streams: %w", err)
	}

	return nil
}

// Close performs cleanup actions
func (c *Core) Close() error {
	var err error
	c.doneOnce.Do(func() {
		if c.commitTicker != nil {
			c.commitTicker.Stop()
		}

		if c.rawCache != nil {
			c.rawCache.Close()
		}
	})
	return err
}

// startCommitTicker starts a ticker to commit indices periodically
func (c *Core) startCommitTicker(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		c.logger.Error("invalid commit interval", "interval", interval)
		return
	}

	c.commitTicker = time.NewTicker(interval)
	c.logger.Debug("starting index commit in background", "interval", interval)

	go func() {
		for {
			select {
			case <-c.commitTicker.C:
				c.commitIndices(ctx)
			case <-ctx.Done():
				c.logger.Info("stopping commit ticker")
				return
			}
		}
	}()
}

// commitIndices commits provider indices to the cache
func (c *Core) commitIndices(ctx context.Context) {
	indices := c.streamMgr.GetProviderIndices()
	for provider, idx := range indices {
		err := c.rawCache.Do(ctx, c.rawCache.B().
			Hset().Key("provider:"+provider).FieldValue().
			FieldValue("event-index", fmt.Sprint(idx)).
			Build()).Error()
		if err != nil {
			c.logger.Error("failed to commit index", "provider", provider, "error", err)
		} else {
			c.logger.Debug("committed index", "provider", provider, "index", idx)
		}
	}
}

// initProviders initializes all configured providers
func (c *Core) initProviders(ctx context.Context) error {
	provider.Initialize(c.logger)

	// Get list of providers from config
	providers := c.config.Koanf.MapKeys("provider")
	if len(providers) == 0 {
		return fmt.Errorf("no providers enabled in configuration")
	}

	// Try to load plugins if configured
	pluginDir := c.config.Koanf.String("plugins.directory")
	if pluginDir != "" {
		if err := provider.GetRegistry().LoadPlugins(pluginDir); err != nil {
			c.logger.Warn("error loading provider plugins", "error", err)
			// Continue even if plugin loading fails
		}
	}

	c.logger.Info("available provider types",
		"types", provider.GetRegistry().Types())

	for _, providerName := range providers {
		// Extract provider configuration
		providerConfig := c.config.Koanf.Cut("provider." + providerName)
		providerType := providerConfig.String("type")

		if providerType == "" {
			return fmt.Errorf("provider '%s' has no type specified", providerName)
		}

		// Create provider using factory
		p, err := provider.Create(
			ctx,
			providerType,
			providerName,
			c.logger.With("provider", providerName),
			c.client,
			c.cache,
			providerConfig,
		)
		if err != nil {
			return fmt.Errorf("failed to initialize provider %s: %w", providerName, err)
		}

		// Get last index from cache
		key := "provider:" + p.Name()
		var idx uint64 = 0

		result := c.rawCache.Do(ctx, c.rawCache.B().
			Hget().Key(key).Field("event-index").
			Build())

		if result.Error() == nil {
			idxStr, err := result.ToString()
			if err == nil {
				// Parse the string value to uint64
				parsedIdx, parseErr := strconv.ParseUint(idxStr, 10, 64)
				if parseErr == nil {
					idx = parsedIdx
					c.logger.Info("loaded existing event index",
						"provider", p.Name(),
						"index", idx)
				}
			}
		}

		namespace := providerConfig.String("namespace")
		if namespace == "" {
			c.logger.Warn("namespace not defined, using '*'", "provider", p.Name())
			namespace = "*"
		}

		// Add stream for this provider
		if err := c.streamMgr.AddStream(p, namespace, idx, c.client); err != nil {
			return fmt.Errorf("failed to add stream for provider %s: %w", p.Name(), err)
		}

		c.logger.Info("initialized provider", "provider", p.Name(), "type", providerType)
	}

	return nil
}
