package pod

import (
	"context"
	"errors"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
)

var (
	errFailedRefAPIVersionParse = errors.New("could not parse single pod OwnerReference APIVersion")
)

func reconcileRequestForPod(p *corev1.Pod) reconcile.Request {
	groupName := p.GetLabels()[GroupNameLabel]

	if groupName == "" {
		return reconcile.Request{
			NamespacedName: types.NamespacedName{
				Namespace: p.Namespace,
				Name:      p.Name,
			},
		}
	}
	return reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      groupName,
			Namespace: fmt.Sprintf("group/%s", p.Namespace),
		},
	}
}

// podEventHandler will convert reconcile requests for pods in group from "<namespace>/<pod-name>" to
// "group/<namespace>/<group-name>".
type podEventHandler struct {
	cleanedUpPodsExpectations *expectationsStore
}

func (h *podEventHandler) Create(ctx context.Context, e event.CreateEvent, q workqueue.RateLimitingInterface) {
	h.queueReconcileForPod(ctx, e.Object, q)
}

func (h *podEventHandler) Update(ctx context.Context, e event.UpdateEvent, q workqueue.RateLimitingInterface) {
	h.queueReconcileForPod(ctx, e.ObjectNew, q)
}

func (h *podEventHandler) Delete(ctx context.Context, e event.DeleteEvent, q workqueue.RateLimitingInterface) {
	p, ok := e.Object.(*corev1.Pod)
	if !ok {
		return
	}

	log := ctrl.LoggerFrom(ctx).WithValues("pod", klog.KObj(p))

	if g, isGroup := p.Labels[GroupNameLabel]; isGroup {
		// If the watch was temporarily unavailable, it is possible that the object reported in the event still
		// has a finalizer, but we can consider this Pod cleaned up, as it is being deleted.
		h.cleanedUpPodsExpectations.ObservedUID(log, types.NamespacedName{Namespace: p.Namespace, Name: g}, p.UID)
	}

	log.V(5).Info("Queueing reconcile for pod")

	q.Add(reconcileRequestForPod(p))
}

func (h *podEventHandler) Generic(ctx context.Context, e event.GenericEvent, q workqueue.RateLimitingInterface) {
}

func (h *podEventHandler) queueReconcileForPod(ctx context.Context, object client.Object, q workqueue.RateLimitingInterface) {
	p, ok := object.(*corev1.Pod)
	if !ok {
		return
	}

	log := ctrl.LoggerFrom(ctx).WithValues("pod", klog.KObj(p))

	if g, isGroup := p.Labels[GroupNameLabel]; isGroup {
		if !slices.Contains(p.Finalizers, PodFinalizer) {
			h.cleanedUpPodsExpectations.ObservedUID(log, types.NamespacedName{Namespace: p.Namespace, Name: g}, p.UID)
		}
	}

	log.V(5).Info("Queueing reconcile for pod")

	q.Add(reconcileRequestForPod(p))
}

type workloadHandler struct{}

func (h *workloadHandler) Create(ctx context.Context, e event.CreateEvent, q workqueue.RateLimitingInterface) {
	h.queueReconcileForChildPod(ctx, e.Object, q)
}

func (h *workloadHandler) Update(ctx context.Context, e event.UpdateEvent, q workqueue.RateLimitingInterface) {
	h.queueReconcileForChildPod(ctx, e.ObjectNew, q)
}

func (h *workloadHandler) Delete(context.Context, event.DeleteEvent, workqueue.RateLimitingInterface) {
}

func (h *workloadHandler) Generic(ctx context.Context, e event.GenericEvent, q workqueue.RateLimitingInterface) {
}

func (h *workloadHandler) queueReconcileForChildPod(ctx context.Context, object client.Object, q workqueue.RateLimitingInterface) {
	w, ok := object.(*kueue.Workload)
	if !ok {
		return
	}
	log := ctrl.LoggerFrom(ctx).WithValues("workload", klog.KObj(w))

	if len(w.ObjectMeta.OwnerReferences) == 0 {
		return
	}
	log.V(5).Info("Queueing reconcile for parent pods")

	// Compose request for a pod group if workload has an "is-group-workload" annotation
	if w.Annotations[IsGroupWorkloadAnnotationKey] == IsGroupWorkloadAnnotationValue {
		log.V(5).Info("Queueing reconcile for the pod group", "groupName", w.Name, "namespace", w.Namespace)
		q.Add(reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      w.Name,
				Namespace: fmt.Sprintf("group/%s", w.Namespace),
			},
		})
		return
	}

	// Get controller reference to a single pod object
	if ref := metav1.GetControllerOf(object); ref != nil {
		log.V(5).Info("Queueing reconcile for the single pod", "ControllerReference", ref)

		// Parse the Group out of the OwnerReference to compare it to what was parsed out of the requested OwnerType
		refGV, err := schema.ParseGroupVersion(ref.APIVersion)
		if err != nil {
			log.Error(errFailedRefAPIVersionParse, "failed to enqueue single pod request", "APIVersion", ref.APIVersion)
			return
		}

		// Check if the OwnerReference is pointing to a Pod object.
		if ref.Kind == "Pod" && refGV.Group == "" {
			// Match found - add a Request for the object referred to in the OwnerReference
			q.Add(reconcile.Request{NamespacedName: types.NamespacedName{
				Name:      ref.Name,
				Namespace: object.GetNamespace(),
			}})
			return
		}
	}
}
