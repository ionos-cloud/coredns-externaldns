package externaldns

import (
	"context"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coredns/coredns/plugin"
	"github.com/coredns/coredns/plugin/metrics"
	clog "github.com/coredns/coredns/plugin/pkg/log"
	"github.com/coredns/coredns/plugin/transfer"
	"github.com/miekg/dns"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/external-dns/endpoint"
)

var _ = watch.Added // Force usage of watch package

var log = clog.NewWithPlugin("externaldns")

// ExternalDNS represents a plugin instance
type ExternalDNS struct {
	Next plugin.Handler

	namespace             string
	ttl                   uint32
	metricsUpdateInterval time.Duration

	client dynamic.Interface
	cache  *DNSCache
	ctx    context.Context
	cancel context.CancelFunc
}

// DNSCache holds DNS records in memory
type DNSCache struct {
	sync.RWMutex

	zones                 map[string]*Zone // zone -> Zone info
	metricsUpdateInterval time.Duration
	metricsStopCh         chan struct{}
	metricsStoppedCh      chan struct{}
}

// Zone represents a DNS zone
type Zone struct {
	sync.RWMutex

	Name    string
	Serial  uint32
	Records map[string]map[uint16][]dns.RR // domain -> qtype -> records
}

// DNSRecord represents a DNS record with metadata
type DNSRecord struct {
	Name   string
	Type   uint16
	TTL    uint32
	Target string
	RR     dns.RR
}

// NewDNSCache creates a new DNS cache
func NewDNSCache(metricsUpdateInterval time.Duration) *DNSCache {
	cache := &DNSCache{
		zones:                 make(map[string]*Zone),
		metricsUpdateInterval: metricsUpdateInterval,
		metricsStopCh:         make(chan struct{}),
		metricsStoppedCh:      make(chan struct{}),
	}

	// Start the metrics update goroutine
	go cache.metricsUpdateLoop()

	return cache
}

// Stop stops the DNS cache and its background goroutines
func (c *DNSCache) Stop() {
	close(c.metricsStopCh)
	<-c.metricsStoppedCh
}

// metricsUpdateLoop runs in a background goroutine to periodically update cache metrics
func (c *DNSCache) metricsUpdateLoop() {
	defer close(c.metricsStoppedCh)

	if c.metricsUpdateInterval <= 0 {
		// If interval is 0 or negative, don't run metrics updates
		log.Debug("Metrics update interval disabled")
		return
	}

	ticker := time.NewTicker(c.metricsUpdateInterval)
	defer ticker.Stop()

	// Initial update
	c.updateCacheSizeMetricsUnlocked()

	for {
		select {
		case <-c.metricsStopCh:
			log.Debug("Stopping metrics update loop")
			return
		case <-ticker.C:
			c.updateCacheSizeMetricsUnlocked()
		}
	}
}

func getRecordName(name string) string {
	// Normalize the name to lowercase and FQDN
	name = strings.ToLower(name)
	return dns.Fqdn(name)
}

// AddRecord adds a DNS record to the cache
func (c *DNSCache) AddRecord(name string, qtype uint16, rr dns.RR) {
	name = getRecordName(name)
	zoneName := getZoneName(name)

	c.Lock()
	defer c.Unlock()

	// Ensure zone exists
	zone := c.ensureZoneUnlocked(zoneName)

	zone.Lock()
	defer zone.Unlock()

	// Initialize domain records if needed
	if zone.Records[name] == nil {
		zone.Records[name] = make(map[uint16][]dns.RR)
	}

	zone.Records[name][qtype] = append(zone.Records[name][qtype], rr)

	log.Debugf("Added record: %s %s -> %s", name, dns.TypeToString[qtype], rr.String())

	// Update zone serial
	zone.Serial = uint32(time.Now().Unix())
}

