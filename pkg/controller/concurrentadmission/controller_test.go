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

package concurrentadmission

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	testingclock "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload/concurrentadmission"
)

var (
	workloadCmpOpts = cmp.Options{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(
			kueue.Workload{}, "TypeMeta", "ObjectMeta.ResourceVersion", "ObjectMeta.UID", "Status.AccumulatedPastExecutionTimeSeconds", "Status.SchedulingStats",
		),
		cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
		cmpopts.SortSlices(func(a, b kueue.Workload) bool { return a.Name < b.Name }),
		cmpopts.SortSlices(func(a, b metav1.Condition) bool { return a.Type < b.Type }),
	}
)

func TestReconcile(t *testing.T) {
	defaultCQ := utiltestingapi.MakeClusterQueue("cq").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("on-demand").Obj(),
			*utiltestingapi.MakeFlavorQuotas("spot").Obj(),
		).
		ConcurrentAdmissionPolicy(kueue.ConcurrentAdmissionTryPreferredFlavors).
		Obj()
	defaultLQ := utiltestingapi.MakeLocalQueue("lq", "default").ClusterQueue("cq").Obj()

	migrationCQ := utiltestingapi.MakeClusterQueue("cq-migration").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("reservation").Obj(),
			*utiltestingapi.MakeFlavorQuotas("on-demand").Obj(),
			*utiltestingapi.MakeFlavorQuotas("spot").Obj(),
		).
		MinPreferredFlavorName("reservation").
		Obj()
	migrationLQ := utiltestingapi.MakeLocalQueue("lq-migration", "default").ClusterQueue("cq-migration").Obj()

	migrationCQNoConstraint := utiltestingapi.MakeClusterQueue("cq-migration-no-constraint").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("reservation").Obj(),
			*utiltestingapi.MakeFlavorQuotas("on-demand").Obj(),
			*utiltestingapi.MakeFlavorQuotas("spot").Obj(),
		).
		ConcurrentAdmissionPolicy(kueue.ConcurrentAdmissionTryPreferredFlavors).
		Obj()
	migrationLQNoConstraint := utiltestingapi.MakeLocalQueue("lq-migration-no-constraint", "default").ClusterQueue("cq-migration-no-constraint").Obj()

	testCases := map[string]struct {
		parentWorkload       *kueue.Workload
		variantWorkloads     []kueue.Workload
		wantParentWorkload   *kueue.Workload
		wantVariantWorkloads []kueue.Workload
		req                  reconcile.Request
		wantResult           reconcile.Result
		wantErr              bool
	}{
		"workload not found": {
			req: reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: "default",
					Name:      "non-existing",
				},
			},
			wantResult: reconcile.Result{},
			wantErr:    false,
		},
		"workload is not parent or variant": {
			parentWorkload:     utiltestingapi.MakeWorkload("wl", "default").Obj(),
			wantParentWorkload: utiltestingapi.MakeWorkload("wl", "default").Obj(),
			wantResult:         reconcile.Result{},
			wantErr:            false,
		},
		"parent workload without variants creates them": {
			parentWorkload: utiltestingapi.MakeWorkload("parent-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Obj(),
			wantParentWorkload: utiltestingapi.MakeWorkload("parent-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Obj(),
			wantVariantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("parent-variant-spot-545ad", "default").
					Queue("lq").
					AllowedFlavors("spot").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", ""). // UID is ignored in cmp
					Obj(),
				*utiltestingapi.MakeWorkload("parent-variant-on-demand-39893", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Obj(),
			},
			wantResult: reconcile.Result{},
			wantErr:    false,
		},
		"parent workload with missing variants; creates missing": {
			parentWorkload: utiltestingapi.MakeWorkload("parent-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Obj(),
			variantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("parent-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", ""). // UID is ignored in cmp
					Obj(),
			},
			wantParentWorkload: utiltestingapi.MakeWorkload("parent-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Obj(),
			wantVariantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("parent-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", ""). // UID is ignored in cmp
					Obj(),
				*utiltestingapi.MakeWorkload("parent-variant-on-demand-39893", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Obj(),
			},
			wantResult: reconcile.Result{},
			wantErr:    false,
		},
		"admitted variant syncs admission to parent": {
			parentWorkload: utiltestingapi.MakeWorkload("parent-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Obj(),
			variantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("parent-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					Request(corev1.ResourceCPU, "1").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					SimpleReserveQuota("cq", "spot", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Obj(),
				*utiltestingapi.MakeWorkload("parent-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Obj(),
			},
			wantParentWorkload: utiltestingapi.MakeWorkload("parent-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Admission(utiltestingapi.MakeAdmission("cq", "main").
					PodSets(kueue.PodSetAssignment{
						Name: "main",
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: "spot",
						},
						Count:         ptr.To[int32](1),
						ResourceUsage: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
					}).Obj()).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionTrue,
					Reason:  "Admitted",
					Message: "The variant parent-variant-spot is admitted",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionTrue,
					Reason:  "QuotaReserved",
					Message: "Quota reserved in ClusterQueue cq",
				}).
				Obj(),
			wantVariantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("parent-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					Request(corev1.ResourceCPU, "1").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					SimpleReserveQuota("cq", "spot", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Obj(),
				*utiltestingapi.MakeWorkload("parent-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Obj(),
			},
			wantResult: reconcile.Result{},
			wantErr:    false,
		},
		"no variant admitted; parent admitted; evict parent": {
			parentWorkload: utiltestingapi.MakeWorkload("parent-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Request(corev1.ResourceCPU, "1").
				SimpleReserveQuota("cq", "spot", metav1.Now().Time).
				AdmittedAt(true, metav1.Now().Time).
				Obj(),
			variantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("parent-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltestingapi.MakeWorkload("parent-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					Request(corev1.ResourceCPU, "1").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Obj(),
			},
			wantParentWorkload: utiltestingapi.MakeWorkload("parent-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Request(corev1.ResourceCPU, "1").
				SimpleReserveQuota("cq", "spot", metav1.Now().Time).
				AdmittedAt(true, metav1.Now().Time).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadEvicted,
					Status:  metav1.ConditionTrue,
					Reason:  "ConcurrentAdmission",
					Message: "No variant is running",
				}).
				Obj(),
			wantVariantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("parent-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltestingapi.MakeWorkload("parent-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					Request(corev1.ResourceCPU, "1").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Obj(),
			},
			wantResult: reconcile.Result{},
			wantErr:    false,
		},
		"admitted variant evicted; parent has quota; evict parent and wait": {
			parentWorkload: utiltestingapi.MakeWorkload("parent-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Request(corev1.ResourceCPU, "1").
				SimpleReserveQuota("cq", "spot", metav1.Now().Time).
				AdmittedAt(true, metav1.Now().Time).
				Obj(),
			variantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("parent-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Request(corev1.ResourceCPU, "1").
					SimpleReserveQuota("cq", "spot", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadEvictedByPreemption,
						Message: "Evicted by preemption",
					}).
					Active(true).
					Obj(),
				*utiltestingapi.MakeWorkload("parent-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					Request(corev1.ResourceCPU, "1").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Active(false).
					Obj(),
			},
			wantParentWorkload: utiltestingapi.MakeWorkload("parent-12345", "default").
				Queue("lq").
				Request(corev1.ResourceCPU, "1").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				SimpleReserveQuota("cq", "spot", metav1.Now().Time).
				AdmittedAt(true, metav1.Now().Time).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadEvicted,
					Status:  metav1.ConditionTrue,
					Reason:  "VariantEvicted",
					Message: "Admitted variant was evicted",
				}).
				Obj(),
			wantVariantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("parent-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					Request(corev1.ResourceCPU, "1").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					SimpleReserveQuota("cq", "spot", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadEvictedByPreemption,
						Message: "Evicted by preemption",
					}).
					Active(true).
					Obj(),
				*utiltestingapi.MakeWorkload("parent-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					Request(corev1.ResourceCPU, "1").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Active(false).
					Obj(),
			},
			wantResult: reconcile.Result{},
			wantErr:    false,
		},
		"admitted variant evicted; parent has no quota reservation; clear variant reservation": {
			parentWorkload: utiltestingapi.MakeWorkload("parent-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Request(corev1.ResourceCPU, "1").
				Obj(),
			variantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("parent-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Request(corev1.ResourceCPU, "1").
					SimpleReserveQuota("cq", "spot", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadEvictedByPreemption,
						Message: "Evicted by preemption",
					}).
					Active(true).
					Obj(),
				*utiltestingapi.MakeWorkload("parent-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					Request(corev1.ResourceCPU, "1").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Active(false).
					Obj(),
			},
			wantParentWorkload: utiltestingapi.MakeWorkload("parent-12345", "default").
				Queue("lq").
				Request(corev1.ResourceCPU, "1").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Obj(),
			wantVariantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("parent-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					Request(corev1.ResourceCPU, "1").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					SimpleReserveQuota("cq", "spot", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadQuotaReserved,
						Status:  metav1.ConditionFalse,
						Reason:  "Pending",
						Message: "Evicted by preemption",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadAdmitted,
						Status:  metav1.ConditionFalse,
						Reason:  "NoReservation",
						Message: "The workload has no reservation",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadEvictedByPreemption,
						Message: "Evicted by preemption",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadRequeued,
						Status:  metav1.ConditionTrue,
						Reason:  "Preempted",
						Message: "Evicted by preemption",
					}).
					Active(true).
					Obj(),
				*utiltestingapi.MakeWorkload("parent-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					Request(corev1.ResourceCPU, "1").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Active(true).
					Obj(),
			},
			wantResult: reconcile.Result{},
			wantErr:    false,
		},
		"with minTargetFlavor=reservation, variant admitted on spot, deactivate on-demand": {
			parentWorkload: utiltestingapi.MakeWorkload("parent-12345", "default").
				Queue("lq-migration").
				Request(corev1.ResourceCPU, "1").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Obj(),
			variantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("parent-variant-reservation", "default").
					Queue("lq-migration").
					AllowedFlavors("reservation").
					Request(corev1.ResourceCPU, "1").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Obj(),
				*utiltestingapi.MakeWorkload("parent-variant-on-demand", "default").
					Queue("lq-migration").
					AllowedFlavors("on-demand").
					Request(corev1.ResourceCPU, "1").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Obj(),
				*utiltestingapi.MakeWorkload("parent-variant-spot", "default").
					Queue("lq-migration").
					AllowedFlavors("spot").
					Request(corev1.ResourceCPU, "1").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					SimpleReserveQuota("cq-migration", "spot", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Obj(),
			},
			wantParentWorkload: utiltestingapi.MakeWorkload("parent-12345", "default").
				Queue("lq-migration").
				Request(corev1.ResourceCPU, "1").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Admission(utiltestingapi.MakeAdmission("cq-migration", "main").
					PodSets(kueue.PodSetAssignment{
						Name: "main",
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: "spot",
						},
						Count:         ptr.To[int32](1),
						ResourceUsage: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
					}).Obj()).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionTrue,
					Reason:  "Admitted",
					Message: "The variant parent-variant-spot is admitted",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionTrue,
					Reason:  "QuotaReserved",
					Message: "Quota reserved in ClusterQueue cq-migration",
				}).
				Obj(),
			wantVariantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("parent-variant-reservation", "default").
					Queue("lq-migration").
					Request(corev1.ResourceCPU, "1").
					AllowedFlavors("reservation").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Obj(),
				*utiltestingapi.MakeWorkload("parent-variant-on-demand", "default").
					Queue("lq-migration").
					Request(corev1.ResourceCPU, "1").
					AllowedFlavors("on-demand").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Active(false).
					Obj(),
				*utiltestingapi.MakeWorkload("parent-variant-spot", "default").
					Queue("lq-migration").
					Request(corev1.ResourceCPU, "1").
					AllowedFlavors("spot").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					SimpleReserveQuota("cq-migration", "spot", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Obj(),
			},
		},
		"with minTargetFlavor=reservation, variant admitted on on-demand, deactivate spot": {
			parentWorkload: utiltestingapi.MakeWorkload("parent-12345", "default").
				Queue("lq-migration").
				Request(corev1.ResourceCPU, "1").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Obj(),
			variantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("parent-variant-reservation", "default").
					Queue("lq-migration").
					Request(corev1.ResourceCPU, "1").
					AllowedFlavors("reservation").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Obj(),
				*utiltestingapi.MakeWorkload("parent-variant-on-demand", "default").
					Queue("lq-migration").
					Request(corev1.ResourceCPU, "1").
					AllowedFlavors("on-demand").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					SimpleReserveQuota("cq-migration", "on-demand", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Obj(),
				*utiltestingapi.MakeWorkload("parent-variant-spot", "default").
					Queue("lq-migration").
					Request(corev1.ResourceCPU, "1").
					AllowedFlavors("spot").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Obj(),
			},
			wantParentWorkload: utiltestingapi.MakeWorkload("parent-12345", "default").
				Queue("lq-migration").
				Request(corev1.ResourceCPU, "1").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Admission(utiltestingapi.MakeAdmission("cq-migration", "main").
					PodSets(kueue.PodSetAssignment{
						Name: "main",
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: "on-demand",
						},
						Count:         ptr.To[int32](1),
						ResourceUsage: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
					}).Obj()).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionTrue,
					Reason:  "Admitted",
					Message: "The variant parent-variant-on-demand is admitted",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionTrue,
					Reason:  "QuotaReserved",
					Message: "Quota reserved in ClusterQueue cq-migration",
				}).
				Obj(),
			wantVariantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("parent-variant-reservation", "default").
					Queue("lq-migration").
					Request(corev1.ResourceCPU, "1").
					AllowedFlavors("reservation").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Obj(),
				*utiltestingapi.MakeWorkload("parent-variant-on-demand", "default").
					Queue("lq-migration").
					Request(corev1.ResourceCPU, "1").
					AllowedFlavors("on-demand").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					SimpleReserveQuota("cq-migration", "on-demand", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Obj(),
				*utiltestingapi.MakeWorkload("parent-variant-spot", "default").
					Queue("lq-migration").
					Request(corev1.ResourceCPU, "1").
					AllowedFlavors("spot").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Active(false).
					Obj(),
			},
		},
		"with minTargetFlavor=reservation, variant admitted on reservation, deactivate spot and on-demand": {
			parentWorkload: utiltestingapi.MakeWorkload("parent-12345", "default").
				Queue("lq-migration").
				Request(corev1.ResourceCPU, "1").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Obj(),
			variantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("parent-variant-reservation", "default").
					Queue("lq-migration").
					Request(corev1.ResourceCPU, "1").
					AllowedFlavors("reservation").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					SimpleReserveQuota("cq-migration", "reservation", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Obj(),
				*utiltestingapi.MakeWorkload("parent-variant-on-demand", "default").
					Queue("lq-migration").
					Request(corev1.ResourceCPU, "1").
					AllowedFlavors("on-demand").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Obj(),
				*utiltestingapi.MakeWorkload("parent-variant-spot", "default").
					Queue("lq-migration").
					Request(corev1.ResourceCPU, "1").
					AllowedFlavors("spot").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Obj(),
			},
			wantParentWorkload: utiltestingapi.MakeWorkload("parent-12345", "default").
				Queue("lq-migration").
				Request(corev1.ResourceCPU, "1").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Admission(utiltestingapi.MakeAdmission("cq-migration", "main").
					PodSets(kueue.PodSetAssignment{
						Name: "main",
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: "reservation",
						},
						Count:         ptr.To[int32](1),
						ResourceUsage: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
					}).Obj()).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionTrue,
					Reason:  "Admitted",
					Message: "The variant parent-variant-reservation is admitted",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionTrue,
					Reason:  "QuotaReserved",
					Message: "Quota reserved in ClusterQueue cq-migration",
				}).
				Obj(),
			wantVariantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("parent-variant-reservation", "default").
					Queue("lq-migration").
					Request(corev1.ResourceCPU, "1").
					AllowedFlavors("reservation").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					SimpleReserveQuota("cq-migration", "reservation", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Obj(),
				*utiltestingapi.MakeWorkload("parent-variant-on-demand", "default").
					Queue("lq-migration").
					Request(corev1.ResourceCPU, "1").
					AllowedFlavors("on-demand").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Active(false).
					Obj(),
				*utiltestingapi.MakeWorkload("parent-variant-spot", "default").
					Queue("lq-migration").
					Request(corev1.ResourceCPU, "1").
					AllowedFlavors("spot").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Active(false).
					Obj(),
			},
		},
		"without minTargetFlavor, variant admitted on spot, nothing deactivated": {
			parentWorkload: utiltestingapi.MakeWorkload("parent-12345", "default").
				Queue("lq-migration-no-constraint").
				Request(corev1.ResourceCPU, "1").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Obj(),
			variantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("parent-variant-reservation", "default").
					Queue("lq-migration-no-constraint").
					Request(corev1.ResourceCPU, "1").
					AllowedFlavors("reservation").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Obj(),
				*utiltestingapi.MakeWorkload("parent-variant-on-demand", "default").
					Queue("lq-migration-no-constraint").
					Request(corev1.ResourceCPU, "1").
					AllowedFlavors("on-demand").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Obj(),
				*utiltestingapi.MakeWorkload("parent-variant-spot", "default").
					Queue("lq-migration-no-constraint").
					Request(corev1.ResourceCPU, "1").
					AllowedFlavors("spot").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					SimpleReserveQuota("cq-migration-no-constraint", "spot", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Obj(),
			},
			wantParentWorkload: utiltestingapi.MakeWorkload("parent-12345", "default").
				Queue("lq-migration-no-constraint").
				Request(corev1.ResourceCPU, "1").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Admission(utiltestingapi.MakeAdmission("cq-migration-no-constraint", "main").
					PodSets(kueue.PodSetAssignment{
						Name: "main",
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: "spot",
						},
						Count:         ptr.To[int32](1),
						ResourceUsage: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
					}).Obj()).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionTrue,
					Reason:  "Admitted",
					Message: "The variant parent-variant-spot is admitted",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionTrue,
					Reason:  "QuotaReserved",
					Message: "Quota reserved in ClusterQueue cq-migration-no-constraint",
				}).
				Obj(),
			wantVariantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("parent-variant-reservation", "default").
					Queue("lq-migration-no-constraint").
					Request(corev1.ResourceCPU, "1").
					AllowedFlavors("reservation").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Obj(),
				*utiltestingapi.MakeWorkload("parent-variant-on-demand", "default").
					Queue("lq-migration-no-constraint").
					Request(corev1.ResourceCPU, "1").
					AllowedFlavors("on-demand").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Obj(),
				*utiltestingapi.MakeWorkload("parent-variant-spot", "default").
					Queue("lq-migration-no-constraint").
					Request(corev1.ResourceCPU, "1").
					AllowedFlavors("spot").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					SimpleReserveQuota("cq-migration-no-constraint", "spot", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Obj(),
			},
		},
		"without minTargetFlavor, variant admitted on on-demand, deactivate spot": {
			parentWorkload: utiltestingapi.MakeWorkload("parent-12345", "default").
				Queue("lq-migration-no-constraint").
				Request(corev1.ResourceCPU, "1").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Obj(),
			variantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("parent-variant-reservation", "default").
					Queue("lq-migration-no-constraint").
					Request(corev1.ResourceCPU, "1").
					AllowedFlavors("reservation").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Obj(),
				*utiltestingapi.MakeWorkload("parent-variant-on-demand", "default").
					Queue("lq-migration-no-constraint").
					Request(corev1.ResourceCPU, "1").
					AllowedFlavors("on-demand").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					SimpleReserveQuota("cq-migration-no-constraint", "on-demand", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Obj(),
				*utiltestingapi.MakeWorkload("parent-variant-spot", "default").
					Queue("lq-migration-no-constraint").
					Request(corev1.ResourceCPU, "1").
					AllowedFlavors("spot").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Obj(),
			},
			wantParentWorkload: utiltestingapi.MakeWorkload("parent-12345", "default").
				Queue("lq-migration-no-constraint").
				Request(corev1.ResourceCPU, "1").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Admission(utiltestingapi.MakeAdmission("cq-migration-no-constraint", "main").
					PodSets(kueue.PodSetAssignment{
						Name: "main",
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: "on-demand",
						},
						Count:         ptr.To[int32](1),
						ResourceUsage: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
					}).Obj()).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionTrue,
					Reason:  "Admitted",
					Message: "The variant parent-variant-on-demand is admitted",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionTrue,
					Reason:  "QuotaReserved",
					Message: "Quota reserved in ClusterQueue cq-migration-no-constraint",
				}).
				Obj(),
			wantVariantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("parent-variant-reservation", "default").
					Queue("lq-migration-no-constraint").
					Request(corev1.ResourceCPU, "1").
					AllowedFlavors("reservation").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Obj(),
				*utiltestingapi.MakeWorkload("parent-variant-on-demand", "default").
					Queue("lq-migration-no-constraint").
					Request(corev1.ResourceCPU, "1").
					AllowedFlavors("on-demand").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					SimpleReserveQuota("cq-migration-no-constraint", "on-demand", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Obj(),
				*utiltestingapi.MakeWorkload("parent-variant-spot", "default").
					Queue("lq-migration-no-constraint").
					Request(corev1.ResourceCPU, "1").
					AllowedFlavors("spot").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Active(false).
					Obj(),
			},
		},
		"without minTargetFlavor, variant admitted on reservation, deactivate spot and on-demand": {
			parentWorkload: utiltestingapi.MakeWorkload("parent-12345", "default").
				Queue("lq-migration-no-constraint").
				Request(corev1.ResourceCPU, "1").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Obj(),
			variantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("parent-variant-reservation", "default").
					Queue("lq-migration-no-constraint").
					Request(corev1.ResourceCPU, "1").
					AllowedFlavors("reservation").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					SimpleReserveQuota("cq-migration-no-constraint", "reservation", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Obj(),
				*utiltestingapi.MakeWorkload("parent-variant-on-demand", "default").
					Queue("lq-migration-no-constraint").
					Request(corev1.ResourceCPU, "1").
					AllowedFlavors("on-demand").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Obj(),
				*utiltestingapi.MakeWorkload("parent-variant-spot", "default").
					Queue("lq-migration-no-constraint").
					Request(corev1.ResourceCPU, "1").
					AllowedFlavors("spot").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Obj(),
			},
			wantParentWorkload: utiltestingapi.MakeWorkload("parent-12345", "default").
				Queue("lq-migration-no-constraint").
				Request(corev1.ResourceCPU, "1").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Admission(utiltestingapi.MakeAdmission("cq-migration-no-constraint", "main").
					PodSets(kueue.PodSetAssignment{
						Name: "main",
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: "reservation",
						},
						Count:         ptr.To[int32](1),
						ResourceUsage: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
					}).Obj()).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionTrue,
					Reason:  "Admitted",
					Message: "The variant parent-variant-reservation is admitted",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionTrue,
					Reason:  "QuotaReserved",
					Message: "Quota reserved in ClusterQueue cq-migration-no-constraint",
				}).
				Obj(),
			wantVariantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("parent-variant-reservation", "default").
					Queue("lq-migration-no-constraint").
					Request(corev1.ResourceCPU, "1").
					AllowedFlavors("reservation").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					SimpleReserveQuota("cq-migration-no-constraint", "reservation", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Obj(),
				*utiltestingapi.MakeWorkload("parent-variant-on-demand", "default").
					Queue("lq-migration-no-constraint").
					Request(corev1.ResourceCPU, "1").
					AllowedFlavors("on-demand").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Active(false).
					Obj(),
				*utiltestingapi.MakeWorkload("parent-variant-spot", "default").
					Queue("lq-migration-no-constraint").
					Request(corev1.ResourceCPU, "1").
					AllowedFlavors("spot").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Active(false).
					Obj(),
			},
		},
		"parent marked finished, mark all variants finished": {
			parentWorkload: utiltestingapi.MakeWorkload("parent-12345", "default").
				Queue("lq").
				Request(corev1.ResourceCPU, "1").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadFinished,
					Status:  metav1.ConditionTrue,
					Reason:  "Succeeded",
					Message: "Job finished successfully",
				}).
				Obj(),
			variantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("parent-variant-on-demand", "default").
					Queue("lq").
					Request(corev1.ResourceCPU, "1").
					AllowedFlavors("on-demand").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Obj(),
				*utiltestingapi.MakeWorkload("parent-variant-spot", "default").
					Queue("lq").
					Request(corev1.ResourceCPU, "1").
					AllowedFlavors("spot").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Obj(),
			},
			wantParentWorkload: utiltestingapi.MakeWorkload("parent-12345", "default").
				Queue("lq").
				Request(corev1.ResourceCPU, "1").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadFinished,
					Status:  metav1.ConditionTrue,
					Reason:  "Succeeded",
					Message: "Job finished successfully",
				}).
				Obj(),
			wantVariantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("parent-variant-on-demand", "default").
					Queue("lq").
					Request(corev1.ResourceCPU, "1").
					AllowedFlavors("on-demand").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Condition(metav1.Condition{
						Type:    kueue.WorkloadFinished,
						Status:  metav1.ConditionTrue,
						Reason:  "Succeeded",
						Message: "Job finished successfully",
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("parent-variant-spot", "default").
					Queue("lq").
					Request(corev1.ResourceCPU, "1").
					AllowedFlavors("spot").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Condition(metav1.Condition{
						Type:    kueue.WorkloadFinished,
						Status:  metav1.ConditionTrue,
						Reason:  "Succeeded",
						Message: "Job finished successfully",
					}).
					Obj(),
			},
		},
		"parent is not active, propagating deactivation to all variants": {
			parentWorkload: utiltestingapi.MakeWorkload("parent-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Request(corev1.ResourceCPU, "1").
				Active(false).
				Obj(),
			variantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("parent-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					Request(corev1.ResourceCPU, "1").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Obj(),
				*utiltestingapi.MakeWorkload("parent-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					Request(corev1.ResourceCPU, "1").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Obj(),
			},
			wantParentWorkload: utiltestingapi.MakeWorkload("parent-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Request(corev1.ResourceCPU, "1").
				Active(false).
				Obj(),
			wantVariantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("parent-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					Request(corev1.ResourceCPU, "1").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Active(false).
					Obj(),
				*utiltestingapi.MakeWorkload("parent-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					Request(corev1.ResourceCPU, "1").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Active(false).
					Obj(),
			},
			wantResult: reconcile.Result{},
			wantErr:    false,
		},
		"parent changes WaitForPodsReady from False to True, syncs to admitted variant": {
			parentWorkload: utiltestingapi.MakeWorkload("parent-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Request(corev1.ResourceCPU, "1").
				SimpleReserveQuota("cq", "spot", metav1.Now().Time).
				AdmittedAt(true, metav1.Now().Time).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionTrue,
					Reason:  "PodsReady",
					Message: "Pods are ready",
				}).
				Obj(),
			variantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("parent-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					Request(corev1.ResourceCPU, "1").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					SimpleReserveQuota("cq", "spot", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadPodsReady,
						Status:  metav1.ConditionFalse,
						Reason:  "PodsNotReady",
						Message: "Pods are not ready",
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("parent-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					Request(corev1.ResourceCPU, "1").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Obj(),
			},
			wantParentWorkload: utiltestingapi.MakeWorkload("parent-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Request(corev1.ResourceCPU, "1").
				SimpleReserveQuota("cq", "spot", metav1.Now().Time).
				AdmittedAt(true, metav1.Now().Time).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionTrue,
					Reason:  "PodsReady",
					Message: "Pods are ready",
				}).
				Obj(),
			wantVariantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("parent-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					Request(corev1.ResourceCPU, "1").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					SimpleReserveQuota("cq", "spot", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadPodsReady,
						Status:  metav1.ConditionTrue,
						Reason:  "PodsReady",
						Message: "Pods are ready",
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("parent-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					Request(corev1.ResourceCPU, "1").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Obj(),
			},
			wantResult: reconcile.Result{},
			wantErr:    false,
		},
		"parent changes WaitForPodsReady from True to False, syncs to admitted variant": {
			parentWorkload: utiltestingapi.MakeWorkload("parent-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Request(corev1.ResourceCPU, "1").
				SimpleReserveQuota("cq", "spot", metav1.Now().Time).
				AdmittedAt(true, metav1.Now().Time).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionFalse,
					Reason:  "PodsNotReady",
					Message: "Pods are not ready",
				}).
				Obj(),
			variantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("parent-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					Request(corev1.ResourceCPU, "1").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					SimpleReserveQuota("cq", "spot", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadPodsReady,
						Status:  metav1.ConditionTrue,
						Reason:  "PodsReady",
						Message: "Pods are ready",
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("parent-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					Request(corev1.ResourceCPU, "1").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Obj(),
			},
			wantParentWorkload: utiltestingapi.MakeWorkload("parent-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Request(corev1.ResourceCPU, "1").
				SimpleReserveQuota("cq", "spot", metav1.Now().Time).
				AdmittedAt(true, metav1.Now().Time).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionFalse,
					Reason:  "PodsNotReady",
					Message: "Pods are not ready",
				}).
				Obj(),
			wantVariantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("parent-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					Request(corev1.ResourceCPU, "1").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					SimpleReserveQuota("cq", "spot", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadPodsReady,
						Status:  metav1.ConditionFalse,
						Reason:  "PodsNotReady",
						Message: "Pods are not ready",
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("parent-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					Request(corev1.ResourceCPU, "1").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Obj(),
			},
			wantResult: reconcile.Result{},
			wantErr:    false,
		},
		"parent not admitted, variant admitted, parent changes WaitForPodsReady from True to False, syncs to variant and admits parent": {
			parentWorkload: utiltestingapi.MakeWorkload("parent-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Request(corev1.ResourceCPU, "1").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionFalse,
					Reason:  "PodsNotReady",
					Message: "Pods are not ready",
				}).
				Obj(),
			variantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("parent-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					Request(corev1.ResourceCPU, "1").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					SimpleReserveQuota("cq", "spot", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadPodsReady,
						Status:  metav1.ConditionTrue,
						Reason:  "PodsReady",
						Message: "Pods are ready",
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("parent-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					Request(corev1.ResourceCPU, "1").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Obj(),
			},
			wantParentWorkload: utiltestingapi.MakeWorkload("parent-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				Request(corev1.ResourceCPU, "1").
				Admission(utiltestingapi.MakeAdmission("cq", "main").
					PodSets(kueue.PodSetAssignment{
						Name: "main",
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: "spot",
						},
						Count:         ptr.To[int32](1),
						ResourceUsage: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
					}).Obj()).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionTrue,
					Reason:  "Admitted",
					Message: "The variant parent-variant-spot is admitted",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionTrue,
					Reason:  "QuotaReserved",
					Message: "Quota reserved in ClusterQueue cq",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionFalse,
					Reason:  "PodsNotReady",
					Message: "Pods are not ready",
				}).
				Obj(),
			wantVariantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("parent-variant-spot", "default").
					Queue("lq").
					AllowedFlavors("spot").
					Request(corev1.ResourceCPU, "1").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					SimpleReserveQuota("cq", "spot", metav1.Now().Time).
					AdmittedAt(true, metav1.Now().Time).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadPodsReady,
						Status:  metav1.ConditionFalse,
						Reason:  "PodsNotReady",
						Message: "Pods are not ready",
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("parent-variant-on-demand", "default").
					Queue("lq").
					AllowedFlavors("on-demand").
					Request(corev1.ResourceCPU, "1").
					ControllerReference(kueue.GroupVersion.WithKind("Workload"), "parent-12345", "").
					Obj(),
			},
			wantResult: reconcile.Result{},
			wantErr:    false,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.ConcurrentAdmission, true)
			var objects []client.Object
			if tc.parentWorkload != nil {
				objects = append(objects, tc.parentWorkload)
			}
			for i := range tc.variantWorkloads {
				objects = append(objects, &tc.variantWorkloads[i])
			}
			cl := utiltesting.NewClientBuilder().
				WithObjects(objects...).
				WithStatusSubresource(objects...).
				Build()
			preemptionExpectations := preemptexpectations.New()
			qManager := qcache.NewManagerForUnitTests(cl, nil, qcache.WithPreemptionExpectations(preemptionExpectations))
			roleTracker := roletracker.NewFakeRoleTracker(roletracker.RoleLeader)

			cqs := []*kueue.ClusterQueue{defaultCQ.DeepCopy(), migrationCQ.DeepCopy(), migrationCQNoConstraint.DeepCopy()}
			lqs := []*kueue.LocalQueue{defaultLQ.DeepCopy(), migrationLQ.DeepCopy(), migrationLQNoConstraint.DeepCopy()}

			for _, cq := range cqs {
				if err := cl.Create(t.Context(), cq); err != nil {
					t.Fatal(err)
				}
				if err := qManager.AddClusterQueue(t.Context(), cq); err != nil {
					t.Fatal(err)
				}
			}

			for _, lq := range lqs {
				if err := cl.Create(t.Context(), lq); err != nil {
					t.Fatal(err)
				}
				if err := qManager.AddLocalQueue(t.Context(), lq); err != nil {
					t.Fatal(err)
				}
			}

			for i := range tc.variantWorkloads {
				_ = qManager.AddOrUpdateWorkload(ctrl.Log, tc.variantWorkloads[i].DeepCopy())
			}

			r := &variantReconciler{
				logName:     ConcurrentAdmissionController,
				client:      cl,
				queues:      qManager,
				roleTracker: roleTracker,
				clock:       testingclock.NewFakeClock(metav1.Now().Time),
				recorder:    record.NewFakeRecorder(10),
			}

			req := tc.req
			if req.Name == "" && tc.parentWorkload != nil {
				req = reconcile.Request{
					NamespacedName: types.NamespacedName{
						Namespace: tc.parentWorkload.Namespace,
						Name:      tc.parentWorkload.Name,
					},
				}
			}

			got, err := r.Reconcile(t.Context(), req)
			if (err != nil) != tc.wantErr {
				t.Errorf("Reconcile() error = %v, wantErr %v", err, tc.wantErr)
				return
			}
			if !cmp.Equal(got, tc.wantResult) {
				t.Errorf("Reconcile() got = %v, want %v", got, tc.wantResult)
			}

			if tc.wantParentWorkload != nil {
				var gotParent kueue.Workload
				err := cl.Get(t.Context(), types.NamespacedName{Namespace: tc.wantParentWorkload.Namespace, Name: tc.wantParentWorkload.Name}, &gotParent)
				if err != nil {
					t.Fatal(err)
				}
				if diff := cmp.Diff(tc.wantParentWorkload, &gotParent, workloadCmpOpts); diff != "" {
					t.Errorf("Unexpected parent workload (-want +got):\n%s", diff)
				}
			}

			var allWorkloads kueue.WorkloadList
			if err := cl.List(t.Context(), &allWorkloads, client.InNamespace(tc.req.Namespace)); err != nil {
				t.Fatal(err)
			}

			var gotVariants []kueue.Workload
			for _, wl := range allWorkloads.Items {
				if concurrentadmission.IsVariant(&wl) {
					gotVariants = append(gotVariants, wl)
				}
			}

			if diff := cmp.Diff(tc.wantVariantWorkloads, gotVariants, workloadCmpOpts); diff != "" {
				t.Errorf("Unexpected variant workloads (-want +got):\n%s", diff)
			}
		})
	}
}
