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
	"context"
	stderrors "errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-base/featuregate"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/dra"
	"sigs.k8s.io/kueue/pkg/features"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestReconcileDRA(t *testing.T) {
	errTest := stderrors.New("test error")
	fakeClock := testingclock.NewFakeClock(time.Now().Truncate(time.Second))

	draConfig := []configapi.DeviceClassMapping{
		{
			Name:             corev1.ResourceName("foo"),
			DeviceClassNames: []corev1.ResourceName{"foo.example.com"},
		},
		{
			Name:             corev1.ResourceName("gpu"),
			DeviceClassNames: []corev1.ResourceName{"gpu.example.com", "gpu-class"},
		},
	}
	draMapper := dra.NewResourceMapper()
	if err := draMapper.PopulateFromConfiguration(draConfig); err != nil {
		t.Fatalf("Failed to initialize DRA mapper: %v", err)
	}

	wlPreprocessedTemplate := utiltestingapi.MakeWorkload("wlWithDRAResourceClaimTemplate", "ns").
		Queue("lq").
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
			ResourceClaimTemplate("gpu", "gpu-template").
			Obj()).
		Obj()
	wlWaitingForBackoff := utiltestingapi.MakeWorkload("wlDRAWaitingForBackoff", "ns").
		Queue("lq").
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
			ResourceClaimTemplate("gpu", "gpu-template").
			Obj()).
		RequeueState(new(int32(1)), new(metav1.NewTime(fakeClock.Now().Add(time.Hour)))).
		Obj()
	wlRequeuedAfterBackoff := utiltestingapi.MakeWorkload("wlDRARequeuedAfterBackoff", "ns").
		Queue("lq").
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
			ResourceClaimTemplate("gpu", "gpu-template").
			Obj()).
		Condition(metav1.Condition{
			Type:    kueue.WorkloadRequeued,
			Status:  metav1.ConditionFalse,
			Reason:  kueue.WorkloadEvictedByPodsReadyTimeout,
			Message: "Exceeded the PodsReady timeout ns",
		}).
		RequeueState(new(int32(1)), new(metav1.NewTime(fakeClock.Now().Add(-time.Hour)))).
		Obj()
	wlExtendedRequeuedAfterBackoff := utiltestingapi.MakeWorkload("wlDRAExtendedRequeuedAfterBackoff", "ns").
		Queue("lq").
		Request("example.com/gpu", "1").
		Condition(metav1.Condition{
			Type:    kueue.WorkloadRequeued,
			Status:  metav1.ConditionFalse,
			Reason:  kueue.WorkloadEvictedByPodsReadyTimeout,
			Message: "Exceeded the PodsReady timeout ns",
		}).
		RequeueState(new(int32(1)), new(metav1.NewTime(fakeClock.Now().Add(-time.Hour)))).
		Obj()
	wlMultiPod := utiltestingapi.MakeWorkload("wlMultiPodDRA", "ns").
		Queue("lq").
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).
			ResourceClaimTemplate("gpu", "gpu-template").
			Obj()).
		Obj()

	cases := map[string]struct {
		featureGates      map[featuregate.Feature]bool
		reconcilerOpts    []Option
		listErr           error
		workload          *kueue.Workload
		additionalObjects []client.Object
		cq                *kueue.ClusterQueue
		lq                *kueue.LocalQueue
		wantResult        reconcile.Result
		wantWorkload      *kueue.Workload
		wantErrorMsg      string
		wantEvents        []utiltesting.EventRecord
		verify            func(t *testing.T, qManager *qcache.Manager, cqName kueue.ClusterQueueReference)
	}{
		"reconcile DRA ResourceClaim should be rejected as inadmissible": {
			featureGates: map[featuregate.Feature]bool{
				features.KueueDRAIntegration:              true,
				features.MultiKueueOrchestratedPreemption: false,
			},
			reconcilerOpts: []Option{WithDRAMapper(draMapper)},
			workload: utiltestingapi.MakeWorkload("wlWithDRAResourceClaim", "ns").
				Queue("lq").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaim("gpu", "rc1").
					Obj()).
				Obj(),
			additionalObjects: []client.Object{
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
			reconcilerOpts: []Option{WithDRAMapper(draMapper)},
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
			reconcilerOpts: []Option{WithDRAMapper(draMapper)},
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
		"reconcile DRA ResourceClaimTemplate with unmapped device class": {
			featureGates: map[featuregate.Feature]bool{
				features.KueueDRAIntegration:              true,
				features.MultiKueueOrchestratedPreemption: false,
			},
			reconcilerOpts: []Option{WithDRAMapper(draMapper)},
			workload: utiltestingapi.MakeWorkload("wlUnmappedDRA", "ns").
				Queue("lq").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaimTemplate("gpu", "gpu-template").
					Obj()).
				Obj(),
			additionalObjects: []client.Object{
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
			reconcilerOpts: []Option{WithDRAMapper(draMapper)},
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
			reconcilerOpts: []Option{WithDRAMapper(draMapper)},
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
			reconcilerOpts: []Option{WithDRAMapper(draMapper)},
			listErr:        errTest,
			workload: utiltestingapi.MakeWorkload("wlListErrDRA", "ns").
				Queue("lq").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaimTemplate("gpu", "gpu-template").
					Obj()).
				Obj(),
			additionalObjects: []client.Object{
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
		"reconcile DRA ResourceClaimTemplate should be pre-processed and queued": {
			featureGates: map[featuregate.Feature]bool{
				features.KueueDRAIntegration:              true,
				features.MultiKueueOrchestratedPreemption: false,
			},
			reconcilerOpts: []Option{WithDRAMapper(draMapper)},
			workload:       wlPreprocessedTemplate,
			additionalObjects: []client.Object{
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
			verify: func(t *testing.T, qManager *qcache.Manager, cqName kueue.ClusterQueueReference) {
				found := false
				for _, wlInfo := range qManager.PendingWorkloadsInfo(cqName) {
					if wlInfo.Obj.Name != wlPreprocessedTemplate.Name || wlInfo.Obj.Namespace != wlPreprocessedTemplate.Namespace {
						continue
					}
					found = true
					if len(wlInfo.TotalRequests) == 0 || wlInfo.TotalRequests[0].Requests == nil {
						t.Errorf("Expected TotalRequests with DRA resources, but TotalRequests is empty")
					} else if gpuVal := wlInfo.TotalRequests[0].Requests.ResourceValue("gpu"); gpuVal != 1 {
						t.Errorf("Expected gpu resource total to be %d, got %d", 1, gpuVal)
					}
					break
				}
				if !found {
					t.Errorf("DRA workload %s/%s not found in queue - expected to be queued for processing", wlPreprocessedTemplate.Namespace, wlPreprocessedTemplate.Name)
				}
			},
		},
		"reconcile DRA workload waiting for backoff should preprocess and queue as inadmissible": {
			featureGates: map[featuregate.Feature]bool{
				features.KueueDRAIntegration:              true,
				features.MultiKueueOrchestratedPreemption: false,
			},
			reconcilerOpts: []Option{WithDRAMapper(draMapper)},
			wantResult:     reconcile.Result{RequeueAfter: time.Hour},
			workload:       wlWaitingForBackoff,
			additionalObjects: []client.Object{
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
			wantWorkload: utiltestingapi.MakeWorkload("wlDRAWaitingForBackoff", "ns").
				Queue("lq").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaimTemplate("gpu", "gpu-template").
					Obj()).
				RequeueState(new(int32(1)), new(metav1.NewTime(fakeClock.Now().Add(time.Hour)))).
				Obj(),
			verify: func(t *testing.T, qManager *qcache.Manager, cqName kueue.ClusterQueueReference) {
				found := false
				for _, wlInfo := range qManager.PendingWorkloadsInfo(cqName) {
					if wlInfo.Obj.Name != wlWaitingForBackoff.Name || wlInfo.Obj.Namespace != wlWaitingForBackoff.Namespace {
						continue
					}
					found = true
					if len(wlInfo.TotalRequests) == 0 || wlInfo.TotalRequests[0].Requests == nil {
						t.Errorf("Expected TotalRequests with DRA resources, but TotalRequests is empty")
					} else if gpuVal := wlInfo.TotalRequests[0].Requests.ResourceValue("gpu"); gpuVal != 1 {
						t.Errorf("Expected gpu resource total to be %d, got %d", 1, gpuVal)
					}
					break
				}
				if !found {
					t.Errorf("DRA workload %s/%s not found in queue - expected to be queued for processing", wlWaitingForBackoff.Namespace, wlWaitingForBackoff.Name)
				}

				wlRef := workload.Key(wlWaitingForBackoff)
				inHeap := slices.Contains(qManager.Dump()[cqName], wlRef)
				inInadmissible := slices.Contains(qManager.DumpInadmissible()[cqName], wlRef)
				if inHeap {
					t.Errorf("Expected workload in heap=%v, got %v", false, inHeap)
				}
				if !inInadmissible {
					t.Errorf("Expected workload in inadmissible=%v, got %v", true, inInadmissible)
				}
			},
		},
		"reconcile DRA ResourceClaimTemplate requeued after backoff should keep DRA resources in queue": {
			featureGates: map[featuregate.Feature]bool{
				features.KueueDRAIntegration:              true,
				features.MultiKueueOrchestratedPreemption: false,
			},
			reconcilerOpts: []Option{WithDRAMapper(draMapper)},
			workload:       wlRequeuedAfterBackoff,
			additionalObjects: []client.Object{
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
			wantWorkload: utiltestingapi.MakeWorkload("wlDRARequeuedAfterBackoff", "ns").
				Queue("lq").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaimTemplate("gpu", "gpu-template").
					Obj()).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadEvictedByPodsReadyTimeout,
					Message: "Exceeded the PodsReady timeout ns",
				}).
				RequeueState(new(int32(1)), nil).
				Obj(),
			verify: func(t *testing.T, qManager *qcache.Manager, cqName kueue.ClusterQueueReference) {
				found := false
				for _, wlInfo := range qManager.PendingWorkloadsInfo(cqName) {
					if wlInfo.Obj.Name != wlRequeuedAfterBackoff.Name || wlInfo.Obj.Namespace != wlRequeuedAfterBackoff.Namespace {
						continue
					}
					found = true
					if len(wlInfo.TotalRequests) == 0 || wlInfo.TotalRequests[0].Requests == nil {
						t.Errorf("Expected TotalRequests with DRA resources, but TotalRequests is empty")
					} else if gpuVal := wlInfo.TotalRequests[0].Requests.ResourceValue("gpu"); gpuVal != 1 {
						t.Errorf("Expected gpu resource total to be %d, got %d", 1, gpuVal)
					}
					break
				}
				if !found {
					t.Errorf("DRA workload %s/%s not found in queue - expected to be queued for processing", wlRequeuedAfterBackoff.Namespace, wlRequeuedAfterBackoff.Name)
				}

				wlRef := workload.Key(wlRequeuedAfterBackoff)
				inHeap := slices.Contains(qManager.Dump()[cqName], wlRef)
				inInadmissible := slices.Contains(qManager.DumpInadmissible()[cqName], wlRef)
				if !inHeap {
					t.Errorf("Expected workload in heap=%v, got %v", true, inHeap)
				}
				if inInadmissible {
					t.Errorf("Expected workload in inadmissible=%v, got %v", false, inInadmissible)
				}
			},
		},
		"reconcile DRA extended resource requeued after backoff should replace extended resource in queue": {
			featureGates: map[featuregate.Feature]bool{
				features.KueueDRAIntegration:                 true,
				features.KueueDRAIntegrationExtendedResource: true,
				features.MultiKueueOrchestratedPreemption:    false,
			},
			reconcilerOpts: []Option{WithDRAMapper(draMapper)},
			workload:       wlExtendedRequeuedAfterBackoff,
			additionalObjects: []client.Object{
				utiltesting.MakeDeviceClass("gpu-class").
					ExtendedResourceName("example.com/gpu").
					Obj(),
			},
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("flavor1").
						Resource("gpu", "2").Obj(),
				).Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wlDRAExtendedRequeuedAfterBackoff", "ns").
				Queue("lq").
				Request("example.com/gpu", "1").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadEvictedByPodsReadyTimeout,
					Message: "Exceeded the PodsReady timeout ns",
				}).
				RequeueState(new(int32(1)), nil).
				Obj(),
			verify: func(t *testing.T, qManager *qcache.Manager, cqName kueue.ClusterQueueReference) {
				found := false
				for _, wlInfo := range qManager.PendingWorkloadsInfo(cqName) {
					if wlInfo.Obj.Name != wlExtendedRequeuedAfterBackoff.Name || wlInfo.Obj.Namespace != wlExtendedRequeuedAfterBackoff.Namespace {
						continue
					}
					found = true
					if len(wlInfo.TotalRequests) == 0 || wlInfo.TotalRequests[0].Requests == nil {
						t.Errorf("Expected TotalRequests with DRA resources, but TotalRequests is empty")
					} else if gpuVal := wlInfo.TotalRequests[0].Requests.ResourceValue("gpu"); gpuVal != 1 {
						t.Errorf("Expected gpu resource total to be %d, got %d", 1, gpuVal)
					}
					break
				}
				if !found {
					t.Errorf("DRA workload %s/%s not found in queue - expected to be queued for processing", wlExtendedRequeuedAfterBackoff.Namespace, wlExtendedRequeuedAfterBackoff.Name)
				}

				for _, wlInfo := range qManager.PendingWorkloadsInfo(cqName) {
					if wlInfo.Obj.Name != wlExtendedRequeuedAfterBackoff.Name || wlInfo.Obj.Namespace != wlExtendedRequeuedAfterBackoff.Namespace {
						continue
					}
					if len(wlInfo.TotalRequests) != 0 && wlInfo.TotalRequests[0].Requests != nil {
						wlInfo.TotalRequests[0].Requests.ForEach(func(name corev1.ResourceName, _ int64) {
							if name == corev1.ResourceName("example.com/gpu") {
								t.Errorf("Expected resource %q to be absent from queued TotalRequests", "example.com/gpu")
							}
						})
					}
					break
				}

				wlRef := workload.Key(wlExtendedRequeuedAfterBackoff)
				inHeap := slices.Contains(qManager.Dump()[cqName], wlRef)
				inInadmissible := slices.Contains(qManager.DumpInadmissible()[cqName], wlRef)
				if !inHeap {
					t.Errorf("Expected workload in heap=%v, got %v", true, inHeap)
				}
				if inInadmissible {
					t.Errorf("Expected workload in inadmissible=%v, got %v", false, inInadmissible)
				}
			},
		},
		"reconcile DRA ResourceClaimTemplate multi-pod should be pre-processed and queued": {
			featureGates: map[featuregate.Feature]bool{
				features.KueueDRAIntegration:              true,
				features.MultiKueueOrchestratedPreemption: false,
			},
			reconcilerOpts: []Option{WithDRAMapper(draMapper)},
			workload:       wlMultiPod,
			additionalObjects: []client.Object{
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
			verify: func(t *testing.T, qManager *qcache.Manager, cqName kueue.ClusterQueueReference) {
				found := false
				for _, wlInfo := range qManager.PendingWorkloadsInfo(cqName) {
					if wlInfo.Obj.Name != wlMultiPod.Name || wlInfo.Obj.Namespace != wlMultiPod.Namespace {
						continue
					}
					found = true
					if len(wlInfo.TotalRequests) == 0 || wlInfo.TotalRequests[0].Requests == nil {
						t.Errorf("Expected TotalRequests with DRA resources, but TotalRequests is empty")
					} else if gpuVal := wlInfo.TotalRequests[0].Requests.ResourceValue("gpu"); gpuVal != 6 {
						t.Errorf("Expected gpu resource total to be %d, got %d", 6, gpuVal)
					}
					break
				}
				if !found {
					t.Errorf("DRA workload %s/%s not found in queue - expected to be queued for processing", wlMultiPod.Namespace, wlMultiPod.Name)
				}
			},
		},
	}

	scenarios := []map[featuregate.Feature]bool{
		{
			features.WorkloadRequestUseMergePatch:     false,
			features.UnadmittedWorkloadsObservability: false,
		},
		{
			features.WorkloadRequestUseMergePatch:     false,
			features.UnadmittedWorkloadsObservability: true,
		},
		{
			features.WorkloadRequestUseMergePatch:     true,
			features.UnadmittedWorkloadsObservability: false,
		},
		{
			features.WorkloadRequestUseMergePatch:     true,
			features.UnadmittedWorkloadsObservability: true,
		},
	}

	for name, tc := range cases {
		for _, scenario := range scenarios {
			// Skip scenarios where the test case overrides the scenario's feature gate value
			// to avoid running duplicate tests and misreporting the gate values in the subtest name.
			skip := false
			for fg, val := range tc.featureGates {
				if scenarioVal, exists := scenario[fg]; exists && scenarioVal != val {
					skip = true
					break
				}
			}
			if skip {
				continue
			}

			t.Run(fmt.Sprintf("%s WorkloadRequestUseMergePatch enabled: %t, UnadmittedWorkloadsObservability enabled: %t",
				name, scenario[features.WorkloadRequestUseMergePatch], scenario[features.UnadmittedWorkloadsObservability]), func(t *testing.T) {
				fgMap := make(map[featuregate.Feature]bool)
				maps.Copy(fgMap, scenario)
				maps.Copy(fgMap, tc.featureGates)
				features.SetFeatureGatesDuringTest(t, fgMap)
				features.SetFeatureGateDuringTest(t, features.AdmissionGatedBy, true)

				testWl := tc.workload.DeepCopy()
				objs := []client.Object{testWl}
				if testWl.Namespace != "" {
					objs = append(objs, &corev1.Namespace{
						ObjectMeta: metav1.ObjectMeta{
							Name: testWl.Namespace,
						},
					})
				}
				objs = append(objs, tc.additionalObjects...)

				clientBuilder := utiltesting.NewClientBuilder().
					WithObjects(objs...).
					WithStatusSubresource(objs...).
					WithInterceptorFuncs(interceptor.Funcs{
						SubResourceApply: func(ctx context.Context, client client.Client, subResourceName string, applyConf runtime.ApplyConfiguration, opts ...client.SubResourceApplyOption) error {
							return utiltesting.TreatSSAAsStrategicMergeForApplyConfiguration(ctx, client, subResourceName, applyConf, opts...)
						},
						List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
							if tc.listErr != nil {
								if _, ok := list.(*resourcev1.ResourceSliceList); ok {
									return tc.listErr
								}
							}
							return c.List(ctx, list, opts...)
						},
					})
				if features.Enabled(features.KueueDRAIntegrationExtendedResource) {
					clientBuilder = clientBuilder.WithIndex(&resourcev1.DeviceClass{}, indexer.DeviceClassExtendedResourceNameIndex, indexer.IndexDeviceClassExtendedResourceName)
				}
				cl := clientBuilder.Build()
				recorder := &utiltesting.EventRecorder{}

				cqCache := schdcache.New(cl)
				var draCache *dra.ExtendedResourceCache
				if features.Enabled(features.KueueDRAIntegration) {
					draCache = setupDRACache(objs)
				}
				queueOptions := []qcache.Option{qcache.WithPreemptionExpectations(preemptexpectations.New())}
				if draCache != nil {
					queueOptions = append(queueOptions, qcache.WithDRABackedResources(draCache))
				}
				qManager := qcache.NewManagerForUnitTests(cl, cqCache, queueOptions...)
				reconcilerOpts := tc.reconcilerOpts
				if draCache != nil {
					reconcilerOpts = append(reconcilerOpts, WithDRABackedResources(draCache))
				}
				reconciler := NewWorkloadReconciler(cl, qManager, cqCache, recorder, reconcilerOpts...)
				if features.Enabled(features.KueueDRAIntegration) {
					qManager.SetDRAReconcileChannel(reconciler.GetDRAReconcileChannel())
				}
				
				reconciler.clock = fakeClock

				ctxWithLogger, _ := utiltesting.ContextWithLog(t)
				ctx, ctxCancel := context.WithCancel(ctxWithLogger)
				defer ctxCancel()

				if tc.cq != nil {
					setupClusterQueue(ctx, t, cl, qManager, cqCache, tc.cq, false)
				}

				if tc.lq != nil {
					setupLocalQueue(ctx, t, cl, qManager, tc.lq, false)
				}

				gotResult, gotError := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(testWl)})

				switch {
				case tc.wantErrorMsg != "":
					if gotError == nil {
						t.Errorf("expected error containing %q, got nil", tc.wantErrorMsg)
					} else if !strings.Contains(gotError.Error(), tc.wantErrorMsg) {
						t.Errorf("expected error containing %q, got %v", tc.wantErrorMsg, gotError)
					}
				case gotError != nil:
					t.Errorf("unexpected error: %v", gotError)
				}

				if diff := cmp.Diff(tc.wantResult, gotResult); diff != "" {
					t.Errorf("unexpected reconcile result (-want/+got):\n%s", diff)
				}

				if tc.wantWorkload != nil {
					gotWorkload := &kueue.Workload{}
					if err := cl.Get(ctx, client.ObjectKeyFromObject(testWl), gotWorkload); err != nil {
						if !errors.IsNotFound(err) {
							t.Fatalf("Could not get Workloads after reconcile: %v", err)
						}
						t.Fatalf("expected workload to persist")
					}

					wantWl := tc.wantWorkload.DeepCopy()
					if !features.Enabled(features.UnadmittedWorkloadsObservability) {
						wantWl.Status.Conditions = utiltesting.AdjustConditionsForDisabledObservabilityInWorkloadController(
							wantWl.Status.Conditions,
							apimeta.IsStatusConditionTrue(tc.workload.Status.Conditions, kueue.WorkloadAdmitted),
						)
					}

					if diff := cmp.Diff(wantWl, gotWorkload, workloadCmpOpts...); diff != "" {
						t.Errorf("Workloads after reconcile (-want,+got):\n%s", diff)
					}
				}
				if diff := cmp.Diff(tc.wantEvents, recorder.RecordedEvents); diff != "" {
					t.Errorf("unexpected events (-want/+got):\n%s", diff)
				}

				if tc.verify != nil {
					cqName, found := qManager.ClusterQueueFromLocalQueue(utilqueue.KeyFromWorkload(testWl))
					if !found {
						t.Fatalf("LocalQueue not found in queue manager - workload should have been queued")
					}
					tc.verify(t, qManager, cqName)
				}
			})
		}
	}
}

