package externaldns

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coredns/coredns/plugin"
	"github.com/coredns/coredns/plugin/metrics"
	clog "github.com/coredns/coredns/plugin/pkg/log"
	"github.com/coredns/coredns/plugin/transfer"
	"github.com/miekg/dns"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	coreclientv1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	externaldnsv1alpha1 "sigs.k8s.io/external-dns/apis/v1alpha1"
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

	client     dynamic.Interface
	coreClient coreclientv1.CoreV1Interface
	cache      *DNSCache
	ctx        context.Context
	cancel     context.CancelFunc
	transfer   *transfer.Transfer

	// Zone serial management
	zoneSerials            map[string]uint32
	serialsMutex           sync.RWMutex
	configMapName          string
	configMapNamespace     string
	serialsResourceVersion string
}

// RecordRef represents a reference to a specific DNS record
type RecordRef struct {
	Zone   string
	Name   string
	Type   uint16
	Target string
}

// DNSCache holds DNS records in memory
type DNSCache struct {
	sync.RWMutex

	zones                 map[string]*Zone       // zone -> Zone info
	endpointRecords       map[string][]RecordRef // namespace/name -> record references
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
		endpointRecords:       make(map[string][]RecordRef),
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

// generateConsistentSerial produces a deterministic serial number based on creation timestamp and generation.
// Ensures all CoreDNS instances generate the same serial for the same CR state.
// Format: YYYYMMDDnn, where nn is derived from .metadata.generation.
func generateConsistentSerial(dnsEndpoint *externaldnsv1alpha1.DNSEndpoint) uint32 {
	t := dnsEndpoint.GetCreationTimestamp()
	if t.IsZero() {
		// Fallback: use current time if creationTimestamp is unset
		t = metav1.NewTime(time.Now())
	}

	// Format date as YYYYMMDD and reserve two digits (nn) for generation
	base := uint32(t.Year()*10000 + int(t.Month())*100 + t.Day())
	serial := base*100 + uint32(dnsEndpoint.GetGeneration())

	return serial
}

func getRecordName(name string) string {
	// Normalize the name to lowercase and FQDN
	name = strings.ToLower(name)
	return dns.Fqdn(name)
}

// AddRecordWithEndpoint adds a DNS record to the cache and tracks its ownership
func (c *DNSCache) AddRecordWithEndpoint(name string, qtype uint16, rr dns.RR, endpointKey, target string) {
	name = getRecordName(name)
	zoneName := getZoneName(name)

	// Normalize target for certain record types that use FQDN
	normalizedTarget := target
	switch qtype {
	case dns.TypeCNAME, dns.TypeMX, dns.TypeSRV, dns.TypePTR, dns.TypeNS:
		// For these types, we need to store the FQDN version for proper matching
		normalizedTarget = dns.Fqdn(target)
	}

	c.Lock()
	defer c.Unlock()

	// Ensure zone exists
	zone := c.getOrCreateZoneUnlocked(zoneName)

	zone.Lock()
	defer zone.Unlock()

	// Initialize domain records if needed
	if zone.Records[name] == nil {
		zone.Records[name] = make(map[uint16][]dns.RR)
	}

	zone.Records[name][qtype] = append(zone.Records[name][qtype], rr)

	// Track record ownership using structured reference
	recordRef := RecordRef{
		Zone:   zoneName,
		Name:   name,
		Type:   qtype,
		Target: normalizedTarget,
	}
	c.endpointRecords[endpointKey] = append(c.endpointRecords[endpointKey], recordRef)

	log.Debugf("Added record: %s %s -> %s (endpoint: %s)", name, dns.TypeToString[qtype], rr.String(), endpointKey)

	// NOTE: Serial updates are now handled atomically at the endpoint level
	// Do not update zone serial here
}

// AddRecord adds a DNS record to the cache
func (c *DNSCache) AddRecord(name string, qtype uint16, rr dns.RR) {
	name = getRecordName(name)
	zoneName := getZoneName(name)

	c.Lock()
	defer c.Unlock()

	// Ensure zone exists
	zone := c.getOrCreateZoneUnlocked(zoneName)

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

// UpdateRecordsByEndpointWithAddition performs a differential update that ensures DNS availability
// It adds new records first, then removes only obsolete ones, without updating zone serials
func (c *DNSCache) UpdateRecordsByEndpointWithAddition(endpointKey string, newRecords []RecordRef, plugin *ExternalDNS, createPTR bool) {
	c.Lock()
	existingRecords, hasExisting := c.endpointRecords[endpointKey]
	c.Unlock()

	if !hasExisting {
		// No existing records, add all new ones
		for _, ref := range newRecords {
			plugin.addRecordFromRef(ref, endpointKey)
		}

		c.Lock()
		c.endpointRecords[endpointKey] = newRecords
		c.Unlock()

		return
	}

	// Create sets for efficient comparison
	existingSet := make(map[RecordRef]bool)
	for _, record := range existingRecords {
		existingSet[record] = true
	}

	newSet := make(map[RecordRef]bool)
	for _, record := range newRecords {
		newSet[record] = true
	}

	// Find records to add (exist in new but not in old)
	var toAdd []RecordRef
	for _, record := range newRecords {
		if !existingSet[record] {
			toAdd = append(toAdd, record)
		}
	}

	// Find records to remove (exist in old but not in new)
	var toRemove []RecordRef
	for _, record := range existingRecords {
		if !newSet[record] {
			toRemove = append(toRemove, record)
		}
	}

	log.Debugf("Endpoint %s: adding %d new records, keeping %d unchanged records, removing %d old records",
		endpointKey, len(toAdd), len(existingRecords)-len(toRemove), len(toRemove))

	// CRITICAL: Add new records FIRST to ensure DNS availability
	for _, ref := range toAdd {
		plugin.addRecordFromRef(ref, endpointKey)
	}

	// Only after new records are added, remove the obsolete ones
	c.removeSpecificRecords(toRemove)

	// Update tracking with new records
	c.Lock()
	c.endpointRecords[endpointKey] = newRecords
	c.Unlock()
}

// UpdateRecordsByEndpoint performs a differential update of records for a DNSEndpoint
// This minimizes DNS downtime by only removing records that are no longer needed
func (c *DNSCache) UpdateRecordsByEndpoint(endpointKey string, newRecords []RecordRef) {
	c.Lock()
	existingRecords, hasExisting := c.endpointRecords[endpointKey]
	c.Unlock()

	if !hasExisting {
		// No existing records, just update tracking
		c.Lock()
		c.endpointRecords[endpointKey] = newRecords
		c.Unlock()
		return
	}

	// Create sets for efficient comparison
	existingSet := make(map[RecordRef]bool)
	for _, record := range existingRecords {
		existingSet[record] = true
	}

	newSet := make(map[RecordRef]bool)
	for _, record := range newRecords {
		newSet[record] = true
	}

	// Find records to remove (exist in old but not in new)
	var toRemove []RecordRef
	for _, record := range existingRecords {
		if !newSet[record] {
			toRemove = append(toRemove, record)
		}
	}

	log.Debugf("Endpoint %s: keeping %d unchanged records, removing %d old records",
		endpointKey, len(existingRecords)-len(toRemove), len(toRemove))

	// Remove only the records that are no longer needed
	// New records are added through the normal AddRecordWithEndpoint flow
	c.removeSpecificRecords(toRemove)

	// Update tracking with new records
	c.Lock()
	c.endpointRecords[endpointKey] = newRecords
	c.Unlock()
}

// RemoveRecordsByEndpoint removes all records associated with a specific DNSEndpoint (used for deletions)
func (c *DNSCache) RemoveRecordsByEndpoint(endpointKey string) []RecordRef {
	c.Lock()
	recordRefs, exists := c.endpointRecords[endpointKey]
	if !exists {
		c.Unlock()
		return nil
	}

	// Remove the endpoint tracking immediately to free memory
	delete(c.endpointRecords, endpointKey)
	c.Unlock()

	log.Debugf("Removing %d records for endpoint %s", len(recordRefs), endpointKey)

	// Remove all records
	c.removeSpecificRecords(recordRefs)

	return recordRefs
}

// removeSpecificRecords removes only the specified records without updating zone serials
func (c *DNSCache) removeSpecificRecords(recordsToRemove []RecordRef) {
	// Group records by zone to minimize lock operations
	zoneGroups := make(map[string][]RecordRef)
	for _, ref := range recordsToRemove {
		zoneGroups[ref.Zone] = append(zoneGroups[ref.Zone], ref)
	}

	// Process each zone separately
	for zoneName, refs := range zoneGroups {
		c.RLock()
		zone, exists := c.zones[zoneName]
		c.RUnlock()

		if !exists {
			continue
		}

		zone.Lock()

		// Remove specific records for this zone
		for _, ref := range refs {
			if zone.Records[ref.Name] != nil {
				records := zone.Records[ref.Name][ref.Type]
				for i, rr := range records {
					if c.recordMatches(rr, ref.Target) {
						zone.Records[ref.Name][ref.Type] = append(records[:i], records[i+1:]...)
						log.Debugf("Removed record: %s %s -> %s", ref.Name, dns.TypeToString[ref.Type], ref.Target)
						break
					}
				}

				// Clean up empty entries
				if len(zone.Records[ref.Name][ref.Type]) == 0 {
					delete(zone.Records[ref.Name], ref.Type)
				}
				if len(zone.Records[ref.Name]) == 0 {
					delete(zone.Records, ref.Name)
				}
			}
		}

		// NOTE: Serial updates are now handled atomically at the endpoint level
		// Do not update zone serial here
		zone.Unlock()
	}
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

	for zoneName, zone := range c.zones {
		zone.RLock()
		total := 0
		for _, types := range zone.Records {
			for _, records := range types {
				total += len(records)
			}
		}
		zone.RUnlock()

		// Update the gauge for each zone - using "coredns" as server label since we don't have server context here
		externalDNSCacheSize.WithLabelValues("coredns", zoneName).Set(float64(total))
	}
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
	fqdn := dns.Fqdn(name)
	labels := dns.SplitDomainName(fqdn)
	if len(labels) <= 1 {
		return fqdn // Root zone or invalid
	}
	return dns.Fqdn(strings.Join(labels[1:], "."))
}

// getOrCreateZone gets an existing zone or creates it if it doesn't exist
func (c *DNSCache) getOrCreateZone(zoneName string) *Zone {
	c.Lock()
	defer c.Unlock()

	return c.getOrCreateZoneUnlocked(zoneName)
}

// getOrCreateZoneUnlocked gets an existing zone or creates it if it doesn't exist (assumes lock is held)
func (c *DNSCache) getOrCreateZoneUnlocked(zoneName string) *Zone {
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

// updateZoneSerial updates the serial number for a zone with a specific serial
func (c *DNSCache) updateZoneSerial(zoneName string, serial uint32) {
	c.RLock()
	zone, exists := c.zones[zoneName]
	c.RUnlock()

	if exists && zone != nil {
		zone.Lock()
		zone.Serial = serial
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

	// If transfer is not loaded, we'll see these, answer with refused (no transfer allowed).
	if qtype == dns.TypeAXFR || qtype == dns.TypeIXFR {
		return dns.RcodeRefused, nil
	}

	// Increment request counter
	zoneName := getZoneName(qname)
	externalDNSRequestCount.WithLabelValues(server, "udp", dns.TypeToString[qtype], zoneName).Inc()

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

	err := w.WriteMsg(m)
	if err != nil {
		log.Errorf("Failed to write DNS response: %v", err)
		return dns.RcodeServerFailure, err
	}

	return dns.RcodeSuccess, nil
}

// Name implements the plugin.Handler interface
func (e *ExternalDNS) Name() string { return "externaldns" }

// Transfer implements the transfer.Transfer interface for AXFR support
func (e *ExternalDNS) Transfer(zone string, serial uint32) (<-chan []dns.RR, error) {
	log.Debugf("AXFR requested for zone: %s, serial: %d", zone, serial)

	zoneName := dns.Fqdn(zone)

	// Ensure zone exists
	e.cache.getOrCreateZone(zoneName)

	e.cache.RLock()
	records := e.cache.getZoneRecordsForAXFR(zoneName)
	e.cache.RUnlock()

	if len(records) == 0 {
		log.Debugf("Zone %s not found for AXFR", zoneName)
		return nil, transfer.ErrNotAuthoritative
	}

	// First SOA must appear at the start
	soa := records[0]

	// Create channel and send records
	ch := make(chan []dns.RR)
	go func() {
		defer close(ch)

		if serial != 0 && soa.(*dns.SOA).Serial == serial { // ixfr fallback, only send SOA
			log.Debugf("Zone %s serial %d matches requested serial %d, sending only SOA", zoneName, soa.(*dns.SOA).Serial, serial)
			ch <- []dns.RR{soa}

			return
		}

		log.Debugf("Sending AXFR for zone %s with %d records", zoneName, len(records))

		// Send all records, starting with SOA
		ch <- records

		// And AXFR must end with SOA again
		ch <- []dns.RR{soa}
		log.Debugf("Completed AXFR for zone %s", zoneName)
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
		zoneInfo = e.cache.getOrCreateZone(zoneName)
	}

	zoneInfo.RLock()
	serial := zoneInfo.Serial
	zoneInfo.RUnlock()

	log.Debugf("Serial for zone %s: %d", zoneName, serial)
	return serial
}

// Start starts watching DNSEndpoint resources
func (e *ExternalDNS) Start(ctx context.Context) error {
	// Use in-cluster configuration (service account based)
	config, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("failed to create in-cluster kubernetes config: %v", err)
	}

	e.client, err = dynamic.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create kubernetes client: %v", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create kubernetes clientset: %v", err)
	}
	e.coreClient = clientset.CoreV1()

	// Set default ConfigMap namespace if not configured
	if e.configMapNamespace == "" {
		e.configMapNamespace = e.namespace
		if e.configMapNamespace == "" {
			e.configMapNamespace = "default"
		}
	}

	// Load existing zone serials from ConfigMap
	if err := e.loadZoneSerials(ctx); err != nil {
		return fmt.Errorf("failed to load zone serials: %v", err)
	}

	// Start watching DNSEndpoint resources
	go e.watchDNSEndpoints()

	log.Info("ExternalDNS plugin started with service account authentication")
	return nil
}

// loadZoneSerials loads zone serials from ConfigMap
func (e *ExternalDNS) loadZoneSerials(ctx context.Context) error {
	if e.coreClient == nil {
		// Not initialized, skip loading
		e.serialsMutex.Lock()
		if e.zoneSerials == nil {
			e.zoneSerials = make(map[string]uint32)
		}
		e.serialsMutex.Unlock()
		return nil
	}

	cm, err := e.coreClient.ConfigMaps(e.configMapNamespace).Get(ctx, e.configMapName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Create empty ConfigMap
			cm = &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      e.configMapName,
					Namespace: e.configMapNamespace,
				},
				Data: map[string]string{},
			}
			cm, err = e.coreClient.ConfigMaps(e.configMapNamespace).Create(ctx, cm, metav1.CreateOptions{})
			if err != nil {
				// Handle race condition: another replica might have created it
				if apierrors.IsAlreadyExists(err) {
					// Try to get it again
					cm, err = e.coreClient.ConfigMaps(e.configMapNamespace).Get(ctx, e.configMapName, metav1.GetOptions{})
					if err != nil {
						return fmt.Errorf("failed to get ConfigMap after creation conflict: %v", err)
					}
				} else {
					return fmt.Errorf("failed to create ConfigMap: %v", err)
				}
			}
		} else {
			return fmt.Errorf("failed to get ConfigMap: %v", err)
		}
	}

	e.serialsMutex.Lock()
	e.zoneSerials = make(map[string]uint32)
	for zone, serialStr := range cm.Data {
		if serial, err := strconv.ParseUint(serialStr, 10, 32); err == nil {
			e.zoneSerials[zone] = uint32(serial)
			// Update cache with loaded serial
			e.cache.updateZoneSerial(zone, uint32(serial))
		} else {
			log.Warningf("Invalid serial value for zone %s: %s", zone, serialStr)
		}
	}
	e.serialsResourceVersion = cm.ResourceVersion
	e.serialsMutex.Unlock()

	log.Infof("Loaded %d zone serials from ConfigMap", len(e.zoneSerials))
	return nil
}

