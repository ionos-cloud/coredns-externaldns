package cache

import (
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
	"github.com/prometheus/client_golang/prometheus"
)

// Cache represents a high-performance in-memory DNS cache
type Cache struct {
	mu      sync.RWMutex
	zones   map[string]*Zone
	metrics *Metrics
}

// Zone represents a DNS zone with its records
type Zone struct {
	mu      sync.RWMutex
	Name    string
	Serial  uint32
	Records map[string]map[uint16][]dns.RR // domain -> type -> records
}

// Record represents a DNS record reference for tracking
type Record struct {
	Zone   string
	Name   string
	Type   uint16
	Target string
	TTL    uint32
}

// Metrics holds cache metrics
type Metrics struct {
	CacheSize      *prometheus.GaugeVec
	RequestCount   *prometheus.CounterVec
	EndpointEvents *prometheus.CounterVec
}

// New creates a new DNS cache
func New(metrics *Metrics) *Cache {
	return &Cache{
		zones:   make(map[string]*Zone),
		metrics: metrics,
	}
}

// normalizeName normalizes DNS names to lowercase FQDN
func normalizeName(name string) string {
	return dns.Fqdn(strings.ToLower(name))
}

// AddRecord adds a DNS record to the cache with the specified zone
func (c *Cache) AddRecord(name string, zone string, qtype uint16, rr dns.RR) {
	name = normalizeName(name)
	zoneName := dns.Fqdn(strings.ToLower(zone))

	c.mu.Lock()
	zoneObj := c.getOrCreateZone(zoneName)
	c.mu.Unlock()

	zoneObj.mu.Lock()
	defer zoneObj.mu.Unlock()

	if zoneObj.Records[name] == nil {
		zoneObj.Records[name] = make(map[uint16][]dns.RR)
	}

	// Check for duplicates to avoid unnecessary memory usage
	for _, existing := range zoneObj.Records[name][qtype] {
		if existing.String() == rr.String() {
			return // Record already exists
		}
	}

	zoneObj.Records[name][qtype] = append(zoneObj.Records[name][qtype], rr)

	// Update metrics after releasing the zone lock to avoid deadlock
	go c.updateMetrics()
}

// GetRecords retrieves DNS records from cache
func (c *Cache) GetRecords(name string, zone string, qtype uint16) []dns.RR {
	name = normalizeName(name)
	zoneName := dns.Fqdn(strings.ToLower(zone))

	c.mu.RLock()
	zoneObj, exists := c.zones[zoneName]
	c.mu.RUnlock()

	if !exists {
		return c.findWildcardRecords(name, zone, qtype)
	}

	zoneObj.mu.RLock()
	defer zoneObj.mu.RUnlock()

	if records, ok := zoneObj.Records[name][qtype]; ok {
		result := make([]dns.RR, len(records))
		copy(result, records)
		return result
	}

	return c.findWildcardRecords(name, zone, qtype)
}

// findWildcardRecords finds wildcard matches for a query
func (c *Cache) findWildcardRecords(name string, zone string, qtype uint16) []dns.RR {
	parts := dns.SplitDomainName(name)
	zoneName := dns.Fqdn(strings.ToLower(zone))

	c.mu.RLock()
	zoneObj, exists := c.zones[zoneName]
	c.mu.RUnlock()

	if !exists {
		return nil
	}

	// Try wildcard patterns from most specific to least specific
	for i := 1; i < len(parts); i++ {
		wildcard := "*." + strings.Join(parts[i:], ".")
		wildcard = normalizeName(wildcard)

		zoneObj.mu.RLock()
		if records, ok := zoneObj.Records[wildcard][qtype]; ok {
			zoneObj.mu.RUnlock()
			// Return records with the queried name, not wildcard
			result := make([]dns.RR, len(records))
			for i, rr := range records {
				newRR := dns.Copy(rr)
				newRR.Header().Name = name
				result[i] = newRR
			}
			return result
		}
		zoneObj.mu.RUnlock()
	}

	return nil
}