// RemoveRecord removes a DNS record from the cache
func (c *DNSCache) RemoveRecord(name string, qtype uint16, target string) {
	name = getRecordName(name)
	zoneName := getZoneName(name)

	c.RLock()
	zone, exists := c.zones[zoneName]
	c.RUnlock()

	if !exists {
		return
	}

	zone.Lock()
	defer zone.Unlock()

	if zone.Records[name] == nil {
		return
	}

	records := zone.Records[name][qtype]
	for i, rr := range records {
		if c.recordMatches(rr, target) {
			zone.Records[name][qtype] = append(records[:i], records[i+1:]...)
			break
		}
	}

	// Clean up empty entries
	if len(zone.Records[name][qtype]) == 0 {
		delete(zone.Records[name], qtype)
	}
	if len(zone.Records[name]) == 0 {
		delete(zone.Records, name)
	}

	// Update zone serial
	zone.Serial = uint32(time.Now().Unix())
}

// GetRecords retrieves DNS records from the cache
func (c *DNSCache) GetRecords(name string, qtype uint16) []dns.RR {
	name = getRecordName(name)
	zoneName := getZoneName(name)

	c.RLock()
	zone, exists := c.zones[zoneName]
	c.RUnlock()

	if !exists {
		return nil
	}

	zone.RLock()
	defer zone.RUnlock()

	if zone.Records[name] != nil {
		if records, typeExists := zone.Records[name][qtype]; typeExists {
			result := make([]dns.RR, len(records))
			copy(result, records)
			return result
		}
	}
	return nil
}

// ClearRecords clears all records for a domain
func (c *DNSCache) ClearRecords(name string) {
	name = getRecordName(name)
	zoneName := getZoneName(name)

	c.RLock()
	zone, exists := c.zones[zoneName]
	c.RUnlock()

	if !exists {
		return
	}

	zone.Lock()
	defer zone.Unlock()

	delete(zone.Records, name)
	// Update zone serial
	zone.Serial = uint32(time.Now().Unix())
}

// GetCacheSize returns the total number of records in the cache
func (c *DNSCache) GetCacheSize() int {
	c.RLock()
	defer c.RUnlock()

	total := 0
	for _, zone := range c.zones {
		zone.RLock()
		for _, types := range zone.Records {
			for _, records := range types {
				total += len(records)
			}
		}
		zone.RUnlock()
	}
	return total
}

// updateCacheSizeMetricsUnlocked updates the cache size metrics (assumes cache lock held)
func (c *DNSCache) updateCacheSizeMetricsUnlocked() {
	c.RLock()
	defer c.RUnlock()

	total := 0
	for _, zone := range c.zones {
		zone.RLock()
		for _, types := range zone.Records {
			for _, records := range types {
				total += len(records)
			}
		}
		zone.RUnlock()
	}

	// Update the gauge - using "coredns" as server label since we don't have server context here
	externalDNSCacheSize.WithLabelValues("coredns").Set(float64(total))
}

// recordMatches checks if a DNS record matches the target
func (c *DNSCache) recordMatches(rr dns.RR, target string) bool {
	switch r := rr.(type) {
	case *dns.A:
		return r.A.String() == target
	case *dns.AAAA:
		return r.AAAA.String() == target
	case *dns.CNAME:
		return r.Target == target
	case *dns.MX:
		return r.Mx == target
	case *dns.TXT:
		if slices.Contains(r.Txt, target) {
			return true
		}
	case *dns.SRV:
		return r.Target == target
	case *dns.PTR:
		return r.Ptr == target
	case *dns.NS:
		return r.Ns == target
	}
	return false
}

// getZoneName extracts the zone name from a domain name
func getZoneName(name string) string {
	parts := strings.Split(strings.TrimSuffix(name, "."), ".")
	if len(parts) >= 2 {
		return strings.Join(parts[len(parts)-2:], ".") + "."
	}
	return name
}

// ensureZone creates a zone if it doesn't exist
func (c *DNSCache) ensureZone(zoneName string) *Zone {
	c.Lock()
	defer c.Unlock()

	return c.ensureZoneUnlocked(zoneName)
}

