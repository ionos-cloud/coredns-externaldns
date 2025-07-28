# CoreDNS External-DNS Plugin Helm Chart

This Helm chart deploys CoreDNS with the external-dns plugin integration, allowing CoreDNS to serve DNS records from Kubernetes DNSEndpoint custom resources.

## Prerequisites

- Kubernetes 1.19+
- Helm 3.8+ (for OCI support)

## Installing the Chart

To install the chart with the release name `my-coredns`:

```bash
helm install my-coredns oci://ghcr.io/ionos-cloud/coredns-externaldns/charts/coredns-externaldns
```

To install a specific version:

```bash
helm install my-coredns oci://ghcr.io/ionos-cloud/coredns-externaldns/charts/coredns-externaldns --version 0.1.0
```

## Repository Information

The Helm chart is published as an OCI artifact to GitHub Container Registry:

- **Registry**: `ghcr.io/ionos-cloud/coredns-externaldns/charts`
- **Chart Name**: `coredns-externaldns`
- **Usage**: `helm install my-coredns oci://ghcr.io/ionos-cloud/coredns-externaldns/charts/coredns-externaldns`

To list available versions:

```bash
# Show chart information (requires helm 3.8+)
helm show chart oci://ghcr.io/ionos-cloud/coredns-externaldns/charts/coredns-externaldns
```

## Uninstalling the Chart

To uninstall/delete the `my-coredns` deployment:

```bash
helm delete my-coredns
```

## Configuration

The following table lists the configurable parameters specific to this chart and their default values.

| Parameter | Description | Default |
|-----------|-------------|---------|
| `dnsendpoint.createCRD` | Create DNSEndpoint CRD | `true` |
| `rbac.create` | Create RBAC resources | `true` |
| `rbac.serviceAccountName` | Service account name | `coredns-externaldns` |

For CoreDNS-specific configuration options, refer to the [CoreDNS Helm chart documentation](https://github.com/coredns/helm).

## RBAC Configuration

This chart creates the following resources:

- **CustomResourceDefinition**: Creates the DNSEndpoint CRD if `dnsendpoint.createCRD` is true
- **ClusterRole**: Grants permissions to read DNSEndpoint CRDs, ConfigMaps, Services, Endpoints, Pods, and Nodes
- **ClusterRoleBinding**: Binds the ClusterRole to the ServiceAccount
- **ServiceAccount**: Used by the CoreDNS deployment

## DNSEndpoint CRD

The external-dns plugin watches for DNSEndpoint custom resources. Here's an example:

```yaml
apiVersion: externaldns.k8s.io/v1alpha1
kind: DNSEndpoint
metadata:
  name: example-endpoint
  namespace: default
spec:
  endpoints:
  - dnsName: "example.local"
    recordTTL: 300
    recordType: A
    targets:
    - "192.168.1.100"
```

## Usage Examples

### Basic Installation

```bash
helm install coredns-externaldns oci://ghcr.io/ionos-cloud/coredns-externaldns/charts/coredns-externaldns
```

### Custom Values

```bash
helm install coredns-externaldns oci://ghcr.io/ionos-cloud/coredns-externaldns/charts/coredns-externaldns -f custom-values.yaml
```

Example custom-values.yaml:

```yaml
# Disable CRD creation if it already exists in the cluster
dnsendpoint:
  createCRD: false

# Override CoreDNS image to use custom build
coredns:
  image:
    repository: ghcr.io/ionos-cloud/coredns-externaldns
    tag: "v0.1.0"
```

### Testing DNS Resolution

After installation, you can test DNS resolution:

```bash
# Create a test DNSEndpoint
kubectl apply -f - <<EOF
apiVersion: externaldns.k8s.io/v1alpha1
kind: DNSEndpoint
metadata:
  name: test-endpoint
spec:
  endpoints:
  - dnsName: "test.local"
    recordTTL: 300
    recordType: A
    targets:
    - "127.0.0.1"
EOF

# Test DNS resolution (replace <coredns-service-ip> with actual service IP)
kubectl run -it --rm debug --image=busybox --restart=Never -- nslookup test.local <coredns-service-ip>
```

## Development

### Local Development

To work on this chart locally:

```bash
# Clone the repository
git clone https://github.com/ionos-cloud/coredns-externaldns
cd coredns-externaldns

# Lint the chart
helm lint deploy/charts/coredns-externaldns

# Test template rendering
helm template test-release deploy/charts/coredns-externaldns

# Install from local source
helm install my-test deploy/charts/coredns-externaldns --dry-run --debug
```

### CI/CD Pipeline

This chart is automatically built and published via GitHub Actions using native Helm tooling:

- **On pull requests**: Chart is linted and tested
- **On main branch**: Chart is packaged with semantic versioning and pushed to OCI registry  
- **On releases**: Chart is packaged with release version and pushed to OCI registry

The CI pipeline includes:
1. **Helm lint validation** using native `helm lint`
2. **Chart packaging** with `helm package` and semantic versioning
3. **OCI artifact publishing** to GitHub Container Registry using `helm push`
4. **Automatic version tagging** based on Git refs and build numbers

### Contributing

1. Make changes to the chart in `deploy/charts/coredns-externaldns/`
2. Update version in `Chart.yaml` if needed
3. Test locally with `helm lint` and `helm template`
4. Submit a pull request
5. Chart will be automatically published on merge to main
