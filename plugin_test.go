package externaldns

import (
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	externaldnsv1alpha1 "sigs.k8s.io/external-dns/apis/v1alpha1"
	endpointv1alpha1 "sigs.k8s.io/external-dns/endpoint"
)

func TestNewPlugin(t *testing.T) {
	config := &Config{
		Namespace: "test-namespace",
		TTL:       300,
		SOA: SOAConfig{
			Nameserver: "ns1.example.com",
			Mailbox:    "admin.example.com",
		},
		ConfigMap: ConfigMapConfig{
			Name:      "test-serials",
			Namespace: "test-namespace",
		},
	}
	plugin := New(config)

	assert.Equal(t, "externaldns", plugin.Name())
	assert.Equal(t, "test-namespace", plugin.config.Namespace)
	assert.Equal(t, uint32(300), plugin.config.TTL)
	assert.NotNil(t, plugin.cache)
	assert.NotNil(t, plugin.zoneSerials)
}

func TestRecordTypeConversion(t *testing.T) {
	config := DefaultConfig()
	plugin := New(config)

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
		{"INVALID", 0},
	}

	for _, test := range tests {
		result := plugin.recordBuilder.RecordTypeToQType(test.recordType)
		assert.Equal(t, test.expected, result, "recordType %s should convert to %d", test.recordType, test.expected)
	}
}

func TestCreateRecord(t *testing.T) {
	config := DefaultConfig()
	plugin := New(config)

	// Test A record
	rr := plugin.recordBuilder.CreateRecord("test.example.com", dns.TypeA, 300, "192.168.1.1")
	require.NotNil(t, rr)
	aRecord, ok := rr.(*dns.A)
	require.True(t, ok)
	assert.Equal(t, "192.168.1.1", aRecord.A.String())
	assert.Equal(t, uint32(300), aRecord.Hdr.Ttl)

	// Test AAAA record
	rr = plugin.recordBuilder.CreateRecord("test.example.com", dns.TypeAAAA, 300, "2001:db8::1")
	require.NotNil(t, rr)
	aaaaRecord, ok := rr.(*dns.AAAA)
	require.True(t, ok)
	assert.Equal(t, "2001:db8::1", aaaaRecord.AAAA.String())

	// Test CNAME record
	rr = plugin.recordBuilder.CreateRecord("www.example.com", dns.TypeCNAME, 600, "example.com")
	require.NotNil(t, rr)
	cnameRecord, ok := rr.(*dns.CNAME)
	require.True(t, ok)
	assert.Equal(t, "example.com.", cnameRecord.Target)

	// Test TXT record
	rr = plugin.recordBuilder.CreateRecord("test.example.com", dns.TypeTXT, 300, "v=spf1 include:_spf.google.com ~all")
	require.NotNil(t, rr)
	txtRecord, ok := rr.(*dns.TXT)
	require.True(t, ok)
	assert.Contains(t, txtRecord.Txt, "v=spf1 include:_spf.google.com ~all")
}

func TestSOARecord(t *testing.T) {
	config := DefaultConfig()
	config.SOA.Nameserver = "ns1.example.com"
	config.SOA.Mailbox = "admin.example.com"
	plugin := New(config)

	soa := plugin.createSOARecord("example.com.", 12345)
	require.NotNil(t, soa)

	soaRecord, ok := soa.(*dns.SOA)
	require.True(t, ok)
	assert.Equal(t, "ns1.example.com.", soaRecord.Ns)
	assert.Equal(t, "admin.example.com.", soaRecord.Mbox)
	assert.Equal(t, uint32(12345), soaRecord.Serial)
	assert.Equal(t, uint32(3600), soaRecord.Refresh)
	assert.Equal(t, uint32(1800), soaRecord.Retry)
	assert.Equal(t, uint32(1209600), soaRecord.Expire)
	assert.Equal(t, uint32(300), soaRecord.Minttl)
}

func TestPTRRecord(t *testing.T) {
	config := DefaultConfig()
	plugin := New(config)

	ptrRR := plugin.recordBuilder.CreatePTRRecord("192.168.1.10", "app.example.com", 300)
	require.NotNil(t, ptrRR)

	ptrRecord, ok := ptrRR.(*dns.PTR)
	require.True(t, ok)
	assert.Equal(t, "app.example.com.", ptrRecord.Ptr)
	assert.Equal(t, "10.1.168.192.in-addr.arpa.", ptrRecord.Hdr.Name)
}

