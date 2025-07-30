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

package paddlejob

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingpaddlejob "sigs.k8s.io/kueue/pkg/util/testingjobs/paddlejob"
)

func TestPriorityClass(t *testing.T) {
	testcases := map[string]struct {
		job                   kftraining.PaddleJob
		wantPriorityClassName string
	}{
		"none priority class name specified": {
			job:                   kftraining.PaddleJob{},
			wantPriorityClassName: "",
		},
		"priority specified at runPolicy and replicas; use priority in runPolicy": {
			job: kftraining.PaddleJob{
				Spec: kftraining.PaddleJobSpec{
					RunPolicy: kftraining.RunPolicy{
						SchedulingPolicy: &kftraining.SchedulingPolicy{
							PriorityClass: "scheduling-priority",
						},
					},
					PaddleReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.PaddleJobReplicaTypeMaster: {
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									PriorityClassName: "master-priority",
								},
							},
						},
						kftraining.PaddleJobReplicaTypeWorker: {
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
			job: kftraining.PaddleJob{
				Spec: kftraining.PaddleJobSpec{
					RunPolicy: kftraining.RunPolicy{
						SchedulingPolicy: &kftraining.SchedulingPolicy{},
					},
					PaddleReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.PaddleJobReplicaTypeMaster: {
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
			job: kftraining.PaddleJob{
				Spec: kftraining.PaddleJobSpec{
					PaddleReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.PaddleJobReplicaTypeMaster: {
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									PriorityClassName: "master-priority",
								},
							},
						},
						kftraining.PaddleJobReplicaTypeWorker: {
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
			job: kftraining.PaddleJob{
				Spec: kftraining.PaddleJobSpec{
					PaddleReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.PaddleJobReplicaTypeMaster: {
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{},
							},
						},
						kftraining.PaddleJobReplicaTypeWorker: {
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
			job: kftraining.PaddleJob{
				Spec: kftraining.PaddleJobSpec{
					PaddleReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.PaddleJobReplicaTypeMaster: {},
						kftraining.PaddleJobReplicaTypeWorker: {
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
			job: kftraining.PaddleJob{
				Spec: kftraining.PaddleJobSpec{
					PaddleReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.PaddleJobReplicaTypeMaster: {},
						kftraining.PaddleJobReplicaTypeWorker: {
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
			paddleJob := fromObject(&tc.job)
			gotPriorityClassName := paddleJob.PriorityClass()
			if tc.wantPriorityClassName != gotPriorityClassName {
				t.Errorf("Unexpected response (want: %v, got: %v)", tc.wantPriorityClassName, gotPriorityClassName)
			}
		})
	}
}

func TestOrderedReplicaTypes(t *testing.T) {
	testcases := map[string]struct {
		job              kftraining.PaddleJob
		wantReplicaTypes []kftraining.ReplicaType
	}{
		"job has no replicas": {
			job:              kftraining.PaddleJob{},
			wantReplicaTypes: []kftraining.ReplicaType{},
		},
		"job has all replicas": {
			job: kftraining.PaddleJob{
				Spec: kftraining.PaddleJobSpec{
					PaddleReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.PaddleJobReplicaTypeMaster: {},
						kftraining.PaddleJobReplicaTypeWorker: {},
					},
				},
			},
			wantReplicaTypes: []kftraining.ReplicaType{
				kftraining.PaddleJobReplicaTypeMaster,
				kftraining.PaddleJobReplicaTypeWorker,
			},
		},
		"job only has the worker replica": {
			job: kftraining.PaddleJob{
				Spec: kftraining.PaddleJobSpec{
					PaddleReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.PaddleJobReplicaTypeWorker: {},
					},
				},
			},
			wantReplicaTypes: []kftraining.ReplicaType{
				kftraining.PaddleJobReplicaTypeWorker,
			},
		},
	}
	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			paddleJob := fromObject(&tc.job)
			gotReplicaTypes := paddleJob.OrderedReplicaTypes()
			if diff := cmp.Diff(tc.wantReplicaTypes, gotReplicaTypes); len(diff) != 0 {
				t.Errorf("Unexpected response (-want, +got): %v", diff)
			}
		})
	}
}

func TestPodSets(t *testing.T) {
	testCases := map[string]struct {
		job                           *kftraining.PaddleJob
		wantPodSets                   func(job *kftraining.PaddleJob) []kueue.PodSet
		enableTopologyAwareScheduling bool
	}{
		"no annotations": {
			job: testingpaddlejob.MakePaddleJob("paddlejob", "ns").
				PaddleReplicaSpecs(
					testingpaddlejob.PaddleReplicaSpecRequirement{
						ReplicaType:  kftraining.PaddleJobReplicaTypeMaster,
						ReplicaCount: 1,
					},
					testingpaddlejob.PaddleReplicaSpecRequirement{
						ReplicaType:  kftraining.PaddleJobReplicaTypeWorker,
						ReplicaCount: 1,
					},
				).
				Obj(),
			wantPodSets: func(job *kftraining.PaddleJob) []kueue.PodSet {
				return []kueue.PodSet{
					*utiltesting.MakePodSet(kueue.NewPodSetReference(string(kftraining.PaddleJobReplicaTypeMaster)), 1).
						PodSpec(job.Spec.PaddleReplicaSpecs[kftraining.PaddleJobReplicaTypeMaster].Template.Spec).
						Obj(),
					*utiltesting.MakePodSet(kueue.NewPodSetReference(string(kftraining.PaddleJobReplicaTypeWorker)), 1).
						PodSpec(job.Spec.PaddleReplicaSpecs[kftraining.PaddleJobReplicaTypeWorker].Template.Spec).
						Obj(),
				}
			},
			enableTopologyAwareScheduling: false,
		},
		"with required and preferred topology annotation": {
			job: testingpaddlejob.MakePaddleJob("paddlejob", "ns").
				PaddleReplicaSpecs(
					testingpaddlejob.PaddleReplicaSpecRequirement{
						ReplicaType:  kftraining.PaddleJobReplicaTypeMaster,
						ReplicaCount: 1,
						Annotations: map[string]string{
							kueuealpha.PodSetRequiredTopologyAnnotation: "cloud.com/rack",
						},
					},
					testingpaddlejob.PaddleReplicaSpecRequirement{
						ReplicaType:  kftraining.PaddleJobReplicaTypeWorker,
						ReplicaCount: 1,
						Annotations: map[string]string{
							kueuealpha.PodSetPreferredTopologyAnnotation: "cloud.com/block",
						},
					},
				).
				Obj(),
			wantPodSets: func(job *kftraining.PaddleJob) []kueue.PodSet {
				return []kueue.PodSet{
					*utiltesting.MakePodSet(kueue.NewPodSetReference(string(kftraining.PaddleJobReplicaTypeMaster)), 1).
						PodSpec(job.Spec.PaddleReplicaSpecs[kftraining.PaddleJobReplicaTypeMaster].Template.Spec).
						Annotations(map[string]string{kueuealpha.PodSetRequiredTopologyAnnotation: "cloud.com/rack"}).
						RequiredTopologyRequest("cloud.com/rack").
						PodIndexLabel(ptr.To(kftraining.ReplicaIndexLabel)).
						Obj(),
					*utiltesting.MakePodSet(kueue.NewPodSetReference(string(kftraining.PaddleJobReplicaTypeWorker)), 1).
						PodSpec(job.Spec.PaddleReplicaSpecs[kftraining.PaddleJobReplicaTypeWorker].Template.Spec).
						Annotations(map[string]string{kueuealpha.PodSetPreferredTopologyAnnotation: "cloud.com/block"}).
						PreferredTopologyRequest("cloud.com/block").
						PodIndexLabel(ptr.To(kftraining.ReplicaIndexLabel)).
						Obj(),
				}
			},
			enableTopologyAwareScheduling: true,
		},
		"without required and preferred topology annotation if TAS is disabled": {
			job: testingpaddlejob.MakePaddleJob("paddlejob", "ns").
				PaddleReplicaSpecs(
					testingpaddlejob.PaddleReplicaSpecRequirement{
						ReplicaType:  kftraining.PaddleJobReplicaTypeMaster,
						ReplicaCount: 1,
						Annotations: map[string]string{
							kueuealpha.PodSetRequiredTopologyAnnotation: "cloud.com/rack",
						},
					},
					testingpaddlejob.PaddleReplicaSpecRequirement{
						ReplicaType:  kftraining.PaddleJobReplicaTypeWorker,
						ReplicaCount: 1,
						Annotations: map[string]string{
							kueuealpha.PodSetPreferredTopologyAnnotation: "cloud.com/block",
						},
					},
				).
				Obj(),
			wantPodSets: func(job *kftraining.PaddleJob) []kueue.PodSet {
				return []kueue.PodSet{
					*utiltesting.MakePodSet(kueue.NewPodSetReference(string(kftraining.PaddleJobReplicaTypeMaster)), 1).
						PodSpec(job.Spec.PaddleReplicaSpecs[kftraining.PaddleJobReplicaTypeMaster].Template.Spec).
						Annotations(map[string]string{kueuealpha.PodSetRequiredTopologyAnnotation: "cloud.com/rack"}).
						Obj(),
					*utiltesting.MakePodSet(kueue.NewPodSetReference(string(kftraining.PaddleJobReplicaTypeWorker)), 1).
						PodSpec(job.Spec.PaddleReplicaSpecs[kftraining.PaddleJobReplicaTypeWorker].Template.Spec).
						Annotations(map[string]string{kueuealpha.PodSetPreferredTopologyAnnotation: "cloud.com/block"}).
						Obj(),
				}
			},
			enableTopologyAwareScheduling: false,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, tc.enableTopologyAwareScheduling)
			gotPodSets, err := fromObject(tc.job).PodSets()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if diff := cmp.Diff(tc.wantPodSets(tc.job), gotPodSets); diff != "" {
				t.Errorf("pod sets mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	testCases := map[string]struct {
		job                     *kftraining.PaddleJob
		wantValidationErrs      field.ErrorList
		wantErr                 error
		topologyAwareScheduling bool
	}{
		"no annotations": {
			job: testingpaddlejob.MakePaddleJob("paddlejob", "ns").PaddleReplicaSpecsDefault().Obj(),
		},
		"valid TAS request": {
			job: testingpaddlejob.MakePaddleJob("paddlejob", "ns").
				PaddleReplicaSpecs(
					testingpaddlejob.PaddleReplicaSpecRequirement{
						ReplicaType:  kftraining.PaddleJobReplicaTypeMaster,
						ReplicaCount: 1,
						Annotations: map[string]string{
							kueuealpha.PodSetRequiredTopologyAnnotation: "cloud.com/rack",
						},
					},
					testingpaddlejob.PaddleReplicaSpecRequirement{
						ReplicaType:  kftraining.PaddleJobReplicaTypeWorker,
						ReplicaCount: 1,
						Annotations: map[string]string{
							kueuealpha.PodSetPreferredTopologyAnnotation: "cloud.com/block",
						},
					},
				).
				Obj(),
			topologyAwareScheduling: true,
		},
		"invalid TAS request": {
			job: testingpaddlejob.MakePaddleJob("paddlejob", "ns").
				PaddleReplicaSpecs(
					testingpaddlejob.PaddleReplicaSpecRequirement{
						ReplicaType:  kftraining.PaddleJobReplicaTypeMaster,
						ReplicaCount: 1,
						Annotations: map[string]string{
							kueuealpha.PodSetRequiredTopologyAnnotation:  "cloud.com/rack",
							kueuealpha.PodSetPreferredTopologyAnnotation: "cloud.com/block",
						},
					},
					testingpaddlejob.PaddleReplicaSpecRequirement{
						ReplicaType:  kftraining.PaddleJobReplicaTypeWorker,
						ReplicaCount: 1,
						Annotations: map[string]string{
							kueuealpha.PodSetRequiredTopologyAnnotation:  "cloud.com/rack",
							kueuealpha.PodSetPreferredTopologyAnnotation: "cloud.com/block",
						},
					},
				).
				Obj(),
			wantValidationErrs: field.ErrorList{
				field.Invalid(
					field.NewPath("spec", "paddleReplicaSpecs").
						Key("Master").
						Child("template", "metadata", "annotations"),
					field.OmitValueType{},
					`must not contain more than one topology annotation: ["kueue.x-k8s.io/podset-required-topology", `+
						`"kueue.x-k8s.io/podset-preferred-topology", "kueue.x-k8s.io/podset-unconstrained-topology"]`),
				field.Invalid(
					field.NewPath("spec", "paddleReplicaSpecs").
						Key("Worker").
						Child("template", "metadata", "annotations"),
					field.OmitValueType{},
					`must not contain more than one topology annotation: ["kueue.x-k8s.io/podset-required-topology", `+
						`"kueue.x-k8s.io/podset-preferred-topology", "kueue.x-k8s.io/podset-unconstrained-topology"]`),
			},
			topologyAwareScheduling: true,
		},
		"invalid slice topology request - slice size larger than number of podsets": {
			job: testingpaddlejob.MakePaddleJob("paddlejob", "ns").
				PaddleReplicaSpecs(
					testingpaddlejob.PaddleReplicaSpecRequirement{
						ReplicaType:  kftraining.PaddleJobReplicaTypeMaster,
						ReplicaCount: 5,
						Annotations: map[string]string{
							kueuealpha.PodSetRequiredTopologyAnnotation:      "cloud.com/rack",
							kueuealpha.PodSetSliceRequiredTopologyAnnotation: "cloud.com/block",
							kueuealpha.PodSetSliceSizeAnnotation:             "10",
						},
					},
					testingpaddlejob.PaddleReplicaSpecRequirement{
						ReplicaType:  kftraining.PaddleJobReplicaTypeWorker,
						ReplicaCount: 10,
						Annotations: map[string]string{
							kueuealpha.PodSetRequiredTopologyAnnotation:      "cloud.com/rack",
							kueuealpha.PodSetSliceRequiredTopologyAnnotation: "cloud.com/block",
							kueuealpha.PodSetSliceSizeAnnotation:             "20",
						},
					},
				).
				Obj(),
			wantValidationErrs: field.ErrorList{
				field.Invalid(
					field.NewPath("spec", "paddleReplicaSpecs").
						Key("Master").
						Child("template", "metadata", "annotations").
						Key("kueue.x-k8s.io/podset-slice-size"),
					"10", "must not be greater than pod set count 5",
				),
				field.Invalid(
					field.NewPath("spec", "paddleReplicaSpecs").
						Key("Worker").
						Child("template", "metadata", "annotations").
						Key("kueue.x-k8s.io/podset-slice-size"),
					"20", "must not be greater than pod set count 10",
				),
			},
			topologyAwareScheduling: true,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, tc.topologyAwareScheduling)

			gotValidationErrs, gotErr := fromObject(tc.job).ValidateOnCreate()
			if diff := cmp.Diff(tc.wantErr, gotErr); diff != "" {
				t.Errorf("validate create error mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantValidationErrs, gotValidationErrs); diff != "" {
				t.Errorf("validate create validation errors list mismatch (-want +got):\n%s", diff)
			}

			gotValidationErrs, gotErr = fromObject(tc.job).ValidateOnUpdate(nil)
			if diff := cmp.Diff(tc.wantErr, gotErr); diff != "" {
				t.Errorf("validate create error mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantValidationErrs, gotValidationErrs); diff != "" {
				t.Errorf("validate create validation errors list mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

var (
	jobCmpOpts = cmp.Options{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(kftraining.PaddleJob{}, "TypeMeta", "ObjectMeta"),
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
	testNamespace := utiltesting.MakeNamespaceWrapper("ns").Label(corev1.LabelMetadataName, "ns").Obj()
	cases := map[string]struct {
		reconcilerOptions []jobframework.Option
		job               *kftraining.PaddleJob
		workloads         []kueue.Workload
		wantJob           *kftraining.PaddleJob
		wantWorkloads     []kueue.Workload
		wantErr           error
	}{
		"workload is created with podsets": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
				jobframework.WithManagedJobsNamespaceSelector(labels.Everything()),
			},
			job:     testingpaddlejob.MakePaddleJob("paddlejob", "ns").PaddleReplicaSpecsDefault().Parallelism(2).Obj(),
			wantJob: testingpaddlejob.MakePaddleJob("paddlejob", "ns").PaddleReplicaSpecsDefault().Parallelism(2).Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("paddlejob", "ns").
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
			job:           testingpaddlejob.MakePaddleJob("paddlejob", "ns").PaddleReplicaSpecsDefault().Parallelism(2).Obj(),
			wantJob:       testingpaddlejob.MakePaddleJob("paddlejob", "ns").PaddleReplicaSpecsDefault().Parallelism(2).Obj(),
			wantWorkloads: []kueue.Workload{},
		},
		"when workload is evicted, suspended is reset, restore node affinity": {
			job: testingpaddlejob.MakePaddleJob("paddlejob", "ns").
				PaddleReplicaSpecsDefault().
				Image("").
				Args(nil).
				Queue("foo").
				Suspend(false).
				Parallelism(10).
				Request(kftraining.PaddleJobReplicaTypeMaster, corev1.ResourceCPU, "1").
				Request(kftraining.PaddleJobReplicaTypeWorker, corev1.ResourceCPU, "5").
				NodeSelector("provisioning", "spot").
				Active(kftraining.PaddleJobReplicaTypeMaster, 1).
				Active(kftraining.PaddleJobReplicaTypeWorker, 10).
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
			wantJob: testingpaddlejob.MakePaddleJob("paddlejob", "ns").
				PaddleReplicaSpecsDefault().
				Image("").
				Args(nil).
				Queue("foo").
				Suspend(true).
				Parallelism(10).
				Request(kftraining.PaddleJobReplicaTypeMaster, corev1.ResourceCPU, "1").
				Request(kftraining.PaddleJobReplicaTypeWorker, corev1.ResourceCPU, "5").
				Active(kftraining.PaddleJobReplicaTypeMaster, 1).
				Active(kftraining.PaddleJobReplicaTypeWorker, 10).
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
			kcBuilder := utiltesting.NewClientBuilder(kftraining.AddToScheme).WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})
			if err := SetupIndexes(ctx, utiltesting.AsIndexer(kcBuilder)); err != nil {
				t.Fatalf("Failed to setup indexes: %v", err)
			}
			kcBuilder = kcBuilder.WithObjects(utiltesting.MakeResourceFlavor("default").Obj())
			kcBuilder = kcBuilder.WithObjects(tc.job, testNamespace)
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

			var gotPaddleJob kftraining.PaddleJob
			if err = kClient.Get(ctx, jobKey, &gotPaddleJob); err != nil {
				t.Fatalf("Could not get Job after reconcile: %v", err)
			}
			if diff := cmp.Diff(tc.wantJob, &gotPaddleJob, jobCmpOpts...); diff != "" {
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
