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
	cache.AddRecord("test.example.com", dns.TypeA, aRecord)

	// Verify record was added
	records := cache.GetRecords("test.example.com.", dns.TypeA)
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
	cache.AddRecord("www.example.com", dns.TypeA, aRecord)

	zones := cache.GetZones()
	assert.Contains(t, zones, "example.com.")
}
