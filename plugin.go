package externaldns

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/coredns/coredns/plugin"
	clog "github.com/coredns/coredns/plugin/pkg/log"
	"github.com/coredns/coredns/plugin/transfer"
	"github.com/miekg/dns"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	externaldnsv1alpha1 "sigs.k8s.io/external-dns/apis/v1alpha1"
	"sigs.k8s.io/external-dns/endpoint"

	"github.com/ionos-cloud/coredns-externaldns/internal/cache"
	dnsbuilder "github.com/ionos-cloud/coredns-externaldns/internal/dns"
	"github.com/ionos-cloud/coredns-externaldns/internal/utils"
	"github.com/ionos-cloud/coredns-externaldns/internal/watcher"
)

var pluginLog = clog.NewWithPlugin("externaldns-plugin")

// Plugin represents the ExternalDNS CoreDNS plugin
type Plugin struct {
	Next plugin.Handler

	// Configuration
	config *Config

	// Dependencies
	cache         *cache.Cache
	watcher       *watcher.DNSEndpointWatcher
	transfer      *transfer.Transfer
	recordBuilder *dnsbuilder.RecordBuilder

	// Kubernetes clients
	client     dynamic.Interface
	coreClient kubernetes.Interface

	// Zone serial management
	zoneSerials            map[string]uint32
	serialsMutex           sync.RWMutex
	configMapName          string
	configMapNamespace     string
	serialsResourceVersion string

	// Context for cancellation
	ctx    context.Context
	cancel context.CancelFunc

	// Metrics
	metrics *cache.Metrics
}

// Define metrics
var (
	requestCount = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: plugin.Namespace,
		Subsystem: "externaldns",
		Name:      "requests_total",
		Help:      "Counter of DNS requests made to external-dns plugin.",
	}, []string{"proto", "type", "zone"})

	cacheSize = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: plugin.Namespace,
		Subsystem: "externaldns",
		Name:      "cache_size",
		Help:      "Number of entries in the DNS cache.",
	}, []string{"zone"})

	endpointEvents = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: plugin.Namespace,
		Subsystem: "externaldns",
		Name:      "endpoint_events_total",
		Help:      "Counter of DNSEndpoint events processed.",
	}, []string{"type"})
)

// New creates a new ExternalDNS plugin instance
func New(config *Config) *Plugin {
	metrics := &cache.Metrics{
		CacheSize:      cacheSize,
		RequestCount:   requestCount,
		EndpointEvents: endpointEvents,
	}

	return &Plugin{
		config:             config,
		cache:              cache.New(metrics),
		metrics:            metrics,
		zoneSerials:        make(map[string]uint32),
		configMapName:      config.ConfigMap.Name,
		configMapNamespace: config.ConfigMap.Namespace,
		recordBuilder:      dnsbuilder.NewRecordBuilder(),
	}
}

// Name implements the plugin.Handler interface
func (p *Plugin) Name() string {
	return "externaldns"
}

