package externaldns

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coredns/coredns/plugin"
	clog "github.com/coredns/coredns/plugin/pkg/log"
	"github.com/coredns/coredns/plugin/transfer"
	"github.com/miekg/dns"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
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
	"github.com/ionos-cloud/coredns-externaldns/internal/watcher"
)

var pluginLog = clog.NewWithPlugin("externaldns-plugin")

// Plugin represents the ExternalDNS CoreDNS plugin
type Plugin struct {
	Next plugin.Handler

	// Configuration
	config      *Config
	sortedZones []string // Zones sorted by length (longest first) for efficient matching

	// Dependencies
	cache          *cache.Cache
	watcher        *watcher.DNSEndpointWatcher
	serviceWatcher *watcher.ServiceWatcher
	ingressWatcher *watcher.IngressWatcher
	transfer       *transfer.Transfer
	recordBuilder  *dnsbuilder.RecordBuilder

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

	// Normalize and sort zones by length (longest first) for efficient matching
	sortedZones := make([]string, len(config.Zones))
	for i, zone := range config.Zones {
		// Normalize each zone once during creation
		sortedZones[i] = dns.Fqdn(strings.ToLower(zone))
	}

	// Sort zones by length descending for longest match first
	sort.Slice(sortedZones, func(i, j int) bool {
		return len(sortedZones[i]) > len(sortedZones[j])
	})

	return &Plugin{
		config:             config,
		sortedZones:        sortedZones,
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
	zone := p.getConfiguredZoneForDomain(qname)

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
	records := p.cache.GetRecords(qname, zone, qtype)
	clog.Debugf("Cache lookup for %s %s returned %d records", qname, dns.TypeToString[qtype], len(records))

	// If no direct records found and querying for A or AAAA, check for CNAME records
	if len(records) == 0 && (qtype == dns.TypeA || qtype == dns.TypeAAAA) {
		cnameRecords := p.cache.GetRecords(qname, zone, dns.TypeCNAME)
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
		// No records found
		clog.Debugf("No records found for %s %s", qname, dns.TypeToString[qtype])

		// If not in a configured zone, pass to next plugin
		if zone == "." {
			return plugin.NextOrFailure(p.Name(), p.Next, ctx, w, r)
		}

		// Check if the domain exists with any record type
		domainExists := p.cache.DomainExists(qname, zone)

		if domainExists {
			// Domain exists but not this record type - return NODATA (NOERROR with empty answer)
			m := new(dns.Msg)
			m.SetRcode(r, dns.RcodeSuccess)
			m.Authoritative = true
			m.Ns = []dns.RR{p.createSOARecord(zone, p.getZoneSerial(zone))}

			clog.Debugf("Returning NODATA for %s %s in zone %s (domain exists)", qname, dns.TypeToString[qtype], zone)

			if err := w.WriteMsg(m); err != nil {
				clog.Errorf("Failed to write NODATA response: %v", err)
				return dns.RcodeServerFailure, err
			}

			return dns.RcodeSuccess, nil
		}

		// Domain doesn't exist at all - return authoritative NXDOMAIN
		m := new(dns.Msg)
		m.SetRcode(r, dns.RcodeNameError)
		m.Authoritative = true
		m.Ns = []dns.RR{p.createSOARecord(zone, p.getZoneSerial(zone))}

		clog.Debugf("Returning NXDOMAIN for %s %s in zone %s (domain does not exist)", qname, dns.TypeToString[qtype], zone)

		if err := w.WriteMsg(m); err != nil {
			clog.Errorf("Failed to write NXDOMAIN response: %v", err)
			return dns.RcodeServerFailure, err
		}

		return dns.RcodeNameError, nil
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

	// Create and start the service watcher
	p.serviceWatcher = watcher.NewServiceWatcher(p.coreClient, p.config.Namespace, p)
	if err := p.serviceWatcher.Start(ctx); err != nil {
		return fmt.Errorf("failed to start service watcher: %w", err)
	}

	// Create and start the ingress watcher
	p.ingressWatcher = watcher.NewIngressWatcher(p.coreClient, p.config.Namespace, p)
	if err := p.ingressWatcher.Start(ctx); err != nil {
		return fmt.Errorf("failed to start ingress watcher: %w", err)
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
	if p.serviceWatcher != nil {
		p.serviceWatcher.Stop()
	}
	if p.ingressWatcher != nil {
		p.ingressWatcher.Stop()
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

	createPTR := p.shouldCreatePTR(endpoint)
	serial := p.generateSerial(endpoint, watch.Added)

	return p.processEndpoints(endpoint.Spec.Endpoints, createPTR, watch.Added, serial)
}

// OnUpdate handles DNSEndpoint update events
func (p *Plugin) OnUpdate(endpoint *externaldnsv1alpha1.DNSEndpoint) error {
	pluginLog.Debugf("Updating DNSEndpoint: %s/%s", endpoint.Namespace, endpoint.Name)

	createPTR := p.shouldCreatePTR(endpoint)
	serial := p.generateSerial(endpoint, watch.Modified)

	return p.processEndpoints(endpoint.Spec.Endpoints, createPTR, watch.Modified, serial)
}

// OnDelete handles DNSEndpoint deletion events
func (p *Plugin) OnDelete(endpoint *externaldnsv1alpha1.DNSEndpoint) error {
	pluginLog.Debugf("Deleting DNSEndpoint: %s/%s", endpoint.Namespace, endpoint.Name)

	createPTR := p.shouldCreatePTR(endpoint)
	serial := p.generateSerial(endpoint, watch.Deleted)

	return p.processEndpoints(endpoint.Spec.Endpoints, createPTR, watch.Deleted, serial)
}

// OnServiceAdd handles Service addition events
func (p *Plugin) OnServiceAdd(svc *corev1.Service) error {
	pluginLog.Debugf("Adding Service: %s/%s", svc.Namespace, svc.Name)
	return p.processService(svc, watch.Added)
}

// OnServiceUpdate handles Service update events
func (p *Plugin) OnServiceUpdate(svc *corev1.Service) error {
	pluginLog.Debugf("Updating Service: %s/%s", svc.Namespace, svc.Name)
	return p.processService(svc, watch.Modified)
}

// OnServiceDelete handles Service deletion events
func (p *Plugin) OnServiceDelete(svc *corev1.Service) error {
	pluginLog.Debugf("Deleting Service: %s/%s", svc.Namespace, svc.Name)
	return p.processService(svc, watch.Deleted)
}

func (p *Plugin) processService(svc *corev1.Service, eventType watch.EventType) error {
	endpoints := p.getEndpointsFromService(svc)
	if len(endpoints) == 0 {
		return nil
	}

	createPTR := p.shouldCreatePTR(svc)

	zones := make(map[string]bool)
	for _, ep := range endpoints {
		zones[p.getConfiguredZoneForDomain(ep.DNSName)] = true
	}

	serial := p.getNextSerial(zones)
	return p.processEndpoints(endpoints, createPTR, eventType, serial)
}

func (p *Plugin) processEndpoints(endpoints []*endpoint.Endpoint, createPTR bool, eventType watch.EventType, serial uint32) error {
	if eventType == watch.Deleted {
		for _, ep := range endpoints {
			if err := p.removeEndpointRecord(ep, createPTR); err != nil {
				pluginLog.Errorf("Failed to remove endpoint record: %v", err)
			}
		}
	} else {
		// Added or Modified
		if eventType == watch.Modified {
			for _, ep := range endpoints {
				qtype := p.recordBuilder.RecordTypeToQType(ep.RecordType)
				if qtype != 0 {
					zone := p.getConfiguredZoneForDomain(ep.DNSName)
					p.cache.RemoveAllRecords(ep.DNSName, zone, qtype)
					if createPTR && (qtype == dns.TypeA || qtype == dns.TypeAAAA) {
						p.cache.RemoveAllPTRRecordsForName(ep.DNSName)
					}
				}
			}
		}

		for _, ep := range endpoints {
			if err := p.addEndpointRecord(ep, createPTR); err != nil {
				pluginLog.Errorf("Failed to add endpoint record: %v", err)
				continue
			}
		}
	}

	// Update metrics
	if p.metrics.EndpointEvents != nil {
		label := "added"
		switch eventType {
		case watch.Modified:
			label = "updated"
		case watch.Deleted:
			label = "deleted"
		}
		p.metrics.EndpointEvents.WithLabelValues(label).Inc()
	}

	// Update zone serials
	zones := make(map[string]bool)
	for _, ep := range endpoints {
		zones[p.getConfiguredZoneForDomain(ep.DNSName)] = true
	}

	updatedZones := make([]string, 0, len(zones))
	p.serialsMutex.Lock()
	for zone := range zones {
		// Skip updating serial for ROOT zone
		if zone == "." {
			continue
		}

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

	return nil
}

func (p *Plugin) getEndpointsFromService(svc *corev1.Service) []*endpoint.Endpoint {
	if svc.Spec.Type != corev1.ServiceTypeLoadBalancer {
		return nil
	}

	hostname, ok := svc.Annotations["external-dns.alpha.kubernetes.io/hostname"]
	if !ok || hostname == "" {
		return nil
	}

	var endpoints []*endpoint.Endpoint
	ttlVal := endpoint.TTL(p.config.TTL)
	// Check for TTL annotation
	if ttlStr, ok := svc.Annotations["external-dns.alpha.kubernetes.io/ttl"]; ok {
		if t, err := strconv.Atoi(ttlStr); err == nil {
			ttlVal = endpoint.TTL(t)
		}
	}

	hostnames := strings.SplitSeq(hostname, ",")

	for host := range hostnames {
		host = strings.TrimSpace(host)
		if host == "" {
			continue
		}

		for _, ingress := range svc.Status.LoadBalancer.Ingress {
			if ingress.IP != "" {
				endpoints = append(endpoints, &endpoint.Endpoint{
					DNSName:    host,
					Targets:    endpoint.Targets{ingress.IP},
					RecordType: "A",
					RecordTTL:  ttlVal,
				})
			}
			if ingress.Hostname != "" {
				endpoints = append(endpoints, &endpoint.Endpoint{
					DNSName:    host,
					Targets:    endpoint.Targets{ingress.Hostname},
					RecordType: "CNAME",
					RecordTTL:  ttlVal,
				})
			}
		}
	}

	return endpoints
}

// OnIngressAdd handles Ingress addition events
func (p *Plugin) OnIngressAdd(ing *networkingv1.Ingress) error {
	pluginLog.Debugf("Adding Ingress: %s/%s", ing.Namespace, ing.Name)
	return p.processIngress(ing, watch.Added)
}

// OnIngressUpdate handles Ingress update events
func (p *Plugin) OnIngressUpdate(ing *networkingv1.Ingress) error {
	pluginLog.Debugf("Updating Ingress: %s/%s", ing.Namespace, ing.Name)
	return p.processIngress(ing, watch.Modified)
}

// OnIngressDelete handles Ingress deletion events
func (p *Plugin) OnIngressDelete(ing *networkingv1.Ingress) error {
	pluginLog.Debugf("Deleting Ingress: %s/%s", ing.Namespace, ing.Name)
	return p.processIngress(ing, watch.Deleted)
}

func (p *Plugin) processIngress(ing *networkingv1.Ingress, eventType watch.EventType) error {
	endpoints := p.getEndpointsFromIngress(ing)
	if len(endpoints) == 0 {
		return nil
	}

	createPTR := p.shouldCreatePTR(ing)

	zones := make(map[string]bool)
	for _, ep := range endpoints {
		zones[p.getConfiguredZoneForDomain(ep.DNSName)] = true
	}

	serial := p.getNextSerial(zones)
	return p.processEndpoints(endpoints, createPTR, eventType, serial)
}

func (p *Plugin) getEndpointsFromIngress(ing *networkingv1.Ingress) []*endpoint.Endpoint {
	var endpoints []*endpoint.Endpoint
	ttlVal := endpoint.TTL(p.config.TTL)

	// Check for TTL annotation
	if ttlStr, ok := ing.Annotations["external-dns.alpha.kubernetes.io/ttl"]; ok {
		if t, err := strconv.Atoi(ttlStr); err == nil {
			ttlVal = endpoint.TTL(t)
		}
	}

	// Get targets from Ingress status
	var targets endpoint.Targets
	for _, ingress := range ing.Status.LoadBalancer.Ingress {
		if ingress.IP != "" {
			targets = append(targets, ingress.IP)
		}
		if ingress.Hostname != "" {
			targets = append(targets, ingress.Hostname)
		}
	}

	if len(targets) == 0 {
		return nil
	}

	// Determine hosts to use
	var hosts []string
	seenHosts := make(map[string]bool)

	if hostname, ok := ing.Annotations["external-dns.alpha.kubernetes.io/hostname"]; ok && hostname != "" {
		// Use explicitly defined hostname(s)
		for _, h := range strings.Split(hostname, ",") {
			if h = strings.TrimSpace(h); h != "" {
				if !seenHosts[h] {
					hosts = append(hosts, h)
					seenHosts[h] = true
				}
			}
		}
	}

	if val, ok := ing.Annotations["coredns-externaldns.ionos.cloud/ingress-from-rules"]; ok && (val == "true" || val == "1") {
		// Use hosts from Ingress rules if enabled via annotation
		for _, rule := range ing.Spec.Rules {
			if rule.Host != "" {
				if !seenHosts[rule.Host] {
					hosts = append(hosts, rule.Host)
					seenHosts[rule.Host] = true
				}
			}
		}
	}

	if len(hosts) == 0 {
		return nil
	}

	// Create endpoints for each host
	for _, host := range hosts {
		targetsByRecordType := make(map[string]endpoint.Targets)
		for _, target := range targets {
			recordType := "A"
			if net.ParseIP(target) == nil {
				recordType = "CNAME"
			}
			targetsByRecordType[recordType] = append(targetsByRecordType[recordType], target)
		}

		for recordType, t := range targetsByRecordType {
			endpoints = append(endpoints, &endpoint.Endpoint{
				DNSName:    host,
				Targets:    t,
				RecordType: recordType,
				RecordTTL:  ttlVal,
			})
		}
	}

	return endpoints
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

	// Determine the zone for this endpoint using configured zones
	zone := p.getConfiguredZoneForDomain(ep.DNSName)

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

		p.cache.AddRecord(ep.DNSName, zone, qtype, rr)

		// Create PTR record if requested and applicable
		if createPTR && (qtype == dns.TypeA || qtype == dns.TypeAAAA) {
			if ptrRR := p.recordBuilder.CreatePTRRecord(target, ep.DNSName, ttl); ptrRR != nil {
				// PTR records typically go in reverse zones, but we'll use the same zone for simplicity
				p.cache.AddRecord(ptrRR.Header().Name, zone, dns.TypePTR, ptrRR)
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

	// Determine the zone for this endpoint
	zone := p.getConfiguredZoneForDomain(ep.DNSName)

	for _, target := range ep.Targets {
		p.cache.RemoveRecord(ep.DNSName, zone, qtype, target)

		// Remove PTR record if it was created
		if createPTR && (qtype == dns.TypeA || qtype == dns.TypeAAAA) {
			if ptrName, err := dns.ReverseAddr(target); err == nil {
				// PTR records are in reverse zones, determine the appropriate zone
				ptrZone := p.getConfiguredZoneForDomain(ptrName)
				p.cache.RemoveRecord(ptrName, ptrZone, dns.TypePTR, ep.DNSName)
			}
		}
	}

	return nil
}

// shouldCreatePTR checks if PTR records should be created
func (p *Plugin) shouldCreatePTR(obj metav1.Object) bool {
	annotations := obj.GetAnnotations()
	if annotations == nil {
		return false
	}

	val, exists := annotations["coredns-externaldns.ionos.cloud/create-ptr"]
	return exists && (val == "true" || val == "1")
}

// generateSerial generates a serial number for the DNSEndpoint
// Uses a resource-based approach that's idempotent and doesn't depend on event types
func (p *Plugin) generateSerial(endpoint *externaldnsv1alpha1.DNSEndpoint, eventType watch.EventType) uint32 {
	if eventType == watch.Added {
		// For ADD events, always use resource-based serial for idempotency
		// This ensures same resource always gets same serial regardless of watcher restarts
		return p.calculateResourceSerial(endpoint)
	}

	// Determine which zones will be affected
	zones := make(map[string]bool)
	for _, ep := range endpoint.Spec.Endpoints {
		zones[p.getConfiguredZoneForDomain(ep.DNSName)] = true
	}

	return p.getNextSerial(zones)
}

// getNextSerial calculates the next serial number for the given zones
func (p *Plugin) getNextSerial(zones map[string]bool) uint32 {
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

	if maxSerial == 0 {
		return uint32(time.Now().Unix())
	}
	return maxSerial + 1
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

// zoneToConfigKey converts a zone name to a valid ConfigMap key
// Kubernetes ConfigMap keys cannot be "." so we convert it to "ROOT"
func (p *Plugin) zoneToConfigKey(zone string) string {
	if zone == "." {
		return "ROOT"
	}
	return zone
}

// configKeyToZone converts a ConfigMap key back to a zone name
func (p *Plugin) configKeyToZone(key string) string {
	if key == "ROOT" {
		return "."
	}
	return key
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
	for configKey, serialStr := range cm.Data {
		// Convert ConfigMap key back to zone name
		zone := p.configKeyToZone(configKey)
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
		// Convert zone name to valid ConfigMap key
		configKey := p.zoneToConfigKey(zone)
		data[configKey] = strconv.FormatUint(uint64(serial), 10)
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

// getZoneSerial retrieves the serial number for a zone
func (p *Plugin) getZoneSerial(zone string) uint32 {
	p.serialsMutex.RLock()
	defer p.serialsMutex.RUnlock()
	if serial, exists := p.zoneSerials[zone]; exists {
		return serial
	}
	return uint32(time.Now().Unix())
}

// resolveCNAMETarget resolves a CNAME target to get the actual A/AAAA records
func (p *Plugin) resolveCNAMETarget(target string, qtype uint16, maxHops int) []dns.RR {
	if maxHops <= 0 {
		clog.Debugf("Maximum CNAME hops reached for %s", target)
		return nil
	}

	// Determine the zone for the target
	targetZone := p.getConfiguredZoneForDomain(target)

	// First, try to get direct A/AAAA records for the target
	targetRecords := p.cache.GetRecords(target, targetZone, qtype)
	if len(targetRecords) > 0 {
		clog.Debugf("Found %d %s records for CNAME target %s", len(targetRecords), dns.TypeToString[qtype], target)
		return targetRecords
	}

	// If no direct records, check if the target is also a CNAME
	cnameRecords := p.cache.GetRecords(target, targetZone, dns.TypeCNAME)
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

// getConfiguredZoneForDomain finds the best matching configured zone for a domain
// Returns the longest matching zone or "." if no match is found
func (p *Plugin) getConfiguredZoneForDomain(domain string) string {
	// Normalize the domain
	domain = dns.Fqdn(strings.ToLower(domain))

	// Remove wildcard prefix if present
	domain = strings.TrimPrefix(domain, "*.")

	// Find the best matching zone from pre-sorted and pre-normalized zones (longest first)
	for _, normalizedZone := range p.sortedZones {
		// Handle root zone specially
		if normalizedZone == "." {
			return normalizedZone
		}

		// Check if domain matches exactly
		if domain == normalizedZone {
			return normalizedZone
		}

		// Check if domain is a subdomain of this zone
		// Ensure proper zone boundary by checking if domain ends with ".<zone>"
		if strings.HasSuffix(domain, "."+normalizedZone) {
			return normalizedZone
		}
	}

	// If no configured zone matches, return "." as default
	return "."
}
