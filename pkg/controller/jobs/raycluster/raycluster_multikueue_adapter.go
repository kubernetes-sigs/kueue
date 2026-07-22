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
// MultiKueue adapter to propagate manager-driven worker replica changes.
func elasticReplicaSync() *ray.ElasticReplicaSync[*rayv1.RayCluster, rayv1.RayCluster] {
	return &ray.ElasticReplicaSync[*rayv1.RayCluster, rayv1.RayCluster]{
		SyncReplicas:          syncWorkerReplicas,
		WorkerReplicas:        workerReplicaCounts,
		WorkloadNameExtraPart: func(rc *rayv1.RayCluster) string { return GetWorkloadNameExtraPart(rc) },
	}
}

// workerReplicaCounts returns the effective worker pod count per worker group,
// matching how BuildPodSets derives PodSet counts (replicas scaled by NumOfHosts).
func workerReplicaCounts(rc *rayv1.RayCluster) map[kueue.PodSetReference]int32 {
	counts := make(map[kueue.PodSetReference]int32, len(rc.Spec.WorkerGroupSpecs))
	for i := range rc.Spec.WorkerGroupSpecs {
		wgs := &rc.Spec.WorkerGroupSpecs[i]
		counts[kueue.NewPodSetReference(wgs.GroupName)] = effectiveWorkerCount(wgs)
	}
	return counts
}

// syncWorkerReplicas copies each worker group's Replicas and NumOfHosts from
// src into dst, matching groups by name, and returns whether dst changed. Both
// fields feed the effective per-group pod count that needElasticSync compares
// (see workerReplicaCounts), so both must be propagated to keep the remote in
// sync when either changes.
func syncWorkerReplicas(dst, src *rayv1.RayCluster) bool {
	type groupSize struct {
		replicas   *int32
		numOfHosts int32
	}
	srcSizes := make(map[string]groupSize, len(src.Spec.WorkerGroupSpecs))
	for i := range src.Spec.WorkerGroupSpecs {
		wgs := &src.Spec.WorkerGroupSpecs[i]
		srcSizes[wgs.GroupName] = groupSize{replicas: wgs.Replicas, numOfHosts: wgs.NumOfHosts}
	}
	changed := false
	for i := range dst.Spec.WorkerGroupSpecs {
		wgs := &dst.Spec.WorkerGroupSpecs[i]
		want, ok := srcSizes[wgs.GroupName]
		if !ok {
			continue
		}
		if !ptr.Equal(wgs.Replicas, want.replicas) {
			if want.replicas == nil {
				wgs.Replicas = nil
			} else {
				wgs.Replicas = new(*want.replicas)
			}
			changed = true
		}
		if wgs.NumOfHosts != want.numOfHosts {
			wgs.NumOfHosts = want.numOfHosts
			changed = true
		}
	}
	return changed
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
