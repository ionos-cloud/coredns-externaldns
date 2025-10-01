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

// getZoneName extracts zone name from a domain name
func getZoneName(name string) string {
	name = normalizeName(name)
	parts := dns.SplitDomainName(name)
	if len(parts) >= 2 {
		return dns.Fqdn(strings.Join(parts[len(parts)-2:], "."))
	}
	return name
}

// AddRecord adds a DNS record to the cache
func (c *Cache) AddRecord(name string, qtype uint16, rr dns.RR) {
	name = normalizeName(name)
	zoneName := getZoneName(name)

	c.mu.Lock()
	zone := c.getOrCreateZone(zoneName)
	c.mu.Unlock()

	zone.mu.Lock()
	defer zone.mu.Unlock()

	if zone.Records[name] == nil {
		zone.Records[name] = make(map[uint16][]dns.RR)
	}

	// Check for duplicates to avoid unnecessary memory usage
	for _, existing := range zone.Records[name][qtype] {
		if existing.String() == rr.String() {
			return // Record already exists
		}
	}

	zone.Records[name][qtype] = append(zone.Records[name][qtype], rr)

	// Update metrics after releasing the zone lock to avoid deadlock
	go c.updateMetrics()
}

// GetRecords retrieves DNS records from cache
func (c *Cache) GetRecords(name string, qtype uint16) []dns.RR {
	name = normalizeName(name)
	zoneName := getZoneName(name)

	c.mu.RLock()
	zone, exists := c.zones[zoneName]
	c.mu.RUnlock()

	if !exists {
		return c.findWildcardRecords(name, qtype)
	}

	zone.mu.RLock()
	defer zone.mu.RUnlock()

	if records, ok := zone.Records[name][qtype]; ok {
		result := make([]dns.RR, len(records))
		copy(result, records)
		return result
	}

	return c.findWildcardRecords(name, qtype)
}

// findWildcardRecords finds wildcard matches for a query
func (c *Cache) findWildcardRecords(name string, qtype uint16) []dns.RR {
	parts := dns.SplitDomainName(name)

	// Try wildcard patterns from most specific to least specific
	for i := 1; i < len(parts); i++ {
		wildcard := "*." + strings.Join(parts[i:], ".")
		wildcard = normalizeName(wildcard)
		zoneName := getZoneName(wildcard)

		c.mu.RLock()
		zone, exists := c.zones[zoneName]
		c.mu.RUnlock()

		if !exists {
			continue
		}

		zone.mu.RLock()
		if records, ok := zone.Records[wildcard][qtype]; ok {
			zone.mu.RUnlock()
			// Return records with the queried name, not wildcard
			result := make([]dns.RR, len(records))
			for i, rr := range records {
				newRR := dns.Copy(rr)
				newRR.Header().Name = name
				result[i] = newRR
			}
			return result
		}
		zone.mu.RUnlock()
	}

	return nil
}

// RemoveRecord removes a DNS record from cache
func (c *Cache) RemoveRecord(name string, qtype uint16, target string) bool {
	name = normalizeName(name)
	zoneName := getZoneName(name)

	c.mu.RLock()
	zone, exists := c.zones[zoneName]
	c.mu.RUnlock()

	if !exists {
		return false
	}

	zone.mu.Lock()
	defer zone.mu.Unlock()

	records, ok := zone.Records[name][qtype]
	if !ok {
		return false
	}

	for i, rr := range records {
		if c.recordMatches(rr, qtype, target) {
			// Remove record by swapping with last and truncating
			records[i] = records[len(records)-1]
			zone.Records[name][qtype] = records[:len(records)-1]

			// Clean up empty structures
			if len(zone.Records[name][qtype]) == 0 {
				delete(zone.Records[name], qtype)
				if len(zone.Records[name]) == 0 {
					delete(zone.Records, name)
				}
			}

			// Update metrics after releasing the zone lock to avoid deadlock
			go c.updateMetrics()
			return true
		}
	}

	return false
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
