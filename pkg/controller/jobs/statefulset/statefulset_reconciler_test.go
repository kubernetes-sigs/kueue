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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjobspod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	statefulsettesting "sigs.k8s.io/kueue/pkg/util/testingjobs/statefulset"
)

var (
	baseCmpOpts = cmp.Options{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
	}
	testUID = types.UID("test-uid")
)

func TestReconciler(t *testing.T) {
	now := time.Now()
	cases := map[string]struct {
		stsKey          client.ObjectKey
		statefulSet     *appsv1.StatefulSet
		pods            []corev1.Pod
		workloads       []kueue.Workload
		wantStatefulSet *appsv1.StatefulSet
		wantPods        []corev1.Pod
		wantWorkloads   []kueue.Workload
		wantErr         error
	}{
		"statefulset not found": {
			stsKey: client.ObjectKey{Name: "sts", Namespace: "ns"},
			pods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", "ns").
					Label(podconstants.GroupNameLabel, GetWorkloadName(testUID, "sts")).
					ManagedByKueueLabel().
					OwnerReference("sts", gvk).
					KueueFinalizer().
					StatusPhase(corev1.PodSucceeded).
					Obj(),
				*testingjobspod.MakePod("pod2", "ns").
					Label(podconstants.GroupNameLabel, GetWorkloadName(testUID, "sts")).
					ManagedByKueueLabel().
					OwnerReference("sts", gvk).
					KueueFinalizer().
					StatusPhase(corev1.PodFailed).
					Obj(),
				*testingjobspod.MakePod("pod3", "ns").
					Label(podconstants.GroupNameLabel, GetWorkloadName(testUID, "sts")).
					ManagedByKueueLabel().
					OwnerReference("sts", gvk).
					KueueFinalizer().
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", "ns").
					Label(podconstants.GroupNameLabel, GetWorkloadName(testUID, "sts")).
					ManagedByKueueLabel().
					OwnerReference("sts", gvk).
					StatusPhase(corev1.PodSucceeded).
					Obj(),
				*testingjobspod.MakePod("pod2", "ns").
					Label(podconstants.GroupNameLabel, GetWorkloadName(testUID, "sts")).
					ManagedByKueueLabel().
					OwnerReference("sts", gvk).
					StatusPhase(corev1.PodFailed).
					Obj(),
				*testingjobspod.MakePod("pod3", "ns").
					Label(podconstants.GroupNameLabel, GetWorkloadName(testUID, "sts")).
					ManagedByKueueLabel().
					OwnerReference("sts", gvk).
					Obj(),
			},
		},
		"statefulset with finished pods": {
			stsKey: client.ObjectKey{Name: "sts", Namespace: "ns"},
			statefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				Replicas(0).
				Queue("lq").
				UID(string(testUID)).
				PodTemplateSpecPodGroupNameLabel("sts", testUID, gvk).
				DeepCopy(),
			wantStatefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				Replicas(0).
				Queue("lq").
				UID(string(testUID)).
				PodTemplateSpecPodGroupNameLabel("sts", testUID, gvk).
				DeepCopy(),
			pods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", "ns").
					ManagedByKueueLabel().
					Queue("lq").
					OwnerReference("sts", gvk).
					KueueFinalizer().
					StatusPhase(corev1.PodSucceeded).
					Obj(),
				*testingjobspod.MakePod("pod2", "ns").
					ManagedByKueueLabel().
					Queue("lq").
					OwnerReference("sts", gvk).
					KueueFinalizer().
					StatusPhase(corev1.PodFailed).
					Obj(),
				*testingjobspod.MakePod("pod3", "ns").
					ManagedByKueueLabel().
					Queue("lq").
					OwnerReference("sts", gvk).
					KueueFinalizer().
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", "ns").
					Label(podconstants.GroupNameLabel, GetWorkloadName(testUID, "sts")).
					ManagedByKueueLabel().
					PrebuiltWorkload(GetWorkloadName(testUID, "sts")).
					Queue("lq").
					OwnerReference("sts", gvk).
					StatusPhase(corev1.PodSucceeded).
					Obj(),
				*testingjobspod.MakePod("pod2", "ns").
					Label(podconstants.GroupNameLabel, GetWorkloadName(testUID, "sts")).
					ManagedByKueueLabel().
					PrebuiltWorkload(GetWorkloadName(testUID, "sts")).
					Queue("lq").
					OwnerReference("sts", gvk).
					StatusPhase(corev1.PodFailed).
					Obj(),
				*testingjobspod.MakePod("pod3", "ns").
					Label(podconstants.GroupNameLabel, GetWorkloadName(testUID, "sts")).
					ManagedByKueueLabel().
					PrebuiltWorkload(GetWorkloadName(testUID, "sts")).
					Queue("lq").
					OwnerReference("sts", gvk).
					KueueFinalizer().
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testUID, "sts"), "ns").
					PodSets(*utiltestingapi.MakePodSet("main", 0).Image("pause").RestartPolicy(corev1.RestartPolicy("")).Obj()).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Queue("lq").
					Priority(0).
					Finalizers(kueue.ResourceInUseFinalizerName).
					OwnerReference(gvk, "sts", string(testUID)).
					Obj(),
			},
		},
		"statefulset with update revision": {
			stsKey: client.ObjectKey{Name: "sts", Namespace: "ns"},
			statefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				Queue("lq").
				CurrentRevision("1").
				UpdateRevision("2").
				UID(string(testUID)).
				PodTemplateSpecPodGroupNameLabel("sts", testUID, gvk).
				DeepCopy(),
			wantStatefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				Queue("lq").
				CurrentRevision("1").
				UpdateRevision("2").
				UID(string(testUID)).
				PodTemplateSpecPodGroupNameLabel("sts", testUID, gvk).
				DeepCopy(),
			pods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", "ns").
					ManagedByKueueLabel().
					Queue("lq").
					OwnerReference("sts", gvk).
					Label(appsv1.ControllerRevisionHashLabelKey, "1").
					Gate(podconstants.SchedulingGateName).
					KueueFinalizer().
					Obj(),
				*testingjobspod.MakePod("pod2", "ns").
					ManagedByKueueLabel().
					Queue("lq").
					OwnerReference("sts", gvk).
					Label(appsv1.ControllerRevisionHashLabelKey, "1").
					KueueFinalizer().
					Obj(),
				*testingjobspod.MakePod("pod3", "ns").
					ManagedByKueueLabel().
					Queue("lq").
					OwnerReference("sts", gvk).
					Label(appsv1.ControllerRevisionHashLabelKey, "2").
					Gate(podconstants.SchedulingGateName).
					KueueFinalizer().
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", "ns").
					Label(podconstants.GroupNameLabel, GetWorkloadName(testUID, "sts")).
					ManagedByKueueLabel().
					PrebuiltWorkload(GetWorkloadName(testUID, "sts")).
					Queue("lq").
					OwnerReference("sts", gvk).
					Label(appsv1.ControllerRevisionHashLabelKey, "1").
					Obj(),
				*testingjobspod.MakePod("pod2", "ns").
					Label(podconstants.GroupNameLabel, GetWorkloadName(testUID, "sts")).
					ManagedByKueueLabel().
					PrebuiltWorkload(GetWorkloadName(testUID, "sts")).
					Queue("lq").
					OwnerReference("sts", gvk).
					Label(appsv1.ControllerRevisionHashLabelKey, "1").
					Obj(),
				*testingjobspod.MakePod("pod3", "ns").
					Label(podconstants.GroupNameLabel, GetWorkloadName(testUID, "sts")).
					ManagedByKueueLabel().
					PrebuiltWorkload(GetWorkloadName(testUID, "sts")).
					Queue("lq").
					OwnerReference("sts", gvk).
					Label(appsv1.ControllerRevisionHashLabelKey, "2").
					Gate(podconstants.SchedulingGateName).
					KueueFinalizer().
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testUID, "sts"), "ns").
					PodSets(*utiltestingapi.MakePodSet("main", 1).Image("pause").RestartPolicy(corev1.RestartPolicy("")).Obj()).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Queue("lq").
					Priority(0).
					Finalizers(kueue.ResourceInUseFinalizerName).
					OwnerReference(gvk, "sts", string(testUID)).
					Obj(),
			},
		},
		"should add StatefulSet to Workload owner references if replicas > 0": {
			stsKey: client.ObjectKey{Name: "sts", Namespace: "ns"},
			statefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID(string(testUID)).
				Queue("lq").
				PodTemplateSpecPodGroupNameLabel("sts", testUID, gvk).
				Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testUID, "sts"), "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Obj(),
			},
			wantStatefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID(string(testUID)).
				Queue("lq").
				PodTemplateSpecPodGroupNameLabel("sts", testUID, gvk).
				DeepCopy(),
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testUID, "sts"), "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					OwnerReference(gvk, "sts", string(testUID)).
					Obj(),
			},
		},
		"shouldn't add StatefulSet to Workload owner references if replicas = 0": {
			stsKey: client.ObjectKey{Name: "sts", Namespace: "ns"},
			statefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				Replicas(0).
				UID(string(testUID)).
				Queue("lq").
				PodTemplateSpecPodGroupNameLabel("sts", testUID, gvk).
				Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testUID, "sts"), "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Obj(),
			},
			wantStatefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				Replicas(0).
				UID(string(testUID)).
				Queue("lq").
				PodTemplateSpecPodGroupNameLabel("sts", testUID, gvk).
				DeepCopy(),
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testUID, "sts"), "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Obj(),
			},
		},
		"should remove StatefulSet from Workload owner references if replicas = 0": {
			stsKey: client.ObjectKey{Name: "sts", Namespace: "ns"},
			statefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID(string(testUID)).
				Queue("lq").
				Replicas(0).
				PodTemplateSpecPodGroupNameLabel("sts", testUID, gvk).
				Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testUID, "sts"), "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					OwnerReference(gvk, "sts", string(testUID)).
					Obj(),
			},
			wantStatefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				UID(string(testUID)).
				Queue("lq").
				Replicas(0).
				PodTemplateSpecPodGroupNameLabel("sts", testUID, gvk).
				DeepCopy(),
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testUID, "sts"), "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Obj(),
			},
		},
		"should finalize deleted pod": {
			stsKey: client.ObjectKey{Name: "sts", Namespace: "ns"},
			statefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				Replicas(0).
				Queue("lq").
				UID(string(testUID)).
				PodTemplateSpecPodGroupNameLabel("sts", testUID, gvk).
				DeepCopy(),
			wantStatefulSet: statefulsettesting.MakeStatefulSet("sts", "ns").
				Replicas(0).
				Queue("lq").
				UID(string(testUID)).
				PodTemplateSpecPodGroupNameLabel("sts", testUID, gvk).
				DeepCopy(),
			pods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", "ns").
					Label(podconstants.GroupNameLabel, GetWorkloadName(testUID, "sts")).
					ManagedByKueueLabel().
					Queue("lq").
					OwnerReference("sts", gvk).
					KueueFinalizer().
					DeletionTimestamp(now).
					Obj(),
			},
			wantPods: nil,
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadName(testUID, "sts"), "ns").
					PodSets(*utiltestingapi.MakePodSet("main", 0).Image("pause").RestartPolicy(corev1.RestartPolicy("")).Obj()).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Queue("lq").
					Priority(0).
					Finalizers(kueue.ResourceInUseFinalizerName).
					OwnerReference(gvk, "sts", string(testUID)).
					Obj(),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			clientBuilder := utiltesting.NewClientBuilder()
			indexer := utiltesting.AsIndexer(clientBuilder)

			objs := make([]client.Object, 0, len(tc.pods)+len(tc.workloads)+1)
			if tc.statefulSet != nil {
				objs = append(objs, tc.statefulSet)
			}

			for _, p := range tc.pods {
				objs = append(objs, p.DeepCopy())
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
				t.Fatalf("Could not get StatefuleSet after reconcile: %v", err)
			}
			if err != nil {
				gotStatefulSet = nil
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
			want: true,
		},
		"statefulset": {
			obj:  statefulsettesting.MakeStatefulSet("sts", metav1.NamespaceDefault),
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
