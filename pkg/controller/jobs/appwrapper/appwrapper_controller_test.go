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
	"errors"
	"maps"
	"strconv"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	kfmpi "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	awv1beta2 "github.com/project-codeflare/appwrapper/api/v1beta2"
	awutils "github.com/project-codeflare/appwrapper/pkg/utils"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/component-base/featuregate"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	controllerconsts "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/podset"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingappwrapper "sigs.k8s.io/kueue/pkg/util/testingjobs/appwrapper"
	utiltestingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingmpijob "sigs.k8s.io/kueue/pkg/util/testingjobs/mpijob"
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
					kueue.PodSetRequiredTopologyAnnotation: "cloud.com/rack",
				},
			},
			testingpytorchjob.PyTorchReplicaSpecRequirement{
				ReplicaType:  kftraining.PyTorchJobReplicaTypeWorker,
				ReplicaCount: 4,
				Annotations: map[string]string{
					kueue.PodSetPreferredTopologyAnnotation: "cloud.com/block",
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
		job          *awv1beta2.AppWrapper
		wantPodSets  []kueue.PodSet
		featureGates map[featuregate.Feature]bool
	}{
		"no annotations": {
			job: testingappwrapper.MakeAppWrapper("aw", "ns").
				Component(testingappwrapper.Component{Template: pytorchJob}).
				Component(testingappwrapper.Component{Template: batchJob}).
				Obj(),
			wantPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet("aw-0", 1).
					PodSpec(pytorchJob.Spec.PyTorchReplicaSpecs[kftraining.PyTorchJobReplicaTypeMaster].Template.Spec).
					Obj(),
				*utiltestingapi.MakePodSet("aw-1", 4).
					PodSpec(pytorchJob.Spec.PyTorchReplicaSpecs[kftraining.PyTorchJobReplicaTypeWorker].Template.Spec).
					Obj(),
				*utiltestingapi.MakePodSet("aw-2", 2).
					PodSpec(batchJob.Spec.Template.Spec).
					Obj(),
			},
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
		},
		"with required and preferred topology annotation": {
			job: testingappwrapper.MakeAppWrapper("aw", "ns").
				Component(testingappwrapper.Component{Template: pytorchJobTAS}).
				Obj(),

			wantPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet("aw-0", 1).
					PodSpec(pytorchJobTAS.Spec.PyTorchReplicaSpecs[kftraining.PyTorchJobReplicaTypeMaster].Template.Spec).
					Annotations(map[string]string{kueue.PodSetRequiredTopologyAnnotation: "cloud.com/rack"}).
					RequiredTopologyRequest("cloud.com/rack").
					PodIndexLabel(new(kftraining.ReplicaIndexLabel)).
					Obj(),
				*utiltestingapi.MakePodSet("aw-1", 4).
					PodSpec(pytorchJobTAS.Spec.PyTorchReplicaSpecs[kftraining.PyTorchJobReplicaTypeWorker].Template.Spec).
					Annotations(map[string]string{kueue.PodSetPreferredTopologyAnnotation: "cloud.com/block"}).
					PreferredTopologyRequest("cloud.com/block").
					PodIndexLabel(new(kftraining.ReplicaIndexLabel)).
					Obj(),
			},

			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: true},
		},
		"without required and preferred topology annotation if TAS is disabled": {
			job: testingappwrapper.MakeAppWrapper("aw", "ns").
				Component(testingappwrapper.Component{Template: pytorchJobTAS}).
				Obj(),

			wantPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet("aw-0", 1).
					PodSpec(pytorchJobTAS.Spec.PyTorchReplicaSpecs[kftraining.PyTorchJobReplicaTypeMaster].Template.Spec).
					Annotations(map[string]string{kueue.PodSetRequiredTopologyAnnotation: "cloud.com/rack"}).
					Obj(),
				*utiltestingapi.MakePodSet("aw-1", 4).
					PodSpec(pytorchJobTAS.Spec.PyTorchReplicaSpecs[kftraining.PyTorchJobReplicaTypeWorker].Template.Spec).
					Annotations(map[string]string{kueue.PodSetPreferredTopologyAnnotation: "cloud.com/block"}).
					Obj(),
			},

			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			ctx, _ := utiltesting.ContextWithLog(t)
			gotPodSets, err := fromObject(tc.job).PodSets(ctx, nil)
			if err != nil {
				t.Fatalf("failed to get pod sets: %v", err)
			}
			if diff := cmp.Diff(tc.wantPodSets, gotPodSets, podSetCmpOpts...); diff != "" {
				t.Errorf("pod sets mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRunWithPodSetsInfo(t *testing.T) {
	testCases := map[string]struct {
		grouped         bool
		templateOffset  string
		infoAnnotations map[string]string
		wantAnnotations map[string]string
		disableTAS      bool
	}{
		"grouped PyTorchJob": {
			grouped:         true,
			infoAnnotations: map[string]string{"admission": "value"},
			wantAnnotations: map[string]string{"admission": "value", kueue.PodIndexOffsetAnnotation: "0"},
		},
		"grouped PyTorchJob with nil admission annotations": {
			grouped:         true,
			wantAnnotations: map[string]string{kueue.PodIndexOffsetAnnotation: "0"},
		},
		"explicit template offset is preserved": {
			grouped:         true,
			templateOffset:  "2",
			infoAnnotations: map[string]string{"admission": "value"},
			wantAnnotations: map[string]string{"admission": "value"},
		},
		"explicit admission offset is preserved": {
			grouped:         true,
			infoAnnotations: map[string]string{kueue.PodIndexOffsetAnnotation: "2"},
			wantAnnotations: map[string]string{kueue.PodIndexOffsetAnnotation: "2"},
		},
		"ungrouped PyTorchJob": {},
		"TAS disabled": {
			grouped:    true,
			disableTAS: true,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, !tc.disableTAS)
			ctx, _ := utiltesting.ContextWithLog(t)
			pytorchJob := testingpytorchjob.MakePyTorchJob("pytorchjob", "ns").
				PyTorchReplicaSpecsDefault().
				Parallelism(4).
				SetTypeMeta()
			for _, replicaType := range []kftraining.ReplicaType{kftraining.PyTorchJobReplicaTypeMaster, kftraining.PyTorchJobReplicaTypeWorker} {
				if tc.grouped {
					pytorchJob.PodAnnotation(replicaType, kueue.PodSetGroupName, "training").
						PodAnnotation(replicaType, kueue.PodSetRequiredTopologyAnnotation, "cloud.com/rack")
				}
				if tc.templateOffset != "" {
					pytorchJob.PodAnnotation(replicaType, kueue.PodIndexOffsetAnnotation, tc.templateOffset)
				}
			}
			batchJob := utiltestingjob.MakeJob("job", "ns").
				PodAnnotation(kueue.PodSetGroupName, "other").
				SetTypeMeta().
				Obj()
			aw := testingappwrapper.MakeAppWrapper("aw", "ns").
				Component(testingappwrapper.Component{Template: batchJob}).
				Component(testingappwrapper.Component{Template: pytorchJob.Obj()}).
				Obj()
			originalComponents := aw.DeepCopy().Spec.Components
			originalAnnotations := maps.Clone(tc.infoAnnotations)
			podSetsInfo := []podset.PodSetInfo{
				{NodeSelector: map[string]string{"node": "batch"}},
				{Annotations: tc.infoAnnotations, NodeSelector: map[string]string{"node": "master"}},
				{Annotations: tc.infoAnnotations, NodeSelector: map[string]string{"node": "worker"}},
			}
			wantInfos := [][]awv1beta2.AppWrapperPodSetInfo{
				{{NodeSelector: map[string]string{"node": "batch"}}},
				{
					{Annotations: tc.wantAnnotations, NodeSelector: map[string]string{"node": "master"}},
					{Annotations: tc.wantAnnotations, NodeSelector: map[string]string{"node": "worker"}},
				},
			}

			if err := fromObject(aw).RunWithPodSetsInfo(ctx, nil, podSetsInfo); err != nil {
				t.Fatalf("RunWithPodSetsInfo() failed: %v", err)
			}
			if aw.Spec.Suspend {
				t.Error("AppWrapper is still suspended")
			}
			for i := range aw.Spec.Components {
				if diff := cmp.Diff(wantInfos[i], aw.Spec.Components[i].PodSetInfos); diff != "" {
					t.Errorf("component %d pod set infos mismatch (-want +got):\n%s", i, diff)
				}
				if diff := cmp.Diff(originalComponents[i].Template, aw.Spec.Components[i].Template); diff != "" {
					t.Errorf("component %d template changed (-want +got):\n%s", i, diff)
				}
			}
			for i := 1; i < len(podSetsInfo); i++ {
				if diff := cmp.Diff(originalAnnotations, podSetsInfo[i].Annotations); diff != "" {
					t.Errorf("input pod set %d annotations changed (-want +got):\n%s", i, diff)
				}
			}
		})
	}
}

