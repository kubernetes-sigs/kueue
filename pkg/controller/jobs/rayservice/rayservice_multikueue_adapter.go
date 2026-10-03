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
	"fmt"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/ray"
	"sigs.k8s.io/kueue/pkg/controller/jobs/raycluster"
	"sigs.k8s.io/kueue/pkg/util/api"
)

var _ jobframework.MultiKueueAdapter = ray.NewMKAdapter(
	copyJobSpec, copyJobStatus, getEmptyList, gvk, getManagedBy, setManagedBy,
)

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
