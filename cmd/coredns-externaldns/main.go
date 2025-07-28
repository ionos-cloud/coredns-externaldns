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

// Version information (can be overridden with -ldflags)
var (
	appName    = "CoreDNS-ExternalDNS"
	appVersion = "dev"
)

func init() {
	// Find etcd and insert externaldns plugin after it
	inserted := false
	for i, dir := range dnsserver.Directives {
		if dir == "etcd" {
			// Insert externaldns after etcd using slice insertion
			dnsserver.Directives = append(
				dnsserver.Directives[:i+1],
				append([]string{"externaldns"}, dnsserver.Directives[i+1:]...)...,
			)

			inserted = true
			break
		}
	}
	if !inserted {
		panic("etcd directive not found, cannot insert externaldns plugin")
	}

	// Override CoreDNS app name and version after CoreDNS init
	caddy.AppName = appName
	caddy.AppVersion = appVersion
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
