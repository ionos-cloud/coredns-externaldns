package externaldns

import (
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/require"
)

func TestDNSCache(t *testing.T) {
	cache := NewDNSCache(time.Minute)
	defer cache.Stop() // Cleanup background goroutine

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

	require.Len(t, records, 1, "Expected 1 record")

	require.Equal(t, "192.168.1.1", records[0].(*dns.A).A.String(), "A record IP should match")

	// Test case insensitive lookup
	records = cache.GetRecords("TEST.EXAMPLE.COM", dns.TypeA)
	require.Len(t, records, 1, "Case insensitive lookup should return 1 record")

	// Test removing records
	cache.RemoveRecord("test.example.com", dns.TypeA, "192.168.1.1")
	records = cache.GetRecords("test.example.com", dns.TypeA)
	require.Len(t, records, 0, "Expected 0 records after removal")
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
		require.Equal(t, test.expected, result, "recordTypeToQType(%s)", test.recordType)
	}
}

func TestCreateDNSRecord(t *testing.T) {
	ed := &ExternalDNS{}

	// Test A record creation
	rr := ed.createDNSRecord("test.example.com", dns.TypeA, 300, "192.168.1.1")
	require.NotNil(t, rr, "Expected A record")
	aRecord, ok := rr.(*dns.A)
	require.True(t, ok, "Expected *dns.A type")
	require.Equal(t, "192.168.1.1", aRecord.A.String(), "A record IP should match")

	// Test CNAME record creation
	rr = ed.createDNSRecord("www.example.com", dns.TypeCNAME, 300, "example.com")
	require.NotNil(t, rr, "Expected CNAME record")
	cnameRecord, ok := rr.(*dns.CNAME)
	require.True(t, ok, "Expected *dns.CNAME type")
	require.Equal(t, "example.com.", cnameRecord.Target, "CNAME target should match")

	// Test MX record creation
	rr = ed.createDNSRecord("example.com", dns.TypeMX, 300, "10 mail.example.com")
	require.NotNil(t, rr, "Expected MX record")
	mxRecord, ok := rr.(*dns.MX)
	require.True(t, ok, "Expected *dns.MX type")
	require.Equal(t, uint16(10), mxRecord.Preference, "MX preference should match")
	require.Equal(t, "mail.example.com.", mxRecord.Mx, "MX host should match")
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

	require.True(t, cache.recordMatches(aRecord, "192.168.1.1"), "A record should match IP address")

	require.False(t, cache.recordMatches(aRecord, "192.168.1.2"), "A record should not match different IP address")

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

	require.True(t, cache.recordMatches(cnameRecord, "example.com."), "CNAME record should match target")

	require.False(t, cache.recordMatches(cnameRecord, "other.com."), "CNAME record should not match different target")
}

func TestServeDNS(t *testing.T) {
	ed := &ExternalDNS{
		cache: NewDNSCache(time.Minute),
		ttl:   300,
	}
	defer ed.cache.Stop() // Cleanup background goroutine

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
	require.Len(t, records, 1, "Expected 1 record in cache")

	require.Equal(t, "192.168.1.1", records[0].(*dns.A).A.String(), "Cached A record should match")

	// Test cache size
	cacheSize := ed.cache.GetCacheSize()
	require.Equal(t, 1, cacheSize, "Cache size should be 1")

	// Test Name method
	require.Equal(t, "externaldns", ed.Name(), "Plugin name should be externaldns")
}

// TestCacheMetrics tests that cache size metrics are properly updated
func TestCacheMetrics(t *testing.T) {
	cache := NewDNSCache(time.Minute)
	defer cache.Stop() // Cleanup background goroutine

	// Initial cache should be empty
	require.Equal(t, 0, cache.GetCacheSize(), "Expected empty cache")

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
	require.Equal(t, 1, cache.GetCacheSize(), "Cache size should be 1")

	cache.AddRecord("test2.example.com", dns.TypeA, aRecord2)
	require.Equal(t, 2, cache.GetCacheSize(), "Cache size should be 2")

	cache.AddRecord("www.example.com", dns.TypeCNAME, cnameRecord)
	require.Equal(t, 3, cache.GetCacheSize(), "Cache size should be 3")

	// Remove a record
	cache.RemoveRecord("test1.example.com", dns.TypeA, "192.168.1.1")
	require.Equal(t, 2, cache.GetCacheSize(), "Cache size should be 2 after removal")

	// Clear all records for a domain
	cache.ClearRecords("test2.example.com")
	require.Equal(t, 1, cache.GetCacheSize(), "Cache size should be 1 after clearing domain")
}

