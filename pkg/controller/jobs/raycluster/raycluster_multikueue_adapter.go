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

package raycluster

import (
	"context"
	"fmt"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/ray"
	"sigs.k8s.io/kueue/pkg/util/api"
)

var _ jobframework.MultiKueueAdapter = ray.NewMKAdapter(
	copyJobSpec, copyJobStatus, getEmptyList, gvk, getManagedBy, setManagedBy,
	ray.WithElasticReplicaSync(elasticReplicaSync()),
)

func copyJobStatus(dst, src *rayv1.RayCluster) {
	dst.Status = src.Status
}

func copyJobSpec(dst, src *rayv1.RayCluster) {
	*dst = rayv1.RayCluster{
		ObjectMeta: api.CloneObjectMetaForCreation(&src.ObjectMeta),
		Spec:       *src.Spec.DeepCopy(),
	}
}

// elasticReplicaSync wires the RayCluster-specific hooks used by the shared Ray
// MultiKueue adapter: Spec pushes manager-driven (non-autoscaling) replica edits
// to the worker copy, and Runtime reflects worker-side autoscaler resizes back
// onto the manager as annotations (leaving the manager spec untouched).
func elasticReplicaSync() *ray.ElasticReplicaSync[*rayv1.RayCluster, rayv1.RayCluster] {
	return &ray.ElasticReplicaSync[*rayv1.RayCluster, rayv1.RayCluster]{
		SyncReplicas: syncWorkerReplicas,
		WorkerReplicas: func(rc *rayv1.RayCluster) map[kueue.PodSetReference]int32 {
			return WorkerGroupPodCounts(&rc.Spec)
		},
		Runtime: &ray.RuntimeReplicaSync[*rayv1.RayCluster]{
			Fetch: fetchOwnWorkerState,
			Apply: SetRuntimeWorkerStateAnnotations,
		},
		WorkloadNameExtraPart: func(rc *rayv1.RayCluster) string { return GetWorkloadNameExtraPart(rc) },
		AutoscalingEnabled:    func(rc *rayv1.RayCluster) bool { return ptr.Deref(rc.Spec.EnableInTreeAutoscaling, false) },
		IsSuspended:           func(rc *rayv1.RayCluster) bool { return ptr.Deref(rc.Spec.Suspend, false) },
	}
}

// fetchOwnWorkerState reads the effective per-worker-group pod counts from the
// remote RayCluster — the worker's copy is where the Ray Autoscaler resizes
// worker groups — plus a revision derived from the remote's UID and generation.
// The UID keeps the revision, and with it the workload-slice name, unique when
// the remote is recreated and its generation restarts.
//
// A suspended remote is skipped (nil result): its replica counts were restored
// by the worker's Kueue while stopping the job, not set by the autoscaler.
func fetchOwnWorkerState(_ context.Context, _ client.Client, remoteCluster *rayv1.RayCluster) (*ray.FetchResult, error) {
	if ptr.Deref(remoteCluster.Spec.Suspend, false) {
		return nil, nil
	}
	return &ray.FetchResult{
		Counts:   WorkerGroupPodCounts(&remoteCluster.Spec),
		Revision: fmt.Sprintf("%s-%d", remoteCluster.UID, remoteCluster.Generation),
	}, nil
}

// syncWorkerReplicas copies Replicas, MinReplicas, MaxReplicas and NumOfHosts of each
// worker group from src into dst by group name and returns whether dst changed. They
// are the inputs of the per-group count that needElasticSync compares.
func syncWorkerReplicas(dst, src *rayv1.RayCluster) bool {
	srcGroups := make(map[string]*rayv1.WorkerGroupSpec, len(src.Spec.WorkerGroupSpecs))
	for i := range src.Spec.WorkerGroupSpecs {
		srcGroups[src.Spec.WorkerGroupSpecs[i].GroupName] = &src.Spec.WorkerGroupSpecs[i]
	}
	changed := false
	for i := range dst.Spec.WorkerGroupSpecs {
		wgs := &dst.Spec.WorkerGroupSpecs[i]
		want, ok := srcGroups[wgs.GroupName]
		if !ok {
			continue
		}
		changed = syncInt32Ptr(&wgs.Replicas, want.Replicas) || changed
		changed = syncInt32Ptr(&wgs.MinReplicas, want.MinReplicas) || changed
		changed = syncInt32Ptr(&wgs.MaxReplicas, want.MaxReplicas) || changed
		if wgs.NumOfHosts != want.NumOfHosts {
			wgs.NumOfHosts = want.NumOfHosts
			changed = true
		}
	}
	return changed
}

// syncInt32Ptr sets *dst to a copy of want when they differ and reports whether it did.
func syncInt32Ptr(dst **int32, want *int32) bool {
	if ptr.Equal(*dst, want) {
		return false
	}
	if want == nil {
		*dst = nil
	} else {
		*dst = new(*want)
	}
	return true
}

func getEmptyList() client.ObjectList {
	return &rayv1.RayClusterList{}
}

func getManagedBy(job *rayv1.RayCluster) *string {
	return job.Spec.ManagedBy
}

func setManagedBy(job *rayv1.RayCluster, val *string) {
	job.Spec.ManagedBy = val
}
