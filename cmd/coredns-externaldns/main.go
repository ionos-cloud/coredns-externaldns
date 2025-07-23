package main

import (
	"log"
	"os"

	"github.com/coredns/caddy"
	"github.com/coredns/coredns/core/dnsserver"
	"github.com/coredns/coredns/coremain"

	// Include the externaldns plugin
	_ "github.com/ionos-cloud/coredns-externaldns"

	// Include ALL CoreDNS plugins - complete set
	_ "github.com/coredns/coredns/core/plugin"
)

func init() {
	// Set complete plugin order - using available plugins from CoreDNS v1.12.2
	dnsserver.Directives = []string{
		"root",
		"metadata",
		"geoip",
		"cancel",
		"tls",
		"quic",
		"timeouts",
		"multisocket",
		"reload",
		"nsid",
		"bufsize",
		"bind",
		"debug",
		"trace",
		"ready",
		"health",
		"pprof",
		"prometheus",
		"errors",
		"log",
		"dnstap",
		"local",
		"dns64",
		"acl",
		"any",
		"chaos",
		"loadbalance",
		"tsig",
		"cache",
		"rewrite",
		"header",
		"dnssec",
		"autopath",
		"minimal",
		"template",
		"transfer",
		"hosts",
		"route53",
		"azure",
		"clouddns",
		"k8s_external",
		"kubernetes",
		"file",
		"auto",
		"secondary",
		"etcd",
		"externaldns", // Our plugin comes here
		"loop",
		"forward",
		"grpc",
		"erratic",
		"whoami",
		"on",
		"sign",
		"view",
	}
}

func main() {
	// Setup CoreDNS with our plugin
	caddy.TrapSignals()

	// Use default Corefile if none specified
	if len(os.Args) == 1 {
		// Check if Corefile exists in current directory
		if _, err := os.Stat("Corefile"); os.IsNotExist(err) {
			log.Println("No Corefile found and none specified. Please provide a Corefile or use -conf flag.")
			os.Exit(1)
		}
		// Add default Corefile path
		os.Args = append(os.Args, "-conf", "Corefile")
	}

	// Start CoreDNS
	coremain.Run()
}
