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
	"fmt"
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
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	queueafs "sigs.k8s.io/kueue/pkg/cache/queue/afs"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingmetrics "sigs.k8s.io/kueue/pkg/util/testing/metrics"
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
				LastUpdate: now.Add(-5 * time.Minute),
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
				LastUpdate: clock.Now().Add(-5 * time.Minute),
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
				LastUpdate: clock.Now().Add(-5 * time.Minute),
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
				LastUpdate: clock.Now().Add(-5 * time.Minute),
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
				LastUpdate: clock.Now().Add(-5 * time.Minute),
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
				LastUpdate: clock.Now().Add(-5 * time.Minute),
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
				LastUpdate: clock.Now().Add(-5 * time.Minute),
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
				LastUpdate: clock.Now().Add(-4 * time.Minute),
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
		"local queue with uninitialized cache captures current usage": {
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
				qManager.AfsConsumedResources.Set(lqKey, tc.initialConsumedResources.Resources, tc.initialConsumedResources.LastUpdate)
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
		})
	}
}

// TestLocalQueueUpdateClearsMetricsOnLabelChange verifies the #12164 fix: an Update that
// makes a LocalQueue stop matching the metrics selector clears its per-queue metrics.
func TestLocalQueueUpdateClearsMetricsOnLabelChange(t *testing.T) {
	matching := map[string]string{"metrics-test": "true"}
	nonMatching := map[string]string{"metrics-test": "false"}
	lqMetrics := metrics.NewLocalQueueMetricsConfig(&config.LocalQueueMetrics{
		Enable:             true,
		LocalQueueSelector: &metav1.LabelSelector{MatchLabels: matching},
	})

	cases := map[string]struct {
		gateEnabled bool
		oldLabels   map[string]string
		newLabels   map[string]string
		wantCleared bool
	}{
		"matching to non-matching clears metrics": {
			gateEnabled: true,
			oldLabels:   matching,
			newLabels:   nonMatching,
			wantCleared: true,
		},
		"matching to matching keeps metrics": {
			gateEnabled: true,
			oldLabels:   matching,
			newLabels:   matching,
			wantCleared: false,
		},
		"feature gate off is a no-op": {
			gateEnabled: false,
			oldLabels:   matching,
			newLabels:   nonMatching,
			wantCleared: false,
		},
	}

	idx := 0
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.LocalQueueMetrics, tc.gateEnabled)

			// Unique names so Prometheus series do not leak across subtests.
			idx++
			nsName := fmt.Sprintf("ns-%d", idx)
			lqName := fmt.Sprintf("lq-%d", idx)

			cq := utiltestingapi.MakeClusterQueue("cq-" + lqName).Obj()
			oldLQ := utiltestingapi.MakeLocalQueue(lqName, nsName).ClusterQueue(cq.Name).Obj()
			oldLQ.Labels = tc.oldLabels
			newLQ := oldLQ.DeepCopy()
			newLQ.Labels = tc.newLabels

			cl := utiltesting.NewClientBuilder().
				WithObjects(cq, oldLQ).
				WithStatusSubresource(cq, oldLQ).
				Build()

			ctx, _ := utiltesting.ContextWithLog(t)
			cqCache := schdcache.New(cl)
			if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
				t.Fatalf("AddClusterQueue (scheduler cache): %v", err)
			}
			_ = cqCache.AddLocalQueue(oldLQ)
			qManager := qcache.NewManagerForUnitTests(cl, cqCache)
			if err := qManager.AddClusterQueue(ctx, cq); err != nil {
				t.Fatalf("AddClusterQueue (queue manager): %v", err)
			}
			_ = qManager.AddLocalQueue(ctx, oldLQ)

			reconciler := NewLocalQueueReconciler(cl, qManager, cqCache, WithLocalQueueMetrics(lqMetrics))

			// Simulate a previously reported pending-workloads series for this LocalQueue.
			lqRef := metrics.LocalQueueReference{Name: kueue.LocalQueueName(lqName), Namespace: nsName}
			metrics.ReportLocalQueuePendingWorkloads(lqRef, 1, 0, nil, nil)
			defer metrics.ClearLocalQueueMetrics(lqRef)

			filter := map[string]string{"name": lqName, "namespace": nsName}
			if got := testingmetrics.CollectFilteredGaugeVec(metrics.LocalQueuePendingWorkloads, filter); len(got) == 0 {
				t.Fatalf("precondition failed: pending_workloads series should exist before the update")
			}

			reconciler.Update(event.TypedUpdateEvent[*kueue.LocalQueue]{ObjectOld: oldLQ, ObjectNew: newLQ})

			got := testingmetrics.CollectFilteredGaugeVec(metrics.LocalQueuePendingWorkloads, filter)
			if cleared := len(got) == 0; cleared != tc.wantCleared {
				t.Errorf("after update: cleared=%v, want %v (remaining series=%+v)", cleared, tc.wantCleared, got)
			}
		})
	}
}
