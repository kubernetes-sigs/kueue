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

package raycluster

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	rayclusterapi "github.com/ray-project/kuberay/ray-operator/apis/ray/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/podset"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingrayutil "sigs.k8s.io/kueue/pkg/util/testingjobs/raycluster"
)

func TestPodSets(t *testing.T) {
	job := testingrayutil.MakeJob("job", "ns").
		WithHeadGroupSpec(
			rayclusterapi.HeadGroupSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name: "head_c",
							},
						},
					},
				},
			},
		).
		WithWorkerGroups(
			rayclusterapi.WorkerGroupSpec{
				GroupName: "group1",
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name: "group1_c",
							},
						},
					},
				},
			},
			rayclusterapi.WorkerGroupSpec{
				GroupName: "group2",
				Replicas:  ptr.To[int32](3),
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name: "group2_c",
							},
						},
					},
				},
			},
		).
		Obj()

	wantPodSets := []kueue.PodSet{
		{
			Name:  "head",
			Count: 1,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: "head_c",
						},
					},
				},
			},
		},
		{
			Name:  "group1",
			Count: 1,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: "group1_c",
						},
					},
				},
			},
		},
		{
			Name:  "group2",
			Count: 3,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: "group2_c",
						},
					},
				},
			},
		},
	}

	result := ((*RayCluster)(job)).PodSets()

	if diff := cmp.Diff(wantPodSets, result); diff != "" {
		t.Errorf("PodSets() mismatch (-want +got):\n%s", diff)
	}
}

func TestNodeSelectors(t *testing.T) {
	baseJob := testingrayutil.MakeJob("job", "ns").
		WithHeadGroupSpec(rayclusterapi.HeadGroupSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					NodeSelector: map[string]string{},
				},
			},
		}).
		WithWorkerGroups(rayclusterapi.WorkerGroupSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					NodeSelector: map[string]string{
						"key-wg1": "value-wg1",
					},
				},
			},
		}, rayclusterapi.WorkerGroupSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					NodeSelector: map[string]string{
						"key-wg2": "value-wg2",
					},
				},
			},
		}).
		Obj()

	cases := map[string]struct {
		job          *rayclusterapi.RayCluster
		runInfo      []podset.PodSetInfo
		restoreInfo  []podset.PodSetInfo
		wantRunError error
		wantAfterRun *rayclusterapi.RayCluster
		wantFinal    *rayclusterapi.RayCluster
	}{
		"valid configuration": {
			job: baseJob.DeepCopy(),
			runInfo: []podset.PodSetInfo{
				{
					NodeSelector: map[string]string{
						"newKey": "newValue",
					},
				},
				{
					NodeSelector: map[string]string{
						"key-wg1": "value-wg1",
					},
				},
				{
					NodeSelector: map[string]string{
						// don't add anything
					},
				},
			},
			restoreInfo: []podset.PodSetInfo{
				{
					NodeSelector: map[string]string{
						// clean it all
					},
				},
				{
					NodeSelector: map[string]string{
						"key-wg1": "value-wg1",
					},
				},
				{
					NodeSelector: map[string]string{
						"key-wg2": "value-wg2",
					},
				},
			},
			wantAfterRun: testingrayutil.MakeJob("job", "ns").
				Suspend(false).
				WithHeadGroupSpec(rayclusterapi.HeadGroupSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							NodeSelector: map[string]string{
								"newKey": "newValue",
							},
						},
					},
				}).
				WithWorkerGroups(rayclusterapi.WorkerGroupSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							NodeSelector: map[string]string{
								"key-wg1": "value-wg1",
							},
						},
					},
				}, rayclusterapi.WorkerGroupSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							NodeSelector: map[string]string{
								"key-wg2": "value-wg2",
							},
						},
					},
				}).
				Obj(),

			wantFinal: baseJob.DeepCopy(),
		},
		"invalid runInfo": {
			job: baseJob.DeepCopy(),
			runInfo: []podset.PodSetInfo{
				{
					NodeSelector: map[string]string{
						"newKey": "newValue",
					},
				},
				{
					NodeSelector: map[string]string{
						"key-wg1": "updated-value-wg1",
					},
				},
			},
			wantRunError: podset.ErrInvalidPodsetInfo,
			wantAfterRun: baseJob.DeepCopy(),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			genJob := (*RayCluster)(tc.job)
			gotRunError := genJob.RunWithPodSetsInfo(tc.runInfo)

			if diff := cmp.Diff(tc.wantRunError, gotRunError, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Unexpected run error (-want/+got): %s", diff)
			}
			if diff := cmp.Diff(tc.wantAfterRun, tc.job); diff != "" {
				t.Errorf("Unexpected job after run (-want/+got): %s", diff)
			}

			if tc.wantRunError == nil {
				genJob.Suspend()
				genJob.RestorePodSetsInfo(tc.restoreInfo)
				if diff := cmp.Diff(tc.wantFinal, tc.job); diff != "" {
					t.Errorf("Unexpected job after restore (-want/+got): %s", diff)
				}
			}
		})
	}
}

