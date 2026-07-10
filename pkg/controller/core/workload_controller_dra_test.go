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

package core

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/component-base/featuregate"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestReconcileDRA(t *testing.T) {
	fakeClock := testingclock.NewFakeClock(time.Now().Truncate(time.Second))
	cases := map[string]reconcileTestCase{
		"reconcile DRA ResourceClaim should be rejected as inadmissible": {
			featureGates: map[featuregate.Feature]bool{
				features.KueueDRAIntegration:              true,
				features.MultiKueueOrchestratedPreemption: false,
			},
			workload: utiltestingapi.MakeWorkload("wlWithDRAResourceClaim", "ns").
				Queue("lq").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaim("gpu", "rc1").
					Obj()).
				Obj(),
			resourceClaims: []*resourcev1.ResourceClaim{
				utiltesting.MakeResourceClaim("rc1", "ns").
					DeviceRequest("", "gpu.example.com", 1).
					Obj(),
			},
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("flavor1").
						Resource("gpus", "2").Obj(),
				).Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wlWithDRAResourceClaim", "ns").
				Queue("lq").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaim("gpu", "rc1").
					Obj()).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonMisconfigured,
					Message: "KueueDRAIntegration feature does not support use of resource claims",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadInadmissible,
					Message: "DRA resource claims not supported",
				}).
				Obj(),
			wantEvents: nil,
		},
		"reconcile DRA ResourceClaimTemplate rejected when DRA disabled and KueueDRARejectWorkloadsWhenDRADisabled enabled": {
			featureGates: map[featuregate.Feature]bool{
				features.KueueDRAIntegration:                    false,
				features.KueueDRARejectWorkloadsWhenDRADisabled: true,
				features.MultiKueueOrchestratedPreemption:       false,
			},
			workload: utiltestingapi.MakeWorkload("wlWithDRAResourceClaimTemplate", "ns").
				Queue("lq").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaimTemplate("gpu", "gpu-template").
					Obj()).
				Obj(),
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("flavor1").
						Resource("gpus", "2").Obj(),
				).Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wlWithDRAResourceClaimTemplate", "ns").
				Queue("lq").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaimTemplate("gpu", "gpu-template").
					Obj()).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonMisconfigured,
					Message: "Workload uses DRA resources but the KueueDRAIntegration feature gate is not enabled",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadInadmissible,
					Message: "Workload uses DRA resources but the KueueDRAIntegration feature gate is not enabled",
				}).
				Obj(),
			wantEvents: nil,
		},
		"reconcile DRA ResourceClaim rejected when DRA disabled and KueueDRARejectWorkloadsWhenDRADisabled enabled": {
			featureGates: map[featuregate.Feature]bool{
				features.KueueDRAIntegration:                    false,
				features.KueueDRARejectWorkloadsWhenDRADisabled: true,
				features.MultiKueueOrchestratedPreemption:       false,
			},
			workload: utiltestingapi.MakeWorkload("wlWithDRAResourceClaim", "ns").
				Queue("lq").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaim("gpu", "rc1").
					Obj()).
				Obj(),
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("flavor1").
						Resource("gpus", "2").Obj(),
				).Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wlWithDRAResourceClaim", "ns").
				Queue("lq").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaim("gpu", "rc1").
					Obj()).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonMisconfigured,
					Message: "Workload uses DRA resources but the KueueDRAIntegration feature gate is not enabled",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadInadmissible,
					Message: "Workload uses DRA resources but the KueueDRAIntegration feature gate is not enabled",
				}).
				Obj(),
			wantEvents: nil,
		},
		"reconcile DRA ResourceClaimTemplate should be pre-processed and queued": {
			featureGates: map[featuregate.Feature]bool{
				features.KueueDRAIntegration:              true,
				features.MultiKueueOrchestratedPreemption: false,
			},
			wantDRAResourceTotal: new(int64(1)),
			wantWorkloadsInQueue: new(1),
			workload: utiltestingapi.MakeWorkload("wlWithDRAResourceClaimTemplate", "ns").
				Queue("lq").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaimTemplate("gpu", "gpu-template").
					Obj()).
				Obj(),
			resourceClaimTemplates: []*resourcev1.ResourceClaimTemplate{
				utiltesting.MakeResourceClaimTemplate("gpu-template", "ns").
					DeviceRequest("gpu-request", "gpu.example.com", 1).
					Obj(),
			},
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("flavor1").
						Resource("gpu", "2").Obj(),
				).Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wlWithDRAResourceClaimTemplate", "ns").
				Queue("lq").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaimTemplate("gpu", "gpu-template").
					Obj()).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonSuspended,
					Message: "ClusterQueue cq is inactive",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
			wantEvents: nil,
		},
		"reconcile DRA ResourceClaimTemplate multi-pod should be pre-processed and queued": {
			featureGates: map[featuregate.Feature]bool{
				features.KueueDRAIntegration:              true,
				features.MultiKueueOrchestratedPreemption: false,
			},
			wantDRAResourceTotal: new(int64(6)),
			wantWorkloadsInQueue: new(1),
			workload: utiltestingapi.MakeWorkload("wlMultiPodDRA", "ns").
				Queue("lq").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).
					ResourceClaimTemplate("gpu", "gpu-template").
					Obj()).
				Obj(),
			resourceClaimTemplates: []*resourcev1.ResourceClaimTemplate{
				utiltesting.MakeResourceClaimTemplate("gpu-template", "ns").
					DeviceRequest("gpu-request", "gpu.example.com", 2).
					Obj(),
			},
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("flavor1").
						Resource("gpu", "10").Obj(),
				).Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wlMultiPodDRA", "ns").
				Queue("lq").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).
					ResourceClaimTemplate("gpu", "gpu-template").
					Obj()).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonSuspended,
					Message: "ClusterQueue cq is inactive",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
			wantEvents: nil,
		},
		"reconcile DRA ResourceClaimTemplate with unmapped device class": {
			featureGates: map[featuregate.Feature]bool{
				features.KueueDRAIntegration:              true,
				features.MultiKueueOrchestratedPreemption: false,
			},
			workload: utiltestingapi.MakeWorkload("wlUnmappedDRA", "ns").
				Queue("lq").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaimTemplate("gpu", "gpu-template").
					Obj()).
				Obj(),
			resourceClaimTemplates: []*resourcev1.ResourceClaimTemplate{
				utiltesting.MakeResourceClaimTemplate("gpu-template", "ns").
					DeviceRequest("gpu-request", "unmapped.example.com", 1).
					Obj(),
			},
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("flavor1").
						Resource("gpu", "2").Obj(),
				).Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			wantWorkload: func() *kueue.Workload {
				wl := utiltestingapi.MakeWorkload("wlUnmappedDRA", "ns").
					Queue("lq").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
						ResourceClaimTemplate("gpu", "gpu-template").
						Obj()).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadQuotaReserved,
						Status:  metav1.ConditionFalse,
						Reason:  kueue.WorkloadQuotaReservedReasonMisconfigured,
						Message: "spec.podSets[0].template.spec.resourceClaims[0].resourceClaimTemplateName: Not found: \"DeviceClass unmapped.example.com is not mapped in DRA configuration for podset main\"",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadAdmitted,
						Status:  metav1.ConditionFalse,
						Reason:  kueue.WorkloadAdmittedReasonNoReservation,
						Message: "The workload has no reservation",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadRequeued,
						Status:  metav1.ConditionFalse,
						Reason:  kueue.WorkloadInadmissible,
						Message: "spec.podSets[0].template.spec.resourceClaims[0].resourceClaimTemplateName: Not found: \"DeviceClass unmapped.example.com is not mapped in DRA configuration for podset main\"",
					}).
					Obj()
				wl.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{{
					Name: "gpu", ResourceClaimTemplateName: new("gpu-template"),
				}}
				if len(wl.Spec.PodSets[0].Template.Spec.Containers) > 0 {
					wl.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{{Name: "gpu"}}
				}
				return wl
			}(),
			wantEvents: nil,
		},
		"reconcile DRA validation fails with KueueDRAIntegrationExtendedResource enabled": {
			featureGates: map[featuregate.Feature]bool{
				features.KueueDRAIntegration:                 true,
				features.KueueDRAIntegrationExtendedResource: true,
			},
			workload: utiltestingapi.MakeWorkload("wl-invalid-extended-resource", "ns").
				Queue("lq").
				Request("example.com/gpu", "1500m"). // 1.5 GPUs is invalid because extended resources must be integer quantities
				Obj(),
			additionalObjects: []client.Object{
				utiltesting.MakeDeviceClass("gpu-class").
					ExtendedResourceName("example.com/gpu").
					Obj(),
			},
			cq: utiltestingapi.MakeClusterQueue("cq").Active(metav1.ConditionTrue).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("flavor1").
						Resource("example.com/gpu", "2").Obj(),
				).Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl-invalid-extended-resource", "ns").
				Queue("lq").
				Request("example.com/gpu", "1500m").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonMisconfigured,
					Message: "spec.podSets[0].template.spec.containers[0].resources.requests.example.com/gpu: Invalid value: \"1500m\": extended resource quantity must be an integer",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadInadmissible,
					Message: "spec.podSets[0].template.spec.containers[0].resources.requests.example.com/gpu: Invalid value: \"1500m\": extended resource quantity must be an integer",
				}).
				Obj(),
		},
		"reconcile DRA ResourceClaimTemplate not found should return error": {
			featureGates: map[featuregate.Feature]bool{
				features.KueueDRAIntegration:              true,
				features.MultiKueueOrchestratedPreemption: false,
			},
			workload: utiltestingapi.MakeWorkload("wlMissingTemplate", "ns").
				Queue("lq").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaimTemplate("gpu", "missing-template").
					Obj()).
				Obj(),
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("flavor1").
						Resource("gpu", "2").Obj(),
				).Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wlMissingTemplate", "ns").
				Queue("lq").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaimTemplate("gpu", "missing-template").
					Obj()).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonMisconfigured,
					Message: `spec.podSets[0].template.spec.resourceClaims[0]: Internal error: failed to get claim spec for ResourceClaimTemplate missing-template in podset main: resourceclaimtemplates.resource.k8s.io "missing-template" not found`,
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadInadmissible,
					Message: `spec.podSets[0].template.spec.resourceClaims[0]: Internal error: failed to get claim spec for ResourceClaimTemplate missing-template in podset main: resourceclaimtemplates.resource.k8s.io "missing-template" not found`,
				}).
				Obj(),
			wantErrorMsg: "failed to get claim spec",
			wantEvents:   nil,
		},
		"reconcile DRA transient ResourceSlice list failure should back off": {
			featureGates: map[featuregate.Feature]bool{
				features.KueueDRAIntegration:              true,
				features.MultiKueueOrchestratedPreemption: false,
			},
			listErr: errTest,
			workload: utiltestingapi.MakeWorkload("wlListErrDRA", "ns").
				Queue("lq").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaimTemplate("gpu", "gpu-template").
					Obj()).
				Obj(),
			resourceClaimTemplates: []*resourcev1.ResourceClaimTemplate{
				utiltesting.MakeResourceClaimTemplate("gpu-template", "ns").
					DeviceRequest("gpu-request", "gpu.example.com", 1).
					WithCELSelectors("device.driver == \"test-driver\"").
					Obj(),
			},
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("flavor1").
						Resource("gpu", "2").Obj(),
				).Obj(),
			lq:           utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			wantErrorMsg: "failed to list ResourceSlices",
			wantEvents:   nil,
		},
	}
	runReconcileTestCases(t, cases, fakeClock)
}
