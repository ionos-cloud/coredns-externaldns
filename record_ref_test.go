package externaldns

import (
	"net"
	"testing"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/external-dns/endpoint"
)

func TestAddRecordFromRef(t *testing.T) {
	plugin := &ExternalDNS{
		ttl:   300,
		cache: NewDNSCache(0), // Disable metrics updates for test
	}
	defer plugin.cache.Stop()

	tests := []struct {
		name        string
		ref         RecordRef
		endpointKey string
		expectError bool
	}{
		{
			name: "A record",
			ref: RecordRef{
				Zone:   "example.com.",
				Name:   "test.example.com.",
				Type:   dns.TypeA,
				Target: "192.168.1.1",
				TTL:    300,
			},
			endpointKey: "default/test-a",
			expectError: false,
		},
		{
			name: "AAAA record",
			ref: RecordRef{
				Zone:   "example.com.",
				Name:   "test.example.com.",
				Type:   dns.TypeAAAA,
				Target: "2001:db8::1",
				TTL:    300,
			},
			endpointKey: "default/test-aaaa",
			expectError: false,
		},
		{
			name: "CNAME record",
			ref: RecordRef{
				Zone:   "example.com.",
				Name:   "alias.example.com.",
				Type:   dns.TypeCNAME,
				Target: "target.example.com.",
				TTL:    300,
			},
			endpointKey: "default/test-cname",
			expectError: false,
		},
		{
			name: "PTR record",
			ref: RecordRef{
				Zone:   "1.168.192.in-addr.arpa.",
				Name:   "1.1.168.192.in-addr.arpa.",
				Type:   dns.TypePTR,
				Target: "host.example.com.",
				TTL:    300,
			},
			endpointKey: "default/test-ptr",
			expectError: false,
		},
		{
			name: "NS record",
			ref: RecordRef{
				Zone:   "example.com.",
				Name:   "sub.example.com.",
				Type:   dns.TypeNS,
				Target: "ns1.example.com.",
				TTL:    300,
			},
			endpointKey: "default/test-ns",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Add the record
			plugin.addRecordFromRef(tt.ref, tt.endpointKey)

			// Verify the record was added to cache
			records := plugin.cache.GetRecords(tt.ref.Name, tt.ref.Type)
			require.Len(t, records, 1, "Expected exactly one record")

			// Verify the record content is correct
			rr := records[0]
			require.Equal(t, tt.ref.Name, rr.Header().Name)
			require.Equal(t, tt.ref.Type, rr.Header().Rrtype)
			require.Equal(t, uint32(300), rr.Header().Ttl)

			// Verify record-specific fields
			switch tt.ref.Type {
			case dns.TypeA:
				aRecord := rr.(*dns.A)
				require.Equal(t, tt.ref.Target, aRecord.A.String())
			case dns.TypeAAAA:
				aaaaRecord := rr.(*dns.AAAA)
				require.Equal(t, tt.ref.Target, aaaaRecord.AAAA.String())
			case dns.TypeCNAME:
				cnameRecord := rr.(*dns.CNAME)
				require.Equal(t, tt.ref.Target, cnameRecord.Target)
			case dns.TypePTR:
				ptrRecord := rr.(*dns.PTR)
				require.Equal(t, tt.ref.Target, ptrRecord.Ptr)
			case dns.TypeNS:
				nsRecord := rr.(*dns.NS)
				require.Equal(t, tt.ref.Target, nsRecord.Ns)
			}

			// Verify endpoint tracking
			plugin.cache.Lock()
			endpointRecs, exists := plugin.cache.endpointRecords[tt.endpointKey]
			plugin.cache.Unlock()
			require.True(t, exists, "Endpoint should be tracked")
			require.Len(t, endpointRecs, 1, "Should have one tracked record")
			require.Equal(t, tt.ref, endpointRecs[0], "Tracked record should match")
		})
	}
}

