package core

import (
	"context"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
)

type DefaultLocalQueueUpdateWatcher interface{}

type DefaultLocalQueueReconciler struct {
	client            client.Client
	log               logr.Logger
	qManager          *qcache.Manager
	cache             *schdcache.Cache
	recorder          record.EventRecorder
	namespaceSelector labels.Selector
}

var _ reconcile.Reconciler = (*DefaultLocalQueueReconciler)(nil)

type DefaultLocalQueueReconcilerOptions struct {
	NamespaceSelector labels.Selector
}

type DefaultLocalQueueReconcilerOption func(*DefaultLocalQueueReconcilerOptions)

func WithNamespaceSelector(s labels.Selector) DefaultLocalQueueReconcilerOption {
	return func(o *DefaultLocalQueueReconcilerOptions) {
		o.NamespaceSelector = s
	}
}

var defaultDLQOptions = DefaultLocalQueueReconcilerOptions{}

func NewDefaultLocalQueueReconciler(
	client client.Client,
	qMgr *qcache.Manager,
	cache *schdcache.Cache,
	recorder record.EventRecorder,
	opts ...DefaultLocalQueueReconcilerOption,
) *DefaultLocalQueueReconciler {
	options := defaultDLQOptions
	for _, opt := range opts {
		opt(&options)
	}
	return &DefaultLocalQueueReconciler{
		client:            client,
		log:               ctrl.Log.WithName("default-local-queue-reconciler"),
		qManager:          qMgr,
		cache:             cache,
		recorder:          recorder,
		namespaceSelector: options.NamespaceSelector,
	}
}

func (r *DefaultLocalQueueReconciler) SetupWithManager(mgr ctrl.Manager, cfg *config.Configuration) error {
	return builder.TypedControllerManagedBy[reconcile.Request](mgr).
		For(&kueue.ClusterQueue{}).
		Watches(
			&corev1.Namespace{},
			handler.EnqueueRequestsFromMapFunc(r.mapNamespaceToClusterQueues),
		).
		Complete(WithLeadingManager(mgr, r, &kueue.ClusterQueue{}, cfg))
}

// mapNamespaceToClusterQueues is an event handler that maps an event on a Namespace object
// to a reconciliation request for any ClusterQueue that might select it. This ensures that
// when a namespace's labels change, the controllers for relevant ClusterQueues
// are triggered to re-evaluate their state.
func (r *DefaultLocalQueueReconciler) mapNamespaceToClusterQueues(ctx context.Context, ns client.Object) []reconcile.Request {
	if r.namespaceSelector != nil {
		if !r.namespaceSelector.Matches(labels.Set(ns.GetLabels())) {
			return nil
		}
	}

	log := r.log.WithValues("namespace", klog.KObj(ns))

	var cqs kueue.ClusterQueueList
	if err := r.client.List(ctx, &cqs); err != nil {
		log.Error(err, "Failed to list ClusterQueues for namespace mapping")
		return nil
	}
	requests := make([]reconcile.Request, 0, len(cqs.Items))
	for i := range cqs.Items {
		cq := &cqs.Items[i]
		if cq.Spec.DefaultLocalQueue == nil {
			continue
		}

		selector, err := metav1.LabelSelectorAsSelector(cq.Spec.NamespaceSelector)
		if err != nil {
			log.Error(err, "Failed to parse namespaceSelector for ClusterQueue", "clusterQueue", klog.KObj(cq))
			continue
		}

		if selector.Matches(labels.Set(ns.GetLabels())) {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: cq.Name},
			})
		}
	}
	return requests
}

// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;watch;update;patch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=clusterqueues,verbs=get;list;watch

func (r *DefaultLocalQueueReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	var cq kueue.ClusterQueue
	if err := r.client.Get(ctx, req.NamespacedName, &cq); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if cq.Spec.DefaultLocalQueue == nil || !cq.DeletionTimestamp.IsZero() {
		// Feature not enabled or CQ is being deleted, nothing to do.
		return ctrl.Result{}, nil
	}

	selector, err := metav1.LabelSelectorAsSelector(cq.Spec.NamespaceSelector)
	if err != nil {
		log.Error(err, "Failed to parse namespaceSelector, skipping reconciliation", "clusterQueue", klog.KObj(&cq))
		return ctrl.Result{}, nil
	}

	var matchingNamespaces corev1.NamespaceList
	if err := r.client.List(ctx, &matchingNamespaces, client.MatchingLabelsSelector{Selector: selector}); err != nil {
		log.Error(err, "Failed to list matching namespaces")
		return ctrl.Result{}, err
	}
	for i := range matchingNamespaces.Items {
		ns := &matchingNamespaces.Items[i]
		if r.namespaceSelector != nil {
			if !r.namespaceSelector.Matches(labels.Set(ns.Labels)) {
				continue
			}
		}
		err := r.ensureLocalQueueExists(ctx, &cq, ns)
		if err != nil {
			log.Error(err, "Failed to ensure LocalQueue exists in namespace", "namespace", klog.KObj(ns))
		}
	}

	return ctrl.Result{}, nil
}

func (r *DefaultLocalQueueReconciler) ensureLocalQueueExists(ctx context.Context, cq *kueue.ClusterQueue, ns *corev1.Namespace) error {
	log := ctrl.LoggerFrom(ctx)

	targetLQ := types.NamespacedName{
		Namespace: ns.Name,
		Name:      cq.Spec.DefaultLocalQueue.Name,
	}

	var lq kueue.LocalQueue
	if err := r.client.Get(ctx, targetLQ, &lq); err == nil {
		if lq.Annotations["kueue.x-k8s.io/created-by-clusterqueue"] == cq.Name {
			return nil
		}
		r.recorder.Eventf(cq, corev1.EventTypeWarning, "DefaultLocalQueueExists", "Skipping LocalQueue creation in namespace %s, a LocalQueue with name %s already exists", ns.Name, targetLQ.Name)
		return nil
	} else if !apierrors.IsNotFound(err) {
		return err
	}

	log.Info("Creating default LocalQueue", "namespace", ns.Name, "localQueue", targetLQ.Name)

	newLQ := &kueue.LocalQueue{
		ObjectMeta: metav1.ObjectMeta{
			Name:      targetLQ.Name,
			Namespace: targetLQ.Namespace,
			Annotations: map[string]string{
				"kueue.x-k8s.io/created-by-clusterqueue": cq.Name,
			},
			Labels: map[string]string{
				"kueue.x-k8s.io/auto-generated": "true",
			},
		},
		Spec: kueue.LocalQueueSpec{
			ClusterQueue: kueue.ClusterQueueReference(cq.Name),
		},
	}

	if err := r.client.Create(ctx, newLQ); err != nil {
		r.recorder.Eventf(cq, corev1.EventTypeWarning, "LocalQueueCreationFailed", "Failed to create LocalQueue %s in namespace %s: %v", newLQ.Name, newLQ.Namespace, err)
		return err
	}
	r.recorder.Eventf(cq, corev1.EventTypeNormal, "LocalQueueCreated", "Created LocalQueue %s in namespace %s", newLQ.Name, newLQ.Namespace)
	return nil
}
