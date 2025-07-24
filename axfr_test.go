package externaldns

import (
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestAXFRSupport(t *testing.T) {
	e := &ExternalDNS{
		cache: NewDNSCache(time.Minute),
		ttl:   300,
	}

	// Add some test records
	aRecord := &dns.A{
		Hdr: dns.RR_Header{
			Name:   "test.example.com.",
			Rrtype: dns.TypeA,
			Class:  dns.ClassINET,
			Ttl:    300,
		},
		A: []byte{192, 168, 1, 10},
	}

	cnameRecord := &dns.CNAME{
		Hdr: dns.RR_Header{
			Name:   "www.example.com.",
			Rrtype: dns.TypeCNAME,
			Class:  dns.ClassINET,
			Ttl:    300,
		},
		Target: "test.example.com.",
	}

	e.cache.AddRecord("test.example.com.", dns.TypeA, aRecord)
	e.cache.AddRecord("www.example.com.", dns.TypeCNAME, cnameRecord)

	// Test Serial method
	serial := e.Serial("example.com.")
	if serial == 0 {
		t.Errorf("Expected non-zero serial, got %d", serial)
	}

	// Test Transfer method
	ch, err := e.Transfer("example.com.", 0)
	if err != nil {
		t.Fatalf("Transfer failed: %v", err)
	}

	// Read from the channel
	records := <-ch
	if len(records) < 2 { // At least SOA + our records
		t.Errorf("Expected at least 2 records, got %d", len(records))
	}

	// Check if SOA record is present
	var soaFound bool
	for _, rr := range records {
		if rr.Header().Rrtype == dns.TypeSOA {
			soaFound = true
			break
		}
	}
	if !soaFound {
		t.Error("SOA record not found in AXFR response")
	}
}

func TestZoneManagement(t *testing.T) {
	cache := NewDNSCache(time.Minute)

	// Test zone creation
	zone := cache.ensureZone("example.com.")
	if zone == nil {
		t.Fatal("Failed to create zone")
	}

	if zone.Name != "example.com." {
		t.Errorf("Expected zone name 'example.com.', got '%s'", zone.Name)
	}

	// Test zone serial update
	originalSerial := zone.Serial

	// Sleep a bit to ensure different timestamp
	time.Sleep(1 * time.Second)

	cache.updateZoneSerial("example.com.")

	cache.RLock()
	updatedZone := cache.zones["example.com."]
	cache.RUnlock()

	updatedZone.RLock()
	newSerial := updatedZone.Serial
	updatedZone.RUnlock()

	if newSerial <= originalSerial {
		t.Errorf("Expected serial to increase from %d, got %d", originalSerial, newSerial)
	}
}

func TestGetZoneName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{
			input:    "test.example.com.",
			expected: "example.com.",
		},
		{
			input:    "www.test.example.com.",
			expected: "example.com.",
		},
		{
			input:    "example.com.",
			expected: "example.com.",
		},
		{
			input:    "single",
			expected: "single",
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := getZoneName(tt.input)
			if result != tt.expected {
				t.Errorf("getZoneName(%s) = %s, want %s", tt.input, result, tt.expected)
			}
		})
	}
}

func TestRebuildZoneRecords(t *testing.T) {
	cache := NewDNSCache(time.Minute)

	// Add some records
	aRecord := &dns.A{
		Hdr: dns.RR_Header{
			Name:   "test.example.com.",
			Rrtype: dns.TypeA,
			Class:  dns.ClassINET,
			Ttl:    300,
		},
		A: []byte{192, 168, 1, 10},
	}

	cache.AddRecord("test.example.com.", dns.TypeA, aRecord)

	cache.RLock()
	zone := cache.zones["example.com."]
	cache.RUnlock()

	zone.RLock()
	recordCount := 0
	for _, types := range zone.Records {
		for _, records := range types {
			recordCount += len(records)
		}
	}
	zone.RUnlock()

	// Should have at least 1 A record
	if recordCount < 1 {
		t.Errorf("Expected at least 1 record, got %d", recordCount)
	}
}
