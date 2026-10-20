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

package jobframework

import (
	"context"
	"fmt"

	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadfinish "sigs.k8s.io/kueue/pkg/workload/finish"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

// syncWorkloadSliceFields keeps priority and maximumExecutionTimeSeconds up to date
// on the Workload slice(s) that EnsureWorkloadSlices decided to keep. Slice
// compatibility only considers the pod set shape, so changes to these fields
// arrive here with the old values still on the slices.
//
// While a scale-up waits for quota the quota-reserved slice is kept alongside its
// pending replacement and only the replacement is returned, but both are
// live and must follow these fields where the Workload API allows it. wl is nil
// when a new slice is to be created.
func (r *JobReconciler) syncWorkloadSliceFields(ctx context.Context, job GenericJob, object client.Object, wl *kueue.Workload) error {
	log := ctrl.LoggerFrom(ctx)

	// Quota-reserved rather than admitted: FindLatestActiveWorkload selects on
	// the reservation, and the distinction is what makes the skip below matter.
	retained, err := workloadslicing.FindLatestActiveWorkload(ctx, r.client, object, job.GVK())
	if err != nil {
		return err
	}
	live := []*kueue.Workload{wl}
	if retained != nil && (wl == nil || retained.Name != wl.Name) {
		log.V(4).Info("Workload slice priority and maximum execution time apply to the replacement and to the quota-reserved slice it is waiting behind",
			"replacement", klog.KObj(wl), "retained", klog.KObj(retained))
		live = append(live, retained)
	}
	// The quota-reserved/no-priorityClassRef legality check lives in
	// UpdateWorkloadPriority so the ordinary Job and LeaderWorkerSet paths, which
	// reach the shared helper without this caller's filtering, get the same guard.
	if err := UpdateWorkloadPriority(ctx, r.client, r.record, job.Object(), getCustomPriorityClassFuncFromJob(job), live...); err != nil {
		return err
	}
	return updateWorkloadSliceMaximumExecutionTime(ctx, r.client, object, live)
}

func updateWorkloadSliceMaximumExecutionTime(ctx context.Context, c client.Client, obj client.Object, wls []*kueue.Workload) error {
	desired := MaximumExecutionTimeSecondsForObject(obj)

	for _, wl := range wls {
		if wl == nil {
			continue
		}

		// WithRetryOnConflict only re-Gets after a 409; refresh first so the
		// admission and finished checks use the current status.
		if err := c.Get(ctx, client.ObjectKeyFromObject(wl), wl); err != nil {
			return fmt.Errorf("getting workload slice %s before maximum execution time sync: %w", workload.Key(wl), err)
		}

		if err := clientutil.Patch(ctx, c, wl, func() (bool, error) {
			if workload.IsAdmitted(wl) || workloadfinish.IsFinished(wl) || ptr.Equal(wl.Spec.MaximumExecutionTimeSeconds, desired) {
				return false, nil
			}
			wl.Spec.MaximumExecutionTimeSeconds = nil
			if desired != nil {
				wl.Spec.MaximumExecutionTimeSeconds = new(*desired)
			}
			return true, nil
		}, clientutil.WithRetryOnConflict()); err != nil {
			return fmt.Errorf("updating maximum execution time of workload slice %s: %w", workload.Key(wl), err)
		}
	}
	return nil
}
