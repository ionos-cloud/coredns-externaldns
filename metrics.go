package externaldns

import (
	"github.com/coredns/coredns/plugin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Define metrics
var (
	externalDNSRequestCount = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: plugin.Namespace,
		Subsystem: "externaldns",
		Name:      "requests_total",
		Help:      "Counter of DNS requests made to external-dns plugin.",
	}, []string{"server", "proto", "type"})

	externalDNSCacheSize = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: plugin.Namespace,
		Subsystem: "externaldns",
		Name:      "cache_size",
		Help:      "Number of entries in the DNS cache.",
	}, []string{"server"})

	externalDNSEndpointEvents = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: plugin.Namespace,
		Subsystem: "externaldns",
		Name:      "endpoint_events_total",
		Help:      "Counter of DNSEndpoint events processed.",
	}, []string{"server", "type"})
)
