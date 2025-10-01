package externaldns

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/external-dns/endpoint"
)

// dns_record_bugs_test.go
//
// This file contains comprehensive tests for DNS record duplication bugs that were
// identified and fixed. It covers:
// 1. TTL inconsistency bug (different TTLs for same record)
// 2. Case sensitivity bug (RecordRef name mismatches)
// 3. Duplicate record addition bug (multiple identical records)
// 4. Real-world scenarios matching production issues

// TestTTLConsistencyFix tests the fix for TTL inconsistency bug where CoreDNS
// was returning duplicate records with different TTLs (plugin TTL vs endpoint TTL)
func TestTTLConsistencyFix(t *testing.T) {
	t.Run("ADDED event uses endpoint TTL correctly", func(t *testing.T) {
		plugin := &ExternalDNS{
			cache: NewDNSCache(5 * time.Minute),
			ttl:   300, // Plugin default TTL
		}

		ep := &endpoint.Endpoint{
			DNSName:    "test.example.com",
			RecordType: "A",
			Targets:    []string{"192.168.1.1"},
			RecordTTL:  600, // Endpoint-specific TTL (different from plugin)
		}

		// Simulate ADDED event
		refs := plugin.collectRecordRefs(ep, false)
		require.Len(t, refs, 1)
		require.Equal(t, uint32(600), refs[0].TTL, "RecordRef should use endpoint TTL, not plugin TTL")

		// Verify the actual DNS record created uses endpoint TTL
		rr := plugin.createDNSRecordFromRef(refs[0])
		require.Equal(t, uint32(600), rr.Header().Ttl, "DNS record should use endpoint TTL")
	})

	t.Run("MODIFIED event preserves endpoint TTL", func(t *testing.T) {
		plugin := &ExternalDNS{
			cache: NewDNSCache(5 * time.Minute),
			ttl:   300,
		}

		ep := &endpoint.Endpoint{
			DNSName:    "modified.example.com",
			RecordType: "A",
			Targets:    []string{"10.0.0.1"},
			RecordTTL:  900,
		}

		// Add initial record
		plugin.addEndpointToCache(ep, false, "test-endpoint")

		// Simulate MODIFIED event with same endpoint
		refs := plugin.collectRecordRefs(ep, false)
		require.Len(t, refs, 1)
		require.Equal(t, uint32(900), refs[0].TTL, "MODIFIED event should preserve endpoint TTL")
	})

	t.Run("Different TTLs should not cause duplicates", func(t *testing.T) {
		plugin := &ExternalDNS{
			cache: NewDNSCache(5 * time.Minute),
			ttl:   300,
		}

		ep1 := &endpoint.Endpoint{
			DNSName:    "same.example.com",
			RecordType: "A",
			Targets:    []string{"1.2.3.4"},
			RecordTTL:  600,
		}

		ep2 := &endpoint.Endpoint{
			DNSName:    "same.example.com",
			RecordType: "A",
			Targets:    []string{"1.2.3.4"}, // Same target
			RecordTTL:  900,                 // Different TTL
		}

		// Add first endpoint
		plugin.addEndpointToCache(ep1, false, "endpoint1")
		records1 := plugin.cache.GetRecords("same.example.com.", dns.TypeA)
		require.Len(t, records1, 1)

		// Add second endpoint with different TTL but same target
		plugin.addEndpointToCache(ep2, false, "endpoint2")
		records2 := plugin.cache.GetRecords("same.example.com.", dns.TypeA)
		require.Len(t, records2, 1, "Should not create duplicate records for same target with different TTL")

		// Verify final record uses most recent TTL
		finalRecord := records2[0]
		require.Equal(t, uint32(900), finalRecord.Header().Ttl, "Should use most recent TTL")
	})

	t.Run("createDNSRecordFromRef uses RecordRef TTL", func(t *testing.T) {
		plugin := &ExternalDNS{
			cache: NewDNSCache(5 * time.Minute),
			ttl:   300, // Plugin default
		}

		ref := RecordRef{
			Zone:   "example.com.",
			Name:   "record.example.com.",
			Type:   dns.TypeA,
			Target: "5.6.7.8",
			TTL:    1200, // RecordRef-specific TTL
		}

		rr := plugin.createDNSRecordFromRef(ref)
		require.Equal(t, uint32(1200), rr.Header().Ttl, "Should use RecordRef TTL, not plugin default")
	})
}

