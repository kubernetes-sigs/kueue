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
	"math"

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
	// The spec is copied verbatim, including enableInTreeAutoscaling: the remote
	// copy runs the autoscaler sidecar KubeRay injects for it, matching the
	// autoscaler container accounted in the Workload's head PodSet. The remote
	// autoscaler cannot fight the manager: with autoscaling enabled the manager
	// never pushes replicas — the worker owns them and resizes are reflected
	// back (see workerOwnsReplicas in the shared Ray adapter).
}

// elasticReplicaSync wires the RayCluster-specific hooks used by the shared Ray
// MultiKueue adapter to propagate manager-driven worker replica changes.
func elasticReplicaSync() *ray.ElasticReplicaSync[*rayv1.RayCluster, rayv1.RayCluster] {
	return &ray.ElasticReplicaSync[*rayv1.RayCluster, rayv1.RayCluster]{
		Spec: &ray.SpecReplicaSync[*rayv1.RayCluster]{
			Push:    syncWorkerReplicas,
			Reflect: reflectWorkerReplicas,
			Counts: func(rc *rayv1.RayCluster) map[kueue.PodSetReference]int32 {
				return WorkerGroupPodCounts(&rc.Spec)
			},
		},
		WorkloadNameExtraPart: func(rc *rayv1.RayCluster) string { return GetWorkloadNameExtraPart(rc) },
		AutoscalingEnabled:    func(rc *rayv1.RayCluster) bool { return ptr.Deref(rc.Spec.EnableInTreeAutoscaling, false) },
		RemoteSuspended:       func(rc *rayv1.RayCluster) bool { return ptr.Deref(rc.Spec.Suspend, false) },
	}
}

// reflectWorkerReplicas copies autoscaler-driven Replicas from the remote
// (worker) copy in src onto the manager's copy in dst, matching groups by
// name, and returns whether dst changed. Only Replicas moves in this
// direction: the manager owns MinReplicas, MaxReplicas and the autoscaling
// flag. A count outside the manager-declared [minReplicas, maxReplicas]
// cannot come from the autoscaler and is ignored.
func reflectWorkerReplicas(dst, src *rayv1.RayCluster) bool {
	srcReplicas := make(map[string]*int32, len(src.Spec.WorkerGroupSpecs))
	for i := range src.Spec.WorkerGroupSpecs {
		wgs := &src.Spec.WorkerGroupSpecs[i]
		srcReplicas[wgs.GroupName] = wgs.Replicas
	}
	changed := false
	for i := range dst.Spec.WorkerGroupSpecs {
		wgs := &dst.Spec.WorkerGroupSpecs[i]
		want, ok := srcReplicas[wgs.GroupName]
		if !ok || ptr.Equal(wgs.Replicas, want) {
			continue
		}
		count := ptr.Deref(want, 1)
		if count < ptr.Deref(wgs.MinReplicas, 0) || count > ptr.Deref(wgs.MaxReplicas, math.MaxInt32) {
			continue
		}
		wgs.Replicas = want
		changed = true
	}
	return changed
}

// syncWorkerReplicas is the manager-driven push onto the remote copy in dst:
// it copies each worker group's Replicas, MinReplicas, MaxReplicas and
// NumOfHosts from src, matching groups by name, re-asserts src's
// enableInTreeAutoscaling, and returns whether dst changed. Replicas and
// NumOfHosts feed the effective per-group pod count that needElasticSync
// compares (see WorkerGroupPodCounts); the bounds and the autoscaling flag
// follow so an out-of-band edit on the worker cannot leave an autoscaler
// running against a manager that expects to own the replica counts.
func syncWorkerReplicas(dst, src *rayv1.RayCluster) bool {
	type groupSize struct {
		replicas    *int32
		minReplicas *int32
		maxReplicas *int32
		numOfHosts  int32
	}
	srcSizes := make(map[string]groupSize, len(src.Spec.WorkerGroupSpecs))
	for i := range src.Spec.WorkerGroupSpecs {
		wgs := &src.Spec.WorkerGroupSpecs[i]
		srcSizes[wgs.GroupName] = groupSize{
			replicas:    wgs.Replicas,
			minReplicas: wgs.MinReplicas,
			maxReplicas: wgs.MaxReplicas,
			numOfHosts:  wgs.NumOfHosts,
		}
	}
	changed := false
	syncField := func(dstField **int32, want *int32) {
		if !ptr.Equal(*dstField, want) {
			if want == nil {
				*dstField = nil
			} else {
				*dstField = new(*want)
			}
			changed = true
		}
	}
	for i := range dst.Spec.WorkerGroupSpecs {
		wgs := &dst.Spec.WorkerGroupSpecs[i]
		want, ok := srcSizes[wgs.GroupName]
		if !ok {
			continue
		}
		syncField(&wgs.Replicas, want.replicas)
		syncField(&wgs.MinReplicas, want.minReplicas)
		syncField(&wgs.MaxReplicas, want.maxReplicas)
		if wgs.NumOfHosts != want.numOfHosts {
			wgs.NumOfHosts = want.numOfHosts
			changed = true
		}
	}
	if !ptr.Equal(dst.Spec.EnableInTreeAutoscaling, src.Spec.EnableInTreeAutoscaling) {
		dst.Spec.EnableInTreeAutoscaling = src.Spec.EnableInTreeAutoscaling
		changed = true
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
