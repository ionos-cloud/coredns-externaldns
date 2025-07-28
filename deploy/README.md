# Deployment Assets

This directory contains all deployment-related assets for the CoreDNS external-dns plugin.

## Structure

```
deploy/
├── k8s-deployment.yaml          # Raw Kubernetes manifests for manual deployment
└── charts/
    └── coredns-externaldns/     # Helm chart for production deployment
        ├── Chart.yaml
        ├── values.yaml
        ├── templates/
        └── tests/
```

## Deployment Options

### 1. Helm Chart (Recommended)

The Helm chart provides the most complete and production-ready deployment option:

```bash
# Install from OCI registry
helm install my-coredns oci://ghcr.io/ionos-cloud/coredns-externaldns/charts/coredns-externaldns

# Install from local source
helm install my-coredns ./deploy/charts/coredns-externaldns
```

**Features:**
- Automated CRD creation
- RBAC configuration
- Service account management
- Production-ready defaults
- Configurable via values.yaml

### 2. Raw Kubernetes Manifests

For simple deployments or testing purposes:

```bash
kubectl apply -f deploy/k8s-deployment.yaml
```

**Features:**
- Simple deployment
- Manual RBAC setup
- Basic configuration
- Good for testing and development

## Files

- **`k8s-deployment.yaml`**: Complete Kubernetes deployment manifest including ConfigMap, ServiceAccount, RBAC, Deployment, and Service
- **`charts/coredns-externaldns/`**: Production-ready Helm chart with comprehensive configuration options

## Makefile Integration

You can also use the provided Makefile targets:

```bash
make k8s-apply    # Deploy using raw manifests
make k8s-delete   # Remove deployment
```

For complete documentation on the Helm chart, see [charts/coredns-externaldns/README.md](charts/coredns-externaldns/README.md).
