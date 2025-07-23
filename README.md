# CoreDNS External-DNS Plugin

This CoreDNS plugin integrates with external-dns by watching DNSEndpoint Custom Resource Definitions (CRDs) and serving DNS queries directly from an in-memory cache. This allows CoreDNS to serve DNS records managed by external-dns without requiring external DNS providers for internal resolution.

## Features

- **Real-time sync**: Watches DNSEndpoint CRDs for changes and updates the in-memory cache immediately
- **Complete record type support**: Supports all DNS record types that external-dns supports:
  - A records
  - AAAA records  
  - CNAME records
  - MX records
  - TXT records
  - SRV records
  - PTR records
  - NS records
  - SOA records
- **Wildcard support**: Supports wildcard DNS records for subdomain matching
- **Kubernetes native**: Uses Kubernetes client-go for efficient CRD watching
- **Configurable**: Supports namespace filtering, custom TTLs, and debug logging
- **High performance**: In-memory cache provides fast DNS query responses
- **Comprehensive plugin support**: Standalone version includes 45+ CoreDNS plugins for maximum functionality
- **Production-ready**: Docker containers with multi-stage builds for optimal size and security

## Installation

This plugin supports two integration methods:

1. **Plugin integration**: Add to an existing CoreDNS installation
2. **Standalone**: Run as a standalone CoreDNS instance with the plugin included

### Prerequisites

- CoreDNS 1.8.0 or later (for plugin integration)
- Kubernetes cluster with external-dns DNSEndpoint CRDs installed
- Go 1.21 or later for building

### Building

```bash
git clone https://github.com/ionos-cloud/coredns-externaldns
cd coredns-externaldns
go mod tidy

# Build both plugin and standalone versions
make build

# Or build specific versions:
make build-plugin      # For plugin integration
make build-standalone  # For standalone usage
```

### Method 1: Plugin Integration (Traditional)

Add this plugin to your CoreDNS build by modifying the CoreDNS `plugin.cfg` file:

```
externaldns:github.com/ionos-cloud/coredns-externaldns
```

Then rebuild CoreDNS:

```bash
go generate
go build
```

Use the plugin in your existing CoreDNS Corefile (see `Corefile.example`).

### Method 2: Standalone (Recommended)

The standalone method includes a pre-built CoreDNS with the externaldns plugin already integrated:

```bash
# Build the standalone binary
make build-standalone

# Run with a Corefile
./coredns-externaldns -conf Corefile.standalone

# Or run with default Corefile (if exists in current directory)
./coredns-externaldns
```

This method is recommended because:
- No need to rebuild CoreDNS from source
- Simpler deployment and distribution
- Includes commonly needed CoreDNS plugins
- Ready to run out of the box

### Integration Methods Comparison

| Feature | Plugin Integration | Standalone |
|---------|-------------------|------------|
| **Setup Complexity** | High (rebuild CoreDNS) | Low (ready to run) |
| **Binary Size** | Smaller (plugin only) | Larger (full CoreDNS) |
| **Deployment** | Requires CoreDNS rebuild | Single binary |
| **Plugin Management** | Manual plugin.cfg editing | Pre-configured |
| **Recommended For** | Existing CoreDNS setups | New deployments |
| **Build Command** | `make build-plugin` | `make build-standalone` |

### Included Plugins (Standalone)

The standalone version includes 45 CoreDNS plugins providing comprehensive DNS functionality:

**Core DNS Features**: acl, any, auto, autopath, bind, bufsize, cache, cancel, chaos, debug, dns64, dnssec, dnstap, erratic, errors, etcd, file, forward, geoip, grpc, header, health, hosts, loadbalance, local, log, loop, metadata, minimal, nsid, pprof, ready, reload, rewrite, root, secondary, template, tls, trace, transfer, tsig, whoami

**Additional Features**: 
- **externaldns**: Our custom plugin for ExternalDNS CRD integration
- **prometheus**: Metrics collection and monitoring

**Excluded Plugins**: Some plugins are excluded due to CoreDNS v1.12.2 compatibility or dependency conflicts: `on`, `quic`, `timeouts`, `multisocket`, `sign`, `view`, cloud providers (`azure`, `clouddns`, `route53`), and kubernetes plugins.

To see all available plugins in your build:
```bash
./coredns-externaldns -plugins
```

