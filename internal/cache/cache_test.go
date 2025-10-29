package cache

import (
	"net"
	"testing"

	"github.com/miekg/dns"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Helper function to create test metrics
func createTestMetrics() *Metrics {
	return &Metrics{
		CacheSize: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "test_cache_size",
				Help: "Test cache size",
			},
			[]string{"zone"},
		),
		RequestCount: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "test_requests_total",
				Help: "Test request count",
			},
			[]string{"type"},
		),
		EndpointEvents: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "test_endpoint_events_total",
				Help: "Test endpoint events",
			},
			[]string{"action"},
		),
	}
}

func TestNewCache(t *testing.T) {
	metrics := createTestMetrics()
	cache := New(metrics)

	require.NotNil(t, cache)
	assert.NotNil(t, cache.zones)
	assert.Equal(t, metrics, cache.metrics)
	assert.Len(t, cache.zones, 0)
}

func TestAddRecordBasic(t *testing.T) {
	cache := New(createTestMetrics())

	// Create test A record
	aRecord := &dns.A{
		Hdr: dns.RR_Header{
			Name:   "test.example.com.",
			Rrtype: dns.TypeA,
			Class:  dns.ClassINET,
			Ttl:    300,
		},
		A: net.ParseIP("192.168.1.1"),
	}

	// This should not hang
	cache.AddRecord("test.example.com", "example.com.", dns.TypeA, aRecord)

	// Verify record was added
	records := cache.GetRecords("test.example.com.", "example.com.", dns.TypeA)
	assert.Len(t, records, 1)
}

func TestGetZones(t *testing.T) {
	cache := New(createTestMetrics())

	// Add a simple record
	aRecord := &dns.A{
		Hdr: dns.RR_Header{
			Name:   "www.example.com.",
			Rrtype: dns.TypeA,
			Class:  dns.ClassINET,
			Ttl:    300,
		},
		A: net.ParseIP("192.168.1.1"),
	}
	cache.AddRecord("www.example.com", "example.com.", dns.TypeA, aRecord)

	zones := cache.GetZones()
	assert.Contains(t, zones, "example.com.")
}

func TestDomainExists(t *testing.T) {
	cache := New(createTestMetrics())

	// Add A record for www.example.com
	aRecord := &dns.A{
		Hdr: dns.RR_Header{
			Name:   "www.example.com.",
			Rrtype: dns.TypeA,
			Class:  dns.ClassINET,
			Ttl:    300,
		},
		A: net.ParseIP("192.168.1.1"),
	}
	cache.AddRecord("www.example.com", "example.com.", dns.TypeA, aRecord)

	// Add CNAME record for alias.example.com
	cnameRecord := &dns.CNAME{
		Hdr: dns.RR_Header{
			Name:   "alias.example.com.",
			Rrtype: dns.TypeCNAME,
			Class:  dns.ClassINET,
			Ttl:    300,
		},
		Target: "www.example.com.",
	}
	cache.AddRecord("alias.example.com", "example.com.", dns.TypeCNAME, cnameRecord)

	t.Run("Domain exists with A record", func(t *testing.T) {
		exists := cache.DomainExists("www.example.com", "example.com.")
		assert.True(t, exists, "www.example.com should exist in cache")
	})

	t.Run("Domain exists with CNAME record", func(t *testing.T) {
		exists := cache.DomainExists("alias.example.com", "example.com.")
		assert.True(t, exists, "alias.example.com should exist in cache")
	})

	t.Run("Domain does not exist", func(t *testing.T) {
		exists := cache.DomainExists("nonexistent.example.com", "example.com.")
		assert.False(t, exists, "nonexistent.example.com should not exist in cache")
	})

	t.Run("Domain does not exist in wrong zone", func(t *testing.T) {
		exists := cache.DomainExists("www.example.com", "wrong.com.")
		assert.False(t, exists, "www.example.com should not exist in wrong.com. zone")
	})

	t.Run("Domain does not exist when zone doesn't exist", func(t *testing.T) {
		exists := cache.DomainExists("test.nonexistent.com", "nonexistent.com.")
		assert.False(t, exists, "Should return false when zone doesn't exist")
	})

	t.Run("Domain exists is case insensitive", func(t *testing.T) {
		exists := cache.DomainExists("WWW.EXAMPLE.COM", "EXAMPLE.COM.")
		assert.True(t, exists, "Domain check should be case insensitive")
	})

	t.Run("Domain with trailing dot", func(t *testing.T) {
		exists := cache.DomainExists("www.example.com.", "example.com.")
		assert.True(t, exists, "Should handle domain with trailing dot")
	})
}

func TestDomainExistsAfterRemoval(t *testing.T) {
	cache := New(createTestMetrics())

	// Add an A record
	aRecord := &dns.A{
		Hdr: dns.RR_Header{
			Name:   "test.example.com.",
			Rrtype: dns.TypeA,
			Class:  dns.ClassINET,
			Ttl:    300,
		},
		A: net.ParseIP("192.168.1.1"),
	}
	cache.AddRecord("test.example.com", "example.com.", dns.TypeA, aRecord)

	// Verify domain exists
	exists := cache.DomainExists("test.example.com", "example.com.")
	assert.True(t, exists, "Domain should exist after adding record")

	// Remove the record
	removed := cache.RemoveRecord("test.example.com", "example.com.", dns.TypeA, "192.168.1.1")
	assert.True(t, removed, "Record should be removed")

	// Verify domain no longer exists
	exists = cache.DomainExists("test.example.com", "example.com.")
	assert.False(t, exists, "Domain should not exist after removing all records")
}

func TestDomainExistsWithMultipleRecordTypes(t *testing.T) {
	cache := New(createTestMetrics())

	// Add both A and AAAA records for the same domain
	aRecord := &dns.A{
		Hdr: dns.RR_Header{
			Name:   "multi.example.com.",
			Rrtype: dns.TypeA,
			Class:  dns.ClassINET,
			Ttl:    300,
		},
		A: net.ParseIP("192.168.1.1"),
	}
	cache.AddRecord("multi.example.com", "example.com.", dns.TypeA, aRecord)

	aaaaRecord := &dns.AAAA{
		Hdr: dns.RR_Header{
			Name:   "multi.example.com.",
			Rrtype: dns.TypeAAAA,
			Class:  dns.ClassINET,
			Ttl:    300,
		},
		AAAA: net.ParseIP("2001:db8::1"),
	}
	cache.AddRecord("multi.example.com", "example.com.", dns.TypeAAAA, aaaaRecord)

	// Domain should exist
	exists := cache.DomainExists("multi.example.com", "example.com.")
	assert.True(t, exists, "Domain should exist with multiple record types")

	// Remove one record type
	cache.RemoveRecord("multi.example.com", "example.com.", dns.TypeA, "192.168.1.1")

	// Domain should still exist (has AAAA record)
	exists = cache.DomainExists("multi.example.com", "example.com.")
	assert.True(t, exists, "Domain should still exist with remaining record type")

	// Remove the other record type
	cache.RemoveAllRecords("multi.example.com", "example.com.", dns.TypeAAAA)

	// Now domain should not exist
	exists = cache.DomainExists("multi.example.com", "example.com.")
	assert.False(t, exists, "Domain should not exist after removing all record types")
}
