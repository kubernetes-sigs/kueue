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

package jaxjob

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/component-base/featuregate"
	"k8s.io/utils/ptr"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingJAXjob "sigs.k8s.io/kueue/pkg/util/testingjobs/jaxjob"
)

func TestPriorityClass(t *testing.T) {
	testcases := map[string]struct {
		job                   kftraining.JAXJob
		wantPriorityClassName string
	}{
		"none priority class name specified": {
			job:                   kftraining.JAXJob{},
			wantPriorityClassName: "",
		},
		"priority specified at runPolicy and replicas; use priority in runPolicy": {
			job: kftraining.JAXJob{
				Spec: kftraining.JAXJobSpec{
					RunPolicy: kftraining.RunPolicy{
						SchedulingPolicy: &kftraining.SchedulingPolicy{
							PriorityClass: "scheduling-priority",
						},
					},
					JAXReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.JAXJobReplicaTypeWorker: {
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
	}

	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			JAXJob := fromObject(&tc.job)
			gotPriorityClassName := JAXJob.PriorityClass()
			if tc.wantPriorityClassName != gotPriorityClassName {
				t.Errorf("Unexpected response (want: %v, got: %v)", tc.wantPriorityClassName, gotPriorityClassName)
			}
		})
	}
}

func TestOrderedReplicaTypes(t *testing.T) {
	testcases := map[string]struct {
		job              kftraining.JAXJob
		wantReplicaTypes []kftraining.ReplicaType
	}{
		"job has no replicas": {
			job:              kftraining.JAXJob{},
			wantReplicaTypes: []kftraining.ReplicaType{},
		},
		"job has worker replicas": {
			job: kftraining.JAXJob{
				Spec: kftraining.JAXJobSpec{
					JAXReplicaSpecs: map[kftraining.ReplicaType]*kftraining.ReplicaSpec{
						kftraining.JAXJobReplicaTypeWorker: {},
					},
				},
			},
			wantReplicaTypes: []kftraining.ReplicaType{
				kftraining.JAXJobReplicaTypeWorker,
			},
		},
	}
	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			JAXJob := fromObject(&tc.job)
			gotReplicaTypes := JAXJob.OrderedReplicaTypes()
			if diff := cmp.Diff(tc.wantReplicaTypes, gotReplicaTypes); len(diff) != 0 {
				t.Errorf("Unexpected response (-want, +got): %v", diff)
			}
		})
	}
}

