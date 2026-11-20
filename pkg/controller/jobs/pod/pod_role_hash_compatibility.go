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

package pod

import (
	"context"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
)

// restoreLegacyGroupRoles lets equivalent replacements use reservations created
// before restartPolicy contributed to the role hash. Persist the role before
// filtering group members so matching, ungating and finalization use one identity.
// TODO: Remove only when Workloads with pre-restartPolicy role hashes no longer
// need to survive upgrades.
func (p *Pod) restoreLegacyGroupRoles(ctx context.Context, c client.Client, wl *kueue.Workload) error {
	if !p.isGroup {
		return nil
	}
	var candidates []*corev1.Pod
	for i := range p.list.Items {
		pod := &p.list.Items[i]
		_, annotated := pod.Annotations[podconstants.RoleHashAnnotation]
		// Old Pods without an annotation also need their persisted role, even
		// when already running or terminating. Do not rename an annotated Pod
		// that has already started or is being deleted.
		if annotated && (!isGated(pod) || !pod.DeletionTimestamp.IsZero()) {
			continue
		}
		role, err := getRoleHash(*pod)
		if err != nil {
			return errRoleHashCalculationForPod(pod.Name, err)
		}
		if !slices.ContainsFunc(wl.Spec.PodSets, func(ps kueue.PodSet) bool { return string(ps.Name) == role }) {
			candidates = append(candidates, pod)
		}
	}
	if len(candidates) == 0 {
		return nil
	}

	for i := range wl.Spec.PodSets {
		ps := &wl.Spec.PodSets[i]
		legacySpec := legacyPodSpec(&ps.Template.Spec)
		legacyRole, err := utilpod.GenerateRoleHash(legacySpec)
		if err != nil {
			return fmt.Errorf("failed to calculate legacy role for podset %q: %w", ps.Name, err)
		}
		if string(ps.Name) != legacyRole {
			continue
		}
		shape := utilpod.SpecShape(&ps.Template.Spec)
		for _, pod := range candidates {
			// An unannotated member that already started may have shared its
			// old role with a different sidecar layout. Preserve its reservation.
			// New gated replacements must match the complete persisted shape.
			_, annotated := pod.Annotations[podconstants.RoleHashAnnotation]
			matches := apiequality.Semantic.DeepEqual(shape, utilpod.SpecShape(&pod.Spec))
			if !annotated && !isGated(pod) {
				matches = apiequality.Semantic.DeepEqual(utilpod.SpecShape(legacySpec), utilpod.SpecShape(legacyPodSpec(&pod.Spec)))
			}
			if !matches {
				continue
			}
			computedRole, err := utilpod.GenerateRoleHash(&pod.Spec)
			if err != nil {
				return errRoleHashCalculationForPod(pod.Name, err)
			}
			// A distinct caller-supplied role is intentional, not an automatic
			// hash to migrate. An absent annotation also denotes an automatic role.
			if role, present := pod.Annotations[podconstants.RoleHashAnnotation]; present && role != computedRole {
				continue
			}
			if err := clientutil.Patch(ctx, c, pod, func() (bool, error) {
				if pod.Annotations == nil {
					pod.Annotations = make(map[string]string)
				}
				pod.Annotations[podconstants.RoleHashAnnotation] = legacyRole
				return true, nil
			}); err != nil {
				return err
			}
			if pod.Name == p.pod.Name && pod.Namespace == p.pod.Namespace {
				p.pod = *pod.DeepCopy()
			}
		}
	}
	return nil
}

// warnLegacyGroupRoleMismatch makes rejected replacements of pre-upgrade mixed
// groups visible. A regular-init reservation cannot safely admit its sidecar role.
func (p *Pod) warnLegacyGroupRoleMismatch(wl *kueue.Workload, recorder events.EventRecorder) error {
	for i := range p.list.Items {
		pod := &p.list.Items[i]
		if !isGated(pod) || !pod.DeletionTimestamp.IsZero() {
			continue
		}
		if !slices.ContainsFunc(pod.Spec.InitContainers, func(c corev1.Container) bool {
			return c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways
		}) {
			continue
		}
		computedRole, err := utilpod.GenerateRoleHash(&pod.Spec)
		if err != nil {
			return errRoleHashCalculationForPod(pod.Name, err)
		}
		if role, annotated := pod.Annotations[podconstants.RoleHashAnnotation]; annotated && role != computedRole {
			continue
		}
		if slices.ContainsFunc(wl.Spec.PodSets, func(ps kueue.PodSet) bool { return string(ps.Name) == computedRole }) {
			continue
		}
		legacySpec := legacyPodSpec(&pod.Spec)
		legacyRole, err := utilpod.GenerateRoleHash(legacySpec)
		if err != nil {
			return errRoleHashCalculationForPod(pod.Name, err)
		}
		for _, ps := range wl.Spec.PodSets {
			if string(ps.Name) != legacyRole || !apiequality.Semantic.DeepEqual(utilpod.SpecShape(legacySpec), utilpod.SpecShape(legacyPodSpec(&ps.Template.Spec))) {
				continue
			}
			recorder.Eventf(pod, wl, corev1.EventTypeWarning, ReasonLegacyPodGroupRoleMismatch, "Admission",
				"Sidecar replacement does not match the legacy PodSet role in Workload %q. Stop and recreate the entire PodGroup and its Workload to rebuild role reservations", wl.Name)
			break
		}
	}
	return nil
}

// legacyPodSpec reproduces the Pod spec used before init-container restart policies
// participated in automatic role hashes.
func legacyPodSpec(spec *corev1.PodSpec) *corev1.PodSpec {
	legacy := spec.DeepCopy()
	for i := range legacy.InitContainers {
		legacy.InitContainers[i].RestartPolicy = nil
	}
	return legacy
}