// The DeviceClass handler runs outside the reconcile loop, so the tests below
// drive it directly instead of going through runReconcileTestCases. A reserved
// Workload is deliberately skipped on a DeviceClass event (#14563).

func TestDeviceClassHandler_Create(t *testing.T) {
	const extResource = "example.com/gpu"

	pending := utiltestingapi.MakeWorkload("wl-pending", "ns").
		Queue("lq").
		Request(corev1.ResourceName(extResource), "1").
		Obj()
	reserved := utiltestingapi.MakeWorkload("wl-reserved", "ns").
		Queue("lq").
		Request(corev1.ResourceName(extResource), "1").
		SimpleReserveQuota("cq", "default", time.Now()).
		Obj()

	testCases := map[string]struct {
		dc           *resourcev1.DeviceClass
		wantRequests []reconcile.Request
	}{
		"create of a DeviceClass with an extended resource name requeues pending workloads and skips reserved workloads": {
			dc: utiltesting.MakeDeviceClass("dc1").ExtendedResourceName(extResource).Obj(),
			wantRequests: []reconcile.Request{
				{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "wl-pending"}},
			},
		},
		"create of a DeviceClass without an extended resource name is ignored": {
			dc: utiltesting.MakeDeviceClass("dc1").Obj(),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			cl := utiltesting.NewClientBuilder().
				WithIndex(&kueue.Workload{}, indexer.WorkloadQuotaReservedKey, indexer.IndexWorkloadQuotaReserved).
				WithIndex(&kueue.Workload{}, indexer.WorkloadExtendedResourceKey, indexer.IndexWorkloadExtendedResources).
				WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType).
				WithObjects(pending, reserved).
				Build()
			cqCache := schdcache.New(cl)
			qManager := qcache.NewManagerForUnitTests(cl, cqCache)
			r := NewWorkloadReconciler(cl, qManager, cqCache, &utiltesting.EventRecorder{}, WithDRABackedResources(dra.NewExtendedResourceCache()))
			h := &deviceClassHandler{r: r}
			q := &utiltesting.MockTypedRateLimitingInterface{}

			h.Create(ctx, event.CreateEvent{Object: tc.dc}, q)

			gotRequests := q.Items
			slices.SortFunc(gotRequests, func(a, b reconcile.Request) int {
				return strings.Compare(a.Name, b.Name)
			})

			if diff := cmp.Diff(tc.wantRequests, gotRequests); diff != "" {
				t.Errorf("Unexpected requests (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestDeviceClassHandler_Update(t *testing.T) {
	const (
		oldExtResource = "example.com/old-gpu"
		newExtResource = "example.com/new-gpu"
	)

	oldPending := utiltestingapi.MakeWorkload("wl-old-pending", "ns").
		Queue("lq").
		Request(corev1.ResourceName(oldExtResource), "1").
		Obj()
	newPending := utiltestingapi.MakeWorkload("wl-new-pending", "ns").
		Queue("lq").
		Request(corev1.ResourceName(newExtResource), "1").
		Obj()
	reserved := utiltestingapi.MakeWorkload("wl-reserved", "ns").
		Queue("lq").
		Request(corev1.ResourceName(oldExtResource), "1").
		Request(corev1.ResourceName(newExtResource), "1").
		SimpleReserveQuota("cq", "default", time.Now()).
		Obj()

	testCases := map[string]struct {
		oldDC        *resourcev1.DeviceClass
		newDC        *resourcev1.DeviceClass
		wantRequests []reconcile.Request
	}{
		"update changing the extended resource name requeues pending workloads for both the old and new resource, and skips reserved workloads": {
			oldDC: utiltesting.MakeDeviceClass("dc1").ExtendedResourceName(oldExtResource).Obj(),
			newDC: utiltesting.MakeDeviceClass("dc1").ExtendedResourceName(newExtResource).Obj(),
			wantRequests: []reconcile.Request{
				{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "wl-new-pending"}},
				{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "wl-old-pending"}},
			},
		},
		"update keeping the same extended resource name requeues pending workloads once per resource occurrence and skips reserved workloads": {
			oldDC: utiltesting.MakeDeviceClass("dc1").ExtendedResourceName(oldExtResource).Obj(),
			newDC: utiltesting.MakeDeviceClass("dc1").ExtendedResourceName(oldExtResource).Obj(),
			wantRequests: []reconcile.Request{
				{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "wl-old-pending"}},
				{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "wl-old-pending"}},
			},
		},
		"update of a DeviceClass without an extended resource name is ignored": {
			oldDC: utiltesting.MakeDeviceClass("dc1").Obj(),
			newDC: utiltesting.MakeDeviceClass("dc1").Obj(),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			cl := utiltesting.NewClientBuilder().
				WithIndex(&kueue.Workload{}, indexer.WorkloadQuotaReservedKey, indexer.IndexWorkloadQuotaReserved).
				WithIndex(&kueue.Workload{}, indexer.WorkloadExtendedResourceKey, indexer.IndexWorkloadExtendedResources).
				WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType).
				WithObjects(oldPending, newPending, reserved).
				Build()
			cqCache := schdcache.New(cl)
			qManager := qcache.NewManagerForUnitTests(cl, cqCache)
			r := NewWorkloadReconciler(cl, qManager, cqCache, &utiltesting.EventRecorder{}, WithDRABackedResources(dra.NewExtendedResourceCache()))
			h := &deviceClassHandler{r: r}
			q := &utiltesting.MockTypedRateLimitingInterface{}

			h.Update(ctx, event.UpdateEvent{ObjectOld: tc.oldDC, ObjectNew: tc.newDC}, q)

			gotRequests := q.Items
			slices.SortFunc(gotRequests, func(a, b reconcile.Request) int {
				return strings.Compare(a.Name, b.Name)
			})

			if diff := cmp.Diff(tc.wantRequests, gotRequests); diff != "" {
				t.Errorf("Unexpected requests (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestDeviceClassHandler_Delete(t *testing.T) {
	const extResource = "example.com/gpu"

	pending := utiltestingapi.MakeWorkload("wl-pending", "ns").
		Queue("lq").
		Request(corev1.ResourceName(extResource), "1").
		Obj()
	reserved := utiltestingapi.MakeWorkload("wl-reserved", "ns").
		Queue("lq").
		Request(corev1.ResourceName(extResource), "1").
		SimpleReserveQuota("cq", "default", time.Now()).
		Obj()

	testCases := map[string]struct {
		dc           *resourcev1.DeviceClass
		wantRequests []reconcile.Request
	}{
		"delete of a DeviceClass with an extended resource name requeues pending workloads and skips reserved workloads": {
			dc: utiltesting.MakeDeviceClass("dc1").ExtendedResourceName(extResource).Obj(),
			wantRequests: []reconcile.Request{
				{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "wl-pending"}},
			},
		},
		"delete of a DeviceClass without an extended resource name is ignored": {
			dc: utiltesting.MakeDeviceClass("dc1").Obj(),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			cl := utiltesting.NewClientBuilder().
				WithIndex(&kueue.Workload{}, indexer.WorkloadQuotaReservedKey, indexer.IndexWorkloadQuotaReserved).
				WithIndex(&kueue.Workload{}, indexer.WorkloadExtendedResourceKey, indexer.IndexWorkloadExtendedResources).
				WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType).
				WithObjects(pending, reserved).
				Build()
			cqCache := schdcache.New(cl)
			qManager := qcache.NewManagerForUnitTests(cl, cqCache)
			r := NewWorkloadReconciler(cl, qManager, cqCache, &utiltesting.EventRecorder{}, WithDRABackedResources(dra.NewExtendedResourceCache()))
			h := &deviceClassHandler{r: r}
			q := &utiltesting.MockTypedRateLimitingInterface{}

			h.Delete(ctx, event.DeleteEvent{Object: tc.dc}, q)

			gotRequests := q.Items
			slices.SortFunc(gotRequests, func(a, b reconcile.Request) int {
				return strings.Compare(a.Name, b.Name)
			})

			if diff := cmp.Diff(tc.wantRequests, gotRequests); diff != "" {
				t.Errorf("Unexpected requests (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestDeviceClassHandler_Generic(t *testing.T) {
	const extResource = "example.com/gpu"

	pending := utiltestingapi.MakeWorkload("wl-pending", "ns").
		Queue("lq").
		Request(corev1.ResourceName(extResource), "1").
		Obj()

	testCases := map[string]struct {
		dc *resourcev1.DeviceClass
	}{
		"generic event is ignored even for a DeviceClass with an extended resource name": {
			dc: utiltesting.MakeDeviceClass("dc1").ExtendedResourceName(extResource).Obj(),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			cl := utiltesting.NewClientBuilder().
				WithIndex(&kueue.Workload{}, indexer.WorkloadQuotaReservedKey, indexer.IndexWorkloadQuotaReserved).
				WithIndex(&kueue.Workload{}, indexer.WorkloadExtendedResourceKey, indexer.IndexWorkloadExtendedResources).
				WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType).
				WithObjects(pending).
				Build()
			cqCache := schdcache.New(cl)
			qManager := qcache.NewManagerForUnitTests(cl, cqCache)
			r := NewWorkloadReconciler(cl, qManager, cqCache, &utiltesting.EventRecorder{}, WithDRABackedResources(dra.NewExtendedResourceCache()))
			h := &deviceClassHandler{r: r}
			q := &utiltesting.MockTypedRateLimitingInterface{}

			h.Generic(ctx, event.GenericEvent{Object: tc.dc}, q)

			if diff := cmp.Diff([]reconcile.Request(nil), q.Items); diff != "" {
				t.Errorf("Unexpected requests (-want,+got):\n%s", diff)
			}
		})
	}
}
