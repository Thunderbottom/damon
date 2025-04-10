package nomad

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/hashicorp/nomad/api"
	"github.com/knadh/koanf/v2"
	"github.com/thunderbottom/damon/internal/interfaces"
	"github.com/thunderbottom/damon/internal/utils"
	"github.com/thunderbottom/damon/provider"
)

const (
	cacheKeyFormat = "nomad:%s:%s-%s"
)

// Register the provider factory
func init() {
	err := provider.RegisterProvider("nomad", New)
	if err != nil {
		slog.Error("failed to register nomad provider", "error", err)
	}
}

// Nomad represents the nomad provider
type Nomad struct {
	logger *slog.Logger
	client interfaces.NomadClient
	config *config
	cache  interfaces.CacheClient
	ctx    context.Context
}

// config holds provider-specific configuration
type config struct {
	Name        string
	Tags        []string
	JobTemplate string
	AclTemplate string
	Deregister  bool
	AddPayload  bool
}

// tplData holds data passed to job templates
type tplData struct {
	JobID       string
	Namespace   string
	Datacenters []string
	Tags        map[string]string
	Payload     map[string]any
}

type templates struct {
	job string
	acl string
}

// Load and validate both templates
func loadTemplates(jobPath, aclPath string) (*templates, error) {
	// Check job template
	if jobPath == "" {
		return nil, fmt.Errorf("job template path is empty")
	}

	jobTemp, err := utils.ReadFile(jobPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read job template: %w", err)
	}

	// Check ACL template
	if aclPath == "" {
		return nil, fmt.Errorf("ACL template path is empty")
	}

	aclTemp, err := utils.ReadFile(aclPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read ACL template: %w", err)
	}

	return &templates{
		job: string(jobTemp),
		acl: string(aclTemp),
	}, nil
}

// New creates a new Nomad provider instance
func New(ctx context.Context, logger *slog.Logger, client interfaces.NomadClient,
	cache interfaces.CacheClient, k *koanf.Koanf) (interfaces.Provider, error) {

	// Extract configuration with defaults
	name := k.String("name")
	if name == "" {
		name = "nomad" // Default name
	}

	// Load templates first since they're critical
	jobTemplatePath := k.String("job_template")
	aclTemplatePath := k.String("acl_template")

	if jobTemplatePath == "" || aclTemplatePath == "" {
		return nil, fmt.Errorf("both job_template and acl_template must be specified")
	}

	templates, err := loadTemplates(jobTemplatePath, aclTemplatePath)
	if err != nil {
		return nil, err
	}

	// Configure provider
	cfg := &config{
		Name:        name,
		Deregister:  k.Bool("deregister_job"),
		Tags:        k.Strings("tags"),
		AddPayload:  k.Bool("add_payload"),
		JobTemplate: templates.job,
		AclTemplate: templates.acl,
	}

	// Create new provider
	provider := &Nomad{
		logger: logger.With("provider", name),
		cache:  cache,
		config: cfg,
		client: client,
		ctx:    ctx,
	}

	logger.Info("initialized nomad provider",
		"name", name,
		"deregister_enabled", cfg.Deregister,
		"tags", cfg.Tags)

	return provider, nil
}

// Name returns the provider name
func (n *Nomad) Name() string {
	return n.config.Name
}

// Topics returns the topics required by this provider
func (n *Nomad) Topics() map[api.Topic][]string {
	// Subscribe to all job event topics
	return map[api.Topic][]string{
		api.TopicJob: {"*"},
	}
}

// Close performs any cleanup needed
func (n *Nomad) Close() error {
	n.logger.Info("closing nomad provider", "name", n.config.Name)
	return nil
}

// OnEvent processes job events
func (n *Nomad) OnEvent(event *api.Event) {
	// Extract job from event
	job, err := event.Job()
	if err != nil {
		n.logger.Error("failed to fetch job from event",
			"provider", n.Name(),
			"error", err,
			"event_type", event.Type)
		return
	}
	if job == nil {
		n.logger.Debug("job not found in event, skipping", "provider", n.Name())
		return
	}

	// Skip periodic jobs, batch jobs, or unhandled event types
	if job.IsPeriodic() || *job.Type == "batch" ||
		(event.Type != "JobRegistered" && event.Type != "JobDeregistered") {
		return
	}

	// Skip dead jobs that are registered
	if event.Type == "JobRegistered" && *job.Status == "dead" {
		return
	}

	// Check if damon is enabled for this job
	val, ok := job.Meta["damon-enable"]
	if !ok {
		n.logger.Info("damon-enable tag not found on job", "job", *job.ID)
		if err := n.deregisterJob(*job.ID, *job.Namespace); err != nil {
			n.logger.Error("failed to deregister job without damon-enable tag",
				"job", *job.ID,
				"namespace", *job.Namespace,
				"error", err)
		}
		return
	}

	enabled, err := strconv.ParseBool(val)
	if err != nil {
		n.logger.Error("failed to parse damon-enable meta", "job", *job.ID, "meta", val, "error", err)
		return
	}

	if !enabled {
		n.logger.Info("damon disabled on job, attempting to remove", "job", *job.ID)
		if err := n.deregisterJob(*job.ID, *job.Namespace); err != nil {
			n.logger.Error("failed to deregister disabled job",
				"job", *job.ID,
				"namespace", *job.Namespace,
				"error", err)
		}
		return
	}

	// Process job based on event type
	switch event.Type {
	case "JobRegistered":
		data, ok := n.prepareTemplateData(job)
		if !ok {
			return
		}

		if err := n.registerJob(*job.ID, &data); err != nil {
			n.logger.Error("failed to register job",
				"job", *job.ID,
				"error", err)
			// Cleanup after failed registration
			if deregErr := n.deregisterJob(*job.ID, *job.Namespace); deregErr != nil {
				n.logger.Error("cleanup after failed registration also failed",
					"job", *job.ID,
					"error", deregErr)
			}
		}

	case "JobDeregistered":
		if err := n.deregisterJob(*job.ID, *job.Namespace); err != nil {
			n.logger.Error("failed to deregister job on JobDeregistered event",
				"job", *job.ID,
				"namespace", *job.Namespace,
				"error", err)
		}
	}
}

// prepareTemplateData extracts metadata from job for template rendering
func (n *Nomad) prepareTemplateData(job *api.Job) (tplData, bool) {
	var data tplData
	data.Tags = make(map[string]string, len(n.config.Tags))

	for _, tag := range n.config.Tags {
		tagValue, ok := job.Meta[tag]
		if !ok {
			n.logger.Error("required tag not found in job meta", "job", *job.ID, "tag", tag)
			return data, false
		}
		data.Tags[tag] = tagValue
	}

	// Pass the entire job as payload if enabled
	if n.config.AddPayload {
		// Create a payload map with the job
		data.Payload = make(map[string]any)
		data.Payload["job"] = job
	}

	data.Datacenters = job.Datacenters
	data.JobID = "damon-" + *job.ID
	data.Namespace = *job.Namespace

	return data, true
}
