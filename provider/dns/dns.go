package dns

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/nomad/api"
	"github.com/knadh/koanf/v2"
	"github.com/miekg/dns"
	"github.com/thunderbottom/damon/internal/interfaces"
	"github.com/thunderbottom/damon/provider"
	"github.com/valkey-io/valkey-go"
)

const (
	// DNS server defaults
	defaultTTL      = 30
	defaultPort     = 53
	cacheKeyPrefix  = "dns:service:"
	indexKey        = "dns:index"
	maxConcurrency  = 10
	refreshInterval = 5 * time.Minute
)

func init() {
	// Register the DNS provider
	err := provider.RegisterProvider("dns", New)
	if err != nil {
		slog.Error("failed to register dns provider", "error", err)
	}
}

// ServiceRecord represents a DNS service record
type ServiceRecord struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Address    string `json:"address"`
	Port       int    `json:"port"`
	Namespace  string `json:"namespace"`
	Datacenter string `json:"datacenter"`
	Updated    int64  `json:"updated"` // Unix timestamp
}

// DNS represents the DNS provider
type DNS struct {
	logger       *slog.Logger
	client       interfaces.NomadClient
	cache        interfaces.CacheClient
	config       *Config
	ctx          context.Context
	cancel       context.CancelFunc
	server       *dns.Server
	refreshTimer *time.Timer
	mu           sync.RWMutex
}

// Config holds DNS provider configuration
type Config struct {
	ListenAddr string
	TTL        int
	Tags       []string
	Namespace  string
}

// New creates a new DNS provider
func New(
	ctx context.Context,
	logger *slog.Logger,
	client interfaces.NomadClient,
	cache interfaces.CacheClient,
	config *koanf.Koanf,
) (interfaces.Provider, error) {
	// Create provider context that can be cancelled when Close is called
	providerCtx, cancel := context.WithCancel(ctx)

	// Parse configuration
	listenAddr := config.String("listen_addr")
	if listenAddr == "" {
		listenAddr = ":5353" // Default to port 5353 if not specified
	}

	ttl := config.Int("ttl")
	if ttl <= 0 {
		ttl = defaultTTL
	}

	namespace := config.String("namespace")
	if namespace == "" {
		namespace = "*" // Listen for all namespaces by default
	}

	tags := config.Strings("tags")

	dnsProvider := &DNS{
		logger: logger.With("provider", "dns"),
		client: client,
		cache:  cache,
		config: &Config{
			ListenAddr: listenAddr,
			TTL:        ttl,
			Tags:       tags,
			Namespace:  namespace,
		},
		ctx:    providerCtx,
		cancel: cancel,
	}

	// Start the DNS server in a goroutine
	if err := dnsProvider.startServer(); err != nil {
		cancel() // Clean up the context in case of failure
		return nil, fmt.Errorf("failed to start DNS server: %w", err)
	}

	// Start a periodic refresh of the service catalog
	dnsProvider.startRefreshTimer()

	// Initialize by syncing all services
	go dnsProvider.syncServices()

	return dnsProvider, nil
}

// Name returns the name of the provider.
func (d *DNS) Name() string {
	return "dns"
}

// Topics returns the topics required by this
// event stream provider.
func (d *DNS) Topics() map[api.Topic][]string {
	// Subscribe to all service events and deployment events
	return map[api.Topic][]string{
		api.TopicService:    {"*"},
		api.TopicDeployment: {"*"},
	}
}

// OnEvent runs whenever an event is triggered
// in the event stream.
func (d *DNS) OnEvent(event *api.Event) {
	// Handle based on the event topic
	switch event.Topic {
	case api.TopicService:
		d.handleServiceEvent(event)
	case api.TopicDeployment:
		// Deployment events could contain service updates
		d.handleDeploymentEvent(event)
	}
}

// handleServiceEvent processes service registration and deregistration events
func (d *DNS) handleServiceEvent(event *api.Event) {
	srv, err := event.Service()
	if err != nil {
		d.logger.Error("error occurred while fetching service from event", "error", err)
		return
	}
	if srv == nil {
		d.logger.Debug("service not found, skipping")
		return
	}

	// Skip if service doesn't have required tags (if configured)
	if len(d.config.Tags) > 0 && !hasRequiredTags(srv.Tags, d.config.Tags) {
		d.logger.Debug("service missing required tags, attempting to deregister",
			"service", srv.ServiceName,
			"tags", srv.Tags)
		d.deregisterService(srv)
		return
	}

	switch event.Type {
	case "ServiceRegistration":
		d.registerService(srv)
	case "ServiceDeregistration":
		d.deregisterService(srv)
	}
}