For detailed integration instructions, see [INTEGRATION.md](INTEGRATION.md).

## Configuration

### Basic Configuration

#### For Standalone CoreDNS

Use the provided `Corefile.standalone` as a starting point:

```yaml
.:53 {
    log
    errors
    health
    prometheus
    
    externaldns {
        # Optional: specify namespace to watch (default: all namespaces)
        # namespace default
        
        # Optional: set default TTL in seconds (default: 300)
        ttl 300
        
        # Optional: enable debug logging
        # debug
    }
    
    # Forward queries not handled by externaldns to upstream DNS
    forward . 8.8.8.8 8.8.4.4
}
```

#### For Plugin Integration

When using the plugin with existing CoreDNS, add the externaldns block to your Corefile:

```yaml
. {
    externaldns {
        namespace my-namespace          # Optional: watch specific namespace only
        ttl 300                        # Optional: default TTL in seconds (default: 300)
        debug                          # Optional: enable debug logging
    }
    forward . 8.8.8.8
}
```

### Configuration Options

- `namespace`: Kubernetes namespace to watch for DNSEndpoint resources. If not specified, watches all namespaces
- `ttl`: Default TTL for DNS records in seconds (default: 300)
- `debug`: Enable debug logging for troubleshooting

**Note**: This plugin uses in-cluster service account authentication. Ensure proper RBAC permissions are configured.

### Example DNSEndpoint CRD

The plugin watches for DNSEndpoint resources like this:

```yaml
apiVersion: externaldns.k8s.io/v1alpha1
kind: DNSEndpoint
metadata:
  name: example-endpoint
  namespace: default
spec:
  endpoints:
  - dnsName: app.example.com
    recordType: A
    recordTTL: 300
    targets:
    - 192.168.1.10
    - 192.168.1.11
  - dnsName: "*.api.example.com"
    recordType: CNAME
    recordTTL: 600
    targets:
    - app.example.com
  - dnsName: mail.example.com
    recordType: MX
    recordTTL: 300
    targets:
    - "10 mail1.example.com"
    - "20 mail2.example.com"
```

## Development and Building

### Available Makefile Targets

The project includes a comprehensive Makefile with the following targets:

#### Building
- `make build` - Build both plugin and standalone binaries
- `make build-plugin` - Build only the plugin binary (for existing CoreDNS)
- `make build-standalone` - Build only the standalone CoreDNS with plugin

#### Testing and Quality
- `make test` - Run unit tests
- `make lint` - Run code linter
- `make deps` - Download and tidy dependencies
- `make verify` - Verify dependencies

#### Deployment
- `make docker-build` - Build Docker image
- `make docker-push` - Push Docker image to registry
- `make k8s-apply` - Apply Kubernetes manifests
- `make k8s-delete` - Delete Kubernetes resources

#### Utilities
- `make clean` - Clean build artifacts
- `make help` - Show all available targets

### Quick Start for Development

```bash
# Clone and setup
git clone https://github.com/ionos-cloud/coredns-externaldns
cd coredns-externaldns

# Install dependencies and run tests
make deps
make test

# Build both versions
make build

# Run standalone version for testing
./coredns-externaldns -conf Corefile.standalone
```

### Docker Deployment

The project includes a Dockerfile optimized for the standalone version:

```bash
# Build Docker image
make docker-build

# Run locally (maps to port 5353 to avoid conflicts)
make docker-run

# Run with custom Corefile
make docker-run-custom

# Manual Docker commands
docker build -t coredns-externaldns-standalone .
docker run --rm -p 5353:53/udp coredns-externaldns-standalone:latest
```

## Supported Record Types

### A Records
```yaml
- dnsName: web.example.com
  recordType: A
  targets:
  - 192.168.1.10
```

### AAAA Records
```yaml
- dnsName: web.example.com
  recordType: AAAA
  targets:
  - 2001:db8::1
```

### CNAME Records
```yaml
- dnsName: www.example.com
  recordType: CNAME
  targets:
  - web.example.com
```

### MX Records
```yaml
- dnsName: example.com
  recordType: MX
  targets:
  - "10 mail1.example.com"
  - "20 mail2.example.com"
```

### TXT Records
```yaml
- dnsName: example.com
  recordType: TXT
  targets:
  - "v=spf1 include:_spf.google.com ~all"
  - "google-site-verification=..."
```