func TestCreateDNSRecordFromRef(t *testing.T) {
	plugin := &ExternalDNS{
		ttl: 600,
	}

	tests := []struct {
		name         string
		ref          RecordRef
		expectRecord bool
		validateFunc func(t *testing.T, rr dns.RR, ref RecordRef)
	}{
		{
			name: "A record creation",
			ref: RecordRef{
				Zone:   "example.com.",
				Name:   "test.example.com.",
				Type:   dns.TypeA,
				Target: "10.0.0.1",
				TTL:    600,
			},
			expectRecord: true,
			validateFunc: func(t *testing.T, rr dns.RR, ref RecordRef) {
				aRecord, ok := rr.(*dns.A)
				require.True(t, ok, "Should be A record")
				require.Equal(t, net.ParseIP("10.0.0.1"), aRecord.A)
				require.Equal(t, ref.Name, aRecord.Hdr.Name)
				require.Equal(t, dns.TypeA, aRecord.Hdr.Rrtype)
				require.Equal(t, uint32(600), aRecord.Hdr.Ttl)
			},
		},
		{
			name: "AAAA record creation",
			ref: RecordRef{
				Zone:   "example.com.",
				Name:   "test.example.com.",
				Type:   dns.TypeAAAA,
				Target: "2001:db8::42",
				TTL:    600,
			},
			expectRecord: true,
			validateFunc: func(t *testing.T, rr dns.RR, ref RecordRef) {
				aaaaRecord, ok := rr.(*dns.AAAA)
				require.True(t, ok, "Should be AAAA record")
				require.Equal(t, net.ParseIP("2001:db8::42"), aaaaRecord.AAAA)
				require.Equal(t, ref.Name, aaaaRecord.Hdr.Name)
				require.Equal(t, dns.TypeAAAA, aaaaRecord.Hdr.Rrtype)
			},
		},
		{
			name: "CNAME record creation",
			ref: RecordRef{
				Zone:   "example.com.",
				Name:   "alias.example.com.",
				Type:   dns.TypeCNAME,
				Target: "canonical.example.com.",
				TTL:    600,
			},
			expectRecord: true,
			validateFunc: func(t *testing.T, rr dns.RR, ref RecordRef) {
				cnameRecord, ok := rr.(*dns.CNAME)
				require.True(t, ok, "Should be CNAME record")
				require.Equal(t, "canonical.example.com.", cnameRecord.Target)
				require.Equal(t, ref.Name, cnameRecord.Hdr.Name)
				require.Equal(t, dns.TypeCNAME, cnameRecord.Hdr.Rrtype)
			},
		},
		{
			name: "PTR record creation",
			ref: RecordRef{
				Zone:   "1.168.192.in-addr.arpa.",
				Name:   "42.1.168.192.in-addr.arpa.",
				Type:   dns.TypePTR,
				Target: "server.example.com.",
				TTL:    600,
			},
			expectRecord: true,
			validateFunc: func(t *testing.T, rr dns.RR, ref RecordRef) {
				ptrRecord, ok := rr.(*dns.PTR)
				require.True(t, ok, "Should be PTR record")
				require.Equal(t, "server.example.com.", ptrRecord.Ptr)
				require.Equal(t, ref.Name, ptrRecord.Hdr.Name)
				require.Equal(t, dns.TypePTR, ptrRecord.Hdr.Rrtype)
			},
		},
		{
			name: "NS record creation",
			ref: RecordRef{
				Zone:   "example.com.",
				Name:   "subdomain.example.com.",
				Type:   dns.TypeNS,
				Target: "ns1.provider.com.",
				TTL:    600,
			},
			expectRecord: true,
			validateFunc: func(t *testing.T, rr dns.RR, ref RecordRef) {
				nsRecord, ok := rr.(*dns.NS)
				require.True(t, ok, "Should be NS record")
				require.Equal(t, "ns1.provider.com.", nsRecord.Ns)
				require.Equal(t, ref.Name, nsRecord.Hdr.Name)
				require.Equal(t, dns.TypeNS, nsRecord.Hdr.Rrtype)
			},
		},
		{
			name: "Unsupported record type",
			ref: RecordRef{
				Zone:   "example.com.",
				Name:   "test.example.com.",
				Type:   dns.TypeMX, // Not implemented in createDNSRecordFromRef
				Target: "10 mail.example.com.",
				TTL:    600,
			},
			expectRecord: false,
			validateFunc: func(t *testing.T, rr dns.RR, ref RecordRef) {
				require.Nil(t, rr, "Should return nil for unsupported record type")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := plugin.createDNSRecordFromRef(tt.ref)

			if tt.expectRecord {
				require.NotNil(t, rr, "Should create a valid DNS record")
			}

			tt.validateFunc(t, rr, tt.ref)
		})
	}
}