// saveZoneSerials saves zone serials to ConfigMap
func (e *ExternalDNS) saveZoneSerials(ctx context.Context) {
	if e.coreClient == nil {
		// Not initialized, skip saving (e.g., in tests)
		return
	}

	e.serialsMutex.RLock()
	data := make(map[string]string)
	for zone, serial := range e.zoneSerials {
		data[zone] = strconv.FormatUint(uint64(serial), 10)
	}
	rv := e.serialsResourceVersion
	e.serialsMutex.RUnlock()

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:            e.configMapName,
			Namespace:       e.configMapNamespace,
			ResourceVersion: rv,
		},
		Data: data,
	}

	_, err := e.coreClient.ConfigMaps(e.configMapNamespace).Update(ctx, cm, metav1.UpdateOptions{})
	if err != nil {
		if apierrors.IsConflict(err) {
			log.Warningf("ConfigMap update conflict, reloading serials")
			// Reload serials to get latest
			if reloadErr := e.loadZoneSerials(ctx); reloadErr != nil {
				log.Errorf("Failed to reload serials after conflict: %v", reloadErr)
			}
		} else {
			log.Errorf("Failed to update ConfigMap: %v", err)
		}

		return
	}

	// Notify others
	if e.transfer != nil {
		for zone := range data {
			if err := e.transfer.Notify(zone); err != nil {
				log.Warningf("Failed to notify transfer of serial update for zone %s: %v", zone, err)
			}
		}
	}

	log.Debugf("Updated ConfigMap with %d zone serials", len(data))
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

	resourceInterface := e.client.Resource(dnsEndpointGVR).Namespace(e.namespace)

	// Initial sync - get all existing DNSEndpoints
	list, err := resourceInterface.List(e.ctx, metav1.ListOptions{})
	if err != nil {
		log.Errorf("Failed to list DNSEndpoints: %v", err)
		return
	}

	log.Infof("Initial sync: found %d DNSEndpoints", len(list.Items))
	for _, item := range list.Items {
		e.processDNSEndpoint(e.ctx, &item, "ADDED")
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
				e.processDNSEndpoint(e.ctx, obj, string(event.Type))
			}
		}
	}
}

