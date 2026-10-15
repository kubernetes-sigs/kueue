/*
Copyright The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package multikueue

import (
	"context"
	"errors"
	"slices"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
	workloadfinish "sigs.k8s.io/kueue/pkg/workload/finish"
	workloadpatching "sigs.k8s.io/kueue/pkg/workload/patching"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

// sliceCleanupAdapter scopes deletion to the remote job's prebuilt workload and
// reports completion only after that job has disappeared.
type sliceCleanupAdapter interface {
	CleanupRemoteSlice(ctx context.Context, remoteClient client.Client, key types.NamespacedName, workloadName, origin string) (bool, error)
}

var errSliceCleanupPending = errors.New("remote slice deletion is still pending")

// Retention and completion are metadata writes guarded by resourceVersion, so
// a stale reconcile cannot acknowledge a newer eviction or lose its finalizer.
func (w *wlReconciler) patchSliceCleanupMetadata(ctx context.Context, wl *kueue.Workload, complete bool) error {
	updated := wl.DeepCopy()
	switch {
	case complete:
		if workloadevict.IsEvicted(wl) {
			if updated.Annotations == nil {
				updated.Annotations = make(map[string]string)
			}
			updated.Annotations[constants.MultiKueueEvictionCleanupAnnotation] = workloadslicing.EvictionCleanupToken(wl)
		}
		controllerutil.RemoveFinalizer(updated, constants.MultiKueueEvictionCleanupFinalizer)
	case workloadfinish.IsFinished(wl) && !workloadevict.IsEvicted(wl):
		controllerutil.RemoveFinalizer(updated, constants.MultiKueueEvictionCleanupFinalizer)
	default:
		controllerutil.AddFinalizer(updated, constants.MultiKueueEvictionCleanupFinalizer)
	}
	if slices.Equal(updated.Finalizers, wl.Finalizers) && updated.Annotations[constants.MultiKueueEvictionCleanupAnnotation] == wl.Annotations[constants.MultiKueueEvictionCleanupAnnotation] {
		return nil
	}
	if err := w.client.Patch(ctx, updated, client.MergeFromWithOptions(wl, client.MergeFromWithOptimisticLock{})); err != nil {
		return err
	}
	wl.ObjectMeta = updated.ObjectMeta
	return nil
}

func (w *wlReconciler) cleanupEvictedSlice(ctx context.Context, group *wlGroup) (reconcile.Result, error) {
	for cluster := range group.remotes {
		if err := group.RemoveRemoteObjects(ctx, cluster); err != nil {
			if errors.Is(err, errSliceCleanupPending) {
				return reconcile.Result{RequeueAfter: syncDeferredRequeueAfter}, nil
			}
			return reconcile.Result{}, err
		}
	}
	if len(group.unavailableClusters) > 0 {
		return reconcile.Result{RequeueAfter: w.workerLostTimeout}, nil
	}
	// A prematurely Finished slice cannot be drained by the job reconciler. Once
	// foreground deletion is complete there are no remote dependents left, so
	// releasing any remaining reservation is safe.
	if workload.HasQuotaReservation(group.local) {
		clk := w.clock
		if clk == nil {
			clk = realClock
		}
		if err := workloadpatching.PatchAdmissionStatus(ctx, w.client, group.local, clk, func(wl *kueue.Workload) (bool, error) {
			return workload.UnsetQuotaReservationWithCondition(wl, kueue.WorkloadQuotaReservedReasonPendingEvaluation, "Remote RayCluster eviction cleanup completed", clk.Now()), nil
		}); err != nil {
			return reconcile.Result{}, err
		}
	}
	if err := w.patchSliceCleanupMetadata(ctx, group.local, true); err != nil {
		return reconcile.Result{}, err
	}
	ctrl.LoggerFrom(ctx).V(1).Info("Evicted slice cleanup completed", "workload", klog.KObj(group.local))
	return reconcile.Result{}, nil
}
