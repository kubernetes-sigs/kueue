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
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	testingclock "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	queueafs "sigs.k8s.io/kueue/pkg/cache/queue/afs"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	kueuemetrics "sigs.k8s.io/kueue/pkg/metrics"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingmetrics "sigs.k8s.io/kueue/pkg/util/testing/metrics"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util"
)

const (
	resourceGPU = corev1.ResourceName("GPU")
)

func TestLocalQueueReconcile(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	clock := testingclock.NewFakeClock(time.Now().Truncate(time.Second))
	cases := map[string]struct {
		clusterQueue             *kueue.ClusterQueue
		localQueue               *kueue.LocalQueue
		wantLocalQueue           *kueue.LocalQueue
		wantError                error
		afsConfig                *config.AdmissionFairSharing
		runningWls               []kueue.Workload
		wantRequeueAfter         *time.Duration
		initialConsumedResources queueafs.ConsumedResourcesEntry
		pendingEntryPenalty      corev1.ResourceList
		wantConsumedResources    *queueafs.ConsumedResourcesEntry
	}{
		"local queue with Hold StopPolicy": {
			clusterQueue: utiltestingapi.MakeClusterQueue("test-cluster-queue").
				Obj(),
			localQueue: utiltestingapi.MakeLocalQueue("test-queue", "default").
				ClusterQueue("test-cluster-queue").
				PendingWorkloads(1).
				StopPolicy(kueue.Hold).
				Generation(1).
				Obj(),
			wantLocalQueue: utiltestingapi.MakeLocalQueue("test-queue", "default").
				ClusterQueue("test-cluster-queue").
				PendingWorkloads(0).
				StopPolicy(kueue.Hold).
				Generation(1).
				Condition(
					kueue.LocalQueueActive,
					metav1.ConditionFalse,
					StoppedReason,
					localQueueIsInactiveMsg,
					1,
				).
				Obj(),
			wantError: nil,
		},
		"local queue with HoldAndDrain StopPolicy": {
			clusterQueue: utiltestingapi.MakeClusterQueue("test-cluster-queue").
				Obj(),
			localQueue: utiltestingapi.MakeLocalQueue("test-queue", "default").
				ClusterQueue("test-cluster-queue").
				PendingWorkloads(1).
				StopPolicy(kueue.HoldAndDrain).
				Generation(1).
				Obj(),
			wantLocalQueue: utiltestingapi.MakeLocalQueue("test-queue", "default").
				ClusterQueue("test-cluster-queue").
				PendingWorkloads(0).
				StopPolicy(kueue.HoldAndDrain).
				Generation(1).
				Condition(
					kueue.LocalQueueActive,
					metav1.ConditionFalse,
					StoppedReason,
					localQueueIsInactiveMsg,
					1,
				).
				Obj(),
			wantError: nil,
		},
		"cluster queue is inactive": {
			clusterQueue: utiltestingapi.MakeClusterQueue("test-cluster-queue").
				Obj(),
			localQueue: utiltestingapi.MakeLocalQueue("test-queue", "default").
				ClusterQueue("test-cluster-queue").
				PendingWorkloads(1).
				Generation(1).
				Obj(),
			wantLocalQueue: utiltestingapi.MakeLocalQueue("test-queue", "default").
				ClusterQueue("test-cluster-queue").
				PendingWorkloads(0).
				Generation(1).
				Condition(
					kueue.LocalQueueActive,
					metav1.ConditionFalse,
					clusterQueueIsInactiveReason,
					clusterQueueIsInactiveMsg,
					1,
				).
				Obj(),
			wantError: nil,
		},
		"local queue decaying usage decays if there is no running workloads": {
			clusterQueue: utiltestingapi.MakeClusterQueue("cq").
				Active(metav1.ConditionTrue).
				Obj(),
			localQueue: utiltestingapi.MakeLocalQueue("test-queue", "default").
				ClusterQueue("cq").
				Active(metav1.ConditionTrue).
				FairSharing(&kueue.FairSharing{
					Weight: new(resource.MustParse("1")),
				}).
				Obj(),
			initialConsumedResources: queueafs.ConsumedResourcesEntry{
				Resources: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("8"),
				},
				LastUpdate:      now.Add(-5 * time.Minute),
				StatusAccounted: true,
			},
			wantLocalQueue: utiltestingapi.MakeLocalQueue("test-queue", "default").
				ClusterQueue("cq").
				Active(metav1.ConditionTrue).
				FairSharing(&kueue.FairSharing{
					Weight: new(resource.MustParse("1")),
				}).
				FairSharingStatus(
					&kueue.LocalQueueFairSharingStatus{
						AdmissionFairSharingStatus: &kueue.LocalQueueAdmissionFairSharingStatus{
							ConsumedResources: map[corev1.ResourceName]resource.Quantity{
								corev1.ResourceCPU: resource.MustParse("4"),
							},
						},
					}).
				Obj(),
			afsConfig: &config.AdmissionFairSharing{
				UsageHalfLifeTime:     metav1.Duration{Duration: 5 * time.Minute},
				UsageSamplingInterval: metav1.Duration{Duration: 5 * time.Minute},
			},
		},
		"clamps a future LastUpdate stamped by a concurrent settlement": {
			clusterQueue: utiltestingapi.MakeClusterQueue("cq").
				Active(metav1.ConditionTrue).
				Obj(),
			localQueue: utiltestingapi.MakeLocalQueue("test-queue", "default").
				ClusterQueue("cq").
				Active(metav1.ConditionTrue).
				FairSharing(&kueue.FairSharing{
					Weight: new(resource.MustParse("1")),
				}).
				Obj(),
			initialConsumedResources: queueafs.ConsumedResourcesEntry{
				Resources: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("8"),
				},
				// A concurrent settlement stamped a LastUpdate later than now.
				LastUpdate:      now.Add(5 * time.Minute),
				StatusAccounted: true,
			},
			// A pending penalty lets the tick run despite the not-yet-stale
			// (future) LastUpdate, exercising the clamp instead of an early requeue.
			pendingEntryPenalty: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
			wantLocalQueue: utiltestingapi.MakeLocalQueue("test-queue", "default").
				ClusterQueue("cq").
				Active(metav1.ConditionTrue).
				FairSharing(&kueue.FairSharing{
					Weight: new(resource.MustParse("1")),
				}).
				FairSharingStatus(
					&kueue.LocalQueueFairSharingStatus{
						AdmissionFairSharingStatus: &kueue.LocalQueueAdmissionFairSharingStatus{
							ConsumedResources: map[corev1.ResourceName]resource.Quantity{
								// elapsed clamps to 0, so the persisted status keeps
								// the usage verbatim instead of inflating it via the
								// negative-elapsed path.
								corev1.ResourceCPU: resource.MustParse("8"),
							},
						},
					}).
				Obj(),
			wantConsumedResources: &queueafs.ConsumedResourcesEntry{
				Resources: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("8"),
				},
				// Monotonic: the stored timestamp keeps the later value.
				LastUpdate:      now.Add(5 * time.Minute),
				StatusAccounted: true,
			},
			afsConfig: &config.AdmissionFairSharing{
				UsageHalfLifeTime:     metav1.Duration{Duration: 5 * time.Minute},
				UsageSamplingInterval: metav1.Duration{Duration: 5 * time.Minute},
			},
			// The full interval pins that the tick ran; the sampling-interval
			// guard short-circuit would return interval minus the (negative)
			// sinceLastUpdate instead.
			wantRequeueAfter: ptr.To(5 * time.Minute),
		},
		"local queue decaying usage sums the previous state and running workloads": {
			clusterQueue: utiltestingapi.MakeClusterQueue("cq").
				Active(metav1.ConditionTrue).
				Obj(),
			localQueue: utiltestingapi.MakeLocalQueue("lq", "default").
				ClusterQueue("cq").
				Active(metav1.ConditionTrue).
				FairSharing(&kueue.FairSharing{
					Weight: new(resource.MustParse("1")),
				}).
				Obj(),
			initialConsumedResources: queueafs.ConsumedResourcesEntry{
				Resources: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("8"),
				},
				LastUpdate:      clock.Now().Add(-5 * time.Minute),
				StatusAccounted: true,
			},
			wantLocalQueue: utiltestingapi.MakeLocalQueue("lq", "default").
				ClusterQueue("cq").
				Active(metav1.ConditionTrue).
				ReservingWorkloads(1).
				AdmittedWorkloads(1).
				FairSharing(&kueue.FairSharing{
					Weight: new(resource.MustParse("1")),
				}).
				FairSharingStatus(
					&kueue.LocalQueueFairSharingStatus{
						AdmissionFairSharingStatus: &kueue.LocalQueueAdmissionFairSharingStatus{
							ConsumedResources: map[corev1.ResourceName]resource.Quantity{
								corev1.ResourceCPU: resource.MustParse("6"),
							},
						},
					}).
				Obj(),
			runningWls: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "default").
					Queue("lq").
					Request(corev1.ResourceCPU, "4").
					SimpleReserveQuota("cq", "rf", clock.Now()).
					AdmittedAt(true, now).
					Obj(),
			},
			afsConfig: &config.AdmissionFairSharing{
				UsageHalfLifeTime:     metav1.Duration{Duration: 5 * time.Minute},
				UsageSamplingInterval: metav1.Duration{Duration: 5 * time.Minute},
			},
		},
		"local queue decaying usage sums the usage from different flavors and resources": {
			clusterQueue: utiltestingapi.MakeClusterQueue("cq").
				Active(metav1.ConditionTrue).
				Obj(),
			localQueue: utiltestingapi.MakeLocalQueue("lq", "default").
				ClusterQueue("cq").
				Active(metav1.ConditionTrue).
				FairSharing(&kueue.FairSharing{
					Weight: new(resource.MustParse("1")),
				}).
				Obj(),
			initialConsumedResources: queueafs.ConsumedResourcesEntry{
				Resources: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("8"),
					resourceGPU:        resource.MustParse("16"),
				},
				LastUpdate:      clock.Now().Add(-5 * time.Minute),
				StatusAccounted: true,
			},
			wantLocalQueue: utiltestingapi.MakeLocalQueue("lq", "default").
				ClusterQueue("cq").
				Active(metav1.ConditionTrue).
				ReservingWorkloads(3).
				AdmittedWorkloads(3).
				FairSharing(&kueue.FairSharing{
					Weight: new(resource.MustParse("1")),
				}).
				FairSharingStatus(
					&kueue.LocalQueueFairSharingStatus{
						AdmissionFairSharingStatus: &kueue.LocalQueueAdmissionFairSharingStatus{
							ConsumedResources: map[corev1.ResourceName]resource.Quantity{
								corev1.ResourceCPU: resource.MustParse("6"),
								"GPU":              resource.MustParse("10"),
							},
						},
					}).
				Obj(),
			runningWls: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-1", "default").
					Queue("lq").
					Request(corev1.ResourceCPU, "2").
					SimpleReserveQuota("cq", "rf-1", clock.Now()).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-2", "default").
					Queue("lq").
					Request(corev1.ResourceCPU, "2").
					SimpleReserveQuota("cq", "rf-2", clock.Now()).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-3", "default").
					Queue("lq").
					Request("GPU", "4").
					SimpleReserveQuota("cq", "rf-3", clock.Now()).
					AdmittedAt(true, now).
					Obj(),
			},
			afsConfig: &config.AdmissionFairSharing{
				UsageHalfLifeTime:     metav1.Duration{Duration: 5 * time.Minute},
				UsageSamplingInterval: metav1.Duration{Duration: 5 * time.Minute},
			},
		},
		"local queue decaying usage sums the previous state and running workloads half time twice larger than sampling": {
			clusterQueue: utiltestingapi.MakeClusterQueue("cq").
				Active(metav1.ConditionTrue).
				Obj(),
			localQueue: utiltestingapi.MakeLocalQueue("lq", "default").
				ClusterQueue("cq").
				Active(metav1.ConditionTrue).
				FairSharing(&kueue.FairSharing{
					Weight: new(resource.MustParse("1")),
				}).
				Obj(),
			initialConsumedResources: queueafs.ConsumedResourcesEntry{
				Resources: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("8"),
				},
				LastUpdate:      clock.Now().Add(-5 * time.Minute),
				StatusAccounted: true,
			},
			wantLocalQueue: utiltestingapi.MakeLocalQueue("lq", "default").
				ClusterQueue("cq").
				Active(metav1.ConditionTrue).
				FairSharing(&kueue.FairSharing{
					Weight: new(resource.MustParse("1")),
				}).
				ReservingWorkloads(1).
				AdmittedWorkloads(1).
				FairSharingStatus(
					&kueue.LocalQueueFairSharingStatus{
						AdmissionFairSharingStatus: &kueue.LocalQueueAdmissionFairSharingStatus{
							ConsumedResources: map[corev1.ResourceName]resource.Quantity{
								corev1.ResourceCPU: resource.MustParse("6827m"),
							},
						},
					}).
				Obj(),
			runningWls: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "default").
					Queue("lq").
					Request(corev1.ResourceCPU, "4").
					SimpleReserveQuota("cq", "rf", clock.Now()).
					AdmittedAt(true, now).
					Obj(),
			},
			afsConfig: &config.AdmissionFairSharing{
				UsageHalfLifeTime:     metav1.Duration{Duration: 10 * time.Minute},
				UsageSamplingInterval: metav1.Duration{Duration: 5 * time.Minute},
			},
		},
		"local queue decaying usage sums the previous state and running workloads with long half time": {
			clusterQueue: utiltestingapi.MakeClusterQueue("cq").
				Active(metav1.ConditionTrue).
				Obj(),
			localQueue: utiltestingapi.MakeLocalQueue("lq", "default").
				ClusterQueue("cq").
				Active(metav1.ConditionTrue).
				FairSharing(&kueue.FairSharing{
					Weight: new(resource.MustParse("1")),
				}).
				Obj(),
			initialConsumedResources: queueafs.ConsumedResourcesEntry{
				Resources: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("8"),
				},
				LastUpdate:      clock.Now().Add(-5 * time.Minute),
				StatusAccounted: true,
			},
			wantLocalQueue: utiltestingapi.MakeLocalQueue("lq", "default").
				ClusterQueue("cq").
				Active(metav1.ConditionTrue).
				FairSharing(&kueue.FairSharing{
					Weight: new(resource.MustParse("1")),
				}).
				FairSharingStatus(
					&kueue.LocalQueueFairSharingStatus{
						AdmissionFairSharingStatus: &kueue.LocalQueueAdmissionFairSharingStatus{
							ConsumedResources: map[corev1.ResourceName]resource.Quantity{
								corev1.ResourceCPU: resource.MustParse("7980m"),
							},
						},
					}).
				Obj(),
			afsConfig: &config.AdmissionFairSharing{
				UsageHalfLifeTime:     metav1.Duration{Duration: 24 * time.Hour},
				UsageSamplingInterval: metav1.Duration{Duration: 5 * time.Minute},
			},
		},
		"local queue decaying usage sums the previous state and running GPU workloads half time twice larger than sampling": {
			clusterQueue: utiltestingapi.MakeClusterQueue("cq").
				Active(metav1.ConditionTrue).
				Obj(),
			localQueue: utiltestingapi.MakeLocalQueue("lq", "default").
				ClusterQueue("cq").
				Active(metav1.ConditionTrue).
				FairSharing(&kueue.FairSharing{
					Weight: new(resource.MustParse("1")),
				}).
				Obj(),
			initialConsumedResources: queueafs.ConsumedResourcesEntry{
				Resources: corev1.ResourceList{
					resourceGPU: resource.MustParse("8"),
				},
				LastUpdate:      clock.Now().Add(-5 * time.Minute),
				StatusAccounted: true,
			},
			wantLocalQueue: utiltestingapi.MakeLocalQueue("lq", "default").
				ClusterQueue("cq").
				Active(metav1.ConditionTrue).
				FairSharing(&kueue.FairSharing{
					Weight: new(resource.MustParse("1")),
				}).
				ReservingWorkloads(1).
				AdmittedWorkloads(1).
				FairSharingStatus(
					&kueue.LocalQueueFairSharingStatus{
						AdmissionFairSharingStatus: &kueue.LocalQueueAdmissionFairSharingStatus{
							ConsumedResources: map[corev1.ResourceName]resource.Quantity{
								resourceGPU: resource.MustParse("6827m"),
							},
						},
					}).
				Obj(),
			runningWls: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "default").
					Queue("lq").
					Request("GPU", "4").
					SimpleReserveQuota("cq", "rf", clock.Now()).
					AdmittedAt(true, now).
					Obj(),
			},
			afsConfig: &config.AdmissionFairSharing{
				UsageHalfLifeTime:     metav1.Duration{Duration: 10 * time.Minute},
				UsageSamplingInterval: metav1.Duration{Duration: 5 * time.Minute},
			},
		},
		"local queue decaying usage resets to 0 when half life is 0": {
			clusterQueue: utiltestingapi.MakeClusterQueue("cq").
				Active(metav1.ConditionTrue).
				Obj(),
			localQueue: utiltestingapi.MakeLocalQueue("lq", "default").
				ClusterQueue("cq").
				Active(metav1.ConditionTrue).
				FairSharing(&kueue.FairSharing{
					Weight: new(resource.MustParse("1")),
				}).
				Obj(),
			initialConsumedResources: queueafs.ConsumedResourcesEntry{
				Resources: corev1.ResourceList{
					resourceGPU: resource.MustParse("8"),
				},
				LastUpdate:      clock.Now().Add(-5 * time.Minute),
				StatusAccounted: true,
			},
			wantLocalQueue: utiltestingapi.MakeLocalQueue("lq", "default").
				ClusterQueue("cq").
				Active(metav1.ConditionTrue).
				FairSharing(&kueue.FairSharing{
					Weight: new(resource.MustParse("1")),
				}).
				ReservingWorkloads(1).
				AdmittedWorkloads(1).
				FairSharingStatus(
					&kueue.LocalQueueFairSharingStatus{
						AdmissionFairSharingStatus: &kueue.LocalQueueAdmissionFairSharingStatus{},
					}).
				Obj(),
			runningWls: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "default").
					Queue("lq").
					Request("GPU", "4").
					SimpleReserveQuota("cq", "rf", clock.Now()).
					AdmittedAt(true, now).
					Obj(),
			},
			afsConfig: &config.AdmissionFairSharing{
				UsageHalfLifeTime:     metav1.Duration{Duration: 0 * time.Minute},
				UsageSamplingInterval: metav1.Duration{Duration: 5 * time.Minute},
			},
		},
		"local queue decaying usage is not reconciled if not enough time has passed": {
			clusterQueue: utiltestingapi.MakeClusterQueue("cq").
				Active(metav1.ConditionTrue).
				Obj(),
			localQueue: utiltestingapi.MakeLocalQueue("test-queue", "default").
				ClusterQueue("cq").
				Active(metav1.ConditionTrue).
				FairSharing(&kueue.FairSharing{
					Weight: new(resource.MustParse("1")),
				}).
				FairSharingStatus(
					&kueue.LocalQueueFairSharingStatus{
						AdmissionFairSharingStatus: &kueue.LocalQueueAdmissionFairSharingStatus{
							LastUpdate: metav1.NewTime(clock.Now().Add(-4 * time.Minute)),
							ConsumedResources: map[corev1.ResourceName]resource.Quantity{
								corev1.ResourceCPU: resource.MustParse("8")},
						},
					}).
				Obj(),
			initialConsumedResources: queueafs.ConsumedResourcesEntry{
				Resources: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("8"),
				},
				LastUpdate:      clock.Now().Add(-4 * time.Minute),
				StatusAccounted: true,
			},
			wantLocalQueue: utiltestingapi.MakeLocalQueue("test-queue", "default").
				ClusterQueue("cq").
				Active(metav1.ConditionTrue).
				FairSharing(&kueue.FairSharing{
					Weight: new(resource.MustParse("1")),
				}).
				FairSharingStatus(
					&kueue.LocalQueueFairSharingStatus{
						AdmissionFairSharingStatus: &kueue.LocalQueueAdmissionFairSharingStatus{
							LastUpdate: metav1.NewTime(clock.Now().Add(-4 * time.Minute)),
							ConsumedResources: map[corev1.ResourceName]resource.Quantity{
								corev1.ResourceCPU: resource.MustParse("8")},
						},
					}).
				Obj(),
			wantRequeueAfter: ptr.To(time.Minute),
			afsConfig: &config.AdmissionFairSharing{
				UsageHalfLifeTime:     metav1.Duration{Duration: 5 * time.Minute},
				UsageSamplingInterval: metav1.Duration{Duration: 5 * time.Minute},
			},
		},
		"local queue with uninitialized cache initializes status": {
			clusterQueue: utiltestingapi.MakeClusterQueue("cq").
				Active(metav1.ConditionTrue).
				Obj(),
			localQueue: utiltestingapi.MakeLocalQueue("lq", "default").
				ClusterQueue("cq").
				Active(metav1.ConditionTrue).
				FairSharing(&kueue.FairSharing{
					Weight: new(resource.MustParse("1")),
				}).
				Obj(),
			wantLocalQueue: utiltestingapi.MakeLocalQueue("lq", "default").
				ClusterQueue("cq").
				Active(metav1.ConditionTrue).
				FairSharing(&kueue.FairSharing{
					Weight: new(resource.MustParse("1")),
				}).
				FairSharingStatus(
					&kueue.LocalQueueFairSharingStatus{
						AdmissionFairSharingStatus: &kueue.LocalQueueAdmissionFairSharingStatus{
							LastUpdate:        metav1.NewTime(clock.Now()),
							ConsumedResources: corev1.ResourceList{},
						},
					}).
				Obj(),
			afsConfig: &config.AdmissionFairSharing{
				UsageHalfLifeTime:     metav1.Duration{Duration: 5 * time.Minute},
				UsageSamplingInterval: metav1.Duration{Duration: 5 * time.Minute},
			},
		},
		"local queue with uninitialized cache and no prior status seeds empty usage": {
			// The admitted workload must not be counted at full weight here:
			// its entry penalty is added by the workload reconciler, and a
			// full-usage seed would double-count the admission (#12783).
			clusterQueue: utiltestingapi.MakeClusterQueue("cq").
				Active(metav1.ConditionTrue).
				Obj(),
			localQueue: utiltestingapi.MakeLocalQueue("lq", "default").
				ClusterQueue("cq").
				Active(metav1.ConditionTrue).
				FairSharing(&kueue.FairSharing{
					Weight: new(resource.MustParse("1")),
				}).
				Obj(),
			runningWls: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "default").
					Queue("lq").
					Request(corev1.ResourceCPU, "4").
					SimpleReserveQuota("cq", "rf", clock.Now()).
					AdmittedAt(true, now).
					Obj(),
			},
			wantLocalQueue: utiltestingapi.MakeLocalQueue("lq", "default").
				ClusterQueue("cq").
				Active(metav1.ConditionTrue).
				ReservingWorkloads(1).
				AdmittedWorkloads(1).
				FairSharing(&kueue.FairSharing{
					Weight: new(resource.MustParse("1")),
				}).
				FairSharingStatus(
					&kueue.LocalQueueFairSharingStatus{
						AdmissionFairSharingStatus: &kueue.LocalQueueAdmissionFairSharingStatus{
							LastUpdate: metav1.NewTime(clock.Now()),
							ConsumedResources: map[corev1.ResourceName]resource.Quantity{
								corev1.ResourceCPU: resource.MustParse("0"),
							},
						},
					}).
				Obj(),
			afsConfig: &config.AdmissionFairSharing{
				UsageHalfLifeTime:     metav1.Duration{Duration: 5 * time.Minute},
				UsageSamplingInterval: metav1.Duration{Duration: 5 * time.Minute},
			},
		},
		"local queue with uninitialized cache seeds from persisted status usage": {
			// Restart recovery: the persisted status EMA (8 CPU) is restored
			// verbatim; the first tick after seeding folds no live usage, so the
			// rebuilt entry stays 8 CPU and converges toward current usage (4 CPU) over ticks.
			clusterQueue: utiltestingapi.MakeClusterQueue("cq").
				Active(metav1.ConditionTrue).
				Obj(),
			localQueue: utiltestingapi.MakeLocalQueue("lq", "default").
				ClusterQueue("cq").
				Active(metav1.ConditionTrue).
				FairSharing(&kueue.FairSharing{
					Weight: new(resource.MustParse("1")),
				}).
				FairSharingStatus(
					&kueue.LocalQueueFairSharingStatus{
						AdmissionFairSharingStatus: &kueue.LocalQueueAdmissionFairSharingStatus{
							LastUpdate: metav1.NewTime(clock.Now().Add(-5 * time.Minute)),
							ConsumedResources: map[corev1.ResourceName]resource.Quantity{
								corev1.ResourceCPU: resource.MustParse("8"),
							},
						},
					}).
				Obj(),
			runningWls: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "default").
					Queue("lq").
					Request(corev1.ResourceCPU, "4").
					SimpleReserveQuota("cq", "rf", clock.Now()).
					AdmittedAt(true, now).
					Obj(),
			},
			wantLocalQueue: utiltestingapi.MakeLocalQueue("lq", "default").
				ClusterQueue("cq").
				Active(metav1.ConditionTrue).
				ReservingWorkloads(1).
				AdmittedWorkloads(1).
				FairSharing(&kueue.FairSharing{
					Weight: new(resource.MustParse("1")),
				}).
				FairSharingStatus(
					&kueue.LocalQueueFairSharingStatus{
						AdmissionFairSharingStatus: &kueue.LocalQueueAdmissionFairSharingStatus{
							LastUpdate: metav1.NewTime(clock.Now()),
							ConsumedResources: map[corev1.ResourceName]resource.Quantity{
								corev1.ResourceCPU: resource.MustParse("8"),
							},
						},
					}).
				Obj(),
			afsConfig: &config.AdmissionFairSharing{
				UsageHalfLifeTime:     metav1.Duration{Duration: 5 * time.Minute},
				UsageSamplingInterval: metav1.Duration{Duration: 5 * time.Minute},
			},
		},
		"local queue seed merges persisted status into a settlement-created entry": {
			// Restart recovery when settlement wins the race: the entry (2 CPU,
			// StatusAccounted=false) was created by workload settlement before the
			// LocalQueue's first reconcile; the seed merges the persisted history
			// (8 CPU) into it exactly once -> 10 CPU, and the tick then decays
			// toward live usage (4 CPU) over one half-life: 10*0.5 + 4*0.5 = 7.
			clusterQueue: utiltestingapi.MakeClusterQueue("cq").
				Active(metav1.ConditionTrue).
				Obj(),
			localQueue: utiltestingapi.MakeLocalQueue("lq", "default").
				ClusterQueue("cq").
				Active(metav1.ConditionTrue).
				FairSharing(&kueue.FairSharing{
					Weight: new(resource.MustParse("1")),
				}).
				FairSharingStatus(
					&kueue.LocalQueueFairSharingStatus{
						AdmissionFairSharingStatus: &kueue.LocalQueueAdmissionFairSharingStatus{
							LastUpdate: metav1.NewTime(clock.Now().Add(-5 * time.Minute)),
							ConsumedResources: map[corev1.ResourceName]resource.Quantity{
								corev1.ResourceCPU: resource.MustParse("8"),
							},
						},
					}).
				Obj(),
			initialConsumedResources: queueafs.ConsumedResourcesEntry{
				Resources: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("2"),
				},
				LastUpdate:      clock.Now().Add(-5 * time.Minute),
				StatusAccounted: false,
			},
			runningWls: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "default").
					Queue("lq").
					Request(corev1.ResourceCPU, "4").
					SimpleReserveQuota("cq", "rf", clock.Now()).
					AdmittedAt(true, now).
					Obj(),
			},
			wantLocalQueue: utiltestingapi.MakeLocalQueue("lq", "default").
				ClusterQueue("cq").
				Active(metav1.ConditionTrue).
				ReservingWorkloads(1).
				AdmittedWorkloads(1).
				FairSharing(&kueue.FairSharing{
					Weight: new(resource.MustParse("1")),
				}).
				FairSharingStatus(
					&kueue.LocalQueueFairSharingStatus{
						AdmissionFairSharingStatus: &kueue.LocalQueueAdmissionFairSharingStatus{
							LastUpdate: metav1.NewTime(clock.Now()),
							ConsumedResources: map[corev1.ResourceName]resource.Quantity{
								corev1.ResourceCPU: resource.MustParse("7"),
							},
						},
					}).
				Obj(),
			wantConsumedResources: &queueafs.ConsumedResourcesEntry{
				Resources: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("7"),
				},
				LastUpdate:      clock.Now(),
				StatusAccounted: true,
			},
			afsConfig: &config.AdmissionFairSharing{
				UsageHalfLifeTime:     metav1.Duration{Duration: 5 * time.Minute},
				UsageSamplingInterval: metav1.Duration{Duration: 5 * time.Minute},
			},
		},
		"local queue seed does not re-merge persisted status into an accounted entry": {
			// Same shape as above but the entry is already StatusAccounted: the
			// persisted 8 CPU must not be added again; the tick decays the entry
			// (2 CPU) toward live usage (4 CPU): 2*0.5 + 4*0.5 = 3.
			clusterQueue: utiltestingapi.MakeClusterQueue("cq").
				Active(metav1.ConditionTrue).
				Obj(),
			localQueue: utiltestingapi.MakeLocalQueue("lq", "default").
				ClusterQueue("cq").
				Active(metav1.ConditionTrue).
				FairSharing(&kueue.FairSharing{
					Weight: new(resource.MustParse("1")),
				}).
				FairSharingStatus(
					&kueue.LocalQueueFairSharingStatus{
						AdmissionFairSharingStatus: &kueue.LocalQueueAdmissionFairSharingStatus{
							LastUpdate: metav1.NewTime(clock.Now().Add(-5 * time.Minute)),
							ConsumedResources: map[corev1.ResourceName]resource.Quantity{
								corev1.ResourceCPU: resource.MustParse("8"),
							},
						},
					}).
				Obj(),
			initialConsumedResources: queueafs.ConsumedResourcesEntry{
				Resources: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("2"),
				},
				LastUpdate:      clock.Now().Add(-5 * time.Minute),
				StatusAccounted: true,
			},
			runningWls: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "default").
					Queue("lq").
					Request(corev1.ResourceCPU, "4").
					SimpleReserveQuota("cq", "rf", clock.Now()).
					AdmittedAt(true, now).
					Obj(),
			},
			wantLocalQueue: utiltestingapi.MakeLocalQueue("lq", "default").
				ClusterQueue("cq").
				Active(metav1.ConditionTrue).
				ReservingWorkloads(1).
				AdmittedWorkloads(1).
				FairSharing(&kueue.FairSharing{
					Weight: new(resource.MustParse("1")),
				}).
				FairSharingStatus(
					&kueue.LocalQueueFairSharingStatus{
						AdmissionFairSharingStatus: &kueue.LocalQueueAdmissionFairSharingStatus{
							LastUpdate: metav1.NewTime(clock.Now()),
							ConsumedResources: map[corev1.ResourceName]resource.Quantity{
								corev1.ResourceCPU: resource.MustParse("3"),
							},
						},
					}).
				Obj(),
			wantConsumedResources: &queueafs.ConsumedResourcesEntry{
				Resources: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("3"),
				},
				LastUpdate:      clock.Now(),
				StatusAccounted: true,
			},
			afsConfig: &config.AdmissionFairSharing{
				UsageHalfLifeTime:     metav1.Duration{Duration: 5 * time.Minute},
				UsageSamplingInterval: metav1.Duration{Duration: 5 * time.Minute},
			},
		},
		"AFS reconcile skips when cache is recent but status lacks AdmissionFairSharingStatus": {
			clusterQueue: utiltestingapi.MakeClusterQueue("cq-afs-skip").
				Active(metav1.ConditionTrue).
				Obj(),
			localQueue: utiltestingapi.MakeLocalQueue("lq-afs-skip", "default").
				ClusterQueue("cq-afs-skip").
				Active(metav1.ConditionTrue).
				FairSharing(&kueue.FairSharing{
					Weight: new(resource.MustParse("1")),
				}).
				Obj(),
			initialConsumedResources: queueafs.ConsumedResourcesEntry{
				Resources: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("500m"),
				},
				// 1s ago: within the 5s sampling interval, so reconcile should skip.
				LastUpdate:      clock.Now().Add(-1 * time.Second),
				StatusAccounted: true,
			},
			wantLocalQueue: utiltestingapi.MakeLocalQueue("lq-afs-skip", "default").
				ClusterQueue("cq-afs-skip").
				Active(metav1.ConditionTrue).
				FairSharing(&kueue.FairSharing{
					Weight: new(resource.MustParse("1")),
				}).
				Obj(),
			// 5s interval - 1s elapsed = 4s remaining
			wantRequeueAfter: ptr.To(4 * time.Second),
			afsConfig: &config.AdmissionFairSharing{
				UsageHalfLifeTime:     metav1.Duration{Duration: 60 * time.Second},
				UsageSamplingInterval: metav1.Duration{Duration: 5 * time.Second},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			objs := []client.Object{
				tc.clusterQueue,
				tc.localQueue,
			}
			cl := utiltesting.NewClientBuilder().
				WithObjects(objs...).
				WithStatusSubresource(objs...).
				WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge}).
				Build()

			ctxWithLogger, log := utiltesting.ContextWithLog(t)
			cqCache := schdcache.New(cl)
			if err := cqCache.AddClusterQueue(ctxWithLogger, tc.clusterQueue); err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}
			_ = cqCache.AddLocalQueue(tc.localQueue)
			for _, wl := range tc.runningWls {
				cqCache.AddOrUpdateWorkload(log, &wl)
			}
			qManager := qcache.NewManagerForUnitTests(cl, cqCache)
			if err := qManager.AddClusterQueue(ctxWithLogger, tc.clusterQueue); err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}
			_ = qManager.AddLocalQueue(ctxWithLogger, tc.localQueue)
			if tc.initialConsumedResources.Resources != nil {
				lqKey := utilqueue.Key(tc.localQueue)
				qManager.AfsConsumedResources.Update(lqKey, func(queueafs.ConsumedResourcesEntry, bool) queueafs.ConsumedResourcesEntry {
					return tc.initialConsumedResources
				})
			}
			if tc.pendingEntryPenalty != nil {
				// A pending entry penalty bypasses the sampling-interval guard so
				// the tick runs even when the cached LastUpdate is not yet stale.
				qManager.AfsEntryPenalties.Push(utilqueue.Key(tc.localQueue), tc.pendingEntryPenalty)
			}
			reconciler := NewLocalQueueReconciler(cl, qManager, cqCache,
				WithClock(clock),
				WithAdmissionFairSharingConfig(tc.afsConfig))

			ctx, ctxCancel := context.WithCancel(ctxWithLogger)
			defer ctxCancel()

			result, gotError := reconciler.Reconcile(
				ctx,
				reconcile.Request{NamespacedName: client.ObjectKeyFromObject(tc.localQueue)},
			)
			if tc.wantRequeueAfter != nil {
				if diff := cmp.Diff(*tc.wantRequeueAfter, result.RequeueAfter); diff != "" {
					t.Errorf("unexpected reconcile requeue after (-want/+got):\n%s", diff)
				}
			}

			if diff := cmp.Diff(tc.wantError, gotError); diff != "" {
				t.Errorf("unexpected reconcile error (-want/+got):\n%s", diff)
			}

			gotLocalQueue := &kueue.LocalQueue{}
			if err := cl.Get(ctx, client.ObjectKeyFromObject(tc.localQueue), gotLocalQueue); err != nil {
				if tc.wantLocalQueue != nil && !errors.IsNotFound(err) {
					t.Fatalf("Could not get LocalQueue after reconcile: %v", err)
				}
				gotLocalQueue = nil
			}

			cmpOpts := cmp.Options{
				cmpopts.EquateEmpty(),
				util.IgnoreConditionTimestamps,
				util.IgnoreObjectMetaResourceVersion,
				cmpopts.IgnoreFields(kueue.LocalQueueAdmissionFairSharingStatus{}, "LastUpdate"),
			}
			if diff := cmp.Diff(tc.wantLocalQueue, gotLocalQueue, cmpOpts...); diff != "" {
				t.Errorf("Workloads after reconcile (-want,+got):\n%s", diff)
			}

			if tc.wantConsumedResources != nil {
				gotEntry, found := qManager.AfsConsumedResources.Get(utilqueue.Key(tc.localQueue))
				if !found {
					t.Fatal("expected an AfsConsumedResources entry after reconcile")
				}
				if diff := cmp.Diff(tc.wantConsumedResources.Resources, gotEntry.Resources, cmpopts.EquateEmpty()); diff != "" {
					t.Errorf("unexpected consumed resources in cache entry (-want,+got):\n%s", diff)
				}
				if !gotEntry.LastUpdate.Equal(tc.wantConsumedResources.LastUpdate) {
					t.Errorf("unexpected cache entry LastUpdate: want %v, got %v", tc.wantConsumedResources.LastUpdate, gotEntry.LastUpdate)
				}
				if gotEntry.StatusAccounted != tc.wantConsumedResources.StatusAccounted {
					t.Errorf("unexpected cache entry StatusAccounted: want %t, got %t", tc.wantConsumedResources.StatusAccounted, gotEntry.StatusAccounted)
				}
			}
		})
	}
}

