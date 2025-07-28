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

package tfjob

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingtfjob "sigs.k8s.io/kueue/pkg/util/testingjobs/tfjob"
)

func TestPriorityClass(t *testing.T) {
	testcases := map[string]struct {
		job                   kftraining.TFJob
		wantPriorityClassName string
	}{
		"none priority class name specified": {
			job:                   kftraining.TFJob{},
			wantPriorityClassName: "",
		},
		"priority specified at runPolicy and replicas; use priority in runPolicy": {
			job: kftraining.TFJob{
				Spec: kftraining.TFJobSpec{
					RunPolicy: kftraining.RunPolicy{
						SchedulingPolicy: &kftraining.SchedulingPolicy{
							PriorityClass: "scheduling-priority",
						},
					},
					TFReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.TFJobReplicaTypeChief: {
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									PriorityClassName: "chief-priority",
								},
							},
						},
						kftraining.TFJobReplicaTypeWorker: {
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
		"runPolicy present, but without priority; fallback to chief": {
			job: kftraining.TFJob{
				Spec: kftraining.TFJobSpec{
					RunPolicy: kftraining.RunPolicy{
						SchedulingPolicy: &kftraining.SchedulingPolicy{},
					},
					TFReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.TFJobReplicaTypeChief: {
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									PriorityClassName: "chief-priority",
								},
							},
						},
					},
				},
			},
			wantPriorityClassName: "chief-priority",
		},
		"specified on chief takes precedence over chief": {
			job: kftraining.TFJob{
				Spec: kftraining.TFJobSpec{
					TFReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.TFJobReplicaTypeChief: {
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									PriorityClassName: "chief-priority",
								},
							},
						},
						kftraining.TFJobReplicaTypePS: {
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									PriorityClassName: "ps-priority",
								},
							},
						},
						kftraining.TFJobReplicaTypeWorker: {
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									PriorityClassName: "worker-priority",
								},
							},
						},
						kftraining.TFJobReplicaTypeEval: {
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									PriorityClassName: "eval-priority",
								},
							},
						},
					},
				},
			},
			wantPriorityClassName: "chief-priority",
		},
		"chief present, but without priority; fallback to evaluator": {
			job: kftraining.TFJob{
				Spec: kftraining.TFJobSpec{
					TFReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.TFJobReplicaTypeChief: {
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{},
							},
						},
						kftraining.TFJobReplicaTypeEval: {
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									PriorityClassName: "eval-priority",
								},
							},
						},
					},
				},
			},
			wantPriorityClassName: "eval-priority",
		},
		"specified on worker only": {
			job: kftraining.TFJob{
				Spec: kftraining.TFJobSpec{
					TFReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.TFJobReplicaTypeChief: {},
						kftraining.TFJobReplicaTypeWorker: {
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
		"evaluator present, but without priority; fallback to empty": {
			job: kftraining.TFJob{
				Spec: kftraining.TFJobSpec{
					TFReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.TFJobReplicaTypeChief: {},
						kftraining.TFJobReplicaTypeEval: {
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
			tfJob := fromObject(&tc.job)
			gotPriorityClassName := tfJob.PriorityClass()
			if tc.wantPriorityClassName != gotPriorityClassName {
				t.Errorf("Unexpected response (want: %v, got: %v)", tc.wantPriorityClassName, gotPriorityClassName)
			}
		})
	}
}

func TestOrderedReplicaTypes(t *testing.T) {
	testcases := map[string]struct {
		job              kftraining.TFJob
		wantReplicaTypes []kftraining.ReplicaType
	}{
		"job has no replicas": {
			job:              kftraining.TFJob{},
			wantReplicaTypes: []kftraining.ReplicaType{},
		},
		"job has all replicas": {
			job: kftraining.TFJob{
				Spec: kftraining.TFJobSpec{
					TFReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.TFJobReplicaTypePS:     {},
						kftraining.TFJobReplicaTypeEval:   {},
						kftraining.TFJobReplicaTypeWorker: {},
						kftraining.TFJobReplicaTypeChief:  {},
						kftraining.TFJobReplicaTypeMaster: {},
					},
				},
			},
			wantReplicaTypes: []kftraining.ReplicaType{
				kftraining.TFJobReplicaTypeChief,
				kftraining.TFJobReplicaTypeMaster,
				kftraining.TFJobReplicaTypePS,
				kftraining.TFJobReplicaTypeWorker,
				kftraining.TFJobReplicaTypeEval,
			},
		},
		"job has only worker replicas": {
			job: kftraining.TFJob{
				Spec: kftraining.TFJobSpec{
					TFReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.TFJobReplicaTypeWorker: {},
					},
				},
			},
			wantReplicaTypes: []kftraining.ReplicaType{kftraining.TFJobReplicaTypeWorker},
		},
	}
	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			tfJob := fromObject(&tc.job)
			gotReplicaTypes := tfJob.OrderedReplicaTypes()
			if diff := cmp.Diff(tc.wantReplicaTypes, gotReplicaTypes); len(diff) != 0 {
				t.Errorf("Unexpected response (want: %v, got: %v)", tc.wantReplicaTypes, gotReplicaTypes)
			}
		})
	}
}