// ensureZoneUnlocked creates a zone if it doesn't exist (assumes lock is held)
func (c *DNSCache) ensureZoneUnlocked(zoneName string) *Zone {
	if zone, exists := c.zones[zoneName]; exists {
		return zone
	}

	zone := &Zone{
		Name:    zoneName,
		Serial:  uint32(time.Now().Unix()),
		Records: make(map[string]map[uint16][]dns.RR),
	}
	c.zones[zoneName] = zone

	log.Debugf("Created new zone: %s", zoneName)
	return zone
}

// updateZoneSerial updates the serial number for a zone
func (c *DNSCache) updateZoneSerial(zoneName string) {
	c.RLock()
	zone, exists := c.zones[zoneName]
	c.RUnlock()

	if exists && zone != nil {
		zone.Lock()
		zone.Serial = uint32(time.Now().Unix())
		zone.Unlock()
		log.Debugf("Updated serial for zone %s to %d", zoneName, zone.Serial)
	}
}

// getZoneRecordsForAXFR gets all records for a zone in AXFR format
func (c *DNSCache) getZoneRecordsForAXFR(zoneName string) []dns.RR {
	c.RLock()
	zone, exists := c.zones[zoneName]
	c.RUnlock()

	if !exists {
		return nil
	}

	zone.RLock()
	defer zone.RUnlock()

	var allRecords []dns.RR

	// Add SOA record first
	soaRecord := &dns.SOA{
		Hdr: dns.RR_Header{
			Name:   zoneName,
			Rrtype: dns.TypeSOA,
			Class:  dns.ClassINET,
			Ttl:    300,
		},
		Ns:      "ns1." + zoneName,
		Mbox:    "admin." + zoneName,
		Serial:  zone.Serial,
		Refresh: 3600,
		Retry:   1800,
		Expire:  604800,
		Minttl:  300,
	}
	allRecords = append(allRecords, soaRecord)

	// Add all records for this zone
	for domain, types := range zone.Records {
		if strings.HasSuffix(domain, zoneName) || domain == strings.TrimSuffix(zoneName, ".") {
			for _, records := range types {
				allRecords = append(allRecords, records...)
			}
		}
	}

	log.Debugf("Zone %s has %d records for AXFR", zoneName, len(allRecords))
	return allRecords
}

// ServeDNS implements the plugin.Handler interface
func (e *ExternalDNS) ServeDNS(ctx context.Context, w dns.ResponseWriter, r *dns.Msg) (int, error) {
	log.Info("Received DNS query")

	if len(r.Question) == 0 {
		return plugin.NextOrFailure(e.Name(), e.Next, ctx, w, r)
	}

	q := r.Question[0]
	qname := strings.ToLower(q.Name)
	qtype := q.Qtype

	// Extract server info for metrics
	server := metrics.WithServer(ctx)

	log.Debugf("Query for %s %s", qname, dns.TypeToString[qtype])

	// Handle AXFR requests
	if qtype == dns.TypeAXFR {
		log.Debugf("Handling AXFR request for zone: %s", qname)
		return e.handleAXFR(w, r, qname)
	}

	// Increment request counter
	externalDNSRequestCount.WithLabelValues(server, "udp", dns.TypeToString[qtype]).Inc()

	// Get records from cache
	records := e.cache.GetRecords(qname, qtype)

	if len(records) == 0 {
		// Try wildcard lookup for subdomains
		parts := strings.Split(qname, ".")
		for i := 1; i < len(parts); i++ {
			wildcard := "*." + strings.Join(parts[i:], ".")
			records = e.cache.GetRecords(wildcard, qtype)
			if len(records) > 0 {
				break
			}
		}
	}

	if len(records) == 0 {
		log.Debugf("No records found for %s %s", qname, dns.TypeToString[qtype])
		return plugin.NextOrFailure(e.Name(), e.Next, ctx, w, r)
	}

	// Create response
	m := new(dns.Msg)
	m.SetReply(r)
	m.Authoritative = true
	m.RecursionAvailable = false

	// Add records to answer section
	m.Answer = append(m.Answer, records...)

	log.Debugf("Returning %d records for %s %s", len(records), qname, dns.TypeToString[qtype])

	w.WriteMsg(m)
	return dns.RcodeSuccess, nil
}

