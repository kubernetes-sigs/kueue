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

package xgboostjob

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingxgboostjob "sigs.k8s.io/kueue/pkg/util/testingjobs/xgboostjob"
)

func TestPriorityClass(t *testing.T) {
	testcases := map[string]struct {
		job                   kftraining.XGBoostJob
		wantPriorityClassName string
	}{
		"none priority class name specified": {
			job:                   kftraining.XGBoostJob{},
			wantPriorityClassName: "",
		},
		"priority specified at runPolicy and replicas; use priority in runPolicy": {
			job: kftraining.XGBoostJob{
				Spec: kftraining.XGBoostJobSpec{
					RunPolicy: kftraining.RunPolicy{
						SchedulingPolicy: &kftraining.SchedulingPolicy{
							PriorityClass: "scheduling-priority",
						},
					},
					XGBReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.XGBoostJobReplicaTypeMaster: {
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									PriorityClassName: "master-priority",
								},
							},
						},
						kftraining.XGBoostJobReplicaTypeWorker: {
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									PriorityClassName: "worker-priority",
								},
							},
						},
					},
				},
			},
			wantPriorityClassName: "scheduling-priority",
		},
		"runPolicy present, but without priority; fallback to master": {
			job: kftraining.XGBoostJob{
				Spec: kftraining.XGBoostJobSpec{
					RunPolicy: kftraining.RunPolicy{
						SchedulingPolicy: &kftraining.SchedulingPolicy{},
					},
					XGBReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.XGBoostJobReplicaTypeMaster: {
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									PriorityClassName: "master-priority",
								},
							},
						},
					},
				},
			},
			wantPriorityClassName: "master-priority",
		},
		"specified on master takes precedence over worker": {
			job: kftraining.XGBoostJob{
				Spec: kftraining.XGBoostJobSpec{
					XGBReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.XGBoostJobReplicaTypeMaster: {
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									PriorityClassName: "master-priority",
								},
							},
						},
						kftraining.XGBoostJobReplicaTypeWorker: {
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									PriorityClassName: "worker-priority",
								},
							},
						},
					},
				},
			},
			wantPriorityClassName: "master-priority",
		},
		"master present, but without priority; fallback to worker": {
			job: kftraining.XGBoostJob{
				Spec: kftraining.XGBoostJobSpec{
					XGBReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.XGBoostJobReplicaTypeMaster: {
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{},
							},
						},
						kftraining.XGBoostJobReplicaTypeWorker: {
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									PriorityClassName: "worker-priority",
								},
							},
						},
					},
				},
			},
			wantPriorityClassName: "worker-priority",
		},
		"specified on worker only": {
			job: kftraining.XGBoostJob{
				Spec: kftraining.XGBoostJobSpec{
					XGBReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.XGBoostJobReplicaTypeWorker: {
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									PriorityClassName: "worker-priority",
								},
							},
						},
					},
				},
			},
			wantPriorityClassName: "worker-priority",
		},
		"worker present, but without priority; fallback to empty": {
			job: kftraining.XGBoostJob{
				Spec: kftraining.XGBoostJobSpec{
					XGBReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.XGBoostJobReplicaTypeMaster: {},
						kftraining.XGBoostJobReplicaTypeWorker: {
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{},
							},
						},
					},
				},
			},
			wantPriorityClassName: "",
		},
	}
	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			xgboostJob := fromObject(&tc.job)
			gotPriorityClassName := xgboostJob.PriorityClass()
			if tc.wantPriorityClassName != gotPriorityClassName {
				t.Errorf("Unexpected response (want: %v, got: %v)", tc.wantPriorityClassName, gotPriorityClassName)
			}
		})
	}
}

func TestOrderedReplicaTypes(t *testing.T) {
	testcases := map[string]struct {
		job              kftraining.XGBoostJob
		wantReplicaTypes []kftraining.ReplicaType
	}{
		"job has no replicas": {
			job:              kftraining.XGBoostJob{},
			wantReplicaTypes: []kftraining.ReplicaType{},
		},
		"job has all replicas": {
			job: kftraining.XGBoostJob{
				Spec: kftraining.XGBoostJobSpec{
					XGBReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.XGBoostJobReplicaTypeMaster: {},
						kftraining.XGBoostJobReplicaTypeWorker: {},
					},
				},
			},
			wantReplicaTypes: []kftraining.ReplicaType{
				kftraining.XGBoostJobReplicaTypeMaster,
				kftraining.XGBoostJobReplicaTypeWorker,
			},
		},
		"job has only worker replica": {
			job: kftraining.XGBoostJob{
				Spec: kftraining.XGBoostJobSpec{
					XGBReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.XGBoostJobReplicaTypeWorker: {},
					},
				},
			},
			wantReplicaTypes: []kftraining.ReplicaType{
				kftraining.XGBoostJobReplicaTypeWorker,
			},
		},
	}
	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			xgboostJob := fromObject(&tc.job)
			gotReplicaTypes := xgboostJob.OrderedReplicaTypes()
			if diff := cmp.Diff(tc.wantReplicaTypes, gotReplicaTypes); diff != "" {
				t.Errorf("Unexpected response (-want, +got): %s", diff)
			}
		})
	}
}

