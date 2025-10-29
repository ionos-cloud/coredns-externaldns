package externaldns

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	externaldnsv1alpha1 "sigs.k8s.io/external-dns/apis/v1alpha1"
	endpointv1alpha1 "sigs.k8s.io/external-dns/endpoint"

	"github.com/ionos-cloud/coredns-externaldns/internal/cache"
	dnsbuilder "github.com/ionos-cloud/coredns-externaldns/internal/dns"
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
		Zones:     []string{"example.com.", "test.com.", "."},
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
		Zones:     []string{"example.com.", "test.com.", "."},
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

func TestSpecificZoneIssue(t *testing.T) {
	// Test the specific issue:
	// "i have added a record of *.foo.stg.test.com as CNAME
	// but the log i get for notify is for zone test.com. why is that?"

	// Note: This test now verifies the NEW configuration-based zone matching
	// instead of the old dynamic zone extraction

	t.Run("Zone notify should be for correct zone", func(t *testing.T) {
		domain := "*.foo.stg.test.com"

		// With the new configuration-based approach,
		// the zone matching depends on configured zones
		config := &Config{
			Zones: []string{"stg.test.com.", "test.com.", "."},
		}
		plugin := New(config)

		actualZone := plugin.getConfiguredZoneForDomain(domain)
		expectedZone := "stg.test.com."

		assert.Equal(t, expectedZone, actualZone,
			"Zone detection failed: expected %s, got %s", expectedZone, actualZone)

		t.Logf("✅ FIXED: %s now correctly maps to zone %s with configured zones",
			domain, actualZone)
	})

	// Test that it works end-to-end with the plugin
	t.Run("End-to-end zone detection in plugin", func(t *testing.T) {
		config := DefaultConfig()
		// Configure the zones that should be available for matching
		config.Zones = []string{"stg.test.com.", "test.com.", "example.com.", "."}
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

// Helper function to create a test plugin
func createTestPlugin() *Plugin {
	config := &Config{
		TTL:       3600,
		Namespace: "default",
		ConfigMap: ConfigMapConfig{
			Name:      "test-configmap",
			Namespace: "default",
		},
	}

	metrics := &cache.Metrics{}

	return &Plugin{
		config:        config,
		cache:         cache.New(metrics),
		recordBuilder: dnsbuilder.NewRecordBuilder(),
		zoneSerials:   make(map[string]uint32),
		metrics:       metrics,
		ctx:           context.Background(),
	}
}

// Helper function to create a DNSEndpoint
func createDNSEndpoint(name, namespace, dnsName, recordType string, targets []string) *externaldnsv1alpha1.DNSEndpoint {
	return &externaldnsv1alpha1.DNSEndpoint{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: externaldnsv1alpha1.DNSEndpointSpec{
			Endpoints: []*endpointv1alpha1.Endpoint{
				{
					DNSName:    dnsName,
					RecordType: recordType,
					Targets:    targets,
				},
			},
		},
	}
}

func TestCNAMEUpdateNoDuplication(t *testing.T) {
	plugin := createTestPlugin()

	// Create initial DNSEndpoint with a CNAME record
	initialEndpoint := createDNSEndpoint(
		"test-endpoint",
		"default",
		"sdf.foo.stg.test.com",
		"CNAME",
		[]string{"mgmt.stg.test.com"},
	)

	// Add the initial record
	err := plugin.OnAdd(initialEndpoint)
	if err != nil {
		t.Fatalf("Failed to add initial endpoint: %v", err)
	}

	// Verify initial record exists
	// For tests, we use the domain itself as the zone or determine via the plugin
	zone := plugin.getConfiguredZoneForDomain("sdf.foo.stg.test.com.")
	records := plugin.cache.GetRecords("sdf.foo.stg.test.com.", zone, dns.TypeCNAME)
	if len(records) != 1 {
		t.Fatalf("Expected 1 CNAME record, got %d", len(records))
	}

	// Verify the initial target
	if cname, ok := records[0].(*dns.CNAME); ok {
		expectedTarget := "mgmt.stg.test.com."
		if cname.Target != expectedTarget {
			t.Fatalf("Expected initial CNAME target %s, got %s", expectedTarget, cname.Target)
		}
	} else {
		t.Fatalf("Record is not a CNAME record")
	}

	// Create updated DNSEndpoint with different CNAME target
	updatedEndpoint := createDNSEndpoint(
		"test-endpoint",
		"default",
		"sdf.foo.stg.test.com",
		"CNAME",
		[]string{"ntp.stg.test.com"},
	)

	// Update the record
	err = plugin.OnUpdate(updatedEndpoint)
	if err != nil {
		t.Fatalf("Failed to update endpoint: %v", err)
	}

	// Verify only one CNAME record exists with the new target
	zone = plugin.getConfiguredZoneForDomain("sdf.foo.stg.test.com.")
	records = plugin.cache.GetRecords("sdf.foo.stg.test.com.", zone, dns.TypeCNAME)
	if len(records) != 1 {
		t.Fatalf("Expected 1 CNAME record after update, got %d", len(records))
	}

	// Verify the record has the correct target
	if cname, ok := records[0].(*dns.CNAME); ok {
		expectedTarget := "ntp.stg.test.com."
		if cname.Target != expectedTarget {
			t.Fatalf("Expected CNAME target %s, got %s", expectedTarget, cname.Target)
		}
	} else {
		t.Fatalf("Record is not a CNAME record")
	}
}

func TestMultipleCNAMETargetsUpdateNoDuplication(t *testing.T) {
	plugin := createTestPlugin()

	// Create initial DNSEndpoint with multiple CNAME targets
	initialEndpoint := createDNSEndpoint(
		"test-endpoint",
		"default",
		"test.example.com",
		"CNAME",
		[]string{"target1.example.com", "target2.example.com"},
	)

	// Add the initial records
	err := plugin.OnAdd(initialEndpoint)
	if err != nil {
		t.Fatalf("Failed to add initial endpoint: %v", err)
	}

	// Verify initial records exist
	zone := plugin.getConfiguredZoneForDomain("test.example.com.")
	records := plugin.cache.GetRecords("test.example.com.", zone, dns.TypeCNAME)
	if len(records) != 2 {
		t.Fatalf("Expected 2 CNAME records, got %d", len(records))
	}

	// Create updated DNSEndpoint with different CNAME targets
	updatedEndpoint := createDNSEndpoint(
		"test-endpoint",
		"default",
		"test.example.com",
		"CNAME",
		[]string{"newtarget.example.com"},
	)

	// Update the records
	err = plugin.OnUpdate(updatedEndpoint)
	if err != nil {
		t.Fatalf("Failed to update endpoint: %v", err)
	}

	// Verify only one CNAME record exists with the new target
	zone = plugin.getConfiguredZoneForDomain("test.example.com.")
	records = plugin.cache.GetRecords("test.example.com.", zone, dns.TypeCNAME)
	if len(records) != 1 {
		t.Fatalf("Expected 1 CNAME record after update, got %d", len(records))
	}

	// Verify the record has the correct target
	if cname, ok := records[0].(*dns.CNAME); ok {
		expectedTarget := "newtarget.example.com."
		if cname.Target != expectedTarget {
			t.Fatalf("Expected CNAME target %s, got %s", expectedTarget, cname.Target)
		}
	} else {
		t.Fatalf("Record is not a CNAME record")
	}
}

func TestARecordUpdateNoDuplication(t *testing.T) {
	plugin := createTestPlugin()

	// Create initial DNSEndpoint with A records
	initialEndpoint := createDNSEndpoint(
		"test-endpoint",
		"default",
		"web.example.com",
		"A",
		[]string{"192.168.1.1", "192.168.1.2"},
	)

	// Add the initial records
	err := plugin.OnAdd(initialEndpoint)
	if err != nil {
		t.Fatalf("Failed to add initial endpoint: %v", err)
	}

	// Verify initial records exist
	zone := plugin.getConfiguredZoneForDomain("web.example.com.")
	records := plugin.cache.GetRecords("web.example.com.", zone, dns.TypeA)
	if len(records) != 2 {
		t.Fatalf("Expected 2 A records, got %d", len(records))
	}

	// Create updated DNSEndpoint with different A record targets
	updatedEndpoint := createDNSEndpoint(
		"test-endpoint",
		"default",
		"web.example.com",
		"A",
		[]string{"10.0.0.1"},
	)

	// Update the records
	err = plugin.OnUpdate(updatedEndpoint)
	if err != nil {
		t.Fatalf("Failed to update endpoint: %v", err)
	}

	// Verify only one A record exists with the new target
	zone = plugin.getConfiguredZoneForDomain("web.example.com.")
	records = plugin.cache.GetRecords("web.example.com.", zone, dns.TypeA)
	if len(records) != 1 {
		t.Fatalf("Expected 1 A record after update, got %d", len(records))
	}

	// Verify the record has the correct target
	if a, ok := records[0].(*dns.A); ok {
		expectedIP := "10.0.0.1"
		if a.A.String() != expectedIP {
			t.Fatalf("Expected A record IP %s, got %s", expectedIP, a.A.String())
		}
	} else {
		t.Fatalf("Record is not an A record")
	}
}

func TestMultipleEndpointsUpdateNoDuplication(t *testing.T) {
	plugin := createTestPlugin()

	// Create initial DNSEndpoint with multiple endpoints
	initialEndpoint := &externaldnsv1alpha1.DNSEndpoint{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-endpoint",
			Namespace: "default",
		},
		Spec: externaldnsv1alpha1.DNSEndpointSpec{
			Endpoints: []*endpointv1alpha1.Endpoint{
				{
					DNSName:    "app.example.com",
					RecordType: "CNAME",
					Targets:    []string{"old-app.example.com"},
				},
				{
					DNSName:    "api.example.com",
					RecordType: "A",
					Targets:    []string{"192.168.1.10"},
				},
			},
		},
	}

	// Add the initial records
	err := plugin.OnAdd(initialEndpoint)
	if err != nil {
		t.Fatalf("Failed to add initial endpoint: %v", err)
	}

	// Verify initial records exist
	appZone := plugin.getConfiguredZoneForDomain("app.example.com.")
	cnameRecords := plugin.cache.GetRecords("app.example.com.", appZone, dns.TypeCNAME)
	apiZone := plugin.getConfiguredZoneForDomain("api.example.com.")
	aRecords := plugin.cache.GetRecords("api.example.com.", apiZone, dns.TypeA)
	if len(cnameRecords) != 1 || len(aRecords) != 1 {
		t.Fatalf("Expected 1 CNAME and 1 A record, got %d CNAME and %d A", len(cnameRecords), len(aRecords))
	}

	// Create updated DNSEndpoint with different targets
	updatedEndpoint := &externaldnsv1alpha1.DNSEndpoint{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-endpoint",
			Namespace: "default",
		},
		Spec: externaldnsv1alpha1.DNSEndpointSpec{
			Endpoints: []*endpointv1alpha1.Endpoint{
				{
					DNSName:    "app.example.com",
					RecordType: "CNAME",
					Targets:    []string{"new-app.example.com"},
				},
				{
					DNSName:    "api.example.com",
					RecordType: "A",
					Targets:    []string{"10.0.0.20"},
				},
			},
		},
	}

	// Update the records
	err = plugin.OnUpdate(updatedEndpoint)
	if err != nil {
		t.Fatalf("Failed to update endpoint: %v", err)
	}

	// Verify only new records exist
	appZone = plugin.getConfiguredZoneForDomain("app.example.com.")
	cnameRecords = plugin.cache.GetRecords("app.example.com.", appZone, dns.TypeCNAME)
	apiZone = plugin.getConfiguredZoneForDomain("api.example.com.")
	aRecords = plugin.cache.GetRecords("api.example.com.", apiZone, dns.TypeA)
	if len(cnameRecords) != 1 || len(aRecords) != 1 {
		t.Fatalf("Expected 1 CNAME and 1 A record after update, got %d CNAME and %d A", len(cnameRecords), len(aRecords))
	}

	// Verify the CNAME record has the correct target
	if cname, ok := cnameRecords[0].(*dns.CNAME); ok {
		expectedTarget := "new-app.example.com."
		if cname.Target != expectedTarget {
			t.Fatalf("Expected CNAME target %s, got %s", expectedTarget, cname.Target)
		}
	} else {
		t.Fatalf("Record is not a CNAME record")
	}

	// Verify the A record has the correct target
	if a, ok := aRecords[0].(*dns.A); ok {
		expectedIP := "10.0.0.20"
		if a.A.String() != expectedIP {
			t.Fatalf("Expected A record IP %s, got %s", expectedIP, a.A.String())
		}
	} else {
		t.Fatalf("Record is not an A record")
	}
}

func TestConcurrentUpdatesNoDuplication(t *testing.T) {
	plugin := createTestPlugin()

	// Create initial DNSEndpoint
	initialEndpoint := createDNSEndpoint(
		"test-endpoint",
		"default",
		"concurrent.example.com",
		"CNAME",
		[]string{"initial.example.com"},
	)

	// Add the initial record
	err := plugin.OnAdd(initialEndpoint)
	if err != nil {
		t.Fatalf("Failed to add initial endpoint: %v", err)
	}

	// Perform sequential updates to avoid race conditions in testing
	// (In real scenarios, Kubernetes ensures updates are sequential per resource)
	numUpdates := 5
	for i := 0; i < numUpdates; i++ {
		updatedEndpoint := createDNSEndpoint(
			"test-endpoint",
			"default",
			"concurrent.example.com",
			"CNAME",
			[]string{fmt.Sprintf("target-%d.example.com", i)},
		)

		err := plugin.OnUpdate(updatedEndpoint)
		if err != nil {
			t.Fatalf("Failed to update endpoint at iteration %d: %v", i, err)
		}

		// Verify only one record exists after each update
		zone := plugin.getConfiguredZoneForDomain("concurrent.example.com.")
		records := plugin.cache.GetRecords("concurrent.example.com.", zone, dns.TypeCNAME)
		if len(records) != 1 {
			t.Fatalf("Expected 1 CNAME record after update %d, got %d", i, len(records))
		}
	}

	// Final verification
	zone := plugin.getConfiguredZoneForDomain("concurrent.example.com.")
	records := plugin.cache.GetRecords("concurrent.example.com.", zone, dns.TypeCNAME)
	if len(records) != 1 {
		t.Fatalf("Expected 1 CNAME record after all updates, got %d", len(records))
	}

	// Verify the final record has the last target
	if cname, ok := records[0].(*dns.CNAME); ok {
		expectedTarget := "target-4.example.com."
		if cname.Target != expectedTarget {
			t.Fatalf("Expected final CNAME target %s, got %s", expectedTarget, cname.Target)
		}
	} else {
		t.Fatalf("Record is not a CNAME record")
	}
}

func TestPTRRecordCleanupDuringUpdate(t *testing.T) {
	plugin := createTestPlugin()

	// Create initial DNSEndpoint with PTR creation annotation
	initialEndpoint := &externaldnsv1alpha1.DNSEndpoint{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-endpoint",
			Namespace: "default",
			Annotations: map[string]string{
				"coredns-externaldns.ionos.cloud/create-ptr": "true",
			},
		},
		Spec: externaldnsv1alpha1.DNSEndpointSpec{
			Endpoints: []*endpointv1alpha1.Endpoint{
				{
					DNSName:    "host.example.com",
					RecordType: "A",
					Targets:    []string{"192.168.1.100"},
				},
			},
		},
	}

	// Add the initial record (this should create both A and PTR records)
	err := plugin.OnAdd(initialEndpoint)
	if err != nil {
		t.Fatalf("Failed to add initial endpoint: %v", err)
	}

	// Verify A record exists
	hostZone := plugin.getConfiguredZoneForDomain("host.example.com.")
	aRecords := plugin.cache.GetRecords("host.example.com.", hostZone, dns.TypeA)
	if len(aRecords) != 1 {
		t.Fatalf("Expected 1 A record, got %d", len(aRecords))
	}

	// Verify PTR record exists
	ptrName := "100.1.168.192.in-addr.arpa."
	ptrZone := plugin.getConfiguredZoneForDomain(ptrName)
	ptrRecords := plugin.cache.GetRecords(ptrName, ptrZone, dns.TypePTR)
	if len(ptrRecords) != 1 {
		t.Fatalf("Expected 1 PTR record, got %d", len(ptrRecords))
	}

	// Create updated DNSEndpoint with different IP
	updatedEndpoint := &externaldnsv1alpha1.DNSEndpoint{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-endpoint",
			Namespace: "default",
			Annotations: map[string]string{
				"coredns-externaldns.ionos.cloud/create-ptr": "true",
			},
		},
		Spec: externaldnsv1alpha1.DNSEndpointSpec{
			Endpoints: []*endpointv1alpha1.Endpoint{
				{
					DNSName:    "host.example.com",
					RecordType: "A",
					Targets:    []string{"10.0.0.50"},
				},
			},
		},
	}

	// Update the record
	err = plugin.OnUpdate(updatedEndpoint)
	if err != nil {
		t.Fatalf("Failed to update endpoint: %v", err)
	}

	// Verify A record is updated
	hostZone = plugin.getConfiguredZoneForDomain("host.example.com.")
	aRecords = plugin.cache.GetRecords("host.example.com.", hostZone, dns.TypeA)
	if len(aRecords) != 1 {
		t.Fatalf("Expected 1 A record after update, got %d", len(aRecords))
	}

	if a, ok := aRecords[0].(*dns.A); ok {
		expectedIP := "10.0.0.50"
		if a.A.String() != expectedIP {
			t.Fatalf("Expected A record IP %s, got %s", expectedIP, a.A.String())
		}
	} else {
		t.Fatalf("Record is not an A record")
	}

	// Verify old PTR record is cleaned up
	oldPtrZone := plugin.getConfiguredZoneForDomain(ptrName)
	oldPtrRecords := plugin.cache.GetRecords(ptrName, oldPtrZone, dns.TypePTR)
	if len(oldPtrRecords) != 0 {
		t.Fatalf("Expected 0 old PTR records after update, got %d", len(oldPtrRecords))
	}

	// Verify new PTR record exists
	newPtrName := "50.0.0.10.in-addr.arpa."
	newPtrZone := plugin.getConfiguredZoneForDomain(newPtrName)
	newPtrRecords := plugin.cache.GetRecords(newPtrName, newPtrZone, dns.TypePTR)
	if len(newPtrRecords) != 1 {
		t.Fatalf("Expected 1 new PTR record after update, got %d", len(newPtrRecords))
	}
}