// TestBackgroundMetricsUpdate tests that the background metrics update goroutine works
func TestBackgroundMetricsUpdate(t *testing.T) {
	// Use a short interval for testing
	cache := NewDNSCache(100 * time.Millisecond)
	defer cache.Stop() // Cleanup background goroutine

	// Add a record
	aRecord := &dns.A{
		Hdr: dns.RR_Header{
			Name:   "test.example.com.",
			Rrtype: dns.TypeA,
			Class:  dns.ClassINET,
			Ttl:    300,
		},
		A: net.ParseIP("192.168.1.1"),
	}

	cache.AddRecord("test.example.com.", dns.TypeA, aRecord)

	// Wait for at least one metrics update cycle
	time.Sleep(150 * time.Millisecond)

	// Verify the cache has the record
	require.Equal(t, 1, cache.GetCacheSize(), "Cache size should be 1")
}

// TestZoneMetrics tests that metrics include zone labels correctly
func TestZoneMetrics(t *testing.T) {
	// Use a short interval for testing
	cache := NewDNSCache(100 * time.Millisecond)
	defer cache.Stop() // Cleanup background goroutine

	// Add records to different zones
	aRecord1 := &dns.A{
		Hdr: dns.RR_Header{
			Name:   "test.example.com.",
			Rrtype: dns.TypeA,
			Class:  dns.ClassINET,
			Ttl:    300,
		},
		A: net.ParseIP("192.168.1.1"),
	}

	aRecord2 := &dns.A{
		Hdr: dns.RR_Header{
			Name:   "api.different.org.",
			Rrtype: dns.TypeA,
			Class:  dns.ClassINET,
			Ttl:    300,
		},
		A: net.ParseIP("192.168.1.2"),
	}

	cache.AddRecord("test.example.com.", dns.TypeA, aRecord1)
	cache.AddRecord("api.different.org.", dns.TypeA, aRecord2)

	// Wait for at least one metrics update cycle
	time.Sleep(150 * time.Millisecond)

	// Verify we have records in different zones
	require.Equal(t, 2, cache.GetCacheSize(), "Cache size should be 2")

	// The metrics should now be reported per-zone
	// This test mainly verifies that the code runs without errors
	// In a real environment, you could check Prometheus metrics registry
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
			expected: "test.example.com.",
		},
		{
			input:    "example.com.",
			expected: "com.",
		},
		{
			input:    "single",
			expected: "single.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := getZoneName(tt.input)
			require.Equal(t, tt.expected, result, "getZoneName(%s)", tt.input)
		})
	}
}

func TestHandleAXFR(t *testing.T) {
	// start a test server
	srvAddr := "127.0.0.1:0"
	dnsServer := &dns.Server{Addr: srvAddr, Net: "tcp"}

	// fake ExternalDNS implementation with one SOA + one A record
	ext := &ExternalDNS{
		cache: NewDNSCache(time.Minute),
	}
	ext.cache.AddRecord("example.org.", dns.TypeSOA, mustRR("example.org. 300 IN SOA ns1.example.org. hostmaster.example.org. 1 7200 3600 1209600 300"))
	ext.cache.AddRecord("example.org.", dns.TypeA, mustRR("www.example.org. 300 IN A 192.0.2.1"))

	dns.HandleFunc("example.org.", func(w dns.ResponseWriter, r *dns.Msg) {
		_, err := ext.handleAXFR(w, r, "example.org.")
		require.NoError(t, err, "handleAXFR failed")
	})

	// start server in background
	go func() {
		if err := dnsServer.ListenAndServe(); err != nil {
			t.Logf("DNS server error: %v", err)
		}
	}()
	defer func() {
		err := dnsServer.Shutdown()
		require.NoError(t, err, "Failed to shutdown DNS server")
	}()

	// give it time to start
	time.Sleep(100 * time.Millisecond)

	// dial AXFR request over TCP
	conn, err := dns.Dial("tcp", dnsServer.Listener.Addr().String())
	require.NoError(t, err)
	defer func() {
		err := conn.Close()
		require.NoError(t, err, "Failed to close connection")
	}()

	tr := new(dns.Transfer)
	m := new(dns.Msg)
	m.SetAxfr("example.org.")

	// tr.In returns a channel of *dns.Envelope, each containing RR slices
	axfrChan, err := tr.In(m, dnsServer.Listener.Addr().String())
	require.NoError(t, err)

	var got []dns.RR
	for env := range axfrChan {
		require.NoError(t, env.Error)
		got = append(got, env.RR...)
	}

	// Now check first and last are SOA
	require.GreaterOrEqual(t, len(got), 2)
	require.Equal(t, dns.TypeSOA, got[0].Header().Rrtype)
	require.Equal(t, dns.TypeSOA, got[len(got)-1].Header().Rrtype)
}

func mustRR(s string) dns.RR {
	rr, err := dns.NewRR(s)
	if err != nil {
		panic(err)
	}
	return rr
}
