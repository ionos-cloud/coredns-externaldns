# CoreDNS External-DNS Plugin Integration Guide

This document provides detailed instructions for integrating the external-dns plugin with CoreDNS using both supported methods.

## Method 1: Plugin Integration (Traditional CoreDNS Plugin)

### Prerequisites
- Existing CoreDNS installation or source code
- Go 1.21+ for building
- Access to modify CoreDNS build configuration

### Step-by-Step Integration

1. **Clone the plugin repository**:
   ```bash
   git clone https://github.com/ionos-cloud/coredns-externaldns
   cd coredns-externaldns
   ```

2. **Build the plugin**:
   ```bash
   make build-plugin
   ```

3. **Integrate with CoreDNS source**:
   ```bash
   # Clone CoreDNS source
   git clone https://github.com/coredns/coredns
   cd coredns
   
   # Add plugin to plugin.cfg
   echo "externaldns:github.com/ionos-cloud/coredns-externaldns" >> plugin.cfg
   
   # Generate and build
   go generate
   go build
   ```

4. **Configure Corefile**:
   ```
   .:53 {
       externaldns {
           namespace default
           ttl 300
           debug
       }
       forward . 8.8.8.8
   }
   ```

### Advantages
- Integrates with existing CoreDNS installations
- Smaller binary size (plugin only)
- Full control over CoreDNS configuration

### Disadvantages
- Requires rebuilding CoreDNS from source
- More complex setup process
- Need to maintain CoreDNS build pipeline

## Method 2: Standalone CoreDNS (Recommended)

### Prerequisites
- Go 1.21+ for building (or use pre-built binaries)
- Kubernetes cluster access for DNSEndpoint resources

### Step-by-Step Setup

1. **Clone and build**:
   ```bash
   git clone https://github.com/ionos-cloud/coredns-externaldns
   cd coredns-externaldns
   make build-standalone
   ```

2. **Create Corefile**:
   ```bash
   cp Corefile.standalone Corefile
   # Edit Corefile as needed
   ```

3. **Run the standalone CoreDNS**:
   ```bash
   ./coredns-externaldns -conf Corefile
   ```

### Container Deployment

Create a Dockerfile for containerized deployment (already provided):

```bash
# Build the Docker image
make docker-build

# Or build with custom tag
make docker-build-tag

# Run locally for testing (maps to port 5353 to avoid conflicts)
make docker-run

# Run with custom Corefile
make docker-run-custom
```

Manual Docker commands:
```bash
# Build image
docker build -t coredns-externaldns-standalone .

# Run container
docker run --rm -p 5353:53/udp coredns-externaldns-standalone:latest

# Run with custom Corefile mounted
docker run --rm -p 5353:53/udp \
  -v $(pwd)/my-custom-Corefile:/root/Corefile \
  coredns-externaldns-standalone:latest
```

### Kubernetes Deployment

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: coredns-externaldns
spec:
  replicas: 2
  selector:
    matchLabels:
      app: coredns-externaldns
  template:
    metadata:
      labels:
        app: coredns-externaldns
    spec:
      containers:
      - name: coredns
        image: your-registry/coredns-externaldns:latest
        ports:
        - containerPort: 53
          protocol: UDP
        - containerPort: 53
          protocol: TCP
        volumeMounts:
        - name: config
          mountPath: /root/Corefile
          subPath: Corefile
      volumes:
      - name: config
        configMap:
          name: coredns-config
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: coredns-config
data:
  Corefile: |
    .:53 {
        log
        errors
        health
        prometheus
        externaldns {
            ttl 300
        }
        forward . 8.8.8.8 8.8.4.4
    }
```

### Advantages
- Simple single-binary deployment
- No need to rebuild CoreDNS
- Pre-configured with common plugins
- Container-ready

### Disadvantages
- Larger binary size
- Less flexibility in plugin selection

## Configuration Options

### Plugin Configuration Block

The `externaldns` plugin supports the following options:

```
externaldns {
    namespace <string>     # Kubernetes namespace to watch (optional, default: all)
    ttl <seconds>         # Default TTL for DNS records (optional, default: 300)
    debug                 # Enable debug logging (optional, default: false)
}
```

### RBAC Requirements

The plugin requires the following Kubernetes permissions:

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: coredns-externaldns
  namespace: kube-system
---
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

## Testing Your Integration

1. **Verify plugin loading**:
   ```bash
   # For plugin integration
   ./coredns -plugins | grep externaldns
   
   # For standalone
   ./coredns-externaldns -plugins | grep externaldns
   ```

2. **Test with sample DNSEndpoint**:
   ```bash
   kubectl apply -f examples/dnsendpoint-examples.yaml
   ```

3. **Query DNS records**:
   ```bash
   dig @localhost app.example.com
   nslookup app.example.com localhost
   ```

## Troubleshooting

### Common Issues

1. **Plugin not found**: Ensure the plugin is properly registered in plugin.cfg
2. **Permission errors**: Check RBAC configuration and service account
3. **DNS resolution fails**: Verify DNSEndpoint resources exist and are in the correct namespace
4. **Debug mode**: Enable debug logging to see detailed plugin operation

### Debug Commands

```bash
# Check DNSEndpoint resources
kubectl get dnsendpoints --all-namespaces

# View plugin logs
kubectl logs -n kube-system deployment/coredns-externaldns

# Test DNS resolution
kubectl run -it --rm debug --image=busybox --restart=Never -- nslookup app.example.com
```

## Performance Considerations

- The plugin maintains an in-memory cache for fast DNS lookups
- Watch connections to Kubernetes API are efficient and long-lived
- Consider namespace filtering for large clusters to reduce memory usage
- Monitor metrics exposed on the /metrics endpoint

For more examples and advanced configuration, see the main README.md file.
