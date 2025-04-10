package interfaces

import (
	"context"
	"log/slog"

	"github.com/hashicorp/nomad/api"
	"github.com/knadh/koanf/v2"
	"github.com/valkey-io/valkey-go"
)

// CacheClient defines the interface for cache operations
type CacheClient interface {
	Close()
	Do(ctx context.Context, cmd any) any
	RawClient() valkey.Client
}

// NomadClient defines the interface for Nomad API operations
type NomadClient interface {
	Address() string
	Jobs() JobsAPI
	EventStream() EventStreamAPI
	ACLPolicies() ACLPoliciesAPI
	Services() ServicesAPI
}

// JobsAPI defines the Nomad Jobs API interface
type JobsAPI interface {
	List(q *api.QueryOptions) ([]*api.JobListStub, *api.QueryMeta, error)
	Register(job *api.Job, q *api.WriteOptions) (*api.JobRegisterResponse, *api.WriteMeta, error)
	Deregister(jobID string, purge bool, q *api.WriteOptions) (string, *api.WriteMeta, error)
}

// EventStreamAPI defines the Nomad Event Stream API interface
type EventStreamAPI interface {
	Stream(ctx context.Context, topics map[api.Topic][]string, index uint64, q *api.QueryOptions) (<-chan *api.Events, error)
}

// ACLPoliciesAPI defines the Nomad ACL Policies API interface
type ACLPoliciesAPI interface {
	Upsert(policy *api.ACLPolicy, q *api.WriteOptions) (*api.ACLPolicy, error)
	Delete(policyName string, q *api.WriteOptions) error
}

// Provider encapsulates the event stream provider interface
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

// ProviderFactory is a function type that creates a provider
type ProviderFactory func(context.Context, *slog.Logger, NomadClient, CacheClient, *koanf.Koanf) (Provider, error)

// StreamManager defines the interface for managing event streams
type StreamManager interface {
	AddStream(p Provider, namespace string, index uint64, client NomadClient) error
	Consume(ctx context.Context) error
	GetProviderIndices() map[string]uint64
}
