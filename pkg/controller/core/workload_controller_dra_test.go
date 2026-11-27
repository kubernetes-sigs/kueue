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
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/util/workqueue"
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
	"sigs.k8s.io/kueue/pkg/resources"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingdra "sigs.k8s.io/kueue/pkg/util/testingjobs/dra"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestReconcileDRA(t *testing.T) {
	errTest := errors.New("test error")
	errStatus := errors.New("status write failed")
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

	wlWithDRAResourceClaimTemplate := utiltestingapi.MakeWorkload("wlWithDRAResourceClaimTemplate", "ns").
		Queue("lq").
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
			ResourceClaimTemplate("gpu", "gpu-template").
			Obj()).
		Obj()
	wlStaleInadmissibleDRA := utiltestingapi.MakeWorkload("wlStaleInadmissibleDRA", "ns").
		Queue("lq").
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
			ResourceClaimTemplate("gpu", "gpu-template").
			Obj()).
		Condition(metav1.Condition{
			Type:    kueue.WorkloadQuotaReserved,
			Status:  metav1.ConditionFalse,
			Reason:  kueue.WorkloadQuotaReservedReasonMisconfigured,
			Message: "stale inadmissible marking from a previous reconcile",
		}).
		Condition(metav1.Condition{
			Type:    kueue.WorkloadRequeued,
			Status:  metav1.ConditionFalse,
			Reason:  kueue.WorkloadInadmissible,
			Message: "stale inadmissible marking from a previous reconcile",
		}).
		Obj()
	wlStaleDRANotResolved := utiltestingapi.MakeWorkload("wlStaleDRANotResolved", "ns").
		Queue("lq").
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
			ResourceClaimTemplate("gpu", "gpu-template").
			Obj()).
		Condition(metav1.Condition{
			Type:    kueue.WorkloadQuotaReserved,
			Status:  metav1.ConditionFalse,
			Reason:  kueue.WorkloadQuotaReservedReasonDRAResourcesUnresolved,
			Message: "stale inadmissible marking from a previous reconcile",
		}).
		Condition(metav1.Condition{
			Type:    kueue.WorkloadRequeued,
			Status:  metav1.ConditionFalse,
			Reason:  kueue.WorkloadDRAResourcesUnresolved,
			Message: "stale inadmissible marking from a previous reconcile",
		}).
		Obj()
	wlDRAWaitingForBackoff := utiltestingapi.MakeWorkload("wlDRAWaitingForBackoff", "ns").
		Queue("lq").
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
			ResourceClaimTemplate("gpu", "gpu-template").
			Obj()).
		RequeueState(new(int32(1)), new(metav1.NewTime(fakeClock.Now().Add(time.Hour)))).
		Obj()
	wlDRARequeuedAfterBackoff := utiltestingapi.MakeWorkload("wlDRARequeuedAfterBackoff", "ns").
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
	wlDRAExtendedRequeuedAfterBackoff := utiltestingapi.MakeWorkload("wlDRAExtendedRequeuedAfterBackoff", "ns").
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
	wlMultiPodDRA := utiltestingapi.MakeWorkload("wlMultiPodDRA", "ns").
		Queue("lq").
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).
			ResourceClaimTemplate("gpu", "gpu-template").
			Obj()).
		Obj()
	wlWithFirstAvailable := utiltestingapi.MakeWorkload("wlWithFirstAvailable", "ns").
		Queue("lq").
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
			ResourceClaimTemplate("gpu", "gpu-template").
			Obj()).
		Obj()

	cases := map[string]struct {
		featureGates      map[featuregate.Feature]bool
		reconcilerOpts    []Option
		listErr           error
		patchErr          error
		workload          *kueue.Workload
		additionalObjects []client.Object
		cq                *kueue.ClusterQueue
		lq                *kueue.LocalQueue
		wantResult        reconcile.Result
		wantWorkload      *kueue.Workload
		wantError         error
		wantErrorMsg      string
		wantEvents        []utiltesting.EventRecord
		beforeReconcile   func(context.Context, client.Client, *qcache.Manager)
		// wantQueuedRequests, when set, requires the workload to be pending in the
		// ClusterQueue with these resource totals.
		wantQueuedRequests map[corev1.ResourceName]int64
		// wantAbsentRequests lists resources that must not appear in the queued totals.
		wantAbsentRequests []corev1.ResourceName
		// wantInHeap requires the workload to be in the active heap, not inadmissible.
		wantInHeap bool
		// wantNoPendingWorkloads requires the ClusterQueue to have no pending workloads.
		wantNoPendingWorkloads bool
	}{
		"reconcile DRA ResourceClaim should be rejected as inadmissible": {
			featureGates: map[featuregate.Feature]bool{
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
				features.KueueDRAIntegration:              false,
				features.MultiKueueOrchestratedPreemption: false,
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
				features.KueueDRAIntegration:              false,
				features.MultiKueueOrchestratedPreemption: false,
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
		"reconcile DRA ResourceClaimTemplate should be pre-processed and queued": {
			featureGates: map[featuregate.Feature]bool{
				features.MultiKueueOrchestratedPreemption: false,
			},
			reconcilerOpts: []Option{WithDRAMapper(draMapper)},
			workload:       wlWithDRAResourceClaimTemplate,
			additionalObjects: []client.Object{
				testingdra.MakeDeviceClass("gpu.example.com").Obj(),
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
			wantEvents:         nil,
			wantQueuedRequests: map[corev1.ResourceName]int64{"gpu": 1},
		},
		"reconcile DRA persists Requeued=True when previously inadmissible workload resources are resolved": {
			featureGates: map[featuregate.Feature]bool{
				features.MultiKueueOrchestratedPreemption: false,
			},
			reconcilerOpts: []Option{WithDRAMapper(draMapper)},
			workload:       wlStaleInadmissibleDRA,
			additionalObjects: []client.Object{
				testingdra.MakeDeviceClass("gpu.example.com").Obj(),
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
			wantWorkload: utiltestingapi.MakeWorkload("wlStaleInadmissibleDRA", "ns").
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
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadDRAResourcesResolved,
					Message: "DRA resources were resolved after a previous inadmissible marking",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
			wantEvents:         nil,
			wantQueuedRequests: map[corev1.ResourceName]int64{"gpu": 1},
			wantInHeap:         true,
		},
		"reconcile DRA persists Requeued=True when previously DRAResourcesUnresolved workload resources are resolved": {
			featureGates: map[featuregate.Feature]bool{
				features.MultiKueueOrchestratedPreemption: false,
			},
			reconcilerOpts: []Option{WithDRAMapper(draMapper)},
			workload:       wlStaleDRANotResolved,
			additionalObjects: []client.Object{
				testingdra.MakeDeviceClass("gpu.example.com").Obj(),
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
			wantWorkload: utiltestingapi.MakeWorkload("wlStaleDRANotResolved", "ns").
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
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadDRAResourcesResolved,
					Message: "DRA resources were resolved after a previous inadmissible marking",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
			wantEvents:         nil,
			wantQueuedRequests: map[corev1.ResourceName]int64{"gpu": 1},
			wantInHeap:         true,
		},
		"reconcile DRA workload waiting for backoff should preprocess but not queue": {
			featureGates: map[featuregate.Feature]bool{
				features.MultiKueueOrchestratedPreemption: false,
			},
			reconcilerOpts: []Option{WithDRAMapper(draMapper)},
			wantResult:     reconcile.Result{RequeueAfter: time.Hour},
			workload:       wlDRAWaitingForBackoff,
			additionalObjects: []client.Object{
				testingdra.MakeDeviceClass("gpu.example.com").Obj(),
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
			wantEvents:             nil,
			wantNoPendingWorkloads: true,
		},
		"reconcile DRA ResourceClaimTemplate requeued after backoff should keep DRA resources in queue": {
			featureGates: map[featuregate.Feature]bool{
				features.MultiKueueOrchestratedPreemption: false,
			},
			reconcilerOpts: []Option{WithDRAMapper(draMapper)},
			workload:       wlDRARequeuedAfterBackoff,
			additionalObjects: []client.Object{
				testingdra.MakeDeviceClass("gpu.example.com").Obj(),
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
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadBackoffFinished,
					Message: "The workload backoff was finished",
				}).
				RequeueState(new(int32(1)), nil).
				Obj(),
			wantEvents:         nil,
			wantQueuedRequests: map[corev1.ResourceName]int64{"gpu": 1},
			wantInHeap:         true,
		},
		"reconcile DRA extended resource requeued after backoff should replace extended resource in queue": {
			featureGates: map[featuregate.Feature]bool{
				features.KueueDRAIntegrationExtendedResource: true,
				features.MultiKueueOrchestratedPreemption:    false,
			},
			reconcilerOpts: []Option{WithDRAMapper(draMapper)},
			workload:       wlDRAExtendedRequeuedAfterBackoff,
			additionalObjects: []client.Object{
				testingdra.MakeDeviceClass("gpu-class").
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
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadBackoffFinished,
					Message: "The workload backoff was finished",
				}).
				RequeueState(new(int32(1)), nil).
				Obj(),
			wantEvents:         nil,
			wantQueuedRequests: map[corev1.ResourceName]int64{"gpu": 1},
			wantAbsentRequests: []corev1.ResourceName{"example.com/gpu"},
			wantInHeap:         true,
		},
		"reconcile DRA ResourceClaimTemplate multi-pod should be pre-processed and queued": {
			featureGates: map[featuregate.Feature]bool{
				features.MultiKueueOrchestratedPreemption: false,
			},
			reconcilerOpts: []Option{WithDRAMapper(draMapper)},
			workload:       wlMultiPodDRA,
			additionalObjects: []client.Object{
				testingdra.MakeDeviceClass("gpu.example.com").Obj(),
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
			wantEvents:         nil,
			wantQueuedRequests: map[corev1.ResourceName]int64{"gpu": 6},
		},
		"reconcile DRA firstAvailable alternatives of one count should be charged that count once and queued": {
			featureGates: map[featuregate.Feature]bool{
				features.KueueDRAIntegrationPrioritizedList: true,
				features.MultiKueueOrchestratedPreemption:   false,
			},
			reconcilerOpts: []Option{WithDRAMapper(draMapper)},
			workload:       wlWithFirstAvailable,
			additionalObjects: []client.Object{
				testingdra.MakeDeviceClass("gpu-class").Obj(),
				testingdra.MakeDeviceClass("gpu.example.com").Obj(),
				utiltesting.MakeResourceClaimTemplate("gpu-template", "ns").
					DeviceRequests(testingdra.MakeFirstAvailableRequest("gpu",
						testingdra.MakeDeviceSubRequest("a", "gpu.example.com", 2).Obj(),
						testingdra.MakeDeviceSubRequest("b", "gpu-class", 2).Obj(),
					).Obj()).
					Obj(),
			},
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("flavor1").
						Resource("gpu", "2").Obj(),
				).Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wlWithFirstAvailable", "ns").
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
			wantEvents:         nil,
			wantQueuedRequests: map[corev1.ResourceName]int64{"gpu": 2},
		},
		"reconcile DRA firstAvailable alternatives of different counts should be inadmissible": {
			featureGates: map[featuregate.Feature]bool{
				features.KueueDRAIntegrationPrioritizedList: true,
				features.MultiKueueOrchestratedPreemption:   false,
			},
			reconcilerOpts: []Option{WithDRAMapper(draMapper)},
			workload: utiltestingapi.MakeWorkload("wlWithUnequalFirstAvailable", "ns").
				Queue("lq").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaimTemplate("gpu", "gpu-template").
					Obj()).
				Obj(),
			additionalObjects: []client.Object{
				utiltesting.MakeResourceClaimTemplate("gpu-template", "ns").
					DeviceRequests(testingdra.MakeFirstAvailableRequest("gpu",
						testingdra.MakeDeviceSubRequest("a", "gpu.example.com", 1).Obj(),
						testingdra.MakeDeviceSubRequest("b", "gpu-class", 2).Obj(),
					).Obj()).
					Obj(),
			},
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("flavor1").
						Resource("gpu", "2").Obj(),
				).Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wlWithUnequalFirstAvailable", "ns").
				Queue("lq").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaimTemplate("gpu", "gpu-template").
					Obj()).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonDRAResourcesUnresolved,
					Message: "spec.podSets[0].template.spec.resourceClaims[0].devices.requests[0].firstAvailable[1].count: Invalid value: 2: ResourceClaimTemplate gpu-template: every alternative must have count 1, this one has 2",
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
					Reason:  kueue.WorkloadDRAResourcesUnresolved,
					Message: "spec.podSets[0].template.spec.resourceClaims[0].devices.requests[0].firstAvailable[1].count: Invalid value: 2: ResourceClaimTemplate gpu-template: every alternative must have count 1, this one has 2",
				}).
				Obj(),
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "ns", Name: "wlWithUnequalFirstAvailable"},
					EventType: corev1.EventTypeWarning,
					Reason:    kueue.WorkloadDRAResourcesUnresolved,
					Message:   "spec.podSets[0].template.spec.resourceClaims[0].devices.requests[0].firstAvailable[1].count: Invalid value: 2: ResourceClaimTemplate gpu-template: every alternative must have count 1, this one has 2",
				},
			},
		},
		"reconcile DRA firstAvailable alternatives of different counts already inadmissible keeps the conditions": {
			featureGates: map[featuregate.Feature]bool{
				features.KueueDRAIntegrationPrioritizedList: true,
				features.MultiKueueOrchestratedPreemption:   false,
			},
			reconcilerOpts: []Option{WithDRAMapper(draMapper)},
			workload: utiltestingapi.MakeWorkload("wlWithUnequalFirstAvailable", "ns").
				Queue("lq").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaimTemplate("gpu", "gpu-template").
					Obj()).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonDRAResourcesUnresolved,
					Message: "spec.podSets[0].template.spec.resourceClaims[0].devices.requests[0].firstAvailable[1].count: Invalid value: 2: ResourceClaimTemplate gpu-template: every alternative must have count 1, this one has 2",
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
					Reason:  kueue.WorkloadDRAResourcesUnresolved,
					Message: "spec.podSets[0].template.spec.resourceClaims[0].devices.requests[0].firstAvailable[1].count: Invalid value: 2: ResourceClaimTemplate gpu-template: every alternative must have count 1, this one has 2",
				}).
				Obj(),
			additionalObjects: []client.Object{
				utiltesting.MakeResourceClaimTemplate("gpu-template", "ns").
					DeviceRequests(testingdra.MakeFirstAvailableRequest("gpu",
						testingdra.MakeDeviceSubRequest("a", "gpu.example.com", 1).Obj(),
						testingdra.MakeDeviceSubRequest("b", "gpu-class", 2).Obj(),
					).Obj()).
					Obj(),
			},
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("flavor1").
						Resource("gpu", "2").Obj(),
				).Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wlWithUnequalFirstAvailable", "ns").
				Queue("lq").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaimTemplate("gpu", "gpu-template").
					Obj()).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonDRAResourcesUnresolved,
					Message: "spec.podSets[0].template.spec.resourceClaims[0].devices.requests[0].firstAvailable[1].count: Invalid value: 2: ResourceClaimTemplate gpu-template: every alternative must have count 1, this one has 2",
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
					Reason:  kueue.WorkloadDRAResourcesUnresolved,
					Message: "spec.podSets[0].template.spec.resourceClaims[0].devices.requests[0].firstAvailable[1].count: Invalid value: 2: ResourceClaimTemplate gpu-template: every alternative must have count 1, this one has 2",
				}).
				Obj(),
			wantEvents: nil,
		},
		"reconcile DRA preprocessed workload returns error when LocalQueue is missing": {
			featureGates: map[featuregate.Feature]bool{
				features.MultiKueueOrchestratedPreemption: false,
			},
			reconcilerOpts: []Option{WithDRAMapper(draMapper)},
			workload: utiltestingapi.MakeWorkload("wlDRAMissingLQ", "ns").
				Queue("missing-lq").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaimTemplate("gpu", "gpu-template").
					Obj()).
				Obj(),
			additionalObjects: []client.Object{
				testingdra.MakeDeviceClass("gpu.example.com").Obj(),
				utiltesting.MakeResourceClaimTemplate("gpu-template", "ns").
					DeviceRequest("gpu-request", "gpu.example.com", 1).
					Obj(),
			},
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("flavor1").
						Resource("gpu", "2").Obj(),
				).Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wlDRAMissingLQ", "ns").
				Queue("missing-lq").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaimTemplate("gpu", "gpu-template").
					Obj()).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonMisconfigured,
					Message: "LocalQueue missing-lq doesn't exist",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
			wantEvents:   nil,
			wantError:    qcache.ErrLocalQueueDoesNotExistOrInactive,
			wantErrorMsg: "failed to add DRA-preprocessed workload to queue",
		},
		"reconcile DRA ResourceClaimTemplate with unmapped device class": {
			featureGates: map[featuregate.Feature]bool{
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
				testingdra.MakeDeviceClass("unmapped.example.com").Obj(),
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
						Reason:  kueue.WorkloadQuotaReservedReasonDRAResourcesUnresolved,
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
						Reason:  kueue.WorkloadDRAResourcesUnresolved,
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
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "ns", Name: "wlUnmappedDRA"},
					EventType: corev1.EventTypeWarning,
					Reason:    kueue.WorkloadDRAResourcesUnresolved,
					Message:   `spec.podSets[0].template.spec.resourceClaims[0].resourceClaimTemplateName: Not found: "DeviceClass unmapped.example.com is not mapped in DRA configuration for podset main"`,
				},
			},
		},
		"reconcile DRA ResourceClaimTemplate with unmapped device class already inadmissible should not emit duplicate event": {
			featureGates: map[featuregate.Feature]bool{
				features.MultiKueueOrchestratedPreemption: false,
			},
			reconcilerOpts: []Option{WithDRAMapper(draMapper)},
			workload: func() *kueue.Workload {
				wl := utiltestingapi.MakeWorkload("wlUnmappedDRAAlreadyInadmissible", "ns").
					Generation(2).
					Queue("lq").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
						ResourceClaimTemplate("gpu", "gpu-template").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonDRAResourcesUnresolved,
						Message:            `spec.podSets[0].template.spec.resourceClaims[0].resourceClaimTemplateName: Not found: "DeviceClass unmapped.example.com is not mapped in DRA configuration for podset main"`,
						ObservedGeneration: 1,
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadAdmittedReasonNoReservation,
						Message:            "The workload has no reservation",
						ObservedGeneration: 1,
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadRequeued,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadDRAResourcesUnresolved,
						Message:            `spec.podSets[0].template.spec.resourceClaims[0].resourceClaimTemplateName: Not found: "DeviceClass unmapped.example.com is not mapped in DRA configuration for podset main"`,
						ObservedGeneration: 1,
					}).
					Obj()
				return wl
			}(),
			additionalObjects: []client.Object{
				testingdra.MakeDeviceClass("unmapped.example.com").Obj(),
				utiltesting.MakeResourceClaimTemplate("gpu-template", "ns").
					DeviceRequest("gpu-request", "unmapped.example.com", 1).
					Obj(),
			},
			beforeReconcile: func(ctx context.Context, cl client.Client, _ *qcache.Manager) {
				if !features.Enabled(features.UnadmittedWorkloadsObservability) {
					wl := &kueue.Workload{}
					if err := cl.Get(ctx, types.NamespacedName{Namespace: "ns", Name: "wlUnmappedDRAAlreadyInadmissible"}, wl); err != nil {
						panic(err)
					}
					wl.Status.Conditions = utiltesting.AdjustConditionsForDisabledObservabilityInWorkloadController(
						wl.Status.Conditions,
						false,
					)
					if err := cl.Status().Update(ctx, wl); err != nil {
						panic(err)
					}
				}
			},
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("flavor1").
						Resource("gpu", "2").Obj(),
				).Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			wantWorkload: func() *kueue.Workload {
				wl := utiltestingapi.MakeWorkload("wlUnmappedDRAAlreadyInadmissible", "ns").
					Generation(2).
					Queue("lq").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
						ResourceClaimTemplate("gpu", "gpu-template").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonDRAResourcesUnresolved,
						Message:            `spec.podSets[0].template.spec.resourceClaims[0].resourceClaimTemplateName: Not found: "DeviceClass unmapped.example.com is not mapped in DRA configuration for podset main"`,
						ObservedGeneration: 2,
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadAdmittedReasonNoReservation,
						Message:            "The workload has no reservation",
						ObservedGeneration: 2,
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadRequeued,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadDRAResourcesUnresolved,
						Message:            `spec.podSets[0].template.spec.resourceClaims[0].resourceClaimTemplateName: Not found: "DeviceClass unmapped.example.com is not mapped in DRA configuration for podset main"`,
						ObservedGeneration: 2,
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
				features.KueueDRAIntegrationExtendedResource: true,
			},
			reconcilerOpts: []Option{WithDRAMapper(draMapper)},
			workload: utiltestingapi.MakeWorkload("wl-invalid-extended-resource", "ns").
				Queue("lq").
				Request("example.com/gpu", "1500m"). // 1.5 GPUs is invalid because extended resources must be integer quantities
				Obj(),
			additionalObjects: []client.Object{
				testingdra.MakeDeviceClass("gpu-class").
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
					Reason:  kueue.WorkloadQuotaReservedReasonDRAResourcesUnresolved,
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
					Reason:  kueue.WorkloadDRAResourcesUnresolved,
					Message: "spec.podSets[0].template.spec.containers[0].resources.requests.example.com/gpu: Invalid value: \"1500m\": extended resource quantity must be an integer",
				}).
				Obj(),
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "ns", Name: "wl-invalid-extended-resource"},
					EventType: corev1.EventTypeWarning,
					Reason:    kueue.WorkloadDRAResourcesUnresolved,
					Message:   `spec.podSets[0].template.spec.containers[0].resources.requests.example.com/gpu: Invalid value: "1500m": extended resource quantity must be an integer`,
				},
			},
		},
		"reconcile DRA ResourceClaimTemplate not found should return error": {
			featureGates: map[featuregate.Feature]bool{
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
					Reason:  kueue.WorkloadQuotaReservedReasonDRAResourcesUnresolved,
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
					Reason:  kueue.WorkloadDRAResourcesUnresolved,
					Message: `spec.podSets[0].template.spec.resourceClaims[0]: Internal error: failed to get claim spec for ResourceClaimTemplate missing-template in podset main: resourceclaimtemplates.resource.k8s.io "missing-template" not found`,
				}).
				Obj(),
			wantErrorMsg: "failed to get claim spec",
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "ns", Name: "wlMissingTemplate"},
					EventType: corev1.EventTypeWarning,
					Reason:    kueue.WorkloadDRAResourcesUnresolved,
					Message:   `spec.podSets[0].template.spec.resourceClaims[0]: Internal error: failed to get claim spec for ResourceClaimTemplate missing-template in podset main: resourceclaimtemplates.resource.k8s.io "missing-template" not found`,
				},
			},
		},
		"reconcile DRA transient ResourceSlice list failure should back off": {
			featureGates: map[featuregate.Feature]bool{
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
				testingdra.MakeDeviceClass("gpu.example.com").Obj(),
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
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "ns", Name: "wlListErrDRA"},
					EventType: corev1.EventTypeWarning,
					Reason:    kueue.WorkloadDRAResourcesUnresolved,
					Message:   `spec.podSets[0].template.spec.resourceClaims[0]: Internal error: ResourceClaimTemplate gpu-template: failed to list ResourceSlices: test error`,
				},
			},
		},
		"reconcile DRA keeps the processing cause when the status write also fails": {
			featureGates: map[featuregate.Feature]bool{
				features.MultiKueueOrchestratedPreemption: false,
			},
			reconcilerOpts: []Option{WithDRAMapper(draMapper)},
			listErr:        errTest,
			patchErr:       errStatus,
			workload: utiltestingapi.MakeWorkload("wlListErrDRA", "ns").
				Queue("lq").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaimTemplate("gpu", "gpu-template").
					Obj()).
				Obj(),
			additionalObjects: []client.Object{
				testingdra.MakeDeviceClass("gpu.example.com").Obj(),
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
			wantError:    errStatus,
			wantErrorMsg: "failed to list ResourceSlices",
			wantEvents:   nil,
		},
		"reconcile DRA firstAvailable alternatives of different counts sets Requeued back to False when failing again with the same message after the resources were resolved": {
			featureGates: map[featuregate.Feature]bool{
				features.KueueDRAIntegrationPrioritizedList: true,
				features.MultiKueueOrchestratedPreemption:   false,
			},
			reconcilerOpts: []Option{WithDRAMapper(draMapper)},
			workload: utiltestingapi.MakeWorkload("wlWithUnequalFirstAvailable", "ns").
				Queue("lq").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaimTemplate("gpu", "gpu-template").
					Obj()).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonDRAResourcesUnresolved,
					Message: "spec.podSets[0].template.spec.resourceClaims[0].devices.requests[0].firstAvailable[1].count: Invalid value: 2: ResourceClaimTemplate gpu-template: every alternative must have count 1, this one has 2",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadDRAResourcesResolved,
					Message: "DRA resources were resolved after a previous inadmissible marking",
				}).
				Obj(),
			additionalObjects: []client.Object{
				utiltesting.MakeResourceClaimTemplate("gpu-template", "ns").
					DeviceRequests(testingdra.MakeFirstAvailableRequest("gpu",
						testingdra.MakeDeviceSubRequest("a", "gpu.example.com", 1).Obj(),
						testingdra.MakeDeviceSubRequest("b", "gpu-class", 2).Obj(),
					).Obj()).
					Obj(),
			},
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("flavor1").
						Resource("gpu", "2").Obj(),
				).Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wlWithUnequalFirstAvailable", "ns").
				Queue("lq").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaimTemplate("gpu", "gpu-template").
					Obj()).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonDRAResourcesUnresolved,
					Message: "spec.podSets[0].template.spec.resourceClaims[0].devices.requests[0].firstAvailable[1].count: Invalid value: 2: ResourceClaimTemplate gpu-template: every alternative must have count 1, this one has 2",
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
					Reason:  kueue.WorkloadDRAResourcesUnresolved,
					Message: "spec.podSets[0].template.spec.resourceClaims[0].devices.requests[0].firstAvailable[1].count: Invalid value: 2: ResourceClaimTemplate gpu-template: every alternative must have count 1, this one has 2",
				}).
				Obj(),
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "ns", Name: "wlWithUnequalFirstAvailable"},
					EventType: corev1.EventTypeWarning,
					Reason:    kueue.WorkloadDRAResourcesUnresolved,
					Message:   "spec.podSets[0].template.spec.resourceClaims[0].devices.requests[0].firstAvailable[1].count: Invalid value: 2: ResourceClaimTemplate gpu-template: every alternative must have count 1, this one has 2",
				},
			},
		},
		"reconcile DRA ResourceClaimTemplate with unmapped device class sets Requeued back to False when failing again with the same message after the resources were resolved": {
			featureGates: map[featuregate.Feature]bool{
				features.MultiKueueOrchestratedPreemption: false,
			},
			reconcilerOpts: []Option{WithDRAMapper(draMapper)},
			workload: utiltestingapi.MakeWorkload("wlUnmappedDRA", "ns").
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
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadDRAResourcesResolved,
					Message: "DRA resources were resolved after a previous inadmissible marking",
				}).
				Obj(),
			additionalObjects: []client.Object{
				testingdra.MakeDeviceClass("unmapped.example.com").Obj(),
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
						Reason:  kueue.WorkloadDRAResourcesUnresolved,
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
						Reason:  kueue.WorkloadDRAResourcesUnresolved,
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
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "ns", Name: "wlUnmappedDRA"},
					EventType: corev1.EventTypeWarning,
					Reason:    kueue.WorkloadDRAResourcesUnresolved,
					Message:   `spec.podSets[0].template.spec.resourceClaims[0].resourceClaimTemplateName: Not found: "DeviceClass unmapped.example.com is not mapped in DRA configuration for podset main"`,
				},
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
				fgMap := map[featuregate.Feature]bool{features.AdmissionGatedBy: true}
				maps.Copy(fgMap, scenario)
				maps.Copy(fgMap, tc.featureGates)
				features.SetFeatureGatesDuringTest(t, fgMap)

				testWl := tc.workload.DeepCopy()
				if _, ok := tc.featureGates[features.UnadmittedWorkloadsObservability]; !ok && !features.Enabled(features.UnadmittedWorkloadsObservability) {
					testWl.Status.Conditions = utiltesting.AdjustConditionsForDisabledObservabilityInWorkloadController(
						testWl.Status.Conditions,
						apimeta.IsStatusConditionTrue(tc.workload.Status.Conditions, kueue.WorkloadAdmitted),
					)
				}
				objs := []client.Object{testWl}
				if testWl.Namespace != "" {
					objs = append(objs, &corev1.Namespace{
						Name: testWl.Namespace,
					})
				}
				objs = append(objs, tc.additionalObjects...)

				clientBuilder := utiltesting.NewClientBuilder().
					WithObjects(objs...).
					WithStatusSubresource(objs...).
					WithInterceptorFuncs(interceptor.Funcs{
						SubResourcePatch: func(ctx context.Context, client client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
							if tc.patchErr != nil {
								return tc.patchErr
							}
							return client.SubResource(subResourceName).Patch(ctx, obj, patch, opts...)
						},
						SubResourceApply: func(ctx context.Context, client client.Client, subResourceName string, applyConf runtime.ApplyConfiguration, opts ...client.SubResourceApplyOption) error {
							if tc.patchErr != nil {
								return tc.patchErr
							}
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

				if tc.beforeReconcile != nil {
					tc.beforeReconcile(ctx, cl, qManager)
				}

				gotResult, gotError := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(testWl)})

				// wantError and wantErrorMsg are checked independently, so a case can
				// assert both causes of a joined error.
				if tc.wantError != nil && !errors.Is(gotError, tc.wantError) {
					t.Errorf("unexpected error: want %v, got %v", tc.wantError, gotError)
				}
				if tc.wantErrorMsg != "" && (gotError == nil || !strings.Contains(gotError.Error(), tc.wantErrorMsg)) {
					t.Errorf("expected error containing %q, got %v", tc.wantErrorMsg, gotError)
				}
				if tc.wantError == nil && tc.wantErrorMsg == "" && gotError != nil {
					t.Errorf("unexpected error: %v", gotError)
				}

				if diff := cmp.Diff(tc.wantResult, gotResult); diff != "" {
					t.Errorf("unexpected reconcile result (-want/+got):\n%s", diff)
				}

				if tc.wantWorkload != nil {
					gotWorkload := &kueue.Workload{}
					if err := cl.Get(ctx, client.ObjectKeyFromObject(testWl), gotWorkload); err != nil {
						if !apierrors.IsNotFound(err) {
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
				wantEvents := slices.Clone(tc.wantEvents)
				if !features.Enabled(features.UnadmittedWorkloadsObservability) {
					for i := range wantEvents {
						if wantEvents[i].Reason == kueue.WorkloadDRAResourcesUnresolved {
							wantEvents[i].Reason = kueue.WorkloadInadmissible
						}
					}
				}
				if diff := cmp.Diff(wantEvents, recorder.RecordedEvents); diff != "" {
					t.Errorf("unexpected events (-want/+got):\n%s", diff)
				}

				cqName, found := qManager.ClusterQueueFromLocalQueue(utilqueue.KeyFromWorkload(testWl))
				if found {
					pending := qManager.PendingWorkloadsInfo(cqName)
					if tc.wantNoPendingWorkloads && len(pending) != 0 {
						t.Errorf("Expected no workload(s) in queue, got %d workloads", len(pending))
					}
					if tc.wantQueuedRequests != nil {
						idx := slices.IndexFunc(pending, func(info *workload.Info) bool {
							return info.Obj.Name == testWl.Name && info.Obj.Namespace == testWl.Namespace
						})
						if idx < 0 {
							t.Fatalf("DRA workload %s/%s not found in queue - expected to be queued for processing", testWl.Namespace, testWl.Name)
						}
						info := pending[idx]
						if len(info.TotalRequests) == 0 || info.TotalRequests[0].Requests == nil {
							t.Fatalf("Expected TotalRequests with DRA resources, but TotalRequests is empty")
						}
						gotRequests := info.TotalRequests[0].Requests
						for name, want := range tc.wantQueuedRequests {
							if got := gotRequests.ResourceValue(name); got.CmpInt64(want) != 0 {
								t.Errorf("Expected %s resource total to be %d, got %s", name, want, got)
							}
						}
						gotRequests.ForEach(func(name corev1.ResourceName, _ resources.Amount) {
							if slices.Contains(tc.wantAbsentRequests, name) {
								t.Errorf("Expected resource %q to be absent from queued TotalRequests", name)
							}
						})
					}
					if tc.wantInHeap {
						wlRef := workload.Key(testWl)
						if !workloadRefInDump(qManager.Dump(), cqName, wlRef) {
							t.Errorf("Expected workload in heap")
						}
						if workloadRefInDump(qManager.DumpInadmissible(), cqName, wlRef) {
							t.Errorf("Expected workload not in inadmissible")
						}
					}
				} else if tc.wantQueuedRequests != nil || tc.wantInHeap || tc.wantNoPendingWorkloads {
					t.Fatalf("LocalQueue not found in queue manager - workload should have been queued")
				}
			})
		}
	}
}