func TestLocalQueueReconcileReportsAdmissionFairSharingUsageMetric(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.AdmissionFairSharing, true)
	features.SetFeatureGateDuringTest(t, features.LocalQueueMetrics, true)

	now := time.Now().Truncate(time.Second)
	clock := testingclock.NewFakeClock(now)
	afsConfig := &config.AdmissionFairSharing{
		UsageHalfLifeTime:     metav1.Duration{Duration: 5 * time.Minute},
		UsageSamplingInterval: metav1.Duration{Duration: 5 * time.Minute},
		ResourceWeights: map[corev1.ResourceName]float64{
			corev1.ResourceCPU: 2,
			resourceGPU:        10,
		},
	}
	clusterQueue := utiltestingapi.MakeClusterQueue("cq-afs-metric").
		Active(metav1.ConditionTrue).
		Obj()
	localQueue := utiltestingapi.MakeLocalQueue("lq-afs-metric", "default").
		ClusterQueue("cq-afs-metric").
		Active(metav1.ConditionTrue).
		FairSharing(&kueue.FairSharing{
			Weight: new(resource.MustParse("2")),
		}).
		Obj()
	lqKey := utilqueue.Key(localQueue)
	defer kueuemetrics.ClearLocalQueueMetrics(kueuemetrics.LocalQueueReference{
		Name:      kueue.LocalQueueName(localQueue.Name),
		Namespace: localQueue.Namespace,
	})

	objs := []client.Object{clusterQueue, localQueue}
	cl := utiltesting.NewClientBuilder().
		WithObjects(objs...).
		WithStatusSubresource(objs...).
		WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge}).
		Build()

	ctx, _ := utiltesting.ContextWithLog(t)
	cqCache := schdcache.New(cl)
	if err := cqCache.AddClusterQueue(ctx, clusterQueue); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	_ = cqCache.AddLocalQueue(localQueue)
	qManager := qcache.NewManagerForUnitTests(cl, cqCache, qcache.WithAdmissionFairSharing(afsConfig))
	if err := qManager.AddClusterQueue(ctx, clusterQueue); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if err := qManager.AddLocalQueue(ctx, localQueue); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	qManager.AfsConsumedResources.Set(lqKey, corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("8"),
		resourceGPU:        resource.MustParse("4"),
	}, now.Add(-5*time.Minute))
	qManager.AfsEntryPenalties.Push(lqKey, corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("1"),
		resourceGPU:        resource.MustParse("1"),
	})

	reconciler := NewLocalQueueReconciler(cl, qManager, cqCache,
		WithClock(clock),
		WithAdmissionFairSharingConfig(afsConfig))

	if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(localQueue)}); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	got := utiltestingmetrics.CollectFilteredGaugeVec(kueuemetrics.LocalQueueAdmissionFairSharingUsage, map[string]string{
		"name":          localQueue.Name,
		"namespace":     localQueue.Namespace,
		"cluster_queue": string(localQueue.Spec.ClusterQueue),
	})
	if len(got) != 1 {
		t.Fatalf("Expected one LocalQueue AFS usage metric, got %d", len(got))
	}
	// One half-life decays cached usage to CPU=4 and GPU=2.
	// Pending penalties add CPU=1 and GPU=1: ((4+1)*2 + (2+1)*10) / 2 = 20.
	const wantUsage = 20
	if got[0].Value != wantUsage {
		t.Fatalf("Expected LocalQueue AFS usage metric value %v, got %v", wantUsage, got[0].Value)
	}
}