func unstructuredToDNSEndpoint(obj *unstructured.Unstructured) (*externaldnsv1alpha1.DNSEndpoint, error) {
	b, err := obj.MarshalJSON()
	if err != nil {
		return nil, err
	}

	var dnsEndpoint externaldnsv1alpha1.DNSEndpoint
	err = json.Unmarshal(b, &dnsEndpoint)
	return &dnsEndpoint, err
}

// processDNSEndpoint processes a DNSEndpoint object
func (e *ExternalDNS) processDNSEndpoint(ctx context.Context, obj *unstructured.Unstructured, eventType string) {
	log.Debugf("Processing DNSEndpoint %s/%s (event: %s)",
		obj.GetNamespace(), obj.GetName(), eventType)

	dnsEndpoint, err := unstructuredToDNSEndpoint(obj)
	if err != nil {
		log.Errorf("Failed to convert to DNSEndpoint: %v", err)
		return
	}

	// Generate consistent serial number based on CR metadata
	// This ensures all CoreDNS instances generate the same serial for the same CR state
	consistentSerial := generateConsistentSerial(dnsEndpoint)
	log.Debugf("Generated consistent serial %d for DNSEndpoint %s/%s (resourceVersion: %s, generation: %d)",
		consistentSerial, dnsEndpoint.GetNamespace(), dnsEndpoint.GetName(), dnsEndpoint.GetResourceVersion(), dnsEndpoint.GetGeneration())

	// Check for PTR record creation annotation
	createPTR := false
	if annotations := dnsEndpoint.GetAnnotations(); annotations != nil {
		if val, exists := annotations["coredns-externaldns.ionos.cloud/create-ptr"]; exists {
			createPTR = (val == "true" || val == "1")
		}
	}

	// Increment endpoint event metric
	externalDNSEndpointEvents.WithLabelValues("coredns", eventType).Inc()

	switch eventType {
	case "ADDED":
		endpointKey := fmt.Sprintf("%s/%s", dnsEndpoint.GetNamespace(), dnsEndpoint.GetName())

		// For new endpoints, add records without serial updates during processing
		var addedRecordRefs []RecordRef
		for _, ep := range dnsEndpoint.Spec.Endpoints {
			refs := e.addEndpointToCache(ep, createPTR, endpointKey)
			addedRecordRefs = append(addedRecordRefs, refs...)
		}

		// Update zone serials ONCE at the end for all affected zones
		e.updateZoneSerialsForRecords(ctx, addedRecordRefs, consistentSerial)

		log.Debugf("Added cache with %d endpoints from %s/%s (createPTR: %v, serial: %d)",
			len(dnsEndpoint.Spec.Endpoints), dnsEndpoint.GetNamespace(), dnsEndpoint.GetName(), createPTR, consistentSerial)

	case "MODIFIED":
		endpointKey := fmt.Sprintf("%s/%s", dnsEndpoint.GetNamespace(), dnsEndpoint.GetName())

		// For modifications, we need to be careful about DNS availability
		// First collect what records SHOULD exist (without adding them yet)
		var newRecordRefs []RecordRef
		for _, ep := range dnsEndpoint.Spec.Endpoints {
			refs := e.collectRecordRefs(ep, createPTR)
			newRecordRefs = append(newRecordRefs, refs...)
		}

		// Now perform the differential update which:
		// 1. Adds any new records that don't exist
		// 2. Removes only records that are no longer needed
		// 3. Keeps unchanged records untouched
		// 4. Does NOT update zone serials during processing
		e.cache.UpdateRecordsByEndpointWithAddition(endpointKey, newRecordRefs, e, createPTR)

		// Update zone serials ONCE at the end for all affected zones
		// This ensures slaves see atomic updates and don't request AXFR mid-processing
		e.updateZoneSerialsForRecords(ctx, newRecordRefs, consistentSerial)

		log.Debugf("Modified cache with %d endpoints from %s/%s (createPTR: %v, serial: %d)",
			len(dnsEndpoint.Spec.Endpoints), dnsEndpoint.GetNamespace(), dnsEndpoint.GetName(), createPTR, consistentSerial)

	case "DELETED":
		// Remove all records for this DNSEndpoint
		endpointKey := fmt.Sprintf("%s/%s", dnsEndpoint.GetNamespace(), dnsEndpoint.GetName())
		deletedRecords := e.cache.RemoveRecordsByEndpoint(endpointKey)

		// Update zone serials ONCE at the end for all affected zones
		e.updateZoneSerialsForRecords(ctx, deletedRecords, consistentSerial)

		log.Debugf("Removed %d records for deleted DNSEndpoint %s/%s",
			len(deletedRecords), dnsEndpoint.GetNamespace(), dnsEndpoint.GetName())
	}
}