// The DeviceClass handler runs outside the reconcile loop, so the tests below
// drive it directly instead of going through runReconcileTestCases. A reserved
// Workload is deliberately skipped on a DeviceClass event (#14563).

func TestDeviceClassHandler_Create(t *testing.T) {
	const (
		extResource      = "example.com/gpu"
		implicitResource = corev1.ResourceName(resourcev1.ResourceDeviceClassPrefix + "dc1")
	)

	pending := utiltestingapi.MakeWorkload("wl-pending", "ns").
		Queue("lq").
		Request(corev1.ResourceName(extResource), "1").
		Obj()
	implicitPending := utiltestingapi.MakeWorkload("wl-implicit-pending", "ns").
		Queue("lq").
		Request(implicitResource, "1").
		Obj()
	reserved := utiltestingapi.MakeWorkload("wl-reserved", "ns").
		Queue("lq").
		Request(corev1.ResourceName(extResource), "1").
		Request(implicitResource, "1").
		SimpleReserveQuota("cq", "default", time.Now()).
		Obj()

	testCases := map[string]struct {
		dc                     *resourcev1.DeviceClass
		limitRanges            []*corev1.LimitRange
		wantRequests           []reconcile.Request
		wantDRABackedResources sets.Set[corev1.ResourceName]
	}{
		"create of a DeviceClass with an extended resource name requeues pending workloads for both of its names and skips reserved workloads": {
			dc: testingdra.MakeDeviceClass("dc1").ExtendedResourceName(extResource).Obj(),
			wantRequests: []reconcile.Request{
				{Namespace: "ns", Name: "wl-implicit-pending"},
				{Namespace: "ns", Name: "wl-pending"},
			},
			wantDRABackedResources: sets.New[corev1.ResourceName](extResource, implicitResource),
		},
		"create of a DeviceClass without an extended resource name requeues pending workloads for its implicit name": {
			dc: testingdra.MakeDeviceClass("dc1").Obj(),
			wantRequests: []reconcile.Request{
				{Namespace: "ns", Name: "wl-implicit-pending"},
			},
			wantDRABackedResources: sets.New[corev1.ResourceName](implicitResource),
		},
		"create requeues workloads requesting the implicit resource through LimitRange defaults and skips reserved workloads": {
			dc: testingdra.MakeDeviceClass("dc1").Obj(),
			limitRanges: []*corev1.LimitRange{
				utiltesting.MakeLimitRange("defaults", "defaulted").
					WithValue("Default", implicitResource, "1").
					WithValue("DefaultRequest", implicitResource, "1").Obj(),
				utiltesting.MakeLimitRange("other", "other").
					WithValue("DefaultRequest", "example.com/other", "1").Obj(),
			},
			wantRequests: []reconcile.Request{
				{Namespace: "defaulted", Name: "wl-defaulted"},
				{Namespace: "ns", Name: "wl-implicit-pending"},
			},
			wantDRABackedResources: sets.New[corev1.ResourceName](implicitResource),
		},
		"create requeues workloads requesting the configured resource through LimitRange default requests": {
			dc: testingdra.MakeDeviceClass("dc1").ExtendedResourceName(extResource).Obj(),
			limitRanges: []*corev1.LimitRange{
				utiltesting.MakeLimitRange("defaults", "defaulted").
					WithValue("DefaultRequest", extResource, "1").Obj(),
			},
			wantRequests: []reconcile.Request{
				{Namespace: "defaulted", Name: "wl-defaulted"},
				{Namespace: "ns", Name: "wl-implicit-pending"},
				{Namespace: "ns", Name: "wl-pending"},
			},
			wantDRABackedResources: sets.New[corev1.ResourceName](extResource, implicitResource),
		},
		"create ignores zero defaults and resources mentioned only in LimitRange constraints": {
			dc: testingdra.MakeDeviceClass("dc1").Obj(),
			limitRanges: []*corev1.LimitRange{
				utiltesting.MakeLimitRange("defaults", "defaulted").
					WithValue("Default", implicitResource, "0").
					WithValue("DefaultRequest", implicitResource, "0").
					WithValue("Max", implicitResource, "2").Obj(),
			},
			wantRequests: []reconcile.Request{
				{Namespace: "ns", Name: "wl-implicit-pending"},
			},
			wantDRABackedResources: sets.New[corev1.ResourceName](implicitResource),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			builder := utiltesting.NewClientBuilder().
				WithIndex(&kueue.Workload{}, indexer.WorkloadQuotaReservedKey, indexer.IndexWorkloadQuotaReserved).
				WithIndex(&kueue.Workload{}, indexer.WorkloadExtendedResourceKey, indexer.IndexWorkloadExtendedResources).
				WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType).
				WithObjects(pending, implicitPending, reserved,
					utiltestingapi.MakeWorkload("wl-defaulted", "defaulted").Queue("lq").Obj(),
					utiltestingapi.MakeWorkload("wl-defaulted-reserved", "defaulted").
						Queue("lq").SimpleReserveQuota("cq", "default", time.Now()).Obj(),
					utiltestingapi.MakeWorkload("wl-other", "other").Queue("lq").Obj())
			for _, lr := range tc.limitRanges {
				builder = builder.WithObjects(lr)
			}
			cl := builder.Build()
			cqCache := schdcache.New(cl)
			qManager := qcache.NewManagerForUnitTests(cl, cqCache)
			erCache := dra.NewExtendedResourceCache()
			r := NewWorkloadReconciler(cl, qManager, cqCache, &utiltesting.EventRecorder{}, WithDRABackedResources(erCache))
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
			for _, name := range []corev1.ResourceName{extResource, implicitResource} {
				if got, want := erCache.Has(name), tc.wantDRABackedResources.Has(name); got != want {
					t.Errorf("Unexpected DRA backing for %q: got %v, want %v", name, got, want)
				}
			}
		})
	}
}