func TestZoneSerialBasic(t *testing.T) {
	config := DefaultConfig()
	plugin := New(config)

	// Test that zone serials map is initialized
	assert.NotNil(t, plugin.zoneSerials)

	// Test that we can access the serials (even if empty initially)
	plugin.serialsMutex.RLock()
	serials := plugin.zoneSerials
	plugin.serialsMutex.RUnlock()

	assert.NotNil(t, serials)
}

func TestCacheBasic(t *testing.T) {
	config := DefaultConfig()
	plugin := New(config)

	// Test that cache is initialized
	assert.NotNil(t, plugin.cache)
}

func TestPluginName(t *testing.T) {
	config := DefaultConfig()
	plugin := New(config)
	assert.Equal(t, "externaldns", plugin.Name())
}

func TestSerialGeneration(t *testing.T) {
	config := &Config{
		TTL:       300,
		Namespace: "test",
		SOA: SOAConfig{
			Nameserver: "ns1.example.com",
			Mailbox:    "admin.example.com",
		},
		ConfigMap: ConfigMapConfig{
			Name:      "test-serials",
			Namespace: "test",
		},
	}

	plugin := New(config)

	// Create test endpoint
	now := time.Now()
	endpoint := &externaldnsv1alpha1.DNSEndpoint{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-endpoint",
			Namespace:         "test",
			CreationTimestamp: metav1.NewTime(now),
			Generation:        1,
			ResourceVersion:   "12345",
		},
		Spec: externaldnsv1alpha1.DNSEndpointSpec{
			Endpoints: []*endpointv1alpha1.Endpoint{
				{
					DNSName:    "test.example.com",
					Targets:    []string{"1.2.3.4"},
					RecordType: "A",
				},
			},
		},
	}

	t.Run("Add event with no existing serial uses resource metadata", func(t *testing.T) {
		serial := plugin.generateSerial(endpoint, watch.Added)
		// Should be creation time + generation (no ResourceVersion hash anymore)
		expectedSerial := uint32(now.Unix()) + 1
		if serial != expectedSerial {
			t.Errorf("Expected serial %d, got %d", expectedSerial, serial)
		}
	})

	t.Run("Add event with same resource is idempotent", func(t *testing.T) {
		// First add
		serial1 := plugin.generateSerial(endpoint, watch.Added)

		// Second add of same resource (watcher restart scenario)
		serial2 := plugin.generateSerial(endpoint, watch.Added)

		// Should be exactly the same
		if serial1 != serial2 {
			t.Errorf("Expected same serial for identical resource, got %d and %d", serial1, serial2)
		}
	})

	t.Run("Add event with newer resource updates serial", func(t *testing.T) {
		// Set existing serial lower than resource serial
		resourceSerial := plugin.calculateResourceSerial(endpoint)
		plugin.serialsMutex.Lock()
		plugin.zoneSerials["example.com."] = resourceSerial - 100
		plugin.serialsMutex.Unlock()

		serial := plugin.generateSerial(endpoint, watch.Added)
		if serial != resourceSerial {
			t.Errorf("Expected resource serial %d, got %d", resourceSerial, serial)
		}
	})

	t.Run("Add event with older resource still uses resource serial", func(t *testing.T) {
		// Set existing serial higher than resource serial
		resourceSerial := plugin.calculateResourceSerial(endpoint)
		higherSerial := resourceSerial + 100
		plugin.serialsMutex.Lock()
		plugin.zoneSerials["example.com."] = higherSerial
		plugin.serialsMutex.Unlock()

		// ADD events always use resource serial for idempotency
		serial := plugin.generateSerial(endpoint, watch.Added)
		if serial != resourceSerial {
			t.Errorf("Expected resource serial %d, got %d", resourceSerial, serial)
		}

		// Note: The OnAdd function will only update zone serial if resource serial is higher
		// This maintains the "never go backwards" property at the zone level
	})

	t.Run("Update event always increments", func(t *testing.T) {
		// Set existing serial
		plugin.serialsMutex.Lock()
		plugin.zoneSerials["example.com."] = 1500
		plugin.serialsMutex.Unlock()

		serial := plugin.generateSerial(endpoint, watch.Modified)
		expectedSerial := uint32(1501) // increment existing serial
		if serial != expectedSerial {
			t.Errorf("Expected serial %d, got %d", expectedSerial, serial)
		}
	})

	t.Run("Delete event always increments", func(t *testing.T) {
		// Set existing serial
		plugin.serialsMutex.Lock()
		plugin.zoneSerials["example.com."] = 2000
		plugin.serialsMutex.Unlock()

		serial := plugin.generateSerial(endpoint, watch.Deleted)
		expectedSerial := uint32(2001) // increment existing serial
		if serial != expectedSerial {
			t.Errorf("Expected serial %d, got %d", expectedSerial, serial)
		}
	})

	t.Run("Watcher restart scenario - no serial inflation", func(t *testing.T) {
		// Simulate watcher restart: same resources get ADD events again
		resourceSerial := plugin.calculateResourceSerial(endpoint)

		// Set initial state with resource serial
		plugin.serialsMutex.Lock()
		plugin.zoneSerials["example.com."] = resourceSerial
		plugin.serialsMutex.Unlock()

		// Simulate watcher restart - same endpoint gets ADD event again
		serialAfterRestart := plugin.generateSerial(endpoint, watch.Added)

		// Should NOT increment - should stay the same
		if serialAfterRestart != resourceSerial {
			t.Errorf("Serial should not change on watcher restart. Expected %d, got %d", resourceSerial, serialAfterRestart)
		}
	})

	t.Run("Multiple zones use highest serial", func(t *testing.T) {
		// Set different serials for different zones
		plugin.serialsMutex.Lock()
		plugin.zoneSerials["example.com."] = 1000
		plugin.zoneSerials["test.com."] = 2000
		plugin.serialsMutex.Unlock()

		// Create endpoint affecting both zones
		multiZoneEndpoint := &externaldnsv1alpha1.DNSEndpoint{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "multi-zone",
				Namespace:         "test",
				CreationTimestamp: metav1.NewTime(now),
				Generation:        1,
				ResourceVersion:   "67890",
			},
			Spec: externaldnsv1alpha1.DNSEndpointSpec{
				Endpoints: []*endpointv1alpha1.Endpoint{
					{
						DNSName:    "service.example.com",
						Targets:    []string{"1.2.3.6"},
						RecordType: "A",
					},
					{
						DNSName:    "service.test.com",
						Targets:    []string{"1.2.3.7"},
						RecordType: "A",
					},
				},
			},
		}

		serial := plugin.generateSerial(multiZoneEndpoint, watch.Modified)
		expectedSerial := uint32(2001) // highest existing serial + 1
		if serial != expectedSerial {
			t.Errorf("Expected serial %d, got %d", expectedSerial, serial)
		}
	})
}