func TestPodSets(t *testing.T) {
	testCases := map[string]struct {
		job         *kftraining.XGBoostJob
		wantPodSets func(job *kftraining.XGBoostJob) []kueue.PodSet
	}{
		"no annotations": {
			job: testingxgboostjob.MakeXGBoostJob("xgboostjob", "ns").
				XGBReplicaSpecs(
					testingxgboostjob.XGBReplicaSpecRequirement{
						ReplicaType:  kftraining.XGBoostJobReplicaTypeMaster,
						ReplicaCount: 1,
					},
					testingxgboostjob.XGBReplicaSpecRequirement{
						ReplicaType:  kftraining.XGBoostJobReplicaTypeWorker,
						ReplicaCount: 1,
					},
				).
				Obj(),
			wantPodSets: func(job *kftraining.XGBoostJob) []kueue.PodSet {
				return []kueue.PodSet{
					{
						Name:     strings.ToLower(string(kftraining.XGBoostJobReplicaTypeMaster)),
						Template: job.Spec.XGBReplicaSpecs[kftraining.XGBoostJobReplicaTypeMaster].Template,
						Count:    1,
					},
					{
						Name:     strings.ToLower(string(kftraining.XGBoostJobReplicaTypeWorker)),
						Template: job.Spec.XGBReplicaSpecs[kftraining.XGBoostJobReplicaTypeWorker].Template,
						Count:    1,
					},
				}
			},
		},
		"with required and preferred topology annotation": {
			job: testingxgboostjob.MakeXGBoostJob("xgboostjob", "ns").
				XGBReplicaSpecs(
					testingxgboostjob.XGBReplicaSpecRequirement{
						ReplicaType:  kftraining.XGBoostJobReplicaTypeMaster,
						ReplicaCount: 1,
						Annotations: map[string]string{
							kueuealpha.PodSetRequiredTopologyAnnotation: "cloud.com/rack",
						},
					},
					testingxgboostjob.XGBReplicaSpecRequirement{
						ReplicaType:  kftraining.XGBoostJobReplicaTypeWorker,
						ReplicaCount: 1,
						Annotations: map[string]string{
							kueuealpha.PodSetPreferredTopologyAnnotation: "cloud.com/block",
						},
					},
				).
				Obj(),
			wantPodSets: func(job *kftraining.XGBoostJob) []kueue.PodSet {
				return []kueue.PodSet{
					{
						Name:            strings.ToLower(string(kftraining.XGBoostJobReplicaTypeMaster)),
						Template:        job.Spec.XGBReplicaSpecs[kftraining.XGBoostJobReplicaTypeMaster].Template,
						Count:           1,
						TopologyRequest: &kueue.PodSetTopologyRequest{Required: ptr.To("cloud.com/rack")},
					},
					{
						Name:            strings.ToLower(string(kftraining.XGBoostJobReplicaTypeWorker)),
						Template:        job.Spec.XGBReplicaSpecs[kftraining.XGBoostJobReplicaTypeWorker].Template,
						Count:           1,
						TopologyRequest: &kueue.PodSetTopologyRequest{Preferred: ptr.To("cloud.com/block")},
					},
				}
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			gotPodSets := fromObject(tc.job).PodSets()
			if diff := cmp.Diff(tc.wantPodSets(tc.job), gotPodSets); diff != "" {
				t.Errorf("pod sets mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	testCases := map[string]struct {
		job      *kftraining.XGBoostJob
		wantErrs field.ErrorList
	}{
		"no annotations": {
			job: testingxgboostjob.MakeXGBoostJob("xgboostjob", "ns").
				XGBReplicaSpecs(
					testingxgboostjob.XGBReplicaSpecRequirement{
						ReplicaType:  kftraining.XGBoostJobReplicaTypeMaster,
						ReplicaCount: 1,
					},
					testingxgboostjob.XGBReplicaSpecRequirement{
						ReplicaType:  kftraining.XGBoostJobReplicaTypeWorker,
						ReplicaCount: 1,
					},
				).
				Obj(),
		},
		"valid TAS request": {
			job: testingxgboostjob.MakeXGBoostJob("xgboostjob", "ns").
				XGBReplicaSpecs(
					testingxgboostjob.XGBReplicaSpecRequirement{
						ReplicaType:  kftraining.XGBoostJobReplicaTypeMaster,
						ReplicaCount: 1,
						Annotations: map[string]string{
							kueuealpha.PodSetRequiredTopologyAnnotation: "cloud.com/rack",
						},
					},
					testingxgboostjob.XGBReplicaSpecRequirement{
						ReplicaType:  kftraining.XGBoostJobReplicaTypeWorker,
						ReplicaCount: 3,
						Annotations: map[string]string{
							kueuealpha.PodSetPreferredTopologyAnnotation: "cloud.com/block",
						},
					},
				).
				Obj(),
		},
		"invalid TAS request": {
			job: testingxgboostjob.MakeXGBoostJob("xgboostjob", "ns").
				XGBReplicaSpecs(
					testingxgboostjob.XGBReplicaSpecRequirement{
						ReplicaType:  kftraining.XGBoostJobReplicaTypeMaster,
						ReplicaCount: 1,
						Annotations: map[string]string{
							kueuealpha.PodSetRequiredTopologyAnnotation:  "cloud.com/rack",
							kueuealpha.PodSetPreferredTopologyAnnotation: "cloud.com/block",
						},
					},
					testingxgboostjob.XGBReplicaSpecRequirement{
						ReplicaType:  kftraining.XGBoostJobReplicaTypeWorker,
						ReplicaCount: 3,
						Annotations: map[string]string{
							kueuealpha.PodSetRequiredTopologyAnnotation:  "cloud.com/rack",
							kueuealpha.PodSetPreferredTopologyAnnotation: "cloud.com/block",
						},
					},
				).
				Obj(),
			wantErrs: field.ErrorList{
				field.Invalid(
					field.NewPath("spec", "xgbReplicaSpecs").
						Key("Master").
						Child("template", "metadata", "annotations"),
					field.OmitValueType{},
					`must not contain both "kueue.x-k8s.io/podset-required-topology" and "kueue.x-k8s.io/podset-preferred-topology"`,
				),
				field.Invalid(
					field.NewPath("spec", "xgbReplicaSpecs").
						Key("Worker").
						Child("template", "metadata", "annotations"),
					field.OmitValueType{},
					`must not contain both "kueue.x-k8s.io/podset-required-topology" and "kueue.x-k8s.io/podset-preferred-topology"`,
				),
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			if diff := cmp.Diff(tc.wantErrs, fromObject(tc.job).ValidateOnCreate()); diff != "" {
				t.Errorf("validate create error list mismatch (-want +got):\n%s", diff)
			}

			if diff := cmp.Diff(tc.wantErrs, fromObject(tc.job).ValidateOnUpdate(nil)); diff != "" {
				t.Errorf("validate create error list mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

var (
	jobCmpOpts = cmp.Options{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(kftraining.XGBoostJob{}, "TypeMeta", "ObjectMeta"),
	}
	workloadCmpOpts = cmp.Options{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(kueue.Workload{}, "TypeMeta", "ObjectMeta"),
		cmpopts.IgnoreFields(kueue.WorkloadSpec{}, "Priority"),
		cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
		cmpopts.IgnoreFields(kueue.PodSet{}, "Template"),
	}
)

func TestReconciler(t *testing.T) {
	cases := map[string]struct {
		reconcilerOptions []jobframework.Option
		job               *kftraining.XGBoostJob
		workloads         []kueue.Workload
		wantJob           *kftraining.XGBoostJob
		wantWorkloads     []kueue.Workload
		wantErr           error
	}{
		"workload is created with podsets": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
			},
			job:     testingxgboostjob.MakeXGBoostJob("xgboostjob", "ns").XGBReplicaSpecsDefault().Parallelism(2).Obj(),
			wantJob: testingxgboostjob.MakeXGBoostJob("xgboostjob", "ns").XGBReplicaSpecsDefault().Parallelism(2).Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("xgboostjob", "ns").
					PodSets(
						*utiltesting.MakePodSet("master", 1).Obj(),
						*utiltesting.MakePodSet("worker", 2).Obj(),
					).
					Obj(),
			},
		},
		"workload isn't created due to manageJobsWithoutQueueName=false": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(false),
			},
			job:           testingxgboostjob.MakeXGBoostJob("xgboostjob", "ns").XGBReplicaSpecsDefault().Parallelism(2).Obj(),
			wantJob:       testingxgboostjob.MakeXGBoostJob("xgboostjob", "ns").XGBReplicaSpecsDefault().Parallelism(2).Obj(),
			wantWorkloads: []kueue.Workload{},
		},
		"when workload is evicted, suspended is reset, restore node affinity": {
			job: testingxgboostjob.MakeXGBoostJob("xgboostjob", "ns").
				XGBReplicaSpecsDefault().
				Image("").
				Args(nil).
				Queue("foo").
				Suspend(false).
				Parallelism(10).
				Request(kftraining.XGBoostJobReplicaTypeMaster, corev1.ResourceCPU, "1").
				Request(kftraining.XGBoostJobReplicaTypeWorker, corev1.ResourceCPU, "5").
				NodeSelector("provisioning", "spot").
				Active(kftraining.XGBoostJobReplicaTypeMaster, 1).
				Active(kftraining.XGBoostJobReplicaTypeWorker, 10).
				Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").
					PodSets(
						*utiltesting.MakePodSet("master", 1).Request(corev1.ResourceCPU, "1").Obj(),
						*utiltesting.MakePodSet("worker", 10).Request(corev1.ResourceCPU, "5").Obj(),
					).
					ReserveQuota(utiltesting.MakeAdmission("cq").
						PodSets(
							kueue.PodSetAssignment{
								Name: "master",
								Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
									corev1.ResourceCPU: "default",
								},
								Count: ptr.To[int32](1),
							},
							kueue.PodSetAssignment{
								Name: "worker",
								Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
									corev1.ResourceCPU: "default",
								},
								Count: ptr.To[int32](10),
							},
						).
						Obj()).
					Admitted(true).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadEvicted,
						Status: metav1.ConditionTrue,
					}).
					Obj(),
			},
			wantJob: testingxgboostjob.MakeXGBoostJob("xgboostjob", "ns").
				XGBReplicaSpecsDefault().
				Image("").
				Args(nil).
				Queue("foo").
				Suspend(true).
				Parallelism(10).
				Request(kftraining.XGBoostJobReplicaTypeMaster, corev1.ResourceCPU, "1").
				Request(kftraining.XGBoostJobReplicaTypeWorker, corev1.ResourceCPU, "5").
				Active(kftraining.XGBoostJobReplicaTypeMaster, 1).
				Active(kftraining.XGBoostJobReplicaTypeWorker, 10).
				Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").
					PodSets(
						*utiltesting.MakePodSet("master", 1).Request(corev1.ResourceCPU, "1").Obj(),
						*utiltesting.MakePodSet("worker", 10).Request(corev1.ResourceCPU, "5").Obj(),
					).
					ReserveQuota(utiltesting.MakeAdmission("cq").
						PodSets(
							kueue.PodSetAssignment{
								Name: "master",
								Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
									corev1.ResourceCPU: "default",
								},
								Count: ptr.To[int32](1),
							},
							kueue.PodSetAssignment{
								Name: "worker",
								Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
									corev1.ResourceCPU: "default",
								},
								Count: ptr.To[int32](10),
							},
						).
						Obj()).
					Admitted(true).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadEvicted,
						Status: metav1.ConditionTrue,
					}).
					Obj(),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			kcBuilder := utiltesting.NewClientBuilder(kftraining.AddToScheme)
			if err := SetupIndexes(ctx, utiltesting.AsIndexer(kcBuilder)); err != nil {
				t.Fatalf("Failed to setup indexes: %v", err)
			}
			kcBuilder = kcBuilder.WithObjects(utiltesting.MakeResourceFlavor("default").Obj())
			kcBuilder = kcBuilder.WithObjects(tc.job)
			for i := range tc.workloads {
				kcBuilder = kcBuilder.WithStatusSubresource(&tc.workloads[i])
			}

			kClient := kcBuilder.Build()
			for i := range tc.workloads {
				if err := ctrl.SetControllerReference(tc.job, &tc.workloads[i], kClient.Scheme()); err != nil {
					t.Fatalf("Could not set controller reference: %v", err)
				}
				if err := kClient.Create(ctx, &tc.workloads[i]); err != nil {
					t.Fatalf("Could not create Workload: %v", err)
				}
			}
			recorder := record.NewBroadcaster().NewRecorder(kClient.Scheme(), corev1.EventSource{Component: "test"})
			reconciler := NewReconciler(kClient, recorder, tc.reconcilerOptions...)

			jobKey := client.ObjectKeyFromObject(tc.job)
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: jobKey,
			})
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Reconcile returned error (-want,+got):\n%s", diff)
			}

			var gotXGBoostJob kftraining.XGBoostJob
			if err = kClient.Get(ctx, jobKey, &gotXGBoostJob); err != nil {
				t.Fatalf("Could not get Job after reconcile: %v", err)
			}
			if diff := cmp.Diff(tc.wantJob, &gotXGBoostJob, jobCmpOpts...); diff != "" {
				t.Errorf("Job after reconcile (-want,+got):\n%s", diff)
			}
			var gotWorkloads kueue.WorkloadList
			if err = kClient.List(ctx, &gotWorkloads); err != nil {
				t.Fatalf("Could not list Workloads after reconcile: %v", err)
			}
			if diff := cmp.Diff(tc.wantWorkloads, gotWorkloads.Items, workloadCmpOpts...); diff != "" {
				t.Errorf("Workloads after reconcile (-want,+got):\n%s", diff)
			}
		})
	}
}
