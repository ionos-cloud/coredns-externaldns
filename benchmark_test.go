package externaldns

import (
	"fmt"
	"testing"
	"time"

	"sigs.k8s.io/external-dns/endpoint"
)

func BenchmarkEndpointCleanup(b *testing.B) {
	e := &ExternalDNS{
		cache: NewDNSCache(time.Minute),
		ttl:   300,
	}
	defer e.cache.Stop()

	// Pre-populate with many endpoints
	numEndpoints := 100
	recordsPerEndpoint := 10

	for i := 0; i < numEndpoints; i++ {
		endpointKey := fmt.Sprintf("default/endpoint-%d", i)

		for j := 0; j < recordsPerEndpoint; j++ {
			ep := &endpoint.Endpoint{
				DNSName:    fmt.Sprintf("test-%d-%d.example.com", i, j),
				RecordType: "A",
				Targets:    []string{fmt.Sprintf("192.168.%d.%d", i%256, j%256)},
				RecordTTL:  300,
			}
			e.addEndpointToCache(ep, false, endpointKey)
		}
	}

	b.ResetTimer()

	// Benchmark cleanup operations
	for i := 0; i < b.N; i++ {
		endpointKey := fmt.Sprintf("default/endpoint-%d", i%numEndpoints)
		e.clearDNSEndpointRecords("default", fmt.Sprintf("endpoint-%d", i%numEndpoints))

		// Re-add the endpoint for next iteration
		if i < b.N-1 {
			for j := 0; j < recordsPerEndpoint; j++ {
				ep := &endpoint.Endpoint{
					DNSName:    fmt.Sprintf("test-%d-%d.example.com", i%numEndpoints, j),
					RecordType: "A",
					Targets:    []string{fmt.Sprintf("192.168.%d.%d", (i%numEndpoints)%256, j%256)},
					RecordTTL:  300,
				}
				e.addEndpointToCache(ep, false, endpointKey)
			}
		}
	}
}

func BenchmarkRecordTracking(b *testing.B) {
	e := &ExternalDNS{
		cache: NewDNSCache(time.Minute),
		ttl:   300,
	}
	defer e.cache.Stop()

	ep := &endpoint.Endpoint{
		DNSName:    "test.example.com",
		RecordType: "A",
		Targets:    []string{"192.168.1.1"},
		RecordTTL:  300,
	}

	// Benchmark adding and tracking records
	for i := 0; b.Loop(); i++ {
		endpointKey := fmt.Sprintf("default/endpoint-%d", i)
		e.addEndpointToCache(ep, false, endpointKey)
	}
}
