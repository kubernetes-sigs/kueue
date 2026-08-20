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

package scheduler

import (
	"context"
	"slices"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
)

// headGroupPodSetName is the PodSet name Kueue assigns to a RayCluster head
// group. Only worker-group PodSets map to a RayCluster worker group, so the head
// PodSet is skipped when computing scaling gates.
const headGroupPodSetName = "head"

var rayClusterGVK = rayv1.GroupVersion.WithKind("RayCluster")

// reconcileRayClusterScaleGate keeps the quota-exceeded scaling gate on a
// RayCluster's worker groups in sync with the outcome of a scheduling attempt.
//
// When gated is true, the gate is added to the worker groups that did not fit
// (identified from the in-memory assignment); when gated is false, the gate is
// removed from all worker groups. KubeRay preserves the gate across reconciles,
// and the Ray Autoscaler uses it to fall back to a lower-priority worker group.
//
// It is best-effort: it never blocks scheduling and only logs on failure.
func (s *Scheduler) reconcileRayClusterScaleGate(ctx context.Context, e *entry, gated bool) {
	ownerRef := metav1.GetControllerOf(e.Obj)
	if ownerRef == nil || ownerRef.Kind != rayClusterGVK.Kind || ownerRef.APIVersion != rayClusterGVK.GroupVersion().String() {
		return
	}

	var gatedGroups map[string]bool
	if gated {
		gatedGroups = notFitWorkerGroups(&e.assignment)
		if len(gatedGroups) == 0 {
			return
		}
	}

	log := ctrl.LoggerFrom(ctx)
	rc := &rayv1.RayCluster{}
	key := types.NamespacedName{Namespace: e.Obj.Namespace, Name: ownerRef.Name}
	if err := s.client.Get(ctx, key, rc); err != nil {
		if !apierrors.IsNotFound(err) {
			log.Error(err, "Failed to get RayCluster for scaleGate reconcile", "rayCluster", key)
		}
		return
	}

	if err := clientutil.Patch(ctx, s.client, rc, func() (bool, error) {
		return applyScaleGate(rc, gatedGroups, gated), nil
	}); err != nil {
		log.Error(err, "Failed to patch RayCluster scaleGate", "rayCluster", key)
	}
}

// notFitWorkerGroups returns the set of RayCluster worker group names (PodSet
// names, excluding the head group) that did not fit in the assignment.
func notFitWorkerGroups(assignment *flavorassigner.Assignment) map[string]bool {
	groups := make(map[string]bool)
	for i := range assignment.PodSets {
		ps := &assignment.PodSets[i]
		// Exclude the head group from scaling-gate computation.
		if string(ps.Name) == headGroupPodSetName {
			continue
		}
		if ps.RepresentativeMode() == flavorassigner.NoFit {
			groups[string(ps.Name)] = true
		}
	}
	return groups
}

// applyScaleGate adds or removes the quota-exceeded gate on the RayCluster's
// worker groups and returns whether anything changed. When gated is true, the
// gate is added only to groups in gatedGroups; when false, it is removed from
// all groups.
func applyScaleGate(rc *rayv1.RayCluster, gatedGroups map[string]bool, gated bool) bool {
	changed := false
	for i := range rc.Spec.WorkerGroupSpecs {
		wgs := &rc.Spec.WorkerGroupSpecs[i]
		groupName := string(kueue.NewPodSetReference(wgs.GroupName))
		// Add the gate only when gating this group; otherwise remove it (on
		// admission, or from a group that fit).
		wantGate := gated && gatedGroups[groupName]
		hasGate := slices.Contains(wgs.ScaleStrategy.ScaleGate, controllerconstants.RayClusterQuotaExceededScaleGate)
		switch {
		case wantGate && !hasGate:
			wgs.ScaleStrategy.ScaleGate = append(wgs.ScaleStrategy.ScaleGate, controllerconstants.RayClusterQuotaExceededScaleGate)
			changed = true
		case !wantGate && hasGate:
			wgs.ScaleStrategy.ScaleGate = slices.DeleteFunc(wgs.ScaleStrategy.ScaleGate, func(g string) bool {
				return g == controllerconstants.RayClusterQuotaExceededScaleGate
			})
			changed = true
		}
	}
	return changed
}
