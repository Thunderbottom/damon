package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/hashicorp/nomad/api"
	"github.com/knadh/koanf/v2"
	"github.com/thunderbottom/damon/internal/interfaces"
	"github.com/thunderbottom/damon/provider"
)

// Register is the entry point for the plugin
func Register(registry *provider.Registry) error {
	return registry.Register("slack", New)
}

// SlackProvider sends notifications to Slack for Nomad events
type SlackProvider struct {
	logger     *slog.Logger
	name       string
	webhookURL string
	channel    string
	username   string
	topics     map[api.Topic][]string
	eventTypes []string
}

// SlackMessage represents a Slack message payload
type SlackMessage struct {
	Channel     string            `json:"channel,omitempty"`
	Username    string            `json:"username,omitempty"`
	Text        string            `json:"text,omitempty"`
	IconEmoji   string            `json:"icon_emoji,omitempty"`
	Attachments []SlackAttachment `json:"attachments,omitempty"`
}

// SlackAttachment represents a Slack message attachment
type SlackAttachment struct {
	Fallback   string       `json:"fallback,omitempty"`
	Color      string       `json:"color,omitempty"`
	Pretext    string       `json:"pretext,omitempty"`
	Title      string       `json:"title,omitempty"`
	TitleLink  string       `json:"title_link,omitempty"`
	Text       string       `json:"text,omitempty"`
	Fields     []SlackField `json:"fields,omitempty"`
	MarkdownIn []string     `json:"mrkdwn_in,omitempty"`
	Footer     string       `json:"footer,omitempty"`
	FooterIcon string       `json:"footer_icon,omitempty"`
	Timestamp  int64        `json:"ts,omitempty"`
}

// SlackField represents a field in a Slack attachment
type SlackField struct {
	Title string `json:"title,omitempty"`
	Value string `json:"value,omitempty"`
	Short bool   `json:"short,omitempty"`
}

// New creates a new Slack provider
func New(
	ctx context.Context,
	logger *slog.Logger,
	client interfaces.NomadClient,
	cache interfaces.CacheClient,
	config *koanf.Koanf,
) (interfaces.Provider, error) {
	name := config.String("name")
	if name == "" {
		name = "slack"
	}

	// Get configuration
	webhookURL := config.String("webhook_url")
	if webhookURL == "" {
		return nil, fmt.Errorf("webhook_url is required")
	}

	channel := config.String("channel")
	username := config.String("username")
	if username == "" {
		username = "Damon Nomad Events"
	}

	// Parse topics
	topics := make(map[api.Topic][]string)
	topicsMap := config.StringMap("topics")

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

	// Get event types to filter (optional)
	eventTypes := config.Strings("event_types")

	return &SlackProvider{
		logger:     logger.With("provider", name),
		name:       name,
		webhookURL: webhookURL,
		channel:    channel,
		username:   username,
		topics:     topics,
		eventTypes: eventTypes,
	}, nil
}

// Name returns the provider name
func (p *SlackProvider) Name() string {
	return p.name
}

// OnEvent processes Nomad events and sends Slack notifications
func (p *SlackProvider) OnEvent(event *api.Event) {
	// Skip if we're filtering by event type and this isn't one we care about
	if len(p.eventTypes) > 0 {
		found := false
		for _, t := range p.eventTypes {
			if t == event.Type {
				found = true
				break
			}
		}
		if !found {
			return
		}
	}

	// Create a message based on the event
	message := p.createMessage(event)
	if message == nil {
		return
	}

	// Send the message to Slack
	if err := p.sendMessage(message); err != nil {
		p.logger.Error("failed to send slack message", "error", err)
	}
}

// Topics returns the topics this provider subscribes to
func (p *SlackProvider) Topics() map[api.Topic][]string {
	return p.topics
}

// Close handles cleanup
func (p *SlackProvider) Close() error {
	p.logger.Info("closing slack provider")
	return nil
}

// createMessage builds a Slack message from a Nomad event
func (p *SlackProvider) createMessage(event *api.Event) *SlackMessage {
	var title, text string
	var color string
	var fields []SlackField

	// Set color based on event type
	switch {
	case strings.Contains(event.Type, "Registered"):
		color = "good" // green
	case strings.Contains(event.Type, "Deregistered"):
		color = "danger" // red
	default:
		color = "warning" // yellow
	}

	// Build message content based on event topic
	switch event.Topic {
	case api.TopicJob:
		job, err := event.Job()
		if err != nil || job == nil {
			p.logger.Error("failed to extract job from event", "error", err)
			return nil
		}

		title = fmt.Sprintf("Job %s", event.Type)
		text = fmt.Sprintf("Job: *%s*", *job.ID)

		fields = []SlackField{
			{Title: "Namespace", Value: *job.Namespace, Short: true},
			{Title: "Type", Value: *job.Type, Short: true},
		}

		if job.Status != nil {
			fields = append(fields, SlackField{Title: "Status", Value: *job.Status, Short: true})
		}

	case api.TopicDeployment:
		deployment, err := event.Deployment()
		if err != nil || deployment == nil {
			p.logger.Error("failed to extract deployment from event", "error", err)
			return nil
		}

		title = fmt.Sprintf("Deployment %s", event.Type)
		text = fmt.Sprintf("Deployment: *%s*", deployment.ID)

		fields = []SlackField{
			{Title: "Job", Value: deployment.JobID, Short: true},
			{Title: "Status", Value: deployment.Status, Short: true},
		}

	case api.TopicEvaluation:
		eval, err := event.Evaluation()
		if err != nil || eval == nil {
			p.logger.Error("failed to extract evaluation from event", "error", err)
			return nil
		}

		title = fmt.Sprintf("Evaluation %s", event.Type)
		text = fmt.Sprintf("Evaluation: *%s*", eval.ID)

		fields = []SlackField{
			{Title: "Job", Value: eval.JobID, Short: true},
			{Title: "Status", Value: eval.Status, Short: true},
		}

	default:
		title = fmt.Sprintf("%s %s", event.Topic, event.Type)
		text = fmt.Sprintf("Event Index: %d", event.Index)
	}

	// Create the message
	message := &SlackMessage{
		Channel:  p.channel,
		Username: p.username,
		Attachments: []SlackAttachment{
			{
				Fallback:   fmt.Sprintf("%s: %s", title, text),
				Color:      color,
				Title:      title,
				Text:       text,
				Fields:     fields,
				MarkdownIn: []string{"text", "fields"},
				Footer:     "Damon Nomad Event Operator",
				Timestamp:  time.Now().Unix(),
			},
		},
	}

	return message
}

// sendMessage posts a message to the Slack webhook
func (p *SlackProvider) sendMessage(message *SlackMessage) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal slack message: %w", err)
	}

	resp, err := http.Post(p.webhookURL, "application/json", bytes.NewBuffer(payload))
	if err != nil {
		return fmt.Errorf("failed to post to slack: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("slack returned non-200 status: %d", resp.StatusCode)
	}

	p.logger.Info("sent slack notification",
		"topic", string(message.Attachments[0].Title),
		"status", resp.Status)
	return nil
}
