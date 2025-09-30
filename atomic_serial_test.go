package externaldns

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestAtomicSerialUpdate(t *testing.T) {
	// Create plugin instance
	plugin := &ExternalDNS{
		ttl:   300,
		cache: NewDNSCache(0), // Disable metrics updates for test
	}
	defer plugin.cache.Stop()

	// Create a DNSEndpoint with multiple records in the same zone
	dnsEndpoint := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "externaldns.k8s.io/v1alpha1",
			"kind":       "DNSEndpoint",
			"metadata": map[string]interface{}{
				"name":              "multi-record-endpoint",
				"namespace":         "default",
				"creationTimestamp": "2025-07-29T10:30:45Z",
				"generation":        int64(50),
				"resourceVersion":   "123456",
			},
			"spec": map[string]interface{}{
				"endpoints": []interface{}{
					map[string]interface{}{
						"dnsName":    "a.example.com",
						"recordType": "A",
						"targets":    []interface{}{"1.2.3.4"},
					},
					map[string]interface{}{
						"dnsName":    "b.example.com",
						"recordType": "A",
						"targets":    []interface{}{"5.6.7.8"},
					},
					map[string]interface{}{
						"dnsName":    "c.example.com",
						"recordType": "CNAME",
						"targets":    []interface{}{"target.example.com"},
					},
				},
			},
		},
	}

	ep, err := unstructuredToDNSEndpoint(dnsEndpoint)
	require.NoError(t, err, "Failed to convert unstructured to DNSEndpoint")

	// Get the expected consistent serial
	expectedSerial := suggestSerialNumber(ep)
	t.Logf("Expected consistent serial: %d", expectedSerial)

	// Process the endpoint as ADDED
	plugin.processDNSEndpoint(t.Context(), dnsEndpoint, "ADDED")

	// Verify that all zones have the same serial (the consistent one)
	zoneName := "example.com."

	plugin.cache.RLock()
	zone, exists := plugin.cache.zones[zoneName]
	plugin.cache.RUnlock()

	require.True(t, exists, "Zone %s was not created", zoneName)

	zone.RLock()
	actualSerial := zone.Serial
	zone.RUnlock()

	require.Equal(t, expectedSerial, actualSerial, "Zone serial mismatch")

	// Verify all records exist
	records := []string{"a.example.com.", "b.example.com.", "c.example.com."}
	for _, recordName := range records {
		var recordType uint16
		if recordName == "c.example.com." {
			recordType = 5 // CNAME
		} else {
			recordType = 1 // A
		}

		cachedRecords := plugin.cache.GetRecords(recordName, recordType)
		require.Greater(t, len(cachedRecords), 0, "Record %s not found in cache", recordName)
	}

	t.Logf("All records successfully added with atomic serial update: %d", actualSerial)
}

func TestAtomicSerialUpdateOnModification(t *testing.T) {
	// Create plugin instance
	plugin := &ExternalDNS{
		ttl:   300,
		cache: NewDNSCache(0), // Disable metrics updates for test
	}
	defer plugin.cache.Stop()

	// Create initial DNSEndpoint
	dnsEndpoint := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "externaldns.k8s.io/v1alpha1",
			"kind":       "DNSEndpoint",
			"metadata": map[string]interface{}{
				"name":              "test-endpoint",
				"namespace":         "default",
				"creationTimestamp": "2025-07-29T10:30:45Z",
				"generation":        int64(1),
				"resourceVersion":   "123456",
			},
			"spec": map[string]interface{}{
				"endpoints": []interface{}{
					map[string]interface{}{
						"dnsName":    "old.example.com",
						"recordType": "A",
						"targets":    []interface{}{"1.2.3.4"},
					},
				},
			},
		},
	}

	// Add initial endpoint
	plugin.processDNSEndpoint(t.Context(), dnsEndpoint, "ADDED")

	// Get initial serial
	zoneName := "example.com."
	plugin.cache.RLock()
	zone := plugin.cache.zones[zoneName]
	plugin.cache.RUnlock()

	zone.RLock()
	initialSerial := zone.Serial
	zone.RUnlock()

	// Wait a bit to ensure different timestamp
	time.Sleep(10 * time.Millisecond)

	// Modify the endpoint (increment generation)
	dnsEndpoint.Object["metadata"].(map[string]interface{})["generation"] = int64(2)
	dnsEndpoint.Object["spec"] = map[string]interface{}{
		"endpoints": []interface{}{
			map[string]interface{}{
				"dnsName":    "new.example.com",
				"recordType": "A",
				"targets":    []interface{}{"5.6.7.8"},
			},
		},
	}

	ep, err := unstructuredToDNSEndpoint(dnsEndpoint)
	require.NoError(t, err, "Failed to convert unstructured to DNSEndpoint")

	expectedSerial := suggestSerialNumber(ep)
	t.Logf("Initial serial: %d, Expected new serial: %d", initialSerial, expectedSerial)

	// Process as MODIFIED
	plugin.processDNSEndpoint(t.Context(), dnsEndpoint, "MODIFIED")

	// Verify atomic serial update
	zone.RLock()
	finalSerial := zone.Serial
	zone.RUnlock()

	require.Equal(t, expectedSerial, finalSerial, "Serial not updated atomically")

	// Verify old record is gone and new record exists
	oldRecords := plugin.cache.GetRecords("old.example.com.", 1)
	require.Equal(t, 0, len(oldRecords), "Old record still exists after modification")

	newRecords := plugin.cache.GetRecords("new.example.com.", 1)
	require.Greater(t, len(newRecords), 0, "New record not found after modification")

	t.Logf("Atomic modification completed successfully. Serial: %d", finalSerial)
}