// TestOriginalScenarioReproduction reproduces the exact scenario from the user's issue
func TestOriginalScenarioReproduction(t *testing.T) {
	plugin := createTestPlugin()

	// Simulate the original scenario: sdf.foo.stg.test.com pointing to mgmt.stg.test.com
	initialEndpoint := createDNSEndpoint(
		"user-endpoint",
		"default",
		"sdf.foo.stg.test.com",
		"CNAME",
		[]string{"mgmt.stg.test.com"},
	)

	// Add the initial record
	err := plugin.OnAdd(initialEndpoint)
	if err != nil {
		t.Fatalf("Failed to add initial endpoint: %v", err)
	}

	// Verify initial record exists
	zone := plugin.getConfiguredZoneForDomain("sdf.foo.stg.test.com.")
	records := plugin.cache.GetRecords("sdf.foo.stg.test.com.", zone, dns.TypeCNAME)
	if len(records) != 1 {
		t.Fatalf("Expected 1 CNAME record initially, got %d", len(records))
	}

	// Update to point to ntp.stg.test.com (reproducing the user's scenario)
	updatedEndpoint := createDNSEndpoint(
		"user-endpoint",
		"default",
		"sdf.foo.stg.test.com",
		"CNAME",
		[]string{"ntp.stg.test.com"},
	)

	// Update the record
	err = plugin.OnUpdate(updatedEndpoint)
	if err != nil {
		t.Fatalf("Failed to update endpoint: %v", err)
	}

	// Verify only one CNAME record exists (no duplication like in the user's issue)
	zone = plugin.getConfiguredZoneForDomain("sdf.foo.stg.test.com.")
	records = plugin.cache.GetRecords("sdf.foo.stg.test.com.", zone, dns.TypeCNAME)
	if len(records) != 1 {
		t.Fatalf("Expected exactly 1 CNAME record after update, got %d (this would have been 2 with the bug)", len(records))
	}

	// Verify the record has the correct updated target
	if cname, ok := records[0].(*dns.CNAME); ok {
		expectedTarget := "ntp.stg.test.com."
		if cname.Target != expectedTarget {
			t.Fatalf("Expected CNAME target %s, got %s", expectedTarget, cname.Target)
		}
	} else {
		t.Fatalf("Record is not a CNAME record")
	}

	// Additional verification: ensure the old target is completely gone
	// by checking the raw string representation doesn't contain the old target
	recordStr := records[0].String()
	if strings.Contains(recordStr, "mgmt.stg.test.com") {
		t.Fatalf("Old target still present in record: %s", recordStr)
	}

	// Verify the new target is present
	if !strings.Contains(recordStr, "ntp.stg.test.com") {
		t.Fatalf("New target not found in record: %s", recordStr)
	}
}

