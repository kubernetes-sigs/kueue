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

package mpijob

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	common "github.com/kubeflow/common/pkg/apis/common/v1"
	kubeflow "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingmpijob "sigs.k8s.io/kueue/pkg/util/testingjobs/mpijob"
)

func TestCalcPriorityClassName(t *testing.T) {
	testcases := map[string]struct {
		job                   kubeflow.MPIJob
		wantPriorityClassName string
	}{
		"none priority class name specified": {
			job:                   kubeflow.MPIJob{},
			wantPriorityClassName: "",
		},
		"priority specified at runPolicy and replicas; use priority in runPolicy": {
			job: kubeflow.MPIJob{
				Spec: kubeflow.MPIJobSpec{
					RunPolicy: kubeflow.RunPolicy{
						SchedulingPolicy: &kubeflow.SchedulingPolicy{
							PriorityClass: "scheduling-priority",
						},
					},
					MPIReplicaSpecs: map[kubeflow.MPIReplicaType]*common.ReplicaSpec{
						kubeflow.MPIReplicaTypeLauncher: {
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									PriorityClassName: "launcher-priority",
								},
							},
						},
						kubeflow.MPIReplicaTypeWorker: {
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
		"runPolicy present, but without priority; fallback to launcher": {
			job: kubeflow.MPIJob{
				Spec: kubeflow.MPIJobSpec{
					RunPolicy: kubeflow.RunPolicy{
						SchedulingPolicy: &kubeflow.SchedulingPolicy{},
					},
					MPIReplicaSpecs: map[kubeflow.MPIReplicaType]*common.ReplicaSpec{
						kubeflow.MPIReplicaTypeLauncher: {
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									PriorityClassName: "launcher-priority",
								},
							},
						},
					},
				},
			},
			wantPriorityClassName: "launcher-priority",
		},
		"specified on launcher takes precedence over worker": {
			job: kubeflow.MPIJob{
				Spec: kubeflow.MPIJobSpec{
					MPIReplicaSpecs: map[kubeflow.MPIReplicaType]*common.ReplicaSpec{
						kubeflow.MPIReplicaTypeLauncher: {
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									PriorityClassName: "launcher-priority",
								},
							},
						},
						kubeflow.MPIReplicaTypeWorker: {
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									PriorityClassName: "worker-priority",
								},
							},
						},
					},
				},
			},
			wantPriorityClassName: "launcher-priority",
		},
		"launcher present, but without priority; fallback to worker": {
			job: kubeflow.MPIJob{
				Spec: kubeflow.MPIJobSpec{
					MPIReplicaSpecs: map[kubeflow.MPIReplicaType]*common.ReplicaSpec{
						kubeflow.MPIReplicaTypeLauncher: {
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{},
							},
						},
						kubeflow.MPIReplicaTypeWorker: {
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
			job: kubeflow.MPIJob{
				Spec: kubeflow.MPIJobSpec{
					MPIReplicaSpecs: map[kubeflow.MPIReplicaType]*common.ReplicaSpec{
						kubeflow.MPIReplicaTypeLauncher: {},
						kubeflow.MPIReplicaTypeWorker: {
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
			job: kubeflow.MPIJob{
				Spec: kubeflow.MPIJobSpec{
					MPIReplicaSpecs: map[kubeflow.MPIReplicaType]*common.ReplicaSpec{
						kubeflow.MPIReplicaTypeLauncher: {},
						kubeflow.MPIReplicaTypeWorker: {
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
			mpiJob := (*MPIJob)(&tc.job)
			gotPriorityClassName := mpiJob.PriorityClass()
			if tc.wantPriorityClassName != gotPriorityClassName {
				t.Errorf("Unexpected response (want: %v, got: %v)", tc.wantPriorityClassName, gotPriorityClassName)
			}
		})
	}
}

var (
	jobCmpOpts = []cmp.Option{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(kubeflow.MPIJob{}, "TypeMeta", "ObjectMeta"),
	}
	workloadCmpOpts = []cmp.Option{
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
		job               *kubeflow.MPIJob
		wantJob           *kubeflow.MPIJob
		wantWorkloads     []kueue.Workload
		wantErr           error
	}{
		"pod sets": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
			},
			job:     testingmpijob.MakeMPIJob("mpijob", "ns").Parallelism(2).Obj(),
			wantJob: testingmpijob.MakeMPIJob("mpijob", "ns").Parallelism(2).Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("mpijob", "ns").
					PodSets(
						*utiltesting.MakePodSet("launcher", 1).Obj(),
						*utiltesting.MakePodSet("worker", 2).Obj(),
					).
					Obj(),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			clientBuilder := utiltesting.NewClientBuilder(kubeflow.AddToScheme)
			if err := SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder)); err != nil {
				t.Fatalf("Could not setup indexes: %v", err)
			}
			kClient := clientBuilder.WithObjects(tc.job).Build()
			recorder := record.NewBroadcaster().NewRecorder(kClient.Scheme(), corev1.EventSource{Component: "test"})
			reconciler := NewReconciler(kClient, recorder, tc.reconcilerOptions...)

			jobKey := client.ObjectKeyFromObject(tc.job)
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: jobKey,
			})
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Reconcile returned error (-want,+got):\n%s", diff)
			}

			var gotMpiJob kubeflow.MPIJob
			if err := kClient.Get(ctx, jobKey, &gotMpiJob); err != nil {
				t.Fatalf("Could not get Job after reconcile: %v", err)
			}
			if diff := cmp.Diff(tc.wantJob, &gotMpiJob, jobCmpOpts...); diff != "" {
				t.Errorf("Job after reconcile (-want,+got):\n%s", diff)
			}
			var gotWorkloads kueue.WorkloadList
			if err := kClient.List(ctx, &gotWorkloads); err != nil {
				t.Fatalf("Could not get Workloads after reconcile: %v", err)
			}
			if diff := cmp.Diff(tc.wantWorkloads, gotWorkloads.Items, workloadCmpOpts...); diff != "" {
				t.Errorf("Workloads after reconcile (-want,+got):\n%s", diff)
			}
		})
	}

}