// Name implements the plugin.Handler interface
func (e *ExternalDNS) Name() string { return "externaldns" }

// Transfer implements the transfer.Transfer interface for AXFR support
func (e *ExternalDNS) Transfer(zone string, serial uint32) (<-chan []dns.RR, error) {
	log.Debugf("AXFR requested for zone: %s, serial: %d", zone, serial)

	zoneName := dns.Fqdn(zone)

	// Ensure zone exists
	e.cache.ensureZone(zoneName)

	e.cache.RLock()
	zoneInfo, exists := e.cache.zones[zoneName]
	e.cache.RUnlock()

	if !exists {
		log.Debugf("Zone %s not found for AXFR", zoneName)
		return nil, fmt.Errorf("zone %s not found", zoneName)
	}

	zoneInfo.RLock()
	currentSerial := zoneInfo.Serial
	zoneInfo.RUnlock()

	// If the requested serial is the same or newer, no transfer needed
	if serial >= currentSerial {
		log.Debugf("No transfer needed for zone %s (current: %d, requested: %d)", zoneName, currentSerial, serial)
		return nil, transfer.ErrNotAuthoritative
	}

	// Get all records for the zone
	records := e.cache.getZoneRecordsForAXFR(zoneName)

	// Create channel and send records
	ch := make(chan []dns.RR, 1)

	go func() {
		defer close(ch)

		log.Debugf("Sending AXFR for zone %s with %d records", zoneName, len(records))

		// Send all records for the zone
		if len(records) > 0 {
			ch <- records
		}
	}()

	return ch, nil
}

// Serial implements the transfer.Transfer interface
func (e *ExternalDNS) Serial(zone string) uint32 {
	zoneName := dns.Fqdn(zone)

	e.cache.RLock()
	zoneInfo, exists := e.cache.zones[zoneName]
	e.cache.RUnlock()

	if !exists {
		// If zone doesn't exist, create it
		zoneInfo = e.cache.ensureZone(zoneName)
	}

	zoneInfo.RLock()
	serial := zoneInfo.Serial
	zoneInfo.RUnlock()

	log.Debugf("Serial for zone %s: %d", zoneName, serial)
	return serial
}

// handleAXFR handles AXFR (zone transfer) requests
func (e *ExternalDNS) handleAXFR(w dns.ResponseWriter, r *dns.Msg, zone string) (int, error) {
	zoneName := dns.Fqdn(zone)

	// Ensure zone exists
	e.cache.ensureZone(zoneName)

	e.cache.RLock()
	_, exists := e.cache.zones[zoneName]
	e.cache.RUnlock()

	if !exists {
		log.Debugf("Zone %s not found for AXFR", zoneName)
		return dns.RcodeNotAuth, nil
	}

	// Get all records for the zone
	records := e.cache.getZoneRecordsForAXFR(zoneName)

	// Create AXFR response
	m := new(dns.Msg)
	m.SetReply(r)
	m.Authoritative = true
	m.Answer = records

	log.Debugf("AXFR response for zone %s with %d records", zoneName, len(records))

	err := w.WriteMsg(m)
	if err != nil {
		log.Errorf("Failed to write AXFR response: %v", err)
		return dns.RcodeServerFailure, err
	}

	return dns.RcodeSuccess, nil
}

// Start starts watching DNSEndpoint resources
func (e *ExternalDNS) Start() error {
	// Use in-cluster configuration (service account based)
	config, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("failed to create in-cluster kubernetes config: %v", err)
	}

	e.client, err = dynamic.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create kubernetes client: %v", err)
	}

	// Start watching DNSEndpoint resources
	go e.watchDNSEndpoints()

	log.Info("ExternalDNS plugin started with service account authentication")
	return nil
}

