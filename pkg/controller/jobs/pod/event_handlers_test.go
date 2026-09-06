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

package pod

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestWorkloadHandlerDelete(t *testing.T) {
	groupRequest := reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-group", Namespace: "group/ns"},
	}

	cases := map[string]struct {
		workload                 *kueue.Workload
		enableValidateGroupOwner bool
		wantRequests             []reconcile.Request
	}{
		"foreign workload sharing the pod group name requeues the blocked group": {
			workload:                 utiltestingapi.MakeWorkload("test-group", "ns").Obj(),
			enableValidateGroupOwner: true,
			wantRequests:             []reconcile.Request{groupRequest},
		},
		"foreign workload owned by another controller still requeues the blocked group": {
			workload: utiltestingapi.MakeWorkload("test-group", "ns").
				OwnerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "some-job", "some-job-uid").Obj(),
			enableValidateGroupOwner: true,
			wantRequests:             []reconcile.Request{groupRequest},
		},
		"pod group workload requeues the group once": {
			workload: utiltestingapi.MakeWorkload("test-group", "ns").Group().
				OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "test-pod", "test-pod-uid").Obj(),
			enableValidateGroupOwner: true,
			wantRequests:             []reconcile.Request{groupRequest},
		},
		"gate disabled keeps the previous behavior": {
			workload:                 utiltestingapi.MakeWorkload("test-group", "ns").Obj(),
			enableValidateGroupOwner: false,
			wantRequests:             nil,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.PodIntegrationValidateGroupOwner, tc.enableValidateGroupOwner)
			ctx, _ := utiltesting.ContextWithLog(t)

			q := &utiltesting.MockTypedRateLimitingInterface{}
			(&workloadHandler{}).Delete(ctx, event.DeleteEvent{Object: tc.workload}, q)

			if diff := cmp.Diff(tc.wantRequests, q.Items); diff != "" {
				t.Errorf("Unexpected reconcile requests (-want +got):\n%s", diff)
			}
		})
	}
}