// RemoveRecord removes a DNS record from cache
func (c *Cache) RemoveRecord(name string, zone string, qtype uint16, target string) bool {
	name = normalizeName(name)
	zoneName := dns.Fqdn(strings.ToLower(zone))

	c.mu.RLock()
	zoneObj, exists := c.zones[zoneName]
	c.mu.RUnlock()

	if !exists {
		return false
	}

	zoneObj.mu.Lock()
	defer zoneObj.mu.Unlock()

	records, ok := zoneObj.Records[name][qtype]
	if !ok {
		return false
	}

	for i, rr := range records {
		if c.recordMatches(rr, qtype, target) {
			// Remove record by swapping with last and truncating
			records[i] = records[len(records)-1]
			zoneObj.Records[name][qtype] = records[:len(records)-1]

			// Clean up empty structures
			if len(zoneObj.Records[name][qtype]) == 0 {
				delete(zoneObj.Records[name], qtype)
				if len(zoneObj.Records[name]) == 0 {
					delete(zoneObj.Records, name)
				}
			}

			// Update metrics after releasing the zone lock to avoid deadlock
			go c.updateMetrics()
			return true
		}
	}

	return false
}

// RemoveAllRecords removes all DNS records for a given name and type from cache
func (c *Cache) RemoveAllRecords(name string, zone string, qtype uint16) bool {
	name = normalizeName(name)
	zoneName := dns.Fqdn(strings.ToLower(zone))

	c.mu.RLock()
	zoneObj, exists := c.zones[zoneName]
	c.mu.RUnlock()

	if !exists {
		return false
	}

	zoneObj.mu.Lock()
	defer zoneObj.mu.Unlock()

	_, ok := zoneObj.Records[name][qtype]
	if !ok {
		return false
	}

	// Remove all records of this type for this name
	delete(zoneObj.Records[name], qtype)

	// Clean up empty structures
	if len(zoneObj.Records[name]) == 0 {
		delete(zoneObj.Records, name)
	}

	// Update metrics after releasing the zone lock to avoid deadlock
	go c.updateMetrics()
	return true
}

// RemoveAllPTRRecordsForName removes all PTR records that point to a given DNS name
func (c *Cache) RemoveAllPTRRecordsForName(targetName string) {
	targetName = dns.Fqdn(targetName)

	c.mu.RLock()
	zones := make([]*Zone, 0, len(c.zones))
	for _, zone := range c.zones {
		zones = append(zones, zone)
	}
	c.mu.RUnlock()

	for _, zone := range zones {
		zone.mu.Lock()
		for name, types := range zone.Records {
			if ptrRecords, exists := types[dns.TypePTR]; exists {
				newPtrRecords := make([]dns.RR, 0, len(ptrRecords))
				for _, rr := range ptrRecords {
					if ptr, ok := rr.(*dns.PTR); ok {
						if ptr.Ptr != targetName {
							newPtrRecords = append(newPtrRecords, rr)
						}
					} else {
						newPtrRecords = append(newPtrRecords, rr)
					}
				}

				if len(newPtrRecords) != len(ptrRecords) {
					if len(newPtrRecords) == 0 {
						delete(zone.Records[name], dns.TypePTR)
						if len(zone.Records[name]) == 0 {
							delete(zone.Records, name)
						}
					} else {
						zone.Records[name][dns.TypePTR] = newPtrRecords
					}
				}
			}
		}
		zone.mu.Unlock()
	}

	// Update metrics
	go c.updateMetrics()
}