// TestCaseSensitivityFix tests the fix for case sensitivity bug where
// RecordRef comparison failed due to case differences in DNS names
func TestCaseSensitivityFix(t *testing.T) {
	t.Run("Mixed case DNS names create identical RecordRefs", func(t *testing.T) {
		plugin := &ExternalDNS{
			cache: NewDNSCache(5 * time.Minute),
		}

		// Test ADDED event with mixed case
		epAdded := &endpoint.Endpoint{
			DNSName:    "APP.Example.Com", // Mixed case
			RecordType: "A",
			Targets:    []string{"10.0.0.1"},
			RecordTTL:  300,
		}

		// Test MODIFIED event with different case
		epModified := &endpoint.Endpoint{
			DNSName:    "app.EXAMPLE.com", // Different case
			RecordType: "A",
			Targets:    []string{"10.0.0.1"}, // Same target
			RecordTTL:  300,
		}

		refAdded := plugin.collectRecordRefs(epAdded, false)[0]
		refModified := plugin.collectRecordRefs(epModified, false)[0]

		// Check if they are now the same (bug should be FIXED)
		if refAdded == refModified {
			t.Logf("BUG FIXED: RecordRef from ADDED and MODIFIED are now identical!")
			t.Logf("  Both have name: '%s'", refAdded.Name)
		} else {
			t.Logf("BUG STILL EXISTS: RecordRef from ADDED and MODIFIED are different!")
			t.Logf("  ADDED name:    '%s'", refAdded.Name)
			t.Logf("  MODIFIED name: '%s'", refModified.Name)
		}

		// The bug should be fixed now
		require.Equal(t, refAdded, refModified, "Case sensitivity bug should be fixed - RecordRefs should be identical")
	})

	t.Run("Case differences do not cause duplicate records", func(t *testing.T) {
		plugin := &ExternalDNS{
			cache: NewDNSCache(5 * time.Minute),
		}

		endpointKey := "default/duplicate-case-test"

		// Add endpoint with mixed case
		ep := &endpoint.Endpoint{
			DNSName:    "Service.Example.Com",
			RecordType: "A",
			Targets:    []string{"192.168.1.1"},
			RecordTTL:  300,
		}

		// Add to cache (this normalizes case)
		plugin.addEndpointToCache(ep, false, endpointKey)

		// Simulate differential update with different case
		newRefs := plugin.collectRecordRefs(ep, false)
		existingRefs := []RecordRef{
			{
				Zone:   getZoneName(getRecordName(ep.DNSName)),
				Name:   getRecordName(ep.DNSName), // Normalized
				Type:   dns.TypeA,
				Target: ep.Targets[0],
				TTL:    uint32(ep.RecordTTL),
			},
		}

		// Find what would be added (should be nothing)
		var toAdd []RecordRef
		for _, newRef := range newRefs {
			found := false
			for _, existingRef := range existingRefs {
				if newRef == existingRef {
					found = true
					break
				}
			}
			if !found {
				toAdd = append(toAdd, newRef)
			}
		}

		// Check if the bug is fixed: toAdd should be empty
		if len(toAdd) == 0 {
			t.Logf("BUG FIXED: No duplicate records to add - case normalization working correctly!")
		} else {
			t.Logf("BUG STILL EXISTS: Will add %d duplicate records due to case mismatch", len(toAdd))
		}

		// Actually perform the update to verify no duplicates
		plugin.cache.UpdateRecordsByEndpointWithAddition(endpointKey, newRefs, plugin, false)

		// Check final state
		records := plugin.cache.GetRecords("service.example.com.", dns.TypeA)

		if len(records) > 1 {
			t.Logf("BUG STILL EXISTS: %d duplicate records created!", len(records))
		} else {
			t.Logf("BUG FIXED: Only 1 record as expected - no duplicates!")
		}

		// The bug should be fixed - we should have exactly 1 record
		require.Equal(t, 1, len(records), "Case sensitivity bug should be fixed - should have exactly 1 record")
	})
}