func TestDeviceClassHandler_RequeuesPendingWorkloadsWithClaimTemplates(t *testing.T) {
	pending := utiltestingapi.MakeWorkload("wl-pending", "ns").
		Queue("lq").
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).ResourceClaimTemplate("gpu", "gpu-template").Obj()).
		Obj()
	heldBack := utiltestingapi.MakeWorkload("wl-held-back", "ns").
		Queue("lq").
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).ResourceClaimTemplate("gpu", "gpu-template").Obj()).
		Condition(metav1.Condition{Type: kueue.WorkloadRequeued, Status: metav1.ConditionFalse, Reason: kueue.WorkloadDRAResourcesUnresolved}).
		Obj()
	withoutClaims := utiltestingapi.MakeWorkload("wl-without-claims", "ns").
		Queue("lq").
		Obj()
	reserved := utiltestingapi.MakeWorkload("wl-reserved", "ns").
		Queue("lq").
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).ResourceClaimTemplate("gpu", "gpu-template").Obj()).
		SimpleReserveQuota("cq", "default", time.Now()).
		Obj()

	testCases := map[string]struct {
		featureGates map[featuregate.Feature]bool
		delete       bool
		wantRequests []reconcile.Request
	}{
		"create of a DeviceClass requeues pending workloads with ResourceClaimTemplates": {
			wantRequests: []reconcile.Request{
				{Namespace: "ns", Name: "wl-held-back"},
				{Namespace: "ns", Name: "wl-pending"},
			},
		},
		"create of a DeviceClass requeues pending workloads with ResourceClaimTemplates when extended resources are disabled": {
			featureGates: map[featuregate.Feature]bool{features.KueueDRAIntegrationExtendedResource: false},
			wantRequests: []reconcile.Request{
				{Namespace: "ns", Name: "wl-held-back"},
				{Namespace: "ns", Name: "wl-pending"},
			},
		},
		"delete of a DeviceClass requeues pending workloads with ResourceClaimTemplates": {
			delete: true,
			wantRequests: []reconcile.Request{
				{Namespace: "ns", Name: "wl-held-back"},
				{Namespace: "ns", Name: "wl-pending"},
			},
		},
		"delete of a DeviceClass requeues pending workloads with ResourceClaimTemplates when extended resources are disabled": {
			featureGates: map[featuregate.Feature]bool{features.KueueDRAIntegrationExtendedResource: false},
			delete:       true,
			wantRequests: []reconcile.Request{
				{Namespace: "ns", Name: "wl-held-back"},
				{Namespace: "ns", Name: "wl-pending"},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			ctx, _ := utiltesting.ContextWithLog(t)
			cl := utiltesting.NewClientBuilder().
				WithIndex(&kueue.Workload{}, indexer.WorkloadQuotaReservedKey, indexer.IndexWorkloadQuotaReserved).
				WithIndex(&kueue.Workload{}, indexer.WorkloadExtendedResourceKey, indexer.IndexWorkloadExtendedResources).
				WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType).
				WithObjects(pending, heldBack, withoutClaims, reserved).
				Build()
			cqCache := schdcache.New(cl)
			qManager := qcache.NewManagerForUnitTests(cl, cqCache)
			r := NewWorkloadReconciler(cl, qManager, cqCache, &utiltesting.EventRecorder{}, WithDRABackedResources(dra.NewExtendedResourceCache()))
			h := &deviceClassHandler{r: r}
			dc := testingdra.MakeDeviceClass("dc1").Obj()
			q := &utiltesting.MockTypedRateLimitingInterface{}

			if tc.delete {
				h.Delete(ctx, event.DeleteEvent{Object: dc}, q)
			} else {
				h.Create(ctx, event.CreateEvent{Object: dc}, q)
			}

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
		oldDC             *resourcev1.DeviceClass
		newDC             *resourcev1.DeviceClass
		defaultedResource corev1.ResourceName
		wantRequests      []reconcile.Request
	}{
		"update changing the extended resource name requeues pending workloads for both the old and new resource, and skips reserved workloads": {
			oldDC: testingdra.MakeDeviceClass("dc1").ExtendedResourceName(oldExtResource).Obj(),
			newDC: testingdra.MakeDeviceClass("dc1").ExtendedResourceName(newExtResource).Obj(),
			wantRequests: []reconcile.Request{
				{Namespace: "ns", Name: "wl-new-pending"},
				{Namespace: "ns", Name: "wl-old-pending"},
			},
		},
		"update keeping the same extended resource name requeues pending workloads once per resource occurrence and skips reserved workloads": {
			oldDC: testingdra.MakeDeviceClass("dc1").ExtendedResourceName(oldExtResource).Obj(),
			newDC: testingdra.MakeDeviceClass("dc1").ExtendedResourceName(oldExtResource).Obj(),
			wantRequests: []reconcile.Request{
				{Namespace: "ns", Name: "wl-old-pending"},
				{Namespace: "ns", Name: "wl-old-pending"},
			},
		},
		"update of a DeviceClass without an extended resource name is ignored": {
			oldDC: testingdra.MakeDeviceClass("dc1").Obj(),
			newDC: testingdra.MakeDeviceClass("dc1").Obj(),
		},
		"removing the configured resource name refreshes defaulted quota": {
			oldDC:             testingdra.MakeDeviceClass("dc1").ExtendedResourceName(oldExtResource).Obj(),
			newDC:             testingdra.MakeDeviceClass("dc1").Obj(),
			defaultedResource: oldExtResource,
			wantRequests: []reconcile.Request{
				{Namespace: "ns", Name: "wl-old-pending"},
			},
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
			erCache := dra.NewExtendedResourceCache()
			if tc.defaultedResource != "" {
				qManager = setupDeviceClassDefaultedQuota(t, cl, erCache, tc.oldDC, tc.defaultedResource)
			}
			r := NewWorkloadReconciler(cl, qManager, cqCache, &utiltesting.EventRecorder{}, WithDRABackedResources(erCache))
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
			if tc.defaultedResource != "" {
				expectDeviceClassDefaultedQuotaRefreshed(t, qManager, tc.defaultedResource)
			}
		})
	}
}

