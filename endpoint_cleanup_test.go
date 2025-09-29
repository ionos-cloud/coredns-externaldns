package externaldns

import (
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/external-dns/endpoint"
)

func TestClearDNSEndpointRecords(t *testing.T) {
	e := &ExternalDNS{
		cache: NewDNSCache(time.Minute),
		ttl:   300,
	}
	defer e.cache.Stop()

	// Create some test endpoints
	ep1 := &endpoint.Endpoint{
		DNSName:    "test1.example.com",
		RecordType: "A",
		Targets:    []string{"192.168.1.1", "192.168.1.2"},
		RecordTTL:  300,
	}

	ep2 := &endpoint.Endpoint{
		DNSName:    "test2.example.com",
		RecordType: "A",
		Targets:    []string{"192.168.2.1"},
		RecordTTL:  300,
	}

	ep3 := &endpoint.Endpoint{
		DNSName:    "test3.example.com",
		RecordType: "CNAME",
		Targets:    []string{"target.example.com"},
		RecordTTL:  300,
	}

	// Add endpoints to cache with different endpoint keys
	e.addEndpointToCache(ep1, false, "default/endpoint1")
	e.addEndpointToCache(ep2, false, "default/endpoint2")
	e.addEndpointToCache(ep3, false, "default/endpoint1") // Same endpoint key as ep1

	// Verify records were added
	records1 := e.cache.GetRecords("test1.example.com.", dns.TypeA)
	require.Len(t, records1, 2, "Expected 2 A records for test1.example.com")

	records2 := e.cache.GetRecords("test2.example.com.", dns.TypeA)
	require.Len(t, records2, 1, "Expected 1 A record for test2.example.com")

	records3 := e.cache.GetRecords("test3.example.com.", dns.TypeCNAME)
	require.Len(t, records3, 1, "Expected 1 CNAME record for test3.example.com")

	// Verify endpoint tracking
	require.Len(t, e.cache.endpointRecords["default/endpoint1"], 3, "Expected 3 tracked records for default/endpoint1")

	require.Len(t, e.cache.endpointRecords["default/endpoint2"], 1, "Expected 1 tracked record for default/endpoint2")

	// Clear records for endpoint1
	e.cache.RemoveRecordsByEndpoint("default/endpoint1")

	// Verify endpoint1 records were removed
	records1After := e.cache.GetRecords("test1.example.com.", dns.TypeA)
	require.Len(t, records1After, 0, "Expected 0 A records for test1.example.com after cleanup")

	records3After := e.cache.GetRecords("test3.example.com.", dns.TypeCNAME)
	require.Len(t, records3After, 0, "Expected 0 CNAME records for test3.example.com after cleanup")

	// Verify endpoint2 records are still there
	records2After := e.cache.GetRecords("test2.example.com.", dns.TypeA)
	require.Len(t, records2After, 1, "Expected 1 A record for test2.example.com after cleanup")

	// Verify endpoint tracking was cleaned up
	_, exists := e.cache.endpointRecords["default/endpoint1"]
	require.False(t, exists, "Expected default/endpoint1 to be removed from tracking")

	require.Len(t, e.cache.endpointRecords["default/endpoint2"], 1, "Expected 1 tracked record for default/endpoint2 after cleanup")
}

func TestClearDNSEndpointRecordsWithPTR(t *testing.T) {
	e := &ExternalDNS{
		cache: NewDNSCache(time.Minute),
		ttl:   300,
	}
	defer e.cache.Stop()

	// Create endpoint with PTR record creation
	ep := &endpoint.Endpoint{
		DNSName:    "test.example.com",
		RecordType: "A",
		Targets:    []string{"192.168.1.10"},
		RecordTTL:  300,
	}

	// Add endpoint to cache with PTR creation enabled
	e.addEndpointToCache(ep, true, "default/ptr-endpoint")

	// Verify A record was added
	aRecords := e.cache.GetRecords("test.example.com.", dns.TypeA)
	require.Len(t, aRecords, 1, "Expected 1 A record")

	// Verify PTR record was added
	ptrRecords := e.cache.GetRecords("10.1.168.192.in-addr.arpa.", dns.TypePTR)
	require.Len(t, ptrRecords, 1, "Expected 1 PTR record")

	// Verify endpoint tracking includes both records
	require.Len(t, e.cache.endpointRecords["default/ptr-endpoint"], 2, "Expected 2 tracked records (A + PTR)")

	// Clear the endpoint
	e.cache.RemoveRecordsByEndpoint("default/ptr-endpoint")

	// Verify both A and PTR records were removed
	aRecordsAfter := e.cache.GetRecords("test.example.com.", dns.TypeA)
	require.Len(t, aRecordsAfter, 0, "Expected 0 A records after cleanup")

	ptrRecordsAfter := e.cache.GetRecords("10.1.168.192.in-addr.arpa.", dns.TypePTR)
	require.Len(t, ptrRecordsAfter, 0, "Expected 0 PTR records after cleanup")

	// Verify endpoint tracking was cleaned up
	_, exists := e.cache.endpointRecords["default/ptr-endpoint"]
	require.False(t, exists, "Expected default/ptr-endpoint to be removed from tracking")
}
