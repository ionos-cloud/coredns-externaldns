package watcher

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	clog "github.com/coredns/coredns/plugin/pkg/log"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	externaldnsv1alpha1 "sigs.k8s.io/external-dns/apis/v1alpha1"
)

var log = clog.NewWithPlugin("externaldns-watcher")

// DNSEndpointWatcher handles watching DNSEndpoint CRDs
type DNSEndpointWatcher struct {
	client    dynamic.Interface
	namespace string
	ctx       context.Context
	cancel    context.CancelFunc
	handler   EventHandler
}

// EventHandler defines the interface for handling DNSEndpoint events
type EventHandler interface {
	OnAdd(endpoint *externaldnsv1alpha1.DNSEndpoint) error
	OnUpdate(endpoint *externaldnsv1alpha1.DNSEndpoint) error
	OnDelete(endpoint *externaldnsv1alpha1.DNSEndpoint) error
}

// New creates a new DNSEndpoint watcher
func New(client dynamic.Interface, namespace string, handler EventHandler) *DNSEndpointWatcher {
	return &DNSEndpointWatcher{
		client:    client,
		namespace: namespace,
		handler:   handler,
	}
}

// Start begins watching for DNSEndpoint changes
func (w *DNSEndpointWatcher) Start(ctx context.Context) error {
	w.ctx, w.cancel = context.WithCancel(ctx)

	// Define the DNSEndpoint GVR
	dnsEndpointGVR := schema.GroupVersionResource{
		Group:    "externaldns.k8s.io",
		Version:  "v1alpha1",
		Resource: "dnsendpoints",
	}

	resourceInterface := w.client.Resource(dnsEndpointGVR).Namespace(w.namespace)

	// Initial sync - load all existing DNSEndpoints
	if err := w.initialSync(resourceInterface); err != nil {
		return fmt.Errorf("initial sync failed: %w", err)
	}

	// Start watching for changes
	go w.watchLoop(resourceInterface)

	log.Info("DNSEndpoint watcher started successfully")
	return nil
}

// Stop stops the watcher
func (w *DNSEndpointWatcher) Stop() {
	if w.cancel != nil {
		w.cancel()
	}
}

// initialSync loads all existing DNSEndpoints
func (w *DNSEndpointWatcher) initialSync(resourceInterface dynamic.ResourceInterface) error {
	list, err := resourceInterface.List(w.ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list DNSEndpoints: %w", err)
	}

	log.Infof("Initial sync: found %d DNSEndpoints", len(list.Items))

	for _, item := range list.Items {
		endpoint, err := w.convertToDNSEndpoint(&item)
		if err != nil {
			log.Errorf("Failed to convert DNSEndpoint: %v", err)
			continue
		}

		if err := w.handler.OnAdd(endpoint); err != nil {
			log.Errorf("Failed to handle initial DNSEndpoint %s/%s: %v",
				endpoint.Namespace, endpoint.Name, err)
		}
	}

	return nil
}

// watchLoop runs the main watch loop with automatic restart on failures
func (w *DNSEndpointWatcher) watchLoop(resourceInterface dynamic.ResourceInterface) {
	for {
		select {
		case <-w.ctx.Done():
			log.Info("Stopping DNSEndpoint watcher")
			return
		default:
		}

		if err := w.runWatch(resourceInterface); err != nil {
			log.Errorf("Watch failed: %v, restarting in 5 seconds...", err)
			time.Sleep(5 * time.Second)
		}
	}
}

// runWatch runs a single watch session
func (w *DNSEndpointWatcher) runWatch(resourceInterface dynamic.ResourceInterface) error {
	// Get current resource version for watch
	list, err := resourceInterface.List(w.ctx, metav1.ListOptions{Limit: 1})
	if err != nil {
		return fmt.Errorf("failed to get resource version: %w", err)
	}

	watchOpts := metav1.ListOptions{
		Watch:           true,
		ResourceVersion: list.GetResourceVersion(),
	}

	watcher, err := resourceInterface.Watch(w.ctx, watchOpts)
	if err != nil {
		return fmt.Errorf("failed to start watch: %w", err)
	}
	defer watcher.Stop()

	log.Debug("Started watching DNSEndpoints")

	for {
		select {
		case <-w.ctx.Done():
			return nil
		case event, ok := <-watcher.ResultChan():
			if !ok {
				log.Debug("Watch channel closed, will restart")
				return nil
			}

			if err := w.handleEvent(event); err != nil {
				log.Errorf("Failed to handle event: %v", err)
			}
		}
	}
}

// handleEvent processes a single watch event
func (w *DNSEndpointWatcher) handleEvent(event watch.Event) error {
	obj, ok := event.Object.(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("unexpected object type in watch event")
	}

	endpoint, err := w.convertToDNSEndpoint(obj)
	if err != nil {
		return fmt.Errorf("failed to convert object to DNSEndpoint: %w", err)
	}

	log.Debugf("Processing DNSEndpoint %s/%s (event: %s)",
		endpoint.Namespace, endpoint.Name, event.Type)

	switch event.Type {
	case watch.Added:
		return w.handler.OnAdd(endpoint)
	case watch.Modified:
		return w.handler.OnUpdate(endpoint)
	case watch.Deleted:
		return w.handler.OnDelete(endpoint)
	default:
		log.Debugf("Ignoring event type: %s", event.Type)
		return nil
	}
}

// convertToDNSEndpoint converts unstructured object to DNSEndpoint
func (w *DNSEndpointWatcher) convertToDNSEndpoint(obj *unstructured.Unstructured) (*externaldnsv1alpha1.DNSEndpoint, error) {
	b, err := obj.MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal object: %w", err)
	}

	var endpoint externaldnsv1alpha1.DNSEndpoint
	if err := json.Unmarshal(b, &endpoint); err != nil {
		return nil, fmt.Errorf("failed to unmarshal DNSEndpoint: %w", err)
	}

	return &endpoint, nil
}
