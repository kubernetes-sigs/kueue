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

package rayjob

import (
	"context"
	"fmt"
	"math"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/ray"
	"sigs.k8s.io/kueue/pkg/controller/jobs/raycluster"
	"sigs.k8s.io/kueue/pkg/util/api"
)

var _ jobframework.MultiKueueAdapter = ray.NewMKAdapter(
	copyJobSpec, copyJobStatus, getEmptyList, gvk, getManagedBy, setManagedBy,
	ray.WithElasticReplicaSync(elasticRuntimeSync()),
)

// elasticRuntimeSync wires the RayJob-specific hooks for worker-side
// autoscaling over MultiKueue. A RayJob's worker replicas live on the child
// RayCluster KubeRay creates on the worker cluster, so the runtime state is
// fetched from that child and reflected onto the manager RayJob as
// annotations (consumed by UpdatePodSets and the workload-slice naming).
// Spec is left unset: a RayJob's own spec carries no live replicas, so without
// autoscaling it keeps its create-once behavior.
func elasticRuntimeSync() *ray.ElasticReplicaSync[*rayv1.RayJob, rayv1.RayJob] {
	return &ray.ElasticReplicaSync[*rayv1.RayJob, rayv1.RayJob]{
		WorkloadNameExtraPart: func(j *rayv1.RayJob) string { return raycluster.GetWorkloadNameExtraPart(j.GetObjectMeta()) },
		AutoscalingEnabled: func(j *rayv1.RayJob) bool {
			return j.Spec.RayClusterSpec != nil && ptr.Deref(j.Spec.RayClusterSpec.EnableInTreeAutoscaling, false)
		},
		RemoteSuspended: func(j *rayv1.RayJob) bool { return j.Spec.Suspend },
		Runtime: &ray.RuntimeReplicaSync[*rayv1.RayJob]{
			Fetch: fetchChildWorkerState,
			Apply: applyChildWorkerState,
		},
	}
}

// fetchChildWorkerState reads the RayJob's child RayCluster on the worker
// cluster and returns its effective per-worker-group pod counts plus a
// revision derived from the child's UID and generation. The UID keeps the
// revision — and with it the workload-slice name — unique when KubeRay
// recreates the child and its generation restarts.
func fetchChildWorkerState(ctx context.Context, remoteClient client.Client, remoteJob *rayv1.RayJob) (map[kueue.PodSetReference]int32, string, bool, error) {
	childName := remoteJob.Status.RayClusterName
	if childName == "" {
		return nil, "", false, nil
	}
	child := &rayv1.RayCluster{}
	err := remoteClient.Get(ctx, types.NamespacedName{Namespace: remoteJob.Namespace, Name: childName}, child)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, "", false, nil
		}
		return nil, "", false, err
	}
	revision := fmt.Sprintf("%s-%d", child.UID, child.Generation)
	return raycluster.WorkerGroupPodCounts(&child.Spec), revision, true, nil
}

// applyChildWorkerState records the child's per-group counts and revision as
// annotations on the manager RayJob, returning whether anything changed. The
// counts feed the manager's PodSet derivation (UpdatePodSets fallback); the
// revision feeds the elastic workload-slice name (GetWorkloadNameExtraPart),
// so an autoscaler-driven resize yields a new slice.
func applyChildWorkerState(localJob *rayv1.RayJob, counts map[kueue.PodSetReference]int32, revision string) bool {
	counts = retainWorkerCountsWithinDeclaredBounds(localJob, counts)
	serialized, err := raycluster.SerializeWorkerGroupCounts(counts)
	if err != nil {
		// Counts are plain name/count pairs; serialization cannot realistically
		// fail, but never propagate a broken value.
		return false
	}
	annotations := localJob.GetAnnotations()
	// Count-neutral child updates (e.g. generation-only bumps) must not mint
	// replacement slices, so equality is decided on the counts alone.
	if annotations[raycluster.MultiKueueRuntimePodSetReplicaSizesAnnotation] == serialized {
		return false
	}
	if annotations == nil {
		annotations = make(map[string]string, 2)
	}
	annotations[raycluster.MultiKueueRuntimePodSetReplicaSizesAnnotation] = serialized
	annotations[raycluster.RayClusterGenerationAnnotation] = revision
	localJob.SetAnnotations(annotations)
	return true
}

// retainWorkerCountsWithinDeclaredBounds drops any per-group count that falls
// outside the manager-declared [MinReplicas, MaxReplicas] of the matching
// worker group, or whose group is absent from the manager's RayClusterSpec.
//
// counts originate from the child RayCluster on the worker cluster, which sits
// across a cluster trust boundary. A count the in-tree autoscaler could not
// have produced (outside the declared bounds) must not be reflected onto the
// manager, where it would inflate the admitted PodSet counts and thus the
// reserved quota. This mirrors the clamp the RayCluster spec-reflect path
// applies in reflectWorkerReplicas.
func retainWorkerCountsWithinDeclaredBounds(localJob *rayv1.RayJob, counts map[kueue.PodSetReference]int32) map[kueue.PodSetReference]int32 {
	if localJob.Spec.RayClusterSpec == nil {
		return nil
	}
	type bounds struct{ min, max int32 }
	declared := make(map[kueue.PodSetReference]bounds, len(localJob.Spec.RayClusterSpec.WorkerGroupSpecs))
	for i := range localJob.Spec.RayClusterSpec.WorkerGroupSpecs {
		wgs := &localJob.Spec.RayClusterSpec.WorkerGroupSpecs[i]
		declared[kueue.NewPodSetReference(wgs.GroupName)] = bounds{
			min: ptr.Deref(wgs.MinReplicas, 0),
			max: ptr.Deref(wgs.MaxReplicas, math.MaxInt32),
		}
	}
	retained := make(map[kueue.PodSetReference]int32, len(counts))
	for name, count := range counts {
		if b, ok := declared[name]; !ok || count < b.min || count > b.max {
			continue
		}
		retained[name] = count
	}
	return retained
}

func copyJobStatus(dst, src *rayv1.RayJob) {
	dst.Status = src.Status
}

func copyJobSpec(dst, src *rayv1.RayJob) {
	*dst = rayv1.RayJob{
		ObjectMeta: api.CloneObjectMetaForCreation(&src.ObjectMeta),
		Spec:       *src.Spec.DeepCopy(),
	}
}

func getEmptyList() client.ObjectList {
	return &rayv1.RayJobList{}
}

func getManagedBy(job *rayv1.RayJob) *string {
	return job.Spec.ManagedBy
}

func setManagedBy(job *rayv1.RayJob, val *string) {
	job.Spec.ManagedBy = val
}
