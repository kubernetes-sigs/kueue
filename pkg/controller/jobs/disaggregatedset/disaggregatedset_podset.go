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

package disaggregatedset

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"
	disaggregatedsetv1 "sigs.k8s.io/lws/api/disaggregatedset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
)

const (
	leaderPodSetSuffix = "-leader"
	workerPodSetSuffix = "-worker"
	// roleGroupPrefix names the TAS PodSet group of a role. All roles share one
	// Workload, so the user's group name is replaced to keep roles apart.
	roleGroupPrefix = "role-"

	dsDomainPrefix  = "disaggregatedset.x-k8s.io"
	lwsDomainPrefix = "leaderworkerset.sigs.k8s.io"

	// defaultRoleReplicas and defaultRoleSize mirror the LeaderWorkerSet API
	// defaults for spec.replicas and spec.leaderWorkerTemplate.size.
	defaultRoleReplicas = 1
	defaultRoleSize     = 1
	// defaultRoleMaxUnavailable mirrors the LeaderWorkerSet API default for
	// rollingUpdateConfiguration.maxUnavailable.
	defaultRoleMaxUnavailable = 1

	// maxSliceMembership caps the Pods that can be owners of one slice Workload
	// at the same time, including the Pods of a rollout.
	maxSliceMembership = 1000
	// maxTemplateBytes caps the role templates that a slice Workload copies.
	maxTemplateBytes = 512 * 1024

	// The constants below estimate the size of a slice Workload, see
	// projectedWorkloadBytes. TestProjectedWorkloadBytes calibrates them.
	//
	// ownerBytes is one Pod owner reference and its managed-fields entry.
	ownerBytes = 256
	// tasBytesPerPod is the TAS assignment of one Pod in its own domain at every
	// level of a Topology with the maximum number of levels.
	tasBytesPerPod = 1400
	// workloadHeadroomBytes covers the rest of the Workload metadata and status.
	workloadHeadroomBytes = 64 * 1024
	// safeWorkloadBytes is safely below the 1.5 MiB object size limit.
	safeWorkloadBytes = 1280 * 1024
)

var errPodSetCountOverflow = errors.New("pod set count overflows int32")

// tasAnnotations lists the pod template annotations that configure Topology
// Aware Scheduling.
var tasAnnotations = []string{
	kueue.PodSetRequiredTopologyAnnotation,
	kueue.PodSetPreferredTopologyAnnotation,
	kueue.PodSetUnconstrainedTopologyAnnotation,
	kueue.PodSetSliceRequiredTopologyAnnotation,
	kueue.PodSetSliceSizeAnnotation,
	kueue.PodSetSliceRequiredTopologyConstraintsAnnotation,
	kueue.PodSetGroupName,
	kueue.PodSetTopologySpreadingAnnotation,
	kueue.PodIndexOffsetAnnotation,
}

// rolePodSet is one PodSet that a role contributes to a slice Workload.
type rolePodSet struct {
	name     kueue.PodSetReference
	roleName string
	count    int64
	template *corev1.PodTemplateSpec
	// metaPath is the path of the template metadata, used in validation errors.
	metaPath *field.Path
}

func roleReplicas(role *disaggregatedsetv1.DisaggregatedRoleSpec) int32 {
	return ptr.Deref(role.Spec.Replicas, defaultRoleReplicas)
}

func roleSize(role *disaggregatedsetv1.DisaggregatedRoleSpec) int32 {
	return ptr.Deref(role.Spec.LeaderWorkerTemplate.Size, defaultRoleSize)
}

// roleMaxUnavailable resolves the rollout maxUnavailable of a role to a group
// count. Percentages round down, as in the LeaderWorkerSet API.
func roleMaxUnavailable(role *disaggregatedsetv1.DisaggregatedRoleSpec) (int, error) {
	cfg := role.Spec.RolloutStrategy.RollingUpdateConfiguration
	if cfg == nil {
		return defaultRoleMaxUnavailable, nil
	}
	return intstr.GetScaledValueFromIntOrPercent(&cfg.MaxUnavailable, int(roleReplicas(role)), false)
}

// roleMaxSurge resolves the rollout maxSurge of a role to a group count.
// Percentages round up, as in the LeaderWorkerSet API.
func roleMaxSurge(role *disaggregatedsetv1.DisaggregatedRoleSpec) (int, error) {
	cfg := role.Spec.RolloutStrategy.RollingUpdateConfiguration
	if cfg == nil {
		return 0, nil
	}
	return intstr.GetScaledValueFromIntOrPercent(&cfg.MaxSurge, int(roleReplicas(role)), true)
}

// allRolesScaledToZero reports whether every role has zero replicas, in which
// case the DisaggregatedSet has no slice Workloads.
func allRolesScaledToZero(ds *disaggregatedsetv1.DisaggregatedSet) bool {
	for i := range ds.Spec.Roles {
		if roleReplicas(&ds.Spec.Roles[i]) != 0 {
			return false
		}
	}
	return true
}

