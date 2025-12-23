package watcher

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
)

// ServiceWatcher handles watching Service resources
type ServiceWatcher struct {
	client    kubernetes.Interface
	namespace string
	ctx       context.Context
	cancel    context.CancelFunc
	handler   ServiceHandler
}

// ServiceHandler defines the interface for handling Service events
type ServiceHandler interface {
	OnServiceAdd(service *corev1.Service) error
	OnServiceUpdate(service *corev1.Service) error
	OnServiceDelete(service *corev1.Service) error
}

// NewServiceWatcher creates a new Service watcher
func NewServiceWatcher(client kubernetes.Interface, namespace string, handler ServiceHandler) *ServiceWatcher {
	return &ServiceWatcher{
		client:    client,
		namespace: namespace,
		handler:   handler,
	}
}

// Start begins watching for Service changes
func (w *ServiceWatcher) Start(ctx context.Context) error {
	w.ctx, w.cancel = context.WithCancel(ctx)

	// Initial sync
	if err := w.initialSync(); err != nil {
		return fmt.Errorf("initial service sync failed: %w", err)
	}

	// Start watching
	go w.watchLoop()

	log.Info("Service watcher started successfully")
	return nil
}

// Stop stops the watcher
func (w *ServiceWatcher) Stop() {
	if w.cancel != nil {
		w.cancel()
	}
}

func (w *ServiceWatcher) initialSync() error {
	list, err := w.client.CoreV1().Services(w.namespace).List(w.ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list Services: %w", err)
	}

	log.Infof("Initial sync: found %d Services", len(list.Items))

	for _, svc := range list.Items {
		// We need to pass a pointer to the item, but range variable is reused.
		// So we copy it.
		svcCopy := svc
		if err := w.handler.OnServiceAdd(&svcCopy); err != nil {
			log.Errorf("Failed to handle initial Service %s/%s: %v", svc.Namespace, svc.Name, err)
		}
	}
	return nil
}

func (w *ServiceWatcher) watchLoop() {
	for {
		select {
		case <-w.ctx.Done():
			log.Info("Stopping Service watcher")
			return
		default:
		}

		if err := w.runWatch(); err != nil {
			log.Errorf("Service watch failed: %v, restarting in 5 seconds...", err)
			time.Sleep(5 * time.Second)
		}
	}
}

func (w *ServiceWatcher) runWatch() error {
	// Get current resource version for watch
	list, err := w.client.CoreV1().Services(w.namespace).List(w.ctx, metav1.ListOptions{Limit: 1})
	if err != nil {
		return fmt.Errorf("failed to get resource version: %w", err)
	}

	watchOpts := metav1.ListOptions{
		Watch:           true,
		ResourceVersion: list.GetResourceVersion(),
	}

	watcher, err := w.client.CoreV1().Services(w.namespace).Watch(w.ctx, watchOpts)
	if err != nil {
		return fmt.Errorf("failed to start watch: %w", err)
	}
	defer watcher.Stop()

	log.Debug("Started watching Services")

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

func (w *ServiceWatcher) handleEvent(event watch.Event) error {
	svc, ok := event.Object.(*corev1.Service)
	if !ok {
		return fmt.Errorf("unexpected object type in watch event")
	}

	log.Debugf("Processing Service %s/%s (event: %s)",
		svc.Namespace, svc.Name, event.Type)

	switch event.Type {
	case watch.Added:
		return w.handler.OnServiceAdd(svc)
	case watch.Modified:
		return w.handler.OnServiceUpdate(svc)
	case watch.Deleted:
		return w.handler.OnServiceDelete(svc)
	default:
		log.Debugf("Ignoring event type: %s", event.Type)
		return nil
	}
}