// addEndpointToCache adds an endpoint to the DNS cache without updating serials
// Returns the RecordRef references for the added records
func (e *ExternalDNS) addEndpointToCache(ep *endpoint.Endpoint, createPTR bool, endpointKey string) []RecordRef {
	var refs []RecordRef

	if ep.DNSName == "" || len(ep.Targets) == 0 {
		return refs
	}

	qtype := e.recordTypeToQType(ep.RecordType)
	if qtype == 0 {
		log.Warningf("Unsupported record type: %s", ep.RecordType)
		return refs
	}

	ttl := e.ttl
	if ep.RecordTTL > 0 {
		ttl = uint32(ep.RecordTTL)
	}

	for _, target := range ep.Targets {
		rr := e.createDNSRecord(ep.DNSName, qtype, ttl, target)
		if rr != nil {
			// Add record without serial update
			e.cache.AddRecordWithEndpoint(ep.DNSName, qtype, rr, endpointKey, target)

			// Collect reference for the main record
			normalizedTarget := target
			switch qtype {
			case dns.TypeCNAME, dns.TypeMX, dns.TypeSRV, dns.TypePTR, dns.TypeNS:
				normalizedTarget = dns.Fqdn(target)
			}

			ref := RecordRef{
				Zone:   getZoneName(getRecordName(ep.DNSName)),
				Name:   getRecordName(ep.DNSName),
				Type:   qtype,
				Target: normalizedTarget,
			}
			refs = append(refs, ref)

			// Create PTR records for A and AAAA records if annotation is present
			if createPTR && (qtype == dns.TypeA || qtype == dns.TypeAAAA) {
				ptrRefs := e.createAndAddPTRRecordWithRefs(ep.DNSName, target, ttl, endpointKey)
				refs = append(refs, ptrRefs...)
			}
		}
	}

	return refs
}

