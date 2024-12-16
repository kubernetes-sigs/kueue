/*
Copyright 2024 The Kubernetes Authors.

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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingjobspod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	statefulsettesting "sigs.k8s.io/kueue/pkg/util/testingjobs/statefulset"
)

var (
	baseCmpOpts = []cmp.Option{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
	}
)

func TestReconciler(t *testing.T) {
	cases := map[string]struct {
		statefulSet     appsv1.StatefulSet
		pods            []corev1.Pod
		wantStatefulSet appsv1.StatefulSet
		wantPods        []corev1.Pod
		wantErr         error
	}{
		"statefulset with finished pods": {
			statefulSet: *statefulsettesting.MakeStatefulSet("sts", "ns").
				Replicas(0).
				Queue("lq").
				DeepCopy(),
			wantStatefulSet: *statefulsettesting.MakeStatefulSet("sts", "ns").
				Replicas(0).
				Queue("lq").
				DeepCopy(),
			pods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", "ns").
					Label(pod.GroupNameLabel, GetWorkloadName("sts")).
					KueueFinalizer().
					StatusPhase(corev1.PodSucceeded).
					Obj(),
				*testingjobspod.MakePod("pod2", "ns").
					Label(pod.GroupNameLabel, GetWorkloadName("sts")).
					KueueFinalizer().
					StatusPhase(corev1.PodFailed).
					Obj(),
				*testingjobspod.MakePod("pod3", "ns").
					Label(pod.GroupNameLabel, GetWorkloadName("sts")).
					KueueFinalizer().
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", "ns").
					Label(pod.GroupNameLabel, GetWorkloadName("sts")).
					StatusPhase(corev1.PodSucceeded).
					Obj(),
				*testingjobspod.MakePod("pod2", "ns").
					Label(pod.GroupNameLabel, GetWorkloadName("sts")).
					StatusPhase(corev1.PodFailed).
					Obj(),
				*testingjobspod.MakePod("pod3", "ns").
					Label(pod.GroupNameLabel, GetWorkloadName("sts")).
					KueueFinalizer().
					Obj(),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			clientBuilder := utiltesting.NewClientBuilder()

			objs := []client.Object{&tc.statefulSet}
			for _, p := range tc.pods {
				objs = append(objs, p.DeepCopy())
			}

			kClient := clientBuilder.WithObjects(objs...).Build()

			reconciler := NewReconciler(kClient, nil)

			statefulSetKey := client.ObjectKeyFromObject(&tc.statefulSet)
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: statefulSetKey})
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Reconcile returned error (-want,+got):\n%s", diff)
			}

			gotStatefulSet := appsv1.StatefulSet{}
			if err := kClient.Get(ctx, statefulSetKey, &gotStatefulSet); err != nil {
				t.Fatalf("Could not get StatefuleSet after reconcile: %v", err)
			}

			if diff := cmp.Diff(tc.wantStatefulSet, gotStatefulSet, baseCmpOpts...); diff != "" {
				t.Errorf("StatefuleSet after reconcile (-want,+got):\n%s", diff)
			}

			gotPodList := &corev1.PodList{}
			if err := kClient.List(ctx, gotPodList); err != nil {
				t.Fatalf("Could not get PodList after reconcile: %v", err)
			}

			if diff := cmp.Diff(tc.wantPods, gotPodList.Items, baseCmpOpts...); diff != "" {
				t.Errorf("Pods after reconcile (-want,+got):\n%s", diff)
			}
		})
	}
}
