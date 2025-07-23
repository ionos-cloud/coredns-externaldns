package externaldns

import (
	"net"
	"testing"

	"github.com/miekg/dns"
)

func TestDNSCache(t *testing.T) {
	cache := NewDNSCache()

	// Test adding and retrieving A records
	aRecord := &dns.A{
		Hdr: dns.RR_Header{
			Name:   "test.example.com.",
			Rrtype: dns.TypeA,
			Class:  dns.ClassINET,
			Ttl:    300,
		},
		A: net.ParseIP("192.168.1.1"),
	}

	cache.AddRecord("test.example.com", dns.TypeA, aRecord)
	records := cache.GetRecords("test.example.com", dns.TypeA)

	if len(records) != 1 {
		t.Errorf("Expected 1 record, got %d", len(records))
	}

	if records[0].(*dns.A).A.String() != "192.168.1.1" {
		t.Errorf("Expected 192.168.1.1, got %s", records[0].(*dns.A).A.String())
	}

	// Test case insensitive lookup
	records = cache.GetRecords("TEST.EXAMPLE.COM", dns.TypeA)
	if len(records) != 1 {
		t.Errorf("Case insensitive lookup failed, expected 1 record, got %d", len(records))
	}

	// Test removing records
	cache.RemoveRecord("test.example.com", dns.TypeA, "192.168.1.1")
	records = cache.GetRecords("test.example.com", dns.TypeA)
	if len(records) != 0 {
		t.Errorf("Expected 0 records after removal, got %d", len(records))
	}
}

func TestRecordTypeToQType(t *testing.T) {
	ed := &ExternalDNS{}

	tests := []struct {
		recordType string
		expected   uint16
	}{
		{"A", dns.TypeA},
		{"AAAA", dns.TypeAAAA},
		{"CNAME", dns.TypeCNAME},
		{"MX", dns.TypeMX},
		{"TXT", dns.TypeTXT},
		{"SRV", dns.TypeSRV},
		{"PTR", dns.TypePTR},
		{"NS", dns.TypeNS},
		{"SOA", dns.TypeSOA},
		{"UNKNOWN", 0},
	}

	for _, test := range tests {
		result := ed.recordTypeToQType(test.recordType)
		if result != test.expected {
			t.Errorf("recordTypeToQType(%s) = %d, expected %d", test.recordType, result, test.expected)
		}
	}
}

func TestCreateDNSRecord(t *testing.T) {
	ed := &ExternalDNS{}

	// Test A record creation
	rr := ed.createDNSRecord("test.example.com", dns.TypeA, 300, "192.168.1.1")
	if rr == nil {
		t.Fatal("Expected A record, got nil")
	}
	aRecord, ok := rr.(*dns.A)
	if !ok {
		t.Fatal("Expected *dns.A type")
	}
	if aRecord.A.String() != "192.168.1.1" {
		t.Errorf("Expected 192.168.1.1, got %s", aRecord.A.String())
	}

	// Test CNAME record creation
	rr = ed.createDNSRecord("www.example.com", dns.TypeCNAME, 300, "example.com")
	if rr == nil {
		t.Fatal("Expected CNAME record, got nil")
	}
	cnameRecord, ok := rr.(*dns.CNAME)
	if !ok {
		t.Fatal("Expected *dns.CNAME type")
	}
	if cnameRecord.Target != "example.com." {
		t.Errorf("Expected example.com., got %s", cnameRecord.Target)
	}

	// Test MX record creation
	rr = ed.createDNSRecord("example.com", dns.TypeMX, 300, "10 mail.example.com")
	if rr == nil {
		t.Fatal("Expected MX record, got nil")
	}
	mxRecord, ok := rr.(*dns.MX)
	if !ok {
		t.Fatal("Expected *dns.MX type")
	}
	if mxRecord.Preference != 10 {
		t.Errorf("Expected preference 10, got %d", mxRecord.Preference)
	}
	if mxRecord.Mx != "mail.example.com." {
		t.Errorf("Expected mail.example.com., got %s", mxRecord.Mx)
	}
}

