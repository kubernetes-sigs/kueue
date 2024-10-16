/*
Copyright 2023 The Kubernetes Authors.

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

package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	visibility "sigs.k8s.io/kueue/apis/visibility/v1beta1"
	"sigs.k8s.io/kueue/pkg/workload"
)

func newPendingWorkload(wlInfo *workload.Info, positionInLq int32, positionInCq int) *visibility.PendingWorkload {
	ownerReferences := make([]metav1.OwnerReference, 0, len(wlInfo.Obj.OwnerReferences))
	for _, ref := range wlInfo.Obj.OwnerReferences {
		ownerReferences = append(ownerReferences, metav1.OwnerReference{
			APIVersion: ref.APIVersion,
			Kind:       ref.Kind,
			Name:       ref.Name,
			UID:        ref.UID,
		})
	}
	return &visibility.PendingWorkload{
		ObjectMeta: metav1.ObjectMeta{
			Name:              wlInfo.Obj.Name,
			Namespace:         wlInfo.Obj.Namespace,
			OwnerReferences:   ownerReferences,
			CreationTimestamp: wlInfo.Obj.CreationTimestamp,
		},
		PositionInClusterQueue: int32(positionInCq),
		Priority:               *wlInfo.Obj.Spec.Priority,
		LocalQueueName:         wlInfo.Obj.Spec.QueueName,
		PositionInLocalQueue:   positionInLq,
	}
}
