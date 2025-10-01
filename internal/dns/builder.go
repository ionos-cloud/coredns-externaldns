package dnsbuilder

import (
	"net"
	"strconv"
	"strings"

	"github.com/miekg/dns"
)

// RecordBuilder handles DNS record creation
type RecordBuilder struct{}

// NewRecordBuilder creates a new record builder
func NewRecordBuilder() *RecordBuilder {
	return &RecordBuilder{}
}

// CreateRecord creates a DNS resource record from components
func (rb *RecordBuilder) CreateRecord(name string, qtype uint16, ttl uint32, target string) dns.RR {
	name = dns.Fqdn(strings.ToLower(name))

	switch qtype {
	case dns.TypeA:
		return rb.createARecord(name, ttl, target)
	case dns.TypeAAAA:
		return rb.createAAAARecord(name, ttl, target)
	case dns.TypeCNAME:
		return rb.createCNAMERecord(name, ttl, target)
	case dns.TypeMX:
		return rb.createMXRecord(name, ttl, target)
	case dns.TypeTXT:
		return rb.createTXTRecord(name, ttl, target)
	case dns.TypeSRV:
		return rb.createSRVRecord(name, ttl, target)
	case dns.TypePTR:
		return rb.createPTRRecord(name, ttl, target)
	case dns.TypeNS:
		return rb.createNSRecord(name, ttl, target)
	case dns.TypeSOA:
		return rb.createSOARecord(name, ttl, target)
	default:
		return nil
	}
}

// RecordTypeToQType converts string record type to DNS query type
func (rb *RecordBuilder) RecordTypeToQType(recordType string) uint16 {
	switch strings.ToUpper(recordType) {
	case "A":
		return dns.TypeA
	case "AAAA":
		return dns.TypeAAAA
	case "CNAME":
		return dns.TypeCNAME
	case "MX":
		return dns.TypeMX
	case "TXT":
		return dns.TypeTXT
	case "SRV":
		return dns.TypeSRV
	case "PTR":
		return dns.TypePTR
	case "NS":
		return dns.TypeNS
	case "SOA":
		return dns.TypeSOA
	default:
		return 0
	}
}

// CreatePTRRecord creates PTR records for reverse DNS
func (rb *RecordBuilder) CreatePTRRecord(ip, target string, ttl uint32) dns.RR {
	arpa, err := dns.ReverseAddr(ip)
	if err != nil {
		return nil
	}
	return rb.CreateRecord(arpa, dns.TypePTR, ttl, target)
}

func (rb *RecordBuilder) createARecord(name string, ttl uint32, target string) dns.RR {
	ip := net.ParseIP(target)
	if ip == nil || ip.To4() == nil {
		return nil
	}

	return &dns.A{
		Hdr: dns.RR_Header{
			Name:   name,
			Rrtype: dns.TypeA,
			Class:  dns.ClassINET,
			Ttl:    ttl,
		},
		A: ip.To4(),
	}
}

func (rb *RecordBuilder) createAAAARecord(name string, ttl uint32, target string) dns.RR {
	ip := net.ParseIP(target)
	if ip == nil || ip.To4() != nil {
		return nil
	}

	return &dns.AAAA{
		Hdr: dns.RR_Header{
			Name:   name,
			Rrtype: dns.TypeAAAA,
			Class:  dns.ClassINET,
			Ttl:    ttl,
		},
		AAAA: ip,
	}
}

func (rb *RecordBuilder) createCNAMERecord(name string, ttl uint32, target string) dns.RR {
	return &dns.CNAME{
		Hdr: dns.RR_Header{
			Name:   name,
			Rrtype: dns.TypeCNAME,
			Class:  dns.ClassINET,
			Ttl:    ttl,
		},
		Target: dns.Fqdn(target),
	}
}

