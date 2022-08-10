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
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation/field"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/util/pointer"
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
		"add podSet name": {
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
		"copy limits to requests": {
			wl: kueue.Workload{
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{
						{
							Name: "foo",
							Spec: corev1.PodSpec{
								InitContainers: []corev1.Container{
									{
										Resources: corev1.ResourceRequirements{
											Requests: corev1.ResourceList{
												"cpu": resource.MustParse("1"),
											},
											Limits: corev1.ResourceList{
												"cpu":    resource.MustParse("2"),
												"memory": resource.MustParse("1Mi"),
											},
										},
									},
								},
							},
						},
						{
							Name: "bar",
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{
									{
										Resources: corev1.ResourceRequirements{
											Limits: corev1.ResourceList{
												"cpu":    resource.MustParse("1.5"),
												"memory": resource.MustParse("5Mi"),
											},
										},
									},
									{
										Resources: corev1.ResourceRequirements{
											Requests: corev1.ResourceList{
												"cpu": resource.MustParse("1"),
											},
										},
									},
								},
							},
						},
					},
				},
			},
			wantWl: kueue.Workload{
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{
						{
							Name: "foo",
							Spec: corev1.PodSpec{
								InitContainers: []corev1.Container{
									{
										Resources: corev1.ResourceRequirements{
											Requests: corev1.ResourceList{
												"cpu":    resource.MustParse("1"),
												"memory": resource.MustParse("1Mi"),
											},
											Limits: corev1.ResourceList{
												"cpu":    resource.MustParse("2"),
												"memory": resource.MustParse("1Mi"),
											},
										},
									},
								},
							},
						},
						{
							Name: "bar",
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{
									{
										Resources: corev1.ResourceRequirements{
											Requests: corev1.ResourceList{
												"cpu":    resource.MustParse("1.5"),
												"memory": resource.MustParse("5Mi"),
											},
											Limits: corev1.ResourceList{
												"cpu":    resource.MustParse("1.5"),
												"memory": resource.MustParse("5Mi"),
											},
										},
									},
									{
										Resources: corev1.ResourceRequirements{
											Requests: corev1.ResourceList{
												"cpu": resource.MustParse("1"),
											},
										},
									},
								},
							},
						},
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
	specField := field.NewPath("spec")
	podSetsField := specField.Child("podSets")
	testCases := map[string]struct {
		workload *kueue.Workload
		wantErr  field.ErrorList
	}{
		"should have at least one podSet": {
			workload: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).PodSets(nil).Obj(),
			wantErr: field.ErrorList{
				field.Required(podSetsField, ""),
			},
		},
		"count should be greater than 0": {
			workload: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).PodSets([]kueue.PodSet{
				{
					Name:  "main",
					Count: -1,
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name: "c",
								Resources: corev1.ResourceRequirements{
									Requests: make(corev1.ResourceList),
								},
							},
						},
					},
				},
			}).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(podSetsField.Index(0).Child("count"), int32(-1), ""),
			},
		},
		"should have valid priorityClassName": {
			workload: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PriorityClass("invalid_class").
				Priority(pointer.Int32(0)).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(specField.Child("priorityClassName"), "invalid_class", ""),
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
				field.Invalid(specField.Child("priority"), (*int32)(nil), ""),
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			gotErr := ValidateWorkload(tc.workload)
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.IgnoreFields(field.Error{}, "Detail")); diff != "" {
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
			after: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).PodSets([]kueue.PodSet{
				{
					Name:  "main",
					Count: 2,
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name: "c",
								Resources: corev1.ResourceRequirements{
									Requests: make(corev1.ResourceList),
								},
							},
						},
					},
				},
			}).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec").Child("podSets"), nil, ""),
			},
		},
		"podSets should not be updated: podSpec": {
			before: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).Obj(),
			after: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).PodSets([]kueue.PodSet{
				{
					Name:  "main",
					Count: 1,
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
			}).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec").Child("podSets"), nil, ""),
			},
		},
		"queueName can be updated when not set": {
			before:  testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).Obj(),
			after:   testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).Queue("queue").Obj(),
			wantErr: nil,
		},
		"queueName should not be updated once set": {
			before: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).Queue("queue1").Obj(),
			after:  testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).Queue("queue2").Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec").Child("queueName"), nil, ""),
			},
		},
		"admission can be updated when not set": {
			before: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).Obj(),
			after: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).Admit(
				testingutil.MakeAdmission("cluster-queue").Flavor("on-demand", "5").Obj(),
			).Obj(),
			wantErr: nil,
		},
		"admission should not be updated once set": {
			before: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).Admit(
				testingutil.MakeAdmission("cluster-queue").Obj(),
			).Obj(),
			after: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).Admit(
				testingutil.MakeAdmission("cluster-queue").Flavor("on-demand", "5").Obj(),
			).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec").Child("admission"), nil, ""),
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
