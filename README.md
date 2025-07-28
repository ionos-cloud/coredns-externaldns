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
- **Automatic PTR records**: Optionally creates reverse DNS (PTR) records for A and AAAA records when enabled via annotation
- **AXFR support**: Supports zone transfers (AXFR) for slave zone configurations
  - NS records
  - SOA records
- **Wildcard support**: Supports wildcard DNS records for subdomain matching
- **Kubernetes native**: Uses Kubernetes client-go for efficient CRD watching
- **Configurable**: Supports namespace filtering, custom TTLs, and debug logging
- **High performance**: In-memory cache provides fast DNS query responses
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

| Feature | Plugin Integration | Standalone | Helm Chart |
|---------|-------------------|------------|------------|
| **Deployment** | Requires CoreDNS rebuild | Single binary | helm install |
| **Plugin Management** | Manual plugin.cfg editing | Pre-configured | Managed via chart |
| **Build Command** | `make build-plugin` | `make build-standalone` | N/A |
| **RBAC Setup** | Manual | Manual | Automated |
| **CRD Creation** | Manual | Manual | Automated |
| **Kubernetes Integration** | Custom manifests | Custom manifests | Native Helm |
| **Best For** | Existing CoreDNS | Simple setups | Production K8s |

For detailed integration instructions, see [INTEGRATION.md](INTEGRATION.md).

### Method 3: Helm Chart (Kubernetes)

The easiest way to deploy in Kubernetes is using the official Helm chart:

```bash
# Install with default configuration
helm install my-coredns oci://ghcr.io/ionos-cloud/coredns-externaldns/charts/coredns-externaldns

# Install specific version
helm install my-coredns oci://ghcr.io/ionos-cloud/coredns-externaldns/charts/coredns-externaldns --version 1.0.0

# Install with custom configuration
helm install my-coredns oci://ghcr.io/ionos-cloud/coredns-externaldns/charts/coredns-externaldns -f values.yaml
```

The Helm chart provides:
- **Automated CRD creation**: DNSEndpoint CRDs are created automatically
- **RBAC configuration**: Proper service accounts and permissions
- **CoreDNS integration**: Uses official CoreDNS chart as dependency
- **OCI registry**: Published to GitHub Container Registry
- **Production ready**: Includes best practices for Kubernetes deployment

For complete Helm chart documentation, see [deploy/charts/coredns-externaldns/README.md](deploy/charts/coredns-externaldns/README.md).

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
    }
    forward . 8.8.8.8
}
```

### Configuration Options

- `namespace`: Kubernetes namespace to watch for DNSEndpoint resources. If not specified, watches all namespaces
- `ttl`: Default TTL for DNS records in seconds (default: 300)

**Note**: This plugin uses in-cluster service account authentication. Ensure proper RBAC permissions are configured.

Debug logging is controlled by CoreDNS's built-in debug system. To enable debug logging, start CoreDNS with the `-debug` flag or add `debug` directive to your Corefile.

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

### Automatic PTR Record Creation

The plugin supports automatic creation of PTR (reverse DNS) records for A and AAAA records when the DNSEndpoint has the appropriate annotation. This is useful for proper reverse DNS resolution.

To enable automatic PTR record creation, add this annotation to your DNSEndpoint:

```yaml
apiVersion: externaldns.k8s.io/v1alpha1
kind: DNSEndpoint
metadata:
  name: server-with-ptr
  namespace: default
  annotations:
    # Enable automatic PTR record creation for A and AAAA records
    coredns-externaldns.ionos.cloud/create-ptr: "true"
spec:
  endpoints:
  # A record - will automatically create PTR record: 10.1.168.192.in-addr.arpa. -> server.example.com
  - dnsName: server.example.com
    recordType: A
    recordTTL: 300
    targets:
    - 192.168.1.10
  
  # AAAA record - will automatically create PTR record in ip6.arpa domain
  - dnsName: ipv6-server.example.com
    recordType: AAAA
    recordTTL: 300
    targets:
    - 2001:db8::1
  
  # CNAME and other record types are not affected
  - dnsName: alias.example.com
    recordType: CNAME
    recordTTL: 300
    targets:
    - server.example.com
