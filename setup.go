package externaldns

import (
	"strconv"
	"time"

	"github.com/coredns/caddy"
	"github.com/coredns/coredns/core/dnsserver"
	"github.com/coredns/coredns/plugin"
)

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

	// Start the plugin
	err = ed.Start()
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
		if ed.cache != nil {
			ed.cache.Stop()
		}
		return nil
	})

	return nil
}

// parseExternalDNS parses the plugin configuration
func parseExternalDNS(c *caddy.Controller) (*ExternalDNS, error) {
	ed := &ExternalDNS{
		ttl:                   300,              // Default TTL of 5 minutes
		metricsUpdateInterval: 30 * time.Second, // Default metrics update interval of 30 seconds
	}

	for c.Next() {
		for c.NextBlock() {
			switch c.Val() {
			case "namespace":
				if !c.NextArg() {
					return nil, c.ArgErr()
				}
				ed.namespace = c.Val()
			case "ttl":
				if !c.NextArg() {
					return nil, c.ArgErr()
				}
				ttl, err := strconv.Atoi(c.Val())
				if err != nil {
					return nil, c.Errf("invalid TTL value: %s", c.Val())
				}
				ed.ttl = uint32(ttl)
			case "metrics_interval":
				if !c.NextArg() {
					return nil, c.ArgErr()
				}
				interval, err := time.ParseDuration(c.Val())
				if err != nil {
					return nil, c.Errf("invalid metrics interval value: %s", c.Val())
				}
				ed.metricsUpdateInterval = interval
			default:
				return nil, c.Errf("unknown property '%s'", c.Val())
			}
		}
	}

	// Create cache with the configured metrics interval
	ed.cache = NewDNSCache(ed.metricsUpdateInterval)

	return ed, nil
}