func TestDeviceClassHandler_Delete(t *testing.T) {
	const (
		extResource      = "example.com/gpu"
		implicitResource = corev1.ResourceName(resourcev1.ResourceDeviceClassPrefix + "dc1")
	)

	pending := utiltestingapi.MakeWorkload("wl-pending", "ns").
		Queue("lq").
		Request(corev1.ResourceName(extResource), "1").
		Obj()
	implicitPending := utiltestingapi.MakeWorkload("wl-implicit-pending", "ns").
		Queue("lq").
		Request(implicitResource, "1").
		Obj()
	reserved := utiltestingapi.MakeWorkload("wl-reserved", "ns").
		Queue("lq").
		Request(corev1.ResourceName(extResource), "1").
		Request(implicitResource, "1").
		SimpleReserveQuota("cq", "default", time.Now()).
		Obj()

	testCases := map[string]struct {
		dc                *resourcev1.DeviceClass
		defaultedResource corev1.ResourceName
		wantRequests      []reconcile.Request
	}{
		"delete of a DeviceClass with an extended resource name requeues pending workloads for both of its names and skips reserved workloads": {
			dc: testingdra.MakeDeviceClass("dc1").ExtendedResourceName(extResource).Obj(),
			wantRequests: []reconcile.Request{
				{Namespace: "ns", Name: "wl-implicit-pending"},
				{Namespace: "ns", Name: "wl-pending"},
			},
		},
		"delete of a DeviceClass without an extended resource name requeues pending workloads for its implicit name": {
			dc: testingdra.MakeDeviceClass("dc1").Obj(),
			wantRequests: []reconcile.Request{
				{Namespace: "ns", Name: "wl-implicit-pending"},
			},
		},
		"delete of a DeviceClass backing the configured resource refreshes defaulted quota": {
			dc:                testingdra.MakeDeviceClass("dc1").ExtendedResourceName(extResource).Obj(),
			defaultedResource: extResource,
			wantRequests: []reconcile.Request{
				{Namespace: "ns", Name: "wl-implicit-pending"},
				{Namespace: "ns", Name: "wl-pending"},
			},
		},
		"delete of a DeviceClass backing the implicit resource refreshes defaulted quota": {
			dc:                testingdra.MakeDeviceClass("dc1").Obj(),
			defaultedResource: implicitResource,
			wantRequests: []reconcile.Request{
				{Namespace: "ns", Name: "wl-implicit-pending"},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			cl := utiltesting.NewClientBuilder().
				WithIndex(&kueue.Workload{}, indexer.WorkloadQuotaReservedKey, indexer.IndexWorkloadQuotaReserved).
				WithIndex(&kueue.Workload{}, indexer.WorkloadExtendedResourceKey, indexer.IndexWorkloadExtendedResources).
				WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType).
				WithObjects(pending, implicitPending, reserved).
				Build()
			cqCache := schdcache.New(cl)
			qManager := qcache.NewManagerForUnitTests(cl, cqCache)
			erCache := dra.NewExtendedResourceCache()
			if tc.defaultedResource != "" {
				qManager = setupDeviceClassDefaultedQuota(t, cl, erCache, tc.dc, tc.defaultedResource)
			}
			r := NewWorkloadReconciler(cl, qManager, cqCache, &utiltesting.EventRecorder{}, WithDRABackedResources(erCache))
			h := &deviceClassHandler{r: r}
			// A DeviceClass is deleted only after it was created, which registered its names.
			h.Create(ctx, event.CreateEvent{Object: tc.dc}, &utiltesting.MockTypedRateLimitingInterface{})
			q := &utiltesting.MockTypedRateLimitingInterface{}

			h.Delete(ctx, event.DeleteEvent{Object: tc.dc}, q)

			gotRequests := q.Items
			slices.SortFunc(gotRequests, func(a, b reconcile.Request) int {
				return strings.Compare(a.Name, b.Name)
			})

			if diff := cmp.Diff(tc.wantRequests, gotRequests); diff != "" {
				t.Errorf("Unexpected requests (-want,+got):\n%s", diff)
			}
			for _, name := range []corev1.ResourceName{extResource, implicitResource} {
				if erCache.Has(name) {
					t.Errorf("%q is still DRA-backed after its DeviceClass was deleted", name)
				}
			}
			if tc.defaultedResource != "" {
				expectDeviceClassDefaultedQuotaRefreshed(t, qManager, tc.defaultedResource)
			}
		})
	}
}