// registerService adds or updates a service in the DNS registry
func (d *DNS) registerService(srv *api.ServiceRegistration) {
	record := &ServiceRecord{
		ID:         srv.ID,
		Name:       srv.ServiceName,
		Address:    srv.Address,
		Port:       srv.Port,
		Namespace:  srv.Namespace,
		Datacenter: srv.Datacenter,
		Updated:    time.Now().Unix(),
	}

	// Store in cache
	if err := d.storeServiceRecord(record); err != nil {
		d.logger.Error("failed to store service record",
			"service", srv.ServiceName,
			"error", err)
		return
	}

	d.logger.Info("registered service",
		"service", srv.ServiceName,
		"namespace", srv.Namespace,
		"address", srv.Address,
		"port", srv.Port)
}

// deregisterService removes a service from the DNS registry
func (d *DNS) deregisterService(srv *api.ServiceRegistration) {
	// Remove from cache
	key := d.serviceCacheKey(srv.ServiceName)

	rawClient := d.cache.RawClient()
	cmd := rawClient.B().Del().Key(key).Build()

	if err := rawClient.Do(d.ctx, cmd).Error(); err != nil {
		d.logger.Error("failed to remove service from cache",
			"service", srv.ServiceName,
			"error", err)
		return
	}

	d.logger.Info("deregistered service",
		"service", srv.ServiceName,
		"namespace", srv.Namespace)
}

// storeServiceRecord saves a service record to the cache
func (d *DNS) storeServiceRecord(record *ServiceRecord) error {
	// Serialize the record
	recordJSON, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("failed to marshal service record: %w", err)
	}

	// Store in cache with service name as key
	key := d.serviceCacheKey(record.Name)

	rawClient := d.cache.RawClient()

	// Use a basic string command instead of the builder pattern
	cmd := rawClient.B().
		Hset().
		Key(key).
		FieldValue().
		FieldValue(record.ID, string(recordJSON)).
		Build()

	if err := rawClient.Do(d.ctx, cmd).Error(); err != nil {
		return fmt.Errorf("failed to store service record: %w", err)
	}

	return nil
}

// startServer starts the DNS server to respond to queries
func (d *DNS) startServer() error {
	// Set up the DNS server
	dns.HandleFunc(".", d.handleDNSRequest)

	server := &dns.Server{
		Addr:    d.config.ListenAddr,
		Net:     "udp",
		Handler: dns.DefaultServeMux,
	}

	d.server = server

	// Start the server in a goroutine with context awareness
	go func() {
		d.logger.Info("starting DNS server", "address", d.config.ListenAddr)
		serverErrCh := make(chan error, 1)

		go func() {
			if err := server.ListenAndServe(); err != nil {
				d.logger.Error("DNS server error", "error", err)
				serverErrCh <- err
			}
		}()

		select {
		case <-d.ctx.Done():
			d.logger.Info("shutting down DNS server due to context cancellation")
			server.Shutdown()
		case err := <-serverErrCh:
			d.logger.Error("DNS server stopped unexpectedly", "error", err)
		}
	}()

	// Wait a moment to ensure server starts
	time.Sleep(100 * time.Millisecond)
	return nil
}

// handleDNSRequest processes incoming DNS requests
func (d *DNS) handleDNSRequest(w dns.ResponseWriter, r *dns.Msg) {
	m := new(dns.Msg)
	m.SetReply(r)
	m.Authoritative = true

	// Cache common query responses for the duration of this request
	// to avoid duplicate lookups for the same service
	serviceCache := make(map[string][]*ServiceRecord)

	// Process each question
	for _, q := range r.Question {
		d.logger.Debug("received DNS query",
			"name", q.Name,
			"type", dns.TypeToString[q.Qtype])

		switch q.Qtype {
		case dns.TypeA:
			d.handleAQuery(q, m, serviceCache)
		case dns.TypeSRV:
			d.handleSRVQuery(q, m, serviceCache)
		case dns.TypeNS:
			// Return NS records for the server itself
			d.handleNSQuery(q, m)
		}
	}

	w.WriteMsg(m)
}

// handleAQuery processes A record queries (IP addresses)
func (d *DNS) handleAQuery(q dns.Question, m *dns.Msg, serviceCache map[string][]*ServiceRecord) {
	serviceName := strings.TrimSuffix(q.Name, ".")

	// Check cache first
	records, ok := serviceCache[serviceName]
	if !ok {
		var err error
		records, err = d.lookupServicesByName(serviceName)
		if err != nil || len(records) == 0 {
			return // No records found
		}
		// Cache the result
		serviceCache[serviceName] = records
	}

	// Add A records for all matching services
	for _, record := range records {
		if record.Address != "" {
			rr := &dns.A{
				Hdr: dns.RR_Header{
					Name:   q.Name,
					Rrtype: dns.TypeA,
					Class:  dns.ClassINET,
					Ttl:    uint32(d.config.TTL),
				},
				A: net.ParseIP(record.Address),
			}
			m.Answer = append(m.Answer, rr)
		}
	}
}

