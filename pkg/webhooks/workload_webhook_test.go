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

package webhooks

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/component-base/featuregate"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

const (
	testWorkloadName      = "test-workload"
	testWorkloadNamespace = "test-ns"
)

func TestValidateWorkload(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	specPath := field.NewPath("spec")
	podSetsPath := specPath.Child("podSets")
	statusPath := field.NewPath("status")
	firstAdmissionChecksPath := statusPath.Child("admissionChecks").Index(0)
	podSetUpdatePath := firstAdmissionChecksPath.Child("podSetUpdates")
	firstPodSetSpecPath := podSetsPath.Index(0).Child("template", "spec")
	testCases := map[string]struct {
		featureGates map[featuregate.Feature]bool
		workload     *kueue.Workload
		wantErr      error
		wantWarnings admission.Warnings
	}{
		"valid": {
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).PodSets(
				*utiltestingapi.MakePodSet("driver", 1).Obj(),
				*utiltestingapi.MakePodSet("workers", 100).Obj(),
			).Obj(),
		},
		"should have a valid podSet name in status assignment": {
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cluster-queue", "@invalid").Obj(), now).
				Obj(),
			wantErr: field.ErrorList{
				field.NotFound(statusPath.Child("admission", "podSetAssignments").Index(0).Child("name"), nil),
			}.ToAggregate(),
		},
		"assignment usage should be divisible by count": {
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).
					Request(corev1.ResourceCPU, "1").
					Obj()).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cluster-queue").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "flv", "1").
						Count(3).
						Obj()).
					Obj(), now).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(statusPath.Child("admission", "podSetAssignments").Index(0).Child("resourceUsage").Key(string(corev1.ResourceCPU)), nil, ""),
			}.ToAggregate(),
		},
		"should not request num-pods resource": {
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					*utiltestingapi.MakePodSet("bad", 1).
						InitContainers(utiltesting.SingleContainerForRequest(map[corev1.ResourceName]string{
							corev1.ResourcePods: "1",
						})...).
						Containers(utiltesting.SingleContainerForRequest(map[corev1.ResourceName]string{
							corev1.ResourcePods: "1",
						})...).
						Obj(),
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(firstPodSetSpecPath.Child("initContainers").Index(0).Child("resources", "requests").Key(string(corev1.ResourcePods)), nil, ""),
				field.Invalid(firstPodSetSpecPath.Child("containers").Index(0).Child("resources", "requests").Key(string(corev1.ResourcePods)), nil, ""),
			}.ToAggregate(),
		},
		"should reject reserved pods resource key in limits": {
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					*utiltestingapi.MakePodSet("bad", 1).
						InitContainers(corev1.Container{
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourcePods: resource.MustParse("1"),
								},
							},
						}).
						Containers(corev1.Container{
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourcePods: resource.MustParse("1"),
								},
							},
						}).
						Obj(),
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(firstPodSetSpecPath.Child("initContainers").Index(0).Child("resources", "limits").Key(string(corev1.ResourcePods)), nil, ""),
				field.Invalid(firstPodSetSpecPath.Child("containers").Index(0).Child("resources", "limits").Key(string(corev1.ResourcePods)), nil, ""),
			}.ToAggregate(),
		},
		"should reject negative container resource request": {
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					*utiltestingapi.MakePodSet("bad", 1).
						Containers(utiltesting.SingleContainerForRequest(map[corev1.ResourceName]string{
							corev1.ResourceCPU: "-1",
						})...).
						Obj(),
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(firstPodSetSpecPath.Child("containers").Index(0).Child("resources", "requests").Key(string(corev1.ResourceCPU)), nil, ""),
			}.ToAggregate(),
		},
		"should reject negative initContainer resource request": {
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					*utiltestingapi.MakePodSet("bad", 1).
						InitContainers(utiltesting.SingleContainerForRequest(map[corev1.ResourceName]string{
							corev1.ResourceCPU: "-1",
						})...).
						Obj(),
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(firstPodSetSpecPath.Child("initContainers").Index(0).Child("resources", "requests").Key(string(corev1.ResourceCPU)), nil, ""),
			}.ToAggregate(),
		},
		"should reject negative container resource limit": {
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					*utiltestingapi.MakePodSet("bad", 1).
						Containers(corev1.Container{
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU: resource.MustParse("-1"),
								},
							},
						}).
						Obj(),
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(firstPodSetSpecPath.Child("containers").Index(0).Child("resources", "limits").Key(string(corev1.ResourceCPU)), nil, ""),
			}.ToAggregate(),
		},
		"should reject negative resource among multiple valid requests and limits": {
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					*utiltestingapi.MakePodSet("bad", 1).
						Containers(corev1.Container{
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("-1Gi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("2"),
									corev1.ResourceMemory: resource.MustParse("-2Gi"),
								},
							},
						}).
						Obj(),
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(firstPodSetSpecPath.Child("containers").Index(0).Child("resources", "requests").Key(string(corev1.ResourceMemory)), nil, ""),
				field.Invalid(firstPodSetSpecPath.Child("containers").Index(0).Child("resources", "limits").Key(string(corev1.ResourceMemory)), nil, ""),
			}.ToAggregate(),
		},
		"should reject negative pod-level resource request": {
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					*utiltestingapi.MakePodSet("bad", 1).
						PodLevelRequest(corev1.ResourceCPU, "-1").
						Obj(),
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(firstPodSetSpecPath.Child("resources", "requests").Key(string(corev1.ResourceCPU)), nil, ""),
			}.ToAggregate(),
		},
		"should reject negative pod-level resource limit": {
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					*utiltestingapi.MakePodSet("bad", 1).
						PodLevelLimit(corev1.ResourceMemory, "-1Gi").
						Obj(),
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(firstPodSetSpecPath.Child("resources", "limits").Key(string(corev1.ResourceMemory)), nil, ""),
			}.ToAggregate(),
		},
		"should accept negative container resource request when WorkloadValidateResourcesAreNonNegative is disabled": {
			featureGates: map[featuregate.Feature]bool{
				features.WorkloadValidateResourcesAreNonNegative: false,
			},
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					*utiltestingapi.MakePodSet("ok", 1).
						Containers(utiltesting.SingleContainerForRequest(map[corev1.ResourceName]string{
							corev1.ResourceCPU: "-1",
						})...).
						Obj(),
				).
				Obj(),
			wantErr: nil,
		},
		"should accept zero container resource request": {
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					*utiltestingapi.MakePodSet("ok", 1).
						Containers(utiltesting.SingleContainerForRequest(map[corev1.ResourceName]string{
							corev1.ResourceCPU: "0",
						})...).
						Obj(),
				).
				Obj(),
			wantErr: nil,
		},
		"empty podSetUpdates": {
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).AdmissionChecks(kueue.AdmissionCheckState{}).Obj(),
			wantErr:  nil,
		},
		"should accept podSetUpdates for only a subset of podSets": {
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).PodSets(
				*utiltestingapi.MakePodSet("first", 1).Obj(),
				*utiltestingapi.MakePodSet("second", 1).Obj(),
			).AdmissionChecks(
				kueue.AdmissionCheckState{PodSetUpdates: []kueue.PodSetUpdate{{Name: "first"}}},
			).Obj(),
			wantErr: nil,
		},
		"mismatched names in podSetUpdates with names in podSets": {
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).PodSets(
				*utiltestingapi.MakePodSet("first", 1).Obj(),
				*utiltestingapi.MakePodSet("second", 1).Obj(),
			).AdmissionChecks(
				kueue.AdmissionCheckState{PodSetUpdates: []kueue.PodSetUpdate{{Name: "first"}, {Name: "third"}}},
			).Obj(),
			wantErr: field.ErrorList{
				field.NotSupported(firstAdmissionChecksPath.Child("podSetUpdates").Index(1).Child("name"), nil, []string{}),
			}.ToAggregate(),
		},
		"matched names in podSetUpdates with names in podSets": {
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).PodSets(
				*utiltestingapi.MakePodSet("first", 1).Obj(),
				*utiltestingapi.MakePodSet("second", 1).Obj(),
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
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				AdmissionChecks(
					kueue.AdmissionCheckState{PodSetUpdates: []kueue.PodSetUpdate{{Name: kueue.DefaultPodSetName, Labels: map[string]string{"@abc": "foo"}}}},
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(podSetUpdatePath.Index(0).Child("labels"), "@abc", "").
					WithOrigin("format=k8s-label-key"),
			}.ToAggregate(),
		},
		"invalid node selector name of podSetUpdate": {
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				AdmissionChecks(
					kueue.AdmissionCheckState{PodSetUpdates: []kueue.PodSetUpdate{{Name: kueue.DefaultPodSetName, NodeSelector: map[string]string{"@abc": "foo"}}}},
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(podSetUpdatePath.Index(0).Child("nodeSelector"), "@abc", "").
					WithOrigin("format=k8s-label-key"),
			}.ToAggregate(),
		},
		"invalid label value of podSetUpdate": {
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				AdmissionChecks(
					kueue.AdmissionCheckState{PodSetUpdates: []kueue.PodSetUpdate{{Name: kueue.DefaultPodSetName, Labels: map[string]string{"foo": "@abc"}}}},
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(podSetUpdatePath.Index(0).Child("labels"), "@abc", "").
					WithOrigin("format=k8s-label-value"),
			}.ToAggregate(),
		},
		"invalid reclaimablePods": {
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					*utiltestingapi.MakePodSet("ps1", 3).Obj(),
				).
				ReclaimablePods(
					kueue.ReclaimablePod{Name: "ps1", Count: 4},
					kueue.ReclaimablePod{Name: "ps2", Count: 1},
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(statusPath.Child("reclaimablePods").Key("ps1").Child("count"), nil, ""),
				field.NotSupported(statusPath.Child("reclaimablePods").Key("ps2").Child("name"), nil, []string{}),
			}.ToAggregate(),
		},
		"too many variable count podSets": {
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					*utiltestingapi.MakePodSet("ps1", 3).SetMinimumCount(2).Obj(),
					*utiltestingapi.MakePodSet("ps2", 3).SetMinimumCount(1).Obj(),
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(podSetsPath, nil, ""),
			}.ToAggregate(),
		},
		"valid priority-boost": {
			featureGates: map[featuregate.Feature]bool{features.PriorityBoost: true},
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(*utiltestingapi.MakePodSet("main", 1).Obj()).
				Annotation(controllerconstants.PriorityBoostAnnotationKey, "10").
				Obj(),
			wantErr: nil,
		},
		"valid priority-boost zero": {
			featureGates: map[featuregate.Feature]bool{features.PriorityBoost: true},
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(*utiltestingapi.MakePodSet("main", 1).Obj()).
				Annotation(controllerconstants.PriorityBoostAnnotationKey, "0").
				Obj(),
			wantErr: nil,
		},
		"valid priority-boost negative": {
			featureGates: map[featuregate.Feature]bool{features.PriorityBoost: true},
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(*utiltestingapi.MakePodSet("main", 1).Obj()).
				Annotation(controllerconstants.PriorityBoostAnnotationKey, "-5").
				Obj(),
			wantErr: nil,
		},
		"invalid priority-boost": {
			featureGates: map[featuregate.Feature]bool{features.PriorityBoost: true},
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(*utiltestingapi.MakePodSet("main", 1).Obj()).
				Annotation(controllerconstants.PriorityBoostAnnotationKey, "invalid").
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(priorityBoostAnnotationPath, "invalid", "must be a valid signed integer"),
			}.ToAggregate(),
		},
		"empty string priority-boost": {
			featureGates: map[featuregate.Feature]bool{features.PriorityBoost: true},
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(*utiltestingapi.MakePodSet("main", 1).Obj()).
				Annotation(controllerconstants.PriorityBoostAnnotationKey, "").
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(priorityBoostAnnotationPath, "", "must be a valid signed integer; use \"0\" explicitly, empty string is not allowed"),
			}.ToAggregate(),
		},
		"missing priority-boost annotation": {
			featureGates: map[featuregate.Feature]bool{features.PriorityBoost: true},
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(*utiltestingapi.MakePodSet("main", 1).Obj()).
				Obj(),
			wantErr: nil,
		},
		"invalid priority-boost when feature off": {
			featureGates: map[featuregate.Feature]bool{features.PriorityBoost: false},
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(*utiltestingapi.MakePodSet("main", 1).Obj()).
				Annotation(controllerconstants.PriorityBoostAnnotationKey, "invalid").
				Obj(),
			wantErr: nil,
		},
		"valid AdmissionGatedBy annotation with single gate": {
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				Annotations(map[string]string{
					constants.AdmissionGatedByAnnotation: "example.com/controller",
				}).
				PodSets(*utiltestingapi.MakePodSet("main", 1).Obj()).
				Obj(),
			wantErr: nil,
		},
		"valid AdmissionGatedBy annotation with multiple gates": {
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				Annotations(map[string]string{
					constants.AdmissionGatedByAnnotation: "example.com/a,not.example.com/b",
				}).
				PodSets(*utiltestingapi.MakePodSet("main", 1).Obj()).
				Obj(),
			wantErr: nil,
		},
		"AdmissionGatedBy annotation - leading space": {
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				Annotations(map[string]string{
					constants.AdmissionGatedByAnnotation: " example.com/gate",
				}).
				PodSets(*utiltestingapi.MakePodSet("main", 1).Obj()).
				Obj(),
			wantErr: nil,
		},
		"AdmissionGatedBy annotation - space before comma": {
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				Annotations(map[string]string{
					constants.AdmissionGatedByAnnotation: "example.com/gate ,example.com/gate2",
				}).
				PodSets(*utiltestingapi.MakePodSet("main", 1).Obj()).
				Obj(),
			wantErr: nil,
		},
		"AdmissionGatedBy annotation - space after comma": {
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				Annotations(map[string]string{
					constants.AdmissionGatedByAnnotation: "example.com/gate, example.com/gate2",
				}).
				PodSets(*utiltestingapi.MakePodSet("main", 1).Obj()).
				Obj(),
			wantErr: nil,
		},
		"AdmissionGatedBy annotation - trailing space": {
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				Annotations(map[string]string{
					constants.AdmissionGatedByAnnotation: "example.com/gate ",
				}).
				PodSets(*utiltestingapi.MakePodSet("main", 1).Obj()).
				Obj(),
			wantErr: nil,
		},
		"invalid AdmissionGatedBy annotation - not in subdomain/path format": {
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				Annotations(map[string]string{
					constants.AdmissionGatedByAnnotation: "this is an invalid value",
				}).
				PodSets(*utiltestingapi.MakePodSet("main", 1).Obj()).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("metadata", "annotations").Key(constants.AdmissionGatedByAnnotation), "this is an invalid value", ""),
			}.ToAggregate(),
		},
		"invalid AdmissionGatedBy annotation - duplicate gates": {
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				Annotations(map[string]string{
					constants.AdmissionGatedByAnnotation: "duplicates.are/invalid,duplicates.are/invalid",
				}).
				PodSets(*utiltestingapi.MakePodSet("main", 1).Obj()).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("metadata", "annotations").Key(constants.AdmissionGatedByAnnotation), "duplicates.are/invalid,duplicates.are/invalid", ""),
			}.ToAggregate(),
		},
		"invalid AdmissionGatedBy annotation - space in path component": {
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				Annotations(map[string]string{
					constants.AdmissionGatedByAnnotation: "example.com/gate name",
				}).
				PodSets(*utiltestingapi.MakePodSet("main", 1).Obj()).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("metadata", "annotations").Key(constants.AdmissionGatedByAnnotation), "example.com/gate name", ""),
			}.ToAggregate(),
		},
		"invalid AdmissionGatedBy annotation - space in domain component": {
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				Annotations(map[string]string{
					constants.AdmissionGatedByAnnotation: "example .com/gate",
				}).
				PodSets(*utiltestingapi.MakePodSet("main", 1).Obj()).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("metadata", "annotations").Key(constants.AdmissionGatedByAnnotation), "example .com/gate", ""),
			}.ToAggregate(),
		},
		"invalid AdmissionGatedBy annotation - multiple gates with one containing space": {
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				Annotations(map[string]string{
					constants.AdmissionGatedByAnnotation: "valid.com/gate,invalid gate.com/controller",
				}).
				PodSets(*utiltestingapi.MakePodSet("main", 1).Obj()).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("metadata", "annotations").Key(constants.AdmissionGatedByAnnotation), "valid.com/gate,invalid gate.com/controller", ""),
			}.ToAggregate(),
		},
		"partial admission and elastic job cannot be used together": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				PodSets(*utiltestingapi.MakePodSet("main", 10).SetMinimumCount(5).Obj()).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(specPath.Child("podSets"), 1, ""),
			}.ToAggregate(),
		},
		"non-negative subGroupCount is accepted without warning": {
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).PodSets(
				*utiltestingapi.MakePodSet("main", 1).SubGroupCount(new(int32(0))).Obj(),
			).Obj(),
		},
		"negative subGroupCount is accepted with a warning": {
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).PodSets(
				*utiltestingapi.MakePodSet("main", 1).SubGroupCount(new(int32(-1))).Obj(),
			).Obj(),
			wantWarnings: admission.Warnings{
				"spec.podSets[0].topologyRequest.subGroupCount: negative value -1 is deprecated and will be rejected in a future release",
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			gotWarnings, gotErr := (&WorkloadWebhook{}).ValidateCreate(t.Context(), tc.workload)
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.IgnoreFields(field.Error{}, "Detail", "BadValue")); diff != "" {
				t.Errorf("ValidateCreate() error mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantWarnings, gotWarnings); diff != "" {
				t.Errorf("ValidateCreate() warnings mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestValidateWorkloadUpdate(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	testCases := map[string]struct {
		featureGates map[featuregate.Feature]bool

		before, after *kueue.Workload
		wantErr       error
		wantWarnings  admission.Warnings
	}{
		"reclaimable pod count can change up": {
			before: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					*utiltestingapi.MakePodSet("ps1", 3).Obj(),
					*utiltestingapi.MakePodSet("ps2", 3).Obj(),
				).
				ReserveQuotaAt(
					utiltestingapi.MakeAdmission("cluster-queue").
						PodSets(kueue.PodSetAssignment{Name: "ps1"}, kueue.PodSetAssignment{Name: "ps2"}).
						Obj(), now,
				).
				ReclaimablePods(
					kueue.ReclaimablePod{Name: "ps1", Count: 1},
				).
				Obj(),
			after: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					*utiltestingapi.MakePodSet("ps1", 3).Obj(),
					*utiltestingapi.MakePodSet("ps2", 3).Obj(),
				).
				ReserveQuotaAt(
					utiltestingapi.MakeAdmission("cluster-queue").
						PodSets(kueue.PodSetAssignment{Name: "ps1"}, kueue.PodSetAssignment{Name: "ps2"}).
						Obj(), now,
				).
				ReclaimablePods(
					kueue.ReclaimablePod{Name: "ps1", Count: 2},
					kueue.ReclaimablePod{Name: "ps2", Count: 1},
				).
				Obj(),
			wantErr: nil,
		},
		"reclaimable pod count cannot change down": {
			before: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					*utiltestingapi.MakePodSet("ps1", 3).Obj(),
					*utiltestingapi.MakePodSet("ps2", 3).Obj(),
				).
				ReserveQuotaAt(
					utiltestingapi.MakeAdmission("cluster-queue").
						PodSets(kueue.PodSetAssignment{Name: "ps1"}, kueue.PodSetAssignment{Name: "ps2"}).
						Obj(), now,
				).
				ReclaimablePods(
					kueue.ReclaimablePod{Name: "ps1", Count: 2},
					kueue.ReclaimablePod{Name: "ps2", Count: 1},
				).
				Obj(),
			after: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					*utiltestingapi.MakePodSet("ps1", 3).Obj(),
					*utiltestingapi.MakePodSet("ps2", 3).Obj(),
				).
				ReserveQuotaAt(
					utiltestingapi.MakeAdmission("cluster-queue").
						PodSets(kueue.PodSetAssignment{Name: "ps1"}, kueue.PodSetAssignment{Name: "ps2"}).
						Obj(), now,
				).
				ReclaimablePods(
					kueue.ReclaimablePod{Name: "ps1", Count: 1},
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("status", "reclaimablePods").Key("ps1").Child("count"), nil, ""),
				field.Required(field.NewPath("status", "reclaimablePods").Key("ps2"), ""),
			}.ToAggregate(),
		},
		"elastic workload: unchanged reclaimable pod count is tolerated while its podSet scales down below it": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			before: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				PodSets(*utiltestingapi.MakePodSet("ps1", 8).Obj()).
				ReserveQuotaAt(
					utiltestingapi.MakeAdmission("cluster-queue").
						PodSets(kueue.PodSetAssignment{Name: "ps1"}).
						Obj(), now,
				).
				ReclaimablePods(
					kueue.ReclaimablePod{Name: "ps1", Count: 4},
				).
				Obj(),
			after: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				PodSets(*utiltestingapi.MakePodSet("ps1", 3).Obj()).
				ReserveQuotaAt(
					utiltestingapi.MakeAdmission("cluster-queue").
						PodSets(kueue.PodSetAssignment{Name: "ps1"}).
						Obj(), now,
				).
				ReclaimablePods(
					kueue.ReclaimablePod{Name: "ps1", Count: 4},
				).
				Obj(),
			wantErr: nil,
		},
		"elastic workload: changed reclaimable pod count above the podSet count is rejected": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			before: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				PodSets(*utiltestingapi.MakePodSet("ps1", 8).Obj()).
				ReserveQuotaAt(
					utiltestingapi.MakeAdmission("cluster-queue").
						PodSets(kueue.PodSetAssignment{Name: "ps1"}).
						Obj(), now,
				).
				ReclaimablePods(
					kueue.ReclaimablePod{Name: "ps1", Count: 4},
				).
				Obj(),
			after: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				PodSets(*utiltestingapi.MakePodSet("ps1", 3).Obj()).
				ReserveQuotaAt(
					utiltestingapi.MakeAdmission("cluster-queue").
						PodSets(kueue.PodSetAssignment{Name: "ps1"}).
						Obj(), now,
				).
				ReclaimablePods(
					kueue.ReclaimablePod{Name: "ps1", Count: 5},
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("status", "reclaimablePods").Key("ps1").Child("count"), nil, ""),
			}.ToAggregate(),
		},
		"elastic workload: reclaimable pod count can decrease after its podSet scaled down below the admitted count": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			before: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				PodSets(*utiltestingapi.MakePodSet("ps1", 3).Obj()).
				ReserveQuotaAt(
					utiltestingapi.MakeAdmission("cluster-queue").
						PodSets(kueue.PodSetAssignment{Name: "ps1", Count: ptr.To[int32](8)}).
						Obj(), now,
				).
				ReclaimablePods(
					kueue.ReclaimablePod{Name: "ps1", Count: 4},
				).
				Obj(),
			after: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				PodSets(*utiltestingapi.MakePodSet("ps1", 3).Obj()).
				ReserveQuotaAt(
					utiltestingapi.MakeAdmission("cluster-queue").
						PodSets(kueue.PodSetAssignment{Name: "ps1", Count: ptr.To[int32](8)}).
						Obj(), now,
				).
				ReclaimablePods(
					kueue.ReclaimablePod{Name: "ps1", Count: 3},
				).
				Obj(),
			wantErr: nil,
		},
		"elastic workload: reclaimable pod count can decrease when scaled down below the admitted count even if the podSet still exceeds it": {
			// The exact kueue#12958 scenario: admitted at 20, scaled to 10, the
			// reconciler lowers reclaimable from 9 to 0. The podSet (10) still
			// exceeds the reclaimable count, so only the admission count reveals
			// the scale-down.
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			before: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				PodSets(*utiltestingapi.MakePodSet("ps1", 10).Obj()).
				ReserveQuotaAt(
					utiltestingapi.MakeAdmission("cluster-queue").
						PodSets(kueue.PodSetAssignment{Name: "ps1", Count: ptr.To[int32](20)}).
						Obj(), now,
				).
				ReclaimablePods(
					kueue.ReclaimablePod{Name: "ps1", Count: 9},
				).
				Obj(),
			after: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				PodSets(*utiltestingapi.MakePodSet("ps1", 10).Obj()).
				ReserveQuotaAt(
					utiltestingapi.MakeAdmission("cluster-queue").
						PodSets(kueue.PodSetAssignment{Name: "ps1", Count: ptr.To[int32](20)}).
						Obj(), now,
				).
				ReclaimablePods(
					kueue.ReclaimablePod{Name: "ps1", Count: 0},
				).
				Obj(),
			wantErr: nil,
		},
		"elastic workload: reclaimable pod count cannot decrease without a podSet scale-down": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			before: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				PodSets(*utiltestingapi.MakePodSet("ps1", 8).Obj()).
				ReserveQuotaAt(
					utiltestingapi.MakeAdmission("cluster-queue").
						PodSets(kueue.PodSetAssignment{Name: "ps1"}).
						Obj(), now,
				).
				ReclaimablePods(
					kueue.ReclaimablePod{Name: "ps1", Count: 4},
				).
				Obj(),
			after: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				PodSets(*utiltestingapi.MakePodSet("ps1", 8).Obj()).
				ReserveQuotaAt(
					utiltestingapi.MakeAdmission("cluster-queue").
						PodSets(kueue.PodSetAssignment{Name: "ps1"}).
						Obj(), now,
				).
				ReclaimablePods(
					kueue.ReclaimablePod{Name: "ps1", Count: 2},
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("status", "reclaimablePods").Key("ps1").Child("count"), nil, ""),
			}.ToAggregate(),
		},
		"elastic workload: reclaimable pod entry can be removed after its podSet scaled down below the admitted count": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			before: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				PodSets(*utiltestingapi.MakePodSet("ps1", 8).Obj()).
				ReserveQuotaAt(
					utiltestingapi.MakeAdmission("cluster-queue").
						PodSets(kueue.PodSetAssignment{Name: "ps1", Count: ptr.To[int32](8)}).
						Obj(), now,
				).
				ReclaimablePods(
					kueue.ReclaimablePod{Name: "ps1", Count: 4},
				).
				Obj(),
			after: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				PodSets(*utiltestingapi.MakePodSet("ps1", 1).Obj()).
				ReserveQuotaAt(
					utiltestingapi.MakeAdmission("cluster-queue").
						PodSets(kueue.PodSetAssignment{Name: "ps1", Count: ptr.To[int32](8)}).
						Obj(), now,
				).
				Obj(),
			wantErr: nil,
		},
		"elastic workload: reclaimable pod entry cannot be removed without a podSet scale-down": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			before: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				PodSets(*utiltestingapi.MakePodSet("ps1", 8).Obj()).
				ReserveQuotaAt(
					utiltestingapi.MakeAdmission("cluster-queue").
						PodSets(kueue.PodSetAssignment{Name: "ps1"}).
						Obj(), now,
				).
				ReclaimablePods(
					kueue.ReclaimablePod{Name: "ps1", Count: 4},
				).
				Obj(),
			after: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				PodSets(*utiltestingapi.MakePodSet("ps1", 8).Obj()).
				ReserveQuotaAt(
					utiltestingapi.MakeAdmission("cluster-queue").
						PodSets(kueue.PodSetAssignment{Name: "ps1"}).
						Obj(), now,
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Required(field.NewPath("status", "reclaimablePods").Key("ps1"), ""),
			}.ToAggregate(),
		},
		"non-elastic workload: stale reclaimable pod count is still rejected when its podSet scales down": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			before: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(*utiltestingapi.MakePodSet("ps1", 8).Obj()).
				ReserveQuotaAt(
					utiltestingapi.MakeAdmission("cluster-queue").
						PodSets(kueue.PodSetAssignment{Name: "ps1"}).
						Obj(), now,
				).
				ReclaimablePods(
					kueue.ReclaimablePod{Name: "ps1", Count: 4},
				).
				Obj(),
			after: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(*utiltestingapi.MakePodSet("ps1", 3).Obj()).
				ReserveQuotaAt(
					utiltestingapi.MakeAdmission("cluster-queue").
						PodSets(kueue.PodSetAssignment{Name: "ps1"}).
						Obj(), now,
				).
				ReclaimablePods(
					kueue.ReclaimablePod{Name: "ps1", Count: 4},
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("status", "reclaimablePods").Key("ps1").Child("count"), nil, ""),
			}.ToAggregate(),
		},
		"reclaimable pod count can go to 0 if the job is suspended": {
			before: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					*utiltestingapi.MakePodSet("ps1", 3).Obj(),
					*utiltestingapi.MakePodSet("ps2", 3).Obj(),
				).
				ReserveQuotaAt(
					utiltestingapi.MakeAdmission("cluster-queue").
						PodSets(kueue.PodSetAssignment{Name: "ps1"}, kueue.PodSetAssignment{Name: "ps2"}).
						Obj(), now,
				).
				ReclaimablePods(
					kueue.ReclaimablePod{Name: "ps1", Count: 2},
					kueue.ReclaimablePod{Name: "ps2", Count: 1},
				).
				Obj(),
			after: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					*utiltestingapi.MakePodSet("ps1", 3).Obj(),
					*utiltestingapi.MakePodSet("ps2", 3).Obj(),
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
		"podSetUpdates should be immutable when state is ready": {
			before: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).PodSets(
				*utiltestingapi.MakePodSet("first", 1).Obj(),
				*utiltestingapi.MakePodSet("second", 1).Obj(),
			).AdmissionChecks(kueue.AdmissionCheckState{
				PodSetUpdates: []kueue.PodSetUpdate{{Name: "first", Labels: map[string]string{"foo": "bar"}}, {Name: "second"}},
				State:         kueue.CheckStateReady,
			}).Obj(),
			after: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).PodSets(
				*utiltestingapi.MakePodSet("first", 1).Obj(),
				*utiltestingapi.MakePodSet("second", 1).Obj(),
			).AdmissionChecks(kueue.AdmissionCheckState{
				PodSetUpdates: []kueue.PodSetUpdate{{Name: "first", Labels: map[string]string{"foo": "baz"}}, {Name: "second"}},
				State:         kueue.CheckStateReady,
			}).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("status").Child("admissionChecks").Index(0).Child("podSetUpdates"), nil, ""),
			}.ToAggregate(),
		},
		"should change other fields of admissionchecks when podSetUpdates is immutable": {
			before: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).PodSets(
				*utiltestingapi.MakePodSet("first", 1).Obj(),
				*utiltestingapi.MakePodSet("second", 1).Obj(),
			).AdmissionChecks(kueue.AdmissionCheckState{
				Name:          "ac1",
				Message:       "old",
				PodSetUpdates: []kueue.PodSetUpdate{{Name: "first", Labels: map[string]string{"foo": "bar"}}, {Name: "second"}},
				State:         kueue.CheckStateReady,
			}).Obj(),
			after: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).PodSets(
				*utiltestingapi.MakePodSet("first", 1).Obj(),
				*utiltestingapi.MakePodSet("second", 1).Obj(),
			).AdmissionChecks(kueue.AdmissionCheckState{
				Name:               "ac1",
				Message:            "new",
				LastTransitionTime: metav1.NewTime(time.Now()),
				PodSetUpdates:      []kueue.PodSetUpdate{{Name: "first", Labels: map[string]string{"foo": "bar"}}, {Name: "second"}},
				State:              kueue.CheckStateReady,
			}).Obj(),
		},
		"TopologyAssignment can be mutated": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: true},
			before: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					*utiltestingapi.MakePodSet("ps1", 3).Obj(),
				).
				ReserveQuotaAt(
					utiltestingapi.MakeAdmission("cluster-queue").
						PodSets(kueue.PodSetAssignment{
							Name: "ps1",
							TopologyAssignment: tas.V1Beta2From(&tas.TopologyAssignment{
								Levels: []string{"level"},
								Domains: []tas.TopologyDomainAssignment{
									{
										Values: []string{"abc"},
										Count:  2,
									},
								},
							}),
						}).
						Obj(), now,
				).
				Obj(),
			after: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					*utiltestingapi.MakePodSet("ps1", 3).Obj(),
				).
				ReserveQuotaAt(
					utiltestingapi.MakeAdmission("cluster-queue").
						PodSets(kueue.PodSetAssignment{
							Name: "ps1",
							TopologyAssignment: tas.V1Beta2From(&tas.TopologyAssignment{
								Levels: []string{"level"},
								Domains: []tas.TopologyDomainAssignment{
									{
										Values: []string{"abc"},
										Count:  3,
									},
								},
							}),
						}).
						Obj(), now,
				).
				Obj(),
			wantErr: nil,
		},
		"PodSets cannot be removed from admission": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: true},
			before: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					*utiltestingapi.MakePodSet("ps1", 3).Obj(),
					*utiltestingapi.MakePodSet("ps2", 3).Obj(),
				).
				ReserveQuotaAt(
					utiltestingapi.MakeAdmission("cluster-queue").
						PodSets(kueue.PodSetAssignment{
							Name: "ps1",
						}).
						PodSets(kueue.PodSetAssignment{
							Name: "ps2",
						}).
						Obj(), now,
				).
				Obj(),
			after: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					*utiltestingapi.MakePodSet("ps1", 3).Obj(),
					*utiltestingapi.MakePodSet("ps2", 3).Obj(),
				).
				ReserveQuotaAt(
					utiltestingapi.MakeAdmission("cluster-queue").
						PodSets(kueue.PodSetAssignment{
							Name: "ps1",
						}).
						Obj(), now,
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("status", "admission"), nil, ""),
			}.ToAggregate(),
		},
		"PodSets cannot be added to admission": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: true},
			before: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					*utiltestingapi.MakePodSet("ps1", 3).Obj(),
					*utiltestingapi.MakePodSet("ps2", 3).Obj(),
				).
				ReserveQuotaAt(
					utiltestingapi.MakeAdmission("cluster-queue").
						PodSets(kueue.PodSetAssignment{
							Name: "ps1",
						}).
						Obj(), now,
				).
				Obj(),
			after: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					*utiltestingapi.MakePodSet("ps1", 3).Obj(),
					*utiltestingapi.MakePodSet("ps2", 3).Obj(),
				).
				ReserveQuotaAt(
					utiltestingapi.MakeAdmission("cluster-queue").
						PodSets(kueue.PodSetAssignment{
							Name: "ps1",
						}).
						PodSets(kueue.PodSetAssignment{
							Name: "ps2",
						}).
						Obj(), now,
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("status", "admission"), nil, ""),
			}.ToAggregate(),
		},

		"workload.podSets[].count is immutable": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: false},
			before: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					*utiltestingapi.MakePodSet("ps1", 3).Obj(),
					*utiltestingapi.MakePodSet("ps2", 4).Obj()).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cluster-queue").PodSets(
					kueue.PodSetAssignment{Name: "ps1"},
					kueue.PodSetAssignment{Name: "ps2"}).Obj(), now).Obj(),
			after: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					*utiltestingapi.MakePodSet("ps1", 3).Obj(),
					*utiltestingapi.MakePodSet("ps2", 3).Obj()).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cluster-queue").PodSets(
					kueue.PodSetAssignment{Name: "ps1"},
					kueue.PodSetAssignment{Name: "ps2"}).Obj(), now).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec", "podSets", "1"), nil, ""),
			}.ToAggregate(),
		},
		"workload.podSets[].count is mutable with ElasticJobs feature gate": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			before: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					*utiltestingapi.MakePodSet("ps1", 3).Obj(),
					*utiltestingapi.MakePodSet("ps2", 4).Obj()).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cluster-queue").PodSets(
					kueue.PodSetAssignment{Name: "ps1"},
					kueue.PodSetAssignment{Name: "ps2"}).Obj(), now).Obj(),
			after: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					*utiltestingapi.MakePodSet("ps1", 3).Obj(),
					*utiltestingapi.MakePodSet("ps2", 3).Obj()).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cluster-queue").PodSets(
					kueue.PodSetAssignment{Name: "ps1"},
					kueue.PodSetAssignment{Name: "ps2"}).Obj(), now).Obj(),
		},
		"ClusterName doesn't have to be one of the nominatedClusterNames with ElasticJobs feature gate": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			before: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					*utiltestingapi.MakePodSet("ps1", 3).Obj(),
					*utiltestingapi.MakePodSet("ps2", 4).Obj()).
				Obj(),
			after: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					*utiltestingapi.MakePodSet("ps1", 3).Obj(),
					*utiltestingapi.MakePodSet("ps2", 4).Obj()).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cluster-queue").PodSets(
					kueue.PodSetAssignment{Name: "ps1"},
					kueue.PodSetAssignment{Name: "ps2"}).Obj(), now).
				ClusterName("worker1").
				Annotation(workloadslicing.WorkloadSliceReplacementFor, string(workload.NewReference(testWorkloadNamespace, testWorkloadName))).
				Obj(),
		},
		"reject adding AdmissionGatedBy annotation after workload creation": {
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
			before: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(*utiltestingapi.MakePodSet("main", 1).Obj()).
				Obj(),
			after: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				Annotations(map[string]string{
					constants.AdmissionGatedByAnnotation: "example.com/controller",
				}).
				PodSets(*utiltestingapi.MakePodSet("main", 1).Obj()).
				Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(field.NewPath("metadata", "annotations").Key(constants.AdmissionGatedByAnnotation), ""),
			}.ToAggregate(),
		},
		"allow removing AdmissionGatedBy annotation with single gate": {
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
			before: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				Annotations(map[string]string{
					constants.AdmissionGatedByAnnotation: "example.com/controller1",
				}).
				PodSets(*utiltestingapi.MakePodSet("main", 1).Obj()).
				Obj(),
			after: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(*utiltestingapi.MakePodSet("main", 1).Obj()).
				Obj(),
			wantErr: nil,
		},
		"allow removing AdmissionGatedBy annotation with multiple gates": {
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
			before: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				Annotations(map[string]string{
					constants.AdmissionGatedByAnnotation: "example.com/controller1,example.com/controller2",
				}).
				PodSets(*utiltestingapi.MakePodSet("main", 1).Obj()).
				Obj(),
			after: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(*utiltestingapi.MakePodSet("main", 1).Obj()).
				Obj(),
			wantErr: nil,
		},
		"allow removing one gate from AdmissionGatedBy annotation": {
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
			before: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				Annotations(map[string]string{
					constants.AdmissionGatedByAnnotation: "example.com/controller1,example.com/controller2",
				}).
				PodSets(*utiltestingapi.MakePodSet("main", 1).Obj()).
				Obj(),
			after: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				Annotations(map[string]string{
					constants.AdmissionGatedByAnnotation: "example.com/controller2",
				}).
				PodSets(*utiltestingapi.MakePodSet("main", 1).Obj()).
				Obj(),
			wantErr: nil,
		},
		"reject injecting new gate in AdmissionGatedBy annotation": {
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
			before: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				Annotations(map[string]string{
					constants.AdmissionGatedByAnnotation: "example.com/controller1,example.com/controller2",
				}).
				PodSets(*utiltestingapi.MakePodSet("main", 1).Obj()).
				Obj(),
			after: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				Annotations(map[string]string{
					constants.AdmissionGatedByAnnotation: "example.com/controller3",
				}).
				PodSets(*utiltestingapi.MakePodSet("main", 1).Obj()).
				Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(field.NewPath("metadata", "annotations").Key(constants.AdmissionGatedByAnnotation), ""),
			}.ToAggregate(),
		},
		"allow reordering gates in AdmissionGatedBy annotation": {
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
			before: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				Annotations(map[string]string{
					constants.AdmissionGatedByAnnotation: "example.com/controller1,example.com/controller2",
				}).
				PodSets(*utiltestingapi.MakePodSet("main", 1).Obj()).
				Obj(),
			after: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				Annotations(map[string]string{
					constants.AdmissionGatedByAnnotation: "example.com/controller2,example.com/controller1",
				}).
				PodSets(*utiltestingapi.MakePodSet("main", 1).Obj()).
				Obj(),
			wantErr: nil,
		},
		"negative subGroupCount is accepted with a warning": {
			before: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).PodSets(
				*utiltestingapi.MakePodSet("main", 1).Obj(),
			).Obj(),
			after: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).PodSets(
				*utiltestingapi.MakePodSet("main", 1).SubGroupCount(new(int32(-1))).Obj(),
			).Obj(),
			wantWarnings: admission.Warnings{
				"spec.podSets[0].topologyRequest.subGroupCount: negative value -1 is deprecated and will be rejected in a future release",
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			gotWarnings, gotErr := (&WorkloadWebhook{}).ValidateUpdate(t.Context(), tc.before, tc.after)
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.IgnoreFields(field.Error{}, "Detail", "BadValue")); diff != "" {
				t.Errorf("ValidateUpdate() error mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantWarnings, gotWarnings); diff != "" {
				t.Errorf("ValidateUpdate() warnings mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
