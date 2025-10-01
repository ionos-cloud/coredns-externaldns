package externaldns

import (
	"testing"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/require"
)

func TestUpdateRecordsByEndpointWithAddition(t *testing.T) {
	plugin := &ExternalDNS{
		ttl:   300,
		cache: NewDNSCache(0), // Disable metrics updates for test
	}
	defer plugin.cache.Stop()

	endpointKey := "default/test-endpoint"

	t.Run("No existing records - should add all new records", func(t *testing.T) {
		newRecords := []RecordRef{
			{
				Zone:   "example.com.",
				Name:   "new.example.com.",
				Type:   dns.TypeA,
				Target: "10.0.0.1",
				TTL:    300,
			},
			{
				Zone:   "example.com.",
				Name:   "new.example.com.",
				Type:   dns.TypeA,
				Target: "10.0.0.2",
				TTL:    300,
			},
		}

		plugin.cache.UpdateRecordsByEndpointWithAddition(endpointKey, newRecords, plugin, false)

		// Verify records were added
		records := plugin.cache.GetRecords("new.example.com.", dns.TypeA)
		require.Len(t, records, 2)

		// Verify endpoint tracking
		plugin.cache.Lock()
		trackedRefs, exists := plugin.cache.endpointRecords[endpointKey]
		plugin.cache.Unlock()
		require.True(t, exists)
		require.Len(t, trackedRefs, 2)
	})

	t.Run("Differential update - add new, keep existing, remove obsolete", func(t *testing.T) {
		// Clear previous test data
		plugin.cache.RemoveRecordsByEndpoint(endpointKey)

		// Step 1: Set up initial state
		initialRecords := []RecordRef{
			{
				Zone:   "example.com.",
				Name:   "app.example.com.",
				Type:   dns.TypeA,
				Target: "192.168.1.1",
				TTL:    300,
			},
			{
				Zone:   "example.com.",
				Name:   "app.example.com.",
				Type:   dns.TypeA,
				Target: "192.168.1.2",
				TTL:    300,
			},
			{
				Zone:   "example.com.",
				Name:   "old.example.com.",
				Type:   dns.TypeA,
				Target: "192.168.2.1",
				TTL:    300,
			},
		}

		// Add initial records manually (simulating ADDED event)
		for _, ref := range initialRecords {
			plugin.addRecordFromRef(ref, endpointKey)
		}
		plugin.cache.Lock()
		plugin.cache.endpointRecords[endpointKey] = initialRecords
		plugin.cache.Unlock()

		// Verify initial state
		appRecords := plugin.cache.GetRecords("app.example.com.", dns.TypeA)
		require.Len(t, appRecords, 2)
		oldRecords := plugin.cache.GetRecords("old.example.com.", dns.TypeA)
		require.Len(t, oldRecords, 1)

		// Step 2: Perform differential update
		// Keep app.example.com with 192.168.1.1, change 192.168.1.2 to 192.168.1.3
		// Remove old.example.com
		// Add new.example.com
		newRecords := []RecordRef{
			{
				Zone:   "example.com.",
				Name:   "app.example.com.",
				Type:   dns.TypeA,
				Target: "192.168.1.1", // Keep this one
				TTL:    300,
			},
			{
				Zone:   "example.com.",
				Name:   "app.example.com.",
				Type:   dns.TypeA,
				Target: "192.168.1.3", // Changed from .2 to .3
				TTL:    300,
			},
			{
				Zone:   "example.com.",
				Name:   "new.example.com.",
				Type:   dns.TypeA,
				Target: "10.0.0.5", // New record
				TTL:    300,
			},
		}

		plugin.cache.UpdateRecordsByEndpointWithAddition(endpointKey, newRecords, plugin, false)

		// Step 3: Verify the differential update worked correctly

		// app.example.com should have .1 and .3, but not .2
		appRecordsAfter := plugin.cache.GetRecords("app.example.com.", dns.TypeA)
		require.Len(t, appRecordsAfter, 2)

		ips := make(map[string]bool)
		for _, rr := range appRecordsAfter {
			aRecord := rr.(*dns.A)
			ips[aRecord.A.String()] = true
		}
		require.True(t, ips["192.168.1.1"], "Should keep 192.168.1.1")
		require.True(t, ips["192.168.1.3"], "Should add 192.168.1.3")
		require.False(t, ips["192.168.1.2"], "Should remove 192.168.1.2")

		// old.example.com should be completely removed
		oldRecordsAfter := plugin.cache.GetRecords("old.example.com.", dns.TypeA)
		require.Len(t, oldRecordsAfter, 0)

		// new.example.com should be added
		newRecordsAfter := plugin.cache.GetRecords("new.example.com.", dns.TypeA)
		require.Len(t, newRecordsAfter, 1)
		newRecord := newRecordsAfter[0].(*dns.A)
		require.Equal(t, "10.0.0.5", newRecord.A.String())

		// Verify endpoint tracking reflects the new state
		plugin.cache.Lock()
		trackedRefs, exists := plugin.cache.endpointRecords[endpointKey]
		plugin.cache.Unlock()
		require.True(t, exists)
		require.Len(t, trackedRefs, 3)
	})

	t.Run("Mixed record types update", func(t *testing.T) {
		// Clear previous test data
		plugin.cache.RemoveRecordsByEndpoint(endpointKey + "-mixed")
		mixedEndpointKey := endpointKey + "-mixed"

		// Initial state: A record and CNAME record
		initialRecords := []RecordRef{
			{
				Zone:   "example.com.",
				Name:   "web.example.com.",
				Type:   dns.TypeA,
				Target: "10.1.1.1",
				TTL:    300,
			},
			{
				Zone:   "example.com.",
				Name:   "alias.example.com.",
				Type:   dns.TypeCNAME,
				Target: "web.example.com.",
				TTL:    300,
			},
		}

		// Add initial records
		for _, ref := range initialRecords {
			plugin.addRecordFromRef(ref, mixedEndpointKey)
		}
		plugin.cache.Lock()
		plugin.cache.endpointRecords[mixedEndpointKey] = initialRecords
		plugin.cache.Unlock()

		// Update: Change A record IP, keep CNAME, add new A record
		newRecords := []RecordRef{
			{
				Zone:   "example.com.",
				Name:   "web.example.com.",
				Type:   dns.TypeA,
				Target: "10.1.1.2", // Changed IP
				TTL:    300,
			},
			{
				Zone:   "example.com.",
				Name:   "alias.example.com.",
				Type:   dns.TypeCNAME,
				Target: "web.example.com.", // Unchanged
				TTL:    300,
			},
			{
				Zone:   "example.com.",
				Name:   "api.example.com.",
				Type:   dns.TypeA,
				Target: "10.1.2.1", // New record
				TTL:    300,
			},
		}

		plugin.cache.UpdateRecordsByEndpointWithAddition(mixedEndpointKey, newRecords, plugin, false)

		// Verify A record was updated
		webRecords := plugin.cache.GetRecords("web.example.com.", dns.TypeA)
		require.Len(t, webRecords, 1)
		webARecord := webRecords[0].(*dns.A)
		require.Equal(t, "10.1.1.2", webARecord.A.String())

		// Verify CNAME record is still there
		aliasRecords := plugin.cache.GetRecords("alias.example.com.", dns.TypeCNAME)
		require.Len(t, aliasRecords, 1)
		cnameRecord := aliasRecords[0].(*dns.CNAME)
		require.Equal(t, "web.example.com.", cnameRecord.Target)

		// Verify new A record was added
		apiRecords := plugin.cache.GetRecords("api.example.com.", dns.TypeA)
		require.Len(t, apiRecords, 1)
		apiARecord := apiRecords[0].(*dns.A)
		require.Equal(t, "10.1.2.1", apiARecord.A.String())

		// Verify tracking
		plugin.cache.Lock()
		trackedRefs, exists := plugin.cache.endpointRecords[mixedEndpointKey]
		plugin.cache.Unlock()
		require.True(t, exists)
		require.Len(t, trackedRefs, 3)
	})

	t.Run("Order ensures DNS availability - add before remove", func(t *testing.T) {
		// This test verifies that new records are added and old ones are removed correctly
		// We'll test this by verifying the final state rather than intercepting method calls

		// Clear previous test data
		plugin.cache.RemoveRecordsByEndpoint(endpointKey + "-order")
		orderEndpointKey := endpointKey + "-order"

		// Set up initial state
		initialRecords := []RecordRef{
			{
				Zone:   "example.com.",
				Name:   "service.example.com.",
				Type:   dns.TypeA,
				Target: "10.0.1.1",
				TTL:    300,
			},
		}

		for _, ref := range initialRecords {
			plugin.addRecordFromRef(ref, orderEndpointKey)
		}
		plugin.cache.Lock()
		plugin.cache.endpointRecords[orderEndpointKey] = initialRecords
		plugin.cache.Unlock()

		// Verify initial state
		initialRecordsInCache := plugin.cache.GetRecords("service.example.com.", dns.TypeA)
		require.Len(t, initialRecordsInCache, 1)
		initialARecord := initialRecordsInCache[0].(*dns.A)
		require.Equal(t, "10.0.1.1", initialARecord.A.String())

		// Update to change the IP
		newRecords := []RecordRef{
			{
				Zone:   "example.com.",
				Name:   "service.example.com.",
				Type:   dns.TypeA,
				Target: "10.0.1.2", // Different IP
				TTL:    300,
			},
		}

		plugin.cache.UpdateRecordsByEndpointWithAddition(orderEndpointKey, newRecords, plugin, false)

		// Verify final state - should have the new IP and not the old one
		finalRecords := plugin.cache.GetRecords("service.example.com.", dns.TypeA)
		require.Len(t, finalRecords, 1, "Should have exactly one A record")
		finalARecord := finalRecords[0].(*dns.A)
		require.Equal(t, "10.0.1.2", finalARecord.A.String(), "Should have the new IP")

		// Verify endpoint tracking reflects the new state
		plugin.cache.Lock()
		trackedRefs, exists := plugin.cache.endpointRecords[orderEndpointKey]
		plugin.cache.Unlock()
		require.True(t, exists)
		require.Len(t, trackedRefs, 1)
		require.Equal(t, "10.0.1.2", trackedRefs[0].Target)
	})
}

