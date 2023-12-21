package pod

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
	errWlOwnersNotFound = fmt.Errorf("unable to find any workload owners")
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
	client client.Client
}

func (h *podEventHandler) Create(ctx context.Context, e event.CreateEvent, q workqueue.RateLimitingInterface) {
	h.queueReconcileForPod(ctx, e.Object, q)
}

func (h *podEventHandler) Update(ctx context.Context, e event.UpdateEvent, q workqueue.RateLimitingInterface) {
	h.queueReconcileForPod(ctx, e.ObjectNew, q)
}

func (h *podEventHandler) Delete(ctx context.Context, e event.DeleteEvent, q workqueue.RateLimitingInterface) {
	h.queueReconcileForPod(ctx, e.Object, q)
}

func (h *podEventHandler) Generic(ctx context.Context, e event.GenericEvent, q workqueue.RateLimitingInterface) {
	h.queueReconcileForPod(ctx, e.Object, q)
}

func (h *podEventHandler) queueReconcileForPod(ctx context.Context, object client.Object, q workqueue.RateLimitingInterface) {
	p, ok := object.(*corev1.Pod)
	if !ok {
		return
	}

	log := ctrl.LoggerFrom(ctx).WithValues("pod", klog.KObj(p))
	ctx = ctrl.LoggerInto(ctx, log)
	log.V(5).Info("Queueing reconcile for pod")

	q.Add(reconcileRequestForPod(p))
}

type parentWorkloadHandler struct {
	client client.Client
}

func (h *parentWorkloadHandler) Create(ctx context.Context, e event.CreateEvent, q workqueue.RateLimitingInterface) {
	h.queueReconcileForChildPod(ctx, e.Object, q)
}

func (h *parentWorkloadHandler) Update(ctx context.Context, e event.UpdateEvent, q workqueue.RateLimitingInterface) {
	h.queueReconcileForChildPod(ctx, e.ObjectNew, q)
}

func (h *parentWorkloadHandler) Delete(context.Context, event.DeleteEvent, workqueue.RateLimitingInterface) {
}

func (h *parentWorkloadHandler) Generic(ctx context.Context, e event.GenericEvent, q workqueue.RateLimitingInterface) {
}

func (h *parentWorkloadHandler) queueReconcileForChildPod(ctx context.Context, object client.Object, q workqueue.RateLimitingInterface) {
	w, ok := object.(*kueue.Workload)
	if !ok {
		return
	}
	log := ctrl.LoggerFrom(ctx).WithValues("workload", klog.KObj(w))
	ctx = ctrl.LoggerInto(ctx, log)

	if len(w.ObjectMeta.OwnerReferences) == 0 {
		return
	}
	log.V(5).Info("Queueing reconcile for parent pods")

	for _, ownerRef := range w.ObjectMeta.OwnerReferences {
		var parentPod corev1.Pod

		err := h.client.Get(ctx, types.NamespacedName{Name: ownerRef.Name, Namespace: w.ObjectMeta.Namespace}, &parentPod)

		if err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			log.Error(err, "Unable to get parent pod")
			return
		}

		if groupName := fromObject(&parentPod).groupName(); groupName == "" {
			log.V(5).Info("Queueing reconcile for the single pod", "pod", klog.KObj(&parentPod))
			q.Add(reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      parentPod.Name,
					Namespace: w.Namespace,
				},
			})
			return
		} else {
			log.V(5).Info("Queueing reconcile for the pod group", "groupName", groupName, "namespace", w.Namespace)
			q.Add(reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      groupName,
					Namespace: fmt.Sprintf("group/%s", w.Namespace),
				},
			})
			return
		}

	}

	log.Error(errWlOwnersNotFound, "Unable to queue reconcile for workload parent pods")
}
