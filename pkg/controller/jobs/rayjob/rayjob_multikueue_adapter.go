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
		Runtime: &ray.RuntimeReplicaSync[*rayv1.RayJob]{
			Fetch: fetchChildWorkerState,
			Apply: raycluster.SetRuntimeWorkerStateAnnotations,
		},
	}
}

// fetchChildWorkerState reads the RayJob's child RayCluster on the worker
// cluster and returns its effective per-worker-group pod counts plus a
// revision derived from the child's UID and generation. The UID keeps the
// revision — and with it the workload-slice name — unique when KubeRay
// recreates the child and its generation restarts.
//
// A suspended remote is skipped (found=false): its worker state was restored by
// the worker's Kueue while stopping the job, not set by the autoscaler.
func fetchChildWorkerState(ctx context.Context, remoteClient client.Client, remoteJob *rayv1.RayJob) (map[kueue.PodSetReference]int32, string, bool, error) {
	if remoteJob.Spec.Suspend {
		return nil, "", false, nil
	}
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