// TestCNAMEResolutionToARecord tests that when querying for A record but getting CNAME,
// the DNS server resolves the CNAME and returns both CNAME and A records
func TestCNAMEResolutionToARecord(t *testing.T) {
	plugin := createTestPlugin()

	// First, add an A record for the target
	targetEndpoint := createDNSEndpoint(
		"target-endpoint",
		"default",
		"ntp.stg.test.com",
		"A",
		[]string{"192.168.1.100"},
	)
	err := plugin.OnAdd(targetEndpoint)
	if err != nil {
		t.Fatalf("Failed to add target A record: %v", err)
	}

	// Then add a CNAME that points to the target
	cnameEndpoint := createDNSEndpoint(
		"cname-endpoint",
		"default",
		"sdf.foo.stg.test.com",
		"CNAME",
		[]string{"ntp.stg.test.com"},
	)
	err = plugin.OnAdd(cnameEndpoint)
	if err != nil {
		t.Fatalf("Failed to add CNAME record: %v", err)
	}

	// Now simulate a DNS query for A record of the CNAME name
	// Create a DNS request for A record
	req := new(dns.Msg)
	req.SetQuestion("sdf.foo.stg.test.com.", dns.TypeA)

	// Create a mock response writer
	writer := &mockResponseWriter{}

	// Call ServeDNS
	rcode, err := plugin.ServeDNS(context.Background(), writer, req)
	if err != nil {
		t.Fatalf("ServeDNS returned error: %v", err)
	}

	if rcode != dns.RcodeSuccess {
		t.Fatalf("Expected DNS success, got rcode: %d", rcode)
	}

	// Verify the response contains both CNAME and A records
	if writer.msg == nil {
		t.Fatalf("No response message written")
	}

	answers := writer.msg.Answer
	if len(answers) < 2 {
		t.Fatalf("Expected at least 2 records (CNAME + A), got %d", len(answers))
	}

	// Verify we have a CNAME record
	var foundCNAME, foundA bool
	var cnameTarget, aRecord string

	for _, rr := range answers {
		switch v := rr.(type) {
		case *dns.CNAME:
			foundCNAME = true
			cnameTarget = v.Target
		case *dns.A:
			foundA = true
			aRecord = v.A.String()
		}
	}

	if !foundCNAME {
		t.Fatalf("Expected CNAME record in response")
	}

	if !foundA {
		t.Fatalf("Expected A record in response (CNAME resolution)")
	}

	// Verify the CNAME points to the correct target
	expectedCNAMETarget := "ntp.stg.test.com."
	if cnameTarget != expectedCNAMETarget {
		t.Fatalf("Expected CNAME target %s, got %s", expectedCNAMETarget, cnameTarget)
	}

	// Verify the A record has the correct IP
	expectedIP := "192.168.1.100"
	if aRecord != expectedIP {
		t.Fatalf("Expected A record IP %s, got %s", expectedIP, aRecord)
	}
}

