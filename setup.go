package externaldns

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/coredns/caddy"
	"github.com/coredns/coredns/core/dnsserver"
	"github.com/coredns/coredns/plugin"
	"github.com/coredns/coredns/plugin/transfer"
)

// Config holds the plugin configuration
type Config struct {
	// Core settings
	Namespace string
	TTL       uint32
	Zones     []string

	// SOA configuration
	SOA SOAConfig

	// ConfigMap settings for zone serial persistence
	ConfigMap ConfigMapConfig
}

// SOAConfig holds SOA record configuration
type SOAConfig struct {
	Nameserver string // NS field
	Mailbox    string // MBOX field
	Refresh    uint32 // Refresh interval
	Retry      uint32 // Retry interval
	Expire     uint32 // Expire time
	MinTTL     uint32 // Minimum TTL
}

// ConfigMapConfig holds ConfigMap settings
type ConfigMapConfig struct {
	Name      string // ConfigMap name
	Namespace string // ConfigMap namespace
}

// DefaultConfig returns a configuration with sensible defaults
func DefaultConfig() *Config {
	return &Config{
		Namespace: "",
		TTL:       300,
		SOA: SOAConfig{
			Nameserver: "ns1.example.com",
			Mailbox:    "admin.example.com",
			Refresh:    3600,
			Retry:      1800,
			Expire:     1209600,
			MinTTL:     300,
		},
		ConfigMap: ConfigMapConfig{
			Name:      "coredns-externaldns-zone-serials",
			Namespace: "",
		},
	}
}

// Validate checks if the configuration is valid
func (c *Config) Validate() error {
	if c.TTL == 0 {
		return fmt.Errorf("TTL cannot be zero")
	}
	if c.SOA.Nameserver == "" {
		return fmt.Errorf("SOA nameserver cannot be empty")
	}
	if c.SOA.Mailbox == "" {
		return fmt.Errorf("SOA mailbox cannot be empty")
	}
	if c.ConfigMap.Name == "" {
		return fmt.Errorf("ConfigMap name cannot be empty")
	}
	return nil
}

// init registers this plugin with CoreDNS
func init() {
	plugin.Register("externaldns", setup)
}

// setup sets up the externaldns plugin
func setup(c *caddy.Controller) error {
	ed, err := parseExternalDNS(c)
	if err != nil {
		return plugin.Error("externaldns", err)
	}

	// get the transfer plugin, so we can send notifies and send notifies on startup as well.
	c.OnStartup(func() error {
		t := dnsserver.GetConfig(c).Handler("transfer")
		if t == nil {
			return nil
		}
		ed.transfer = t.(*transfer.Transfer) // if found this must be OK.
		return nil
	})

	// Start the plugin
	err = ed.Start(context.Background())
	if err != nil {
		return plugin.Error("externaldns", err)
	}

	// Add the plugin to CoreDNS
	dnsserver.GetConfig(c).AddPlugin(func(next plugin.Handler) plugin.Handler {
		ed.Next = next
		return ed
	})

	// Register shutdown handler
	c.OnShutdown(func() error {
		ed.Stop()
		return nil
	})

	return nil
}

// parseExternalDNS parses the plugin configuration
func parseExternalDNS(c *caddy.Controller) (*Plugin, error) {
	config := DefaultConfig()

	for c.Next() {
		// Parse zone names
		config.Zones = append(config.Zones, plugin.OriginsFromArgsOrServerBlock(c.RemainingArgs(), c.ServerBlockKeys)...)

		for c.NextBlock() {
			switch c.Val() {
			// Core settings
			case "namespace":
				if !c.NextArg() {
					return nil, c.ArgErr()
				}
				config.Namespace = c.Val()
			case "ttl":
				if !c.NextArg() {
					return nil, c.ArgErr()
				}
				t, err := strconv.ParseUint(c.Val(), 10, 32)
				if err != nil {
					return nil, c.Errf("invalid TTL: %v", err)
				}
				config.TTL = uint32(t)

			// SOA configuration
			case "soa_ns", "soa_nameserver":
				if !c.NextArg() {
					return nil, c.ArgErr()
				}
				config.SOA.Nameserver = c.Val()
			case "soa_mbox", "soa_mailbox":
				if !c.NextArg() {
					return nil, c.ArgErr()
				}
				config.SOA.Mailbox = c.Val()
			case "soa_refresh":
				if !c.NextArg() {
					return nil, c.ArgErr()
				}
				r, err := strconv.ParseUint(c.Val(), 10, 32)
				if err != nil {
					return nil, c.Errf("invalid SOA refresh: %v", err)
				}
				config.SOA.Refresh = uint32(r)
			case "soa_retry":
				if !c.NextArg() {
					return nil, c.ArgErr()
				}
				r, err := strconv.ParseUint(c.Val(), 10, 32)
				if err != nil {
					return nil, c.Errf("invalid SOA retry: %v", err)
				}
				config.SOA.Retry = uint32(r)
			case "soa_expire":
				if !c.NextArg() {
					return nil, c.ArgErr()
				}
				e, err := strconv.ParseUint(c.Val(), 10, 32)
				if err != nil {
					return nil, c.Errf("invalid SOA expire: %v", err)
				}
				config.SOA.Expire = uint32(e)
			case "soa_minttl":
				if !c.NextArg() {
					return nil, c.ArgErr()
				}
				m, err := strconv.ParseUint(c.Val(), 10, 32)
				if err != nil {
					return nil, c.Errf("invalid SOA minttl: %v", err)
				}
				config.SOA.MinTTL = uint32(m)

			// ConfigMap configuration
			case "configmap_name":
				if !c.NextArg() {
					return nil, c.ArgErr()
				}
				config.ConfigMap.Name = c.Val()
			case "configmap_namespace":
				if !c.NextArg() {
					return nil, c.ArgErr()
				}
				config.ConfigMap.Namespace = c.Val()

			// Authoritative Zones
			case "authoritative_zones":
				if !c.NextArg() {
					return nil, c.ArgErr()
				}
				config.Zones = strings.Split(c.Val(), ",")
			default:
				return nil, c.Errf("unknown property: %s", c.Val())
			}
		}
	}

	// Validate configuration
	if err := config.Validate(); err != nil {
		return nil, c.Errf("configuration validation failed: %v", err)
	}

	// Create plugin with structured configuration
	plugin := New(config)

	return plugin, nil
}