func (rb *RecordBuilder) createMXRecord(name string, ttl uint32, target string) dns.RR {
	parts := strings.Fields(target)
	if len(parts) != 2 {
		return nil
	}

	preference, err := strconv.ParseUint(parts[0], 10, 16)
	if err != nil {
		return nil
	}

	return &dns.MX{
		Hdr: dns.RR_Header{
			Name:   name,
			Rrtype: dns.TypeMX,
			Class:  dns.ClassINET,
			Ttl:    ttl,
		},
		Preference: uint16(preference),
		Mx:         dns.Fqdn(parts[1]),
	}
}

func (rb *RecordBuilder) createTXTRecord(name string, ttl uint32, target string) dns.RR {
	return &dns.TXT{
		Hdr: dns.RR_Header{
			Name:   name,
			Rrtype: dns.TypeTXT,
			Class:  dns.ClassINET,
			Ttl:    ttl,
		},
		Txt: []string{target},
	}
}

func (rb *RecordBuilder) createSRVRecord(name string, ttl uint32, target string) dns.RR {
	parts := strings.Fields(target)
	if len(parts) != 4 {
		return nil
	}

	priority, err := strconv.ParseUint(parts[0], 10, 16)
	if err != nil {
		return nil
	}

	weight, err := strconv.ParseUint(parts[1], 10, 16)
	if err != nil {
		return nil
	}

	port, err := strconv.ParseUint(parts[2], 10, 16)
	if err != nil {
		return nil
	}

	return &dns.SRV{
		Hdr: dns.RR_Header{
			Name:   name,
			Rrtype: dns.TypeSRV,
			Class:  dns.ClassINET,
			Ttl:    ttl,
		},
		Priority: uint16(priority),
		Weight:   uint16(weight),
		Port:     uint16(port),
		Target:   dns.Fqdn(parts[3]),
	}
}

func (rb *RecordBuilder) createPTRRecord(name string, ttl uint32, target string) dns.RR {
	return &dns.PTR{
		Hdr: dns.RR_Header{
			Name:   name,
			Rrtype: dns.TypePTR,
			Class:  dns.ClassINET,
			Ttl:    ttl,
		},
		Ptr: dns.Fqdn(target),
	}
}

func (rb *RecordBuilder) createNSRecord(name string, ttl uint32, target string) dns.RR {
	return &dns.NS{
		Hdr: dns.RR_Header{
			Name:   name,
			Rrtype: dns.TypeNS,
			Class:  dns.ClassINET,
			Ttl:    ttl,
		},
		Ns: dns.Fqdn(target),
	}
}

func (rb *RecordBuilder) createSOARecord(name string, ttl uint32, target string) dns.RR {
	parts := strings.Fields(target)
	if len(parts) < 7 {
		// Default SOA if not properly formatted
		return &dns.SOA{
			Hdr: dns.RR_Header{
				Name:   name,
				Rrtype: dns.TypeSOA,
				Class:  dns.ClassINET,
				Ttl:    ttl,
			},
			Ns:      "ns1." + name,
			Mbox:    "admin." + name,
			Serial:  uint32(1),
			Refresh: 3600,
			Retry:   1800,
			Expire:  1209600,
			Minttl:  300,
		}
	}

	serial, _ := strconv.ParseUint(parts[2], 10, 32)
	refresh, _ := strconv.ParseUint(parts[3], 10, 32)
	retry, _ := strconv.ParseUint(parts[4], 10, 32)
	expire, _ := strconv.ParseUint(parts[5], 10, 32)
	minttl, _ := strconv.ParseUint(parts[6], 10, 32)

	return &dns.SOA{
		Hdr: dns.RR_Header{
			Name:   name,
			Rrtype: dns.TypeSOA,
			Class:  dns.ClassINET,
			Ttl:    ttl,
		},
		Ns:      dns.Fqdn(parts[0]),
		Mbox:    dns.Fqdn(parts[1]),
		Serial:  uint32(serial),
		Refresh: uint32(refresh),
		Retry:   uint32(retry),
		Expire:  uint32(expire),
		Minttl:  uint32(minttl),
	}
}