// Stop stops the plugin
func (e *ExternalDNS) Stop() {
	if e.cancel != nil {
		e.cancel()
	}
	log.Info("ExternalDNS plugin stopped")
}

// watchDNSEndpoints watches for DNSEndpoint CRD changes
func (e *ExternalDNS) watchDNSEndpoints() {
	e.ctx, e.cancel = context.WithCancel(context.Background())

	// Define the DNSEndpoint GVR (Group Version Resource)
	dnsEndpointGVR := schema.GroupVersionResource{
		Group:    "externaldns.k8s.io",
		Version:  "v1alpha1",
		Resource: "dnsendpoints",
	}

	var resourceInterface dynamic.ResourceInterface
	if e.namespace != "" {
		resourceInterface = e.client.Resource(dnsEndpointGVR).Namespace(e.namespace)
	} else {
		resourceInterface = e.client.Resource(dnsEndpointGVR)
	}

	// Initial sync - get all existing DNSEndpoints
	list, err := resourceInterface.List(e.ctx, metav1.ListOptions{})
	if err != nil {
		log.Errorf("Failed to list DNSEndpoints: %v", err)
		return
	}

	log.Infof("Initial sync: found %d DNSEndpoints", len(list.Items))
	for _, item := range list.Items {
		e.processDNSEndpoint(&item, "ADDED")
	}

	// Start watching for changes
	watchOpts := metav1.ListOptions{
		Watch:           true,
		ResourceVersion: list.GetResourceVersion(),
	}

	watcher, err := resourceInterface.Watch(e.ctx, watchOpts)
	if err != nil {
		log.Errorf("Failed to start watching DNSEndpoints: %v", err)
		return
	}
	defer watcher.Stop()

	log.Info("Started watching DNSEndpoints")

	for {
		select {
		case <-e.ctx.Done():
			log.Info("Stopping DNSEndpoint watcher")
			return
		case event, ok := <-watcher.ResultChan():
			if !ok {
				log.Warning("DNSEndpoint watch channel closed, restarting...")
				time.Sleep(5 * time.Second)
				go e.watchDNSEndpoints()
				return
			}

			if obj, ok := event.Object.(*unstructured.Unstructured); ok {
				e.processDNSEndpoint(obj, string(event.Type))
			}
		}
	}
}

// processDNSEndpoint processes a DNSEndpoint object
func (e *ExternalDNS) processDNSEndpoint(obj *unstructured.Unstructured, eventType string) {
	// Increment endpoint event metric
	externalDNSEndpointEvents.WithLabelValues("coredns", eventType).Inc()

	log.Debugf("Processing DNSEndpoint %s/%s (event: %s)",
		obj.GetNamespace(), obj.GetName(), eventType)

	// Check for PTR record creation annotation
	createPTR := false
	if annotations := obj.GetAnnotations(); annotations != nil {
		if val, exists := annotations["coredns-externaldns.ionos.cloud/create-ptr"]; exists {
			createPTR = (val == "true" || val == "1")
		}
	}

	// Extract endpoints from the object
	endpoints, err := e.extractEndpoints(obj)
	if err != nil {
		log.Errorf("Failed to extract endpoints from %s/%s: %v",
			obj.GetNamespace(), obj.GetName(), err)
		return
	}

	switch eventType {
	case "ADDED", "MODIFIED":
		// Clear existing records for this DNSEndpoint
		e.clearDNSEndpointRecords(obj.GetNamespace(), obj.GetName())

		// Add new records
		for _, ep := range endpoints {
			e.addEndpointToCache(ep, createPTR)
		}

		log.Debugf("Updated cache with %d endpoints from %s/%s (createPTR: %v)",
			len(endpoints), obj.GetNamespace(), obj.GetName(), createPTR)

	case "DELETED":
		// Remove all records for this DNSEndpoint
		e.clearDNSEndpointRecords(obj.GetNamespace(), obj.GetName())

		log.Debugf("Removed endpoints from %s/%s",
			obj.GetNamespace(), obj.GetName())
	}
}

