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

package tfjob

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
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
		job         *kftraining.TFJob
		wantPodSets func(job *kftraining.TFJob) []kueue.PodSet
	}{
		"no annotations": {
			job: testingtfjob.MakeTFJob("tfjob", "ns").TFReplicaSpecsDefault().Obj(),
			wantPodSets: func(job *kftraining.TFJob) []kueue.PodSet {
				return []kueue.PodSet{
					{
						Name:     strings.ToLower(string(kftraining.TFJobReplicaTypeChief)),
						Template: job.Spec.TFReplicaSpecs[kftraining.TFJobReplicaTypeChief].Template,
						Count:    1,
					},
					{
						Name:     strings.ToLower(string(kftraining.TFJobReplicaTypePS)),
						Template: job.Spec.TFReplicaSpecs[kftraining.TFJobReplicaTypePS].Template,
						Count:    1,
					},
					{
						Name:     strings.ToLower(string(kftraining.TFJobReplicaTypeWorker)),
						Template: job.Spec.TFReplicaSpecs[kftraining.TFJobReplicaTypeWorker].Template,
						Count:    1,
					},
				}
			},
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
					{
						Name:     strings.ToLower(string(kftraining.TFJobReplicaTypeChief)),
						Template: job.Spec.TFReplicaSpecs[kftraining.TFJobReplicaTypeChief].Template,
						Count:    1,
						TopologyRequest: &kueue.PodSetTopologyRequest{Required: ptr.To("cloud.com/rack"),
							PodIndexLabel: ptr.To(kftraining.ReplicaIndexLabel)},
					},
					{
						Name:     strings.ToLower(string(kftraining.TFJobReplicaTypePS)),
						Template: job.Spec.TFReplicaSpecs[kftraining.TFJobReplicaTypePS].Template,
						Count:    1,
						TopologyRequest: &kueue.PodSetTopologyRequest{Preferred: ptr.To("cloud.com/block"),
							PodIndexLabel: ptr.To(kftraining.ReplicaIndexLabel)},
					},
					{
						Name:     strings.ToLower(string(kftraining.TFJobReplicaTypeWorker)),
						Template: job.Spec.TFReplicaSpecs[kftraining.TFJobReplicaTypeWorker].Template,
						Count:    1,
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
		job      *kftraining.TFJob
		wantErrs field.ErrorList
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
			wantErrs: field.ErrorList{
				field.Invalid(
					field.NewPath("spec", "tfReplicaSpecs").
						Key("Chief").
						Child("template", "metadata", "annotations"),
					field.OmitValueType{},
					`must not contain both "kueue.x-k8s.io/podset-required-topology" and "kueue.x-k8s.io/podset-preferred-topology"`,
				),
				field.Invalid(
					field.NewPath("spec", "tfReplicaSpecs").
						Key("PS").
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
