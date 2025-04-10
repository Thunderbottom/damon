package stream

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/nomad/api"
	"github.com/thunderbottom/damon/internal/interfaces"
)

// Stream represents a Nomad event stream
type Stream struct {
	namespace string
	provider  interfaces.Provider
	client    interfaces.NomadClient
	logger    *slog.Logger
	eventIdx  uint64
	mu        sync.RWMutex
}

// Manager is responsible for managing multiple streams
type Manager struct {
	streams []*Stream
	logger  *slog.Logger
}

// NewManager creates a new stream manager
func NewManager(logger *slog.Logger) interfaces.StreamManager {
	return &Manager{
		streams: []*Stream{},
		logger:  logger,
	}
}

// AddStream adds a new stream to the manager
func (m *Manager) AddStream(p interfaces.Provider, ns string, idx uint64, cl interfaces.NomadClient) error {
	stream := &Stream{
		namespace: ns,
		client:    cl,
		logger:    m.logger.With("provider", p.Name()),
		provider:  p,
		eventIdx:  idx,
	}
	m.streams = append(m.streams, stream)
	return nil
}

// Consume starts all streams in the manager
func (m *Manager) Consume(ctx context.Context) error {
	if len(m.streams) == 0 {
		return fmt.Errorf("no streams configured")
	}

	var wg sync.WaitGroup
	errChan := make(chan error, len(m.streams))

	for _, s := range m.streams {
		wg.Add(1)
		go func(s *Stream) {
			defer wg.Done()
			if err := s.Consume(ctx); err != nil && err != context.Canceled {
				errChan <- fmt.Errorf("stream for provider %s failed: %w", s.provider.Name(), err)
			}
		}(s)
	}

	// Wait for all streams to complete or for context cancellation
	go func() {
		wg.Wait()
		close(errChan)
	}()

	for {
		select {
		case err, ok := <-errChan:
			if !ok {
				// Channel closed, all streams finished without error
				return nil
			}
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// GetProviderIndices returns a map of provider names to their event indices
func (m *Manager) GetProviderIndices() map[string]uint64 {
	indices := make(map[string]uint64)
	for _, s := range m.streams {
		s.mu.RLock()
		indices[s.provider.Name()] = s.eventIdx
		s.mu.RUnlock()
	}
	return indices
}

// Consume starts the event stream and processes events
func (s *Stream) Consume(ctx context.Context) error {
	s.logger.Info("starting provider")

	// Fetch last event meta index if we're starting from zero
	if s.eventIdx == 0 {
		s.logger.Info("event index is 0, fetching latest event index")
		_, meta, err := s.client.Jobs().List(&api.QueryOptions{Namespace: s.namespace})
		if err != nil {
			return fmt.Errorf("failed to fetch job meta for provider %s: %w", s.provider.Name(), err)
		}
		s.eventIdx = meta.LastIndex
	}

	// Get provider-specific topics
	topics := s.provider.Topics()
	s.logger.Debug("starting event stream", "index", s.eventIdx, "namespace", s.namespace)

	// Initialize stream with backoff retry
	stream, err := s.setupStreamWithRetry(ctx, topics)
	if err != nil {
		return fmt.Errorf("stream setup failed for provider %s: %w", s.provider.Name(), err)
	}

	// Process events
	return s.processEvents(ctx, stream)
}

// setupStreamWithRetry initializes the event stream with exponential backoff
func (s *Stream) setupStreamWithRetry(ctx context.Context, topics map[api.Topic][]string) (<-chan *api.Events, error) {
	const maxRetries = 5
	var stream <-chan *api.Events
	var err error

	for retries := range maxRetries {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		es := s.client.EventStream()
		stream, err = es.Stream(ctx, topics, s.eventIdx, &api.QueryOptions{Namespace: s.namespace})
		if err == nil {
			return stream, nil
		}

		s.logger.Error("failed to set up event stream", "error", err, "retry", retries+1)
		if retries < maxRetries-1 {
			// Exponential backoff (1s, 2s, 4s, 8s)
			delay := time.Duration(1<<retries) * time.Second
			time.Sleep(delay)
		}
	}

	return nil, fmt.Errorf("failed to set up event stream after %d retries: %w", maxRetries, err)
}

// processEvents handles incoming events from the stream
func (s *Stream) processEvents(ctx context.Context, stream <-chan *api.Events) error {
	consecutiveErrorCount := 0
	const maxConsecutiveErrors = 5
	backoffDuration := time.Second

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("stopping event stream")
			return ctx.Err()

		case event, ok := <-stream:
			if !ok {
				return fmt.Errorf("event stream closed unexpectedly for provider %s", s.provider.Name())
			}

			if event.Err != nil {
				// Categorize and handle stream errors
				if isRecoverableError(event.Err) {
					consecutiveErrorCount++
					s.logger.Warn("recoverable event stream error",
						"error", event.Err,
						"count", consecutiveErrorCount)

					// Apply exponential backoff if errors persist
					if consecutiveErrorCount > 1 {
						delay := time.Duration(math.Min(
							float64(time.Minute),
							float64(backoffDuration*time.Duration(1<<(consecutiveErrorCount-1))),
						))
						s.logger.Info("backing off before retry", "delay", delay)
						time.Sleep(delay)
					}

					// If too many consecutive errors, attempt to restart the stream
					if consecutiveErrorCount >= maxConsecutiveErrors {
						s.logger.Error("too many consecutive errors, attempting to restart stream")

						// Attempt to set up a new stream
						newTopics := s.provider.Topics()
						newStream, err := s.setupStreamWithRetry(ctx, newTopics)
						if err != nil {
							return fmt.Errorf("failed to restart stream after multiple errors: %w", err)
						}
						stream = newStream
						consecutiveErrorCount = 0
					}

					continue
				}

				// Non-recoverable error
				return fmt.Errorf("fatal event stream error for provider %s: %w",
					s.provider.Name(), event.Err)
			}

			// Reset consecutive error count on successful event
			consecutiveErrorCount = 0

			// Ignore heartbeat events and empty events
			if event.IsHeartbeat() || len(event.Events) == 0 {
				continue
			}

			// Process each event
			for _, e := range event.Events {
				s.logger.Debug("received event",
					"type", e.Type,
					"topic", e.Topic,
					"index", e.Index)

				// Handle the event via provider safely
				func() {
					defer func() {
						if r := recover(); r != nil {
							s.logger.Error("provider panicked during event handling",
								"panic", r)
						}
					}()
					s.provider.OnEvent(&e)
				}()
			}

			// Update the last index
			s.mu.Lock()
			s.eventIdx = event.Events[len(event.Events)-1].Index
			s.mu.Unlock()
		}
	}
}

// isRecoverableError categorizes errors into recoverable and fatal types
func isRecoverableError(err error) bool {
	// Categorize common recoverable errors
	if err == nil {
		return true
	}

	errStr := err.Error()

	// Network-related temporary errors are recoverable
	if strings.Contains(errStr, "connection refused") ||
		strings.Contains(errStr, "deadline exceeded") ||
		strings.Contains(errStr, "temporary network error") ||
		strings.Contains(errStr, "timeout") ||
		strings.Contains(errStr, "EOF") {
		return true
	}

	// Handle specific Nomad API errors that are considered recoverable
	if strings.Contains(errStr, "429") || // Rate limiting
		strings.Contains(errStr, "500") || // Server error
		strings.Contains(errStr, "503") { // Service unavailable
		return true
	}

	// Consider context cancellation as recoverable for graceful shutdown
	if errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	// Default to treating unknown errors as non-recoverable
	return false
}

// GetIndex returns the provider name and the last event index
func (s *Stream) GetIndex() (string, uint64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.provider.Name(), s.eventIdx
}