```

#### How PTR Records Work

- **IPv4**: For A records, PTR records are created in the `in-addr.arpa.` domain
  - Example: `192.168.1.10` → `10.1.168.192.in-addr.arpa. PTR server.example.com`
- **IPv6**: For AAAA records, PTR records are created in the `ip6.arpa.` domain
  - Example: `2001:db8::1` → `1.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.8.b.d.0.1.0.0.2.ip6.arpa. PTR ipv6-server.example.com`

**Note**: Only A and AAAA record types will generate PTR records. Other record types (CNAME, MX, TXT, etc.) are not affected by this annotation.

### Zone Transfer (AXFR) Support

The plugin supports AXFR (Authoritative Zone Transfer) for slave zone configurations. This allows secondary DNS servers to synchronize zone data from the CoreDNS instance running this plugin.

#### How AXFR Works

1. **Zone Management**: The plugin automatically organizes DNS records into zones based on domain names
2. **Serial Numbers**: Each zone maintains a serial number that updates when records are added, modified, or removed
3. **SOA Records**: Automatically generates SOA (Start of Authority) records for each zone
4. **Transfer Protocol**: Supports standard DNS AXFR requests from slave servers

#### Example Slave Zone Configuration

To configure a slave zone that transfers from this CoreDNS instance:

**On the slave DNS server** (e.g., BIND):
```bind
zone "example.com" {
    type slave;
    masters { 192.168.1.100; };  // IP of CoreDNS with externaldns plugin
    file "slaves/example.com.zone";
};
```

**On CoreDNS with externaldns plugin**, ensure your Corefile includes the `transfer` plugin:
```yaml
example.com:53 {
    externaldns {
        namespace default
        ttl 300
    }
    
    # Enable zone transfers
    transfer {
        to *  # Allow transfers to any IP (adjust as needed for security)
    }
}
```

#### AXFR Features

- **Automatic Zone Detection**: Zones are automatically created based on DNS record domain names
- **Real-time Updates**: Zone serials are updated immediately when DNSEndpoint resources change
- **SOA Generation**: Automatically generates SOA records with appropriate serial numbers
- **Multiple Zones**: Supports multiple zones simultaneously

#### Security Considerations

- Configure the `transfer` plugin to restrict AXFR access to authorized slave servers only
- Use firewall rules to limit access to DNS port 53 from trusted networks
- Consider using TSIG (Transaction Signatures) for authenticated transfers

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

### Kubernetes Deployment

For Kubernetes deployments, all assets are organized in the `deploy/` directory:

```bash
# Helm chart deployment (recommended)
helm install my-coredns oci://ghcr.io/ionos-cloud/coredns-externaldns/charts/coredns-externaldns

# Raw Kubernetes manifests
kubectl apply -f deploy/k8s-deployment.yaml

# Using Makefile
make k8s-apply
```

The `deploy/` directory contains:
- **Helm chart**: Production-ready with RBAC, CRD creation, and configuration options
- **Raw manifests**: Simple Kubernetes deployment for testing/development
- **Documentation**: Complete deployment guides and examples

For detailed deployment instructions, see [deploy/README.md](deploy/README.md).

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

Debug logging is controlled by CoreDNS's built-in debug system. To enable debug logging, you can:

1. **Start CoreDNS with debug flag**:
   ```bash
   ./coredns-externaldns -debug -conf Corefile
   ```

2. **Add debug directive to your Corefile**:
   ```yaml
   .:53 {
       debug
       externaldns {
           namespace default
       }
       forward . 8.8.8.8
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

## Required RBAC Permissions

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

## Service Account Configuration

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