func setupDeviceClassDefaultedQuota(t *testing.T, cl client.Client, erCache *dra.ExtendedResourceCache, dc *resourcev1.DeviceClass, resourceName corev1.ResourceName) *qcache.Manager {
	t.Helper()
	ctx, log := utiltesting.ContextWithLog(t)
	const logicalResource corev1.ResourceName = "logical-gpu"
	defaulted := utiltestingapi.MakeWorkload("wl-defaulted", "defaulted").Queue("lq").Obj()
	lr := utiltesting.MakeLimitRange("defaults", defaulted.Namespace).
		WithValue("Default", resourceName, "2").
		WithValue("DefaultRequest", resourceName, "2").Obj()
	if err := cl.Create(ctx, lr); err != nil {
		t.Fatal(err)
	}
	erCache.Add(resourceName, dc.Name)
	erCache.Add(corev1.ResourceName(implicitExtendedResourceName(dc)), dc.Name)
	qManager := qcache.NewManagerForUnitTests(cl, nil,
		qcache.WithPreemptionExpectations(preemptexpectations.New()),
		qcache.WithDRABackedResources(erCache))
	if err := qManager.AddClusterQueue(ctx, utiltestingapi.MakeClusterQueue("cq").Obj()); err != nil {
		t.Fatal(err)
	}
	if err := qManager.AddLocalQueue(ctx, utiltestingapi.MakeLocalQueue("lq", defaulted.Namespace).ClusterQueue("cq").Obj()); err != nil {
		t.Fatal(err)
	}
	if err := cl.Create(ctx, defaulted); err != nil {
		t.Fatal(err)
	}
	// Seed the logical charge computed before the DeviceClass change.
	if err := qManager.AddOrUpdateWorkload(ctx, log, defaulted, workload.WithPreprocessedDRAResources(
		map[kueue.PodSetReference]corev1.ResourceList{
			kueue.DefaultPodSetName: {logicalResource: resource.MustParse("2")},
		},
		map[kueue.PodSetReference]sets.Set[corev1.ResourceName]{
			kueue.DefaultPodSetName: sets.New(resourceName),
		},
	)); err != nil {
		t.Fatal(err)
	}
	heads := qManager.Heads(ctx)
	if len(heads) != 1 {
		t.Fatalf("popped workloads = %d, want 1", len(heads))
	}
	if got := heads[0].TotalRequests[0].Requests.ResourceValue(logicalResource); got.CmpInt64(2) != 0 {
		t.Fatalf("initial logical quota = %s, want 2", got)
	}
	if !qManager.RequeueWorkload(ctx, &heads[0].Info, qcache.RequeueReasonNoFit, qcache.QuotaReservedReasonWaitingForQuota) {
		t.Fatal("workload was not requeued")
	}
	return qManager
}