// ServeDNS implements the plugin.Handler interface
func (p *Plugin) ServeDNS(ctx context.Context, w dns.ResponseWriter, r *dns.Msg) (int, error) {
	if len(r.Question) == 0 {
		clog.Debug("No questions in DNS request")
		return plugin.NextOrFailure(p.Name(), p.Next, ctx, w, r)
	}

	q := r.Question[0]
	qname := q.Name
	qtype := q.Qtype
	zone := getZone(qname)

	clog.Debugf("DNS request: qname=%s, qtype=%s, zone=%s", qname, dns.TypeToString[qtype], zone)

	// Update metrics
	if p.metrics.RequestCount != nil {
		p.metrics.RequestCount.WithLabelValues(proto(w), dns.TypeToString[qtype], zone).Inc()
	}

	// If transfer is not loaded, we'll see these, answer with refused (no transfer allowed).
	if qtype == dns.TypeAXFR || qtype == dns.TypeIXFR {
		clog.Debugf("Zone transfer request refused for %s (type %s)", qname, dns.TypeToString[qtype])
		return dns.RcodeRefused, nil
	}

	// Try to get records from cache
	records := p.cache.GetRecords(qname, qtype)
	clog.Debugf("Cache lookup for %s %s returned %d records", qname, dns.TypeToString[qtype], len(records))

	// If no direct records found and querying for A or AAAA, check for CNAME records
	if len(records) == 0 && (qtype == dns.TypeA || qtype == dns.TypeAAAA) {
		cnameRecords := p.cache.GetRecords(qname, dns.TypeCNAME)
		if len(cnameRecords) > 0 {
			clog.Debugf("Found %d CNAME records for %s when looking for %s", len(cnameRecords), qname, dns.TypeToString[qtype])

			// Start with the CNAME records
			records = make([]dns.RR, len(cnameRecords))
			copy(records, cnameRecords)

			// Now resolve each CNAME to get the actual A/AAAA records
			for _, cnameRR := range cnameRecords {
				if cname, ok := cnameRR.(*dns.CNAME); ok {
					targetRecords := p.resolveCNAMETarget(cname.Target, qtype, 5) // max 5 hops to prevent infinite loops
					records = append(records, targetRecords...)
				}
			}
		} else {
			clog.Debugf("No CNAME records found for %s when looking for %s", qname, dns.TypeToString[qtype])
		}
	}

	if len(records) == 0 {
		// No records found, pass to next plugin
		clog.Debugf("No records found for %s %s, passing to next plugin", qname, dns.TypeToString[qtype])
		return plugin.NextOrFailure(p.Name(), p.Next, ctx, w, r)
	}

	// Create response
	m := new(dns.Msg)
	m.SetReply(r)
	m.Authoritative = true
	m.Answer = records

	clog.Debugf("Responding to %s %s with %d records (authoritative)", qname, dns.TypeToString[qtype], len(records))

	if err := w.WriteMsg(m); err != nil {
		clog.Errorf("Failed to write DNS response for %s %s: %v", qname, dns.TypeToString[qtype], err)
		return dns.RcodeServerFailure, err
	}

	clog.Debugf("Successfully responded to %s %s", qname, dns.TypeToString[qtype])
	return dns.RcodeSuccess, nil
}

// Start initializes and starts the plugin
func (p *Plugin) Start(ctx context.Context) error {
	p.ctx, p.cancel = context.WithCancel(ctx)

	// Initialize Kubernetes clients
	if err := p.initializeClients(); err != nil {
		return fmt.Errorf("failed to initialize clients: %w", err)
	}

	// Set default ConfigMap namespace if not configured
	if p.configMapNamespace == "" {
		p.configMapNamespace = p.config.Namespace
		if p.configMapNamespace == "" {
			p.configMapNamespace = "default"
		}
	}

	// Load existing zone serials from ConfigMap
	if err := p.loadZoneSerials(ctx); err != nil {
		return fmt.Errorf("failed to load zone serials: %w", err)
	}

	// Create and start the watcher
	p.watcher = watcher.New(p.client, p.config.Namespace, p)
	if err := p.watcher.Start(ctx); err != nil {
		return fmt.Errorf("failed to start watcher: %w", err)
	}

	pluginLog.Info("ExternalDNS plugin started successfully")
	return nil
}

// Stop stops the plugin
func (p *Plugin) Stop() {
	if p.cancel != nil {
		p.cancel()
	}
	if p.watcher != nil {
		p.watcher.Stop()
	}
	pluginLog.Info("ExternalDNS plugin stopped")
}

// Transfer implements the transfer.Transfer interface for AXFR support
func (p *Plugin) Transfer(zone string, serial uint32) (<-chan []dns.RR, error) {
	pluginLog.Debugf("AXFR request for zone: %s, serial: %d", zone, serial)

	ch := make(chan []dns.RR)

	go func() {
		defer close(ch)

		records := p.cache.GetZoneRecords(zone)
		if records == nil {
			pluginLog.Debugf("No records found for zone: %s", zone)
			return
		}

		// Send SOA record first
		soaRecord := p.createSOARecord(zone, serial)
		ch <- []dns.RR{soaRecord}

		// Send all records
		for _, typeMap := range records {
			for _, rrs := range typeMap {
				if len(rrs) > 0 {
					ch <- rrs
				}
			}
		}

		// Send SOA record last
		ch <- []dns.RR{soaRecord}
	}()

	return ch, nil
}

