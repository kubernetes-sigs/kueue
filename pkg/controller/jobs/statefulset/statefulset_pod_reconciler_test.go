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

	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingjobspod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	statefulsettesting "sigs.k8s.io/kueue/pkg/util/testingjobs/statefulset"
)

func TestPodReconciler(t *testing.T) {
	now := time.Now()
	wlName := GetWorkloadName(testUID, "sts")
	cases := map[string]struct {
		statefulSet *appsv1.StatefulSet
		pod         *corev1.Pod
		wantPods    []corev1.Pod
		wantErr     error
	}{
		"should label unlabeled pod": {
			statefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID(testUID).
				Queue("lq").
				Obj(),
			pod: testingjobspod.MakePod("pod1", "ns").
				Label("app", "sts-pod").
				OwnerReference("sts", gvk).
				Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				Obj(),
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", "ns").
					Label("app", "sts-pod").
					ManagedByKueueLabel().
					Label(podconstants.GroupNameLabel, wlName).
					Queue("lq").
					OwnerReference("sts", gvk).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					GroupTotalCount("1").
					Obj(),
			},
		},
		"should sync stale queue label": {
			statefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID(testUID).
				Queue("new-queue").
				Obj(),
			pod: testingjobspod.MakePod("pod1", "ns").
				Label("app", "sts-pod").
				ManagedByKueueLabel().
				Label(podconstants.GroupNameLabel, wlName).
				Queue("old-queue").
				OwnerReference("sts", gvk).
				Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				GroupTotalCount("1").
				Obj(),
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", "ns").
					Label("app", "sts-pod").
					ManagedByKueueLabel().
					Label(podconstants.GroupNameLabel, wlName).
					Queue("new-queue").
					OwnerReference("sts", gvk).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					GroupTotalCount("1").
					Obj(),
			},
		},
		"should not update already labeled pod": {
			statefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID(testUID).
				Queue("lq").
				Obj(),
			pod: testingjobspod.MakePod("pod1", "ns").
				Label("app", "sts-pod").
				ManagedByKueueLabel().
				Label(podconstants.GroupNameLabel, wlName).
				Queue("lq").
				OwnerReference("sts", gvk).
				Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				GroupTotalCount("1").
				Obj(),
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", "ns").
					Label("app", "sts-pod").
					ManagedByKueueLabel().
					Label(podconstants.GroupNameLabel, wlName).
					Queue("lq").
					OwnerReference("sts", gvk).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					GroupTotalCount("1").
					Obj(),
			},
		},
		"should ungate old revision pod during rolling update": {
			statefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID(testUID).
				Queue("lq").
				CurrentRevision("1").
				UpdateRevision("2").
				Obj(),
			pod: testingjobspod.MakePod("pod1", "ns").
				Label("app", "sts-pod").
				Label(podconstants.GroupNameLabel, wlName).
				Label(appsv1.ControllerRevisionHashLabelKey, "1").
				ManagedByKueueLabel().
				Queue("lq").
				OwnerReference("sts", gvk).
				Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				Gate(podconstants.SchedulingGateName).
				KueueFinalizer().
				GroupTotalCount("1").
				Obj(),
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", "ns").
					Label("app", "sts-pod").
					Label(podconstants.GroupNameLabel, wlName).
					Label(appsv1.ControllerRevisionHashLabelKey, "1").
					ManagedByKueueLabel().
					Queue("lq").
					OwnerReference("sts", gvk).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					GroupTotalCount("1").
					Obj(),
			},
		},
		"should not ungate new revision pod during rolling update": {
			statefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID(testUID).
				Queue("lq").
				CurrentRevision("1").
				UpdateRevision("2").
				Obj(),
			pod: testingjobspod.MakePod("pod1", "ns").
				Label("app", "sts-pod").
				Label(podconstants.GroupNameLabel, wlName).
				Label(appsv1.ControllerRevisionHashLabelKey, "2").
				ManagedByKueueLabel().
				Queue("lq").
				OwnerReference("sts", gvk).
				Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				Gate(podconstants.SchedulingGateName).
				KueueFinalizer().
				GroupTotalCount("1").
				Obj(),
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", "ns").
					Label("app", "sts-pod").
					Label(podconstants.GroupNameLabel, wlName).
					Label(appsv1.ControllerRevisionHashLabelKey, "2").
					ManagedByKueueLabel().
					Queue("lq").
					OwnerReference("sts", gvk).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Gate(podconstants.SchedulingGateName).
					KueueFinalizer().
					GroupTotalCount("1").
					Obj(),
			},
		},
		"should finalize terminated pod": {
			statefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID(testUID).
				Queue("lq").
				Obj(),
			pod: testingjobspod.MakePod("pod1", "ns").
				Label("app", "sts-pod").
				Label(podconstants.GroupNameLabel, wlName).
				ManagedByKueueLabel().
				OwnerReference("sts", gvk).
				Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				KueueFinalizer().
				StatusPhase(corev1.PodSucceeded).
				Obj(),
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", "ns").
					Label("app", "sts-pod").
					Label(podconstants.GroupNameLabel, wlName).
					ManagedByKueueLabel().
					OwnerReference("sts", gvk).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					StatusPhase(corev1.PodSucceeded).
					Obj(),
			},
		},
		"should finalize deleted pod": {
			statefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID(testUID).
				Queue("lq").
				Obj(),
			pod: testingjobspod.MakePod("pod1", "ns").
				Label("app", "sts-pod").
				Label(podconstants.GroupNameLabel, wlName).
				ManagedByKueueLabel().
				OwnerReference("sts", gvk).
				Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				KueueFinalizer().
				DeletionTimestamp(now).
				Obj(),
			wantPods: nil,
		},
		"should ungate and finalize orphan pod (sts deleted)": {
			pod: testingjobspod.MakePod("pod1", "ns").
				Label("app", "sts-pod").
				Label(podconstants.GroupNameLabel, wlName).
				ManagedByKueueLabel().
				OwnerReference("sts", gvk).
				Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				Gate(podconstants.SchedulingGateName).
				KueueFinalizer().
				StatusPhase(corev1.PodSucceeded).
				Obj(),
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", "ns").
					Label("app", "sts-pod").
					Label(podconstants.GroupNameLabel, wlName).
					ManagedByKueueLabel().
					OwnerReference("sts", gvk).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					StatusPhase(corev1.PodSucceeded).
					Obj(),
			},
		},
		"should finalize orphan pod without ownerRef (terminated)": {
			pod: testingjobspod.MakePod("sts-0", "ns").
				ManagedByKueueLabel().
				Label(podconstants.GroupNameLabel, wlName).
				Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				KueueFinalizer().
				StatusPhase(corev1.PodSucceeded).
				Obj(),
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("sts-0", "ns").
					ManagedByKueueLabel().
					Label(podconstants.GroupNameLabel, wlName).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					StatusPhase(corev1.PodSucceeded).
					Obj(),
			},
		},
		"should finalize orphan pod without ownerRef (failed)": {
			pod: testingjobspod.MakePod("sts-1", "ns").
				ManagedByKueueLabel().
				Label(podconstants.GroupNameLabel, wlName).
				Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				KueueFinalizer().
				StatusPhase(corev1.PodFailed).
				Obj(),
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("sts-1", "ns").
					ManagedByKueueLabel().
					Label(podconstants.GroupNameLabel, wlName).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					StatusPhase(corev1.PodFailed).
					Obj(),
			},
		},
		"should ungate and finalize orphan pod without ownerRef (running)": {
			pod: testingjobspod.MakePod("sts-0", "ns").
				ManagedByKueueLabel().
				Label(podconstants.GroupNameLabel, wlName).
				Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				Gate(podconstants.SchedulingGateName).
				KueueFinalizer().
				Obj(),
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("sts-0", "ns").
					ManagedByKueueLabel().
					Label(podconstants.GroupNameLabel, wlName).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Obj(),
			},
		},
		"should not finalize running pod when sts exists with same revision": {
			statefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID(testUID).
				Replicas(0).
				Queue("lq").
				Obj(),
			pod: testingjobspod.MakePod("pod1", "ns").
				Label("app", "sts-pod").
				Label(podconstants.GroupNameLabel, wlName).
				ManagedByKueueLabel().
				Queue("lq").
				OwnerReference("sts", gvk).
				Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				KueueFinalizer().
				GroupTotalCount("0").
				Obj(),
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", "ns").
					Label("app", "sts-pod").
					Label(podconstants.GroupNameLabel, wlName).
					ManagedByKueueLabel().
					Queue("lq").
					OwnerReference("sts", gvk).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					KueueFinalizer().
					GroupTotalCount("0").
					Obj(),
			},
		},
		"should finalize old revision pod without gate during rolling update": {
			statefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID(testUID).
				Queue("lq").
				CurrentRevision("1").
				UpdateRevision("2").
				Obj(),
			pod: testingjobspod.MakePod("pod2", "ns").
				Label("app", "sts-pod").
				Label(podconstants.GroupNameLabel, wlName).
				Label(appsv1.ControllerRevisionHashLabelKey, "1").
				ManagedByKueueLabel().
				Queue("lq").
				OwnerReference("sts", gvk).
				Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				KueueFinalizer().
				GroupTotalCount("1").
				Obj(),
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod2", "ns").
					Label("app", "sts-pod").
					Label(podconstants.GroupNameLabel, wlName).
					Label(appsv1.ControllerRevisionHashLabelKey, "1").
					ManagedByKueueLabel().
					Queue("lq").
					OwnerReference("sts", gvk).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					GroupTotalCount("1").
					Obj(),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			clientBuilder := utiltesting.NewClientBuilder()

			objs := []client.Object{tc.pod}
			if tc.statefulSet != nil {
				objs = append(objs, tc.statefulSet)
			}

			kClient := clientBuilder.WithObjects(objs...).Build()

			reconciler, err := NewPodReconciler(ctx, kClient, nil, nil)
			if err != nil {
				t.Fatalf("Error creating the reconciler: %v", err)
			}

			podKey := client.ObjectKeyFromObject(tc.pod)
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: podKey})
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Reconcile returned error (-want,+got):\n%s", diff)
			}

			gotPods := &corev1.PodList{}
			if err := kClient.List(ctx, gotPods, client.InNamespace("ns")); err != nil {
				t.Fatalf("Could not list Pods after reconcile: %v", err)
			}

			if diff := cmp.Diff(tc.wantPods, gotPods.Items, baseCmpOpts...); diff != "" {
				t.Errorf("Pods after reconcile (-want,+got):\n%s", diff)
			}
		})
	}
}