func TestReconciler(t *testing.T) {
	baseJobWrapper := testingrayutil.MakeJob("job", "ns").
		Suspend(true).
		Queue("foo")

	cases := map[string]struct {
		reconcilerOptions []jobframework.Option
		job               rayclusterapi.RayCluster
		workloads         []kueue.Workload
		priorityClasses   []client.Object
		wantJob           rayclusterapi.RayCluster
		wantWorkloads     []kueue.Workload
		wantErr           error
	}{
		"when workload is admitted ": {
			job: *baseJobWrapper.Clone().
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(false).
				Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Admitted(true).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: "main",
								Labels: map[string]string{
									"ac-key": "ac-value",
								},
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Admitted(true).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: "main",
								Labels: map[string]string{
									"ac-key": "ac-value",
								},
							},
						},
					}).
					Obj(),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			clientBuilder := utiltesting.NewClientBuilder()
			if err := SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder)); err != nil {
				t.Fatalf("Could not setup indexes: %v", err)
			}
			objs := append(tc.priorityClasses, &tc.job)
			kcBuilder := clientBuilder.
				WithObjects(objs...)

			for i := range tc.workloads {
				kcBuilder = kcBuilder.WithStatusSubresource(&tc.workloads[i])
			}

			kClient := kcBuilder.Build()
			for i := range tc.workloads {
				if err := ctrl.SetControllerReference(&tc.job, &tc.workloads[i], kClient.Scheme()); err != nil {
					t.Fatalf("Could not setup owner reference in Workloads: %v", err)
				}
				if err := kClient.Create(ctx, &tc.workloads[i]); err != nil {
					t.Fatalf("Could not create workload: %v", err)
				}
			}
			recorder := record.NewBroadcaster().NewRecorder(kClient.Scheme(), corev1.EventSource{Component: "test"})
			reconciler := NewReconciler(kClient, recorder, tc.reconcilerOptions...)

			jobKey := client.ObjectKeyFromObject(&tc.job)
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: jobKey,
			})
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Reconcile returned error (-want,+got):\n%s", diff)
			}

			var gotJob rayclusterapi.RayCluster
			if err := kClient.Get(ctx, jobKey, &gotJob); err != nil {
				t.Fatalf("Could not get Job after reconcile: %v", err)
			}
			//if diff := cmp.Diff(tc.wantJob, gotJob, jobCmpOpts...); diff != "" {
			//	t.Errorf("Job after reconcile (-want,+got):\n%s", diff)
			//}
			var gotWorkloads kueue.WorkloadList
			if err := kClient.List(ctx, &gotWorkloads); err != nil {
				t.Fatalf("Could not get Workloads after reconcile: %v", err)
			}
			//if diff := cmp.Diff(tc.wantWorkloads, gotWorkloads.Items, workloadCmpOpts...); diff != "" {
			//	t.Errorf("Workloads after reconcile (-want,+got):\n%s", diff)
			//}
		})
	}
}
