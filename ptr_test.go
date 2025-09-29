package externaldns

import (
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/require"
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
			require.Equal(t, tt.expected, result, "createReverseDNSName(%s)", tt.input)
		})
	}
}

func TestCreateAndAddPTRRecord(t *testing.T) {
	e := &ExternalDNS{
		cache: NewDNSCache(time.Minute),
	}

	hostname := "test.example.com"
	ipAddr := "192.168.1.10"
	ttl := uint32(300)

	// Create PTR record
	e.createAndAddPTRRecordWithRefs(hostname, ipAddr, ttl, "test/endpoint")

	// Verify PTR record was added to cache
	ptrName := "10.1.168.192.in-addr.arpa."
	records := e.cache.GetRecords(ptrName, dns.TypePTR)

	require.Len(t, records, 1, "Expected 1 PTR record")

	ptrRecord, ok := records[0].(*dns.PTR)
	require.True(t, ok, "Expected PTR record type")

	require.Equal(t, dns.Fqdn(hostname), ptrRecord.Ptr, "Expected PTR target to match")
	require.Equal(t, ttl, ptrRecord.Hdr.Ttl, "Expected TTL to match")
}

func TestPTRRecordCreationForIPv6(t *testing.T) {
	e := &ExternalDNS{
		cache: NewDNSCache(time.Minute),
	}

	hostname := "ipv6.example.com"
	ipAddr := "2001:db8::1"
	ttl := uint32(300)

	// Create PTR record
	e.createAndAddPTRRecordWithRefs(hostname, ipAddr, ttl, "test/ipv6-endpoint")

	// Verify PTR record was added to cache
	ptrName := "1.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.8.b.d.0.1.0.0.2.ip6.arpa."
	records := e.cache.GetRecords(ptrName, dns.TypePTR)

	require.Len(t, records, 1, "Expected 1 PTR record")
	ptrRecord, ok := records[0].(*dns.PTR)
	require.True(t, ok, "Expected PTR record type")
	require.Equal(t, dns.Fqdn(hostname), ptrRecord.Ptr, "Expected PTR target to match")
}
