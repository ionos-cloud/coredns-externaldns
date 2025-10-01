package dnsbuilder

import (
	"testing"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRecordBuilder(t *testing.T) {
	builder := NewRecordBuilder()
	require.NotNil(t, builder)
}

func TestRecordTypeToQType(t *testing.T) {
	builder := NewRecordBuilder()

	tests := []struct {
		recordType string
		expected   uint16
	}{
		{"A", dns.TypeA},
		{"a", dns.TypeA},
		{"AAAA", dns.TypeAAAA},
		{"aaaa", dns.TypeAAAA},
		{"CNAME", dns.TypeCNAME},
		{"cname", dns.TypeCNAME},
		{"MX", dns.TypeMX},
		{"mx", dns.TypeMX},
		{"TXT", dns.TypeTXT},
		{"txt", dns.TypeTXT},
		{"SRV", dns.TypeSRV},
		{"srv", dns.TypeSRV},
		{"PTR", dns.TypePTR},
		{"ptr", dns.TypePTR},
		{"NS", dns.TypeNS},
		{"ns", dns.TypeNS},
		{"SOA", dns.TypeSOA},
		{"soa", dns.TypeSOA},
		{"INVALID", 0},
		{"", 0},
	}

	for _, test := range tests {
		result := builder.RecordTypeToQType(test.recordType)
		assert.Equal(t, test.expected, result, "Record type %s should convert to %d", test.recordType, test.expected)
	}
}

func TestCreateARecord(t *testing.T) {
	builder := NewRecordBuilder()

	tests := []struct {
		name     string
		ttl      uint32
		target   string
		expected bool
	}{
		{"test.example.com", 300, "192.168.1.1", true},
		{"test.example.com", 300, "10.0.0.1", true},
		{"test.example.com", 300, "255.255.255.255", true},
		{"test.example.com", 300, "invalid-ip", false},
		{"test.example.com", 300, "2001:db8::1", false}, // IPv6 should fail for A record
		{"test.example.com", 300, "", false},
	}

	for _, test := range tests {
		rr := builder.CreateRecord(test.name, dns.TypeA, test.ttl, test.target)
		if test.expected {
			require.NotNil(t, rr, "Expected A record for target: %s", test.target)
			aRecord, ok := rr.(*dns.A)
			require.True(t, ok, "Expected *dns.A type")
			assert.Equal(t, "test.example.com.", aRecord.Hdr.Name)
			assert.Equal(t, dns.TypeA, aRecord.Hdr.Rrtype)
			assert.Equal(t, test.ttl, aRecord.Hdr.Ttl)
			assert.Equal(t, test.target, aRecord.A.String())
		} else {
			assert.Nil(t, rr, "Expected nil A record for invalid target: %s", test.target)
		}
	}
}

func TestCreateAAAARecord(t *testing.T) {
	builder := NewRecordBuilder()

	tests := []struct {
		name     string
		ttl      uint32
		target   string
		expected bool
	}{
		{"test.example.com", 300, "2001:db8::1", true},
		{"test.example.com", 300, "::1", true},
		{"test.example.com", 300, "2001:db8:85a3::8a2e:370:7334", true},
		{"test.example.com", 300, "192.168.1.1", false}, // IPv4 should fail for AAAA
		{"test.example.com", 300, "invalid-ip", false},
		{"test.example.com", 300, "", false},
	}

	for _, test := range tests {
		rr := builder.CreateRecord(test.name, dns.TypeAAAA, test.ttl, test.target)
		if test.expected {
			require.NotNil(t, rr, "Expected AAAA record for target: %s", test.target)
			aaaaRecord, ok := rr.(*dns.AAAA)
			require.True(t, ok, "Expected *dns.AAAA type")
			assert.Equal(t, "test.example.com.", aaaaRecord.Hdr.Name)
			assert.Equal(t, dns.TypeAAAA, aaaaRecord.Hdr.Rrtype)
			assert.Equal(t, test.ttl, aaaaRecord.Hdr.Ttl)
		} else {
			assert.Nil(t, rr, "Expected nil AAAA record for invalid target: %s", test.target)
		}
	}
}

func TestCreateCNAMERecord(t *testing.T) {
	builder := NewRecordBuilder()

	rr := builder.CreateRecord("www.example.com", dns.TypeCNAME, 300, "example.com")
	require.NotNil(t, rr)

	cnameRecord, ok := rr.(*dns.CNAME)
	require.True(t, ok)
	assert.Equal(t, "www.example.com.", cnameRecord.Hdr.Name)
	assert.Equal(t, dns.TypeCNAME, cnameRecord.Hdr.Rrtype)
	assert.Equal(t, uint32(300), cnameRecord.Hdr.Ttl)
	assert.Equal(t, "example.com.", cnameRecord.Target)
}

func TestCreateMXRecord(t *testing.T) {
	builder := NewRecordBuilder()

	tests := []struct {
		name     string
		target   string
		expected bool
	}{
		{"example.com", "10 mail.example.com", true},
		{"example.com", "5 mail1.example.com", true},
		{"example.com", "20 mail2.example.com", true},
		{"example.com", "invalid", false},
		{"example.com", "10", false},
		{"example.com", "notanumber mail.example.com", false},
		{"example.com", "", false},
	}

	for _, test := range tests {
		rr := builder.CreateRecord(test.name, dns.TypeMX, 300, test.target)
		if test.expected {
			require.NotNil(t, rr, "Expected MX record for target: %s", test.target)
			mxRecord, ok := rr.(*dns.MX)
			require.True(t, ok, "Expected *dns.MX type")
			assert.Equal(t, "example.com.", mxRecord.Hdr.Name)
			assert.Equal(t, dns.TypeMX, mxRecord.Hdr.Rrtype)
		} else {
			assert.Nil(t, rr, "Expected nil MX record for invalid target: %s", test.target)
		}
	}
}

func TestCreateTXTRecord(t *testing.T) {
	builder := NewRecordBuilder()

	tests := []string{
		"v=spf1 include:_spf.google.com ~all",
		"simple text",
		"",
		"key=value",
	}

	for _, target := range tests {
		rr := builder.CreateRecord("example.com", dns.TypeTXT, 300, target)
		require.NotNil(t, rr)

		txtRecord, ok := rr.(*dns.TXT)
		require.True(t, ok)
		assert.Equal(t, "example.com.", txtRecord.Hdr.Name)
		assert.Equal(t, dns.TypeTXT, txtRecord.Hdr.Rrtype)
		assert.Equal(t, uint32(300), txtRecord.Hdr.Ttl)
		assert.Contains(t, txtRecord.Txt, target)
	}
}

func TestCreateSRVRecord(t *testing.T) {
	builder := NewRecordBuilder()

	tests := []struct {
		name     string
		target   string
		expected bool
	}{
		{"_http._tcp.example.com", "10 5 80 web.example.com", true},
		{"_sip._tcp.example.com", "0 5 5060 sip.example.com", true},
		{"_ldap._tcp.example.com", "10 10 389 ldap.example.com", true},
		{"example.com", "invalid", false},
		{"example.com", "10 5 80", false}, // Missing target
		{"example.com", "notanumber 5 80 web.example.com", false},
		{"example.com", "10 notanumber 80 web.example.com", false},
		{"example.com", "10 5 notanumber web.example.com", false},
		{"example.com", "", false},
	}

	for _, test := range tests {
		rr := builder.CreateRecord(test.name, dns.TypeSRV, 300, test.target)
		if test.expected {
			require.NotNil(t, rr, "Expected SRV record for target: %s", test.target)
			srvRecord, ok := rr.(*dns.SRV)
			require.True(t, ok, "Expected *dns.SRV type")
			assert.Equal(t, test.name+".", srvRecord.Hdr.Name)
			assert.Equal(t, dns.TypeSRV, srvRecord.Hdr.Rrtype)
		} else {
			assert.Nil(t, rr, "Expected nil SRV record for invalid target: %s", test.target)
		}
	}
}

func TestCreatePTRRecord(t *testing.T) {
	builder := NewRecordBuilder()

	rr := builder.CreateRecord("1.1.168.192.in-addr.arpa", dns.TypePTR, 300, "host.example.com")
	require.NotNil(t, rr)

	ptrRecord, ok := rr.(*dns.PTR)
	require.True(t, ok)
	assert.Equal(t, "1.1.168.192.in-addr.arpa.", ptrRecord.Hdr.Name)
	assert.Equal(t, dns.TypePTR, ptrRecord.Hdr.Rrtype)
	assert.Equal(t, uint32(300), ptrRecord.Hdr.Ttl)
	assert.Equal(t, "host.example.com.", ptrRecord.Ptr)
}

func TestCreatePTRRecordFromIP(t *testing.T) {
	builder := NewRecordBuilder()

	tests := []struct {
		ip       string
		target   string
		expected bool
	}{
		{"192.168.1.1", "host.example.com", true},
		{"10.0.0.1", "server.example.com", true},
		{"2001:db8::1", "ipv6.example.com", true},
		{"invalid-ip", "host.example.com", false},
		{"", "host.example.com", false},
	}

	for _, test := range tests {
		rr := builder.CreatePTRRecord(test.ip, test.target, 300)
		if test.expected {
			require.NotNil(t, rr, "Expected PTR record for IP: %s", test.ip)
			ptrRecord, ok := rr.(*dns.PTR)
			require.True(t, ok, "Expected *dns.PTR type")
			assert.Equal(t, dns.TypePTR, ptrRecord.Hdr.Rrtype)
			assert.Equal(t, test.target+".", ptrRecord.Ptr)
		} else {
			assert.Nil(t, rr, "Expected nil PTR record for invalid IP: %s", test.ip)
		}
	}
}

func TestCreateNSRecord(t *testing.T) {
	builder := NewRecordBuilder()

	rr := builder.CreateRecord("example.com", dns.TypeNS, 300, "ns1.example.com")
	require.NotNil(t, rr)

	nsRecord, ok := rr.(*dns.NS)
	require.True(t, ok)
	assert.Equal(t, "example.com.", nsRecord.Hdr.Name)
	assert.Equal(t, dns.TypeNS, nsRecord.Hdr.Rrtype)
	assert.Equal(t, uint32(300), nsRecord.Hdr.Ttl)
	assert.Equal(t, "ns1.example.com.", nsRecord.Ns)
}

func TestCreateSOARecord(t *testing.T) {
	builder := NewRecordBuilder()

	tests := []struct {
		name        string
		target      string
		expectValid bool
	}{
		{
			name:        "example.com",
			target:      "ns1.example.com admin.example.com 2023010101 3600 1800 1209600 300",
			expectValid: true,
		},
		{
			name:        "example.com",
			target:      "incomplete",
			expectValid: true, // Should create default SOA
		},
		{
			name:        "example.com",
			target:      "",
			expectValid: true, // Should create default SOA
		},
	}

	for _, test := range tests {
		rr := builder.CreateRecord(test.name, dns.TypeSOA, 300, test.target)
		if test.expectValid {
			require.NotNil(t, rr, "Expected SOA record for target: %s", test.target)
			soaRecord, ok := rr.(*dns.SOA)
			require.True(t, ok, "Expected *dns.SOA type")
			assert.Equal(t, test.name+".", soaRecord.Hdr.Name)
			assert.Equal(t, dns.TypeSOA, soaRecord.Hdr.Rrtype)
			assert.Equal(t, uint32(300), soaRecord.Hdr.Ttl)
			assert.NotEmpty(t, soaRecord.Ns)
			assert.NotEmpty(t, soaRecord.Mbox)
		} else {
			assert.Nil(t, rr, "Expected nil SOA record for invalid target: %s", test.target)
		}
	}
}

func TestCreateRecordUnsupportedType(t *testing.T) {
	builder := NewRecordBuilder()

	rr := builder.CreateRecord("test.example.com", 65535, 300, "target") // Unsupported type
	assert.Nil(t, rr)
}

func TestCreateRecordNameNormalization(t *testing.T) {
	builder := NewRecordBuilder()

	tests := []string{
		"Example.Com",
		"EXAMPLE.COM",
		"example.com.",
		"Example.Com.",
	}

	for _, name := range tests {
		rr := builder.CreateRecord(name, dns.TypeA, 300, "192.168.1.1")
		require.NotNil(t, rr)
		assert.Equal(t, "example.com.", rr.Header().Name, "Name should be normalized to lowercase FQDN")
	}
}
