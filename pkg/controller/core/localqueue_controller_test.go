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

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/queue"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/util"
)

func TestLocalQueueReconcile(t *testing.T) {
	now := time.Now()
	cases := map[string]struct {
		clusterQueue   *kueue.ClusterQueue
		localQueue     *kueue.LocalQueue
		wantLocalQueue *kueue.LocalQueue
		wantError      error
		fsConfig       *config.FairSharing
		runningWls     []kueue.Workload
	}{
		"local queue with Hold StopPolicy": {
			clusterQueue: utiltesting.MakeClusterQueue("test-cluster-queue").
				Obj(),
			localQueue: utiltesting.MakeLocalQueue("test-queue", "default").
				ClusterQueue("test-cluster-queue").
				PendingWorkloads(1).
				StopPolicy(kueue.Hold).
				Generation(1).
				Obj(),
			wantLocalQueue: utiltesting.MakeLocalQueue("test-queue", "default").
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
			clusterQueue: utiltesting.MakeClusterQueue("test-cluster-queue").
				Obj(),
			localQueue: utiltesting.MakeLocalQueue("test-queue", "default").
				ClusterQueue("test-cluster-queue").
				PendingWorkloads(1).
				StopPolicy(kueue.HoldAndDrain).
				Generation(1).
				Obj(),
			wantLocalQueue: utiltesting.MakeLocalQueue("test-queue", "default").
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
			clusterQueue: utiltesting.MakeClusterQueue("test-cluster-queue").
				Obj(),
			localQueue: utiltesting.MakeLocalQueue("test-queue", "default").
				ClusterQueue("test-cluster-queue").
				PendingWorkloads(1).
				Generation(1).
				Obj(),
			wantLocalQueue: utiltesting.MakeLocalQueue("test-queue", "default").
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
			clusterQueue: utiltesting.MakeClusterQueue("cq").
				Active(metav1.ConditionTrue).
				Obj(),
			localQueue: utiltesting.MakeLocalQueue("test-queue", "default").
				ClusterQueue("cq").
				Active(metav1.ConditionTrue).
				FairSharing(&kueue.FairSharing{
					Weight: ptr.To(resource.MustParse("1")),
				}).
				FairSharingStatus(
					&kueue.FairSharingStatus{
						AdmissionFairSharingStatus: &kueue.AdmissionFairSharingStatus{
							LastUpdate: metav1.NewTime(now.Add(-6 * time.Minute)),
							ConsumedResources: map[corev1.ResourceName]resource.Quantity{
								corev1.ResourceCPU: resource.MustParse("8")},
						},
					}).
				Obj(),
			wantLocalQueue: utiltesting.MakeLocalQueue("test-queue", "default").
				ClusterQueue("cq").
				Active(metav1.ConditionTrue).
				FairSharing(&kueue.FairSharing{
					Weight: ptr.To(resource.MustParse("1")),
				}).
				FairSharingStatus(
					&kueue.FairSharingStatus{
						AdmissionFairSharingStatus: &kueue.AdmissionFairSharingStatus{
							ConsumedResources: map[corev1.ResourceName]resource.Quantity{
								corev1.ResourceCPU: resource.MustParse("4"),
							},
						},
					}).
				Obj(),
			fsConfig: &config.FairSharing{
				Enable: true,
				Modes:  []config.FairSharingMode{config.AdmissionTimeMode},
				AdmissionFairSharing: &config.AdmissionFairSharing{
					UsageHalfDecayTime:     metav1.Duration{Duration: 5 * time.Minute},
					UsageSamplingFrequency: metav1.Duration{Duration: 5 * time.Minute},
				},
			},
		},
		"local queue decaying usage sums the previous state and running workloads with 50/50 ratio": {
			clusterQueue: utiltesting.MakeClusterQueue("cq").
				Active(metav1.ConditionTrue).
				Obj(),
			localQueue: utiltesting.MakeLocalQueue("lq", "default").
				ClusterQueue("cq").
				Active(metav1.ConditionTrue).
				FairSharing(&kueue.FairSharing{
					Weight: ptr.To(resource.MustParse("1")),
				}).
				FairSharingStatus(
					&kueue.FairSharingStatus{
						AdmissionFairSharingStatus: &kueue.AdmissionFairSharingStatus{
							LastUpdate: metav1.NewTime(now.Add(-6 * time.Minute)),
							ConsumedResources: map[corev1.ResourceName]resource.Quantity{
								corev1.ResourceCPU: resource.MustParse("8000")},
						},
					}).
				Obj(),
			wantLocalQueue: utiltesting.MakeLocalQueue("lq", "default").
				ClusterQueue("cq").
				Active(metav1.ConditionTrue).
				ReservingWorkloads(1).
				AdmittedWorkloads(1).
				FairSharing(&kueue.FairSharing{
					Weight: ptr.To(resource.MustParse("1")),
				}).
				FairSharingStatus(
					&kueue.FairSharingStatus{
						AdmissionFairSharingStatus: &kueue.AdmissionFairSharingStatus{
							ConsumedResources: map[corev1.ResourceName]resource.Quantity{
								corev1.ResourceCPU: resource.MustParse("6000"),
							},
						},
					}).
				Obj(),
			runningWls: []kueue.Workload{
				*utiltesting.MakeWorkload("wl", "default").
					Queue("lq").
					Request(corev1.ResourceCPU, "4").
					SimpleReserveQuota("cq", "rf", now).
					Admitted(true).
					Obj(),
			},
			fsConfig: &config.FairSharing{
				Enable: true,
				Modes:  []config.FairSharingMode{config.AdmissionTimeMode},
				AdmissionFairSharing: &config.AdmissionFairSharing{
					UsageHalfDecayTime:     metav1.Duration{Duration: 5 * time.Minute},
					UsageSamplingFrequency: metav1.Duration{Duration: 5 * time.Minute},
				},
			},
		},
		"local queue decaying usage sums the previous state and running workloads with 75/25 ratio": {
			clusterQueue: utiltesting.MakeClusterQueue("cq").
				Active(metav1.ConditionTrue).
				Obj(),
			localQueue: utiltesting.MakeLocalQueue("lq", "default").
				ClusterQueue("cq").
				Active(metav1.ConditionTrue).
				FairSharing(&kueue.FairSharing{
					Weight: ptr.To(resource.MustParse("1")),
				}).
				FairSharingStatus(
					&kueue.FairSharingStatus{
						AdmissionFairSharingStatus: &kueue.AdmissionFairSharingStatus{
							LastUpdate: metav1.NewTime(now.Add(-6 * time.Minute)),
							ConsumedResources: map[corev1.ResourceName]resource.Quantity{
								corev1.ResourceCPU: resource.MustParse("8000")},
						},
					}).
				Obj(),
			wantLocalQueue: utiltesting.MakeLocalQueue("lq", "default").
				ClusterQueue("cq").
				Active(metav1.ConditionTrue).
				FairSharing(&kueue.FairSharing{
					Weight: ptr.To(resource.MustParse("1")),
				}).
				ReservingWorkloads(1).
				AdmittedWorkloads(1).
				FairSharingStatus(
					&kueue.FairSharingStatus{
						AdmissionFairSharingStatus: &kueue.AdmissionFairSharingStatus{
							ConsumedResources: map[corev1.ResourceName]resource.Quantity{
								corev1.ResourceCPU: resource.MustParse("7000"),
							},
						},
					}).
				Obj(),
			runningWls: []kueue.Workload{
				*utiltesting.MakeWorkload("wl", "default").
					Queue("lq").
					Request(corev1.ResourceCPU, "4").
					SimpleReserveQuota("cq", "rf", now).
					Admitted(true).
					Obj(),
			},
			fsConfig: &config.FairSharing{
				Enable: true,
				Modes:  []config.FairSharingMode{config.AdmissionTimeMode},
				AdmissionFairSharing: &config.AdmissionFairSharing{
					UsageHalfDecayTime:     metav1.Duration{Duration: 10 * time.Minute},
					UsageSamplingFrequency: metav1.Duration{Duration: 5 * time.Minute},
				},
			},
		},
		"local queue decaying usage is not reconciled if not enough time has passed": {
			clusterQueue: utiltesting.MakeClusterQueue("cq").
				Active(metav1.ConditionTrue).
				Obj(),
			localQueue: utiltesting.MakeLocalQueue("test-queue", "default").
				ClusterQueue("cq").
				Active(metav1.ConditionTrue).
				FairSharing(&kueue.FairSharing{
					Weight: ptr.To(resource.MustParse("1")),
				}).
				FairSharingStatus(
					&kueue.FairSharingStatus{
						AdmissionFairSharingStatus: &kueue.AdmissionFairSharingStatus{
							LastUpdate: metav1.NewTime(now.Add(-4 * time.Minute)),
							ConsumedResources: map[corev1.ResourceName]resource.Quantity{
								corev1.ResourceCPU: resource.MustParse("8")},
						},
					}).
				Obj(),
			wantLocalQueue: utiltesting.MakeLocalQueue("test-queue", "default").
				ClusterQueue("cq").
				Active(metav1.ConditionTrue).
				FairSharing(&kueue.FairSharing{
					Weight: ptr.To(resource.MustParse("1")),
				}).
				FairSharingStatus(
					&kueue.FairSharingStatus{
						AdmissionFairSharingStatus: &kueue.AdmissionFairSharingStatus{
							LastUpdate: metav1.NewTime(now.Add(-4 * time.Minute)),
							ConsumedResources: map[corev1.ResourceName]resource.Quantity{
								corev1.ResourceCPU: resource.MustParse("8")},
						},
					}).
				Obj(),
			wantError: nil,
			fsConfig: &config.FairSharing{
				Enable: true,
				Modes:  []config.FairSharingMode{config.AdmissionTimeMode},
				AdmissionFairSharing: &config.AdmissionFairSharing{
					UsageHalfDecayTime:     metav1.Duration{Duration: 5 * time.Minute},
					UsageSamplingFrequency: metav1.Duration{Duration: 5 * time.Minute},
				},
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

			ctxWithLogger, _ := utiltesting.ContextWithLog(t)
			cqCache := cache.New(cl)
			if err := cqCache.AddClusterQueue(ctxWithLogger, tc.clusterQueue); err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}
			_ = cqCache.AddLocalQueue(tc.localQueue)
			for _, wl := range tc.runningWls {
				cqCache.AddOrUpdateWorkload(&wl)
			}
			qManager := queue.NewManager(cl, cqCache)
			if err := qManager.AddClusterQueue(ctxWithLogger, tc.clusterQueue); err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}
			_ = qManager.AddLocalQueue(ctxWithLogger, tc.localQueue)
			reconciler := NewLocalQueueReconciler(cl, qManager, cqCache,
				WithClock(testingclock.NewFakeClock(now)),
				WithFairSharingConfig(tc.fsConfig))

			ctx, ctxCancel := context.WithCancel(ctxWithLogger)
			defer ctxCancel()

			_, gotError := reconciler.Reconcile(
				ctx,
				reconcile.Request{NamespacedName: client.ObjectKeyFromObject(tc.localQueue)},
			)

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
				cmpopts.IgnoreFields(kueue.AdmissionFairSharingStatus{}, "LastUpdate"),
			}
			if diff := cmp.Diff(tc.wantLocalQueue, gotLocalQueue, cmpOpts...); diff != "" {
				t.Errorf("Workloads after reconcile (-want,+got):\n%s", diff)
			}
		})
	}
}