func TestCollectRecordRefs(t *testing.T) {
	plugin := &ExternalDNS{
		ttl: 300,
	}

	tests := []struct {
		name        string
		endpoint    *endpoint.Endpoint
		createPTR   bool
		expectedLen int
		validate    func(t *testing.T, refs []RecordRef)
	}{
		{
			name: "A record without PTR",
			endpoint: &endpoint.Endpoint{
				DNSName:    "web.example.com",
				RecordType: "A",
				Targets:    []string{"192.168.1.10", "192.168.1.20"},
				RecordTTL:  300,
			},
			createPTR:   false,
			expectedLen: 2,
			validate: func(t *testing.T, refs []RecordRef) {
				require.Len(t, refs, 2)
				for i, ref := range refs {
					require.Equal(t, "web.example.com.", ref.Name)
					require.Equal(t, dns.TypeA, ref.Type)
					require.Equal(t, "example.com.", ref.Zone)
					if i == 0 {
						require.Equal(t, "192.168.1.10", ref.Target)
					} else {
						require.Equal(t, "192.168.1.20", ref.Target)
					}
				}
			},
		},
		{
			name: "A record with PTR",
			endpoint: &endpoint.Endpoint{
				DNSName:    "host.example.com",
				RecordType: "A",
				Targets:    []string{"10.0.0.5"},
				RecordTTL:  300,
			},
			createPTR:   true,
			expectedLen: 2, // 1 A record + 1 PTR record
			validate: func(t *testing.T, refs []RecordRef) {
				require.Len(t, refs, 2)

				// Find A and PTR records
				var aRef, ptrRef *RecordRef
				for _, ref := range refs {
					switch ref.Type {
					case dns.TypeA:
						aRef = &ref
					case dns.TypePTR:
						ptrRef = &ref
					}
				}

				require.NotNil(t, aRef, "Should have A record")
				require.NotNil(t, ptrRef, "Should have PTR record")

				// Validate A record
				require.Equal(t, "host.example.com.", aRef.Name)
				require.Equal(t, "10.0.0.5", aRef.Target)
				require.Equal(t, "example.com.", aRef.Zone)

				// Validate PTR record
				require.Equal(t, "5.0.0.10.in-addr.arpa.", ptrRef.Name)
				require.Equal(t, "host.example.com.", ptrRef.Target)
				require.Equal(t, "0.0.10.in-addr.arpa.", ptrRef.Zone)
			},
		},
		{
			name: "AAAA record with PTR",
			endpoint: &endpoint.Endpoint{
				DNSName:    "ipv6.example.com",
				RecordType: "AAAA",
				Targets:    []string{"2001:db8::1"},
				RecordTTL:  300,
			},
			createPTR:   true,
			expectedLen: 2, // 1 AAAA record + 1 PTR record
			validate: func(t *testing.T, refs []RecordRef) {
				require.Len(t, refs, 2)

				// Find AAAA and PTR records
				var aaaaRef, ptrRef *RecordRef
				for _, ref := range refs {
					switch ref.Type {
					case dns.TypeAAAA:
						aaaaRef = &ref
					case dns.TypePTR:
						ptrRef = &ref
					}
				}

				require.NotNil(t, aaaaRef, "Should have AAAA record")
				require.NotNil(t, ptrRef, "Should have PTR record")

				// Validate AAAA record
				require.Equal(t, "ipv6.example.com.", aaaaRef.Name)
				require.Equal(t, "2001:db8::1", aaaaRef.Target)
				require.Equal(t, "example.com.", aaaaRef.Zone)

				// Validate PTR record
				require.Contains(t, ptrRef.Name, ".ip6.arpa.")
				require.Equal(t, "ipv6.example.com.", ptrRef.Target)
				require.Contains(t, ptrRef.Zone, ".ip6.arpa.")
			},
		},
		{
			name: "CNAME record",
			endpoint: &endpoint.Endpoint{
				DNSName:    "alias.example.com",
				RecordType: "CNAME",
				Targets:    []string{"target.example.com"},
				RecordTTL:  300,
			},
			createPTR:   false,
			expectedLen: 1,
			validate: func(t *testing.T, refs []RecordRef) {
				require.Len(t, refs, 1)
				ref := refs[0]
				require.Equal(t, "alias.example.com.", ref.Name)
				require.Equal(t, dns.TypeCNAME, ref.Type)
				require.Equal(t, "target.example.com.", ref.Target)
				require.Equal(t, "example.com.", ref.Zone)
			},
		},
		{
			name: "Invalid A record target",
			endpoint: &endpoint.Endpoint{
				DNSName:    "invalid.example.com",
				RecordType: "A",
				Targets:    []string{"not-an-ip", "192.168.1.1"},
				RecordTTL:  300,
			},
			createPTR:   false,
			expectedLen: 1, // Only the valid IP should be collected
			validate: func(t *testing.T, refs []RecordRef) {
				require.Len(t, refs, 1)
				ref := refs[0]
				require.Equal(t, "invalid.example.com.", ref.Name)
				require.Equal(t, dns.TypeA, ref.Type)
				require.Equal(t, "192.168.1.1", ref.Target)
			},
		},
		{
			name: "Invalid AAAA record target",
			endpoint: &endpoint.Endpoint{
				DNSName:    "invalid.example.com",
				RecordType: "AAAA",
				Targets:    []string{"192.168.1.1", "2001:db8::1"}, // IPv4 should be ignored
				RecordTTL:  300,
			},
			createPTR:   false,
			expectedLen: 1, // Only the valid IPv6 should be collected
			validate: func(t *testing.T, refs []RecordRef) {
				require.Len(t, refs, 1)
				ref := refs[0]
				require.Equal(t, "invalid.example.com.", ref.Name)
				require.Equal(t, dns.TypeAAAA, ref.Type)
				require.Equal(t, "2001:db8::1", ref.Target)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			refs := plugin.collectRecordRefs(tt.endpoint, tt.createPTR)
			require.Len(t, refs, tt.expectedLen)
			tt.validate(t, refs)
		})
	}
}

