package externaldns

import (
	"testing"

	"github.com/coredns/caddy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig()

	assert.Equal(t, "", config.Namespace)
	assert.Equal(t, uint32(300), config.TTL)
	assert.Equal(t, "ns1.example.com", config.SOA.Nameserver)
	assert.Equal(t, "admin.example.com", config.SOA.Mailbox)
	assert.Equal(t, uint32(3600), config.SOA.Refresh)
	assert.Equal(t, uint32(1800), config.SOA.Retry)
	assert.Equal(t, uint32(1209600), config.SOA.Expire)
	assert.Equal(t, uint32(300), config.SOA.MinTTL)
	assert.Equal(t, "coredns-externaldns-zone-serials", config.ConfigMap.Name)
	assert.Equal(t, "", config.ConfigMap.Namespace)
}

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name      string
		config    *Config
		wantError bool
		errorMsg  string
	}{
		{
			name:      "valid config",
			config:    DefaultConfig(),
			wantError: false,
		},
		{
			name: "zero TTL",
			config: &Config{
				TTL: 0,
				SOA: SOAConfig{
					Nameserver: "ns1.example.com",
					Mailbox:    "admin.example.com",
				},
				ConfigMap: ConfigMapConfig{
					Name: "test",
				},
			},
			wantError: true,
			errorMsg:  "TTL cannot be zero",
		},
		{
			name: "empty nameserver",
			config: &Config{
				TTL: 300,
				SOA: SOAConfig{
					Nameserver: "",
					Mailbox:    "admin.example.com",
				},
				ConfigMap: ConfigMapConfig{
					Name: "test",
				},
			},
			wantError: true,
			errorMsg:  "SOA nameserver cannot be empty",
		},
		{
			name: "empty mailbox",
			config: &Config{
				TTL: 300,
				SOA: SOAConfig{
					Nameserver: "ns1.example.com",
					Mailbox:    "",
				},
				ConfigMap: ConfigMapConfig{
					Name: "test",
				},
			},
			wantError: true,
			errorMsg:  "SOA mailbox cannot be empty",
		},
		{
			name: "empty configmap name",
			config: &Config{
				TTL: 300,
				SOA: SOAConfig{
					Nameserver: "ns1.example.com",
					Mailbox:    "admin.example.com",
				},
				ConfigMap: ConfigMapConfig{
					Name: "",
				},
			},
			wantError: true,
			errorMsg:  "ConfigMap name cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.wantError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestParseExternalDNSBasic(t *testing.T) {
	c := caddy.NewTestController("dns", `externaldns`)

	plugin, err := parseExternalDNS(c)
	require.NoError(t, err)
	require.NotNil(t, plugin)

	// Should use defaults
	assert.Equal(t, "", plugin.config.Namespace)
	assert.Equal(t, uint32(300), plugin.config.TTL)
	assert.Equal(t, "ns1.example.com", plugin.config.SOA.Nameserver)
	assert.Equal(t, "admin.example.com", plugin.config.SOA.Mailbox)
}

func TestParseExternalDNSWithConfig(t *testing.T) {
	config := `externaldns {
		namespace test-namespace
		ttl 600
		soa_ns ns1.test.com
		soa_mbox admin.test.com
		soa_refresh 7200
		soa_retry 3600
		soa_expire 2419200
		soa_minttl 600
		configmap_name custom-serials
		configmap_namespace kube-system
	}`

	c := caddy.NewTestController("dns", config)

	plugin, err := parseExternalDNS(c)
	require.NoError(t, err)
	require.NotNil(t, plugin)

	assert.Equal(t, "test-namespace", plugin.config.Namespace)
	assert.Equal(t, uint32(600), plugin.config.TTL)
	assert.Equal(t, "ns1.test.com", plugin.config.SOA.Nameserver)
	assert.Equal(t, "admin.test.com", plugin.config.SOA.Mailbox)
	assert.Equal(t, "custom-serials", plugin.config.ConfigMap.Name)
	assert.Equal(t, "kube-system", plugin.config.ConfigMap.Namespace)
}

func TestParseExternalDNSAlternativeNames(t *testing.T) {
	config := `externaldns {
		soa_nameserver ns2.example.com
		soa_mailbox postmaster.example.com
	}`

	c := caddy.NewTestController("dns", config)

	plugin, err := parseExternalDNS(c)
	require.NoError(t, err)
	require.NotNil(t, plugin)

	assert.Equal(t, "ns2.example.com", plugin.config.SOA.Nameserver)
	assert.Equal(t, "postmaster.example.com", plugin.config.SOA.Mailbox)
}

func TestParseExternalDNSInvalidTTL(t *testing.T) {
	config := `externaldns {
		ttl invalid
	}`

	c := caddy.NewTestController("dns", config)

	_, err := parseExternalDNS(c)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid TTL")
}

func TestParseExternalDNSInvalidSOARefresh(t *testing.T) {
	config := `externaldns {
		soa_refresh invalid
	}`

	c := caddy.NewTestController("dns", config)

	_, err := parseExternalDNS(c)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid SOA refresh")
}

func TestParseExternalDNSMissingArgs(t *testing.T) {
	tests := []struct {
		name   string
		config string
	}{
		{
			name:   "missing ttl arg",
			config: `externaldns { ttl }`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := caddy.NewTestController("dns", tt.config)
			_, err := parseExternalDNS(c)
			require.Error(t, err)
		})
	}
}

func TestParseExternalDNSUnknownProperty(t *testing.T) {
	config := `externaldns {
		unknown_property value
	}`

	c := caddy.NewTestController("dns", config)

	_, err := parseExternalDNS(c)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown property: unknown_property")
}

func TestParseExternalDNSValidationFailure(t *testing.T) {
	config := `externaldns {
		ttl 0
	}`

	c := caddy.NewTestController("dns", config)

	_, err := parseExternalDNS(c)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "configuration validation failed")
	assert.Contains(t, err.Error(), "TTL cannot be zero")
}