// extractEndpoints extracts endpoint.Endpoint objects from unstructured data
func (e *ExternalDNS) extractEndpoints(obj *unstructured.Unstructured) ([]*endpoint.Endpoint, error) {
	spec, found, err := unstructured.NestedMap(obj.Object, "spec")
	if err != nil || !found {
		return nil, fmt.Errorf("spec not found in DNSEndpoint")
	}

	endpointsRaw, found, err := unstructured.NestedSlice(spec, "endpoints")
	if err != nil || !found {
		return nil, fmt.Errorf("endpoints not found in DNSEndpoint spec")
	}

	var endpoints []*endpoint.Endpoint
	for _, epRaw := range endpointsRaw {
		epMap, ok := epRaw.(map[string]interface{})
		if !ok {
			continue
		}

		ep := &endpoint.Endpoint{}

		// Extract DNS name
		if dnsName, found, _ := unstructured.NestedString(epMap, "dnsName"); found {
			ep.DNSName = dnsName
		}

		// Extract record type
		if recordType, found, _ := unstructured.NestedString(epMap, "recordType"); found {
			ep.RecordType = recordType
		}

		// Extract TTL
		if ttl, found, _ := unstructured.NestedInt64(epMap, "recordTTL"); found {
			ep.RecordTTL = endpoint.TTL(ttl)
		}

		// Extract targets
		if targets, found, _ := unstructured.NestedStringSlice(epMap, "targets"); found {
			ep.Targets = targets
		}

		endpoints = append(endpoints, ep)
	}

	return endpoints, nil
}

// addEndpointToCache adds an endpoint to the DNS cache
func (e *ExternalDNS) addEndpointToCache(ep *endpoint.Endpoint, createPTR bool) {
	if ep.DNSName == "" || len(ep.Targets) == 0 {
		return
	}

	qtype := e.recordTypeToQType(ep.RecordType)
	if qtype == 0 {
		log.Warningf("Unsupported record type: %s", ep.RecordType)
		return
	}

	ttl := e.ttl
	if ep.RecordTTL > 0 {
		ttl = uint32(ep.RecordTTL)
	}

	for _, target := range ep.Targets {
		rr := e.createDNSRecord(ep.DNSName, qtype, ttl, target)
		if rr != nil {
			e.cache.AddRecord(ep.DNSName, qtype, rr)

			// Create PTR records for A and AAAA records if annotation is present
			if createPTR && (qtype == dns.TypeA || qtype == dns.TypeAAAA) {
				e.createAndAddPTRRecord(ep.DNSName, target, ttl)
			}
		}
	}
}

// recordTypeToQType converts string record type to DNS qtype
func (e *ExternalDNS) recordTypeToQType(recordType string) uint16 {
	switch strings.ToUpper(recordType) {
	case "A":
		return dns.TypeA
	case "AAAA":
		return dns.TypeAAAA
	case "CNAME":
		return dns.TypeCNAME
	case "MX":
		return dns.TypeMX
	case "TXT":
		return dns.TypeTXT
	case "SRV":
		return dns.TypeSRV
	case "PTR":
		return dns.TypePTR
	case "NS":
		return dns.TypeNS
	case "SOA":
		return dns.TypeSOA
	default:
		return 0
	}
}