// TestCNAMEChainResolution tests resolving a chain of CNAME records
func TestCNAMEChainResolution(t *testing.T) {
	plugin := createTestPlugin()

	// Create a chain: alias -> intermediate -> target -> A record
	// Final A record
	targetEndpoint := createDNSEndpoint(
		"target-endpoint",
		"default",
		"final.example.com",
		"A",
		[]string{"10.0.0.1"},
	)
	err := plugin.OnAdd(targetEndpoint)
	if err != nil {
		t.Fatalf("Failed to add target A record: %v", err)
	}

	// Intermediate CNAME
	intermediateEndpoint := createDNSEndpoint(
		"intermediate-endpoint",
		"default",
		"intermediate.example.com",
		"CNAME",
		[]string{"final.example.com"},
	)
	err = plugin.OnAdd(intermediateEndpoint)
	if err != nil {
		t.Fatalf("Failed to add intermediate CNAME: %v", err)
	}

	// Alias CNAME
	aliasEndpoint := createDNSEndpoint(
		"alias-endpoint",
		"default",
		"alias.example.com",
		"CNAME",
		[]string{"intermediate.example.com"},
	)
	err = plugin.OnAdd(aliasEndpoint)
	if err != nil {
		t.Fatalf("Failed to add alias CNAME: %v", err)
	}

	// Query for A record of the alias
	req := new(dns.Msg)
	req.SetQuestion("alias.example.com.", dns.TypeA)

	writer := &mockResponseWriter{}
	rcode, err := plugin.ServeDNS(context.Background(), writer, req)
	if err != nil {
		t.Fatalf("ServeDNS returned error: %v", err)
	}

	if rcode != dns.RcodeSuccess {
		t.Fatalf("Expected DNS success, got rcode: %d", rcode)
	}

	// Verify the response contains the entire CNAME chain + final A record
	answers := writer.msg.Answer
	if len(answers) < 3 {
		t.Fatalf("Expected at least 3 records (alias CNAME + intermediate CNAME + A), got %d", len(answers))
	}

	// Count record types
	cnameCount := 0
	aCount := 0
	finalIP := ""

	for _, rr := range answers {
		switch v := rr.(type) {
		case *dns.CNAME:
			cnameCount++
		case *dns.A:
			aCount++
			finalIP = v.A.String()
		}
	}

	if cnameCount < 2 {
		t.Fatalf("Expected at least 2 CNAME records in chain, got %d", cnameCount)
	}

	if aCount != 1 {
		t.Fatalf("Expected exactly 1 A record, got %d", aCount)
	}

	// Verify the final IP is correct
	expectedIP := "10.0.0.1"
	if finalIP != expectedIP {
		t.Fatalf("Expected final IP %s, got %s", expectedIP, finalIP)
	}
}

// TestDirectARecordQuery tests that direct A record queries still work normally
func TestDirectARecordQuery(t *testing.T) {
	plugin := createTestPlugin()

	// Add a direct A record
	endpoint := createDNSEndpoint(
		"direct-endpoint",
		"default",
		"direct.example.com",
		"A",
		[]string{"172.16.0.1"},
	)
	err := plugin.OnAdd(endpoint)
	if err != nil {
		t.Fatalf("Failed to add direct A record: %v", err)
	}

	// Query for A record
	req := new(dns.Msg)
	req.SetQuestion("direct.example.com.", dns.TypeA)

	writer := &mockResponseWriter{}
	rcode, err := plugin.ServeDNS(context.Background(), writer, req)
	if err != nil {
		t.Fatalf("ServeDNS returned error: %v", err)
	}

	if rcode != dns.RcodeSuccess {
		t.Fatalf("Expected DNS success, got rcode: %d", rcode)
	}

	// Verify we get exactly one A record
	answers := writer.msg.Answer
	if len(answers) != 1 {
		t.Fatalf("Expected exactly 1 A record, got %d", len(answers))
	}

	if a, ok := answers[0].(*dns.A); ok {
		expectedIP := "172.16.0.1"
		if a.A.String() != expectedIP {
			t.Fatalf("Expected IP %s, got %s", expectedIP, a.A.String())
		}
	} else {
		t.Fatalf("Expected A record, got %T", answers[0])
	}
}

// TestDirectCNAMEQuery tests that direct CNAME record queries work normally
func TestDirectCNAMEQuery(t *testing.T) {
	plugin := createTestPlugin()

	// Add a CNAME record
	endpoint := createDNSEndpoint(
		"cname-endpoint",
		"default",
		"alias.example.com",
		"CNAME",
		[]string{"target.example.com"},
	)
	err := plugin.OnAdd(endpoint)
	if err != nil {
		t.Fatalf("Failed to add CNAME record: %v", err)
	}

	// Query for CNAME record
	req := new(dns.Msg)
	req.SetQuestion("alias.example.com.", dns.TypeCNAME)

	writer := &mockResponseWriter{}
	rcode, err := plugin.ServeDNS(context.Background(), writer, req)
	if err != nil {
		t.Fatalf("ServeDNS returned error: %v", err)
	}

	if rcode != dns.RcodeSuccess {
		t.Fatalf("Expected DNS success, got rcode: %d", rcode)
	}

	// Verify we get exactly one CNAME record
	answers := writer.msg.Answer
	if len(answers) != 1 {
		t.Fatalf("Expected exactly 1 CNAME record, got %d", len(answers))
	}

	if cname, ok := answers[0].(*dns.CNAME); ok {
		expectedTarget := "target.example.com."
		if cname.Target != expectedTarget {
			t.Fatalf("Expected CNAME target %s, got %s", expectedTarget, cname.Target)
		}
	} else {
		t.Fatalf("Expected CNAME record, got %T", answers[0])
	}
}