// collectRecordRefs collects RecordRef references for an endpoint without adding to cache
func (e *ExternalDNS) collectRecordRefs(ep *endpoint.Endpoint, createPTR bool) []RecordRef {
	refs := make([]RecordRef, 0)

	// Handle main record
	dnsName := dns.Fqdn(ep.DNSName)

	switch ep.RecordType {
	case "A":
		for _, target := range ep.Targets {
			ip := net.ParseIP(target)
			if ip == nil || ip.To4() == nil {
				continue
			}
			refs = append(refs, RecordRef{
				Zone:   getZoneName(getRecordName(ep.DNSName)),
				Name:   dnsName,
				Type:   dns.TypeA,
				Target: target,
			})
		}
	case "AAAA":
		for _, target := range ep.Targets {
			ip := net.ParseIP(target)
			if ip == nil || ip.To4() != nil {
				continue
			}
			refs = append(refs, RecordRef{
				Zone:   getZoneName(getRecordName(ep.DNSName)),
				Name:   dnsName,
				Type:   dns.TypeAAAA,
				Target: target,
			})
		}
	case "CNAME":
		if len(ep.Targets) > 0 {
			refs = append(refs, RecordRef{
				Zone:   getZoneName(getRecordName(ep.DNSName)),
				Name:   dnsName,
				Type:   dns.TypeCNAME,
				Target: dns.Fqdn(ep.Targets[0]),
			})
		}
	}

	// Handle PTR records if enabled
	if createPTR && ep.RecordType != "PTR" {
		for _, target := range ep.Targets {
			ip := net.ParseIP(target)
			if ip == nil {
				continue
			}

			var ptrName string
			if ip.To4() != nil {
				// IPv4
				ptrName, _ = dns.ReverseAddr(ip.String())
			} else {
				// IPv6
				ptrName, _ = dns.ReverseAddr(ip.String())
			}

			refs = append(refs, RecordRef{
				Zone:   getZoneName(ptrName),
				Name:   ptrName,
				Type:   dns.TypePTR,
				Target: dnsName,
			})
		}
	}

	return refs
}