// handleSRVQuery processes SRV record queries (service discovery)
func (d *DNS) handleSRVQuery(q dns.Question, m *dns.Msg, serviceCache map[string][]*ServiceRecord) {
	// SRV query format: _service._proto.name.
	parts := strings.Split(q.Name, ".")
	if len(parts) < 3 {
		return
	}

	// Extract service name from the query
	serviceName := strings.TrimPrefix(parts[0], "_")

	// Check cache first
	records, ok := serviceCache[serviceName]
	if !ok {
		var err error
		records, err = d.lookupServicesByName(serviceName)
		if err != nil || len(records) == 0 {
			return // No records found
		}
		// Cache the result
		serviceCache[serviceName] = records
	}

	// Prepare map for deduplication of A records
	aRecords := make(map[string]net.IP)

	// Add SRV records
	for _, record := range records {
		target := fmt.Sprintf("%s.%s.svc.%s.",
			record.Name,
			record.Namespace,
			record.Datacenter)

		rr := &dns.SRV{
			Hdr: dns.RR_Header{
				Name:   q.Name,
				Rrtype: dns.TypeSRV,
				Class:  dns.ClassINET,
				Ttl:    uint32(d.config.TTL),
			},
			Priority: 10,
			Weight:   10,
			Port:     uint16(record.Port),
			Target:   target,
		}
		m.Answer = append(m.Answer, rr)

		// Store IP for A record (deduplicated)
		if record.Address != "" {
			aRecords[target] = net.ParseIP(record.Address)
		}
	}

	// Add corresponding A records (deduplicated)
	for target, ip := range aRecords {
		a := &dns.A{
			Hdr: dns.RR_Header{
				Name:   target,
				Rrtype: dns.TypeA,
				Class:  dns.ClassINET,
				Ttl:    uint32(d.config.TTL),
			},
			A: ip,
		}
		m.Extra = append(m.Extra, a)
	}
}

// handleNSQuery returns nameserver information
func (d *DNS) handleNSQuery(q dns.Question, m *dns.Msg) {
	ns := &dns.NS{
		Hdr: dns.RR_Header{
			Name:   q.Name,
			Rrtype: dns.TypeNS,
			Class:  dns.ClassINET,
			Ttl:    uint32(d.config.TTL),
		},
		Ns: "ns1." + q.Name,
	}
	m.Answer = append(m.Answer, ns)
}

// lookupServicesByName retrieves all service records with the given name
func (d *DNS) lookupServicesByName(name string) ([]*ServiceRecord, error) {
	key := d.serviceCacheKey(name)

	rawClient := d.cache.RawClient()
	cmd := rawClient.B().Hgetall().Key(key).Build()
	result := rawClient.Do(d.ctx, cmd)

	if err := result.Error(); err != nil {
		if err == valkey.Nil {
			return nil, nil // Key doesn't exist, no services
		}
		return nil, fmt.Errorf("failed to lookup service: %w", err)
	}

	// Parse results
	values, err := result.AsStrMap()
	if err != nil {
		return nil, fmt.Errorf("failed to parse service records: %w", err)
	}

	if len(values) == 0 {
		return nil, nil // Service exists but has no records
	}

	var records []*ServiceRecord
	for _, data := range values {
		var record ServiceRecord
		if err := json.Unmarshal([]byte(data), &record); err != nil {
			d.logger.Error("failed to unmarshal service record",
				"data", data,
				"error", err)
			continue
		}
		records = append(records, &record)
	}

	return records, nil
}

// syncServices performs a full synchronization with Nomad services
func (d *DNS) syncServices() {
	d.logger.Info("starting full service sync")

	// Get all services from Nomad
	services, err := d.getServices()
	if err != nil {
		d.logger.Error("failed to get services from Nomad", "error", err)
		return
	}

	// Filter services that match our tag requirements
	var filteredServices []*api.ServiceRegistration
	for _, srv := range services {
		if len(d.config.Tags) == 0 || hasRequiredTags(srv.Tags, d.config.Tags) {
			filteredServices = append(filteredServices, srv)
		}
	}

	// Process services in batches
	batchSize := 50
	var records []*ServiceRecord

	for i := 0; i < len(filteredServices); i += batchSize {
		// Use math.Min to calculate the end of the batch
		end := int(math.Min(float64(i+batchSize), float64(len(filteredServices))))
		batch := filteredServices[i:end]
		batchRecords := make([]*ServiceRecord, 0, len(batch))

		for _, srv := range batch {
			record := &ServiceRecord{
				ID:         srv.ID,
				Name:       srv.ServiceName,
				Address:    srv.Address,
				Port:       srv.Port,
				Namespace:  srv.Namespace,
				Datacenter: srv.Datacenter,
				Updated:    time.Now().Unix(),
			}

			batchRecords = append(batchRecords, record)
		}

		if err := d.batchStoreServiceRecords(batchRecords); err != nil {
			d.logger.Error("failed to store batch of service records", "error", err, "batch_start", i)
		}

		// Append to overall records for logging
		records = append(records, batchRecords...)
	}

	d.logger.Info("service sync completed", "count", len(records))
}

