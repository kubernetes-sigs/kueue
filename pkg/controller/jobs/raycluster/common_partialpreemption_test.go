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

package raycluster

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	testingrayutil "sigs.k8s.io/kueue/pkg/util/testingjobs/raycluster"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

func workerGroup(name string, replicas, numOfHosts int32, annotations map[string]string) rayv1.WorkerGroupSpec {
	return rayv1.WorkerGroupSpec{
		GroupName:  name,
		Replicas:   new(replicas),
		NumOfHosts: numOfHosts,
		Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Annotations: annotations},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: name}}},
		},
	}
}

func minCountAnnotation(value string) map[string]string {
	return map[string]string{constants.PodSetPartialPreemptionMinCountAnnotation: value}
}

func TestBuildPodSetsPartialPreemption(t *testing.T) {
	headGroup := rayv1.HeadGroupSpec{
		Template: corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "head"}}},
		},
	}
	testCases := map[string]struct {
		workerGroups []rayv1.WorkerGroupSpec
		disableGate  bool
		// wantMinCounts maps each worker PodSet to its expected partial-preemption minimum;
		// a missing entry means partialPreemption is unset.
		wantMinCounts map[kueue.PodSetReference]int32
	}{
		"a floor per worker group": {
			workerGroups: []rayv1.WorkerGroupSpec{
				workerGroup("cpu", 4, 0, minCountAnnotation("2")),
				workerGroup("gpu", 8, 1, minCountAnnotation("0")),
				workerGroup("fixed", 2, 0, nil),
			},
			wantMinCounts: map[kueue.PodSetReference]int32{"cpu": 2, "gpu": 0},
		},
		"multi-host worker group": {
			workerGroups: []rayv1.WorkerGroupSpec{workerGroup("workers", 2, 4, minCountAnnotation("4"))},
		},
		"gate disabled": {
			workerGroups: []rayv1.WorkerGroupSpec{workerGroup("workers", 4, 0, minCountAnnotation("1"))},
			disableGate:  true,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.PartialPreemption, !tc.disableGate)
			podSets, err := BuildPodSets(&rayv1.RayClusterSpec{HeadGroupSpec: headGroup, WorkerGroupSpecs: tc.workerGroups}, nil)
			if err != nil {
				t.Fatalf("BuildPodSets() error: %v", err)
			}
			gotMinCounts := make(map[kueue.PodSetReference]int32)
			for _, ps := range podSets {
				if ps.PartialPreemption != nil {
					gotMinCounts[ps.Name] = *ps.PartialPreemption.MinCount
				}
			}
			if diff := cmp.Diff(tc.wantMinCounts, gotMinCounts, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unexpected partial-preemption minimums (-want +got):\n%s", diff)
			}
		})
	}
}

func TestValidatePartialPreemption(t *testing.T) {
	minCountPath := func(i int) *field.Path {
		return field.NewPath("spec", "workerGroupSpecs").Index(i).Child("template", "metadata", "annotations").Key(constants.PodSetPartialPreemptionMinCountAnnotation)
	}
	elastic := func() *testingrayutil.ClusterWrapper {
		return testingrayutil.MakeCluster("raycluster", "ns").SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue)
	}
	testCases := map[string]struct {
		object       client.Object
		head         rayv1.HeadGroupSpec
		workerGroups []rayv1.WorkerGroupSpec
		wantErrors   field.ErrorList
	}{
		"elastic job that opts in": {
			object:       elastic().SetAnnotation(constants.PartialPreemptionAnnotation, "true").Obj(),
			workerGroups: []rayv1.WorkerGroupSpec{workerGroup("workers", 4, 0, minCountAnnotation("1"))},
		},
		"opt-in without the elastic-job annotation": {
			object: testingrayutil.MakeCluster("raycluster", "ns").SetAnnotation(constants.PartialPreemptionAnnotation, "true").Obj(),
			wantErrors: field.ErrorList{
				field.Invalid(partialPreemptionAnnotationPath, "true",
					fmt.Sprintf("partial preemption requires the %q: %q annotation", workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue)),
			},
		},
		"invalid minimum and multi-host worker group": {
			object: elastic().SetAnnotation(constants.PartialPreemptionAnnotation, "true").Obj(),
			workerGroups: []rayv1.WorkerGroupSpec{
				workerGroup("cpu", 4, 0, minCountAnnotation("two")),
				workerGroup("tpu", 2, 4, minCountAnnotation("4")),
			},
			wantErrors: field.ErrorList{
				field.Invalid(minCountPath(0), "two", `must be a non-negative integer, got "two"`),
				field.Forbidden(minCountPath(1), "partial preemption is not supported for worker groups with numOfHosts > 1"),
			},
		},
		"minimum on the head": {
			object: elastic().SetAnnotation(constants.PartialPreemptionAnnotation, "true").Obj(),
			head: rayv1.HeadGroupSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{constants.PodSetPartialPreemptionMinCountAnnotation: "1"},
			}}},
			wantErrors: field.ErrorList{
				field.Forbidden(field.NewPath("spec", "headGroupSpec", "template", "metadata", "annotations").Key(constants.PodSetPartialPreemptionMinCountAnnotation),
					"partial preemption is not supported for the head group"),
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlices, true)
			features.SetFeatureGateDuringTest(t, features.PartialPreemption, true)
			spec := &rayv1.RayClusterSpec{HeadGroupSpec: tc.head, WorkerGroupSpecs: tc.workerGroups}
			gotErrors := validatePartialPreemption(tc.object, spec, field.NewPath("spec"))
			if diff := cmp.Diff(tc.wantErrors, gotErrors); diff != "" {
				t.Errorf("Unexpected errors (-want +got):\n%s", diff)
			}
		})
	}
}