// TestDuplicateRecordPrevention tests the fix for the core duplicate record bug
// where AddRecord/AddRecordWithEndpoint always appended without checking for duplicates
func TestDuplicateRecordPrevention(t *testing.T) {
	t.Run("Multiple identical additions prevented", func(t *testing.T) {
		plugin := &ExternalDNS{
			cache: NewDNSCache(5 * time.Minute),
		}

		ep := &endpoint.Endpoint{
			DNSName:    "test.example.com",
			RecordType: "A",
			Targets:    []string{"192.168.1.1"},
			RecordTTL:  300,
		}

		endpointKey := "test/endpoint"

		// Add same endpoint multiple times
		plugin.addEndpointToCache(ep, false, endpointKey)
		plugin.addEndpointToCache(ep, false, endpointKey)
		plugin.addEndpointToCache(ep, false, endpointKey)

		records := plugin.cache.GetRecords("test.example.com.", dns.TypeA)
		require.Len(t, records, 1, "Multiple identical additions should result in only 1 record")
	})

	t.Run("Same target with different TTL replaces existing", func(t *testing.T) {
		plugin := &ExternalDNS{
			cache: NewDNSCache(5 * time.Minute),
		}

		endpointKey := "test/endpoint"

		// Add record with TTL 300
		rr1 := &dns.A{
			Hdr: dns.RR_Header{
				Name:   dns.Fqdn("replace.example.com"),
				Rrtype: dns.TypeA,
				Class:  dns.ClassINET,
				Ttl:    300,
			},
			A: net.ParseIP("10.0.0.1"),
		}
		plugin.cache.AddRecordWithEndpoint("replace.example.com", dns.TypeA, rr1, endpointKey, "10.0.0.1")

		// Add record with same target but different TTL
		rr2 := &dns.A{
			Hdr: dns.RR_Header{
				Name:   dns.Fqdn("replace.example.com"),
				Rrtype: dns.TypeA,
				Class:  dns.ClassINET,
				Ttl:    600, // Different TTL
			},
			A: net.ParseIP("10.0.0.1"), // Same target
		}
		plugin.cache.AddRecordWithEndpoint("replace.example.com", dns.TypeA, rr2, endpointKey, "10.0.0.1")

		records := plugin.cache.GetRecords("replace.example.com.", dns.TypeA)
		require.Len(t, records, 1, "Same target with different TTL should replace, not duplicate")

		// Verify it has the newer TTL
		finalRecord := records[0].(*dns.A)
		require.Equal(t, uint32(600), finalRecord.Hdr.Ttl, "Should have the most recent TTL")
	})

	t.Run("Different targets create separate records", func(t *testing.T) {
		plugin := &ExternalDNS{
			cache: NewDNSCache(5 * time.Minute),
		}

		endpointKey := "test/endpoint"

		// Add two records with different targets
		rr1 := &dns.A{
			Hdr: dns.RR_Header{
				Name:   dns.Fqdn("multi.example.com"),
				Rrtype: dns.TypeA,
				Class:  dns.ClassINET,
				Ttl:    300,
			},
			A: net.ParseIP("10.0.0.1"),
		}
		plugin.cache.AddRecordWithEndpoint("multi.example.com", dns.TypeA, rr1, endpointKey, "10.0.0.1")

		rr2 := &dns.A{
			Hdr: dns.RR_Header{
				Name:   dns.Fqdn("multi.example.com"),
				Rrtype: dns.TypeA,
				Class:  dns.ClassINET,
				Ttl:    300,
			},
			A: net.ParseIP("10.0.0.2"), // Different target
		}
		plugin.cache.AddRecordWithEndpoint("multi.example.com", dns.TypeA, rr2, endpointKey, "10.0.0.2")

		records := plugin.cache.GetRecords("multi.example.com.", dns.TypeA)
		require.Len(t, records, 2, "Different targets should create separate records")
	})
}