// OnAdd handles DNSEndpoint addition events
func (p *Plugin) OnAdd(endpoint *externaldnsv1alpha1.DNSEndpoint) error {
	pluginLog.Debugf("Adding DNSEndpoint: %s/%s", endpoint.Namespace, endpoint.Name)

	serial := p.generateSerial(endpoint, watch.Added)
	createPTR := p.shouldCreatePTR(endpoint)

	zones := make(map[string]bool)
	for _, ep := range endpoint.Spec.Endpoints {
		if err := p.addEndpointRecord(ep, createPTR); err != nil {
			pluginLog.Errorf("Failed to add endpoint record: %v", err)
			continue
		}
		zone := getZone(ep.DNSName)
		pluginLog.Debugf("OnAdd: DNSName=%s -> Zone=%s", ep.DNSName, zone)
		zones[zone] = true
	}

	// Update zone serials atomically - only if the serial is actually newer
	updatedZones := make([]string, 0, len(zones))
	p.serialsMutex.Lock()
	for zone := range zones {
		if currentSerial, exists := p.zoneSerials[zone]; !exists || serial > currentSerial {
			p.zoneSerials[zone] = serial
			p.cache.SetZoneSerial(zone, serial)
			updatedZones = append(updatedZones, zone)
		}
	}
	p.serialsMutex.Unlock()

	// Save serials to ConfigMap only if there were actual updates
	if len(updatedZones) > 0 {
		p.saveZoneSerials(p.ctx, updatedZones)
	}

	if p.metrics.EndpointEvents != nil {
		p.metrics.EndpointEvents.WithLabelValues("added").Inc()
	}

	return nil
}

// OnUpdate handles DNSEndpoint update events
func (p *Plugin) OnUpdate(endpoint *externaldnsv1alpha1.DNSEndpoint) error {
	pluginLog.Debugf("Updating DNSEndpoint: %s/%s", endpoint.Namespace, endpoint.Name)

	serial := p.generateSerial(endpoint, watch.Modified)
	createPTR := p.shouldCreatePTR(endpoint)

	// For updates, we need to clean up all existing records for this endpoint
	// since the old targets might be different from the new ones
	for _, ep := range endpoint.Spec.Endpoints {
		qtype := p.recordBuilder.RecordTypeToQType(ep.RecordType)
		if qtype != 0 {
			// Remove all existing records for this name and type
			p.cache.RemoveAllRecords(ep.DNSName, qtype)

			// Also remove PTR records if they were created
			if createPTR && (qtype == dns.TypeA || qtype == dns.TypeAAAA) {
				// Remove all PTR records pointing to this DNS name
				p.cache.RemoveAllPTRRecordsForName(ep.DNSName)
			}
		}
	}

	zones := make(map[string]bool)
	for _, ep := range endpoint.Spec.Endpoints {
		if err := p.addEndpointRecord(ep, createPTR); err != nil {
			pluginLog.Errorf("Failed to add endpoint record during update: %v", err)
			continue
		}
		zone := getZone(ep.DNSName)
		pluginLog.Debugf("OnUpdate: DNSName=%s -> Zone=%s", ep.DNSName, zone)
		zones[zone] = true
	}

	// Update zone serials
	updatedZones := make([]string, 0, len(zones))
	p.serialsMutex.Lock()
	for zone := range zones {
		p.zoneSerials[zone] = serial
		p.cache.SetZoneSerial(zone, serial)
		updatedZones = append(updatedZones, zone)
	}
	p.serialsMutex.Unlock()

	// Save serials to ConfigMap
	p.saveZoneSerials(p.ctx, updatedZones)

	if p.metrics.EndpointEvents != nil {
		p.metrics.EndpointEvents.WithLabelValues("updated").Inc()
	}

	return nil
}

