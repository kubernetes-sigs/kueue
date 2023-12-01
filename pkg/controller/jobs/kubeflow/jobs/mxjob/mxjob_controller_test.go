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

package mxjob

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingmxjob "sigs.k8s.io/kueue/pkg/util/testingjobs/mxjob"
)

func TestPriorityClass(t *testing.T) {
	testcases := map[string]struct {
		job                   kftraining.MXJob
		wantPriorityClassName string
	}{
		"none priority class name specified": {
			job:                   kftraining.MXJob{},
			wantPriorityClassName: "",
		},
		"priority specified at runPolicy and replicas; use priority in runPolicy": {
			job: kftraining.MXJob{
				Spec: kftraining.MXJobSpec{
					JobMode: kftraining.MXTrain,
					RunPolicy: kftraining.RunPolicy{
						SchedulingPolicy: &kftraining.SchedulingPolicy{
							PriorityClass: "scheduling-priority",
						},
					},
					MXReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.MXJobReplicaTypeScheduler: {
							Template: v1.PodTemplateSpec{
								Spec: v1.PodSpec{
									PriorityClassName: "scheduler-priority",
								},
							},
						},
						kftraining.MXJobReplicaTypeServer: {
							Template: v1.PodTemplateSpec{
								Spec: v1.PodSpec{
									PriorityClassName: "server-priority",
								},
							},
						},
						kftraining.MXJobReplicaTypeWorker: {
							Template: v1.PodTemplateSpec{
								Spec: v1.PodSpec{
									PriorityClassName: "worker-priority",
								},
							},
						},
					},
				},
			},
			wantPriorityClassName: "scheduling-priority",
		},
		"runPolicy present, but without priority; fallback to worker": {
			job: kftraining.MXJob{
				Spec: kftraining.MXJobSpec{
					JobMode: kftraining.MXTrain,
					RunPolicy: kftraining.RunPolicy{
						SchedulingPolicy: &kftraining.SchedulingPolicy{},
					},
					MXReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.MXJobReplicaTypeWorker: {
							Template: v1.PodTemplateSpec{
								Spec: v1.PodSpec{
									PriorityClassName: "worker-priority",
								},
							},
						},
					},
				},
			},
			wantPriorityClassName: "worker-priority",
		},
		"specified on scheduler takes precedence over server and worker": {
			job: kftraining.MXJob{
				Spec: kftraining.MXJobSpec{
					JobMode: kftraining.MXTrain,
					MXReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.MXJobReplicaTypeScheduler: {
							Template: v1.PodTemplateSpec{
								Spec: v1.PodSpec{
									PriorityClassName: "scheduler-priority",
								},
							},
						},
						kftraining.MXJobReplicaTypeTunerServer: {
							Template: v1.PodTemplateSpec{
								Spec: v1.PodSpec{
									PriorityClassName: "server-priority",
								},
							},
						},
						kftraining.MXJobReplicaTypeWorker: {
							Template: v1.PodTemplateSpec{
								Spec: v1.PodSpec{
									PriorityClassName: "worker-priority",
								},
							},
						},
					},
				},
			},
			wantPriorityClassName: "scheduler-priority",
		},
		"tunertracker and tunerserver present, but without priority; fallback to tuner": {
			job: kftraining.MXJob{
				Spec: kftraining.MXJobSpec{
					JobMode: kftraining.MXTune,
					MXReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.MXJobReplicaTypeTunerTracker: {
							Template: v1.PodTemplateSpec{
								Spec: v1.PodSpec{},
							},
						},
						kftraining.MXJobReplicaTypeTunerServer: {
							Template: v1.PodTemplateSpec{
								Spec: v1.PodSpec{},
							},
						},
						kftraining.MXJobReplicaTypeTuner: {
							Template: v1.PodTemplateSpec{
								Spec: v1.PodSpec{
									PriorityClassName: "tuner-priority",
								},
							},
						},
					},
				},
			},
			wantPriorityClassName: "tuner-priority",
		},
		"specified on worker only": {
			job: kftraining.MXJob{
				Spec: kftraining.MXJobSpec{
					JobMode: kftraining.MXTrain,
					MXReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.MXJobReplicaTypeWorker: {
							Template: v1.PodTemplateSpec{
								Spec: v1.PodSpec{
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
			job: kftraining.MXJob{
				Spec: kftraining.MXJobSpec{
					JobMode: kftraining.MXTrain,
					MXReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.MXJobReplicaTypeScheduler: {},
						kftraining.MXJobReplicaTypeServer:    {},
						kftraining.MXJobReplicaTypeWorker: {
							Template: v1.PodTemplateSpec{
								Spec: v1.PodSpec{},
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
			mxjob := fromObject(&tc.job)
			gotPriorityClassName := mxjob.PriorityClass()
			if tc.wantPriorityClassName != gotPriorityClassName {
				t.Errorf("Unexpected response (want: %v, got: %v)", tc.wantPriorityClassName, gotPriorityClassName)
			}
		})
	}
}

func TestOrderedReplicaTypes(t *testing.T) {
	testcases := map[string]struct {
		job              kftraining.MXJob
		wantReplicaTypes []kftraining.ReplicaType
	}{
		"job has no replicas": {
			job:              kftraining.MXJob{},
			wantReplicaTypes: []kftraining.ReplicaType{},
		},
		"[MXTrain] job has all replicas": {
			job: kftraining.MXJob{
				Spec: kftraining.MXJobSpec{
					JobMode: kftraining.MXTrain,
					MXReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.MXJobReplicaTypeScheduler: {},
						kftraining.MXJobReplicaTypeServer:    {},
						kftraining.MXJobReplicaTypeWorker:    {},
					},
				},
			},
			wantReplicaTypes: []kftraining.ReplicaType{
				kftraining.MXJobReplicaTypeScheduler,
				kftraining.MXJobReplicaTypeServer,
				kftraining.MXJobReplicaTypeWorker,
			},
		},
		"[MXTune] job has all replicas": {
			job: kftraining.MXJob{
				Spec: kftraining.MXJobSpec{
					JobMode: kftraining.MXTune,
					MXReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.MXJobReplicaTypeTunerTracker: {},
						kftraining.MXJobReplicaTypeTunerServer:  {},
						kftraining.MXJobReplicaTypeTuner:        {},
					},
				},
			},
			wantReplicaTypes: []kftraining.ReplicaType{
				kftraining.MXJobReplicaTypeTunerTracker,
				kftraining.MXJobReplicaTypeTunerServer,
				kftraining.MXJobReplicaTypeTuner,
			},
		},
		"[MXTrain] job has only worker replicas": {
			job: kftraining.MXJob{
				Spec: kftraining.MXJobSpec{
					JobMode: kftraining.MXTrain,
					MXReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.MXJobReplicaTypeWorker: {},
					},
				},
			},
			wantReplicaTypes: []kftraining.ReplicaType{kftraining.MXJobReplicaTypeWorker},
		},
		"[MXTune] job has only tuner replicas": {
			job: kftraining.MXJob{
				Spec: kftraining.MXJobSpec{
					JobMode: kftraining.MXTune,
					MXReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.MXJobReplicaTypeTuner: {},
					},
				},
			},
			wantReplicaTypes: []kftraining.ReplicaType{kftraining.MXJobReplicaTypeTuner},
		},
		"jobMode is an empty": {
			job: kftraining.MXJob{
				Spec: kftraining.MXJobSpec{
					MXReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.MXJobReplicaTypeTunerTracker: {},
						kftraining.MXJobReplicaTypeTunerServer:  {},
						kftraining.MXJobReplicaTypeTuner:        {},
					},
				},
			},
			wantReplicaTypes: []kftraining.ReplicaType{},
		},
	}
	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			mxjob := fromObject(&tc.job)
			gotReplicaTypes := mxjob.OrderedReplicaTypes()
			if diff := cmp.Diff(tc.wantReplicaTypes, gotReplicaTypes); len(diff) != 0 {
				t.Errorf("Unexpected response (want: %v, got: %v)", tc.wantReplicaTypes, gotReplicaTypes)
			}
		})
	}
}