### SRV Records
```yaml
- dnsName: _http._tcp.example.com
  recordType: SRV
  targets:
  - "10 60 8080 web1.example.com"
  - "10 40 8080 web2.example.com"
```

### PTR Records
```yaml
- dnsName: 10.1.168.192.in-addr.arpa
  recordType: PTR
  targets:
  - web.example.com
```

### NS Records
```yaml
- dnsName: subdomain.example.com
  recordType: NS
  targets:
  - ns1.example.com
  - ns2.example.com
```

## How It Works

1. **Initialization**: The plugin connects to the Kubernetes API server using the in-cluster service account and sets up watchers for DNSEndpoint resources
2. **Authentication**: Uses Kubernetes service account token for authentication with proper RBAC permissions
3. **Initial Sync**: All existing DNSEndpoint resources are loaded into the in-memory cache
4. **Real-time Updates**: The plugin watches for CREATE, UPDATE, and DELETE events on DNSEndpoint resources
5. **Cache Management**: DNS records are automatically added, updated, or removed from the cache based on CRD changes
6. **Query Handling**: When DNS queries arrive, the plugin checks its cache first before passing to the next plugin in the chain

## Wildcard Support

The plugin supports wildcard DNS records for subdomain matching:

```yaml
- dnsName: "*.api.example.com"
  recordType: A
  targets:
  - 192.168.1.100
```

This will match queries for `service1.api.example.com`, `service2.api.example.com`, etc.

## Monitoring and Debugging

### Prometheus Metrics

The plugin exposes the following Prometheus metrics:

#### DNS Request Metrics
- **`coredns_externaldns_requests_total`** (Counter)
  - Description: Total number of DNS requests processed by the external-dns plugin
  - Labels: `server`, `proto`, `type`
  - Example: `coredns_externaldns_requests_total{server="dns://.:53",proto="udp",type="A"} 42`

#### Cache Metrics  
- **`coredns_externaldns_cache_size`** (Gauge)
  - Description: Current number of DNS records in the cache
  - Labels: `server`
  - Example: `coredns_externaldns_cache_size{server="coredns"} 156`

#### DNSEndpoint Event Metrics
- **`coredns_externaldns_endpoint_events_total`** (Counter)
  - Description: Total number of DNSEndpoint CRD events processed
  - Labels: `server`, `type`
  - Example: `coredns_externaldns_endpoint_events_total{server="coredns",type="ADDED"} 23`

### Metrics Endpoints

Metrics are exposed on the standard CoreDNS metrics endpoint (typically `:9153/metrics`).

### Debug Logging

Enable debug logging in the configuration:

```yaml
externaldns {
    debug
}
```

This will log:
- DNSEndpoint events (CREATE, UPDATE, DELETE) 
- DNS query processing
- Cache operations
- Cache size changes

## Performance Considerations

- **Memory Usage**: The plugin keeps all DNS records in memory for fast lookup
- **Watch Efficiency**: Uses Kubernetes watch API for efficient real-time updates
- **Concurrent Safe**: All cache operations are protected by read-write mutexes
- **Graceful Restart**: Handles watch connection failures with automatic retry

## Troubleshooting

### Common Issues

1. **Plugin not loading**: Ensure the plugin is properly registered in CoreDNS plugin.cfg
2. **No records found**: Check that DNSEndpoint CRDs exist and are in the correct namespace
3. **Permission denied**: Ensure CoreDNS service account has proper RBAC permissions to watch DNSEndpoint resources
4. **Authentication errors**: Verify the service account token is properly mounted and has the required permissions

### Required RBAC Permissions

The plugin requires a service account with the following RBAC permissions:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: coredns-externaldns
rules:
- apiGroups: ["externaldns.k8s.io"]
  resources: ["dnsendpoints"]
  verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: coredns-externaldns
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: coredns-externaldns
subjects:
- kind: ServiceAccount
  name: coredns-externaldns
  namespace: kube-system
```

### Service Account Configuration

Ensure your CoreDNS deployment uses the service account with proper permissions:

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: coredns-externaldns
  namespace: kube-system
---
# Pod specification should include:
spec:
  serviceAccountName: coredns-externaldns
```

## Contributing

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Add tests if applicable
5. Submit a pull request

## License

This project is licensed under the Apache 2.0 License - see the LICENSE file for details.
