# Variables
BINARY_NAME=externaldns-plugin
STANDALONE_BINARY=coredns-externaldns
DOCKER_IMAGE=coredns-externaldns
DOCKER_STANDALONE_IMAGE=coredns-externaldns-standalone
VERSION?=latest
REGISTRY?=localhost:5000

# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get
GOMOD=$(GOCMD) mod

.PHONY: all build build-plugin build-standalone clean test deps docker-build docker-build-tag docker-push docker-run docker-run-custom help

# Default target
all: deps test build

# Build both plugin and standalone
build: build-plugin build-standalone

# Build the plugin (for use with existing CoreDNS)
build-plugin:
	CGO_ENABLED=0 GOOS=linux $(GOBUILD) -a -installsuffix cgo -o $(BINARY_NAME) .

# Build standalone CoreDNS with plugin included
build-standalone:
	CGO_ENABLED=0 GOOS=linux $(GOBUILD) -a -installsuffix cgo -o $(STANDALONE_BINARY) ./cmd/coredns-externaldns

# Clean build artifacts
clean:
	$(GOCLEAN)
	rm -f $(BINARY_NAME) $(STANDALONE_BINARY)

# Run tests
test:
	$(GOTEST) -v ./...

# Download dependencies
deps:
	$(GOMOD) download
	$(GOMOD) tidy

# Verify dependencies
verify:
	$(GOMOD) verify

# Run linter
lint:
	golangci-lint run

# Build Docker image (standalone CoreDNS)
docker-build:
	docker build -t $(DOCKER_STANDALONE_IMAGE):$(VERSION) .

# Build Docker image with custom tag
docker-build-tag:
	docker build -t $(DOCKER_STANDALONE_IMAGE):$(VERSION) -t $(DOCKER_STANDALONE_IMAGE):latest .

# Push Docker image
docker-push: docker-build
	docker tag $(DOCKER_STANDALONE_IMAGE):$(VERSION) $(REGISTRY)/$(DOCKER_STANDALONE_IMAGE):$(VERSION)
	docker push $(REGISTRY)/$(DOCKER_STANDALONE_IMAGE):$(VERSION)

# Run Docker container locally for testing
docker-run: docker-build
	docker run --rm -p 5353:53/udp $(DOCKER_STANDALONE_IMAGE):$(VERSION)

# Run Docker container with custom Corefile
docker-run-custom:
	@echo "Mount your custom Corefile to /root/Corefile in the container"
	docker run --rm -p 5353:53/udp -v $(PWD)/Corefile:/root/Corefile $(DOCKER_STANDALONE_IMAGE):$(VERSION)

# Install development dependencies
dev-deps:
	$(GOGET) -u github.com/golangci/golangci-lint/cmd/golangci-lint

# Generate mocks (if needed for testing)
generate:
	$(GOCMD) generate ./...

# Run the plugin locally (requires kubeconfig)
run-local:
	./$(BINARY_NAME) -conf Corefile

# Apply Kubernetes manifests
k8s-apply:
	kubectl apply -f deploy/k8s-deployment.yaml

# Apply example DNSEndpoints
examples-apply:
	kubectl apply -f examples/dnsendpoint-examples.yaml

# Remove Kubernetes manifests
k8s-delete:
	kubectl delete -f deploy/k8s-deployment.yaml

# Remove example DNSEndpoints
examples-delete:
	kubectl delete -f examples/dnsendpoint-examples.yaml

# Check plugin status in cluster
k8s-status:
	kubectl get pods -n kube-system -l k8s-app=coredns-externaldns
	kubectl logs -n kube-system -l k8s-app=coredns-externaldns --tail=50

# Watch DNSEndpoints
watch-endpoints:
	kubectl get dnsendpoints --all-namespaces -w

# Test DNS resolution (requires cluster DNS to be configured)
test-dns:
	@echo "Testing DNS resolution..."
	@echo "A record: app.example.com"
	@nslookup app.example.com || true
	@echo "CNAME record: www.example.com" 
	@nslookup www.example.com || true
	@echo "MX record: example.com"
	@nslookup -type=MX example.com || true

# Help target
help:
	@echo "====================================================================="
	@echo "  CoreDNS ExternalDNS Plugin - Available Make Targets"
	@echo "====================================================================="
	@echo ""
	@echo "Build Targets:"
	@echo "  build              Build both plugin and standalone binaries"
	@echo "  build-plugin       Build the plugin binary only (for existing CoreDNS)"
	@echo "  build-standalone   Build standalone CoreDNS with plugin included"
	@echo "  clean              Clean build artifacts"
	@echo ""
	@echo "Development Targets:"
	@echo "  test               Run tests"
	@echo "  deps               Download dependencies"
	@echo "  verify             Verify dependencies"
	@echo "  lint               Run linter"
	@echo "  dev-deps           Install development dependencies"
	@echo "  generate           Generate code (mocks, etc.)"
	@echo "  run-local          Run plugin locally"
	@echo ""
	@echo "Docker Targets:"
	@echo "  docker-build       Build Docker image (standalone CoreDNS)"
	@echo "  docker-build-tag   Build Docker image with latest tag"
	@echo "  docker-push        Push Docker image to registry"
	@echo "  docker-run         Run Docker container locally (port 5353)"
	@echo "  docker-run-custom  Run with custom Corefile mounted"
	@echo ""
	@echo "Kubernetes Targets:"
	@echo "  k8s-apply          Apply Kubernetes manifests"
	@echo "  k8s-delete         Delete Kubernetes manifests"
	@echo "  k8s-status         Check plugin status in cluster"
	@echo "  examples-apply     Apply example DNSEndpoints"
	@echo "  examples-delete    Delete example DNSEndpoints"
	@echo "  watch-endpoints    Watch DNSEndpoint changes"
	@echo ""
	@echo "Testing Targets:"
	@echo "  test-dns           Test DNS resolution"
	@echo ""
	@echo "Other:"
	@echo "  help               Show this help"
	@echo ""
	@echo "====================================================================="