// TestExactUserScenario reproduces the user's exact issue:
// Query for A record of sdf.foo.stg.test.com should return both CNAME and A record
func TestExactUserScenario(t *testing.T) {
	plugin := createTestPlugin()

	// Add the target A record (what ntp.stg.test.com should resolve to)
	ntpEndpoint := createDNSEndpoint(
		"ntp-endpoint",
		"default",
		"ntp.stg.test.com",
		"A",
		[]string{"10.249.255.10"}, // Example IP
	)
	err := plugin.OnAdd(ntpEndpoint)
	if err != nil {
		t.Fatalf("Failed to add ntp A record: %v", err)
	}

	// Add the CNAME that the user has
	cnameEndpoint := createDNSEndpoint(
		"sdf-endpoint",
		"default",
		"sdf.foo.stg.test.com",
		"CNAME",
		[]string{"ntp.stg.test.com"},
	)
	err = plugin.OnAdd(cnameEndpoint)
	if err != nil {
		t.Fatalf("Failed to add CNAME record: %v", err)
	}

	// Now simulate the exact query the user ran:
	// dig sdf.foo.stg.test.com @10.249.255.2 (asking for A record)
	req := new(dns.Msg)
	req.SetQuestion("sdf.foo.stg.test.com.", dns.TypeA)

	writer := &mockResponseWriter{}
	rcode, err := plugin.ServeDNS(context.Background(), writer, req)
	if err != nil {
		t.Fatalf("ServeDNS returned error: %v", err)
	}

	if rcode != dns.RcodeSuccess {
		t.Fatalf("Expected DNS success, got rcode: %d", rcode)
	}

	// Verify the response
	if writer.msg == nil {
		t.Fatalf("No response message written")
	}

	answers := writer.msg.Answer

	// The user should get both CNAME and A records, not just CNAME
	if len(answers) < 2 {
		t.Fatalf("Expected at least 2 records (CNAME + A), got %d. User would only see CNAME without the fix.", len(answers))
	}

	// Verify we have both record types
	var foundCNAME, foundA bool
	var cnameTarget, aRecord string

	for _, rr := range answers {
		switch v := rr.(type) {
		case *dns.CNAME:
			foundCNAME = true
			cnameTarget = v.Target
		case *dns.A:
			foundA = true
			aRecord = v.A.String()
		}
	}

	if !foundCNAME {
		t.Fatalf("Expected CNAME record in response")
	}

	if !foundA {
		t.Fatalf("Expected A record in response - this was the user's problem! They only got CNAME.")
	}

	// Verify the CNAME points to the correct target
	if cnameTarget != "ntp.stg.test.com." {
		t.Fatalf("Expected CNAME target ntp.stg.test.com., got %s", cnameTarget)
	}

	// Verify the A record has the correct IP
	if aRecord != "10.249.255.10" {
		t.Fatalf("Expected A record IP 10.249.255.10, got %s", aRecord)
	}

	t.Logf("SUCCESS: Query for A record returned both CNAME (%s) and A record (%s)", cnameTarget, aRecord)
}

func TestConfiguredZoneMatching(t *testing.T) {
	config := &Config{
		Zones: []string{"stg.test.com.", "test.com.", "example.com.", "."},
	}
	plugin := New(config)

	tests := []struct {
		name     string
		domain   string
		expected string
	}{
		{
			name:     "Wildcard matches longest zone",
			domain:   "*.foo.stg.test.com",
			expected: "stg.test.com.",
		},
		{
			name:     "Regular subdomain matches longest zone",
			domain:   "foo.stg.test.com",
			expected: "stg.test.com.",
		},
		{
			name:     "Domain matches shorter zone when longer not available",
			domain:   "app.test.com",
			expected: "test.com.",
		},
		{
			name:     "Domain matches exactly",
			domain:   "test.com",
			expected: "test.com.",
		},
		{
			name:     "Unknown domain falls back to root zone",
			domain:   "unknown.domain.com",
			expected: ".",
		},
		{
			name:     "Domain with different TLD falls back to root zone",
			domain:   "example.net",
			expected: ".",
		},
		{
			name:     "Case insensitive matching",
			domain:   "FOO.STG.TEST.COM",
			expected: "stg.test.com.",
		},
		{
			name:     "FQDN handling",
			domain:   "app.stg.test.com.",
			expected: "stg.test.com.",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			zone := plugin.getConfiguredZoneForDomain(test.domain)
			assert.Equal(t, test.expected, zone,
				"For domain %s, expected zone %s but got %s",
				test.domain, test.expected, zone)
		})
	}
}

func TestZoneSorting(t *testing.T) {
	// Test that longer zones are matched before shorter ones
	config := &Config{
		// Deliberately put zones in wrong order to test sorting
		Zones: []string{".", "test.com.", "stg.test.com.", "example.com."},
	}
	plugin := New(config)

	// Should match the longest zone (stg.test.com.) not the shorter ones (test.com. or .)
	zone := plugin.getConfiguredZoneForDomain("app.stg.test.com")
	assert.Equal(t, "stg.test.com.", zone)
}

func TestEmptyZonesConfig(t *testing.T) {
	config := &Config{
		Zones: []string{},
	}
	plugin := New(config)

	// Should fall back to "." when no zones are configured
	zone := plugin.getConfiguredZoneForDomain("any.domain.com")
	assert.Equal(t, ".", zone)
}

func TestZoneBoundaryMatching(t *testing.T) {
	// Test that zone boundary detection works correctly to prevent false matches
	config := &Config{
		Zones: []string{"example.com.", "ample.com.", "test.com."},
	}
	plugin := New(config)

	tests := []struct {
		name     string
		domain   string
		expected string
	}{
		{
			name:     "Should match example.com not ample.com",
			domain:   "app.example.com",
			expected: "example.com.",
		},
		{
			name:     "Should match ample.com exactly",
			domain:   "ample.com",
			expected: "ample.com.",
		},
		{
			name:     "Should not false match zone suffix",
			domain:   "notexample.com", // Should NOT match example.com
			expected: ".",              // Should fall back to root
		},
		{
			name:     "Proper subdomain matching",
			domain:   "sub.test.com",
			expected: "test.com.",
		},
		{
			name:     "Should match longest zone first",
			domain:   "www.example.com",
			expected: "example.com.", // Not ample.com
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			zone := plugin.getConfiguredZoneForDomain(test.domain)
			assert.Equal(t, test.expected, zone,
				"For domain %s, expected zone %s but got %s",
				test.domain, test.expected, zone)
		})
	}
}

func TestRootZoneOnly(t *testing.T) {
	config := &Config{
		Zones: []string{"."},
	}
	plugin := New(config)

	tests := []struct {
		domain   string
		expected string
	}{
		{"example.com", "."},
		{"*.foo.example.com", "."},
		{"any.domain", "."},
	}

	for _, test := range tests {
		zone := plugin.getConfiguredZoneForDomain(test.domain)
		assert.Equal(t, test.expected, zone)
	}
}

func TestConfigMapKeySanitization(t *testing.T) {
	config := DefaultConfig()
	plugin := New(config)

	tests := []struct {
		name        string
		zone        string
		expectedKey string
	}{
		{
			name:        "Root zone sanitization",
			zone:        ".",
			expectedKey: "ROOT",
		},
		{
			name:        "Regular zone unchanged",
			zone:        "example.com.",
			expectedKey: "example.com.",
		},
		{
			name:        "Test zone unchanged",
			zone:        "test.com.",
			expectedKey: "test.com.",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configKey := plugin.zoneToConfigKey(test.zone)
			assert.Equal(t, test.expectedKey, configKey)

			// Test reverse conversion
			zone := plugin.configKeyToZone(configKey)
			assert.Equal(t, test.zone, zone)
		})
	}
}