// createDNSRecord creates a DNS resource record
func (e *ExternalDNS) createDNSRecord(name string, qtype uint16, ttl uint32, target string) dns.RR {
	header := dns.RR_Header{
		Name:   dns.Fqdn(name),
		Rrtype: qtype,
		Class:  dns.ClassINET,
		Ttl:    ttl,
	}

	switch qtype {
	case dns.TypeA:
		return &dns.A{
			Hdr: header,
			A:   net.ParseIP(target),
		}
	case dns.TypeAAAA:
		return &dns.AAAA{
			Hdr:  header,
			AAAA: net.ParseIP(target),
		}
	case dns.TypeCNAME:
		return &dns.CNAME{
			Hdr:    header,
			Target: dns.Fqdn(target),
		}
	case dns.TypeMX:
		// Parse priority and target from target string (format: "priority target")
		parts := strings.SplitN(target, " ", 2)
		if len(parts) != 2 {
			return nil
		}
		var priority uint16
		fmt.Sscanf(parts[0], "%d", &priority)
		return &dns.MX{
			Hdr:        header,
			Preference: priority,
			Mx:         dns.Fqdn(parts[1]),
		}
	case dns.TypeTXT:
		return &dns.TXT{
			Hdr: header,
			Txt: []string{target},
		}
	case dns.TypeSRV:
		// Parse SRV record (format: "priority weight port target")
		parts := strings.SplitN(target, " ", 4)
		if len(parts) != 4 {
			return nil
		}
		var priority, weight, port uint16
		fmt.Sscanf(parts[0], "%d", &priority)
		fmt.Sscanf(parts[1], "%d", &weight)
		fmt.Sscanf(parts[2], "%d", &port)
		return &dns.SRV{
			Hdr:      header,
			Priority: priority,
			Weight:   weight,
			Port:     port,
			Target:   dns.Fqdn(parts[3]),
		}
	case dns.TypePTR:
		return &dns.PTR{
			Hdr: header,
			Ptr: dns.Fqdn(target),
		}
	case dns.TypeNS:
		return &dns.NS{
			Hdr: header,
			Ns:  dns.Fqdn(target),
		}
	default:
		return nil
	}
}

// createAndAddPTRRecord creates and adds a PTR record for the given IP address
func (e *ExternalDNS) createAndAddPTRRecord(hostname, ipAddr string, ttl uint32) {
	ptrName := e.createReverseDNSName(ipAddr)
	if ptrName == "" {
		return
	}

	ptrRecord := &dns.PTR{
		Hdr: dns.RR_Header{
			Name:   ptrName,
			Rrtype: dns.TypePTR,
			Class:  dns.ClassINET,
			Ttl:    ttl,
		},
		Ptr: dns.Fqdn(hostname),
	}

	e.cache.AddRecord(ptrName, dns.TypePTR, ptrRecord)

	log.Debugf("Created PTR record: %s -> %s", ptrName, hostname)
}

// createReverseDNSName creates a reverse DNS name from an IP address
func (e *ExternalDNS) createReverseDNSName(ipAddr string) string {
	ip := net.ParseIP(ipAddr)
	if ip == nil {
		log.Warningf("Invalid IP address for PTR record: %s", ipAddr)
		return ""
	}

	if ip.To4() != nil {
		// IPv4 reverse DNS
		octets := strings.Split(ip.To4().String(), ".")
		if len(octets) != 4 {
			return ""
		}
		// Reverse the octets and append .in-addr.arpa.
		return fmt.Sprintf("%s.%s.%s.%s.in-addr.arpa.", octets[3], octets[2], octets[1], octets[0])
	} else if ip.To16() != nil {
		// IPv6 reverse DNS
		// Convert to full hex representation
		ipv6 := ip.To16()
		var nibbles []string
		for i := 15; i >= 0; i-- {
			nibbles = append(nibbles, fmt.Sprintf("%x", ipv6[i]&0x0f))
			nibbles = append(nibbles, fmt.Sprintf("%x", (ipv6[i]&0xf0)>>4))
		}
		return strings.Join(nibbles, ".") + ".ip6.arpa."
	}

	return ""
}

// clearDNSEndpointRecords removes all records associated with a DNSEndpoint
func (e *ExternalDNS) clearDNSEndpointRecords(namespace, name string) {
	// In a real implementation, you might want to track which records
	// belong to which DNSEndpoint for more precise cleanup
	// For now, this is a simplified approach
	log.Debugf("Clearing records for DNSEndpoint %s/%s", namespace, name)
}