// getServices retrieves all services from Nomad
func (d *DNS) getServices() ([]*api.ServiceRegistration, error) {
	q := &api.QueryOptions{
		Namespace: d.config.Namespace,
	}

	// First get the list of service names
	serviceStubs, _, err := d.client.Services().List(q)
	if err != nil {
		return nil, fmt.Errorf("failed to list services: %w", err)
	}

	// We need to convert stubs to full registrations by calling Get for each service
	var services []*api.ServiceRegistration

	// According to the API docs, the List() endpoint returns namespaces with services,
	// and each service has a ServiceName field
	for _, nsServices := range serviceStubs {
		for _, svc := range nsServices.Services {
			// Get the service details using the ServiceName
			serviceRegs, _, err := d.client.Services().Get(svc.ServiceName, q)
			if err != nil {
				d.logger.Error("failed to get service details",
					"service", svc.ServiceName,
					"error", err)
				continue
			}

			// Add all returned service registrations to our slice
			services = append(services, serviceRegs...)
		}
	}

	return services, nil
}

// startRefreshTimer starts a timer to periodically refresh the service catalog
func (d *DNS) startRefreshTimer() {
	d.refreshTimer = time.NewTimer(refreshInterval)

	go func() {
		for {
			select {
			case <-d.refreshTimer.C:
				d.syncServices()
				d.refreshTimer.Reset(refreshInterval)
			case <-d.ctx.Done():
				return
			}
		}
	}()
}

// handleDeploymentEvent processes deployment events to catch service changes
func (d *DNS) handleDeploymentEvent(event *api.Event) {
	// For deployment events, we'll do a targeted sync of affected services
	// to ensure we catch any services that might have been updated
	if event.Type == "DeploymentStatusUpdate" {
		// Schedule a sync after a short delay to allow services to stabilize
		time.AfterFunc(5*time.Second, d.syncServices)
	}
}

// Close handles cleanup when shutting down
func (d *DNS) Close() error {
	d.logger.Info("closing DNS provider")

	// Cancel the provider context
	d.cancel()

	// Stop the refresh timer
	if d.refreshTimer != nil {
		d.refreshTimer.Stop()
	}

	// Shutdown the DNS server
	if d.server != nil {
		return d.server.Shutdown()
	}

	return nil
}

func (d *DNS) batchStoreServiceRecords(records []*ServiceRecord) error {
	if len(records) == 0 {
		return nil
	}

	rawClient := d.cache.RawClient()

	// Group records by service name to avoid multiple operations on the same key
	recordsByName := make(map[string][]*ServiceRecord)
	for _, record := range records {
		recordsByName[record.Name] = append(recordsByName[record.Name], record)
	}

	// Process each service in batches
	for serviceName, serviceRecords := range recordsByName {
		key := d.serviceCacheKey(serviceName)

		// Build a multi-field HSET command
		hsetBuilder := rawClient.B().Hset().Key(key).FieldValue()

		for _, record := range serviceRecords {
			// Serialize the record
			recordJSON, err := json.Marshal(record)
			if err != nil {
				d.logger.Error("failed to marshal service record",
					"service", record.Name,
					"error", err)
				continue
			}

			hsetBuilder = hsetBuilder.FieldValue(record.ID, string(recordJSON))
		}

		// Execute the batch command
		if err := rawClient.Do(d.ctx, hsetBuilder.Build()).Error(); err != nil {
			return fmt.Errorf("failed to batch store service records for %s: %w", serviceName, err)
		}
	}

	return nil
}

// hasRequiredTags checks if a service has all required tags
func hasRequiredTags(serviceTags, requiredTags []string) bool {
	// If no tags are required, all services pass
	if len(requiredTags) == 0 {
		return true
	}

	// Fast path: if the service has fewer tags than required, it can't match
	if len(serviceTags) < len(requiredTags) {
		return false
	}

	// Build tag set once
	tagSet := make(map[string]struct{}, len(serviceTags))
	for _, tag := range serviceTags {
		tagSet[tag] = struct{}{}
	}

	// Check all required tags in one pass
	for _, required := range requiredTags {
		if _, ok := tagSet[required]; !ok {
			return false
		}
	}

	return true
}

func (d *DNS) serviceCacheKey(serviceName string) string {
	return cacheKeyPrefix + serviceName
}