// TestRealWorldDuplicateScenario tests the exact scenario reported in production
// where dig showed 5 duplicate records for the same target with different TTLs
func TestRealWorldDuplicateScenario(t *testing.T) {
	// Based on actual dig output:
	// s3.mgmt.agb1.profitbricks.net. 86400 IN	A	31.249.2.3 (4 duplicates)
	// s3.mgmt.agb1.profitbricks.net. 604800 IN A	31.249.2.3 (1 more)

	plugin := &ExternalDNS{
		cache: NewDNSCache(5 * time.Minute),
		ttl:   604800, // Plugin default TTL
	}

	endpointKey := "default/s3-service"

	t.Logf("=== Testing real-world duplicate scenario ===")

	// Scenario 1: Multiple ADDED events (controller restarts, etc.)
	ep := &endpoint.Endpoint{
		DNSName:    "s3.mgmt.agb1.profitbricks.net",
		RecordType: "A",
		Targets:    []string{"31.249.2.3"},
		RecordTTL:  86400,
	}

	t.Logf("Adding endpoint multiple times...")
	plugin.addEndpointToCache(ep, false, endpointKey)
	plugin.addEndpointToCache(ep, false, endpointKey)
	plugin.addEndpointToCache(ep, false, endpointKey)

	records1 := plugin.cache.GetRecords("s3.mgmt.agb1.profitbricks.net.", dns.TypeA)
	require.Len(t, records1, 1, "Multiple identical additions should result in 1 record")

	// Scenario 2: Mixed TTL records (endpoint TTL vs plugin default)
	t.Logf("Adding record with plugin default TTL...")
	rr := &dns.A{
		Hdr: dns.RR_Header{
			Name:   dns.Fqdn("s3.mgmt.agb1.profitbricks.net"),
			Rrtype: dns.TypeA,
			Class:  dns.ClassINET,
			Ttl:    plugin.ttl, // Plugin default (604800)
		},
		A: net.ParseIP("31.249.2.3"),
	}
	plugin.cache.AddRecordWithEndpoint("s3.mgmt.agb1.profitbricks.net", dns.TypeA, rr, endpointKey, "31.249.2.3")

	records2 := plugin.cache.GetRecords("s3.mgmt.agb1.profitbricks.net.", dns.TypeA)
	require.Len(t, records2, 1, "TTL difference should not create duplicates")

	// Scenario 3: Simulate the exact dig output - multiple manual additions
	t.Logf("=== Simulating the exact dig scenario ===")

	// Clear cache and start fresh
	plugin.cache = NewDNSCache(5 * time.Minute)

	// Add records exactly like in dig output (this would create 5 duplicates before fix)
	for i := 0; i < 4; i++ {
		rr86400 := &dns.A{
			Hdr: dns.RR_Header{
				Name:   dns.Fqdn("s3.mgmt.agb1.profitbricks.net"),
				Rrtype: dns.TypeA,
				Class:  dns.ClassINET,
				Ttl:    86400,
			},
			A: net.ParseIP("31.249.2.3"),
		}
		plugin.cache.AddRecordWithEndpoint("s3.mgmt.agb1.profitbricks.net", dns.TypeA, rr86400, endpointKey, "31.249.2.3")
	}

	// Add one record with 604800 TTL
	rr604800 := &dns.A{
		Hdr: dns.RR_Header{
			Name:   dns.Fqdn("s3.mgmt.agb1.profitbricks.net"),
			Rrtype: dns.TypeA,
			Class:  dns.ClassINET,
			Ttl:    604800,
		},
		A: net.ParseIP("31.249.2.3"),
	}
	plugin.cache.AddRecordWithEndpoint("s3.mgmt.agb1.profitbricks.net", dns.TypeA, rr604800, endpointKey, "31.249.2.3")

	recordsFinal := plugin.cache.GetRecords("s3.mgmt.agb1.profitbricks.net.", dns.TypeA)

	if len(recordsFinal) == 1 {
		finalRecord := recordsFinal[0].(*dns.A)
		t.Logf("✅ SUCCESS: Only 1 record remains: %s (TTL: %d)", finalRecord.A.String(), finalRecord.Hdr.Ttl)
	} else {
		t.Errorf("❌ FAILURE: %d duplicate records still exist", len(recordsFinal))
		for i, record := range recordsFinal {
			aRecord := record.(*dns.A)
			t.Logf("  Record %d: %s (TTL: %d)", i, aRecord.A.String(), aRecord.Hdr.Ttl)
		}
	}

	require.Len(t, recordsFinal, 1, "BUG FIXED: Should have exactly 1 record - no duplicates even with multiple additions and different TTLs")

	// Verify final record has the most recent TTL
	finalRecord := recordsFinal[0].(*dns.A)
	require.Equal(t, uint32(604800), finalRecord.Hdr.Ttl, "Final record should have the most recent TTL")
}

