package externaldns

import (
	"context"
	"net"
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

func TestWildcardCNAMEResolution(t *testing.T) {
	config := DefaultConfig()
	plugin := New(config)

	// Create a wildcard CNAME record: *.something -> target.example.com
	endpoint := &externaldnsv1alpha1.DNSEndpoint{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-wildcard-cname",
			Namespace: "default",
		},
		Spec: externaldnsv1alpha1.DNSEndpointSpec{
			Endpoints: []*endpointv1alpha1.Endpoint{
				{
					DNSName:    "*.something.example.com",
					RecordType: "CNAME",
					Targets:    []string{"target.example.com"},
					RecordTTL:  300,
				},
			},
		},
	}

	// Add the endpoint to the cache
	err := plugin.OnAdd(endpoint)
	require.NoError(t, err)

	// Test Case 1: Query for A record on x.something.example.com
	// Should return the CNAME record
	t.Run("A query for wildcard subdomain returns CNAME", func(t *testing.T) {
		// Mock response writer to capture the response
		responseWriter := &mockResponseWriter{}

		// Create DNS query for A record
		req := new(dns.Msg)
		req.SetQuestion("x.something.example.com.", dns.TypeA)

		code, err := plugin.ServeDNS(context.Background(), responseWriter, req)

		assert.NoError(t, err)
		assert.Equal(t, dns.RcodeSuccess, code)

		// Verify we got a response
		require.NotNil(t, responseWriter.msg)
		assert.Len(t, responseWriter.msg.Answer, 1)

		// Verify it's a CNAME record with correct values
		cnameRecord, ok := responseWriter.msg.Answer[0].(*dns.CNAME)
		assert.True(t, ok, "Expected CNAME record but got %T", responseWriter.msg.Answer[0])
		assert.Equal(t, "x.something.example.com.", cnameRecord.Hdr.Name)
		assert.Equal(t, "target.example.com.", cnameRecord.Target)
		assert.Equal(t, uint32(300), cnameRecord.Hdr.Ttl)
	})

	// Test Case 2: Query for AAAA record on y.something.example.com
	// Should also return the CNAME record
	t.Run("AAAA query for wildcard subdomain returns CNAME", func(t *testing.T) {
		responseWriter := &mockResponseWriter{}

		req := new(dns.Msg)
		req.SetQuestion("y.something.example.com.", dns.TypeAAAA)

		code, err := plugin.ServeDNS(context.Background(), responseWriter, req)

		assert.NoError(t, err)
		assert.Equal(t, dns.RcodeSuccess, code)

		require.NotNil(t, responseWriter.msg)
		assert.Len(t, responseWriter.msg.Answer, 1)

		cnameRecord, ok := responseWriter.msg.Answer[0].(*dns.CNAME)
		assert.True(t, ok, "Expected CNAME record but got %T", responseWriter.msg.Answer[0])
		assert.Equal(t, "y.something.example.com.", cnameRecord.Hdr.Name)
		assert.Equal(t, "target.example.com.", cnameRecord.Target)
	})

	// Test Case 3: Direct CNAME query should work
	t.Run("CNAME query for wildcard subdomain returns CNAME", func(t *testing.T) {
		responseWriter := &mockResponseWriter{}

		req := new(dns.Msg)
		req.SetQuestion("z.something.example.com.", dns.TypeCNAME)

		code, err := plugin.ServeDNS(context.Background(), responseWriter, req)

		assert.NoError(t, err)
		assert.Equal(t, dns.RcodeSuccess, code)

		require.NotNil(t, responseWriter.msg)
		assert.Len(t, responseWriter.msg.Answer, 1)

		cnameRecord, ok := responseWriter.msg.Answer[0].(*dns.CNAME)
		assert.True(t, ok, "Expected CNAME record but got %T", responseWriter.msg.Answer[0])
		assert.Equal(t, "z.something.example.com.", cnameRecord.Hdr.Name)
		assert.Equal(t, "target.example.com.", cnameRecord.Target)
	})

	// Test Case 4: Non-wildcard domain should pass to next plugin
	t.Run("Query for non-matching domain passes to next plugin", func(t *testing.T) {
		responseWriter := &mockResponseWriter{}

		// Set up a mock next plugin
		nextHandlerCalled := false
		plugin.Next = &mockHandler{
			handler: func() (int, error) {
				nextHandlerCalled = true
				return dns.RcodeNameError, nil
			},
		}

		req := new(dns.Msg)
		req.SetQuestion("unrelated.domain.com.", dns.TypeA)

		code, err := plugin.ServeDNS(context.Background(), responseWriter, req)

		assert.NoError(t, err)
		assert.Equal(t, dns.RcodeNameError, code)
		assert.True(t, nextHandlerCalled, "Next handler should have been called")
	})

	// Test Case 5: MX query should not trigger CNAME lookup
	t.Run("MX query for wildcard subdomain passes to next plugin when no MX record exists", func(t *testing.T) {
		responseWriter := &mockResponseWriter{}

		nextHandlerCalled := false
		plugin.Next = &mockHandler{
			handler: func() (int, error) {
				nextHandlerCalled = true
				return dns.RcodeNameError, nil
			},
		}

		req := new(dns.Msg)
		req.SetQuestion("mail.something.example.com.", dns.TypeMX)

		code, err := plugin.ServeDNS(context.Background(), responseWriter, req)

		assert.NoError(t, err)
		assert.Equal(t, dns.RcodeNameError, code)
		assert.True(t, nextHandlerCalled, "Next handler should have been called for MX query")
	})

	// Test Case 6: Edge case - verify the exact scenario from the original question
	t.Run("Specific test case: x.something with wildcard *.something CNAME", func(t *testing.T) {
		responseWriter := &mockResponseWriter{}

		// Query for x.something with A record type
		req := new(dns.Msg)
		req.SetQuestion("x.something.example.com.", dns.TypeA)

		code, err := plugin.ServeDNS(context.Background(), responseWriter, req)

		// Should return success with CNAME record, not pass to next plugin
		assert.NoError(t, err)
		assert.Equal(t, dns.RcodeSuccess, code)

		require.NotNil(t, responseWriter.msg)
		assert.Len(t, responseWriter.msg.Answer, 1)

		// Verify we get the CNAME record, not an A record
		cnameRecord, ok := responseWriter.msg.Answer[0].(*dns.CNAME)
		assert.True(t, ok, "Expected CNAME record for wildcard match, but got %T", responseWriter.msg.Answer[0])
		assert.Equal(t, "x.something.example.com.", cnameRecord.Hdr.Name)
		assert.Equal(t, "target.example.com.", cnameRecord.Target)
		assert.Equal(t, dns.TypeCNAME, cnameRecord.Hdr.Rrtype)
	})
}

