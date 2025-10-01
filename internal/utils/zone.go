package utils

import (
	"strings"

	"github.com/miekg/dns"
)

// ExtractZoneFromDomain extracts the zone name from a domain name
// For wildcard domains (*.example.com), removes the wildcard first
// For all multi-part domains, uses everything after the first dot as the zone
// Examples:
//
//	*.foo.stg.test.com -> stg.test.com.
//	foo.stg.test.com -> stg.test.com.
//	www.example.com -> example.com.
func ExtractZoneFromDomain(name string) string {
	// Normalize the name
	name = dns.Fqdn(strings.ToLower(name))

	// For wildcard domains, remove the wildcard part first
	name = strings.TrimPrefix(name, "*.")

	parts := dns.SplitDomainName(name)
	if len(parts) == 0 {
		return name
	}

	// For single-part domains, return as-is
	if len(parts) == 1 {
		return dns.Fqdn(parts[0])
	}

	// For all multi-part domains, use everything after the first dot as the zone
	// This handles both 2-part and 3+ part domains consistently:
	// - www.example.com -> example.com
	// - foo.stg.test.com -> stg.test.com
	// - example.com -> com (but this case is rare in practice)
	return dns.Fqdn(strings.Join(parts[1:], "."))
}
