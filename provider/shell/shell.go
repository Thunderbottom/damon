package shell

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"text/template"

	"github.com/hashicorp/nomad/api"
	"github.com/hashicorp/nomad/jobspec"
	"github.com/knadh/koanf/v2"
	"github.com/thunderbottom/damon/internal/interfaces"
	"github.com/thunderbottom/damon/internal/utils"
	"github.com/thunderbottom/damon/provider"
)

// Embed the job template in the binary
//
//go:embed templates/job.hcl
var jobTemplateFS embed.FS

// Register the provider factory
func init() {
	err := provider.RegisterProvider("shell", New)
	if err != nil {
		slog.Error("failed to register shell provider", "error", err)
	}
}

// Shell represents the shell provider
type Shell struct {
	logger  *slog.Logger
	client  interfaces.NomadClient
	config  *Config
	ctx     context.Context
	jobTmpl *template.Template
}

// Config holds provider-specific configuration
type Config struct {
	// Name is the unique identifier for this provider
	Name string

	// ScriptPath is the path to the shell script file
	ScriptPath string

	// JobType defines the Nomad job type (batch or system)
	JobType string

	// Priority sets the job priority (1-100)
	Priority int

	// Periodic contains cron configuration (if set, enables periodic execution)
	Periodic *PeriodicConfig

	// Topics to subscribe to
	Topics map[api.Topic][]string

	// EventTypes to filter (empty = all events)
	EventTypes []string

	// Namespace to filter events (empty = all namespaces)
	Namespace string

	// Environment variables to pass to the script
	Environment map[string]string

	// Resources for the job
	Resources *ResourceConfig
}

// PeriodicConfig defines the periodic schedule settings
type PeriodicConfig struct {
	// Cron specifies the schedule in cron format
	Cron string

	// ProhibitOverlap prevents concurrent executions
	ProhibitOverlap bool
}

// ResourceConfig defines resource limits for the job
type ResourceConfig struct {
	CPU    int
	Memory int
}

// TplData holds data passed to job templates
type TplData struct {
	// Event contains event-specific data
	Event map[string]any

	// ShellScript is the user's shell script
	ShellScript string

	// JobName is the name for the created job
	JobName string

	// JobType is the type of job to create
	JobType string

	// Priority sets the job priority
	Priority int

	// Periodic contains cron settings if enabled
	Periodic *PeriodicConfig

	// Namespace for the job
	Namespace string

	// Environment variables
	Environment map[string]string

	// Resources configuration
	Resources *ResourceConfig
}