// recordMatches checks if a record matches the target
func (c *Cache) recordMatches(rr dns.RR, qtype uint16, target string) bool {
	switch qtype {
	case dns.TypeA:
		if a, ok := rr.(*dns.A); ok {
			return a.A.String() == target
		}
	case dns.TypeAAAA:
		if aaaa, ok := rr.(*dns.AAAA); ok {
			return aaaa.AAAA.String() == target
		}
	case dns.TypeCNAME:
		if cname, ok := rr.(*dns.CNAME); ok {
			return cname.Target == dns.Fqdn(target)
		}
	case dns.TypeMX:
		if mx, ok := rr.(*dns.MX); ok {
			return mx.Mx == dns.Fqdn(target)
		}
	case dns.TypeTXT:
		if txt, ok := rr.(*dns.TXT); ok {
			return strings.Join(txt.Txt, "") == target
		}
	case dns.TypePTR:
		if ptr, ok := rr.(*dns.PTR); ok {
			return ptr.Ptr == dns.Fqdn(target)
		}
	case dns.TypeNS:
		if ns, ok := rr.(*dns.NS); ok {
			return ns.Ns == dns.Fqdn(target)
		}
	}
	return false
}

// GetZoneRecords returns all records for a zone (for AXFR)
func (c *Cache) GetZoneRecords(zoneName string) map[string]map[uint16][]dns.RR {
	zoneName = normalizeName(zoneName)

	c.mu.RLock()
	zone, exists := c.zones[zoneName]
	c.mu.RUnlock()

	if !exists {
		return nil
	}

	zone.mu.RLock()
	defer zone.mu.RUnlock()

	// Deep copy to avoid concurrent access issues
	result := make(map[string]map[uint16][]dns.RR)
	for name, types := range zone.Records {
		result[name] = make(map[uint16][]dns.RR)
		for qtype, records := range types {
			result[name][qtype] = make([]dns.RR, len(records))
			copy(result[name][qtype], records)
		}
	}

	return result
}

// SetZoneSerial sets the serial number for a zone
func (c *Cache) SetZoneSerial(zoneName string, serial uint32) {
	zoneName = normalizeName(zoneName)

	c.mu.Lock()
	zone := c.getOrCreateZone(zoneName)
	c.mu.Unlock()

	zone.mu.Lock()
	zone.Serial = serial
	zone.mu.Unlock()
}

// GetZoneSerial gets the serial number for a zone
func (c *Cache) GetZoneSerial(zoneName string) uint32 {
	zoneName = normalizeName(zoneName)

	c.mu.RLock()
	zone, exists := c.zones[zoneName]
	c.mu.RUnlock()

	if !exists {
		return 0
	}

	zone.mu.RLock()
	defer zone.mu.RUnlock()
	return zone.Serial
}

// getOrCreateZone creates a zone if it doesn't exist (must be called with write lock)
func (c *Cache) getOrCreateZone(zoneName string) *Zone {
	if zone, exists := c.zones[zoneName]; exists {
		return zone
	}

	zone := &Zone{
		Name:    zoneName,
		Serial:  uint32(time.Now().Unix()),
		Records: make(map[string]map[uint16][]dns.RR),
	}
	c.zones[zoneName] = zone
	return zone
}

// updateMetrics updates cache size metrics
func (c *Cache) updateMetrics() {
	if c.metrics == nil || c.metrics.CacheSize == nil {
		return
	}

	c.mu.RLock()
	zones := make(map[string]*Zone)
	for k, v := range c.zones {
		zones[k] = v
	}
	c.mu.RUnlock()

	for zoneName, zone := range zones {
		zone.mu.RLock()
		size := len(zone.Records)
		zone.mu.RUnlock()
		c.metrics.CacheSize.WithLabelValues(zoneName).Set(float64(size))
	}
}

// GetZones returns all zone names
func (c *Cache) GetZones() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	zones := make([]string, 0, len(c.zones))
	for zone := range c.zones {
		zones = append(zones, zone)
	}
	return zones
}

// DomainExists checks if a domain has any records in the specified zone
func (c *Cache) DomainExists(name string, zone string) bool {
	name = normalizeName(name)
	zoneName := dns.Fqdn(strings.ToLower(zone))

	c.mu.RLock()
	zoneObj, exists := c.zones[zoneName]
	c.mu.RUnlock()

	if !exists {
		return false
	}

	zoneObj.mu.RLock()
	defer zoneObj.mu.RUnlock()

	// Check if the domain has any records
	if types, ok := zoneObj.Records[name]; ok && len(types) > 0 {
		return true
	}

	return false
}