func TestPodSets(t *testing.T) {
	testCases := map[string]struct {
		job                *kftraining.JAXJob
		wantPodSets        func(job *kftraining.JAXJob) []kueue.PodSet
		enableFeatureGates []featuregate.Feature
	}{
		"no annotations": {
			job: testingJAXjob.MakeJAXJob("JAXjob", "ns").
				JAXReplicaSpecs(
					testingJAXjob.JAXReplicaSpecRequirement{
						ReplicaType:  kftraining.JAXJobReplicaTypeWorker,
						ReplicaCount: 1,
					},
				).
				Obj(),
			wantPodSets: func(job *kftraining.JAXJob) []kueue.PodSet {
				return []kueue.PodSet{
					*utiltesting.MakePodSet(kueue.NewPodSetReference(string(kftraining.JAXJobReplicaTypeWorker)), 1).
						PodSpec(job.Spec.JAXReplicaSpecs[kftraining.JAXJobReplicaTypeWorker].Template.Spec).
						Obj(),
				}
			},
		},
		"with required and preferred topology annotation": {
			job: testingJAXjob.MakeJAXJob("JAXjob", "ns").
				JAXReplicaSpecs(
					testingJAXjob.JAXReplicaSpecRequirement{
						ReplicaType:  kftraining.JAXJobReplicaTypeWorker,
						ReplicaCount: 1,
						Annotations: map[string]string{
							kueuealpha.PodSetPreferredTopologyAnnotation: "cloud.com/block",
						},
					},
				).
				Obj(),
			wantPodSets: func(job *kftraining.JAXJob) []kueue.PodSet {
				return []kueue.PodSet{
					*utiltesting.MakePodSet(kueue.NewPodSetReference(string(kftraining.JAXJobReplicaTypeWorker)), 1).
						PodSpec(job.Spec.JAXReplicaSpecs[kftraining.JAXJobReplicaTypeWorker].Template.Spec).
						Annotations(map[string]string{kueuealpha.PodSetPreferredTopologyAnnotation: "cloud.com/block"}).
						PreferredTopologyRequest("cloud.com/block").
						PodIndexLabel(ptr.To(kftraining.ReplicaIndexLabel)).
						Obj(),
				}
			},
			enableFeatureGates: []featuregate.Feature{
				features.TopologyAwareScheduling,
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			for _, gate := range tc.enableFeatureGates {
				features.SetFeatureGateDuringTest(t, gate, true)
			}
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
		job                     *kftraining.JAXJob
		wantValidationErrs      field.ErrorList
		wantErr                 error
		topologyAwareScheduling bool
	}{
		"no annotations": {
			job: testingJAXjob.MakeJAXJob("JAXjob", "ns").
				JAXReplicaSpecs(
					testingJAXjob.JAXReplicaSpecRequirement{
						ReplicaType:  kftraining.JAXJobReplicaTypeWorker,
						ReplicaCount: 1,
					},
				).
				Obj(),
		},
		"valid TAS request": {
			job: testingJAXjob.MakeJAXJob("JAXjob", "ns").
				JAXReplicaSpecs(
					testingJAXjob.JAXReplicaSpecRequirement{
						ReplicaType:  kftraining.JAXJobReplicaTypeWorker,
						ReplicaCount: 3,
						Annotations: map[string]string{
							kueuealpha.PodSetPreferredTopologyAnnotation: "cloud.com/block",
						},
					},
				).
				Obj(),
			topologyAwareScheduling: true,
		},
		"invalid TAS request": {
			job: testingJAXjob.MakeJAXJob("JAXjob", "ns").
				JAXReplicaSpecs(
					testingJAXjob.JAXReplicaSpecRequirement{
						ReplicaType:  kftraining.JAXJobReplicaTypeWorker,
						ReplicaCount: 3,
						Annotations: map[string]string{
							kueuealpha.PodSetRequiredTopologyAnnotation:  "cloud.com/rack",
							kueuealpha.PodSetPreferredTopologyAnnotation: "cloud.com/block",
						},
					},
				).
				Obj(),
			wantValidationErrs: field.ErrorList{
				field.Invalid(
					field.NewPath("spec", "jaxReplicaSpecs").
						Key("Worker").
						Child("template", "metadata", "annotations"),
					field.OmitValueType{},
					`must not contain more than one topology annotation: ["kueue.x-k8s.io/podset-required-topology", `+
						`"kueue.x-k8s.io/podset-preferred-topology", "kueue.x-k8s.io/podset-unconstrained-topology"]`),
			},
			topologyAwareScheduling: true,
		},
		"invalid slice topology request - slice size larger than number of podsets": {
			job: testingJAXjob.MakeJAXJob("JAXjob", "ns").
				JAXReplicaSpecs(
					testingJAXjob.JAXReplicaSpecRequirement{
						ReplicaType:  kftraining.JAXJobReplicaTypeWorker,
						ReplicaCount: 3,
						Annotations: map[string]string{
							kueuealpha.PodSetRequiredTopologyAnnotation:      "cloud.com/block",
							kueuealpha.PodSetSliceRequiredTopologyAnnotation: "cloud.com/block",
							kueuealpha.PodSetSliceSizeAnnotation:             "20",
						},
					},
				).
				Obj(),
			wantValidationErrs: field.ErrorList{
				field.Invalid(
					field.NewPath("spec", "jaxReplicaSpecs").
						Key("Worker").
						Child("template", "metadata", "annotations").
						Key("kueue.x-k8s.io/podset-slice-size"),
					"20", "must not be greater than pod set count 3",
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
				t.Errorf("validate create error list mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantValidationErrs, gotValidationErrs); diff != "" {
				t.Errorf("validate create validation errors list mismatch (-want +got):\n%s", diff)
			}

			gotValidationErrs, gotErr = fromObject(tc.job).ValidateOnUpdate(nil)
			if diff := cmp.Diff(tc.wantErr, gotErr); diff != "" {
				t.Errorf("validate create error list mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantValidationErrs, gotValidationErrs); diff != "" {
				t.Errorf("validate create validation errors list mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