// Mock response writer for testing
type mockResponseWriter struct {
	msg *dns.Msg
}

func (m *mockResponseWriter) LocalAddr() net.Addr {
	addr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:53")
	return addr
}

func (m *mockResponseWriter) RemoteAddr() net.Addr {
	addr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:12345")
	return addr
}

func (m *mockResponseWriter) WriteMsg(msg *dns.Msg) error {
	m.msg = msg
	return nil
}

func (m *mockResponseWriter) Write([]byte) (int, error) {
	return 0, nil
}

func (m *mockResponseWriter) Close() error {
	return nil
}

func (m *mockResponseWriter) TsigStatus() error {
	return nil
}

func (m *mockResponseWriter) TsigTimersOnly(bool) {}

func (m *mockResponseWriter) Hijack() {}

// Mock handler for testing next plugin behavior
type mockHandler struct {
	handler func() (int, error)
}

func (m *mockHandler) Name() string {
	return "mock"
}

func (m *mockHandler) ServeDNS(ctx context.Context, w dns.ResponseWriter, r *dns.Msg) (int, error) {
	return m.handler()
}

func TestZoneDetectionWithFirstDotApproach(t *testing.T) {
	tests := []struct {
		name         string
		domain       string
		expectedZone string
	}{
		{
			name:         "Wildcard foo case - the original issue",
			domain:       "*.foo.stg.test.com",
			expectedZone: "stg.test.com.",
		},
		{
			name:         "Regular foo subdomain",
			domain:       "foo.stg.test.com",
			expectedZone: "stg.test.com.",
		},
		{
			name:         "API in staging",
			domain:       "api.stg.test.com",
			expectedZone: "stg.test.com.",
		},
		{
			name:         "Traditional 2-level domain",
			domain:       "example.com",
			expectedZone: "com.",
		},
		{
			name:         "Subdomain of 2-level domain",
			domain:       "www.example.com",
			expectedZone: "example.com.",
		},
		{
			name:         "Wildcard for 2-level zone",
			domain:       "*.example.com",
			expectedZone: "com.",
		},
		{
			name:         "Deep nesting",
			domain:       "service.api.stg.test.com",
			expectedZone: "api.stg.test.com.",
		},
		{
			name:         "Wildcard deep nesting",
			domain:       "*.service.api.stg.test.com",
			expectedZone: "api.stg.test.com.",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			zone := getZone(test.domain)
			assert.Equal(t, test.expectedZone, zone,
				"For domain %s, expected zone %s but got %s",
				test.domain, test.expectedZone, zone)
		})
	}
}