// OnDelete handles DNSEndpoint deletion events
func (p *Plugin) OnDelete(endpoint *externaldnsv1alpha1.DNSEndpoint) error {
	pluginLog.Debugf("Deleting DNSEndpoint: %s/%s", endpoint.Namespace, endpoint.Name)

	serial := p.generateSerial(endpoint, watch.Deleted)
	createPTR := p.shouldCreatePTR(endpoint)

	zones := make(map[string]bool)
	for _, ep := range endpoint.Spec.Endpoints {
		if err := p.removeEndpointRecord(ep, createPTR); err != nil {
			pluginLog.Errorf("Failed to remove endpoint record: %v", err)
		}
		zones[getZone(ep.DNSName)] = true
	}

	// Update zone serials even for deletions
	updatedZones := make([]string, 0, len(zones))
	p.serialsMutex.Lock()
	for zone := range zones {
		p.zoneSerials[zone] = serial
		p.cache.SetZoneSerial(zone, serial)
		updatedZones = append(updatedZones, zone)
	}
	p.serialsMutex.Unlock()

	// Save serials to ConfigMap
	p.saveZoneSerials(p.ctx, updatedZones)

	if p.metrics.EndpointEvents != nil {
		p.metrics.EndpointEvents.WithLabelValues("deleted").Inc()
	}

	return nil
}

// initializeClients initializes Kubernetes clients
func (p *Plugin) initializeClients() error {
	config, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("failed to create in-cluster config: %w", err)
	}

	p.client, err = dynamic.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create dynamic client: %w", err)
	}

	p.coreClient, err = kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create core client: %w", err)
	}

	return nil
}

// addEndpointRecord adds a single endpoint record to the cache
func (p *Plugin) addEndpointRecord(ep *endpoint.Endpoint, createPTR bool) error {
	qtype := p.recordBuilder.RecordTypeToQType(ep.RecordType)
	if qtype == 0 {
		return fmt.Errorf("unsupported record type: %s", ep.RecordType)
	}

	ttl := p.config.TTL
	if ep.RecordTTL > 0 {
		ttl = uint32(ep.RecordTTL)
	}

	for _, target := range ep.Targets {
		rr := p.recordBuilder.CreateRecord(ep.DNSName, qtype, ttl, target)
		if rr == nil {
			pluginLog.Errorf("Failed to create record for %s %s %s", ep.DNSName, ep.RecordType, target)
			continue
		}

		p.cache.AddRecord(ep.DNSName, qtype, rr)

		// Create PTR record if requested and applicable
		if createPTR && (qtype == dns.TypeA || qtype == dns.TypeAAAA) {
			if ptrRR := p.recordBuilder.CreatePTRRecord(target, ep.DNSName, ttl); ptrRR != nil {
				p.cache.AddRecord(ptrRR.Header().Name, dns.TypePTR, ptrRR)
			}
		}
	}

	return nil
}

// removeEndpointRecord removes a single endpoint record from the cache
func (p *Plugin) removeEndpointRecord(ep *endpoint.Endpoint, createPTR bool) error {
	qtype := p.recordBuilder.RecordTypeToQType(ep.RecordType)
	if qtype == 0 {
		return fmt.Errorf("unsupported record type: %s", ep.RecordType)
	}

	for _, target := range ep.Targets {
		p.cache.RemoveRecord(ep.DNSName, qtype, target)

		// Remove PTR record if it was created
		if createPTR && (qtype == dns.TypeA || qtype == dns.TypeAAAA) {
			if ptrName, err := dns.ReverseAddr(target); err == nil {
				p.cache.RemoveRecord(ptrName, dns.TypePTR, ep.DNSName)
			}
		}
	}

	return nil
}