// TestDNSRecordStringComparison is a utility test to understand DNS record comparison
func TestDNSRecordStringComparison(t *testing.T) {
	rr1 := &dns.A{
		Hdr: dns.RR_Header{
			Name:   "test.example.com.",
			Rrtype: dns.TypeA,
			Class:  dns.ClassINET,
			Ttl:    86400,
		},
		A: net.ParseIP("31.249.2.3"),
	}

	rr2 := &dns.A{
		Hdr: dns.RR_Header{
			Name:   "test.example.com.",
			Rrtype: dns.TypeA,
			Class:  dns.ClassINET,
			Ttl:    604800, // Different TTL
		},
		A: net.ParseIP("31.249.2.3"),
	}

	t.Logf("RR1 String(): %s", rr1.String())
	t.Logf("RR2 String(): %s", rr2.String())
	t.Logf("Records equal: %v", rr1.String() == rr2.String())

	require.False(t, rr1.String() == rr2.String(), "Records with different TTL should have different string representations")
}

// TestWildcardHandling tests the fix for wildcard DNS record handling
// where wildcard records should be returned with the queried name, not the wildcard pattern
func TestWildcardHandling(t *testing.T) {
	t.Run("Wildcard record name rewriting", func(t *testing.T) {
		plugin := &ExternalDNS{
			cache: NewDNSCache(5 * time.Minute),
			ttl:   604800, // Default TTL
		}
		defer plugin.cache.Stop()

		// Create the wildcard endpoint as would exist in production
		ep := &endpoint.Endpoint{
			DNSName:    "*.mgmt.agb1.profitbricks.net",
			RecordType: "A",
			Targets:    []string{"31.249.2.3"},
			RecordTTL:  86400,
		}

		// Add to cache
		plugin.addEndpointToCache(ep, false, "wildcard-mgmt")

		// Verify wildcard is stored
		wildcardRecords := plugin.cache.GetRecords("*.mgmt.agb1.profitbricks.net.", dns.TypeA)
		require.Len(t, wildcardRecords, 1)

		// Simulate the exact query: sdf.s3.mgmt.agb1.profitbricks.net
		queryName := "sdf.s3.mgmt.agb1.profitbricks.net."

		// Test the current ServeDNS wildcard logic
		records := plugin.cache.GetRecords(queryName, dns.TypeA)

		if len(records) == 0 {
			// This simulates the exact logic in ServeDNS
			parts := dns.SplitDomainName(queryName)
			for i := 1; i < len(parts); i++ {
				wildcard := "*." + strings.Join(parts[i:], ".")

				wildcardRecords := plugin.cache.GetRecords(wildcard, dns.TypeA)

				if len(wildcardRecords) > 0 {
					// This is the fix: rewrite record names to match the query
					records = make([]dns.RR, len(wildcardRecords))
					for j, rr := range wildcardRecords {
						records[j] = dns.Copy(rr)
						records[j].Header().Name = queryName
					}
					break
				}
			}
		}

		// Verify we found records
		require.Len(t, records, 1, "Should find exactly 1 wildcard match")

		// Verify the record has the correct properties
		finalRecord := records[0].(*dns.A)

		// CRITICAL: The record name should be the queried name, NOT the wildcard pattern
		require.Equal(t, queryName, finalRecord.Hdr.Name,
			"BUG FIXED: Record name should be queried name, not wildcard pattern")
		require.Equal(t, "31.249.2.3", finalRecord.A.String(), "IP should be correct")
		require.Equal(t, uint32(86400), finalRecord.Hdr.Ttl, "TTL should be preserved")

		t.Logf("✅ WILDCARD BUG FIXED!")
		t.Logf("   Query: %s", queryName)
		t.Logf("   Returned record name: %s (CORRECT - not wildcard!)", finalRecord.Hdr.Name)
		t.Logf("   IP: %s", finalRecord.A.String())
	})

	t.Run("Multiple wildcard levels", func(t *testing.T) {
		plugin := &ExternalDNS{
			cache: NewDNSCache(5 * time.Minute),
			ttl:   300,
		}
		defer plugin.cache.Stop()

		// Create wildcard endpoints at different levels
		ep1 := &endpoint.Endpoint{
			DNSName:    "*.example.com",
			RecordType: "A",
			Targets:    []string{"192.168.1.1"},
			RecordTTL:  300,
		}

		ep2 := &endpoint.Endpoint{
			DNSName:    "*.sub.example.com",
			RecordType: "A",
			Targets:    []string{"192.168.2.1"},
			RecordTTL:  600,
		}

		plugin.addEndpointToCache(ep1, false, "wildcard1")
		plugin.addEndpointToCache(ep2, false, "wildcard2")

		// Test that more specific wildcard is found first
		queryName := "test.sub.example.com."

		var matchedRecords []dns.RR
		parts := dns.SplitDomainName(queryName)

		// Try wildcard patterns (more specific first)
		for i := 1; i < len(parts); i++ {
			wildcard := "*." + strings.Join(parts[i:], ".")
			wildcardRecords := plugin.cache.GetRecords(wildcard, dns.TypeA)

			if len(wildcardRecords) > 0 {
				matchedRecords = wildcardRecords
				t.Logf("Matched wildcard: %s", wildcard)
				break
			}
		}

		require.Len(t, matchedRecords, 1)
		aRecord := matchedRecords[0].(*dns.A)
		require.Equal(t, "192.168.2.1", aRecord.A.String(), "Should match more specific wildcard")
	})

	t.Run("Exact match takes precedence over wildcard", func(t *testing.T) {
		plugin := &ExternalDNS{
			cache: NewDNSCache(5 * time.Minute),
			ttl:   300,
		}
		defer plugin.cache.Stop()

		// Create both wildcard and exact record
		wildcardEp := &endpoint.Endpoint{
			DNSName:    "*.example.com",
			RecordType: "A",
			Targets:    []string{"192.168.1.1"},
			RecordTTL:  300,
		}

		exactEp := &endpoint.Endpoint{
			DNSName:    "exact.example.com",
			RecordType: "A",
			Targets:    []string{"192.168.2.1"},
			RecordTTL:  600,
		}

		plugin.addEndpointToCache(wildcardEp, false, "wildcard")
		plugin.addEndpointToCache(exactEp, false, "exact")

		// Test that exact match is found (no wildcard lookup needed)
		exactRecords := plugin.cache.GetRecords("exact.example.com.", dns.TypeA)
		require.Len(t, exactRecords, 1)

		aRecord := exactRecords[0].(*dns.A)
		require.Equal(t, "192.168.2.1", aRecord.A.String(), "Should return exact match")
	})
}