// New creates a new Shell provider instance
func New(ctx context.Context, logger *slog.Logger, client interfaces.NomadClient,
	cache interfaces.CacheClient, k *koanf.Koanf) (interfaces.Provider, error) {

	// Extract configuration with defaults
	name := k.String("name")
	if name == "" {
		name = "shell" // Default name
	}

	// Required: Path to the shell script file
	scriptPath := k.String("script_path")
	if scriptPath == "" {
		return nil, fmt.Errorf("script path must be specified in 'script_path' field")
	}

	// Verify the script file exists
	_, err := os.Stat(scriptPath)
	if err != nil {
		return nil, fmt.Errorf("script file error: %w", err)
	}

	// Job configuration
	jobType := k.String("job_type")
	if jobType == "" {
		jobType = "batch" // Default job type
	} else {
		jobType = strings.ToLower(jobType)
		if jobType != "batch" && jobType != "system" {
			return nil, fmt.Errorf("invalid job type: %s (must be 'batch' or 'system')", jobType)
		}
	}

	// Priority
	priority := k.Int("priority")
	if priority <= 0 {
		priority = 50 // Default priority
	} else if priority > 100 {
		priority = 100 // Max priority
	}

	// Parse periodic configuration
	var periodicConfig *PeriodicConfig
	if k.Exists("periodic") {
		cron := k.String("periodic.cron")
		if cron != "" {
			periodicConfig = &PeriodicConfig{
				Cron:            cron,
				ProhibitOverlap: k.Bool("periodic.prohibit_overlap"),
			}
		}
	}

	// Parse topics configuration
	topics := make(map[api.Topic][]string)
	topicsMap := k.StringMap("topics")

	// If no topics specified, default to all job events
	if len(topicsMap) == 0 {
		topics[api.TopicJob] = []string{"*"}
	} else {
		for topic, filters := range topicsMap {
			topicEnum := api.Topic(topic)
			filterSlice := strings.Split(filters, ",")
			for i, f := range filterSlice {
				filterSlice[i] = strings.TrimSpace(f)
			}
			topics[topicEnum] = filterSlice
		}
	}

	// Parse environment variables
	env := make(map[string]string)
	if k.Exists("environment") {
		for key, val := range k.StringMap("environment") {
			env[key] = val
		}
	}

	// Parse resource configuration
	resources := &ResourceConfig{
		CPU:    k.Int("resources.cpu"),
		Memory: k.Int("resources.memory"),
	}
	if resources.CPU <= 0 {
		resources.CPU = 100 // Default CPU (in MHz)
	}
	if resources.Memory <= 0 {
		resources.Memory = 128 // Default Memory (in MB)
	}

	// Configure provider
	cfg := &Config{
		Name:        name,
		ScriptPath:  scriptPath,
		JobType:     jobType,
		Priority:    priority,
		Periodic:    periodicConfig,
		Topics:      topics,
		EventTypes:  k.Strings("event_types"),
		Namespace:   k.String("namespace"),
		Environment: env,
		Resources:   resources,
	}

	// Load the job template from embedded FS
	jobTemplateBytes, err := jobTemplateFS.ReadFile("templates/job.hcl")
	if err != nil {
		return nil, fmt.Errorf("failed to read embedded job template: %w", err)
	}

	// Parse the template
	tmpl, err := template.New("job").Parse(string(jobTemplateBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to parse job template: %w", err)
	}

	// Create new provider
	provider := &Shell{
		logger:  logger.With("provider", name),
		client:  client,
		config:  cfg,
		ctx:     ctx,
		jobTmpl: tmpl,
	}

	logger.Info("initialized shell provider",
		"name", name,
		"script_path", cfg.ScriptPath,
		"job_type", cfg.JobType,
		"topics", cfg.Topics)

	return provider, nil
}

// Name returns the provider name
func (s *Shell) Name() string {
	return s.config.Name
}

// Topics returns the topics required by this provider
func (s *Shell) Topics() map[api.Topic][]string {
	return s.config.Topics
}

// Close performs any cleanup needed
func (s *Shell) Close() error {
	s.logger.Info("closing shell provider", "name", s.config.Name)
	return nil
}

// OnEvent processes events by creating shell script jobs
func (s *Shell) OnEvent(event *api.Event) {
	// Check if we're filtering by namespace
	if s.config.Namespace != "" && s.config.Namespace != "*" {
		// Extract namespace based on event topic
		var eventNamespace string

		switch event.Topic {
		case api.TopicJob:
			job, err := event.Job()
			if err == nil && job != nil && job.Namespace != nil {
				eventNamespace = *job.Namespace
			}
		case api.TopicAllocation:
			alloc, err := event.Allocation()
			if err == nil && alloc != nil {
				eventNamespace = alloc.Namespace
			}
		case api.TopicDeployment:
			deploy, err := event.Deployment()
			if err == nil && deploy != nil {
				eventNamespace = deploy.Namespace
			}
		case api.TopicEvaluation:
			eval, err := event.Evaluation()
			if err == nil && eval != nil {
				eventNamespace = eval.Namespace
			}
		case api.TopicService:
			service, err := event.Service()
			if err == nil && service != nil {
				eventNamespace = service.Namespace
			}
		}

		// Skip if namespace doesn't match our filter
		if eventNamespace != "" && eventNamespace != s.config.Namespace {
			return
		}
	}

	// Skip if we're filtering by event type and this isn't one we care about
	if len(s.config.EventTypes) > 0 {
		if !slices.Contains(s.config.EventTypes, event.Type) {
			return
		}
	}

	// Serialize the event data for the template
	eventData, err := s.prepareEventData(event)
	if err != nil {
		s.logger.Error("failed to prepare event data", "error", err)
		return
	}

	// Load the shell script from file
	shellScript, err := utils.ReadFile(s.config.ScriptPath)
	if err != nil {
		s.logger.Error("failed to read shell script", "path", s.config.ScriptPath, "error", err)
		return
	}

	// Generate a unique job name
	jobName := fmt.Sprintf("damon-shell-%s-%s-%d", s.config.Name, strings.ToLower(event.Type), event.Index)

	// Determine namespace for the job based on event type
	var namespace string

	// Extract namespace from event data
	switch event.Topic {
	case api.TopicJob:
		job, err := event.Job()
		if err == nil && job != nil && job.Namespace != nil {
			namespace = *job.Namespace
		}
	case api.TopicAllocation:
		alloc, err := event.Allocation()
		if err == nil && alloc != nil {
			namespace = alloc.Namespace
		}
	case api.TopicDeployment:
		deploy, err := event.Deployment()
		if err == nil && deploy != nil {
			namespace = deploy.Namespace
		}
	case api.TopicEvaluation:
		eval, err := event.Evaluation()
		if err == nil && eval != nil {
			namespace = eval.Namespace
		}
	case api.TopicService:
		service, err := event.Service()
		if err == nil && service != nil {
			namespace = service.Namespace
		}
	}

	// If we didn't get a namespace from the event, use the one from config
	if namespace == "" {
		namespace = s.config.Namespace
		// If config doesn't have a namespace or is wildcard, use default
		if namespace == "" || namespace == "*" {
			namespace = "default"
		}
	}

	// Create the template data
	tplData := TplData{
		Event:       eventData,
		ShellScript: string(shellScript),
		JobName:     jobName,
		JobType:     s.config.JobType,
		Priority:    s.config.Priority,
		Periodic:    s.config.Periodic,
		Namespace:   namespace,
		Environment: s.config.Environment,
		Resources:   s.config.Resources,
	}

	// Create and register the job
	if err := s.createJob(tplData); err != nil {
		s.logger.Error("failed to create job",
			"error", err,
			"event_type", event.Type,
			"event_index", event.Index)
		return
	}

	s.logger.Info("shell job created successfully",
		"job", jobName,
		"namespace", namespace,
		"event_type", event.Type,
		"event_index", event.Index)
}

// prepareEventData extracts and formats data from the event for use in templates
func (s *Shell) prepareEventData(event *api.Event) (map[string]any, error) {
	data := map[string]any{
		"Type":  event.Type,
		"Topic": string(event.Topic),
		"Index": event.Index,
	}

	// Based on the event topic, extract additional data including namespace
	switch event.Topic {
	case api.TopicJob:
		job, err := event.Job()
		if err != nil {
			return nil, fmt.Errorf("failed to extract job data: %w", err)
		}
		if job != nil {
			jobData := map[string]any{
				"ID":          *job.ID,
				"Name":        *job.Name,
				"Type":        *job.Type,
				"Datacenters": job.Datacenters,
			}

			// Add namespace if available
			if job.Namespace != nil {
				jobData["Namespace"] = *job.Namespace
				data["Namespace"] = *job.Namespace
			}

			// Add meta data if available
			if job.Meta != nil {
				jobData["Meta"] = job.Meta
			}

			// Add status if available
			if job.Status != nil {
				jobData["Status"] = *job.Status
			}

			data["Job"] = jobData
		}

	case api.TopicEvaluation:
		eval, err := event.Evaluation()
		if err != nil {
			return nil, fmt.Errorf("failed to extract evaluation data: %w", err)
		}
		if eval != nil {
			evalData := map[string]any{
				"ID":     eval.ID,
				"JobID":  eval.JobID,
				"Status": eval.Status,
			}

			// Add namespace if available
			if eval.Namespace != "" {
				evalData["Namespace"] = eval.Namespace
				data["Namespace"] = eval.Namespace
			}

			data["Evaluation"] = evalData
		}

	case api.TopicAllocation:
		alloc, err := event.Allocation()
		if err != nil {
			return nil, fmt.Errorf("failed to extract allocation data: %w", err)
		}
		if alloc != nil {
			allocData := map[string]any{
				"ID":           alloc.ID,
				"JobID":        alloc.JobID,
				"ClientStatus": alloc.ClientStatus,
			}

			// Add namespace if available
			if alloc.Namespace != "" {
				allocData["Namespace"] = alloc.Namespace
				data["Namespace"] = alloc.Namespace
			}

			data["Allocation"] = allocData
		}

	case api.TopicDeployment:
		deployment, err := event.Deployment()
		if err != nil {
			return nil, fmt.Errorf("failed to extract deployment data: %w", err)
		}
		if deployment != nil {
			deployData := map[string]any{
				"ID":     deployment.ID,
				"JobID":  deployment.JobID,
				"Status": deployment.Status,
			}

			// Add namespace if available
			if deployment.Namespace != "" {
				deployData["Namespace"] = deployment.Namespace
				data["Namespace"] = deployment.Namespace
			}

			data["Deployment"] = deployData
		}

	case api.TopicService:
		service, err := event.Service()
		if err != nil {
			return nil, fmt.Errorf("failed to extract service data: %w", err)
		}
		if service != nil {
			serviceData := map[string]any{
				"ID":          service.ID,
				"ServiceName": service.ServiceName,
				"Address":     service.Address,
				"Port":        service.Port,
				"Tags":        service.Tags,
			}

			// Add namespace if available
			if service.Namespace != "" {
				serviceData["Namespace"] = service.Namespace
				data["Namespace"] = service.Namespace
			}

			data["Service"] = serviceData
		}
	}

	return data, nil
}

// prepareEnvVars converts event data to environment variables for the shell script
func (s *Shell) prepareEnvVars(eventData map[string]any) map[string]string {
	env := make(map[string]string)

	// Add common event variables
	env["EVENT_TYPE"] = fmt.Sprintf("%v", eventData["Type"])
	env["EVENT_TOPIC"] = fmt.Sprintf("%v", eventData["Topic"])
	env["EVENT_INDEX"] = fmt.Sprintf("%v", eventData["Index"])

	// Add namespace if available at the top level (we put it there in prepareEventData)
	if namespace, ok := eventData["Namespace"].(string); ok && namespace != "" {
		env["EVENT_NAMESPACE"] = namespace
	}

	// Add topic-specific variables
	if job, ok := eventData["Job"].(map[string]any); ok {
		// Job variables
		for k, v := range job {
			switch k {
			case "ID":
				env["JOB_ID"] = fmt.Sprintf("%v", v)
			case "Name":
				env["JOB_NAME"] = fmt.Sprintf("%v", v)
			case "Type":
				env["JOB_TYPE"] = fmt.Sprintf("%v", v)
			case "Status":
				env["JOB_STATUS"] = fmt.Sprintf("%v", v)
			case "Namespace":
				env["JOB_NAMESPACE"] = fmt.Sprintf("%v", v)
			}
		}

		// Handle datacenters array
		if dcs, ok := job["Datacenters"].([]string); ok {
			env["JOB_DATACENTERS"] = strings.Join(dcs, ",")
		}

		// Handle meta map
		if meta, ok := job["Meta"].(map[string]string); ok {
			for k, v := range meta {
				env[fmt.Sprintf("JOB_META_%s", strings.ToUpper(k))] = v
			}
		}
	}

	if deployment, ok := eventData["Deployment"].(map[string]any); ok {
		for k, v := range deployment {
			switch k {
			case "ID":
				env["DEPLOYMENT_ID"] = fmt.Sprintf("%v", v)
			case "JobID":
				env["DEPLOYMENT_JOB_ID"] = fmt.Sprintf("%v", v)
			case "Status":
				env["DEPLOYMENT_STATUS"] = fmt.Sprintf("%v", v)
			case "Namespace":
				env["DEPLOYMENT_NAMESPACE"] = fmt.Sprintf("%v", v)
			}
		}
	}

	if eval, ok := eventData["Evaluation"].(map[string]any); ok {
		for k, v := range eval {
			switch k {
			case "ID":
				env["EVALUATION_ID"] = fmt.Sprintf("%v", v)
			case "JobID":
				env["EVALUATION_JOB_ID"] = fmt.Sprintf("%v", v)
			case "Status":
				env["EVALUATION_STATUS"] = fmt.Sprintf("%v", v)
			case "Namespace":
				env["EVALUATION_NAMESPACE"] = fmt.Sprintf("%v", v)
			}
		}
	}

	if alloc, ok := eventData["Allocation"].(map[string]any); ok {
		for k, v := range alloc {
			switch k {
			case "ID":
				env["ALLOCATION_ID"] = fmt.Sprintf("%v", v)
			case "JobID":
				env["ALLOCATION_JOB_ID"] = fmt.Sprintf("%v", v)
			case "ClientStatus":
				env["ALLOCATION_STATUS"] = fmt.Sprintf("%v", v)
			case "Namespace":
				env["ALLOCATION_NAMESPACE"] = fmt.Sprintf("%v", v)
			}
		}
	}

	if service, ok := eventData["Service"].(map[string]any); ok {
		for k, v := range service {
			switch k {
			case "ID":
				env["SERVICE_ID"] = fmt.Sprintf("%v", v)
			case "ServiceName":
				env["SERVICE_NAME"] = fmt.Sprintf("%v", v)
			case "Address":
				env["SERVICE_ADDRESS"] = fmt.Sprintf("%v", v)
			case "Port":
				env["SERVICE_PORT"] = fmt.Sprintf("%v", v)
			case "Namespace":
				env["SERVICE_NAMESPACE"] = fmt.Sprintf("%v", v)
			}
		}

		// Handle tags array
		if tags, ok := service["Tags"].([]string); ok {
			env["SERVICE_TAGS"] = strings.Join(tags, ",")
		}
	}

	return env
}

// createJob renders the job template and submits it to Nomad
func (s *Shell) createJob(data TplData) error {
	// Render the job HCL
	var buf bytes.Buffer
	if err := s.jobTmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to render job template: %w", err)
	}

	// Parse the job specification
	job, err := jobspec.Parse(bytes.NewReader(buf.Bytes()))
	if err != nil {
		return fmt.Errorf("failed to parse job spec: %w", err)
	}

	// Set the job ID and namespace
	job.ID = &data.JobName
	job.Namespace = &data.Namespace

	// Add environment variables from event data
	envVars := s.prepareEnvVars(data.Event)

	// Find the task in the job specification
	if len(job.TaskGroups) > 0 && len(job.TaskGroups[0].Tasks) > 0 {
		task := job.TaskGroups[0].Tasks[0]

		// Create env map if it doesn't exist
		if task.Env == nil {
			task.Env = make(map[string]string)
		}

		// Add event environment variables
		for k, v := range envVars {
			task.Env[k] = v
		}

		// Add user-defined environment variables
		for k, v := range data.Environment {
			task.Env[k] = v
		}
	}

	// Register the job with Nomad
	_, _, err = s.client.Jobs().Register(job, nil)
	if err != nil {
		return fmt.Errorf("failed to register job: %w", err)
	}

	return nil
}