func TestWatcherRestartScenario(t *testing.T) {
	config := &Config{
		TTL:       300,
		Namespace: "test",
		SOA: SOAConfig{
			Nameserver: "ns1.example.com",
			Mailbox:    "admin.example.com",
		},
		ConfigMap: ConfigMapConfig{
			Name:      "test-serials",
			Namespace: "test",
		},
	}

	plugin := New(config)

	// Create some test endpoints that would exist before watcher restart
	now := time.Now()
	endpoints := []*externaldnsv1alpha1.DNSEndpoint{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "app1",
				Namespace:         "test",
				CreationTimestamp: metav1.NewTime(now.Add(-1 * time.Hour)),
				Generation:        1,
				ResourceVersion:   "1001",
			},
			Spec: externaldnsv1alpha1.DNSEndpointSpec{
				Endpoints: []*endpointv1alpha1.Endpoint{
					{
						DNSName:    "app1.example.com",
						Targets:    []string{"1.2.3.4"},
						RecordType: "A",
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "app2",
				Namespace:         "test",
				CreationTimestamp: metav1.NewTime(now.Add(-30 * time.Minute)),
				Generation:        2,
				ResourceVersion:   "1002",
			},
			Spec: externaldnsv1alpha1.DNSEndpointSpec{
				Endpoints: []*endpointv1alpha1.Endpoint{
					{
						DNSName:    "app2.example.com",
						Targets:    []string{"1.2.3.5"},
						RecordType: "A",
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "app3",
				Namespace:         "test",
				CreationTimestamp: metav1.NewTime(now.Add(-10 * time.Minute)),
				Generation:        1,
				ResourceVersion:   "1003",
			},
			Spec: externaldnsv1alpha1.DNSEndpointSpec{
				Endpoints: []*endpointv1alpha1.Endpoint{
					{
						DNSName:    "app3.example.com",
						Targets:    []string{"1.2.3.6"},
						RecordType: "A",
					},
				},
			},
		},
	}

	t.Run("Simulate initial watcher startup", func(t *testing.T) {
		// All endpoints get ADD events during initial startup
		// Each should establish its own resource-based serial
		resourceSerials := make([]uint32, len(endpoints))

		for i, endpoint := range endpoints {
			// Calculate what the resource serial should be
			resourceSerial := plugin.calculateResourceSerial(endpoint)
			resourceSerials[i] = resourceSerial

			// Process the ADD event
			serial := plugin.generateSerial(endpoint, watch.Added)

			// For initial adds, should get the resource serial
			if serial != resourceSerial {
				t.Errorf("Initial ADD for %s: expected resource serial %d, got %d", endpoint.Name, resourceSerial, serial)
			}

			// Simulate what OnAdd would do - only update zone serial if higher
			plugin.serialsMutex.Lock()
			if currentSerial, exists := plugin.zoneSerials["example.com."]; !exists || serial > currentSerial {
				plugin.zoneSerials["example.com."] = serial
			}
			plugin.serialsMutex.Unlock()

			t.Logf("Initial ADD: %s -> resource serial %d", endpoint.Name, resourceSerial)
		}

		// Record the final serial after initial startup (should be the highest resource serial)
		plugin.serialsMutex.RLock()
		finalSerial := plugin.zoneSerials["example.com."]
		plugin.serialsMutex.RUnlock()

		// Find the highest resource serial to compare
		maxResourceSerial := uint32(0)
		for _, rs := range resourceSerials {
			if rs > maxResourceSerial {
				maxResourceSerial = rs
			}
		}

		if finalSerial != maxResourceSerial {
			t.Errorf("Final zone serial %d should equal highest resource serial %d", finalSerial, maxResourceSerial)
		}

		t.Logf("Final zone serial after initial startup: %d", finalSerial)

		t.Run("Simulate watcher restart - no serial inflation", func(t *testing.T) {
			// Watcher restarts and replays all existing endpoints as ADD events
			for i, endpoint := range endpoints {
				// Each resource should still get its own resource-based serial
				expectedResourceSerial := resourceSerials[i]
				serial := plugin.generateSerial(endpoint, watch.Added)

				if serial != expectedResourceSerial {
					t.Errorf("Restart ADD for %s: expected same resource serial %d, got %d",
						endpoint.Name, expectedResourceSerial, serial)
				}

				t.Logf("Restart ADD: %s -> resource serial %d (idempotent)", endpoint.Name, serial)
			}

			// Zone serial should remain the same (no inflation)
			plugin.serialsMutex.RLock()
			finalSerialAfterRestart := plugin.zoneSerials["example.com."]
			plugin.serialsMutex.RUnlock()

			if finalSerialAfterRestart != finalSerial {
				t.Errorf("Zone serial changed after watcher restart: was %d, now %d", finalSerial, finalSerialAfterRestart)
			}

			t.Logf("✅ Zone serial unchanged after watcher restart: %d", finalSerialAfterRestart)
		})

		t.Run("Real changes still increment serial", func(t *testing.T) {
			// Now simulate an actual update to one of the endpoints
			updatedEndpoint := &externaldnsv1alpha1.DNSEndpoint{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "app1",
					Namespace:         "test",
					CreationTimestamp: endpoints[0].CreationTimestamp, // Same creation time
					Generation:        2,                              // But incremented generation
					ResourceVersion:   "1004",                         // New resource version
				},
				Spec: externaldnsv1alpha1.DNSEndpointSpec{
					Endpoints: []*endpointv1alpha1.Endpoint{
						{
							DNSName:    "app1.example.com",
							Targets:    []string{"1.2.3.7"}, // Changed target
							RecordType: "A",
						},
					},
				},
			}

			updateSerial := plugin.generateSerial(updatedEndpoint, watch.Modified)
			if updateSerial <= finalSerial {
				t.Errorf("Update should increment serial: was %d, update is %d", finalSerial, updateSerial)
			}

			t.Logf("Real update correctly incremented serial: %d -> %d", finalSerial, updateSerial)
		})
	})
}
