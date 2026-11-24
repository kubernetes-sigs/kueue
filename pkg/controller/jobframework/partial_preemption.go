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

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
	"sigs.k8s.io/kueue/pkg/util/parallelize"
	"sigs.k8s.io/kueue/pkg/workload"
)

// reconcilePartialPreemption evicts the Pods above the retained count of an admitted elastic job
// whose Workload carries a pending partial-preemption request, so each PodSet drains to its
// retained count. The integration chooses which Pods leave (see JobWithPartialPreemption);
// without the hook nothing is evicted.
// Eviction goes through the Eviction API so PodDisruptionBudgets are honored.
func (r *JobReconciler) reconcilePartialPreemption(ctx context.Context, job GenericJob, wl *kueue.Workload) error {
	if wl == nil || wl.Status.Admission == nil ||
		!features.Enabled(features.PartialPreemption) ||
		!workload.HasPendingPreemptionRetainedCount(wl) {
		return nil
	}
	targets := make(map[kueue.PodSetReference]int32)
	for i := range wl.Status.Admission.PodSetAssignments {
		psa := &wl.Status.Admission.PodSetAssignments[i]
		if psa.PartialPreemption != nil && psa.PartialPreemption.RetainedCount != nil {
			targets[psa.Name] = *psa.PartialPreemption.RetainedCount
		}
	}
	if len(targets) == 0 {
		return nil
	}
	pods, err := r.reclaimCandidates(ctx, job, wl, targets)
	if err != nil || len(pods) == 0 {
		return err
	}
	return parallelize.Until(ctx, len(pods), func(i int) error {
		return evictPodForReclaim(ctx, r.client, ctrl.LoggerFrom(ctx), pods[i])
	})
}

// reclaimCandidates returns the Pods the integration chooses to evict. Only integrations that
// implement JobWithPartialPreemption reclaim Pods; others return none.
func (r *JobReconciler) reclaimCandidates(ctx context.Context, job GenericJob, wl *kueue.Workload, targets map[kueue.PodSetReference]int32) ([]*corev1.Pod, error) {
	ppJob, ok := job.(JobWithPartialPreemption)
	if !ok {
		return nil, nil
	}
	return ppJob.PodsToReclaim(ctx, r.client, wl, targets)
}

// PropagatePartialPreemptionAnnotation copies the partial-preemption annotations, the opt-in
// and the max reclaimed count, from obj to wl in memory, without persisting the change.
func PropagatePartialPreemptionAnnotation(obj client.Object, wl *kueue.Workload) bool {
	annotations := obj.GetAnnotations()
	optedIn := annotations[constants.PartialPreemptionAnnotation] == "true"
	maxReclaimed, hasMaxReclaimed := annotations[constants.PartialPreemptionMaxReclaimedCountAnnotation]
	// The two calls must both run: a plain || would skip the second one.
	optInChanged := syncAnnotation(wl, constants.PartialPreemptionAnnotation, "true", optedIn)
	maxReclaimedChanged := syncAnnotation(wl, constants.PartialPreemptionMaxReclaimedCountAnnotation, maxReclaimed, hasMaxReclaimed)
	return optInChanged || maxReclaimedChanged
}

// syncAnnotation makes the annotation of wl hold value if present, and absent otherwise.
// It returns true if it changed wl.
func syncAnnotation(wl *kueue.Workload, key, value string, present bool) bool {
	current, has := wl.Annotations[key]
	if present == has && (!present || current == value) {
		return false
	}
	if present {
		metav1.SetMetaDataAnnotation(&wl.ObjectMeta, key, value)
	} else {
		delete(wl.Annotations, key)
	}
	return true
}

// UpdatePartialPreemptionAnnotation patches each workload's partial-preemption opt-in
// annotation to match obj, skipping the request when the value is already up to date.
func UpdatePartialPreemptionAnnotation(ctx context.Context, c client.Client, obj client.Object, wls ...*kueue.Workload) error {
	if !features.Enabled(features.PartialPreemption) {
		return nil
	}
	for _, wl := range wls {
		if wl == nil {
			continue
		}
		if err := clientutil.Patch(ctx, c, wl, func() (bool, error) {
			return PropagatePartialPreemptionAnnotation(obj, wl), nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// evictPodForReclaim evicts one Pod through the Eviction API.
func evictPodForReclaim(ctx context.Context, c client.Client, log logr.Logger, pod *corev1.Pod) error {
	eviction := &policyv1.Eviction{
		Name:      pod.Name,
		Namespace: pod.Namespace,
	}
	if err := c.SubResource("eviction").Create(ctx, pod, eviction); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		if apierrors.IsTooManyRequests(err) {
			log.V(3).Info("partial-preemption eviction blocked by PodDisruptionBudget, will retry", "pod", klog.KObj(pod))
			return err
		}
		return fmt.Errorf("evicting pod %s: %w", klog.KObj(pod), err)
	}
	log.V(3).Info("evicted elastic pod above partial-preemption target", "pod", klog.KObj(pod))
	return nil
}
