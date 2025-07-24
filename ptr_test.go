package externaldns

import (
	"testing"

	"github.com/miekg/dns"
)

func TestCreateReverseDNSName(t *testing.T) {
	e := &ExternalDNS{}

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "IPv4 address",
			input:    "192.168.1.10",
			expected: "10.1.168.192.in-addr.arpa.",
		},
		{
			name:     "IPv6 address",
			input:    "2001:db8::1",
			expected: "1.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.8.b.d.0.1.0.0.2.ip6.arpa.",
		},
		{
			name:     "Invalid IP",
			input:    "invalid",
			expected: "",
		},
		{
			name:     "IPv4 loopback",
			input:    "127.0.0.1",
			expected: "1.0.0.127.in-addr.arpa.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := e.createReverseDNSName(tt.input)
			if result != tt.expected {
				t.Errorf("createReverseDNSName(%s) = %s, want %s", tt.input, result, tt.expected)
			}
		})
	}
}

func TestCreateAndAddPTRRecord(t *testing.T) {
	e := &ExternalDNS{
		cache: NewDNSCache(),
		debug: true,
	}

	hostname := "test.example.com"
	ipAddr := "192.168.1.10"
	ttl := uint32(300)

	// Create PTR record
	e.createAndAddPTRRecord(hostname, ipAddr, ttl)

	// Verify PTR record was added to cache
	ptrName := "10.1.168.192.in-addr.arpa."
	records := e.cache.GetRecords(ptrName, dns.TypePTR)

	if len(records) != 1 {
		t.Fatalf("Expected 1 PTR record, got %d", len(records))
	}

	ptrRecord, ok := records[0].(*dns.PTR)
	if !ok {
		t.Fatalf("Expected PTR record, got %T", records[0])
	}

	if ptrRecord.Ptr != dns.Fqdn(hostname) {
		t.Errorf("Expected PTR target %s, got %s", dns.Fqdn(hostname), ptrRecord.Ptr)
	}

	if ptrRecord.Hdr.Ttl != ttl {
		t.Errorf("Expected TTL %d, got %d", ttl, ptrRecord.Hdr.Ttl)
	}
}

func TestPTRRecordCreationForIPv6(t *testing.T) {
	e := &ExternalDNS{
		cache: NewDNSCache(),
		debug: true,
	}

	hostname := "ipv6.example.com"
	ipAddr := "2001:db8::1"
	ttl := uint32(300)

	// Create PTR record
	e.createAndAddPTRRecord(hostname, ipAddr, ttl)

	// Verify PTR record was added to cache
	ptrName := "1.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.8.b.d.0.1.0.0.2.ip6.arpa."
	records := e.cache.GetRecords(ptrName, dns.TypePTR)

	if len(records) != 1 {
		t.Fatalf("Expected 1 PTR record, got %d", len(records))
	}

	ptrRecord, ok := records[0].(*dns.PTR)
	if !ok {
		t.Fatalf("Expected PTR record, got %T", records[0])
	}

	if ptrRecord.Ptr != dns.Fqdn(hostname) {
		t.Errorf("Expected PTR target %s, got %s", dns.Fqdn(hostname), ptrRecord.Ptr)
	}
}
