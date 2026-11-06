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
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/parallelize"
	"sigs.k8s.io/kueue/pkg/workload"
)

// reconcilePartialPreemption evicts the over-target Pods of an admitted elastic job whose Workload
// carries a partial-preemption reclaim target, so each PodSet drains to its target. The integration
// chooses which Pods leave (see JobWithPartialPreemption); without the hook nothing is evicted.
// Eviction goes through the Eviction API so PodDisruptionBudgets are honored.
func (r *JobReconciler) reconcilePartialPreemption(ctx context.Context, job GenericJob, wl *kueue.Workload) error {
	if wl == nil || wl.Status.Admission == nil ||
		!features.Enabled(features.PartialPreemption) ||
		!workload.HasPendingPreemptionTargetCount(wl) {
		return nil
	}
	targets := make(map[kueue.PodSetReference]int32)
	for i := range wl.Status.Admission.PodSetAssignments {
		psa := &wl.Status.Admission.PodSetAssignments[i]
		if psa.PreemptionTargetCount != nil {
			targets[psa.Name] = *psa.PreemptionTargetCount
		}
	}
	if len(targets) == 0 {
		return nil
	}
	pods, err := r.reclaimCandidates(ctx, job, wl, targets)
	if err != nil || len(pods) == 0 {
		return err
	}
	log := ctrl.LoggerFrom(ctx)
	return parallelize.Until(ctx, len(pods), func(i int) error {
		return evictPodForReclaim(ctx, r.client, log, pods[i])
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