func TestRunWithPodSetsInfoInvalidCount(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, true)
	for _, count := range []int{1, 3} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			aw := testingappwrapper.MakeAppWrapper("aw", "ns").
				Component(testingappwrapper.Component{Template: testingpytorchjob.MakePyTorchJob("pytorchjob", "ns").
					PyTorchReplicaSpecsDefault().SetTypeMeta().Obj()}).
				Obj()
			if err := fromObject(aw).RunWithPodSetsInfo(ctx, nil, make([]podset.PodSetInfo, count)); !errors.Is(err, podset.ErrInvalidPodsetInfo) {
				t.Errorf("expected invalid pod set info error, got %v", err)
			}
			if !aw.Spec.Suspend {
				t.Error("AppWrapper was started with invalid pod set infos")
			}
		})
	}
}

func TestRunWithPodSetsInfoDeclaredComponents(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, true)
	ctx, _ := utiltesting.ContextWithLog(t)
	mpiJob := testingmpijob.MakeMPIJob("mpi", "ns").GenericLauncherAndWorker().RunLauncherAsWorker(true).Obj()
	mpiJob.TypeMeta = metav1.TypeMeta{APIVersion: kfmpi.SchemeGroupVersion.String(), Kind: "MPIJob"}
	mpiJob.Spec.MPIReplicaSpecs[kfmpi.MPIReplicaTypeWorker].Replicas = new(int32(4))
	for _, spec := range mpiJob.Spec.MPIReplicaSpecs {
		spec.Template.Annotations = map[string]string{
			kueue.PodSetGroupName:                  "mpi",
			kueue.PodSetRequiredTopologyAnnotation: "cloud.com/rack",
		}
	}
	mpiComponent := testingappwrapper.Component{
		Template: mpiJob,
		DeclaredPodSets: []awv1beta2.AppWrapperPodSet{
			{
				Path:        "template.spec.mpiReplicaSpecs.Launcher.template",
				Replicas:    new(int32(1)),
				Annotations: map[string]string{awutils.PodSetAnnotationTASPodIndexLabel: kftraining.ReplicaIndexLabel},
			},
			{
				Path:        "template.spec.mpiReplicaSpecs.Worker.template",
				Replicas:    new(int32(4)),
				Annotations: map[string]string{awutils.PodSetAnnotationTASPodIndexLabel: kftraining.ReplicaIndexLabel},
			},
		},
	}
	pytorchJob := testingpytorchjob.MakePyTorchJob("pytorch", "ns").PyTorchReplicaSpecsDefault().Parallelism(4).
		PodAnnotation(kftraining.PyTorchJobReplicaTypeMaster, kueue.PodSetGroupName, "pytorch").
		PodAnnotation(kftraining.PyTorchJobReplicaTypeWorker, kueue.PodSetGroupName, "pytorch").
		PodAnnotation(kftraining.PyTorchJobReplicaTypeMaster, kueue.PodSetRequiredTopologyAnnotation, "cloud.com/rack").
		PodAnnotation(kftraining.PyTorchJobReplicaTypeWorker, kueue.PodSetRequiredTopologyAnnotation, "cloud.com/rack").
		PodAnnotation(kftraining.PyTorchJobReplicaTypeMaster, kueue.PodIndexOffsetAnnotation, "0").
		SetTypeMeta().Obj()
	aw := testingappwrapper.MakeAppWrapper("aw", "ns").
		Component(mpiComponent).
		Component(testingappwrapper.Component{
			Template: pytorchJob,
			DeclaredPodSets: []awv1beta2.AppWrapperPodSet{
				{
					Path:        "template.spec.pytorchReplicaSpecs.Worker.template",
					Replicas:    new(int32(4)),
					Annotations: map[string]string{awutils.PodSetAnnotationTASPodIndexLabel: kftraining.ReplicaIndexLabel},
				},
				{
					Path:        "template.spec.pytorchReplicaSpecs.Master.template",
					Replicas:    new(int32(1)),
					Annotations: map[string]string{awutils.PodSetAnnotationTASPodIndexLabel: kftraining.ReplicaIndexLabel},
				},
			},
		}).
		Component(mpiComponent).
		Obj()
	wantAnnotations := [][]map[string]string{
		{nil, nil},
		{{kueue.PodIndexOffsetAnnotation: "0"}, nil},
		{nil, nil},
	}

	if err := fromObject(aw).RunWithPodSetsInfo(ctx, nil, make([]podset.PodSetInfo, 6)); err != nil {
		t.Fatalf("RunWithPodSetsInfo() failed: %v", err)
	}
	for i, component := range aw.Spec.Components {
		for p, info := range component.PodSetInfos {
			if diff := cmp.Diff(wantAnnotations[i][p], info.Annotations); diff != "" {
				t.Errorf("component %d pod set %d annotations mismatch (-want +got):\n%s", i, p, diff)
			}
		}
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
				*utiltestingapi.MakeWorkload("aw", "ns").
					PodSets(
						*utiltestingapi.MakePodSet("aw-0", 2).
							PodIndexLabel(new("batch.kubernetes.io/job-completion-index")).
							Obj(),
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
				*utiltestingapi.MakeWorkload("aw", "ns").
					Annotations(map[string]string{controllerconsts.ProvReqAnnotationPrefix + "test-annotation": "test-val"}).
					PodSets(
						*utiltestingapi.MakePodSet("aw-0", 2).
							PodIndexLabel(new("batch.kubernetes.io/job-completion-index")).
							Obj(),
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
					*utiltestingapi.MakeResourceFlavor("default").Obj(),
					*utiltestingapi.MakeResourceFlavor("on-demand").NodeLabel("provisioning", "on-demand").Obj(),
					*utiltestingapi.MakeResourceFlavor("spot").NodeLabel("provisioning", "spot").Obj(),
				},
				job: testingappwrapper.MakeAppWrapper("aw", "ns").
					Component(utiltestingjob.MakeJob("job", "ns").Parallelism(1).Request(corev1.ResourceCPU, "1").SetTypeMeta().Obj()).
					Component(utiltestingjob.MakeJob("job", "ns").Parallelism(1).Request(corev1.ResourceCPU, "1").SetTypeMeta().Obj()).
					Component(utiltestingjob.MakeJob("job", "ns").Parallelism(1).Request(corev1.ResourceCPU, "1").SetTypeMeta().Obj()).
					Suspend(true).
					Queue("foo").
					Obj(),
				workloads: []kueue.Workload{
					*utiltestingapi.MakeWorkload("a", "ns").
						PodSets(
							*utiltestingapi.MakePodSet("aw-0", 1).Request(corev1.ResourceCPU, "1").Obj(),
							*utiltestingapi.MakePodSet("aw-1", 1).Request(corev1.ResourceCPU, "1").Obj(),
							*utiltestingapi.MakePodSet("aw-2", 1).Request(corev1.ResourceCPU, "1")Obj(),
						).
						ReserveQuota(utiltestingapi.MakeAdmission("cq").
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
					*utiltestingapi.MakeWorkload("a", "ns").
						PodSets(
							*utiltestingapi.MakePodSet("aw-0", 1).Request(corev1.ResourceCPU, "1").Obj(),
							*utiltestingapi.MakePodSet("aw-1", 1).Request(corev1.ResourceCPU, "1").Obj(),
							*utiltestingapi.MakePodSet("aw-2", 1).Request(corev1.ResourceCPU, "1").Obj(),
						).
						ReserveQuota(utiltestingapi.MakeAdmission("cq").
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
			indexer := utiltesting.AsIndexer(kcBuilder)
			if err := SetupIndexes(ctx, indexer); err != nil {
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
			recorder := &utiltesting.EventRecorder{}
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