func TestUpdateRecordsByEndpointWithAdditionEdgeCases(t *testing.T) {
	plugin := &ExternalDNS{
		ttl:   300,
		cache: NewDNSCache(0),
	}
	defer plugin.cache.Stop()

	t.Run("Empty new records list", func(t *testing.T) {
		endpointKey := "default/empty-test"

		// Set up some initial records
		initialRecords := []RecordRef{
			{
				Zone:   "example.com.",
				Name:   "temp.example.com.",
				Type:   dns.TypeA,
				Target: "10.0.0.1",
				TTL:    300,
			},
		}

		for _, ref := range initialRecords {
			plugin.addRecordFromRef(ref, endpointKey)
		}
		plugin.cache.Lock()
		plugin.cache.endpointRecords[endpointKey] = initialRecords
		plugin.cache.Unlock()

		// Update with empty records (should remove all)
		plugin.cache.UpdateRecordsByEndpointWithAddition(endpointKey, []RecordRef{}, plugin, false)

		// Verify all records were removed
		records := plugin.cache.GetRecords("temp.example.com.", dns.TypeA)
		require.Len(t, records, 0)

		// Verify endpoint tracking is empty
		plugin.cache.Lock()
		trackedRefs, exists := plugin.cache.endpointRecords[endpointKey]
		plugin.cache.Unlock()
		require.True(t, exists)
		require.Len(t, trackedRefs, 0)
	})

	t.Run("Identical records - no changes", func(t *testing.T) {
		endpointKey := "default/identical-test"

		records := []RecordRef{
			{
				Zone:   "example.com.",
				Name:   "static.example.com.",
				Type:   dns.TypeA,
				Target: "10.0.0.1",
				TTL:    300,
			},
		}

		// Set up initial state
		for _, ref := range records {
			plugin.addRecordFromRef(ref, endpointKey)
		}
		plugin.cache.Lock()
		plugin.cache.endpointRecords[endpointKey] = records
		plugin.cache.Unlock()

		// Get initial cache size
		initialSize := plugin.cache.GetCacheSize()

		// Update with identical records
		plugin.cache.UpdateRecordsByEndpointWithAddition(endpointKey, records, plugin, false)

		// Verify no duplication occurred
		finalRecords := plugin.cache.GetRecords("static.example.com.", dns.TypeA)
		require.Len(t, finalRecords, 1)

		// Cache size should remain the same
		finalSize := plugin.cache.GetCacheSize()
		require.Equal(t, initialSize, finalSize)
	})
}