func TestRecordMatches(t *testing.T) {
	cache := &DNSCache{}

	// Test A record matching
	aRecord := &dns.A{
		Hdr: dns.RR_Header{
			Name:   "test.example.com.",
			Rrtype: dns.TypeA,
			Class:  dns.ClassINET,
			Ttl:    300,
		},
		A: net.ParseIP("192.168.1.1"),
	}

	if !cache.recordMatches(aRecord, "192.168.1.1") {
		t.Error("A record should match IP address")
	}

	if cache.recordMatches(aRecord, "192.168.1.2") {
		t.Error("A record should not match different IP address")
	}

	// Test CNAME record matching
	cnameRecord := &dns.CNAME{
		Hdr: dns.RR_Header{
			Name:   "www.example.com.",
			Rrtype: dns.TypeCNAME,
			Class:  dns.ClassINET,
			Ttl:    300,
		},
		Target: "example.com.",
	}

	if !cache.recordMatches(cnameRecord, "example.com.") {
		t.Error("CNAME record should match target")
	}

	if cache.recordMatches(cnameRecord, "other.com.") {
		t.Error("CNAME record should not match different target")
	}
}

func TestServeDNS(t *testing.T) {
	ed := &ExternalDNS{
		cache: NewDNSCache(),
		ttl:   300,
		debug: false,
	}

	// Add a test record to cache
	aRecord := &dns.A{
		Hdr: dns.RR_Header{
			Name:   "test.example.com.",
			Rrtype: dns.TypeA,
			Class:  dns.ClassINET,
			Ttl:    300,
		},
		A: net.ParseIP("192.168.1.1"),
	}
	ed.cache.AddRecord("test.example.com", dns.TypeA, aRecord)

	// Test that records were added to cache
	records := ed.cache.GetRecords("test.example.com", dns.TypeA)
	if len(records) != 1 {
		t.Errorf("Expected 1 record in cache, got %d", len(records))
	}

	if records[0].(*dns.A).A.String() != "192.168.1.1" {
		t.Errorf("Expected 192.168.1.1 in cache, got %s", records[0].(*dns.A).A.String())
	}

	// Test cache size
	cacheSize := ed.cache.GetCacheSize()
	if cacheSize != 1 {
		t.Errorf("Expected cache size 1, got %d", cacheSize)
	}

	// Test Name method
	if ed.Name() != "externaldns" {
		t.Errorf("Expected plugin name 'externaldns', got %s", ed.Name())
	}
}

// TestCacheMetrics tests that cache size metrics are properly updated
func TestCacheMetrics(t *testing.T) {
	cache := NewDNSCache()

	// Initial cache should be empty
	if cache.GetCacheSize() != 0 {
		t.Errorf("Expected empty cache, got size %d", cache.GetCacheSize())
	}

	// Add some records
	aRecord1 := &dns.A{
		Hdr: dns.RR_Header{
			Name:   "test1.example.com.",
			Rrtype: dns.TypeA,
			Class:  dns.ClassINET,
			Ttl:    300,
		},
		A: net.ParseIP("192.168.1.1"),
	}

	aRecord2 := &dns.A{
		Hdr: dns.RR_Header{
			Name:   "test2.example.com.",
			Rrtype: dns.TypeA,
			Class:  dns.ClassINET,
			Ttl:    300,
		},
		A: net.ParseIP("192.168.1.2"),
	}

	cnameRecord := &dns.CNAME{
		Hdr: dns.RR_Header{
			Name:   "www.example.com.",
			Rrtype: dns.TypeCNAME,
			Class:  dns.ClassINET,
			Ttl:    300,
		},
		Target: "test1.example.com.",
	}

	cache.AddRecord("test1.example.com", dns.TypeA, aRecord1)
	if cache.GetCacheSize() != 1 {
		t.Errorf("Expected cache size 1, got %d", cache.GetCacheSize())
	}

	cache.AddRecord("test2.example.com", dns.TypeA, aRecord2)
	if cache.GetCacheSize() != 2 {
		t.Errorf("Expected cache size 2, got %d", cache.GetCacheSize())
	}

	cache.AddRecord("www.example.com", dns.TypeCNAME, cnameRecord)
	if cache.GetCacheSize() != 3 {
		t.Errorf("Expected cache size 3, got %d", cache.GetCacheSize())
	}

	// Remove a record
	cache.RemoveRecord("test1.example.com", dns.TypeA, "192.168.1.1")
	if cache.GetCacheSize() != 2 {
		t.Errorf("Expected cache size 2 after removal, got %d", cache.GetCacheSize())
	}

	// Clear all records for a domain
	cache.ClearRecords("test2.example.com")
	if cache.GetCacheSize() != 1 {
		t.Errorf("Expected cache size 1 after clearing domain, got %d", cache.GetCacheSize())
	}
}
