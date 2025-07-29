package externaldns

import (
	"testing"
	"time"

	"github.com/miekg/dns"
	"sigs.k8s.io/external-dns/endpoint"
)

func TestDifferentialUpdate(t *testing.T) {
	e := &ExternalDNS{
		cache: NewDNSCache(time.Minute),
		ttl:   300,
	}
	defer e.cache.Stop()

	endpointKey := "default/test-endpoint"

	// Initial state: Add some records using the normal flow (simulating ADDED event)
	ep1 := &endpoint.Endpoint{
		DNSName:    "app.example.com",
		RecordType: "A",
		Targets:    []string{"192.168.1.1", "192.168.1.2"},
		RecordTTL:  300,
	}

	ep2 := &endpoint.Endpoint{
		DNSName:    "api.example.com",
		RecordType: "A",
		Targets:    []string{"192.168.2.1"},
		RecordTTL:  300,
	}

	// Add initial records using normal method (ADDED)
	e.addEndpointToCache(ep1, false, endpointKey)
	e.addEndpointToCache(ep2, false, endpointKey)

	// Track the initial records manually (this simulates what processDNSEndpoint does for ADDED)
	initialRefs := make([]RecordRef, 0)
	initialRefs = append(initialRefs, e.collectRecordRefs(ep1, false)...)
	initialRefs = append(initialRefs, e.collectRecordRefs(ep2, false)...)
	e.cache.Lock()
	e.cache.endpointRecords[endpointKey] = initialRefs
	e.cache.Unlock()

	// Verify initial state
	appRecords := e.cache.GetRecords("app.example.com.", dns.TypeA)
	if len(appRecords) != 2 {
		t.Fatalf("Expected 2 A records for app.example.com, got %d", len(appRecords))
	}

	apiRecords := e.cache.GetRecords("api.example.com.", dns.TypeA)
	if len(apiRecords) != 1 {
		t.Fatalf("Expected 1 A record for api.example.com, got %d", len(apiRecords))
	}

	// Now simulate a MODIFIED event: Change app.example.com targets and remove api.example.com
	modifiedEp1 := &endpoint.Endpoint{
		DNSName:    "app.example.com",
		RecordType: "A",
		Targets:    []string{"192.168.1.1", "192.168.1.3"}, // Keep .1, change .2 to .3
		RecordTTL:  300,
	}

	// Add new endpoint
	newEp := &endpoint.Endpoint{
		DNSName:    "web.example.com",
		RecordType: "CNAME",
		Targets:    []string{"app.example.com"},
		RecordTTL:  300,
	}

	// Now simulate the proper MODIFIED flow: collect what should exist, then update
	newRefs := make([]RecordRef, 0)
	newRefs = append(newRefs, e.collectRecordRefs(modifiedEp1, false)...)
	newRefs = append(newRefs, e.collectRecordRefs(newEp, false)...)

	// Use the new method that ensures DNS availability during updates
	e.cache.UpdateRecordsByEndpointWithAddition(endpointKey, newRefs, e, false)

	// Verify the differential update worked correctly

	// app.example.com should have 2 records: .1 and .3
	appRecordsAfter := e.cache.GetRecords("app.example.com.", dns.TypeA)
	if len(appRecordsAfter) != 2 {
		t.Fatalf("Expected 2 A records for app.example.com after update, got %d", len(appRecordsAfter))
	}

	// Check that we have .1 and .3, not .2
	hasOne := false
	hasThree := false
	hasTwo := false

	for _, rr := range appRecordsAfter {
		if aRecord, ok := rr.(*dns.A); ok {
			ip := aRecord.A.String()
			switch ip {
			case "192.168.1.1":
				hasOne = true
			case "192.168.1.2":
				hasTwo = true
			case "192.168.1.3":
				hasThree = true
			}
		}
	}

	if !hasOne {
		t.Error("Expected to keep 192.168.1.1 record")
	}
	if hasTwo {
		t.Error("Expected to remove 192.168.1.2 record")
	}
	if !hasThree {
		t.Error("Expected to add 192.168.1.3 record")
	}

	// api.example.com should be completely removed
	apiRecordsAfter := e.cache.GetRecords("api.example.com.", dns.TypeA)
	if len(apiRecordsAfter) != 0 {
		t.Fatalf("Expected 0 A records for api.example.com after update, got %d", len(apiRecordsAfter))
	}

	// web.example.com should be added
	webRecords := e.cache.GetRecords("web.example.com.", dns.TypeCNAME)
	if len(webRecords) != 1 {
		t.Fatalf("Expected 1 CNAME record for web.example.com, got %d", len(webRecords))
	}

	// Verify tracking is updated correctly
	if len(e.cache.endpointRecords[endpointKey]) != len(newRefs) {
		t.Fatalf("Expected %d tracked records, got %d", len(newRefs), len(e.cache.endpointRecords[endpointKey]))
	}
}

func TestContinuousAvailabilityDuringUpdate(t *testing.T) {
	e := &ExternalDNS{
		cache: NewDNSCache(time.Minute),
		ttl:   300,
	}
	defer e.cache.Stop()

	endpointKey := "default/test-endpoint"

	// Initial state: One record
	ep := &endpoint.Endpoint{
		DNSName:    "service.example.com",
		RecordType: "A",
		Targets:    []string{"192.168.1.100"},
		RecordTTL:  300,
	}

	// Add initial record
	_ = e.addEndpointToCache(ep, false, endpointKey)

	// Verify initial state
	records := e.cache.GetRecords("service.example.com.", dns.TypeA)
	if len(records) != 1 {
		t.Fatalf("Expected 1 A record initially, got %d", len(records))
	}

	// Create modified endpoint with same name but different target
	modifiedEp := &endpoint.Endpoint{
		DNSName:    "service.example.com",
		RecordType: "A",
		Targets:    []string{"192.168.1.200"},
		RecordTTL:  300,
	}

	// Add new record (this happens first in real scenario)
	newRefs := e.addEndpointToCache(modifiedEp, false, endpointKey)

	// At this point, both records should exist temporarily
	recordsDuringUpdate := e.cache.GetRecords("service.example.com.", dns.TypeA)
	if len(recordsDuringUpdate) != 2 {
		t.Fatalf("Expected 2 A records during update (old + new), got %d", len(recordsDuringUpdate))
	}

	// Now perform differential update (removes old, keeps new)
	e.cache.UpdateRecordsByEndpoint(endpointKey, newRefs)

	// After update, should only have the new record
	recordsAfter := e.cache.GetRecords("service.example.com.", dns.TypeA)
	if len(recordsAfter) != 1 {
		t.Fatalf("Expected 1 A record after update, got %d", len(recordsAfter))
	}

	// Verify it's the new record
	if aRecord, ok := recordsAfter[0].(*dns.A); ok {
		if aRecord.A.String() != "192.168.1.200" {
			t.Errorf("Expected new record 192.168.1.200, got %s", aRecord.A.String())
		}
	} else {
		t.Error("Expected A record type")
	}
}
