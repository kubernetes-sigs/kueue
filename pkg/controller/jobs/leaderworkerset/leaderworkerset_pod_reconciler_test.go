/*
Copyright 2025 The Kubernetes Authors.

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

package leaderworkerset

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	podcontroller "sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/util/testingjobs/leaderworkerset"
	testingjobspod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

var (
	baseCmpOpts = []cmp.Option{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
	}
)

func TestPodReconciler(t *testing.T) {
	cases := map[string]struct {
		lws     *leaderworkersetv1.LeaderWorkerSet
		pod     *corev1.Pod
		wantPod *corev1.Pod
		wantErr error
	}{
		"should finalize succeeded pod": {
			lws: leaderworkerset.MakeLeaderWorkerSet("lws", "ns").Obj(),
			pod: testingjobspod.MakePod("pod", "ns").
				Label(leaderworkersetv1.SetNameLabelKey, "lws").
				StatusPhase(corev1.PodSucceeded).
				KueueFinalizer().
				Obj(),
			wantPod: testingjobspod.MakePod("pod", "ns").
				Label(leaderworkersetv1.SetNameLabelKey, "lws").
				StatusPhase(corev1.PodSucceeded).
				Obj(),
		},
		"should finalize failed pod": {
			lws: leaderworkerset.MakeLeaderWorkerSet("lws", "ns").Obj(),
			pod: testingjobspod.MakePod("pod", "ns").
				Label(leaderworkersetv1.SetNameLabelKey, "lws").
				StatusPhase(corev1.PodFailed).
				KueueFinalizer().
				Obj(),
			wantPod: testingjobspod.MakePod("pod", "ns").
				Label(leaderworkersetv1.SetNameLabelKey, "lws").
				StatusPhase(corev1.PodFailed).
				Obj(),
		},
		"shouldn't set default values without group index label": {
			lws: leaderworkerset.MakeLeaderWorkerSet("lws", "ns").Obj(),
			pod: testingjobspod.MakePod("pod", "ns").
				Label(leaderworkersetv1.SetNameLabelKey, "lws").
				Annotation(podcontroller.SuspendedByParentAnnotation, FrameworkName).
				Annotation(podcontroller.GroupServingAnnotation, "true").
				Obj(),
			wantPod: testingjobspod.MakePod("pod", "ns").
				Label(leaderworkersetv1.SetNameLabelKey, "lws").
				Annotation(podcontroller.SuspendedByParentAnnotation, FrameworkName).
				Annotation(podcontroller.GroupServingAnnotation, "true").
				Obj(),
		},
		"shouldn't set default values without queue name": {
			lws: leaderworkerset.MakeLeaderWorkerSet("lws", "ns").
				Obj(),
			pod: testingjobspod.MakePod("pod", "ns").
				Label(leaderworkersetv1.SetNameLabelKey, "lws").
				Label(leaderworkersetv1.GroupIndexLabelKey, "0").
				Annotation(podcontroller.SuspendedByParentAnnotation, FrameworkName).
				Annotation(podcontroller.GroupServingAnnotation, "true").
				Obj(),
			wantPod: testingjobspod.MakePod("pod", "ns").
				Label(leaderworkersetv1.SetNameLabelKey, "lws").
				Label(leaderworkersetv1.GroupIndexLabelKey, "0").
				Annotation(podcontroller.SuspendedByParentAnnotation, FrameworkName).
				Annotation(podcontroller.GroupServingAnnotation, "true").
				Obj(),
		},
		"should set default values": {
			lws: leaderworkerset.MakeLeaderWorkerSet("lws", "ns").
				Queue("queue").
				Obj(),
			pod: testingjobspod.MakePod("pod", "ns").
				Label(leaderworkersetv1.SetNameLabelKey, "lws").
				Label(leaderworkersetv1.GroupIndexLabelKey, "0").
				Annotation(podcontroller.SuspendedByParentAnnotation, FrameworkName).
				Annotation(podcontroller.GroupServingAnnotation, "true").
				Obj(),
			wantPod: testingjobspod.MakePod("pod", "ns").
				Label(leaderworkersetv1.SetNameLabelKey, "lws").
				Label(leaderworkersetv1.GroupIndexLabelKey, "0").
				Queue("queue").
				Group("leaderworkerset-lws-0-97565").
				GroupTotalCount("1").
				Annotation(podcontroller.SuspendedByParentAnnotation, FrameworkName).
				Annotation(podcontroller.GroupServingAnnotation, "true").
				Annotation(podcontroller.RoleHashAnnotation, "7aa6c7b8").
				Obj(),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			clientBuilder := utiltesting.NewClientBuilder(leaderworkersetv1.AddToScheme)

			kClient := clientBuilder.WithObjects(tc.lws, tc.pod).Build()

			reconciler := NewPodReconciler(kClient, nil)

			podKey := client.ObjectKeyFromObject(tc.pod)
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: podKey})
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Reconcile returned error (-want,+got):\n%s", diff)
			}

			gotPod := &corev1.Pod{}
			if err := kClient.Get(ctx, podKey, gotPod); err != nil {
				t.Fatalf("Could not get Pod after reconcile: %v", err)
			}

			if diff := cmp.Diff(tc.wantPod, gotPod, baseCmpOpts...); diff != "" {
				t.Errorf("Pod after reconcile (-want,+got):\n%s", diff)
			}
		})
	}
}