// shouldCreatePTR checks if PTR records should be created
func (p *Plugin) shouldCreatePTR(endpoint *externaldnsv1alpha1.DNSEndpoint) bool {
	annotations := endpoint.GetAnnotations()
	if annotations == nil {
		return false
	}

	val, exists := annotations["coredns-externaldns.ionos.cloud/create-ptr"]
	return exists && (val == "true" || val == "1")
}

// generateSerial generates a serial number for the DNSEndpoint
// Uses a resource-based approach that's idempotent and doesn't depend on event types
func (p *Plugin) generateSerial(endpoint *externaldnsv1alpha1.DNSEndpoint, eventType watch.EventType) uint32 {
	// Determine which zones will be affected
	zones := make(map[string]bool)
	for _, ep := range endpoint.Spec.Endpoints {
		zones[getZone(ep.DNSName)] = true
	}

	p.serialsMutex.RLock()
	defer p.serialsMutex.RUnlock()

	var maxSerial uint32
	for zone := range zones {
		if currentSerial, exists := p.zoneSerials[zone]; exists {
			if currentSerial > maxSerial {
				maxSerial = currentSerial
			}
		}
	}

	switch eventType {
	case watch.Added:
		// For ADD events, always use resource-based serial for idempotency
		// This ensures same resource always gets same serial regardless of watcher restarts
		return p.calculateResourceSerial(endpoint)

	case watch.Modified, watch.Deleted:
		// Always increment for actual changes to ensure forward movement
		if maxSerial == 0 {
			return uint32(time.Now().Unix())
		}
		return maxSerial + 1
	default:
		return uint32(time.Now().Unix())
	}
}

// calculateResourceSerial calculates a deterministic serial based on resource metadata
// This ensures the same resource always gets the same serial regardless of event replay
func (p *Plugin) calculateResourceSerial(endpoint *externaldnsv1alpha1.DNSEndpoint) uint32 {
	t := endpoint.GetCreationTimestamp()
	if t.IsZero() {
		return uint32(time.Now().Unix())
	}

	serial := uint32(t.Unix())

	// Add generation to handle updates to the same resource
	if endpoint.GetGeneration() > 0 {
		serial += uint32(endpoint.GetGeneration())
	}

	return serial
}

// createSOARecord creates a SOA record for a zone
func (p *Plugin) createSOARecord(zone string, serial uint32) dns.RR {
	return &dns.SOA{
		Hdr: dns.RR_Header{
			Name:   zone,
			Rrtype: dns.TypeSOA,
			Class:  dns.ClassINET,
			Ttl:    p.config.TTL,
		},
		Ns:      dns.Fqdn(p.config.SOA.Nameserver),
		Mbox:    dns.Fqdn(p.config.SOA.Mailbox),
		Serial:  serial,
		Refresh: 3600,
		Retry:   1800,
		Expire:  1209600,
		Minttl:  300,
	}
}

// loadZoneSerials loads zone serials from ConfigMap
func (p *Plugin) loadZoneSerials(ctx context.Context) error {
	if p.coreClient == nil {
		return fmt.Errorf("core client not initialized")
	}

	cm, err := p.coreClient.CoreV1().ConfigMaps(p.configMapNamespace).Get(ctx, p.configMapName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Create empty ConfigMap
			cm = &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      p.configMapName,
					Namespace: p.configMapNamespace,
				},
				Data: map[string]string{},
			}
			cm, err = p.coreClient.CoreV1().ConfigMaps(p.configMapNamespace).Create(ctx, cm, metav1.CreateOptions{})
			if err != nil {
				if apierrors.IsAlreadyExists(err) {
					// Try to get it again
					cm, err = p.coreClient.CoreV1().ConfigMaps(p.configMapNamespace).Get(ctx, p.configMapName, metav1.GetOptions{})
					if err != nil {
						return fmt.Errorf("failed to get ConfigMap after creation conflict: %w", err)
					}
				} else {
					return fmt.Errorf("failed to create ConfigMap: %w", err)
				}
			}
		} else {
			return fmt.Errorf("failed to get ConfigMap: %w", err)
		}
	}

	p.serialsMutex.Lock()
	p.zoneSerials = make(map[string]uint32)
	for zone, serialStr := range cm.Data {
		if serial, err := strconv.ParseUint(serialStr, 10, 32); err == nil {
			p.zoneSerials[zone] = uint32(serial)
			// Update cache with loaded serial
			p.cache.SetZoneSerial(zone, uint32(serial))
		} else {
			pluginLog.Warningf("Invalid serial value for zone %s: %s", zone, serialStr)
		}
	}
	p.serialsResourceVersion = cm.ResourceVersion
	p.serialsMutex.Unlock()

	pluginLog.Infof("Loaded %d zone serials from ConfigMap", len(p.zoneSerials))
	return nil
}