// rolePodSets returns the PodSets of one role, in the order they appear in the
// slice Workload. A role with a leader template has a leader PodSet with one
// Pod per group, and a worker PodSet unless every group is only a leader.
func rolePodSets(role *disaggregatedsetv1.DisaggregatedRoleSpec, rolePath *field.Path) []rolePodSet {
	replicas := int64(roleReplicas(role))
	size := int64(roleSize(role))
	lwt := &role.Spec.LeaderWorkerTemplate
	lwtPath := rolePath.Child("spec", "leaderWorkerTemplate")
	workerMetaPath := lwtPath.Child("workerTemplate", "metadata")

	if lwt.LeaderTemplate == nil {
		return []rolePodSet{{
			name:     kueue.PodSetReference(role.Name),
			roleName: role.Name,
			count:    replicas * size,
			template: &lwt.WorkerTemplate,
			metaPath: workerMetaPath,
		}}
	}
	podSets := []rolePodSet{{
		name:     kueue.PodSetReference(role.Name + leaderPodSetSuffix),
		roleName: role.Name,
		count:    replicas,
		template: lwt.LeaderTemplate,
		metaPath: lwtPath.Child("leaderTemplate", "metadata"),
	}}
	if size > 1 {
		podSets = append(podSets, rolePodSet{
			name:     kueue.PodSetReference(role.Name + workerPodSetSuffix),
			roleName: role.Name,
			count:    replicas * (size - 1),
			template: &lwt.WorkerTemplate,
			metaPath: workerMetaPath,
		})
	}
	return podSets
}

// slicePodSets returns the PodSets of every role, in role order.
func slicePodSets(ds *disaggregatedsetv1.DisaggregatedSet) []rolePodSet {
	var podSets []rolePodSet
	rolesPath := field.NewPath("spec", "roles")
	for i := range ds.Spec.Roles {
		podSets = append(podSets, rolePodSets(&ds.Spec.Roles[i], rolesPath.Index(i))...)
	}
	return podSets
}

// podSets builds the PodSets of a slice Workload.
func podSets(ds *disaggregatedsetv1.DisaggregatedSet) ([]kueue.PodSet, error) {
	rolePodSets := slicePodSets(ds)
	podSets := make([]kueue.PodSet, 0, len(rolePodSets))
	for _, rps := range rolePodSets {
		podSet, err := newPodSet(rps)
		if err != nil {
			return nil, err
		}
		podSets = append(podSets, *podSet)
	}
	return podSets, nil
}

func newPodSet(rps rolePodSet) (*kueue.PodSet, error) {
	if rps.count > math.MaxInt32 {
		return nil, fmt.Errorf("%w: pod set %q has %d pods", errPodSetCountOverflow, rps.name, rps.count)
	}
	podSet := &kueue.PodSet{
		Name:     rps.name,
		Count:    int32(rps.count),
		Template: *rps.template.DeepCopy(),
	}
	dropPrefixedKeys(podSet.Template.Labels)
	dropPrefixedKeys(podSet.Template.Annotations)
	jobframework.SanitizePodSet(podSet)
	if features.Enabled(features.TopologyAwareScheduling) {
		topologyRequest, err := jobframework.NewPodSetTopologyRequest(rps.template.ObjectMeta.DeepCopy()).Build()
		if err != nil {
			return nil, err
		}
		if topologyRequest != nil && topologyRequest.PodSetGroupName != nil {
			topologyRequest.PodSetGroupName = new(roleGroupPrefix + rps.roleName)
		}
		podSet.TopologyRequest = topologyRequest
	}
	return podSet, nil
}

func dropPrefixedKeys(m map[string]string) {
	for k := range m {
		if strings.HasPrefix(k, lwsDomainPrefix) || strings.HasPrefix(k, dsDomainPrefix) {
			delete(m, k)
		}
	}
}

// sliceMaxMembership returns the most Pods that one slice Workload can list as
// owners at the same time. During a rollout, each role can have up to
// maxUnavailable terminating groups, the groups that replace them, and one
// extra group that the DisaggregatedSet planner creates when the
// unavailability budget is used.
func sliceMaxMembership(ds *disaggregatedsetv1.DisaggregatedSet) (int64, error) {
	var membership int64
	for i := range ds.Spec.Roles {
		role := &ds.Spec.Roles[i]
		maxUnavailable, err := roleMaxUnavailable(role)
		if err != nil {
			return 0, err
		}
		membership += (int64(roleReplicas(role)) + int64(maxUnavailable) + 1) * int64(roleSize(role))
	}
	return membership, nil
}

// templateBytes returns the serialized size of the role templates, counted once
// per PodSet that copies them.
func templateBytes(podSets []rolePodSet) (int, error) {
	var total int
	for _, rps := range podSets {
		b, err := json.Marshal(rps.template)
		if err != nil {
			return 0, err
		}
		total += len(b)
	}
	return total, nil
}

// projectedWorkloadBytes estimates the largest size of a slice Workload: its
// templates, one owner reference per Pod and, with TAS, one TAS domain per Pod.
func projectedWorkloadBytes(templateBytes int, maxMembership int64, withTAS bool) int64 {
	perPod := int64(ownerBytes)
	if withTAS {
		perPod += tasBytesPerPod
	}
	return int64(templateBytes) + maxMembership*perPod + workloadHeadroomBytes
}

// hasTASAnnotation reports whether a pod template of the DisaggregatedSet has a
// TAS annotation.
func hasTASAnnotation(podSets []rolePodSet) bool {
	for _, rps := range podSets {
		for _, key := range tasAnnotations {
			if _, found := rps.template.Annotations[key]; found {
				return true
			}
		}
	}
	return false
}
