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

package mpijob

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	kfmpi "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
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
		job                           *MPIJob
		wantPodSets                   []kueue.PodSet
		enableTopologyAwareScheduling bool
	}{
		"no annotations": {
			job: (*MPIJob)(jobTemplate.DeepCopy()),
			wantPodSets: []kueue.PodSet{
				*utiltesting.MakePodSet(kueue.NewPodSetReference(string(kfmpi.MPIReplicaTypeLauncher)), 1).
					PodSpec(jobTemplate.Clone().Spec.MPIReplicaSpecs[kfmpi.MPIReplicaTypeLauncher].Template.Spec).
					Obj(),
				*utiltesting.MakePodSet(kueue.NewPodSetReference(string(kfmpi.MPIReplicaTypeWorker)), 3).
					PodSpec(jobTemplate.Clone().Spec.MPIReplicaSpecs[kfmpi.MPIReplicaTypeWorker].Template.Spec).
					Obj(),
			},
			enableTopologyAwareScheduling: false,
		},
		"with required topology annotation": {
			job: (*MPIJob)(jobTemplate.Clone().
				PodAnnotation(
					kfmpi.MPIReplicaTypeLauncher,
					kueue.PodSetRequiredTopologyAnnotation,
					"cloud.com/block",
				).
				PodAnnotation(
					kfmpi.MPIReplicaTypeWorker,
					kueue.PodSetRequiredTopologyAnnotation,
					"cloud.com/block",
				).
				Obj(),
			),
			wantPodSets: []kueue.PodSet{
				*utiltesting.MakePodSet(kueue.NewPodSetReference(string(kfmpi.MPIReplicaTypeLauncher)), 1).
					PodSpec(jobTemplate.Clone().Spec.MPIReplicaSpecs[kfmpi.MPIReplicaTypeLauncher].Template.Spec).
					Annotations(map[string]string{kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block"}).
					RequiredTopologyRequest("cloud.com/block").
					PodIndexLabel(ptr.To(kfmpi.ReplicaIndexLabel)).
					Obj(),
				*utiltesting.MakePodSet(kueue.NewPodSetReference(string(kfmpi.MPIReplicaTypeWorker)), 3).
					PodSpec(jobTemplate.Clone().Spec.MPIReplicaSpecs[kfmpi.MPIReplicaTypeWorker].Template.Spec).
					Annotations(map[string]string{kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block"}).
					RequiredTopologyRequest("cloud.com/block").
					PodIndexLabel(ptr.To(kfmpi.ReplicaIndexLabel)).
					Obj(),
			},
			enableTopologyAwareScheduling: true,
		},
		"with preferred topology annotation": {
			job: (*MPIJob)(jobTemplate.Clone().
				PodAnnotation(
					kfmpi.MPIReplicaTypeLauncher,
					kueue.PodSetPreferredTopologyAnnotation,
					"cloud.com/block",
				).
				PodAnnotation(
					kfmpi.MPIReplicaTypeWorker,
					kueue.PodSetPreferredTopologyAnnotation,
					"cloud.com/block",
				).
				Obj(),
			),
			wantPodSets: []kueue.PodSet{
				*utiltesting.MakePodSet(kueue.NewPodSetReference(string(kfmpi.MPIReplicaTypeLauncher)), 1).
					PodSpec(jobTemplate.Clone().Spec.MPIReplicaSpecs[kfmpi.MPIReplicaTypeLauncher].Template.Spec).
					Annotations(map[string]string{kueue.PodSetPreferredTopologyAnnotation: "cloud.com/block"}).
					PreferredTopologyRequest("cloud.com/block").
					PodIndexLabel(ptr.To(kfmpi.ReplicaIndexLabel)).
					Obj(),
				*utiltesting.MakePodSet(kueue.NewPodSetReference(string(kfmpi.MPIReplicaTypeWorker)), 3).
					PodSpec(jobTemplate.Clone().Spec.MPIReplicaSpecs[kfmpi.MPIReplicaTypeWorker].Template.Spec).
					Annotations(map[string]string{kueue.PodSetPreferredTopologyAnnotation: "cloud.com/block"}).
					PreferredTopologyRequest("cloud.com/block").
					PodIndexLabel(ptr.To(kfmpi.ReplicaIndexLabel)).
					Obj(),
			},
			enableTopologyAwareScheduling: true,
		},
		"without required and preferred topology annotation if TAS is disabled": {
			job: (*MPIJob)(jobTemplate.Clone().
				PodAnnotation(
					kfmpi.MPIReplicaTypeLauncher,
					kueue.PodSetRequiredTopologyAnnotation,
					"cloud.com/block",
				).
				PodAnnotation(
					kfmpi.MPIReplicaTypeWorker,
					kueue.PodSetRequiredTopologyAnnotation,
					"cloud.com/block",
				).
				Obj(),
			),
			wantPodSets: []kueue.PodSet{
				*utiltesting.MakePodSet(kueue.NewPodSetReference(string(kfmpi.MPIReplicaTypeLauncher)), 1).
					PodSpec(jobTemplate.Clone().Spec.MPIReplicaSpecs[kfmpi.MPIReplicaTypeLauncher].Template.Spec).
					Annotations(map[string]string{kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block"}).
					Obj(),
				*utiltesting.MakePodSet(kueue.NewPodSetReference(string(kfmpi.MPIReplicaTypeWorker)), 3).
					PodSpec(jobTemplate.Clone().Spec.MPIReplicaSpecs[kfmpi.MPIReplicaTypeWorker].Template.Spec).
					Annotations(map[string]string{kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block"}).
					Obj(),
			},
			enableTopologyAwareScheduling: false,
		},
		"without preferred topology annotation if TAS is disabled": {
			job: (*MPIJob)(jobTemplate.Clone().
				PodAnnotation(
					kfmpi.MPIReplicaTypeLauncher,
					kueue.PodSetPreferredTopologyAnnotation,
					"cloud.com/block",
				).
				PodAnnotation(
					kfmpi.MPIReplicaTypeWorker,
					kueue.PodSetPreferredTopologyAnnotation,
					"cloud.com/block",
				).
				Obj(),
			),
			wantPodSets: []kueue.PodSet{
				*utiltesting.MakePodSet(kueue.NewPodSetReference(string(kfmpi.MPIReplicaTypeLauncher)), 1).
					PodSpec(jobTemplate.Clone().Spec.MPIReplicaSpecs[kfmpi.MPIReplicaTypeLauncher].Template.Spec).
					Annotations(map[string]string{kueue.PodSetPreferredTopologyAnnotation: "cloud.com/block"}).
					Obj(),
				*utiltesting.MakePodSet(kueue.NewPodSetReference(string(kfmpi.MPIReplicaTypeWorker)), 3).
					PodSpec(jobTemplate.Clone().Spec.MPIReplicaSpecs[kfmpi.MPIReplicaTypeWorker].Template.Spec).
					Annotations(map[string]string{kueue.PodSetPreferredTopologyAnnotation: "cloud.com/block"}).
					Obj(),
			},
			enableTopologyAwareScheduling: false,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, tc.enableTopologyAwareScheduling)
			gotPodSets, err := tc.job.PodSets()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if diff := cmp.Diff(tc.wantPodSets, gotPodSets); diff != "" {
				t.Errorf("pod sets mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

var (
	jobCmpOpts = cmp.Options{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(kfmpi.MPIJob{}, "TypeMeta", "ObjectMeta"),
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
	baseWPCWrapper := utiltesting.MakeWorkloadPriorityClass("test-wpc").
		PriorityValue(100)
	basePCWrapper := utiltesting.MakePriorityClass("test-pc").
		PriorityValue(200)
	testNamespace := utiltesting.MakeNamespaceWrapper("ns").Label(corev1.LabelMetadataName, "ns").Obj()
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
				jobframework.WithManagedJobsNamespaceSelector(labels.Everything()),
			},
			job:     testingmpijob.MakeMPIJob("mpijob", "ns").GenericLauncherAndWorker().Parallelism(2).Obj(),
			wantJob: testingmpijob.MakeMPIJob("mpijob", "ns").GenericLauncherAndWorker().Parallelism(2).Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("mpijob", "ns").
					PodSets(
						*utiltesting.MakePodSet("launcher", 1).
							PodIndexLabel(ptr.To(kfmpi.ReplicaIndexLabel)).
							Obj(),
						*utiltesting.MakePodSet("worker", 2).
							PodIndexLabel(ptr.To(kfmpi.ReplicaIndexLabel)).
							Obj(),
					).
					Obj(),
			},
		},
		"workload is created with podsets and workloadPriorityClass": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
				jobframework.WithManagedJobsNamespaceSelector(labels.Everything()),
			},
			job: testingmpijob.MakeMPIJob("mpijob", "ns").GenericLauncherAndWorker().Parallelism(2).WorkloadPriorityClass("test-wpc").Obj(),
			priorityClasses: []client.Object{
				baseWPCWrapper.Obj(),
			},
			wantJob: testingmpijob.MakeMPIJob("mpijob", "ns").GenericLauncherAndWorker().Parallelism(2).WorkloadPriorityClass("test-wpc").Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("mpijob", "ns").
					PodSets(
						*utiltesting.MakePodSet("launcher", 1).
							PodIndexLabel(ptr.To(kfmpi.ReplicaIndexLabel)).
							Obj(),
						*utiltesting.MakePodSet("worker", 2).
							PodIndexLabel(ptr.To(kfmpi.ReplicaIndexLabel)).
							Obj(),
					).PriorityClass("test-wpc").Priority(100).
					PriorityClassSource(constants.WorkloadPriorityClassSource).
					Obj(),
			},
		},
		"workload is created with podsets and PriorityClass": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
				jobframework.WithManagedJobsNamespaceSelector(labels.Everything()),
			},
			job: testingmpijob.MakeMPIJob("mpijob", "ns").GenericLauncherAndWorker().Parallelism(2).PriorityClass("test-pc").Obj(),
			priorityClasses: []client.Object{
				basePCWrapper.Obj(),
			},
			wantJob: testingmpijob.MakeMPIJob("mpijob", "ns").GenericLauncherAndWorker().Parallelism(2).PriorityClass("test-pc").Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("mpijob", "ns").
					PodSets(
						*utiltesting.MakePodSet("launcher", 1).
							PodIndexLabel(ptr.To(kfmpi.ReplicaIndexLabel)).
							Obj(),
						*utiltesting.MakePodSet("worker", 2).
							PodIndexLabel(ptr.To(kfmpi.ReplicaIndexLabel)).
							Obj(),
					).PriorityClass("test-pc").Priority(200).
					PriorityClassSource(constants.PodPriorityClassSource).
					Obj(),
			},
		},
		"workload is created with podsets, workloadPriorityClass and PriorityClass": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
				jobframework.WithManagedJobsNamespaceSelector(labels.Everything()),
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
						*utiltesting.MakePodSet("launcher", 1).
							PodIndexLabel(ptr.To(kfmpi.ReplicaIndexLabel)).
							Obj(),
						*utiltesting.MakePodSet("worker", 2).
							PodIndexLabel(ptr.To(kfmpi.ReplicaIndexLabel)).
							Obj(),
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
			indexer := utiltesting.AsIndexer(clientBuilder)
			if err := SetupIndexes(ctx, indexer); err != nil {
				t.Fatalf("Could not setup indexes: %v", err)
			}
			objs := append(tc.priorityClasses, tc.job, testNamespace)
			kClient := clientBuilder.WithObjects(objs...).Build()
			recorder := record.NewBroadcaster().NewRecorder(kClient.Scheme(), corev1.EventSource{Component: "test"})
			reconciler, err := NewReconciler(ctx, kClient, indexer, recorder, tc.reconcilerOptions...)
			if err != nil {
				t.Errorf("Error creating the reconciler: %v", err)
			}

			jobKey := client.ObjectKeyFromObject(tc.job)
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
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