// saveZoneSerials saves zone serials to ConfigMap
func (p *Plugin) saveZoneSerials(ctx context.Context, updatedZones []string) {
	if p.coreClient == nil {
		return
	}

	p.serialsMutex.RLock()
	data := make(map[string]string)
	for zone, serial := range p.zoneSerials {
		data[zone] = strconv.FormatUint(uint64(serial), 10)
	}
	rv := p.serialsResourceVersion
	p.serialsMutex.RUnlock()

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:            p.configMapName,
			Namespace:       p.configMapNamespace,
			ResourceVersion: rv,
		},
		Data: data,
	}

	_, err := p.coreClient.CoreV1().ConfigMaps(p.configMapNamespace).Update(ctx, cm, metav1.UpdateOptions{})
	if err != nil {
		if apierrors.IsConflict(err) {
			pluginLog.Warningf("ConfigMap update conflict, reloading serials")
			if reloadErr := p.loadZoneSerials(ctx); reloadErr != nil {
				pluginLog.Errorf("Failed to reload serials after conflict: %v", reloadErr)
			}
		} else {
			pluginLog.Errorf("Failed to update ConfigMap: %v", err)
		}
		return
	}

	// Notify others of zone changes
	if p.transfer != nil {
		for _, zone := range updatedZones {
			pluginLog.Debugf("Notifying transfer plugin for zone: %s", zone)
			if err := p.transfer.Notify(zone); err != nil {
				pluginLog.Warningf("Failed to notify transfer of serial update for zone %s: %v", zone, err)
			}
		}
	}

	pluginLog.Debugf("Updated ConfigMap with %d zone serials", len(data))
}

// resolveCNAMETarget resolves a CNAME target to get the actual A/AAAA records
func (p *Plugin) resolveCNAMETarget(target string, qtype uint16, maxHops int) []dns.RR {
	if maxHops <= 0 {
		clog.Debugf("Maximum CNAME hops reached for %s", target)
		return nil
	}

	// First, try to get direct A/AAAA records for the target
	targetRecords := p.cache.GetRecords(target, qtype)
	if len(targetRecords) > 0 {
		clog.Debugf("Found %d %s records for CNAME target %s", len(targetRecords), dns.TypeToString[qtype], target)
		return targetRecords
	}

	// If no direct records, check if the target is also a CNAME
	cnameRecords := p.cache.GetRecords(target, dns.TypeCNAME)
	if len(cnameRecords) > 0 {
		clog.Debugf("CNAME target %s is also a CNAME, following chain", target)
		var allRecords []dns.RR

		// Add the intermediate CNAME records to the response
		allRecords = append(allRecords, cnameRecords...)

		// Recursively resolve each CNAME
		for _, cnameRR := range cnameRecords {
			if cname, ok := cnameRR.(*dns.CNAME); ok {
				recursiveRecords := p.resolveCNAMETarget(cname.Target, qtype, maxHops-1)
				allRecords = append(allRecords, recursiveRecords...)
			}
		}
		return allRecords
	}

	clog.Debugf("No records found for CNAME target %s", target)
	return nil
}

// Helper functions

func proto(w dns.ResponseWriter) string {
	if _, ok := w.RemoteAddr().(*net.UDPAddr); ok {
		return "udp"
	}
	return "tcp"
}

func getZone(name string) string {
	return utils.ExtractZoneFromDomain(name)
}
