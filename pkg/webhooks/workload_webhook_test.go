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
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
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
	firstAdmissionChecksPath := statusPath.Child("admissionChecks").Index(0)
	podSetUpdatePath := firstAdmissionChecksPath.Child("podSetUpdates")
	firstPodSetSpecPath := podSetsPath.Index(0).Child("template", "spec")
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
				ReserveQuota(testingutil.MakeAdmission("@invalid").Obj()).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(statusPath.Child("admission", "clusterQueue"), nil, ""),
			},
		},
		"should have a valid podSet name in status assigment": {
			workload: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				ReserveQuota(testingutil.MakeAdmission("cluster-queue", "@invalid").Obj()).
				Obj(),
			wantErr: field.ErrorList{
				field.NotFound(statusPath.Child("admission", "podSetAssignments").Index(0).Child("name"), nil),
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
				ReserveQuota(testingutil.MakeAdmission("cluster-queue", "main1", "main2", "main3").Obj()).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(statusPath.Child("admission", "podSetAssignments"), nil, ""),
				field.NotFound(statusPath.Child("admission", "podSetAssignments").Index(2).Child("name"), nil),
			},
		},
		"assignment usage should be divisible by count": {
			workload: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(*testingutil.MakePodSet("main", 3).
					Request(corev1.ResourceCPU, "1").
					Obj()).
				ReserveQuota(testingutil.MakeAdmission("cluster-queue").
					Assignment(corev1.ResourceCPU, "flv", "1").
					AssignmentPodCount(3).
					Obj()).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(statusPath.Child("admission", "podSetAssignments").Index(0).Child("resourceUsage").Key(string(corev1.ResourceCPU)), nil, ""),
			},
		},
		"should not request num-pods resource": {
			workload: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(kueue.PodSet{
					Name:  "bad",
					Count: 1,
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							InitContainers: []corev1.Container{
								{
									Resources: corev1.ResourceRequirements{
										Requests: corev1.ResourceList{
											corev1.ResourcePods: resource.MustParse("1"),
										},
									},
								},
							},
							Containers: []corev1.Container{
								{
									Resources: corev1.ResourceRequirements{
										Requests: corev1.ResourceList{
											corev1.ResourcePods: resource.MustParse("1"),
										},
									},
								},
							},
						},
					},
				}).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(firstPodSetSpecPath.Child("initContainers").Index(0).Child("resources", "requests").Key(string(corev1.ResourcePods)), nil, ""),
				field.Invalid(firstPodSetSpecPath.Child("containers").Index(0).Child("resources", "requests").Key(string(corev1.ResourcePods)), nil, ""),
			},
		},
		"empty podSetUpdates": {
			workload: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).AdmissionChecks(kueue.AdmissionCheckState{}).Obj(),
			wantErr:  nil,
		},
		"should podSetUpdates have the same number of podSets": {
			workload: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).PodSets(
				*testingutil.MakePodSet("first", 1).Obj(),
				*testingutil.MakePodSet("second", 1).Obj(),
			).AdmissionChecks(
				kueue.AdmissionCheckState{PodSetUpdates: []kueue.PodSetUpdate{{Name: "first"}}},
			).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(podSetUpdatePath, nil, "must have the same number of podSetUpdates as the podSets"),
			},
		},
		"mismatched names in podSetUpdates with names in podSets": {
			workload: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).PodSets(
				*testingutil.MakePodSet("first", 1).Obj(),
				*testingutil.MakePodSet("second", 1).Obj(),
			).AdmissionChecks(
				kueue.AdmissionCheckState{PodSetUpdates: []kueue.PodSetUpdate{{Name: "first"}, {Name: "third"}}},
			).Obj(),
			wantErr: field.ErrorList{
				field.NotSupported(firstAdmissionChecksPath.Child("podSetUpdates").Index(1).Child("name"), nil, nil),
			},
		},
		"matched names in podSetUpdates with names in podSets": {
			workload: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).PodSets(
				*testingutil.MakePodSet("first", 1).Obj(),
				*testingutil.MakePodSet("second", 1).Obj(),
			).AdmissionChecks(
				kueue.AdmissionCheckState{
					PodSetUpdates: []kueue.PodSetUpdate{
						{
							Name:        "first",
							Labels:      map[string]string{"l1": "first"},
							Annotations: map[string]string{"foo": "bar"},
							Tolerations: []corev1.Toleration{
								{
									Key:               "t1",
									Operator:          corev1.TolerationOpEqual,
									Value:             "t1v",
									Effect:            corev1.TaintEffectNoExecute,
									TolerationSeconds: ptr.To[int64](5),
								},
							},
							NodeSelector: map[string]string{"type": "first"},
						},
						{
							Name:        "second",
							Labels:      map[string]string{"l2": "second"},
							Annotations: map[string]string{"foo": "baz"},
							Tolerations: []corev1.Toleration{
								{
									Key:               "t2",
									Operator:          corev1.TolerationOpEqual,
									Value:             "t2v",
									Effect:            corev1.TaintEffectNoExecute,
									TolerationSeconds: ptr.To[int64](10),
								},
							},
							NodeSelector: map[string]string{"type": "second"},
						},
					},
				},
			).Obj(),
		},
		"invalid label name of podSetUpdate": {
			workload: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				AdmissionChecks(
					kueue.AdmissionCheckState{PodSetUpdates: []kueue.PodSetUpdate{{Name: "main", Labels: map[string]string{"@abc": "foo"}}}},
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(podSetUpdatePath.Index(0).Child("labels"), "@abc", ""),
			},
		},
		"invalid node selector name of podSetUpdate": {
			workload: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				AdmissionChecks(
					kueue.AdmissionCheckState{PodSetUpdates: []kueue.PodSetUpdate{{Name: "main", NodeSelector: map[string]string{"@abc": "foo"}}}},
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(podSetUpdatePath.Index(0).Child("nodeSelector"), "@abc", ""),
			},
		},
		"invalid label value of podSetUpdate": {
			workload: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				AdmissionChecks(
					kueue.AdmissionCheckState{PodSetUpdates: []kueue.PodSetUpdate{{Name: "main", Labels: map[string]string{"foo": "@abc"}}}},
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(podSetUpdatePath.Index(0).Child("labels"), "@abc", ""),
			},
		},
		"invalid reclaimablePods": {
			workload: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					*testingutil.MakePodSet("ps1", 3).Obj(),
				).
				ReclaimablePods(
					kueue.ReclaimablePod{Name: "ps1", Count: 4},
					kueue.ReclaimablePod{Name: "ps2", Count: 1},
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(statusPath.Child("reclaimablePods").Key("ps1").Child("count"), nil, ""),
				field.NotSupported(statusPath.Child("reclaimablePods").Key("ps2").Child("name"), nil, nil),
			},
		},
		"invalid podSet minCount (negative)": {
			workload: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					*testingutil.MakePodSet("ps1", 3).SetMinimumCount(-1).Obj(),
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(podSetsPath.Index(0).Child("minCount"), ""),
			},
		},
		"invalid podSet minCount (too big)": {
			workload: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					*testingutil.MakePodSet("ps1", 3).SetMinimumCount(4).Obj(),
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(podSetsPath.Index(0).Child("minCount"), ""),
			},
		},
		"too many variable count podSets": {
			workload: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					*testingutil.MakePodSet("ps1", 3).SetMinimumCount(2).Obj(),
					*testingutil.MakePodSet("ps2", 3).SetMinimumCount(1).Obj(),
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(podSetsPath, nil, ""),
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
				ReserveQuota(testingutil.MakeAdmission("cq").Obj()).Obj(),
		},
		"queueName should not be updated once admitted": {
			before: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).Queue("q1").
				ReserveQuota(testingutil.MakeAdmission("cq").Obj()).Obj(),
			after: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).Queue("q2").
				ReserveQuota(testingutil.MakeAdmission("cq").Obj()).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec").Child("queueName"), nil, ""),
			},
		},
		"queueName can be updated when admission is reset": {
			before: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).Queue("q1").
				ReserveQuota(testingutil.MakeAdmission("cq").Obj()).Obj(),
			after: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).Queue("q2").Obj(),
		},
		"admission can be set": {
			before: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).Obj(),
			after: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).ReserveQuota(
				testingutil.MakeAdmission("cluster-queue").Assignment("on-demand", "5", "1").Obj(),
			).Obj(),
			wantErr: nil,
		},
		"admission can be unset": {
			before: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).ReserveQuota(
				testingutil.MakeAdmission("cluster-queue").Assignment("on-demand", "5", "1").Obj(),
			).Obj(),
			after:   testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).Obj(),
			wantErr: nil,
		},
		"admission should not be updated once set": {
			before: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).ReserveQuota(
				testingutil.MakeAdmission("cluster-queue").Obj(),
			).Obj(),
			after: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).ReserveQuota(
				testingutil.MakeAdmission("cluster-queue").Assignment("on-demand", "5", "1").Obj(),
			).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("status", "admission"), nil, ""),
			},
		},

		"reclaimable pod count can change up": {
			before: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					*testingutil.MakePodSet("ps1", 3).Obj(),
					*testingutil.MakePodSet("ps2", 3).Obj(),
				).
				ReserveQuota(
					testingutil.MakeAdmission("cluster-queue").
						PodSets(kueue.PodSetAssignment{Name: "ps1"}, kueue.PodSetAssignment{Name: "ps2"}).
						Obj(),
				).
				ReclaimablePods(
					kueue.ReclaimablePod{Name: "ps1", Count: 1},
				).
				Obj(),
			after: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					*testingutil.MakePodSet("ps1", 3).Obj(),
					*testingutil.MakePodSet("ps2", 3).Obj(),
				).
				ReserveQuota(
					testingutil.MakeAdmission("cluster-queue").
						PodSets(kueue.PodSetAssignment{Name: "ps1"}, kueue.PodSetAssignment{Name: "ps2"}).
						Obj(),
				).
				ReclaimablePods(
					kueue.ReclaimablePod{Name: "ps1", Count: 2},
					kueue.ReclaimablePod{Name: "ps2", Count: 1},
				).
				Obj(),
			wantErr: nil,
		},
		"reclaimable pod count cannot change down": {
			before: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					*testingutil.MakePodSet("ps1", 3).Obj(),
					*testingutil.MakePodSet("ps2", 3).Obj(),
				).
				ReserveQuota(
					testingutil.MakeAdmission("cluster-queue").
						PodSets(kueue.PodSetAssignment{Name: "ps1"}, kueue.PodSetAssignment{Name: "ps2"}).
						Obj(),
				).
				ReclaimablePods(
					kueue.ReclaimablePod{Name: "ps1", Count: 2},
					kueue.ReclaimablePod{Name: "ps2", Count: 1},
				).
				Obj(),
			after: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					*testingutil.MakePodSet("ps1", 3).Obj(),
					*testingutil.MakePodSet("ps2", 3).Obj(),
				).
				ReserveQuota(
					testingutil.MakeAdmission("cluster-queue").
						PodSets(kueue.PodSetAssignment{Name: "ps1"}, kueue.PodSetAssignment{Name: "ps2"}).
						Obj(),
				).
				ReclaimablePods(
					kueue.ReclaimablePod{Name: "ps1", Count: 1},
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("status", "reclaimablePods").Key("ps1").Child("count"), nil, ""),
				field.Required(field.NewPath("status", "reclaimablePods").Key("ps2"), ""),
			},
		},
		"reclaimable pod count can go to 0 if the job is suspended": {
			before: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					*testingutil.MakePodSet("ps1", 3).Obj(),
					*testingutil.MakePodSet("ps2", 3).Obj(),
				).
				ReserveQuota(
					testingutil.MakeAdmission("cluster-queue").
						PodSets(kueue.PodSetAssignment{Name: "ps1"}, kueue.PodSetAssignment{Name: "ps2"}).
						Obj(),
				).
				ReclaimablePods(
					kueue.ReclaimablePod{Name: "ps1", Count: 2},
					kueue.ReclaimablePod{Name: "ps2", Count: 1},
				).
				Obj(),
			after: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					*testingutil.MakePodSet("ps1", 3).Obj(),
					*testingutil.MakePodSet("ps2", 3).Obj(),
				).
				AdmissionChecks(kueue.AdmissionCheckState{
					PodSetUpdates: []kueue.PodSetUpdate{{Name: "ps1"}, {Name: "ps2"}},
					State:         kueue.CheckStateReady,
				}).
				ReclaimablePods(
					kueue.ReclaimablePod{Name: "ps1", Count: 0},
					kueue.ReclaimablePod{Name: "ps2", Count: 1},
				).
				Obj(),
			wantErr: nil,
		},
		"priorityClassSource should not be updated": {
			before: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).Queue("q").
				PriorityClass("test-class").PriorityClassSource(constants.PodPriorityClassSource).
				Priority(10).Obj(),
			after: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).Queue("q").
				PriorityClass("test-class").PriorityClassSource(constants.WorkloadPriorityClassSource).
				Priority(10).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec").Child("priorityClassSource"), nil, ""),
			},
		},
		"priorityClassName should not be updated": {
			before: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).Queue("q").
				PriorityClass("test-class-1").PriorityClassSource(constants.PodPriorityClassSource).
				Priority(10).Obj(),
			after: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).Queue("q").
				PriorityClass("test-class-2").PriorityClassSource(constants.PodPriorityClassSource).
				Priority(10).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec").Child("priorityClassName"), nil, ""),
			},
		},
		"podSetUpdates should be immutable when state is ready": {
			before: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).PodSets(
				*testingutil.MakePodSet("first", 1).Obj(),
				*testingutil.MakePodSet("second", 1).Obj(),
			).AdmissionChecks(kueue.AdmissionCheckState{
				PodSetUpdates: []kueue.PodSetUpdate{{Name: "first", Labels: map[string]string{"foo": "bar"}}, {Name: "second"}},
				State:         kueue.CheckStateReady,
			}).Obj(),
			after: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).PodSets(
				*testingutil.MakePodSet("first", 1).Obj(),
				*testingutil.MakePodSet("second", 1).Obj(),
			).AdmissionChecks(kueue.AdmissionCheckState{
				PodSetUpdates: []kueue.PodSetUpdate{{Name: "first", Labels: map[string]string{"foo": "baz"}}, {Name: "second"}},
				State:         kueue.CheckStateReady,
			}).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("status").Child("admissionChecks").Index(0).Child("podSetUpdates"), nil, ""),
			},
		},
		"should change other fields of admissionchecks when podSetUpdates is immutable": {
			before: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).PodSets(
				*testingutil.MakePodSet("first", 1).Obj(),
				*testingutil.MakePodSet("second", 1).Obj(),
			).AdmissionChecks(kueue.AdmissionCheckState{
				Name:          "ac1",
				Message:       "old",
				PodSetUpdates: []kueue.PodSetUpdate{{Name: "first", Labels: map[string]string{"foo": "bar"}}, {Name: "second"}},
				State:         kueue.CheckStateReady,
			}).Obj(),
			after: testingutil.MakeWorkload(testWorkloadName, testWorkloadNamespace).PodSets(
				*testingutil.MakePodSet("first", 1).Obj(),
				*testingutil.MakePodSet("second", 1).Obj(),
			).AdmissionChecks(kueue.AdmissionCheckState{
				Name:               "ac1",
				Message:            "new",
				LastTransitionTime: metav1.NewTime(time.Now()),
				PodSetUpdates:      []kueue.PodSetUpdate{{Name: "first", Labels: map[string]string{"foo": "bar"}}, {Name: "second"}},
				State:              kueue.CheckStateReady,
			}).Obj(),
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
