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

package statefulset

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	statefulsettesting "sigs.k8s.io/kueue/pkg/util/testingjobs/statefulset"
)

const testUID = "sts"

var (
	baseCmpOpts = cmp.Options{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
	}
)

func TestReconciler(t *testing.T) {
	cases := map[string]struct {
		stsKey          client.ObjectKey
		statefulSet     *appsv1.StatefulSet
		workloads       []kueue.Workload
		wantStatefulSet *appsv1.StatefulSet
		wantWorkloads   []kueue.Workload
		wantErr         error
	}{
		"should add StatefulSet to Workload owner references if replicas > 0": {
			stsKey: client.ObjectKey{Name: "sts", Namespace: "ns"},
			statefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Queue("lq").
				Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(types.UID("sts-uid"), "sts"), "ns").
					Obj(),
			},
			wantStatefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Queue("lq").
				DeepCopy(),
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(types.UID("sts-uid"), "sts"), "ns").
					OwnerReference(gvk, "sts", "sts-uid").
					Obj(),
			},
		},
		"should remove StatefulSet from Workload owner references if replicas = 0": {
			stsKey: client.ObjectKey{Name: "sts", Namespace: "ns"},
			statefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Queue("lq").
				Replicas(0).
				Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(types.UID("sts-uid"), "sts"), "ns").
					OwnerReference(gvk, "sts", "sts-uid").
					Obj(),
			},
			wantStatefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID("sts-uid").
				Queue("lq").
				Replicas(0).
				DeepCopy(),
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(types.UID("sts-uid"), "sts"), "ns").
					Obj(),
			},
		},
		"statefulset not found": {
			stsKey: client.ObjectKey{Name: "sts", Namespace: "ns"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			clientBuilder := utiltesting.NewClientBuilder()
			indexer := utiltesting.AsIndexer(clientBuilder)

			objs := make([]client.Object, 0, len(tc.workloads)+1)
			if tc.statefulSet != nil {
				objs = append(objs, tc.statefulSet)
			}

			for _, wl := range tc.workloads {
				objs = append(objs, wl.DeepCopy())
			}

			kClient := clientBuilder.WithObjects(objs...).Build()

			reconciler, err := NewReconciler(ctx, kClient, indexer, nil)
			if err != nil {
				t.Errorf("Error creating the reconciler: %v", err)
			}

			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: tc.stsKey})
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Reconcile returned error (-want,+got):\n%s", diff)
			}

			gotStatefulSet := &appsv1.StatefulSet{}
			err = kClient.Get(ctx, tc.stsKey, gotStatefulSet)
			if client.IgnoreNotFound(err) != nil {
				t.Fatalf("Could not get StatefulSet after reconcile: %v", err)
			}
			if err != nil {
				gotStatefulSet = nil
			}

			if diff := cmp.Diff(tc.wantStatefulSet, gotStatefulSet, baseCmpOpts...); diff != "" {
				t.Errorf("StatefulSet after reconcile (-want,+got):\n%s", diff)
			}

			gotWorkloadList := &kueue.WorkloadList{}
			if err := kClient.List(ctx, gotWorkloadList); err != nil {
				t.Fatalf("Could not get WorkloadList after reconcile: %v", err)
			}

			if diff := cmp.Diff(tc.wantWorkloads, gotWorkloadList.Items, baseCmpOpts...); diff != "" {
				t.Errorf("Workloads after reconcile (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestHandle(t *testing.T) {
	testCases := map[string]struct {
		obj  client.Object
		want bool
	}{
		"not a statefulset": {
			obj:  &corev1.Pod{},
			want: false,
		},
		"statefulset without queue label": {
			obj:  statefulsettesting.MakeStatefulSet("sts", metav1.NamespaceDefault).Obj(),
			want: false,
		},
		"statefulset with queue label": {
			obj:  statefulsettesting.MakeStatefulSet("sts", metav1.NamespaceDefault).Queue("lq").Obj(),
			want: true,
		},
		"statefulset managed by another framework": {
			obj: statefulsettesting.MakeStatefulSet("sts", metav1.NamespaceDefault).
				PodTemplateAnnotation(podconstants.SuspendedByParentAnnotation, "test-framework").
				Obj(),
			want: false,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			r := Reconciler{}
			got := r.handle(tc.obj)
			if got != tc.want {
				t.Errorf("handle(%T) = %v, want %v", tc.obj, got, tc.want)
			}
		})
	}
}
