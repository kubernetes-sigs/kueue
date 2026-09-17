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
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/ray"
	"sigs.k8s.io/kueue/pkg/util/api"
)

var _ jobframework.MultiKueueAdapter = ray.NewMKAdapter(
	copyJobSpec, copyJobStatus, getEmptyList, gvk, getManagedBy, setManagedBy,
	ray.WithMarkInactiveOnDelete(markInactive),
)

// markInactive clears the manager RayService's mirrored Ready condition once
// MultiKueue has confirmed its remote copy is gone - see
// ray.WithMarkInactiveOnDelete.
//
// If the condition already reports False for some other reason, leave it:
// that's already what IsActive() needs, and relabelling it RemoteDeleted
// would throw away the real reason.
func markInactive(job *rayv1.RayService) {
	if cond := meta.FindStatusCondition(job.Status.Conditions, string(rayv1.RayServiceReady)); cond != nil && cond.Status == metav1.ConditionFalse {
		return
	}
	meta.SetStatusCondition(&job.Status.Conditions, metav1.Condition{
		Type:    string(rayv1.RayServiceReady),
		Status:  metav1.ConditionFalse,
		Reason:  "RemoteDeleted",
		Message: "The remote RayService was deleted by MultiKueue",
	})
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