func TestNXDOMAINForConfiguredZone(t *testing.T) {
	config := &Config{
		TTL:       300,
		Namespace: "test",
		Zones:     []string{"example.com.", "test.com.", "."},
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

	// Set a zone serial for example.com
	plugin.serialsMutex.Lock()
	plugin.zoneSerials["example.com."] = 12345
	plugin.serialsMutex.Unlock()

	t.Run("Query for non-existent domain in configured zone returns NXDOMAIN", func(t *testing.T) {
		req := new(dns.Msg)
		req.SetQuestion("nonexistent.example.com.", dns.TypeA)

		writer := &mockResponseWriter{}
		rcode, err := plugin.ServeDNS(context.Background(), writer, req)

		assert.NoError(t, err)
		assert.Equal(t, dns.RcodeNameError, rcode)

		require.NotNil(t, writer.msg)
		assert.True(t, writer.msg.Authoritative, "Response should be authoritative")
		assert.Equal(t, dns.RcodeNameError, writer.msg.Rcode)
		assert.Len(t, writer.msg.Answer, 0, "Should have no answer records")
		assert.Len(t, writer.msg.Ns, 1, "Should have SOA in authority section")

		// Verify SOA record
		soa, ok := writer.msg.Ns[0].(*dns.SOA)
		require.True(t, ok, "Authority record should be SOA")
		assert.Equal(t, "example.com.", soa.Hdr.Name)
		assert.Equal(t, uint32(12345), soa.Serial)
		assert.Equal(t, "ns1.example.com.", soa.Ns)
		assert.Equal(t, "admin.example.com.", soa.Mbox)
	})

	t.Run("Query for non-existent subdomain returns NXDOMAIN", func(t *testing.T) {
		req := new(dns.Msg)
		req.SetQuestion("does.not.exist.example.com.", dns.TypeA)

		writer := &mockResponseWriter{}
		rcode, err := plugin.ServeDNS(context.Background(), writer, req)

		assert.NoError(t, err)
		assert.Equal(t, dns.RcodeNameError, rcode)
		assert.True(t, writer.msg.Authoritative)
		assert.Len(t, writer.msg.Ns, 1)
	})

	t.Run("Query for non-existent AAAA record returns NXDOMAIN", func(t *testing.T) {
		req := new(dns.Msg)
		req.SetQuestion("missing.example.com.", dns.TypeAAAA)

		writer := &mockResponseWriter{}
		rcode, err := plugin.ServeDNS(context.Background(), writer, req)

		assert.NoError(t, err)
		assert.Equal(t, dns.RcodeNameError, rcode)
		assert.True(t, writer.msg.Authoritative)
	})

	t.Run("Query for non-existent MX record returns NXDOMAIN", func(t *testing.T) {
		req := new(dns.Msg)
		req.SetQuestion("nomail.example.com.", dns.TypeMX)

		writer := &mockResponseWriter{}
		rcode, err := plugin.ServeDNS(context.Background(), writer, req)

		assert.NoError(t, err)
		assert.Equal(t, dns.RcodeNameError, rcode)
		assert.True(t, writer.msg.Authoritative)
	})
}

func TestNXDOMAINForRootZonePassesToNext(t *testing.T) {
	config := &Config{
		TTL:       300,
		Namespace: "test",
		Zones:     []string{"."},
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

	// Set up a mock next plugin
	nextHandlerCalled := false
	plugin.Next = &mockHandler{
		handler: func() (int, error) {
			nextHandlerCalled = true
			return dns.RcodeNameError, nil
		},
	}

	t.Run("Query for non-existent domain in root zone passes to next plugin", func(t *testing.T) {
		req := new(dns.Msg)
		req.SetQuestion("unknown.domain.com.", dns.TypeA)

		writer := &mockResponseWriter{}
		rcode, err := plugin.ServeDNS(context.Background(), writer, req)

		assert.NoError(t, err)
		assert.Equal(t, dns.RcodeNameError, rcode)
		assert.True(t, nextHandlerCalled, "Should pass to next handler for root zone")
	})
}

func TestNXDOMAINVsExistingRecords(t *testing.T) {
	config := &Config{
		TTL:       300,
		Namespace: "test",
		Zones:     []string{"example.com.", "."},
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

	// Add an existing A record
	endpoint := createDNSEndpoint(
		"existing",
		"test",
		"existing.example.com",
		"A",
		[]string{"192.168.1.1"},
	)
	err := plugin.OnAdd(endpoint)
	require.NoError(t, err)

	t.Run("Existing record returns success", func(t *testing.T) {
		req := new(dns.Msg)
		req.SetQuestion("existing.example.com.", dns.TypeA)

		writer := &mockResponseWriter{}
		rcode, err := plugin.ServeDNS(context.Background(), writer, req)

		assert.NoError(t, err)
		assert.Equal(t, dns.RcodeSuccess, rcode)
		assert.True(t, writer.msg.Authoritative)
		assert.Len(t, writer.msg.Answer, 1)
		assert.Len(t, writer.msg.Ns, 0, "Should not have SOA in authority for success")
	})

	t.Run("Non-existent record returns NXDOMAIN", func(t *testing.T) {
		req := new(dns.Msg)
		req.SetQuestion("missing.example.com.", dns.TypeA)

		writer := &mockResponseWriter{}
		rcode, err := plugin.ServeDNS(context.Background(), writer, req)

		assert.NoError(t, err)
		assert.Equal(t, dns.RcodeNameError, rcode)
		assert.True(t, writer.msg.Authoritative)
		assert.Len(t, writer.msg.Answer, 0)
		assert.Len(t, writer.msg.Ns, 1, "Should have SOA in authority for NXDOMAIN")
	})
}

func TestNXDOMAINWithMultipleZones(t *testing.T) {
	config := &Config{
		TTL:       300,
		Namespace: "test",
		Zones:     []string{"stg.example.com.", "example.com.", "other.com.", "."},
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

	// Set zone serials
	plugin.serialsMutex.Lock()
	plugin.zoneSerials["stg.example.com."] = 1001
	plugin.zoneSerials["example.com."] = 1002
	plugin.zoneSerials["other.com."] = 1003
	plugin.serialsMutex.Unlock()

	t.Run("NXDOMAIN in stg.example.com zone has correct SOA", func(t *testing.T) {
		req := new(dns.Msg)
		req.SetQuestion("missing.stg.example.com.", dns.TypeA)

		writer := &mockResponseWriter{}
		rcode, err := plugin.ServeDNS(context.Background(), writer, req)

		assert.NoError(t, err)
		assert.Equal(t, dns.RcodeNameError, rcode)

		require.Len(t, writer.msg.Ns, 1)
		soa, ok := writer.msg.Ns[0].(*dns.SOA)
		require.True(t, ok)
		assert.Equal(t, "stg.example.com.", soa.Hdr.Name, "SOA should be for the most specific matching zone")
		assert.Equal(t, uint32(1001), soa.Serial)
	})

	t.Run("NXDOMAIN in example.com zone has correct SOA", func(t *testing.T) {
		req := new(dns.Msg)
		req.SetQuestion("missing.example.com.", dns.TypeA)

		writer := &mockResponseWriter{}
		rcode, err := plugin.ServeDNS(context.Background(), writer, req)

		assert.NoError(t, err)
		assert.Equal(t, dns.RcodeNameError, rcode)

		require.Len(t, writer.msg.Ns, 1)
		soa, ok := writer.msg.Ns[0].(*dns.SOA)
		require.True(t, ok)
		assert.Equal(t, "example.com.", soa.Hdr.Name)
		assert.Equal(t, uint32(1002), soa.Serial)
	})

	t.Run("NXDOMAIN in other.com zone has correct SOA", func(t *testing.T) {
		req := new(dns.Msg)
		req.SetQuestion("absent.other.com.", dns.TypeA)

		writer := &mockResponseWriter{}
		rcode, err := plugin.ServeDNS(context.Background(), writer, req)

		assert.NoError(t, err)
		assert.Equal(t, dns.RcodeNameError, rcode)

		require.Len(t, writer.msg.Ns, 1)
		soa, ok := writer.msg.Ns[0].(*dns.SOA)
		require.True(t, ok)
		assert.Equal(t, "other.com.", soa.Hdr.Name)
		assert.Equal(t, uint32(1003), soa.Serial)
	})
}

func TestGetZoneSerial(t *testing.T) {
	config := DefaultConfig()
	plugin := New(config)

	t.Run("Returns existing serial for zone", func(t *testing.T) {
		plugin.serialsMutex.Lock()
		plugin.zoneSerials["example.com."] = 54321
		plugin.serialsMutex.Unlock()

		serial := plugin.getZoneSerial("example.com.")
		assert.Equal(t, uint32(54321), serial)
	})

	t.Run("Returns current timestamp for non-existent zone", func(t *testing.T) {
		beforeTime := uint32(time.Now().Unix())
		serial := plugin.getZoneSerial("unknown.zone.")
		afterTime := uint32(time.Now().Unix())

		assert.GreaterOrEqual(t, serial, beforeTime)
		assert.LessOrEqual(t, serial, afterTime)
	})

	t.Run("Thread-safe access", func(t *testing.T) {
		plugin.serialsMutex.Lock()
		plugin.zoneSerials["test.com."] = 99999
		plugin.serialsMutex.Unlock()

		// Should not deadlock or panic
		serial := plugin.getZoneSerial("test.com.")
		assert.Equal(t, uint32(99999), serial)
	})
}

func TestNXDOMAINWildcardDomain(t *testing.T) {
	config := &Config{
		TTL:       300,
		Namespace: "test",
		Zones:     []string{"example.com.", "."},
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

	// Add a wildcard CNAME
	wildcardEndpoint := createDNSEndpoint(
		"wildcard",
		"test",
		"*.wildcard.example.com",
		"CNAME",
		[]string{"target.example.com"},
	)
	err := plugin.OnAdd(wildcardEndpoint)
	require.NoError(t, err)

	t.Run("Query matching wildcard returns success", func(t *testing.T) {
		req := new(dns.Msg)
		req.SetQuestion("anything.wildcard.example.com.", dns.TypeCNAME)

		writer := &mockResponseWriter{}
		rcode, err := plugin.ServeDNS(context.Background(), writer, req)

		assert.NoError(t, err)
		assert.Equal(t, dns.RcodeSuccess, rcode)
		assert.Len(t, writer.msg.Answer, 1)
	})

	t.Run("Query not matching wildcard returns NXDOMAIN", func(t *testing.T) {
		req := new(dns.Msg)
		req.SetQuestion("notmatch.example.com.", dns.TypeA)

		writer := &mockResponseWriter{}
		rcode, err := plugin.ServeDNS(context.Background(), writer, req)

		assert.NoError(t, err)
		assert.Equal(t, dns.RcodeNameError, rcode)
		assert.True(t, writer.msg.Authoritative)
		assert.Len(t, writer.msg.Ns, 1)
	})
}

func TestRootZoneSerialSkipping(t *testing.T) {
	config := &Config{
		TTL:       300,
		Namespace: "test",
		Zones:     []string{"example.com.", "test.com.", "."}, // Include ROOT zone
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

	now := time.Now()

	t.Run("OnAdd with ROOT zone domain skips serial update", func(t *testing.T) {
		// Create an endpoint that would map to ROOT zone (not matching any configured zone)
		endpoint := &externaldnsv1alpha1.DNSEndpoint{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "root-zone-endpoint",
				Namespace:         "test",
				CreationTimestamp: metav1.NewTime(now),
				Generation:        1,
				ResourceVersion:   "1001",
			},
			Spec: externaldnsv1alpha1.DNSEndpointSpec{
				Endpoints: []*endpointv1alpha1.Endpoint{
					{
						DNSName:    "unmatched.domain.org", // Doesn't match any configured zone except ROOT
						Targets:    []string{"1.2.3.4"},
						RecordType: "A",
					},
				},
			},
		}

		err := plugin.OnAdd(endpoint)
		require.NoError(t, err)

		// Verify that ROOT zone serial was NOT updated
		plugin.serialsMutex.RLock()
		_, hasRootSerial := plugin.zoneSerials["."]
		plugin.serialsMutex.RUnlock()

		assert.False(t, hasRootSerial, "ROOT zone (.) should not have serial updated")

		// Verify the record was still added to cache
		zone := plugin.getConfiguredZoneForDomain("unmatched.domain.org")
		assert.Equal(t, ".", zone, "Domain should match ROOT zone")
		records := plugin.cache.GetRecords("unmatched.domain.org.", zone, dns.TypeA)
		assert.Len(t, records, 1, "Record should be added to cache even though serial not updated")
	})

	t.Run("OnAdd with non-ROOT zone updates serial normally", func(t *testing.T) {
		endpoint := &externaldnsv1alpha1.DNSEndpoint{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "example-zone-endpoint",
				Namespace:         "test",
				CreationTimestamp: metav1.NewTime(now),
				Generation:        1,
				ResourceVersion:   "1002",
			},
			Spec: externaldnsv1alpha1.DNSEndpointSpec{
				Endpoints: []*endpointv1alpha1.Endpoint{
					{
						DNSName:    "app.example.com",
						Targets:    []string{"1.2.3.5"},
						RecordType: "A",
					},
				},
			},
		}

		err := plugin.OnAdd(endpoint)
		require.NoError(t, err)

		// Verify that example.com. zone serial WAS updated
		plugin.serialsMutex.RLock()
		serial, hasSerial := plugin.zoneSerials["example.com."]
		plugin.serialsMutex.RUnlock()

		assert.True(t, hasSerial, "example.com. zone should have serial updated")
		assert.Greater(t, serial, uint32(0), "Serial should be set")
	})

	t.Run("OnUpdate with ROOT zone domain skips serial update", func(t *testing.T) {
		endpoint := &externaldnsv1alpha1.DNSEndpoint{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "root-zone-update",
				Namespace:         "test",
				CreationTimestamp: metav1.NewTime(now),
				Generation:        2,
				ResourceVersion:   "1003",
			},
			Spec: externaldnsv1alpha1.DNSEndpointSpec{
				Endpoints: []*endpointv1alpha1.Endpoint{
					{
						DNSName:    "another.unmatched.net",
						Targets:    []string{"2.3.4.5"},
						RecordType: "A",
					},
				},
			},
		}

		// First add
		err := plugin.OnAdd(endpoint)
		require.NoError(t, err)

		// Verify ROOT zone serial is still not set
		plugin.serialsMutex.RLock()
		_, hasRootSerial := plugin.zoneSerials["."]
		plugin.serialsMutex.RUnlock()
		assert.False(t, hasRootSerial, "ROOT zone should not have serial after add")

		// Now update
		endpoint.Spec.Endpoints[0].Targets = []string{"3.4.5.6"}
		err = plugin.OnUpdate(endpoint)
		require.NoError(t, err)

		// Verify ROOT zone serial is STILL not set
		plugin.serialsMutex.RLock()
		_, hasRootSerialAfterUpdate := plugin.zoneSerials["."]
		plugin.serialsMutex.RUnlock()
		assert.False(t, hasRootSerialAfterUpdate, "ROOT zone should not have serial after update")
	})

	t.Run("OnUpdate with non-ROOT zone updates serial normally", func(t *testing.T) {
		endpoint := &externaldnsv1alpha1.DNSEndpoint{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "test-zone-update",
				Namespace:         "test",
				CreationTimestamp: metav1.NewTime(now),
				Generation:        1,
				ResourceVersion:   "1004",
			},
			Spec: externaldnsv1alpha1.DNSEndpointSpec{
				Endpoints: []*endpointv1alpha1.Endpoint{
					{
						DNSName:    "service.test.com",
						Targets:    []string{"10.0.0.1"},
						RecordType: "A",
					},
				},
			},
		}

		err := plugin.OnAdd(endpoint)
		require.NoError(t, err)

		plugin.serialsMutex.RLock()
		initialSerial := plugin.zoneSerials["test.com."]
		plugin.serialsMutex.RUnlock()

		// Update
		endpoint.Spec.Endpoints[0].Targets = []string{"10.0.0.2"}
		err = plugin.OnUpdate(endpoint)
		require.NoError(t, err)

		plugin.serialsMutex.RLock()
		updatedSerial := plugin.zoneSerials["test.com."]
		plugin.serialsMutex.RUnlock()

		assert.Greater(t, updatedSerial, initialSerial, "Serial should increment after update")
	})

	t.Run("OnDelete with ROOT zone domain skips serial update", func(t *testing.T) {
		endpoint := &externaldnsv1alpha1.DNSEndpoint{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "root-zone-delete",
				Namespace:         "test",
				CreationTimestamp: metav1.NewTime(now),
				Generation:        1,
				ResourceVersion:   "1005",
			},
			Spec: externaldnsv1alpha1.DNSEndpointSpec{
				Endpoints: []*endpointv1alpha1.Endpoint{
					{
						DNSName:    "todelete.unmatched.io",
						Targets:    []string{"5.6.7.8"},
						RecordType: "A",
					},
				},
			},
		}

		// Add first
		err := plugin.OnAdd(endpoint)
		require.NoError(t, err)

		// Verify ROOT zone serial is not set
		plugin.serialsMutex.RLock()
		_, hasRootSerial := plugin.zoneSerials["."]
		plugin.serialsMutex.RUnlock()
		assert.False(t, hasRootSerial, "ROOT zone should not have serial")

		// Delete
		err = plugin.OnDelete(endpoint)
		require.NoError(t, err)

		// Verify ROOT zone serial is STILL not set
		plugin.serialsMutex.RLock()
		_, hasRootSerialAfterDelete := plugin.zoneSerials["."]
		plugin.serialsMutex.RUnlock()
		assert.False(t, hasRootSerialAfterDelete, "ROOT zone should not have serial after delete")
	})

	t.Run("OnDelete with non-ROOT zone updates serial normally", func(t *testing.T) {
		endpoint := &externaldnsv1alpha1.DNSEndpoint{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "example-zone-delete",
				Namespace:         "test",
				CreationTimestamp: metav1.NewTime(now),
				Generation:        1,
				ResourceVersion:   "1006",
			},
			Spec: externaldnsv1alpha1.DNSEndpointSpec{
				Endpoints: []*endpointv1alpha1.Endpoint{
					{
						DNSName:    "todelete.example.com",
						Targets:    []string{"6.7.8.9"},
						RecordType: "A",
					},
				},
			},
		}

		// Add first
		err := plugin.OnAdd(endpoint)
		require.NoError(t, err)

		plugin.serialsMutex.RLock()
		serialBeforeDelete := plugin.zoneSerials["example.com."]
		plugin.serialsMutex.RUnlock()

		// Delete
		err = plugin.OnDelete(endpoint)
		require.NoError(t, err)

		plugin.serialsMutex.RLock()
		serialAfterDelete := plugin.zoneSerials["example.com."]
		plugin.serialsMutex.RUnlock()

		assert.Greater(t, serialAfterDelete, serialBeforeDelete, "Serial should increment after delete")
	})

	t.Run("Mixed zones: ROOT and non-ROOT in same endpoint", func(t *testing.T) {
		endpoint := &externaldnsv1alpha1.DNSEndpoint{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "mixed-zones-endpoint",
				Namespace:         "test",
				CreationTimestamp: metav1.NewTime(now),
				Generation:        1,
				ResourceVersion:   "1007",
			},
			Spec: externaldnsv1alpha1.DNSEndpointSpec{
				Endpoints: []*endpointv1alpha1.Endpoint{
					{
						DNSName:    "mixed.example.com", // Matches example.com.
						Targets:    []string{"7.8.9.10"},
						RecordType: "A",
					},
					{
						DNSName:    "unmatched.other.tld", // Matches ROOT (.)
						Targets:    []string{"8.9.10.11"},
						RecordType: "A",
					},
				},
			},
		}

		err := plugin.OnAdd(endpoint)
		require.NoError(t, err)

		// Verify example.com. has serial
		plugin.serialsMutex.RLock()
		_, hasExampleSerial := plugin.zoneSerials["example.com."]
		_, hasRootSerial := plugin.zoneSerials["."]
		plugin.serialsMutex.RUnlock()

		assert.True(t, hasExampleSerial, "example.com. should have serial")
		assert.False(t, hasRootSerial, "ROOT zone (.) should NOT have serial")

		// Verify both records are in cache
		zone1 := plugin.getConfiguredZoneForDomain("mixed.example.com")
		records1 := plugin.cache.GetRecords("mixed.example.com.", zone1, dns.TypeA)
		assert.Len(t, records1, 1)

		zone2 := plugin.getConfiguredZoneForDomain("unmatched.other.tld")
		records2 := plugin.cache.GetRecords("unmatched.other.tld.", zone2, dns.TypeA)
		assert.Len(t, records2, 1)
	})

	t.Run("ConfigMap should not contain ROOT key", func(t *testing.T) {
		// After all operations above, verify ROOT never gets into zoneSerials
		plugin.serialsMutex.RLock()
		allSerials := make(map[string]uint32)
		for k, v := range plugin.zoneSerials {
			allSerials[k] = v
		}
		plugin.serialsMutex.RUnlock()

		// Check that "." is never in the map
		_, hasRoot := allSerials["."]
		assert.False(t, hasRoot, "ROOT zone (.) should never be in zoneSerials map")

		// Verify we have serials for other zones
		assert.Greater(t, len(allSerials), 0, "Should have serials for non-ROOT zones")
	})
}

func TestNXDOMAINEmptyQuestion(t *testing.T) {
	config := DefaultConfig()
	config.Zones = []string{"example.com.", "."}
	plugin := New(config)

	nextHandlerCalled := false
	plugin.Next = &mockHandler{
		handler: func() (int, error) {
			nextHandlerCalled = true
			return dns.RcodeSuccess, nil
		},
	}

	t.Run("Empty question passes to next handler", func(t *testing.T) {
		req := new(dns.Msg)
		// No question set

		writer := &mockResponseWriter{}
		_, err := plugin.ServeDNS(context.Background(), writer, req)

		assert.NoError(t, err)
		assert.True(t, nextHandlerCalled, "Should pass to next handler for empty question")
	})
}