func TestSpecificZoneIssue(t *testing.T) {
	// Test the specific issue:
	// "i have added a record of *.foo.stg.test.com as CNAME
	// but the log i get for notify is for zone test.com. why is that?"

	t.Run("Zone notify should be for correct zone", func(t *testing.T) {
		domain := "*.foo.stg.test.com"

		// This was the OLD incorrect behavior
		incorrectZone := "test.com."

		// This should be the NEW correct behavior
		correctZone := "stg.test.com."

		actualZone := getZone(domain)

		assert.Equal(t, correctZone, actualZone,
			"Zone detection failed: expected %s, got %s", correctZone, actualZone)

		assert.NotEqual(t, incorrectZone, actualZone,
			"Zone should NOT be %s (that was the bug)", incorrectZone)

		t.Logf("✅ FIXED: %s now correctly maps to zone %s (was incorrectly %s)",
			domain, actualZone, incorrectZone)
	})

	// Test that it works end-to-end with the plugin
	t.Run("End-to-end zone detection in plugin", func(t *testing.T) {
		config := DefaultConfig()
		plugin := New(config)

		endpoint := &externaldnsv1alpha1.DNSEndpoint{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-foo-cname",
				Namespace: "default",
			},
			Spec: externaldnsv1alpha1.DNSEndpointSpec{
				Endpoints: []*endpointv1alpha1.Endpoint{
					{
						DNSName:    "*.foo.stg.test.com",
						RecordType: "CNAME",
						Targets:    []string{"target.example.com"},
						RecordTTL:  300,
					},
				},
			},
		}

		// Add the endpoint - this will trigger zone detection
		err := plugin.OnAdd(endpoint)
		require.NoError(t, err)

		// Check that the zone serial was set for the CORRECT zone
		plugin.serialsMutex.RLock()
		_, hasCorrectZone := plugin.zoneSerials["stg.test.com."]
		_, hasIncorrectZone := plugin.zoneSerials["test.com."]
		plugin.serialsMutex.RUnlock()

		assert.True(t, hasCorrectZone,
			"Should have zone serial for correct zone stg.test.com.")
		assert.False(t, hasIncorrectZone,
			"Should NOT have zone serial for incorrect zone test.com.")

		t.Logf("✅ Zone notify will now be for: stg.test.com. (not test.com.)")
	})
}
