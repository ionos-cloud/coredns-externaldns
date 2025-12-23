package watcher

import (
	"context"
	"fmt"
	"time"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
)

// IngressWatcher handles watching Ingress resources
type IngressWatcher struct {
	client    kubernetes.Interface
	namespace string
	ctx       context.Context
	cancel    context.CancelFunc
	handler   IngressHandler
}

// IngressHandler defines the interface for handling Ingress events
type IngressHandler interface {
	OnIngressAdd(ingress *networkingv1.Ingress) error
	OnIngressUpdate(ingress *networkingv1.Ingress) error
	OnIngressDelete(ingress *networkingv1.Ingress) error
}

// NewIngressWatcher creates a new Ingress watcher
func NewIngressWatcher(client kubernetes.Interface, namespace string, handler IngressHandler) *IngressWatcher {
	return &IngressWatcher{
		client:    client,
		namespace: namespace,
		handler:   handler,
	}
}

// Start begins watching for Ingress changes
func (w *IngressWatcher) Start(ctx context.Context) error {
	w.ctx, w.cancel = context.WithCancel(ctx)

	// Initial sync
	if err := w.initialSync(); err != nil {
		return fmt.Errorf("initial ingress sync failed: %w", err)
	}

	// Start watching
	go w.watchLoop()

	log.Info("Ingress watcher started successfully")
	return nil
}

// Stop stops the watcher
func (w *IngressWatcher) Stop() {
	if w.cancel != nil {
		w.cancel()
	}
}

func (w *IngressWatcher) initialSync() error {
	list, err := w.client.NetworkingV1().Ingresses(w.namespace).List(w.ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list Ingresses: %w", err)
	}

	log.Infof("Initial sync: found %d Ingresses", len(list.Items))

	for _, ing := range list.Items {
		ingCopy := ing
		if err := w.handler.OnIngressAdd(&ingCopy); err != nil {
			log.Errorf("Failed to handle initial Ingress %s/%s: %v", ing.Namespace, ing.Name, err)
		}
	}
	return nil
}

func (w *IngressWatcher) watchLoop() {
	for {
		select {
		case <-w.ctx.Done():
			log.Info("Stopping Ingress watcher")
			return
		default:
		}

		if err := w.runWatch(); err != nil {
			log.Errorf("Ingress watch failed: %v, restarting in 5 seconds...", err)
			time.Sleep(5 * time.Second)
		}
	}
}

func (w *IngressWatcher) runWatch() error {
	list, err := w.client.NetworkingV1().Ingresses(w.namespace).List(w.ctx, metav1.ListOptions{Limit: 1})
	if err != nil {
		return fmt.Errorf("failed to get resource version: %w", err)
	}

	watchOpts := metav1.ListOptions{
		Watch:           true,
		ResourceVersion: list.GetResourceVersion(),
	}

	watcher, err := w.client.NetworkingV1().Ingresses(w.namespace).Watch(w.ctx, watchOpts)
	if err != nil {
		return fmt.Errorf("failed to start watch: %w", err)
	}
	defer watcher.Stop()

	log.Debug("Started watching Ingresses")

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

func (w *IngressWatcher) handleEvent(event watch.Event) error {
	ing, ok := event.Object.(*networkingv1.Ingress)
	if !ok {
		return fmt.Errorf("unexpected object type in watch event")
	}

	log.Debugf("Processing Ingress %s/%s (event: %s)",
		ing.Namespace, ing.Name, event.Type)

	switch event.Type {
	case watch.Added:
		return w.handler.OnIngressAdd(ing)
	case watch.Modified:
		return w.handler.OnIngressUpdate(ing)
	case watch.Deleted:
		return w.handler.OnIngressDelete(ing)
	default:
		log.Debugf("Ignoring event type: %s", event.Type)
		return nil
	}
}