var (
	jobCmpOpts = cmp.Options{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(kftraining.MXJob{}, "TypeMeta", "ObjectMeta"),
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
		job               *kftraining.MXJob
		workloads         []kueue.Workload
		wantJob           *kftraining.MXJob
		wantWorkloads     []kueue.Workload
		wantErr           error
	}{
		"workload is created with podsets": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
			},
			job:     testingmxjob.MakeMXJob("mxjob", "ns").Parallelism(2, 2).Obj(),
			wantJob: testingmxjob.MakeMXJob("mxjob", "ns").Parallelism(2, 2).Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("mxjob", "ns").
					PodSets(
						*utiltesting.MakePodSet("scheduler", 1).Obj(),
						*utiltesting.MakePodSet("server", 2).Obj(),
						*utiltesting.MakePodSet("worker", 2).Obj(),
					).
					Obj(),
			},
		},
		"workload isn't created due to manageJobsWithoutQueueName=false": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(false),
			},
			job:           testingmxjob.MakeMXJob("mxjob", "ns").Parallelism(2, 2).Obj(),
			wantJob:       testingmxjob.MakeMXJob("mxjob", "ns").Parallelism(2, 2).Obj(),
			wantWorkloads: []kueue.Workload{},
		},
		"when workload is evicted, suspended is reset, restore node affinity": {
			job: testingmxjob.MakeMXJob("mxjob", "ns").
				Image("").
				Args(nil).
				Queue("foo").
				Suspend(false).
				Parallelism(10, 5).
				Request(kftraining.MXJobReplicaTypeScheduler, v1.ResourceCPU, "1").
				Request(kftraining.MXJobReplicaTypeServer, v1.ResourceCPU, "2").
				Request(kftraining.MXJobReplicaTypeWorker, v1.ResourceCPU, "5").
				NodeSelector("provisioning", "spot").
				Active(kftraining.MXJobReplicaTypeScheduler, 1).
				Active(kftraining.MXJobReplicaTypeServer, 5).
				Active(kftraining.MXJobReplicaTypeWorker, 10).
				Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").
					PodSets(
						*utiltesting.MakePodSet("scheduler", 1).Request(v1.ResourceCPU, "1").Obj(),
						*utiltesting.MakePodSet("server", 5).Request(v1.ResourceCPU, "2").Obj(),
						*utiltesting.MakePodSet("worker", 10).Request(v1.ResourceCPU, "5").Obj(),
					).
					ReserveQuota(utiltesting.MakeAdmission("cq").
						AssignmentPodCount(1).
						AssignmentPodCount(5).
						AssignmentPodCount(10).
						Obj()).
					Admitted(true).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadEvicted,
						Status: metav1.ConditionTrue,
					}).
					Obj(),
			},
			wantJob: testingmxjob.MakeMXJob("mxjob", "ns").
				Image("").
				Args(nil).
				Queue("foo").
				Suspend(true).
				Parallelism(10, 5).
				Request(kftraining.MXJobReplicaTypeScheduler, v1.ResourceCPU, "1").
				Request(kftraining.MXJobReplicaTypeServer, v1.ResourceCPU, "2").
				Request(kftraining.MXJobReplicaTypeWorker, v1.ResourceCPU, "5").
				Active(kftraining.MXJobReplicaTypeScheduler, 1).
				Active(kftraining.MXJobReplicaTypeServer, 5).
				Active(kftraining.MXJobReplicaTypeWorker, 10).
				Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").
					PodSets(
						*utiltesting.MakePodSet("scheduler", 1).Request(v1.ResourceCPU, "1").Obj(),
						*utiltesting.MakePodSet("server", 5).Request(v1.ResourceCPU, "2").Obj(),
						*utiltesting.MakePodSet("worker", 10).Request(v1.ResourceCPU, "5").Obj(),
					).
					ReserveQuota(utiltesting.MakeAdmission("cq").
						AssignmentPodCount(1).
						AssignmentPodCount(5).
						AssignmentPodCount(10).
						Obj()).
					Admitted(true).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadEvicted,
						Status: metav1.ConditionTrue,
					}).
					Obj(),
			},
		},
		"workload shouldn't be recreated for the completed mx job": {
			job: testingmxjob.MakeMXJob("mxjob", "ns").
				Queue("foo").
				StatusConditions(kftraining.JobCondition{Type: kftraining.JobSucceeded, Status: v1.ConditionTrue}).
				Obj(),
			workloads: []kueue.Workload{},
			wantJob: testingmxjob.MakeMXJob("mxjob", "ns").
				Queue("foo").
				StatusConditions(kftraining.JobCondition{Type: kftraining.JobSucceeded, Status: v1.ConditionTrue}).
				Obj(),
			wantWorkloads: []kueue.Workload{},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			kcBuilder := utiltesting.NewClientBuilder(kftraining.AddToScheme)
			if err := SetupIndexes(ctx, utiltesting.AsIndexer(kcBuilder)); err != nil {
				t.Fatalf("Failed to setup indexes: %v", err)
			}
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
			recorder := record.NewBroadcaster().NewRecorder(kClient.Scheme(), v1.EventSource{Component: "test"})
			reconciler := NewReconciler(kClient, recorder, tc.reconcilerOptions...)

			jobKey := client.ObjectKeyFromObject(tc.job)
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: jobKey,
			})
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Reconcile returned error (-want,+got):\n%s", diff)
			}

			var gotMXJob kftraining.MXJob
			if err = kClient.Get(ctx, jobKey, &gotMXJob); err != nil {
				t.Fatalf("Could not get Job after reconcile: %v", err)
			}
			if diff := cmp.Diff(tc.wantJob, &gotMXJob, jobCmpOpts...); diff != "" {
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
