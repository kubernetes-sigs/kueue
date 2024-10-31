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
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	kfmpi "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingmpijob "sigs.k8s.io/kueue/pkg/util/testingjobs/mpijob"
)

func TestCalcPriorityClassName(t *testing.T) {
	testcases := map[string]struct {
		job                   kfmpi.MPIJob
		wantPriorityClassName string
	}{
		"none priority class name specified": {
			job:                   kfmpi.MPIJob{},
			wantPriorityClassName: "",
		},
		"priority specified at runPolicy and replicas; use priority in runPolicy": {
			job: kfmpi.MPIJob{
				Spec: kfmpi.MPIJobSpec{
					RunPolicy: kfmpi.RunPolicy{
						SchedulingPolicy: &kfmpi.SchedulingPolicy{
							PriorityClass: "scheduling-priority",
						},
					},
					MPIReplicaSpecs: map[kfmpi.MPIReplicaType]*kfmpi.ReplicaSpec{
						kfmpi.MPIReplicaTypeLauncher: {
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									PriorityClassName: "launcher-priority",
								},
							},
						},
						kfmpi.MPIReplicaTypeWorker: {
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
			job: kfmpi.MPIJob{
				Spec: kfmpi.MPIJobSpec{
					RunPolicy: kfmpi.RunPolicy{
						SchedulingPolicy: &kfmpi.SchedulingPolicy{},
					},
					MPIReplicaSpecs: map[kfmpi.MPIReplicaType]*kfmpi.ReplicaSpec{
						kfmpi.MPIReplicaTypeLauncher: {
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
			job: kfmpi.MPIJob{
				Spec: kfmpi.MPIJobSpec{
					MPIReplicaSpecs: map[kfmpi.MPIReplicaType]*kfmpi.ReplicaSpec{
						kfmpi.MPIReplicaTypeLauncher: {
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									PriorityClassName: "launcher-priority",
								},
							},
						},
						kfmpi.MPIReplicaTypeWorker: {
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
			job: kfmpi.MPIJob{
				Spec: kfmpi.MPIJobSpec{
					MPIReplicaSpecs: map[kfmpi.MPIReplicaType]*kfmpi.ReplicaSpec{
						kfmpi.MPIReplicaTypeLauncher: {
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{},
							},
						},
						kfmpi.MPIReplicaTypeWorker: {
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
			job: kfmpi.MPIJob{
				Spec: kfmpi.MPIJobSpec{
					MPIReplicaSpecs: map[kfmpi.MPIReplicaType]*kfmpi.ReplicaSpec{
						kfmpi.MPIReplicaTypeLauncher: {},
						kfmpi.MPIReplicaTypeWorker: {
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
			job: kfmpi.MPIJob{
				Spec: kfmpi.MPIJobSpec{
					MPIReplicaSpecs: map[kfmpi.MPIReplicaType]*kfmpi.ReplicaSpec{
						kfmpi.MPIReplicaTypeLauncher: {},
						kfmpi.MPIReplicaTypeWorker: {
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

func TestPodSets(t *testing.T) {
	jobTemplate := testingmpijob.MakeMPIJob("pod", "ns").MPIJobReplicaSpecs(
		testingmpijob.MPIJobReplicaSpecRequirement{
			ReplicaType:  kfmpi.MPIReplicaTypeLauncher,
			ReplicaCount: 1,
		},
		testingmpijob.MPIJobReplicaSpecRequirement{
			ReplicaType:  kfmpi.MPIReplicaTypeWorker,
			ReplicaCount: 3,
		},
	)

	testCases := map[string]struct {
		job         *MPIJob
		wantPodSets []kueue.PodSet
	}{
		"no annotations": {
			job: (*MPIJob)(jobTemplate.DeepCopy()),
			wantPodSets: []kueue.PodSet{
				{
					Name:     strings.ToLower(string(kfmpi.MPIReplicaTypeLauncher)),
					Count:    1,
					Template: *jobTemplate.Clone().Spec.MPIReplicaSpecs[kfmpi.MPIReplicaTypeLauncher].Template.DeepCopy(),
				},
				{
					Name:     strings.ToLower(string(kfmpi.MPIReplicaTypeWorker)),
					Count:    3,
					Template: *jobTemplate.Clone().Spec.MPIReplicaSpecs[kfmpi.MPIReplicaTypeWorker].Template.DeepCopy(),
				},
			},
		},
		"with required topology annotation": {
			job: (*MPIJob)(jobTemplate.Clone().
				PodAnnotation(
					kfmpi.MPIReplicaTypeLauncher,
					kueuealpha.PodSetRequiredTopologyAnnotation,
					"cloud.com/block",
				).
				PodAnnotation(
					kfmpi.MPIReplicaTypeWorker,
					kueuealpha.PodSetRequiredTopologyAnnotation,
					"cloud.com/block",
				).
				Obj(),
			),
			wantPodSets: []kueue.PodSet{
				{
					Name:  strings.ToLower(string(kfmpi.MPIReplicaTypeLauncher)),
					Count: 1,
					Template: jobTemplate.
						Clone().
						PodAnnotation(
							kfmpi.MPIReplicaTypeLauncher,
							kueuealpha.PodSetRequiredTopologyAnnotation,
							"cloud.com/block",
						).
						PodAnnotation(
							kfmpi.MPIReplicaTypeWorker,
							kueuealpha.PodSetRequiredTopologyAnnotation,
							"cloud.com/block",
						).
						Spec.MPIReplicaSpecs[kfmpi.MPIReplicaTypeLauncher].Template,
					TopologyRequest: &kueue.PodSetTopologyRequest{Required: ptr.To("cloud.com/block")},
				},
				{
					Name:  strings.ToLower(string(kfmpi.MPIReplicaTypeWorker)),
					Count: 3,
					Template: jobTemplate.
						Clone().
						PodAnnotation(
							kfmpi.MPIReplicaTypeLauncher,
							kueuealpha.PodSetRequiredTopologyAnnotation,
							"cloud.com/block",
						).
						PodAnnotation(
							kfmpi.MPIReplicaTypeWorker,
							kueuealpha.PodSetRequiredTopologyAnnotation,
							"cloud.com/block",
						).
						Spec.MPIReplicaSpecs[kfmpi.MPIReplicaTypeWorker].Template,
					TopologyRequest: &kueue.PodSetTopologyRequest{Required: ptr.To("cloud.com/block")},
				},
			},
		},
		"with preferred topology annotation": {
			job: (*MPIJob)(jobTemplate.Clone().
				PodAnnotation(
					kfmpi.MPIReplicaTypeLauncher,
					kueuealpha.PodSetPreferredTopologyAnnotation,
					"cloud.com/block",
				).
				PodAnnotation(
					kfmpi.MPIReplicaTypeWorker,
					kueuealpha.PodSetPreferredTopologyAnnotation,
					"cloud.com/block",
				).
				Obj(),
			),
			wantPodSets: []kueue.PodSet{
				{
					Name:  strings.ToLower(string(kfmpi.MPIReplicaTypeLauncher)),
					Count: 1,
					Template: jobTemplate.
						Clone().
						PodAnnotation(
							kfmpi.MPIReplicaTypeLauncher,
							kueuealpha.PodSetPreferredTopologyAnnotation,
							"cloud.com/block",
						).
						PodAnnotation(
							kfmpi.MPIReplicaTypeWorker,
							kueuealpha.PodSetPreferredTopologyAnnotation,
							"cloud.com/block",
						).
						Spec.MPIReplicaSpecs[kfmpi.MPIReplicaTypeLauncher].Template,
					TopologyRequest: &kueue.PodSetTopologyRequest{Preferred: ptr.To("cloud.com/block")},
				},
				{
					Name:  strings.ToLower(string(kfmpi.MPIReplicaTypeWorker)),
					Count: 3,
					Template: jobTemplate.
						Clone().
						PodAnnotation(
							kfmpi.MPIReplicaTypeLauncher,
							kueuealpha.PodSetPreferredTopologyAnnotation,
							"cloud.com/block",
						).
						PodAnnotation(
							kfmpi.MPIReplicaTypeWorker,
							kueuealpha.PodSetPreferredTopologyAnnotation,
							"cloud.com/block",
						).
						Spec.MPIReplicaSpecs[kfmpi.MPIReplicaTypeWorker].Template,
					TopologyRequest: &kueue.PodSetTopologyRequest{Preferred: ptr.To("cloud.com/block")},
				},
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			if diff := cmp.Diff(tc.wantPodSets, tc.job.PodSets()); diff != "" {
				t.Errorf("pod sets mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

var (
	jobCmpOpts = []cmp.Option{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(kfmpi.MPIJob{}, "TypeMeta", "ObjectMeta"),
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
	baseWPCWrapper := utiltesting.MakeWorkloadPriorityClass("test-wpc").
		PriorityValue(100)
	basePCWrapper := utiltesting.MakePriorityClass("test-pc").
		PriorityValue(200)
	cases := map[string]struct {
		reconcilerOptions []jobframework.Option
		job               *kfmpi.MPIJob
		priorityClasses   []client.Object
		wantJob           *kfmpi.MPIJob
		wantWorkloads     []kueue.Workload
		wantErr           error
	}{
		"workload is created with podsets": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
			},
			job:     testingmpijob.MakeMPIJob("mpijob", "ns").GenericLauncherAndWorker().Parallelism(2).Obj(),
			wantJob: testingmpijob.MakeMPIJob("mpijob", "ns").GenericLauncherAndWorker().Parallelism(2).Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("mpijob", "ns").
					PodSets(
						*utiltesting.MakePodSet("launcher", 1).Obj(),
						*utiltesting.MakePodSet("worker", 2).Obj(),
					).
					Obj(),
			},
		},
		"workload is created with podsets and workloadPriorityClass": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
			},
			job: testingmpijob.MakeMPIJob("mpijob", "ns").GenericLauncherAndWorker().Parallelism(2).WorkloadPriorityClass("test-wpc").Obj(),
			priorityClasses: []client.Object{
				baseWPCWrapper.Obj(),
			},
			wantJob: testingmpijob.MakeMPIJob("mpijob", "ns").GenericLauncherAndWorker().Parallelism(2).WorkloadPriorityClass("test-wpc").Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("mpijob", "ns").
					PodSets(
						*utiltesting.MakePodSet("launcher", 1).Obj(),
						*utiltesting.MakePodSet("worker", 2).Obj(),
					).PriorityClass("test-wpc").Priority(100).
					PriorityClassSource(constants.WorkloadPriorityClassSource).
					Obj(),
			},
		},
		"workload is created with podsets and PriorityClass": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
			},
			job: testingmpijob.MakeMPIJob("mpijob", "ns").GenericLauncherAndWorker().Parallelism(2).PriorityClass("test-pc").Obj(),
			priorityClasses: []client.Object{
				basePCWrapper.Obj(),
			},
			wantJob: testingmpijob.MakeMPIJob("mpijob", "ns").GenericLauncherAndWorker().Parallelism(2).PriorityClass("test-pc").Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("mpijob", "ns").
					PodSets(
						*utiltesting.MakePodSet("launcher", 1).Obj(),
						*utiltesting.MakePodSet("worker", 2).Obj(),
					).PriorityClass("test-pc").Priority(200).
					PriorityClassSource(constants.PodPriorityClassSource).
					Obj(),
			},
		},
		"workload is created with podsets, workloadPriorityClass and PriorityClass": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
			},
			job: testingmpijob.MakeMPIJob("mpijob", "ns").GenericLauncherAndWorker().Parallelism(2).
				WorkloadPriorityClass("test-wpc").PriorityClass("test-pc").Obj(),
			priorityClasses: []client.Object{
				basePCWrapper.Obj(), baseWPCWrapper.Obj(),
			},
			wantJob: testingmpijob.MakeMPIJob("mpijob", "ns").GenericLauncherAndWorker().Parallelism(2).
				WorkloadPriorityClass("test-wpc").PriorityClass("test-pc").Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("mpijob", "ns").
					PodSets(
						*utiltesting.MakePodSet("launcher", 1).Obj(),
						*utiltesting.MakePodSet("worker", 2).Obj(),
					).PriorityClass("test-wpc").Priority(100).
					PriorityClassSource(constants.WorkloadPriorityClassSource).
					Obj(),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			clientBuilder := utiltesting.NewClientBuilder(kfmpi.AddToScheme)
			if err := SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder)); err != nil {
				t.Fatalf("Could not setup indexes: %v", err)
			}
			objs := append(tc.priorityClasses, tc.job)
			kClient := clientBuilder.WithObjects(objs...).Build()
			recorder := record.NewBroadcaster().NewRecorder(kClient.Scheme(), corev1.EventSource{Component: "test"})
			reconciler := NewReconciler(kClient, recorder, tc.reconcilerOptions...)

			jobKey := client.ObjectKeyFromObject(tc.job)
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: jobKey,
			})
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Reconcile returned error (-want,+got):\n%s", diff)
			}

			var gotMpiJob kfmpi.MPIJob
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