// updateZoneSerialsForRecords updates the serial for all zones affected by the given records
func (e *ExternalDNS) updateZoneSerialsForRecords(ctx context.Context, records []RecordRef, proposedSerial uint32) {
	// Collect unique zones from the records
	affectedZones := make(map[string]bool)
	for _, ref := range records {
		affectedZones[ref.Zone] = true
	}

	e.serialsMutex.Lock()
	// Initialize map if not already done
	if e.zoneSerials == nil {
		e.zoneSerials = make(map[string]uint32)
	}

	for zoneName := range affectedZones {
		if proposedSerial > e.zoneSerials[zoneName] {
			// Use the proposed serial if it's higher
			e.zoneSerials[zoneName] = proposedSerial
		} else {
			e.zoneSerials[zoneName]++
		}
		e.cache.updateZoneSerial(zoneName, e.zoneSerials[zoneName])
		log.Debugf("Updated serial for zone %s to %d (new high serial)", zoneName, e.zoneSerials[zoneName])
	}
	e.serialsMutex.Unlock()

	go e.saveZoneSerials(ctx)
}

// addRecordFromRef adds a single record to the cache based on a RecordRef
func (e *ExternalDNS) addRecordFromRef(ref RecordRef, endpointKey string) {
	// Create the DNS record
	rr := e.createDNSRecordFromRef(ref)
	if rr != nil {
		e.cache.AddRecordWithEndpoint(ref.Name, ref.Type, rr, endpointKey, ref.Target)
	}
}

