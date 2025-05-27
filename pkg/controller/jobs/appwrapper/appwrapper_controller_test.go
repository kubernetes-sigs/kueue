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

package appwrapper

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	awv1beta2 "github.com/project-codeflare/appwrapper/api/v1beta2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	controllerconsts "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingappwrapper "sigs.k8s.io/kueue/pkg/util/testingjobs/appwrapper"
	utiltestingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingpytorchjob "sigs.k8s.io/kueue/pkg/util/testingjobs/pytorchjob"
)

var (
	podSetCmpOpts = cmp.Options{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(kueue.PodSet{}, "Template"), // complex due to defaulting during serialization/deserialization
	}
)

func TestPodSets(t *testing.T) {
	pytorchJob := testingpytorchjob.MakePyTorchJob("pytorchjob", "ns").
		PyTorchReplicaSpecs(
			testingpytorchjob.PyTorchReplicaSpecRequirement{
				ReplicaType:  kftraining.PyTorchJobReplicaTypeMaster,
				ReplicaCount: 1,
			},
			testingpytorchjob.PyTorchReplicaSpecRequirement{
				ReplicaType:  kftraining.PyTorchJobReplicaTypeWorker,
				ReplicaCount: 4,
			},
		).
		SetTypeMeta().
		Obj()
	pytorchJobTAS := testingpytorchjob.MakePyTorchJob("pytorchjob", "ns").
		PyTorchReplicaSpecs(
			testingpytorchjob.PyTorchReplicaSpecRequirement{
				ReplicaType:  kftraining.PyTorchJobReplicaTypeMaster,
				ReplicaCount: 1,
				Annotations: map[string]string{
					kueuealpha.PodSetRequiredTopologyAnnotation: "cloud.com/rack",
				},
			},
			testingpytorchjob.PyTorchReplicaSpecRequirement{
				ReplicaType:  kftraining.PyTorchJobReplicaTypeWorker,
				ReplicaCount: 4,
				Annotations: map[string]string{
					kueuealpha.PodSetPreferredTopologyAnnotation: "cloud.com/block",
				},
			},
		).
		SetTypeMeta().
		Obj()
	batchJob := utiltestingjob.MakeJob("job", "ns").
		SetTypeMeta().
		Parallelism(2).
		Obj()

	testCases := map[string]struct {
		job                           *awv1beta2.AppWrapper
		wantPodSets                   []kueue.PodSet
		enableTopologyAwareScheduling bool
	}{
		"no annotations": {
			job: testingappwrapper.MakeAppWrapper("aw", "ns").
				Component(testingappwrapper.Component{Template: pytorchJob}).
				Component(testingappwrapper.Component{Template: batchJob}).
				Obj(),
			wantPodSets: []kueue.PodSet{
				*utiltesting.MakePodSet("aw-0", 1).
					PodSpec(pytorchJob.Spec.PyTorchReplicaSpecs[kftraining.PyTorchJobReplicaTypeMaster].Template.Spec).
					Obj(),
				*utiltesting.MakePodSet("aw-1", 4).
					PodSpec(pytorchJob.Spec.PyTorchReplicaSpecs[kftraining.PyTorchJobReplicaTypeWorker].Template.Spec).
					Obj(),
				*utiltesting.MakePodSet("aw-2", 2).
					PodSpec(batchJob.Spec.Template.Spec).
					Obj(),
			},
			enableTopologyAwareScheduling: false,
		},
		"with required and preferred topology annotation": {
			job: testingappwrapper.MakeAppWrapper("aw", "ns").
				Component(testingappwrapper.Component{Template: pytorchJobTAS}).
				Obj(),

			wantPodSets: []kueue.PodSet{
				*utiltesting.MakePodSet("aw-0", 1).
					PodSpec(pytorchJobTAS.Spec.PyTorchReplicaSpecs[kftraining.PyTorchJobReplicaTypeMaster].Template.Spec).
					Annotations(map[string]string{kueuealpha.PodSetRequiredTopologyAnnotation: "cloud.com/rack"}).
					RequiredTopologyRequest("cloud.com/rack").
					PodIndexLabel(ptr.To(kftraining.ReplicaIndexLabel)).
					Obj(),
				*utiltesting.MakePodSet("aw-1", 4).
					PodSpec(pytorchJobTAS.Spec.PyTorchReplicaSpecs[kftraining.PyTorchJobReplicaTypeWorker].Template.Spec).
					Annotations(map[string]string{kueuealpha.PodSetPreferredTopologyAnnotation: "cloud.com/block"}).
					PreferredTopologyRequest("cloud.com/block").
					PodIndexLabel(ptr.To(kftraining.ReplicaIndexLabel)).
					Obj(),
			},

			enableTopologyAwareScheduling: true,
		},
		"without required and preferred topology annotation if TAS is disabled": {
			job: testingappwrapper.MakeAppWrapper("aw", "ns").
				Component(testingappwrapper.Component{Template: pytorchJobTAS}).
				Obj(),

			wantPodSets: []kueue.PodSet{
				*utiltesting.MakePodSet("aw-0", 1).
					PodSpec(pytorchJobTAS.Spec.PyTorchReplicaSpecs[kftraining.PyTorchJobReplicaTypeMaster].Template.Spec).
					Annotations(map[string]string{kueuealpha.PodSetRequiredTopologyAnnotation: "cloud.com/rack"}).
					Obj(),
				*utiltesting.MakePodSet("aw-1", 4).
					PodSpec(pytorchJobTAS.Spec.PyTorchReplicaSpecs[kftraining.PyTorchJobReplicaTypeWorker].Template.Spec).
					Annotations(map[string]string{kueuealpha.PodSetPreferredTopologyAnnotation: "cloud.com/block"}).
					Obj(),
			},

			enableTopologyAwareScheduling: false,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, tc.enableTopologyAwareScheduling)
			gotPodSets, err := fromObject(tc.job).PodSets()
			if err != nil {
				t.Fatalf("failed to get pod sets: %v", err)
			}
			if diff := cmp.Diff(tc.wantPodSets, gotPodSets, podSetCmpOpts...); diff != "" {
				t.Errorf("pod sets mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

var (
	jobCmpOpts = cmp.Options{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(awv1beta2.AppWrapper{}, "TypeMeta", "ObjectMeta"),
	}
	workloadCmpOpts = cmp.Options{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(kueue.Workload{}, "TypeMeta"),
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "Name", "Labels", "ResourceVersion", "OwnerReferences", "Finalizers"), cmpopts.IgnoreFields(kueue.WorkloadSpec{}, "Priority"),
		cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
		cmpopts.IgnoreFields(kueue.PodSet{}, "Template"),
	}
)

func TestReconciler(t *testing.T) {
	testNamespace := utiltesting.MakeNamespaceWrapper("ns").Label(corev1.LabelMetadataName, "ns").Obj()
	cases := map[string]struct {
		reconcilerOptions []jobframework.Option
		job               *awv1beta2.AppWrapper
		flavors           []kueue.ResourceFlavor
		workloads         []kueue.Workload
		wantJob           *awv1beta2.AppWrapper
		wantWorkloads     []kueue.Workload
		wantErr           error
	}{
		"workload is created with podsets": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
				jobframework.WithManagedJobsNamespaceSelector(labels.Everything()),
			},
			job: testingappwrapper.MakeAppWrapper("aw", "ns").
				Component(testingappwrapper.Component{
					Template: utiltestingjob.MakeJob("job", "ns").Parallelism(2).SetTypeMeta().Obj(),
				}).
				Obj(),
			wantJob: testingappwrapper.MakeAppWrapper("aw", "ns").
				Component(testingappwrapper.Component{
					Template: utiltestingjob.MakeJob("job", "ns").Parallelism(2).SetTypeMeta().Obj(),
				}).
				Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("aw", "ns").
					PodSets(
						*utiltesting.MakePodSet("aw-0", 2).Obj(),
					).
					Obj(),
			},
		},

		"workload is created with a ProvReq annotation": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
				jobframework.WithManagedJobsNamespaceSelector(labels.Everything()),
			},
			job: testingappwrapper.MakeAppWrapper("aw", "ns").
				Component(testingappwrapper.Component{
					Template: utiltestingjob.MakeJob("job", "ns").Parallelism(2).SetTypeMeta().Obj(),
				}).
				Annotations(map[string]string{
					controllerconsts.ProvReqAnnotationPrefix + "test-annotation": "test-val",
					"invalid-provreq-prefix/test-annotation-2":                   "test-val-2",
				}).
				Obj(),
			wantJob: testingappwrapper.MakeAppWrapper("aw", "ns").
				Component(testingappwrapper.Component{
					Template: utiltestingjob.MakeJob("job", "ns").Parallelism(2).SetTypeMeta().Obj(),
				}).
				Annotations(map[string]string{
					controllerconsts.ProvReqAnnotationPrefix + "test-annotation": "test-val",
					"invalid-provreq-prefix/test-annotation-2":                   "test-val-2",
				}).
				Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("aw", "ns").
					Annotations(map[string]string{controllerconsts.ProvReqAnnotationPrefix + "test-annotation": "test-val"}).
					PodSets(
						*utiltesting.MakePodSet("aw-0", 2).Obj(),
					).
					Obj(),
			},
		},

		"workload isn't created due to manageJobsWithoutQueueName=false": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(false),
			},
			job: testingappwrapper.MakeAppWrapper("aw", "ns").
				Component(testingappwrapper.Component{
					Template: utiltestingjob.MakeJob("job", "ns").Parallelism(2).SetTypeMeta().Obj(),
				}).
				Obj(),
			wantJob: testingappwrapper.MakeAppWrapper("aw", "ns").
				Component(testingappwrapper.Component{
					Template: utiltestingjob.MakeJob("job", "ns").Parallelism(2).SetTypeMeta().Obj(),
				}).
				Obj(),
			wantWorkloads: []kueue.Workload{},
		},

		/*
			TODO: Disabled until https://github.com/kubernetes-sigs/kueue/issues/3103 is resolved.
			      Too hard to simulate the defaulting behavior for PodSets() to manually construct the PodSets for workloads

			"when workload is admitted, job gets node selectors": {
				flavors: []kueue.ResourceFlavor{
					*utiltesting.MakeResourceFlavor("default").Obj(),
					*utiltesting.MakeResourceFlavor("on-demand").NodeLabel("provisioning", "on-demand").Obj(),
					*utiltesting.MakeResourceFlavor("spot").NodeLabel("provisioning", "spot").Obj(),
				},
				job: testingappwrapper.MakeAppWrapper("aw", "ns").
					Component(utiltestingjob.MakeJob("job", "ns").Parallelism(1).Request(corev1.ResourceCPU, "1").SetTypeMeta().Obj()).
					Component(utiltestingjob.MakeJob("job", "ns").Parallelism(1).Request(corev1.ResourceCPU, "1").SetTypeMeta().Obj()).
					Component(utiltestingjob.MakeJob("job", "ns").Parallelism(1).Request(corev1.ResourceCPU, "1").SetTypeMeta().Obj()).
					Suspend(true).
					Queue("foo").
					Obj(),
				workloads: []kueue.Workload{
					*utiltesting.MakeWorkload("a", "ns").
						PodSets(
							*utiltesting.MakePodSet("aw-0", 1).Request(corev1.ResourceCPU, "1").Obj(),
							*utiltesting.MakePodSet("aw-1", 1).Request(corev1.ResourceCPU, "1").Obj(),
							*utiltesting.MakePodSet("aw-2", 1).Request(corev1.ResourceCPU, "1")Obj(),
						).
						ReserveQuota(utiltesting.MakeAdmission("cq").
							PodSets(
								kueue.PodSetAssignment{
									Name: "aw-0",
									Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
										corev1.ResourceCPU: "on-demand",
									},
								},
								kueue.PodSetAssignment{
									Name: "aw-1",
									Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
										corev1.ResourceCPU: "spot",
									},
								},
								kueue.PodSetAssignment{
									Name: "aw-2",
									Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
										corev1.ResourceCPU: "default",
									},
								},
							).
							Obj()).
						Admitted(true).
						Obj(),
				},
				wantJob: testingappwrapper.MakeAppWrapper("aw", "ns").
					ComponentWithInfos(
						utiltestingjob.MakeJob("job", "ns").Parallelism(1).Request(corev1.ResourceCPU, "1").SetTypeMeta().Obj(),
						awv1beta2.AppWrapperPodSetInfo{NodeSelector: map[string]string{"provisioning": "on-demand"}},
					).
					ComponentWithInfos(
						utiltestingjob.MakeJob("job", "ns").Parallelism(1).Request(corev1.ResourceCPU, "1").SetTypeMeta().Obj(),
						awv1beta2.AppWrapperPodSetInfo{NodeSelector: map[string]string{"provisioning": "spot"}},
					).
					Component(utiltestingjob.MakeJob("job", "ns").Parallelism(1).Request(corev1.ResourceCPU, "1").SetTypeMeta().Obj()).
					Suspend(false).
					Queue("foo").
					Obj(),

				wantWorkloads: []kueue.Workload{
					*utiltesting.MakeWorkload("a", "ns").
						PodSets(
							*utiltesting.MakePodSet("aw-0", 1).Request(corev1.ResourceCPU, "1").Obj(),
							*utiltesting.MakePodSet("aw-1", 1).Request(corev1.ResourceCPU, "1").Obj(),
							*utiltesting.MakePodSet("aw-2", 1).Request(corev1.ResourceCPU, "1").Obj(),
						).
						ReserveQuota(utiltesting.MakeAdmission("cq").
							PodSets(
								kueue.PodSetAssignment{
									Name: "aw-0",
									Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
										corev1.ResourceCPU: "on-demand",
									},
								},
								kueue.PodSetAssignment{
									Name: "aw-1",
									Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
										corev1.ResourceCPU: "spot",
									},
								},
								kueue.PodSetAssignment{
									Name: "aw-2",
									Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
										corev1.ResourceCPU: "default",
									},
								},
							).
							Obj()).
						Admitted(true).
						Obj(),
				},
			},
		*/

		"workload shouldn't be recreated for completed job": {
			job: testingappwrapper.MakeAppWrapper("aw", "ns").
				Component(testingappwrapper.Component{
					Template: utiltestingjob.MakeJob("job", "ns").Parallelism(2).SetTypeMeta().Obj(),
				}).
				Queue("foo").
				SetPhase(awv1beta2.AppWrapperSucceeded).
				Obj(),
			wantJob: testingappwrapper.MakeAppWrapper("aw", "ns").
				Component(testingappwrapper.Component{
					Template: utiltestingjob.MakeJob("job", "ns").Parallelism(2).SetTypeMeta().Obj(),
				}).
				Queue("foo").
				SetPhase(awv1beta2.AppWrapperSucceeded).
				Obj(),
			wantWorkloads: []kueue.Workload{},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			kcBuilder := utiltesting.NewClientBuilder(awv1beta2.AddToScheme)
			if err := SetupIndexes(ctx, utiltesting.AsIndexer(kcBuilder)); err != nil {
				t.Fatalf("Failed to setup indexes: %v", err)
			}
			kcBuilder = kcBuilder.
				WithObjects(tc.job, testNamespace).
				WithLists(&kueue.ResourceFlavorList{Items: tc.flavors})
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

			var gotAppWrapper awv1beta2.AppWrapper
			if err = kClient.Get(ctx, jobKey, &gotAppWrapper); err != nil {
				t.Fatalf("Could not get Job after reconcile: %v", err)
			}
			if diff := cmp.Diff(tc.wantJob, &gotAppWrapper, jobCmpOpts...); diff != "" {
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
