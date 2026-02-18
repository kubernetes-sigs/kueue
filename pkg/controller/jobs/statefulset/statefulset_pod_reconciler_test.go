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
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingjobspod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	statefulsettesting "sigs.k8s.io/kueue/pkg/util/testingjobs/statefulset"
)

func TestPodReconciler(t *testing.T) {
	now := time.Now()
	cases := map[string]struct {
		manageJobsWithoutQueueName bool
		sts                        *appsv1.StatefulSet
		pod                        *corev1.Pod
		wantPods                   []corev1.Pod
		wantErr                    error
	}{
		"should finalize succeeded pod": {
			sts: statefulsettesting.MakeStatefulSet("sts", "ns").Obj(),
			pod: testingjobspod.MakePod("pod", "ns").
				OwnerReference("sts", gvk).
				StatusPhase(corev1.PodSucceeded).
				KueueFinalizer().
				Obj(),
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod", "ns").
					OwnerReference("sts", gvk).
					StatusPhase(corev1.PodSucceeded).
					Obj(),
			},
		},
		"should finalize failed pod": {
			sts: statefulsettesting.MakeStatefulSet("sts", "ns").Obj(),
			pod: testingjobspod.MakePod("pod", "ns").
				OwnerReference("sts", gvk).
				StatusPhase(corev1.PodFailed).
				KueueFinalizer().
				Obj(),
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod", "ns").
					OwnerReference("sts", gvk).
					StatusPhase(corev1.PodFailed).
					Obj(),
			},
		},
		"should finalize deleted pod": {
			sts: statefulsettesting.MakeStatefulSet("sts", "ns").Obj(),
			pod: testingjobspod.MakePod("pod", "ns").
				OwnerReference("sts", gvk).
				DeletionTimestamp(now).
				KueueFinalizer().
				Obj(),
		},
		"shouldn't set default values without controller reference": {
			sts: statefulsettesting.MakeStatefulSet("sts", "ns").Obj(),
			pod: testingjobspod.MakePod("pod", "ns").
				Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				Obj(),
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod", "ns").
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Obj(),
			},
		},
		"shouldn't set default values without queue name": {
			sts: statefulsettesting.MakeStatefulSet("sts", "ns").Obj(),
			pod: testingjobspod.MakePod("pod", "ns").
				OwnerReference("sts", gvk).
				Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				Obj(),
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod", "ns").
					OwnerReference("sts", gvk).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Obj(),
			},
		},
		"should set default values": {
			sts: statefulsettesting.MakeStatefulSet("sts", "ns").
				Queue("queue").
				Replicas(3).
				Obj(),
			pod: testingjobspod.MakePod("pod", "ns").
				OwnerReference("sts", gvk).
				Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				Obj(),
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod", "ns").
					OwnerReference("sts", gvk).
					Queue("queue").
					ManagedByKueueLabel().
					Group(GetWorkloadName("sts")).
					GroupTotalCount("3").
					PrebuiltWorkload(GetWorkloadName("sts")).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupFastAdmissionAnnotationKey, podconstants.GroupFastAdmissionAnnotationValue).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Annotation(kueue.PodGroupPodIndexLabelAnnotation, appsv1.PodIndexLabel).
					Annotation(podconstants.RoleHashAnnotation, string(kueue.DefaultPodSetName)).
					Obj(),
			},
		},
		"should set default values with priority class": {
			sts: statefulsettesting.MakeStatefulSet("sts", "ns").
				Queue("queue").
				Replicas(3).
				WorkloadPriorityClass("high-priority").
				Obj(),
			pod: testingjobspod.MakePod("pod", "ns").
				OwnerReference("sts", gvk).
				Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				Obj(),
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod", "ns").
					OwnerReference("sts", gvk).
					Queue("queue").
					ManagedByKueueLabel().
					Group(GetWorkloadName("sts")).
					GroupTotalCount("3").
					Label(controllerconstants.WorkloadPriorityClassLabel, "high-priority").
					PrebuiltWorkload(GetWorkloadName("sts")).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupFastAdmissionAnnotationKey, podconstants.GroupFastAdmissionAnnotationValue).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Annotation(kueue.PodGroupPodIndexLabelAnnotation, appsv1.PodIndexLabel).
					Annotation(podconstants.RoleHashAnnotation, string(kueue.DefaultPodSetName)).
					Obj(),
			},
		},
		"shouldn't update pod if already labeled": {
			sts: statefulsettesting.MakeStatefulSet("sts", "ns").
				Queue("queue").
				Replicas(3).
				Obj(),
			pod: testingjobspod.MakePod("pod", "ns").
				OwnerReference("sts", gvk).
				Queue("queue").
				ManagedByKueueLabel().
				Group(GetWorkloadName("sts")).
				GroupTotalCount("3").
				Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				Obj(),
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod", "ns").
					OwnerReference("sts", gvk).
					Queue("queue").
					ManagedByKueueLabel().
					Group(GetWorkloadName("sts")).
					GroupTotalCount("3").
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Obj(),
			},
		},
		"should sync queue label on already labeled pod": {
			sts: statefulsettesting.MakeStatefulSet("sts", "ns").
				Queue("new-queue").
				Replicas(3).
				Obj(),
			pod: testingjobspod.MakePod("pod", "ns").
				OwnerReference("sts", gvk).
				Queue("old-queue").
				ManagedByKueueLabel().
				Group(GetWorkloadName("sts")).
				GroupTotalCount("3").
				Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				Obj(),
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod", "ns").
					OwnerReference("sts", gvk).
					Queue("new-queue").
					ManagedByKueueLabel().
					Group(GetWorkloadName("sts")).
					GroupTotalCount("3").
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Obj(),
			},
		},
		"should set default values without queue name when manageJobsWithoutQueueName": {
			manageJobsWithoutQueueName: true,
			sts: statefulsettesting.MakeStatefulSet("sts", "ns").
				Replicas(3).
				Obj(),
			pod: testingjobspod.MakePod("pod", "ns").
				OwnerReference("sts", gvk).
				Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				Obj(),
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod", "ns").
					OwnerReference("sts", gvk).
					ManagedByKueueLabel().
					Group(GetWorkloadName("sts")).
					GroupTotalCount("3").
					PrebuiltWorkload(GetWorkloadName("sts")).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Annotation(podconstants.GroupFastAdmissionAnnotationKey, podconstants.GroupFastAdmissionAnnotationValue).
					Annotation(podconstants.GroupServingAnnotationKey, podconstants.GroupServingAnnotationValue).
					Annotation(kueue.PodGroupPodIndexLabelAnnotation, appsv1.PodIndexLabel).
					Annotation(podconstants.RoleHashAnnotation, string(kueue.DefaultPodSetName)).
					Obj(),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			clientBuilder := utiltesting.NewClientBuilder()

			kClient := clientBuilder.WithObjects(tc.sts, tc.pod).Build()

			var opts []jobframework.Option
			if tc.manageJobsWithoutQueueName {
				opts = append(opts, jobframework.WithManageJobsWithoutQueueName(true))
			}
			reconciler, err := NewPodReconciler(ctx, kClient, nil, nil, opts...)
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
