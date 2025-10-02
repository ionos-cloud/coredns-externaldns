# CoreDNS External-DNS Plugin

A CoreDNS plugin that serves DNS records from external-dns DNSEndpoint CRDs.

## How It Works

1. **Watches DNSEndpoint CRDs** in Kubernetes for DNS record definitions
2. **Caches records in memory** for fast DNS query responses  
3. **Serves DNS queries** directly from the cache
4. **Auto-generates PTR records** when enabled via annotation

## Quick Start

```bash
# Deploy with Helm
helm install coredns-externaldns oci://ghcr.io/ionos-cloud/coredns-externaldns/charts/coredns-externaldns

# Or build and run standalone
git clone https://github.com/ionos-cloud/coredns-externaldns
cd coredns-externaldns
make build
./coredns-externaldns -conf Corefile.standalone
```

## Configuration

### Corefile Example

```yaml
.:53 {
    externaldns {
        namespace default              # Optional: watch specific namespace
        ttl 300                       # Optional: default TTL (seconds)
        configmap_name zone-serials   # Optional: ConfigMap for serial persistence
        soa_ns ns1.example.com        # Optional: SOA nameserver for AXFR
        soa_mbox admin.example.com    # Optional: SOA mailbox for AXFR
        authoritative_zones example.com,test.com  # Optional: explicit zone list
    }
    forward . 8.8.8.8
    log
    errors
}
```

### Authoritative Zones

Configure `authoritative_zones` to define which DNS zones this plugin serves. This enables proper zone boundaries and DNS NOTIFY functionality.

By default, the plugin uses the zones from the Corefile where it's loaded (e.g., `example.com:53`). Setting `authoritative_zones` overrides this with an explicit zone list.

**Format:** Comma-separated zone list
```yaml
authoritative_zones example.com,internal.local,test.net
```

**With DNS NOTIFY:**
```yaml
transfer { to * }
externaldns {
    authoritative_zones example.com
    soa_ns ns1.example.com
}
```

### DNSEndpoint Example

```yaml
apiVersion: externaldns.k8s.io/v1alpha1
kind: DNSEndpoint
metadata:
  name: example
  annotations:
    coredns-externaldns.ionos.cloud/create-ptr: "true"  # Enable PTR records
spec:
  endpoints:
  - dnsName: app.example.com
    recordType: A
    targets: ["192.168.1.100"]
    recordTTL: 300
```

## PTR Record Feature

The plugin can automatically create reverse DNS (PTR) records for A and AAAA records.

### How to Enable

Add this annotation to your DNSEndpoint:

```yaml
metadata:
  annotations:
    coredns-externaldns.ionos.cloud/create-ptr: "true"
```

### How It Works

1. **Forward Record**: `app.example.com A 192.168.1.100`
2. **Auto-Generated PTR**: `100.1.168.192.in-addr.arpa PTR app.example.com`

When you query `192.168.1.100` for reverse DNS, it returns `app.example.com`.

**Supported**: Works with both A (IPv4) and AAAA (IPv6) records.

## RBAC Requirements

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: coredns-externaldns
rules:
- apiGroups: ["externaldns.k8s.io"]
  resources: ["dnsendpoints"]
  verbs: ["get", "list", "watch"]
- apiGroups: [""]
  resources: ["configmaps"]
  verbs: ["get", "list", "create", "update"]
```

## Supported Record Types

A, AAAA, CNAME, MX, TXT, SRV, PTR, NS, SOA

## License

Apache 2.0