func TestAddRecordFromRefIntegration(t *testing.T) {
	plugin := &ExternalDNS{
		ttl:   600,
		cache: NewDNSCache(0),
	}
	defer plugin.cache.Stop()

	endpointKey := "default/integration-test"

	// Test adding multiple different record types for the same endpoint
	refs := []RecordRef{
		{
			Zone:   "example.com.",
			Name:   "multi.example.com.",
			Type:   dns.TypeA,
			Target: "10.1.1.1",
			TTL:    600,
		},
		{
			Zone:   "example.com.",
			Name:   "multi.example.com.",
			Type:   dns.TypeA,
			Target: "10.1.1.2",
			TTL:    600,
		},
		{
			Zone:   "example.com.",
			Name:   "cname.example.com.",
			Type:   dns.TypeCNAME,
			Target: "multi.example.com.",
			TTL:    600,
		},
	}

	// Add all records
	for _, ref := range refs {
		plugin.addRecordFromRef(ref, endpointKey)
	}

	// Verify all A records were added
	aRecords := plugin.cache.GetRecords("multi.example.com.", dns.TypeA)
	require.Len(t, aRecords, 2, "Should have 2 A records")

	ips := make([]string, 0, 2)
	for _, rr := range aRecords {
		aRecord := rr.(*dns.A)
		ips = append(ips, aRecord.A.String())
	}
	require.Contains(t, ips, "10.1.1.1")
	require.Contains(t, ips, "10.1.1.2")

	// Verify CNAME record was added
	cnameRecords := plugin.cache.GetRecords("cname.example.com.", dns.TypeCNAME)
	require.Len(t, cnameRecords, 1, "Should have 1 CNAME record")
	cnameRecord := cnameRecords[0].(*dns.CNAME)
	require.Equal(t, "multi.example.com.", cnameRecord.Target)

	// Verify endpoint tracking
	plugin.cache.Lock()
	trackedRefs, exists := plugin.cache.endpointRecords[endpointKey]
	plugin.cache.Unlock()

	require.True(t, exists, "Endpoint should be tracked")
	require.Len(t, trackedRefs, 3, "Should track all 3 records")

	// Verify tracked records match what we added
	for _, originalRef := range refs {
		found := false
		for _, trackedRef := range trackedRefs {
			if originalRef == trackedRef {
				found = true
				break
			}
		}
		require.True(t, found, "All original refs should be tracked")
	}
}
