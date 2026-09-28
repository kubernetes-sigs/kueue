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

package rayservice

import (
	"context"
	"errors"
	"fmt"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/ray"
	"sigs.k8s.io/kueue/pkg/controller/jobs/raycluster"
	"sigs.k8s.io/kueue/pkg/util/api"
)

var errActiveRayClusterNotControlled = errors.New("active RayCluster is not controlled by RayService")

var _ jobframework.MultiKueueAdapter = ray.NewMKAdapter(
	copyJobSpec, copyJobStatus, getEmptyList, gvk, getManagedBy, setManagedBy,
)

// elasticRuntimeSync wires worker-side autoscaling for RayService. KubeRay
// stores the live worker replicas on the active child RayCluster in the worker
// cluster, so the counts are reflected onto the manager RayService as
// annotations consumed by PodSets derivation and workload-slice naming.
func elasticRuntimeSync() *ray.ElasticReplicaSync[*rayv1.RayService, rayv1.RayService] {
	return &ray.ElasticReplicaSync[*rayv1.RayService, rayv1.RayService]{
		WorkloadNameExtraPart: func(s *rayv1.RayService) string { return raycluster.GetWorkloadNameExtraPart(s.GetObjectMeta()) },
		AutoscalingEnabled: func(s *rayv1.RayService) bool {
			return ptr.Deref(s.Spec.RayClusterSpec.EnableInTreeAutoscaling, false)
		},
		IsSuspended: func(s *rayv1.RayService) bool {
			return ptr.Deref(s.Spec.RayClusterSpec.Suspend, false)
		},
		Runtime: &ray.RuntimeReplicaSync[*rayv1.RayService]{
			Fetch: fetchActiveRayClusterWorkerState,
			Apply: raycluster.SetRuntimeWorkerStateAnnotations,
		},
	}
}

// fetchActiveRayClusterWorkerState reads the active child RayCluster on the
// worker and returns its effective worker counts and a unique revision. A
// missing child is expected while KubeRay creates or replaces the active
// cluster, so it is treated as no runtime state yet.
func fetchActiveRayClusterWorkerState(ctx context.Context, remoteClient client.Client, remoteService *rayv1.RayService) (*ray.FetchResult, error) {
	if ptr.Deref(remoteService.Spec.RayClusterSpec.Suspend, false) {
		return nil, nil
	}
	childName := remoteService.Status.ActiveServiceStatus.RayClusterName
	if childName == "" {
		return nil, nil
	}
	child := &rayv1.RayCluster{}
	err := remoteClient.Get(ctx, types.NamespacedName{Namespace: remoteService.Namespace, Name: childName}, child)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	if !metav1.IsControlledBy(child, remoteService) {
		return nil, fmt.Errorf("%w: RayCluster %q, RayService %q", errActiveRayClusterNotControlled, child.Name, remoteService.Name)
	}
	expectedWorkerGroups := make(map[string]struct{}, len(remoteService.Spec.RayClusterSpec.WorkerGroupSpecs))
	for i := range remoteService.Spec.RayClusterSpec.WorkerGroupSpecs {
		expectedWorkerGroups[remoteService.Spec.RayClusterSpec.WorkerGroupSpecs[i].GroupName] = struct{}{}
	}
	for i := range child.Spec.WorkerGroupSpecs {
		if _, found := expectedWorkerGroups[child.Spec.WorkerGroupSpecs[i].GroupName]; !found {
			return nil, fmt.Errorf("active RayCluster %q has unexpected worker group %q", child.Name, child.Spec.WorkerGroupSpecs[i].GroupName)
		}
	}
	return &ray.FetchResult{
		Counts:   raycluster.WorkerGroupPodCounts(&child.Spec),
		Revision: fmt.Sprintf("%s-%d", child.UID, child.Generation),
	}, nil
}

// remoteSpecSyncer is RayService's RemoteSpecSyncer for MultiKueue.
type remoteSpecSyncer struct{}

var _ ray.RemoteSpecSyncer[*rayv1.RayService] = remoteSpecSyncer{}

// NeedsSync reports whether the manager's serveConfigV2 differs from the worker's.
// serveConfigV2 is the Ray Serve application config; forwarding it performs an
// in-place Serve update on the worker cluster's RayService.
func (remoteSpecSyncer) NeedsSync(remote, local *rayv1.RayService) bool {
	return remote.Spec.ServeConfigV2 != local.Spec.ServeConfigV2
}

func (remoteSpecSyncer) Apply(remote, local *rayv1.RayService) {
	remote.Spec.ServeConfigV2 = local.Spec.ServeConfigV2
}

func copyJobStatus(dst, src *rayv1.RayService) {
	dst.Status = src.Status
}

func copyJobSpec(dst, src *rayv1.RayService) {
	*dst = rayv1.RayService{
		ObjectMeta: api.CloneObjectMetaForCreation(&src.ObjectMeta),
		Spec:       *src.Spec.DeepCopy(),
	}
}

func getEmptyList() client.ObjectList {
	return &rayv1.RayServiceList{}
}

func getManagedBy(job *rayv1.RayService) *string {
	return job.Spec.ManagedBy
}

func setManagedBy(job *rayv1.RayService, val *string) {
	job.Spec.ManagedBy = val
}
