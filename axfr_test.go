package externaldns

import (
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/require"
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
	require.NotZero(t, serial, "Expected non-zero serial")

	// Test Transfer method
	ch, err := e.Transfer("example.com.", 0)
	require.NoError(t, err, "Transfer failed")

	// Read from the channel
	records := <-ch
	require.GreaterOrEqual(t, len(records), 2, "Expected at least 2 records")

	// Check if SOA record is present
	var soaFound bool
	for _, rr := range records {
		if rr.Header().Rrtype == dns.TypeSOA {
			soaFound = true
			break
		}
	}
	require.True(t, soaFound, "SOA record not found in AXFR response")
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
	require.GreaterOrEqual(t, recordCount, 1, "Expected at least 1 record")
}