// createDNSRecordFromRef creates a DNS record from a RecordRef
func (e *ExternalDNS) createDNSRecordFromRef(ref RecordRef) dns.RR {
	header := dns.RR_Header{
		Name:   ref.Name,
		Rrtype: ref.Type,
		Class:  dns.ClassINET,
		Ttl:    e.ttl,
	}

	switch ref.Type {
	case dns.TypeA:
		return &dns.A{
			Hdr: header,
			A:   net.ParseIP(ref.Target),
		}
	case dns.TypeAAAA:
		return &dns.AAAA{
			Hdr:  header,
			AAAA: net.ParseIP(ref.Target),
		}
	case dns.TypeCNAME:
		return &dns.CNAME{
			Hdr:    header,
			Target: ref.Target,
		}
	case dns.TypePTR:
		return &dns.PTR{
			Hdr: header,
			Ptr: ref.Target,
		}
	case dns.TypeNS:
		return &dns.NS{
			Hdr: header,
			Ns:  ref.Target,
		}
	default:
		log.Warningf("Unsupported record type in ref: %s", dns.TypeToString[ref.Type])
		return nil
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
		if _, err := fmt.Sscanf(parts[0], "%d", &priority); err != nil {
			log.Warningf("Invalid MX priority '%s': %v", parts[0], err)
			return nil
		}
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
		if _, err := fmt.Sscanf(parts[0], "%d", &priority); err != nil {
			log.Warningf("Invalid SRV priority '%s': %v", parts[0], err)
			return nil
		}
		if _, err := fmt.Sscanf(parts[1], "%d", &weight); err != nil {
			log.Warningf("Invalid SRV weight '%s': %v", parts[1], err)
			return nil
		}
		if _, err := fmt.Sscanf(parts[2], "%d", &port); err != nil {
			log.Warningf("Invalid SRV port '%s': %v", parts[2], err)
			return nil
		}
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

// createAndAddPTRRecordWithRefs creates PTR record and returns its reference
func (e *ExternalDNS) createAndAddPTRRecordWithRefs(hostname, ipAddr string, ttl uint32, endpointKey string) []RecordRef {
	var refs []RecordRef

	ptrName := e.createReverseDNSName(ipAddr)
	if ptrName == "" {
		return refs
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

	normalizedPtrName := getRecordName(ptrName)
	e.cache.AddRecordWithEndpoint(ptrName, dns.TypePTR, ptrRecord, endpointKey, dns.Fqdn(hostname))

	// Add reference for the PTR record
	ref := RecordRef{
		Zone:   getZoneName(normalizedPtrName),
		Name:   normalizedPtrName,
		Type:   dns.TypePTR,
		Target: dns.Fqdn(hostname),
	}
	refs = append(refs, ref)

	log.Debugf("Created PTR record: %s -> %s", ptrName, hostname)
	return refs
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