func expectDeviceClassDefaultedQuotaRefreshed(t *testing.T, qManager *qcache.Manager, resourceName corev1.ResourceName) {
	t.Helper()
	infos := qManager.PendingWorkloadsInfo("cq")
	if len(infos) != 1 {
		t.Fatalf("pending workloads = %d, want 1", len(infos))
	}
	if got := infos[0].TotalRequests[0].Requests.ResourceValue("logical-gpu"); got.Sign() != 0 {
		t.Errorf("logical quota after DeviceClass change = %s, want 0", got)
	}
	if got := infos[0].TotalRequests[0].Requests.ResourceValue(resourceName); got.CmpInt64(2) != 0 {
		t.Errorf("original resource quota after DeviceClass change = %s, want 2", got)
	}
	if !qManager.HasQueuedWorkloads("cq") {
		t.Error("workload was not reactivated")
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
			dc: testingdra.MakeDeviceClass("dc1").ExtendedResourceName(extResource).Obj(),
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

func TestResourceUpdatesHandler(t *testing.T) {
	podSet := func(runtimeClass string) kueue.PodSet {
		ps := utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).ResourceClaimTemplate("gpu", "tmpl")
		if runtimeClass != "" {
			ps = ps.RuntimeClass(runtimeClass)
		}
		return *ps.Obj()
	}
	workloads := []client.Object{
		utiltestingapi.MakeWorkload("kata-pending", "ns1").Queue("lq").PodSets(podSet("kata")).Obj(),
		utiltestingapi.MakeWorkload("kata-pending", "ns2").Queue("lq").PodSets(podSet("kata")).Obj(),
		utiltestingapi.MakeWorkload("kata-reserved", "ns1").Queue("lq").PodSets(podSet("kata")).
			SimpleReserveQuota("cq", "default", time.Now()).Obj(),
		utiltestingapi.MakeWorkload("gvisor-pending", "ns1").Queue("lq").PodSets(podSet("gvisor")).Obj(),
		utiltestingapi.MakeWorkload("no-runtimeclass-pending", "ns1").Queue("lq").PodSets(podSet("")).Obj(),
	}

	events := map[string]func(context.Context, *resourceUpdatesHandler, client.Object, workqueue.TypedRateLimitingInterface[reconcile.Request]){
		"create": func(ctx context.Context, h *resourceUpdatesHandler, obj client.Object, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			h.Create(ctx, event.CreateEvent{Object: obj}, q)
		},
		"update": func(ctx context.Context, h *resourceUpdatesHandler, obj client.Object, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			h.Update(ctx, event.UpdateEvent{ObjectOld: obj, ObjectNew: obj}, q)
		},
		"delete": func(ctx context.Context, h *resourceUpdatesHandler, obj client.Object, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			h.Delete(ctx, event.DeleteEvent{Object: obj}, q)
		},
	}

	testCases := map[string]struct {
		obj          client.Object
		wantRequests []reconcile.Request
	}{
		"RuntimeClass requeues only pending workloads that use it, in every namespace": {
			obj: utiltesting.MakeRuntimeClass("kata", "kata").Obj(),
			wantRequests: []reconcile.Request{
				{Namespace: "ns1", Name: "kata-pending"},
				{Namespace: "ns2", Name: "kata-pending"},
			},
		},
		"RuntimeClass not used by any workload requeues nothing": {
			obj: utiltesting.MakeRuntimeClass("runc", "runc").Obj(),
		},
		"LimitRange requeues only pending workloads in its namespace": {
			obj: utiltesting.MakeLimitRange("limits", "ns1").Obj(),
			wantRequests: []reconcile.Request{
				{Namespace: "ns1", Name: "gvisor-pending"},
				{Namespace: "ns1", Name: "kata-pending"},
				{Namespace: "ns1", Name: "no-runtimeclass-pending"},
			},
		},
	}

	for name, tc := range testCases {
		for eventName, trigger := range events {
			t.Run(name+"/"+eventName, func(t *testing.T) {
				// DRA workloads are requeued as reconcile requests, which makes the listed workloads observable.
				ctx, _ := utiltesting.ContextWithLog(t)
				cl := utiltesting.NewClientBuilder().
					WithIndex(&kueue.Workload{}, indexer.WorkloadQuotaReservedKey, indexer.IndexWorkloadQuotaReserved).
					WithIndex(&kueue.Workload{}, indexer.WorkloadRuntimeClassKey, indexer.IndexWorkloadRuntimeClass).
					WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType).
					WithObjects(utiltesting.MakeRuntimeClass("kata", "kata").Obj(), utiltesting.MakeRuntimeClass("gvisor", "runsc").Obj()).
					WithObjects(workloads...).
					Build()
				cqCache := schdcache.New(cl)
				qManager := qcache.NewManagerForUnitTests(cl, cqCache)
				h := &resourceUpdatesHandler{r: NewWorkloadReconciler(cl, qManager, cqCache, &utiltesting.EventRecorder{})}
				q := &utiltesting.MockTypedRateLimitingInterface{}

				trigger(ctx, h, tc.obj, q)

				gotRequests := q.Items
				slices.SortFunc(gotRequests, func(a, b reconcile.Request) int {
					return strings.Compare(a.String(), b.String())
				})
				if diff := cmp.Diff(tc.wantRequests, gotRequests); diff != "" {
					t.Errorf("Unexpected requests (-want,+got):\n%s", diff)
				}
			})
		}
	}
}
