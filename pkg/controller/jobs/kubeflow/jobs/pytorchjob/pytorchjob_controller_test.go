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

package pytorchjob

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
	testingpytorchjob "sigs.k8s.io/kueue/pkg/util/testingjobs/pytorchjob"
)

func TestPriorityClass(t *testing.T) {
	testcases := map[string]struct {
		job                   kftraining.PyTorchJob
		wantPriorityClassName string
	}{
		"none priority class name specified": {
			job:                   kftraining.PyTorchJob{},
			wantPriorityClassName: "",
		},
		"priority specified at runPolicy and replicas; use priority in runPolicy": {
			job: kftraining.PyTorchJob{
				Spec: kftraining.PyTorchJobSpec{
					RunPolicy: kftraining.RunPolicy{
						SchedulingPolicy: &kftraining.SchedulingPolicy{
							PriorityClass: "scheduling-priority",
						},
					},
					PyTorchReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.PyTorchJobReplicaTypeMaster: {
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									PriorityClassName: "master-priority",
								},
							},
						},
						kftraining.PyTorchJobReplicaTypeWorker: {
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
			job: kftraining.PyTorchJob{
				Spec: kftraining.PyTorchJobSpec{
					RunPolicy: kftraining.RunPolicy{
						SchedulingPolicy: &kftraining.SchedulingPolicy{},
					},
					PyTorchReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.PyTorchJobReplicaTypeMaster: {
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
			job: kftraining.PyTorchJob{
				Spec: kftraining.PyTorchJobSpec{
					PyTorchReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.PyTorchJobReplicaTypeMaster: {
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									PriorityClassName: "master-priority",
								},
							},
						},
						kftraining.PyTorchJobReplicaTypeWorker: {
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
			job: kftraining.PyTorchJob{
				Spec: kftraining.PyTorchJobSpec{
					PyTorchReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.PyTorchJobReplicaTypeMaster: {
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{},
							},
						},
						kftraining.PyTorchJobReplicaTypeWorker: {
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
			job: kftraining.PyTorchJob{
				Spec: kftraining.PyTorchJobSpec{
					PyTorchReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.PyTorchJobReplicaTypeMaster: {},
						kftraining.PyTorchJobReplicaTypeWorker: {
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
			job: kftraining.PyTorchJob{
				Spec: kftraining.PyTorchJobSpec{
					PyTorchReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.PyTorchJobReplicaTypeMaster: {},
						kftraining.PyTorchJobReplicaTypeWorker: {
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
			pytorchJob := fromObject(&tc.job)
			gotPriorityClassName := pytorchJob.PriorityClass()
			if tc.wantPriorityClassName != gotPriorityClassName {
				t.Errorf("Unexpected response (want: %v, got: %v)", tc.wantPriorityClassName, gotPriorityClassName)
			}
		})
	}
}

func TestOrderedReplicaTypes(t *testing.T) {
	testcases := map[string]struct {
		job              kftraining.PyTorchJob
		wantReplicaTypes []kftraining.ReplicaType
	}{
		"job has no replicas": {
			job:              kftraining.PyTorchJob{},
			wantReplicaTypes: []kftraining.ReplicaType{},
		},
		"job has all replicas": {
			job: kftraining.PyTorchJob{
				Spec: kftraining.PyTorchJobSpec{
					PyTorchReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.PyTorchJobReplicaTypeMaster: {},
						kftraining.PyTorchJobReplicaTypeWorker: {},
					},
				},
			},
			wantReplicaTypes: []kftraining.ReplicaType{
				kftraining.PyTorchJobReplicaTypeMaster,
				kftraining.PyTorchJobReplicaTypeWorker,
			},
		},
		"job only has the worker replica": {
			job: kftraining.PyTorchJob{
				Spec: kftraining.PyTorchJobSpec{
					PyTorchReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.PyTorchJobReplicaTypeWorker: {},
					},
				},
			},
			wantReplicaTypes: []kftraining.ReplicaType{
				kftraining.PyTorchJobReplicaTypeWorker,
			},
		},
	}
	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			pytorchJob := fromObject(&tc.job)
			gotReplicaTypes := pytorchJob.OrderedReplicaTypes()
			if diff := cmp.Diff(tc.wantReplicaTypes, gotReplicaTypes); len(diff) != 0 {
				t.Errorf("Unexpected response (-want, +got): %v", diff)
			}
		})
	}
}

func TestPodSets(t *testing.T) {
	testCases := map[string]struct {
		job         *kftraining.PyTorchJob
		wantPodSets func(job *kftraining.PyTorchJob) []kueue.PodSet
	}{
		"no annotations": {
			job: testingpytorchjob.MakePyTorchJob("pytorchjob", "ns").
				PyTorchReplicaSpecs(
					testingpytorchjob.PyTorchReplicaSpecRequirement{
						ReplicaType:  kftraining.PyTorchJobReplicaTypeMaster,
						ReplicaCount: 1,
					},
					testingpytorchjob.PyTorchReplicaSpecRequirement{
						ReplicaType:  kftraining.PyTorchJobReplicaTypeWorker,
						ReplicaCount: 1,
					},
				).
				Obj(),
			wantPodSets: func(job *kftraining.PyTorchJob) []kueue.PodSet {
				return []kueue.PodSet{
					{
						Name:     strings.ToLower(string(kftraining.PyTorchJobReplicaTypeMaster)),
						Template: job.Spec.PyTorchReplicaSpecs[kftraining.PyTorchJobReplicaTypeMaster].Template,
						Count:    1,
					},
					{
						Name:     strings.ToLower(string(kftraining.PyTorchJobReplicaTypeWorker)),
						Template: job.Spec.PyTorchReplicaSpecs[kftraining.PyTorchJobReplicaTypeWorker].Template,
						Count:    1,
					},
				}
			},
		},
		"with required and preferred topology annotation": {
			job: testingpytorchjob.MakePyTorchJob("pytorchjob", "ns").
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
						ReplicaCount: 1,
						Annotations: map[string]string{
							kueuealpha.PodSetPreferredTopologyAnnotation: "cloud.com/block",
						},
					},
				).
				Obj(),
			wantPodSets: func(job *kftraining.PyTorchJob) []kueue.PodSet {
				return []kueue.PodSet{
					{
						Name:            strings.ToLower(string(kftraining.PyTorchJobReplicaTypeMaster)),
						Template:        job.Spec.PyTorchReplicaSpecs[kftraining.PyTorchJobReplicaTypeMaster].Template,
						Count:           1,
						TopologyRequest: &kueue.PodSetTopologyRequest{Required: ptr.To("cloud.com/rack")},
					},
					{
						Name:            strings.ToLower(string(kftraining.PyTorchJobReplicaTypeWorker)),
						Template:        job.Spec.PyTorchReplicaSpecs[kftraining.PyTorchJobReplicaTypeWorker].Template,
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
		job      *kftraining.PyTorchJob
		wantErrs field.ErrorList
	}{
		"no annotations": {
			job: testingpytorchjob.MakePyTorchJob("pytorchjob", "ns").
				PyTorchReplicaSpecs(
					testingpytorchjob.PyTorchReplicaSpecRequirement{
						ReplicaType:  kftraining.PyTorchJobReplicaTypeMaster,
						ReplicaCount: 1,
					},
					testingpytorchjob.PyTorchReplicaSpecRequirement{
						ReplicaType:  kftraining.PyTorchJobReplicaTypeWorker,
						ReplicaCount: 1,
					},
				).
				Obj(),
		},
		"valid TAS request": {
			job: testingpytorchjob.MakePyTorchJob("pytorchjob", "ns").
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
						ReplicaCount: 3,
						Annotations: map[string]string{
							kueuealpha.PodSetPreferredTopologyAnnotation: "cloud.com/block",
						},
					},
				).
				Obj(),
		},
		"invalid TAS request": {
			job: testingpytorchjob.MakePyTorchJob("pytorchjob", "ns").
				PyTorchReplicaSpecs(
					testingpytorchjob.PyTorchReplicaSpecRequirement{
						ReplicaType:  kftraining.PyTorchJobReplicaTypeMaster,
						ReplicaCount: 1,
						Annotations: map[string]string{
							kueuealpha.PodSetRequiredTopologyAnnotation:  "cloud.com/rack",
							kueuealpha.PodSetPreferredTopologyAnnotation: "cloud.com/block",
						},
					},
					testingpytorchjob.PyTorchReplicaSpecRequirement{
						ReplicaType:  kftraining.PyTorchJobReplicaTypeWorker,
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
					field.NewPath("spec", "pytorchReplicaSpecs").
						Key("Master").
						Child("template", "metadata", "annotations"),
					field.OmitValueType{},
					`must not contain both "kueue.x-k8s.io/podset-required-topology" and "kueue.x-k8s.io/podset-preferred-topology"`,
				),
				field.Invalid(
					field.NewPath("spec", "pytorchReplicaSpecs").
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