func TestPodSets(t *testing.T) {
	testCases := map[string]struct {
		job                           *kftraining.TFJob
		wantPodSets                   func(job *kftraining.TFJob) []kueue.PodSet
		enableTopologyAwareScheduling bool
	}{
		"no annotations": {
			job: testingtfjob.MakeTFJob("tfjob", "ns").TFReplicaSpecsDefault().Obj(),
			wantPodSets: func(job *kftraining.TFJob) []kueue.PodSet {
				return []kueue.PodSet{
					*utiltesting.MakePodSet(kueue.NewPodSetReference(string(kftraining.TFJobReplicaTypeChief)), 1).
						PodSpec(job.Spec.TFReplicaSpecs[kftraining.TFJobReplicaTypeChief].Template.Spec).
						Obj(),
					*utiltesting.MakePodSet(kueue.NewPodSetReference(string(kftraining.TFJobReplicaTypePS)), 1).
						PodSpec(job.Spec.TFReplicaSpecs[kftraining.TFJobReplicaTypePS].Template.Spec).
						Obj(),
					*utiltesting.MakePodSet(kueue.NewPodSetReference(string(kftraining.TFJobReplicaTypeWorker)), 1).
						PodSpec(job.Spec.TFReplicaSpecs[kftraining.TFJobReplicaTypeWorker].Template.Spec).
						Obj(),
				}
			},
			enableTopologyAwareScheduling: false,
		},
		"with required and preferred topology annotation": {
			job: testingtfjob.MakeTFJob("tfjob", "ns").
				TFReplicaSpecs(
					testingtfjob.TFReplicaSpecRequirement{
						ReplicaType:  kftraining.TFJobReplicaTypeChief,
						ReplicaCount: 1,
						Annotations: map[string]string{
							kueuealpha.PodSetRequiredTopologyAnnotation: "cloud.com/rack",
						},
					},
					testingtfjob.TFReplicaSpecRequirement{
						ReplicaType:  kftraining.TFJobReplicaTypePS,
						ReplicaCount: 1,
						Annotations: map[string]string{
							kueuealpha.PodSetPreferredTopologyAnnotation: "cloud.com/block",
						},
					},
					testingtfjob.TFReplicaSpecRequirement{
						ReplicaType:  kftraining.TFJobReplicaTypeWorker,
						ReplicaCount: 1,
					},
				).
				Obj(),
			wantPodSets: func(job *kftraining.TFJob) []kueue.PodSet {
				return []kueue.PodSet{
					*utiltesting.MakePodSet(kueue.NewPodSetReference(string(kftraining.TFJobReplicaTypeChief)), 1).
						PodSpec(job.Spec.TFReplicaSpecs[kftraining.TFJobReplicaTypeChief].Template.Spec).
						Annotations(map[string]string{kueuealpha.PodSetRequiredTopologyAnnotation: "cloud.com/rack"}).
						RequiredTopologyRequest("cloud.com/rack").
						PodIndexLabel(ptr.To(kftraining.ReplicaIndexLabel)).
						Obj(),
					*utiltesting.MakePodSet(kueue.NewPodSetReference(string(kftraining.TFJobReplicaTypePS)), 1).
						PodSpec(job.Spec.TFReplicaSpecs[kftraining.TFJobReplicaTypePS].Template.Spec).
						Annotations(map[string]string{kueuealpha.PodSetPreferredTopologyAnnotation: "cloud.com/block"}).
						PreferredTopologyRequest("cloud.com/block").
						PodIndexLabel(ptr.To(kftraining.ReplicaIndexLabel)).
						Obj(),
					*utiltesting.MakePodSet(kueue.NewPodSetReference(string(kftraining.TFJobReplicaTypeWorker)), 1).
						PodSpec(job.Spec.TFReplicaSpecs[kftraining.TFJobReplicaTypeWorker].Template.Spec).
						Obj(),
				}
			},
			enableTopologyAwareScheduling: true,
		},
		"without required and preferred topology annotation if TAS is disabled": {
			job: testingtfjob.MakeTFJob("tfjob", "ns").
				TFReplicaSpecs(
					testingtfjob.TFReplicaSpecRequirement{
						ReplicaType:  kftraining.TFJobReplicaTypeChief,
						ReplicaCount: 1,
						Annotations: map[string]string{
							kueuealpha.PodSetRequiredTopologyAnnotation: "cloud.com/rack",
						},
					},
					testingtfjob.TFReplicaSpecRequirement{
						ReplicaType:  kftraining.TFJobReplicaTypePS,
						ReplicaCount: 1,
						Annotations: map[string]string{
							kueuealpha.PodSetPreferredTopologyAnnotation: "cloud.com/block",
						},
					},
					testingtfjob.TFReplicaSpecRequirement{
						ReplicaType:  kftraining.TFJobReplicaTypeWorker,
						ReplicaCount: 1,
					},
				).
				Obj(),
			wantPodSets: func(job *kftraining.TFJob) []kueue.PodSet {
				return []kueue.PodSet{
					*utiltesting.MakePodSet(kueue.NewPodSetReference(string(kftraining.TFJobReplicaTypeChief)), 1).
						PodSpec(job.Spec.TFReplicaSpecs[kftraining.TFJobReplicaTypeChief].Template.Spec).
						Annotations(map[string]string{kueuealpha.PodSetRequiredTopologyAnnotation: "cloud.com/rack"}).
						Obj(),
					*utiltesting.MakePodSet(kueue.NewPodSetReference(string(kftraining.TFJobReplicaTypePS)), 1).
						PodSpec(job.Spec.TFReplicaSpecs[kftraining.TFJobReplicaTypePS].Template.Spec).
						Annotations(map[string]string{kueuealpha.PodSetPreferredTopologyAnnotation: "cloud.com/block"}).
						Obj(),
					*utiltesting.MakePodSet(kueue.NewPodSetReference(string(kftraining.TFJobReplicaTypeWorker)), 1).
						PodSpec(job.Spec.TFReplicaSpecs[kftraining.TFJobReplicaTypeWorker].Template.Spec).
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
		job                     *kftraining.TFJob
		wantValidationErrs      field.ErrorList
		wantErr                 error
		topologyAwareScheduling bool
	}{
		"no annotations": {
			job: testingtfjob.MakeTFJob("tfjob", "ns").TFReplicaSpecsDefault().Obj(),
		},
		"valid TAS request": {
			job: testingtfjob.MakeTFJob("tfjob", "ns").
				TFReplicaSpecs(
					testingtfjob.TFReplicaSpecRequirement{
						ReplicaType:  kftraining.TFJobReplicaTypeChief,
						ReplicaCount: 1,
						Annotations: map[string]string{
							kueuealpha.PodSetRequiredTopologyAnnotation: "cloud.com/rack",
						},
					},
					testingtfjob.TFReplicaSpecRequirement{
						ReplicaType:  kftraining.TFJobReplicaTypePS,
						ReplicaCount: 1,
						Annotations: map[string]string{
							kueuealpha.PodSetPreferredTopologyAnnotation: "cloud.com/block",
						},
					},
					testingtfjob.TFReplicaSpecRequirement{
						ReplicaType:  kftraining.TFJobReplicaTypeWorker,
						ReplicaCount: 1,
					},
				).
				Obj(),
			topologyAwareScheduling: true,
		},
		"invalid TAS request": {
			job: testingtfjob.MakeTFJob("tfjob", "ns").
				TFReplicaSpecs(
					testingtfjob.TFReplicaSpecRequirement{
						ReplicaType:  kftraining.TFJobReplicaTypeChief,
						ReplicaCount: 1,
						Annotations: map[string]string{
							kueuealpha.PodSetRequiredTopologyAnnotation:  "cloud.com/rack",
							kueuealpha.PodSetPreferredTopologyAnnotation: "cloud.com/block",
						},
					},
					testingtfjob.TFReplicaSpecRequirement{
						ReplicaType:  kftraining.TFJobReplicaTypePS,
						ReplicaCount: 1,
						Annotations: map[string]string{
							kueuealpha.PodSetRequiredTopologyAnnotation:  "cloud.com/rack",
							kueuealpha.PodSetPreferredTopologyAnnotation: "cloud.com/block",
						},
					},
					testingtfjob.TFReplicaSpecRequirement{
						ReplicaType:  kftraining.TFJobReplicaTypeWorker,
						ReplicaCount: 1,
						Annotations: map[string]string{
							kueuealpha.PodSetRequiredTopologyAnnotation: "cloud.com/rack",
						},
					},
				).
				Obj(),
			wantValidationErrs: field.ErrorList{
				field.Invalid(
					field.NewPath("spec", "tfReplicaSpecs").
						Key("Chief").
						Child("template", "metadata", "annotations"),
					field.OmitValueType{},
					`must not contain more than one topology annotation: ["kueue.x-k8s.io/podset-required-topology", `+
						`"kueue.x-k8s.io/podset-preferred-topology", "kueue.x-k8s.io/podset-unconstrained-topology"]`),
				field.Invalid(
					field.NewPath("spec", "tfReplicaSpecs").
						Key("PS").
						Child("template", "metadata", "annotations"),
					field.OmitValueType{},
					`must not contain more than one topology annotation: ["kueue.x-k8s.io/podset-required-topology", `+
						`"kueue.x-k8s.io/podset-preferred-topology", "kueue.x-k8s.io/podset-unconstrained-topology"]`),
			},
			topologyAwareScheduling: true,
		},
		"invalid slice topology request - slice size larger than number of podsets": {
			job: testingtfjob.MakeTFJob("tfjob", "ns").
				TFReplicaSpecs(
					testingtfjob.TFReplicaSpecRequirement{
						ReplicaType:  kftraining.TFJobReplicaTypeChief,
						ReplicaCount: 3,
						Annotations: map[string]string{
							kueuealpha.PodSetRequiredTopologyAnnotation:      "cloud.com/rack",
							kueuealpha.PodSetSliceRequiredTopologyAnnotation: "cloud.com/block",
							kueuealpha.PodSetSliceSizeAnnotation:             "5",
						},
					},
					testingtfjob.TFReplicaSpecRequirement{
						ReplicaType:  kftraining.TFJobReplicaTypePS,
						ReplicaCount: 5,
						Annotations: map[string]string{
							kueuealpha.PodSetRequiredTopologyAnnotation:      "cloud.com/rack",
							kueuealpha.PodSetSliceRequiredTopologyAnnotation: "cloud.com/block",
							kueuealpha.PodSetSliceSizeAnnotation:             "10",
						},
					},
					testingtfjob.TFReplicaSpecRequirement{
						ReplicaType:  kftraining.TFJobReplicaTypeWorker,
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
					field.NewPath("spec", "tfReplicaSpecs").
						Key("Chief").
						Child("template", "metadata", "annotations").
						Key("kueue.x-k8s.io/podset-slice-size"),
					"5", "must not be greater than pod set count 3",
				),
				field.Invalid(
					field.NewPath("spec", "tfReplicaSpecs").
						Key("PS").
						Child("template", "metadata", "annotations").
						Key("kueue.x-k8s.io/podset-slice-size"),
					"10", "must not be greater than pod set count 5",
				),
				field.Invalid(
					field.NewPath("spec", "tfReplicaSpecs").
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
				t.Errorf("validate create validation error list mismatch (-want +got):\n%s", diff)
			}

			gotValidationErrs, gotErr = fromObject(tc.job).ValidateOnUpdate(nil)
			if diff := cmp.Diff(tc.wantErr, gotErr); diff != "" {
				t.Errorf("validate create error mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantValidationErrs, gotValidationErrs); diff != "" {
				t.Errorf("validate create validation error list mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
