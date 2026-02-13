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

package leaderworkerset

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/util/testingjobs/leaderworkerset"
	testingjobspod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

func TestPodReconciler(t *testing.T) {
	now := time.Now()
	cases := map[string]struct {
		lws      *leaderworkersetv1.LeaderWorkerSet
		pod      *corev1.Pod
		wantPods []corev1.Pod
		wantErr  error
	}{
		"should finalize succeeded pod": {
			lws: leaderworkerset.MakeLeaderWorkerSet("lws", "ns").Obj(),
			pod: testingjobspod.MakePod("pod", "ns").
				Label(leaderworkersetv1.SetNameLabelKey, "lws").
				StatusPhase(corev1.PodSucceeded).
				KueueFinalizer().
				Obj(),
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod", "ns").
					Label(leaderworkersetv1.SetNameLabelKey, "lws").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
			},
		},
		"should finalize failed pod": {
			lws: leaderworkerset.MakeLeaderWorkerSet("lws", "ns").Obj(),
			pod: testingjobspod.MakePod("pod", "ns").
				Label(leaderworkersetv1.SetNameLabelKey, "lws").
				StatusPhase(corev1.PodFailed).
				KueueFinalizer().
				Obj(),
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod", "ns").
					Label(leaderworkersetv1.SetNameLabelKey, "lws").
					StatusPhase(corev1.PodFailed).
					Obj(),
			},
		},
		"shouldn't set default values without group index label": {
			lws: leaderworkerset.MakeLeaderWorkerSet("lws", "ns").Obj(),
			pod: testingjobspod.MakePod("pod", "ns").
				Label(leaderworkersetv1.SetNameLabelKey, "lws").
				Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
				Obj(),
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod", "ns").
					Label(leaderworkersetv1.SetNameLabelKey, "lws").
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Obj(),
			},
		},
		"shouldn't set default values without queue name": {
			lws: leaderworkerset.MakeLeaderWorkerSet("lws", "ns").
				Obj(),
			pod: testingjobspod.MakePod("pod", "ns").
				Label(leaderworkersetv1.SetNameLabelKey, "lws").
				Label(leaderworkersetv1.GroupIndexLabelKey, "0").
				Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
				Obj(),
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod", "ns").
					Label(leaderworkersetv1.SetNameLabelKey, "lws").
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Obj(),
			},
		},
		// Leader pod doesn't have a leaderworkerset.sigs.k8s.io/leader-name annotation.
		"should set default values (worker template, leader pod)": {
			lws: leaderworkerset.MakeLeaderWorkerSet("lws", "ns").
				UID(testUID).
				Queue("queue").
				Obj(),
			pod: testingjobspod.MakePod("pod", "ns").
				Label(leaderworkersetv1.SetNameLabelKey, "lws").
				Label(leaderworkersetv1.GroupIndexLabelKey, "0").
				Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
				Obj(),
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod", "ns").
					Label(leaderworkersetv1.SetNameLabelKey, "lws").
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					Queue("queue").
					ManagedByKueueLabel().
					Group(GetWorkloadName(types.UID(testUID), "lws", "0")).
					GroupTotalCount("1").
					PrebuiltWorkload(GetWorkloadName(types.UID(testUID), "lws", "0")).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Annotation(podconstants.RoleHashAnnotation, string(kueue.DefaultPodSetName)).
					Obj(),
			},
		},
		// Worker pod has a leaderworkerset.sigs.k8s.io/leader-name annotation.
		"should set default values (worker template, worker pod)": {
			lws: leaderworkerset.MakeLeaderWorkerSet("lws", "ns").
				UID(testUID).
				Queue("queue").
				Obj(),
			pod: testingjobspod.MakePod("pod", "ns").
				Label(leaderworkersetv1.SetNameLabelKey, "lws").
				Label(leaderworkersetv1.GroupIndexLabelKey, "0").
				Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
				Annotation(leaderworkersetv1.LeaderPodNameAnnotationKey, "lws-0").
				Obj(),
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod", "ns").
					Label(leaderworkersetv1.SetNameLabelKey, "lws").
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					Queue("queue").
					ManagedByKueueLabel().
					Group(GetWorkloadName(types.UID(testUID), "lws", "0")).
					GroupTotalCount("1").
					PrebuiltWorkload(GetWorkloadName(types.UID(testUID), "lws", "0")).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Annotation(leaderworkersetv1.LeaderPodNameAnnotationKey, "lws-0").
					Annotation(podconstants.RoleHashAnnotation, string(kueue.DefaultPodSetName)).
					Obj(),
			},
		},
		// Leader pod doesn't have a leaderworkerset.sigs.k8s.io/leader-name annotation.
		"should set default values (leader+worker template, leader pod)": {
			lws: leaderworkerset.MakeLeaderWorkerSet("lws", "ns").
				UID(testUID).
				Queue("queue").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Obj(),
			pod: testingjobspod.MakePod("pod", "ns").
				Label(leaderworkersetv1.SetNameLabelKey, "lws").
				Label(leaderworkersetv1.GroupIndexLabelKey, "0").
				Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
				Obj(),
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod", "ns").
					Label(leaderworkersetv1.SetNameLabelKey, "lws").
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					Queue("queue").
					ManagedByKueueLabel().
					Group(GetWorkloadName(types.UID(testUID), "lws", "0")).
					GroupTotalCount("1").
					PrebuiltWorkload(GetWorkloadName(types.UID(testUID), "lws", "0")).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Annotation(podconstants.RoleHashAnnotation, leaderPodSetName).
					Obj(),
			},
		},
		// Worker pod has a leaderworkerset.sigs.k8s.io/leader-name annotation.
		"should set default values (leader+worker template, worker pod)": {
			lws: leaderworkerset.MakeLeaderWorkerSet("lws", "ns").
				UID(testUID).
				Queue("queue").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Obj(),
			pod: testingjobspod.MakePod("pod", "ns").
				Label(leaderworkersetv1.SetNameLabelKey, "lws").
				Label(leaderworkersetv1.GroupIndexLabelKey, "0").
				Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
				Annotation(leaderworkersetv1.LeaderPodNameAnnotationKey, "lws-0").
				Obj(),
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod", "ns").
					Label(leaderworkersetv1.SetNameLabelKey, "lws").
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					Queue("queue").
					ManagedByKueueLabel().
					Group(GetWorkloadName(types.UID(testUID), "lws", "0")).
					GroupTotalCount("1").
					PrebuiltWorkload(GetWorkloadName(types.UID(testUID), "lws", "0")).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Annotation(leaderworkersetv1.LeaderPodNameAnnotationKey, "lws-0").
					Annotation(podconstants.RoleHashAnnotation, workerPodSetName).
					Obj(),
			},
		},
		"should finalize deleted pod": {
			lws: leaderworkerset.MakeLeaderWorkerSet("lws", "ns").Obj(),
			pod: testingjobspod.MakePod("pod", "ns").
				Label(leaderworkersetv1.SetNameLabelKey, "lws").
				DeletionTimestamp(now).
				KueueFinalizer().
				Obj(),
		},
		"should set default values using origin UID in MultiKueue scenario": {
			lws: leaderworkerset.MakeLeaderWorkerSet("lws", "ns").
				UID("worker-uid").
				Queue("queue").
				Label(kueue.MultiKueueOriginLabel, "origin1").
				Annotation(kueue.MultiKueueOriginUIDAnnotation, "origin-uid").
				Obj(),
			pod: testingjobspod.MakePod("pod", "ns").
				Label(leaderworkersetv1.SetNameLabelKey, "lws").
				Label(leaderworkersetv1.GroupIndexLabelKey, "0").
				Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
				Obj(),
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod", "ns").
					Label(leaderworkersetv1.SetNameLabelKey, "lws").
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					Queue("queue").
					ManagedByKueueLabel().
					Group(GetWorkloadName("origin-uid", "lws", "0")).
					GroupTotalCount("1").
					PrebuiltWorkload(GetWorkloadName("origin-uid", "lws", "0")).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Annotation(podconstants.RoleHashAnnotation, string(kueue.DefaultPodSetName)).
					Obj(),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			clientBuilder := utiltesting.NewClientBuilder(leaderworkersetv1.AddToScheme)

			kClient := clientBuilder.WithObjects(tc.lws, tc.pod).Build()

			reconciler, err := NewPodReconciler(ctx, kClient, nil, nil)
			if err != nil {
				t.Errorf("Error creating the reconciler: %v", err)
			}

			podKey := client.ObjectKeyFromObject(tc.pod)
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: podKey})
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Reconcile returned error (-want,+got):\n%s", diff)
			}

			gotPods := &corev1.PodList{}
			if err := kClient.List(ctx, gotPods, client.InNamespace(tc.pod.Namespace)); err != nil {
				t.Fatalf("Could not list Pods after reconcile: %v", err)
			}

			if diff := cmp.Diff(tc.wantPods, gotPods.Items, baseCmpOpts...); diff != "" {
				t.Errorf("Pods after reconcile (-want,+got):\n%s", diff)
			}
		})
	}
}
