/*
Copyright 2022 The Kubernetes Authors.

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

package webhooks

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	testingutil "sigs.k8s.io/kueue/pkg/util/testing"
)

const (
	testWorkloadName      = "test-workload"
	testWorkloadNamespace = "test-ns"
)

func TestWorkloadWebhookDefault(t *testing.T) {
	cases := map[string]struct {
		wl     kueue.Workload
		wantWl kueue.Workload
	}{
		"add default podSet name": {
			wl: kueue.Workload{
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{
						{},
					},
				},
			},
			wantWl: kueue.Workload{
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{
						{Name: "main"},
					},
				},
			},
		},
		"don't set podSetName if multiple": {
			wl: kueue.Workload{
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{
						{},
						{},
					},
				},
			},
			wantWl: kueue.Workload{
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{
						{},
						{},
					},
				},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			wh := &WorkloadWebhook{}
			wlCopy := tc.wl.DeepCopy()
			if err := wh.Default(context.Background(), wlCopy); err != nil {
				t.Fatalf("Could not apply defaults: %v", err)
			}
			if diff := cmp.Diff(tc.wantWl, *wlCopy); diff != "" {
				t.Errorf("Obtained wrong defaults (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestValidateWorkload(t *testing.T) {
	specPath := field.NewPath("spec")
	podSetsPath := specPath.Child("podSets")
	statusPath := field.NewPath("status")
	testCases := map[string]struct {
		workload *kueue.Workload
		wantErr  field.ErrorList
	}{
		"valid": {
			workload: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).PodSets(
				kueue.PodSet{
					Name:  "driver",
					Count: 1,
				},
				kueue.PodSet{
					Name:  "workers",
					Count: 100,
				},
			).Obj(),
		},
		"should have a valid podSet name": {
			workload: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).PodSets(
				kueue.PodSet{
					Name:  "@driver",
					Count: 1,
				},
			).Obj(),
			wantErr: field.ErrorList{field.Invalid(podSetsPath.Index(0).Child("name"), nil, "")},
		},
		"should have valid priorityClassName": {
			workload: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PriorityClass("invalid_class").
				Priority(0).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(specPath.Child("priorityClassName"), nil, ""),
			},
		},
		"should pass validation when priorityClassName is empty": {
			workload: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).Obj(),
			wantErr:  nil,
		},
		"should have priority once priorityClassName is set": {
			workload: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PriorityClass("priority").
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(specPath.Child("priority"), nil, ""),
			},
		},
		"should have a valid queueName": {
			workload: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				Queue("@invalid").
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(specPath.Child("queueName"), nil, ""),
			},
		},
		"should have a valid clusterQueue name": {
			workload: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				Admit(testingutil.MakeAdmission("@invalid").Obj()).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(statusPath.Child("admission", "clusterQueue"), nil, ""),
			},
		},
		"should have a valid podSet name in status": {
			workload: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				Admit(testingutil.MakeAdmission("cluster-queue", "@invalid").Obj()).
				Obj(),
			wantErr: field.ErrorList{
				field.NotFound(statusPath.Child("admission", "podSetFlavors").Index(0).Child("name"), nil),
			},
		},
		"should have same podSets in admission": {
			workload: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					kueue.PodSet{
						Name:  "main2",
						Count: 1,
					},
					kueue.PodSet{
						Name:  "main1",
						Count: 1,
					},
				).
				Admit(testingutil.MakeAdmission("cluster-queue", "main1", "main2", "main3").Obj()).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(statusPath.Child("admission", "podSetFlavors"), nil, ""),
				field.NotFound(statusPath.Child("admission", "podSetFlavors").Index(2).Child("name"), nil),
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			gotErr := ValidateWorkload(tc.workload)
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.IgnoreFields(field.Error{}, "Detail", "BadValue")); diff != "" {
				t.Errorf("ValidateWorkload() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestValidateWorkloadUpdate(t *testing.T) {
	testCases := map[string]struct {
		before, after *kueue.Workload
		wantErr       field.ErrorList
	}{
		"podSets should not be updated: count": {
			before: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).Obj(),
			after: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).PodSets(
				*testingutil.MakePodSet("main", 2).Obj(),
			).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec").Child("podSets"), nil, ""),
			},
		},
		"podSets should not be updated: podSpec": {
			before: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).Obj(),
			after: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).PodSets(
				kueue.PodSet{
					Name:  "main",
					Count: 1,
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name: "c-after",
									Resources: corev1.ResourceRequirements{
										Requests: make(corev1.ResourceList),
									},
								},
							},
						},
					},
				},
			).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec").Child("podSets"), nil, ""),
			},
		},
		"queueName can be updated when not admitted": {
			before:  testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).Queue("q1").Obj(),
			after:   testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).Queue("q2").Obj(),
			wantErr: nil,
		},
		"queueName can be updated when admitting": {
			before: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).Obj(),
			after: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).Queue("q").
				Admit(testingutil.MakeAdmission("cq").Obj()).Obj(),
		},
		"queueName should not be updated once admitted": {
			before: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).Queue("q1").
				Admit(testingutil.MakeAdmission("cq").Obj()).Obj(),
			after: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).Queue("q2").
				Admit(testingutil.MakeAdmission("cq").Obj()).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec").Child("queueName"), nil, ""),
			},
		},
		"queueName can be updated when admission is reset": {
			before: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).Queue("q1").
				Admit(testingutil.MakeAdmission("cq").Obj()).Obj(),
			after: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).Queue("q2").Obj(),
		},
		"admission can be set": {
			before: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).Obj(),
			after: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).Admit(
				testingutil.MakeAdmission("cluster-queue").Assignment("on-demand", "5", "1").Obj(),
			).Obj(),
			wantErr: nil,
		},
		"admission can be unset": {
			before: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).Admit(
				testingutil.MakeAdmission("cluster-queue").Assignment("on-demand", "5", "1").Obj(),
			).Obj(),
			after:   testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).Obj(),
			wantErr: nil,
		},
		"admission should not be updated once set": {
			before: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).Admit(
				testingutil.MakeAdmission("cluster-queue").Obj(),
			).Obj(),
			after: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).Admit(
				testingutil.MakeAdmission("cluster-queue").Assignment("on-demand", "5", "1").Obj(),
			).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("status", "admission"), nil, ""),
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			errList := ValidateWorkloadUpdate(tc.after, tc.before)
			if diff := cmp.Diff(tc.wantErr, errList, cmpopts.IgnoreFields(field.Error{}, "Detail", "BadValue")); diff != "" {
				t.Errorf("ValidateWorkloadUpdate() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
