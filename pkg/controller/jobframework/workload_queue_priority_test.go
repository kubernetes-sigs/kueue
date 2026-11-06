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

package jobframework_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/component-base/featuregate"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

type legacyQueueChangeJob struct {
	jobframework.GenericJob
	skip bool
}

// CustomQueueNameChange models an existing integration that owns its queue write
// and can decline it, without opting into combined queue and priority updates.
func (j *legacyQueueChangeJob) CustomQueueNameChange(ctx context.Context, c client.Client, wl *kueue.Workload) error {
	if j.skip {
		return nil
	}
	return jobframework.QueueNameChange(ctx, c, j, wl)
}

type preparedQueueChangeJob struct {
	*legacyQueueChangeJob
}

// PrepareQueueNameChange mirrors the legacy handler's skip policy in memory,
// allowing the test integration to opt into a combined write.
func (j *preparedQueueChangeJob) PrepareQueueNameChange(wl *kueue.Workload) bool {
	if j.skip || wl.Spec.QueueName == jobframework.QueueName(j) {
		return false
	}
	wl.Spec.QueueName = jobframework.QueueName(j)
	return true
}

// TestReconcileGenericJobQueueAndPriority checks persisted states and write
// boundaries, including admission interleaving and multi-slice retry ordering.
func TestReconcileGenericJobQueueAndPriority(t *testing.T) {
	cases := map[string]struct {
		queue                  kueue.LocalQueueName
		class                  string
		reserveAfterSplitWrite bool
		running                bool
		reserved               bool
		admitted               bool
		evicted                bool
		slicing                bool
		retainedSlice          bool
		retainedCurrentClass   bool
		customHandler          string
		conflict               bool
		wantQueue              kueue.LocalQueueName
		wantClass              string
		wantErr                func(error) bool
		wantWrites             int
	}{
		"simultaneous queue and priority changes are written together": {
			queue: "new-queue", class: "high", wantWrites: 1,
		},
		"admission cannot interleave queue and priority changes": {
			queue: "new-queue", class: "high", reserveAfterSplitWrite: true, wantWrites: 1,
		},
		"queue only": {
			queue: "new-queue", class: "low", wantWrites: 1,
		},
		"priority only": {
			queue: "old-queue", class: "high", wantWrites: 1,
		},
		"unchanged": {
			queue: "old-queue", class: "low",
		},
		"custom handler can prepare the same write": {
			queue: "new-queue", class: "high", customHandler: "prepared", wantWrites: 1,
		},
		"custom handler can decline the queue change": {
			queue: "new-queue", class: "high", customHandler: "skip", wantQueue: "old-queue", wantWrites: 1,
		},
		"legacy custom handler retains its separate write": {
			queue: "new-queue", class: "high", customHandler: "legacy", wantWrites: 2,
		},
		"conflict persists neither change": {
			queue: "new-queue", class: "high", conflict: true,
			wantQueue: "old-queue", wantClass: "low", wantErr: apierrors.IsConflict,
		},
		"missing priority class persists neither change": {
			queue: "new-queue", class: "missing",
			wantQueue: "old-queue", wantClass: "low", wantErr: apierrors.IsNotFound,
		},
		"running job does not move queues": {
			queue: "new-queue", class: "high", running: true,
			wantQueue: "old-queue", wantWrites: 1,
		},
		"admitted suspended job does not move queues": {
			queue: "new-queue", class: "high", reserved: true, admitted: true,
			wantQueue: "old-queue", wantWrites: 1,
		},
		"illegal reserved queue change does not block legal priority change": {
			queue: "new-queue", class: "high", reserved: true,
			wantQueue: "old-queue", wantWrites: 1, wantErr: apierrors.IsInvalid,
		},
		"preempted workload without quota moves queue with priority": {
			queue: "new-queue", class: "high", evicted: true, wantWrites: 1,
		},
		"preempted workload that holds quota keeps its queue": {
			queue: "new-queue", class: "high", evicted: true, reserved: true,
			wantQueue: "old-queue", wantWrites: 1,
		},
		"compatible workload slice co-writes queue and priority": {
			queue: "new-queue", class: "high", slicing: true, wantWrites: 1,
		},
		"pending and retained slices share priority but only pending slice moves queue": {
			queue: "new-queue", class: "high", slicing: true, retainedSlice: true, wantWrites: 2,
		},
		"stale retained slice is repaired before pending slice transitions": {
			queue: "new-queue", class: "high", slicing: true, retainedSlice: true,
			retainedCurrentClass: true, wantWrites: 2,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
				features.TopologyAwareScheduling:      false,
				features.ElasticJobsViaWorkloadSlices: tc.slicing,
			})
			jobWrapper := testingjob.MakeJob("job", "ns").UID("job-uid").
				Queue(tc.queue).WorkloadPriorityClass(tc.class).Suspend(!tc.running)
			if tc.slicing {
				jobWrapper.SetAnnotation(constants.ElasticJobAnnotation, "true")
			}
			obj := jobWrapper.Obj()
			var genericJob jobframework.GenericJob = (*job.Job)(obj)
			podSets, err := genericJob.PodSets(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			wlWrapper := utiltestingapi.MakeWorkload("workload", "ns").
				Finalizers(kueue.ResourceInUseFinalizerName).
				Label(controllerconstants.JobUIDLabel, string(obj.UID)).
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), obj.Name, string(obj.UID)).
				Queue("old-queue").WorkloadPriorityClassRef("low").Priority(10).
				PodSets(podSets...)
			if tc.reserved {
				wlWrapper.ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Count(1).Obj()).Obj(), time.Now())
			}
			if tc.admitted {
				wlWrapper.AdmittedAt(true, time.Now())
			}
			if tc.evicted {
				wlWrapper.Condition(metav1.Condition{
					Type: kueue.WorkloadEvicted, Status: metav1.ConditionTrue,
					Reason: kueue.WorkloadEvictedByPreemption,
				})
				if !tc.reserved {
					wlWrapper.Condition(metav1.Condition{
						Type: kueue.WorkloadQuotaReserved, Status: metav1.ConditionFalse,
						Reason: kueue.WorkloadQuotaReservedReasonPendingEvaluation,
					}).Condition(metav1.Condition{
						Type: kueue.WorkloadRequeued, Status: metav1.ConditionTrue,
						Reason: kueue.WorkloadEvictedByPreemption,
					})
				}
			}
			wl := wlWrapper.Obj()
			var retained *kueue.Workload
			if tc.retainedSlice {
				retainedWrapper := wlWrapper.Clone().Name("retained").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), time.Now())
				if tc.retainedCurrentClass {
					retainedWrapper.WorkloadPriorityClassRef("high")
				}
				retained = retainedWrapper.Obj()
				wlWrapper.Annotation(workloadslicing.WorkloadSliceReplacementFor, "ns/retained")
			}
			if tc.customHandler != "" {
				legacy := &legacyQueueChangeJob{GenericJob: genericJob, skip: tc.customHandler == "skip"}
				genericJob = legacy
				if tc.customHandler != "legacy" {
					genericJob = &preparedQueueChangeJob{legacyQueueChangeJob: legacy}
				}
			}
			var writes []kueue.WorkloadSpec
			var writeNames []string
			classReads := 0
			builder := utiltesting.NewClientBuilder().
				WithObjects(utiltesting.MakeNamespace("ns"), obj, wl,
					utiltestingapi.MakeWorkloadPriorityClass("low").PriorityValue(10).Obj(),
					utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(100).Obj()).
				WithStatusSubresource(wl).
				WithIndex(&kueue.Workload{}, indexer.OwnerReferenceIndexKey(genericJob.GVK()), indexer.WorkloadOwnerIndexFunc(genericJob.GVK())).
				WithInterceptorFuncs(interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if _, ok := obj.(*kueue.WorkloadPriorityClass); ok {
							classReads++
						}
						return c.Get(ctx, key, obj, opts...)
					},
					Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
						if updated, ok := obj.(*kueue.Workload); ok {
							if tc.conflict {
								return apierrors.NewConflict(kueue.Resource("workloads"), updated.Name, errors.New("test conflict"))
							}
							stored := &kueue.Workload{}
							if err := c.Get(ctx, client.ObjectKeyFromObject(updated), stored); err != nil {
								return err
							}
							if workload.HasQuotaReservation(stored) && stored.Spec.QueueName != updated.Spec.QueueName {
								return apierrors.NewInvalid(schema.GroupKind{Group: kueue.SchemeGroupVersion.Group, Kind: "Workload"}, updated.Name,
									field.ErrorList{field.Forbidden(field.NewPath("spec", "queueName"), "queueName is immutable while workload quota reserved")})
							}
						}
						if err := c.Update(ctx, obj, opts...); err != nil {
							return err
						}
						updated, ok := obj.(*kueue.Workload)
						if !ok {
							return nil
						}
						writes = append(writes, *updated.Spec.DeepCopy())
						writeNames = append(writeNames, updated.Name)
						if tc.reserveAfterSplitWrite && updated.Spec.QueueName == "old-queue" && updated.Spec.PriorityClassRef.Name == "high" {
							// Model the scheduler reserving quota as soon as the
							// priority-only write becomes visible in the old queue.
							reserved := updated.DeepCopy()
							reserved.Status.Admission = &kueue.Admission{ClusterQueue: "old-cq"}
							reserved.Status.Conditions = []metav1.Condition{{
								Type: kueue.WorkloadQuotaReserved, Status: metav1.ConditionTrue,
								Reason: "ReservedByTest", LastTransitionTime: metav1.NewTime(time.Now()),
							}}
							return c.Status().Update(ctx, reserved)
						}
						return nil
					},
				})
			if retained != nil {
				builder.WithObjects(retained).WithStatusSubresource(retained)
			}
			cl := builder.Build()
			rec := jobframework.NewReconciler(cl, &utiltesting.EventRecorder{})
			_, err = rec.ReconcileGenericJob(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(obj)}, genericJob)
			if tc.wantErr != nil {
				if !tc.wantErr(err) {
					t.Fatalf("reconciling queue and priority: got %v, want the expected API error", err)
				}
			} else if err != nil {
				t.Fatalf("reconciling queue and priority: %v", err)
			}
			want := wl.Spec.DeepCopy()
			want.QueueName = tc.queue
			if tc.wantQueue != "" {
				want.QueueName = tc.wantQueue
			}
			wantClass := tc.class
			if tc.wantClass != "" {
				wantClass = tc.wantClass
			}
			if wantClass == "high" {
				want.PriorityClassRef.Name = "high"
				want.Priority = new(int32(100))
			}
			if len(writes) != tc.wantWrites {
				t.Errorf("workload writes = %d, want %d", len(writes), tc.wantWrites)
			}
			for i := range writes {
				if writeNames[i] == "retained" {
					retainedWant := retained.Spec.DeepCopy()
					retainedWant.PriorityClassRef.Name = "high"
					retainedWant.Priority = new(int32(100))
					if diff := cmp.Diff(*retainedWant, writes[i], cmpopts.EquateEmpty()); diff != "" {
						t.Errorf("retained slice write (-want,+got):\n%s", diff)
					}
					continue
				}
				if tc.customHandler == "legacy" && i == 0 {
					legacyWant := want.DeepCopy()
					legacyWant.QueueName = "old-queue"
					if diff := cmp.Diff(*legacyWant, writes[i], cmpopts.EquateEmpty()); diff != "" {
						t.Errorf("legacy priority write (-want,+got):\n%s", diff)
					}
					continue
				}
				if diff := cmp.Diff(*want, writes[i], cmpopts.EquateEmpty()); diff != "" {
					t.Errorf("write %d exposed an intermediate state (-want,+got):\n%s", i, diff)
				}
			}
			persisted := &kueue.Workload{}
			if err := cl.Get(ctx, types.NamespacedName{Name: wl.Name, Namespace: wl.Namespace}, persisted); err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(*want, persisted.Spec, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("persisted workload (-want,+got):\n%s", diff)
			}
			if retained != nil {
				persistedRetained := &kueue.Workload{}
				if err := cl.Get(ctx, client.ObjectKeyFromObject(retained), persistedRetained); err != nil {
					t.Fatal(err)
				}
				retainedWant := retained.Spec.DeepCopy()
				retainedWant.PriorityClassRef.Name = "high"
				retainedWant.Priority = new(int32(100))
				if diff := cmp.Diff(*retainedWant, persistedRetained.Spec, cmpopts.EquateEmpty()); diff != "" {
					t.Errorf("persisted retained slice (-want,+got):\n%s", diff)
				}
				if classReads != 1 {
					t.Errorf("class reads = %d, want 1 for both slices", classReads)
				}
				if tc.retainedCurrentClass && (len(writeNames) == 0 || writeNames[0] != "retained") {
					t.Errorf("write order = %v, want stale retained slice repaired first", writeNames)
				}
			}
		})
	}
}
