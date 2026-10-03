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

package statefulset

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	statefulsettesting "sigs.k8s.io/kueue/pkg/util/testingjobs/statefulset"
)

func TestPutWorkloadOnHold(t *testing.T) {
	now := time.Now()

	cases := map[string]struct {
		workload          *kueue.Workload
		wantQuotaReserved *metav1.Condition
		wantAdmissionNil  bool
	}{
		"releases admitted workload": {
			workload: utiltestingapi.MakeWorkload(GetWorkloadName("sts-uid", "sts"), "ns").
				Queue("lq").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).
				AdmittedAt(true, now).
				Obj(),
			wantQuotaReserved: &metav1.Condition{
				Status: metav1.ConditionFalse,
				Reason: kueue.WorkloadOnHold,
			},
			wantAdmissionNil: true,
		},
		"puts workload without active reservation on hold": {
			workload: utiltestingapi.MakeWorkload(GetWorkloadName("sts-uid", "sts"), "ns").
				Queue("lq").
				Obj(),
			wantQuotaReserved: &metav1.Condition{
				Status: metav1.ConditionFalse,
				Reason: kueue.WorkloadOnHold,
			},
			wantAdmissionNil: true,
		},
		"ignores finished workload": {
			workload: utiltestingapi.MakeWorkload(GetWorkloadName("sts-uid", "sts"), "ns").
				Queue("lq").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).
				AdmittedAt(true, now).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadFinished,
					Status:  metav1.ConditionTrue,
					Reason:  "Succeeded",
					Message: "Job finished successfully",
				}).
				Obj(),
			wantQuotaReserved: &metav1.Condition{
				Status: metav1.ConditionTrue,
				Reason: "AdmittedByTest",
			},
			wantAdmissionNil: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			c := utiltesting.NewFakeClient(tc.workload.DeepCopy())
			r := &Reconciler{client: c}

			if err := r.putWorkloadOnHold(ctx, tc.workload); err != nil {
				t.Fatalf("putWorkloadOnHold() error = %v", err)
			}

			got := &kueue.Workload{}
			if err := c.Get(ctx, client.ObjectKeyFromObject(tc.workload), got); err != nil {
				t.Fatalf("failed to get workload: %v", err)
			}

			cond := apimeta.FindStatusCondition(got.Status.Conditions, kueue.WorkloadQuotaReserved)
			switch {
			case tc.wantQuotaReserved == nil && cond != nil:
				t.Fatalf("expected no QuotaReserved condition, got %+v", cond)
			case tc.wantQuotaReserved != nil && (cond == nil || cond.Status != tc.wantQuotaReserved.Status || cond.Reason != tc.wantQuotaReserved.Reason):
				t.Fatalf("unexpected quota reserved condition: %+v", cond)
			}
			if tc.wantAdmissionNil {
				if got.Status.Admission != nil {
					t.Fatalf("expected admission to be nil, got %+v", got.Status.Admission)
				}
			} else if got.Status.Admission == nil {
				t.Fatalf("expected admission to not be nil, but it was nil")
			}
		})
	}
}

func TestReconcilePodEventsSkipUnmanagedStatefulSets(t *testing.T) {
	cases := map[string]struct {
		externalFramework bool
		opts              []jobframework.Option
	}{
		"another framework": {externalFramework: true},
		"namespace selector does not match": {
			opts: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
				jobframework.WithManagedJobsNamespaceSelector(labels.SelectorFromSet(labels.Set{"managed": "true"})),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			sts := statefulsettesting.MakeStatefulSet("sts", "ns").UID("sts-uid").Replicas(1).Obj()
			if tc.externalFramework {
				sts.Labels = map[string]string{"kueue.x-k8s.io/queue-name": "lq"}
				sts.Spec.Template.Annotations = map[string]string{podconstants.SuspendedByParentAnnotation: "other-framework"}
			}
			pod := testingpod.MakePod("pod", "ns").OwnerReferenceWithUID(sts.Name, gvk, string(sts.UID)).Obj()
			ns := &corev1.Namespace{Name: "ns", Labels: map[string]string{"managed": "false"}}
			builder := utiltesting.NewClientBuilder().WithObjects(sts, pod, ns)
			indexer := utiltesting.AsIndexer(builder)
			if err := SetupIndexes(ctx, indexer); err != nil {
				t.Fatalf("SetupIndexes() error = %v", err)
			}
			cl := builder.Build()
			r, err := NewReconciler(ctx, cl, indexer, &utiltesting.EventRecorder{}, tc.opts...)
			if err != nil {
				t.Fatalf("NewReconciler() error = %v", err)
			}
			if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(sts)}); err != nil {
				t.Fatalf("Reconcile() error = %v", err)
			}
			gotPod := &corev1.Pod{}
			if err := cl.Get(ctx, client.ObjectKeyFromObject(pod), gotPod); err != nil {
				t.Fatalf("Get Pod: %v", err)
			}
			if gotPod.Labels[constants.ManagedByKueueLabelKey] != "" {
				t.Errorf("unmanaged Pod gained Kueue managed label: %v", gotPod.Labels)
			}
			gotWorkload := &kueue.Workload{}
			err = cl.Get(ctx, client.ObjectKey{Namespace: sts.Namespace, Name: GetWorkloadName(sts.UID, sts.Name)}, gotWorkload)
			if !apierrors.IsNotFound(err) {
				t.Errorf("Get Workload error = %v, want NotFound", err)
			}
		})
	}
}

func TestScaleDownWaitsForActiveOwnedPods(t *testing.T) {
	cases := map[string]struct {
		hasPod             bool
		phase              corev1.PodPhase
		ownerUID           string
		deletionAge        time.Duration
		graceSeconds       int64
		fastQuotaRelease   bool
		unannotated        bool
		unreservedWorkload bool
		wantWaitingForPods bool
	}{
		"no pods": {},
		"running pod retains quota": {
			hasPod: true, phase: corev1.PodRunning, ownerUID: "sts-uid", wantWaitingForPods: true,
		},
		"succeeded pod releases quota": {
			hasPod: true, phase: corev1.PodSucceeded, ownerUID: "sts-uid",
		},
		"replacement owner UID does not retain quota": {
			hasPod: true, phase: corev1.PodRunning, ownerUID: "old-sts-uid",
		},
		"terminating pod retains quota during grace period": {
			hasPod: true, phase: corev1.PodRunning, ownerUID: "sts-uid",
			deletionAge: time.Second, graceSeconds: 120, wantWaitingForPods: true,
		},
		"unannotated terminating pod uses deadline recheck": {
			hasPod: true, phase: corev1.PodRunning, ownerUID: "sts-uid",
			deletionAge: time.Second, graceSeconds: 120, unannotated: true, wantWaitingForPods: true,
		},
		"fast quota release ignores terminating pod": {
			hasPod: true, phase: corev1.PodRunning, ownerUID: "sts-uid",
			deletionAge: time.Second, graceSeconds: 120, fastQuotaRelease: true,
		},
		"expired grace period releases quota": {
			hasPod: true, phase: corev1.PodRunning, ownerUID: "sts-uid",
			deletionAge: 3 * time.Minute, graceSeconds: 30,
		},
		"unreserved workload converges to OnHold": {
			unreservedWorkload: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			now := time.Now()
			features.SetFeatureGateDuringTest(t, features.FastQuotaReleaseInPodIntegration, tc.fastQuotaRelease)
			ctx := t.Context()
			sts := statefulsettesting.MakeStatefulSet("sts", "ns").UID("sts-uid").Replicas(0).Obj()
			wlBuilder := utiltestingapi.MakeWorkload(GetWorkloadName(sts.UID, sts.Name), sts.Namespace).
				Queue("lq").OwnerReference(gvk, sts.Name, string(sts.UID))
			if !tc.unreservedWorkload {
				wlBuilder.ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).AdmittedAt(true, now)
			} else {
				wlBuilder.Condition(metav1.Condition{
					Type: kueue.WorkloadQuotaReserved, Status: metav1.ConditionFalse,
					Reason: kueue.WorkloadQuotaReservedReasonPendingEvaluation,
				})
			}
			wl := wlBuilder.Obj()
			objects := []client.Object{sts, wl}
			if tc.hasPod {
				pod := testingpod.MakePod("pod", sts.Namespace).
					OwnerReferenceWithUID(sts.Name, gvk, tc.ownerUID).
					StatusPhase(tc.phase).Obj()
				if !tc.unannotated {
					pod.Annotations = map[string]string{podconstants.SuspendedByParentAnnotation: FrameworkName}
				}
				if tc.deletionAge > 0 {
					deletionTimestamp := metav1.NewTime(now.Add(time.Duration(tc.graceSeconds)*time.Second - tc.deletionAge))
					pod.DeletionTimestamp = &deletionTimestamp
					pod.DeletionGracePeriodSeconds = &tc.graceSeconds
					pod.Finalizers = []string{"test-finalizer"}
				}
				objects = append(objects, pod)
			}
			builder := utiltesting.NewClientBuilder().WithObjects(objects...).WithStatusSubresource(wl)
			if err := SetupIndexes(ctx, utiltesting.AsIndexer(builder)); err != nil {
				t.Fatalf("SetupIndexes() error = %v", err)
			}
			r := &Reconciler{client: builder.Build()}
			recheckAfter, err := r.reconcileWorkload(ctx, sts, wl)
			if err != nil {
				t.Fatalf("reconcileWorkload() error = %v", err)
			}
			wantRecheck := tc.wantWaitingForPods && tc.deletionAge > 0
			if (recheckAfter > 0) != wantRecheck {
				t.Errorf("recheckAfter = %v, want recheck = %v", recheckAfter, wantRecheck)
			}
			got := &kueue.Workload{}
			if err := r.client.Get(ctx, client.ObjectKeyFromObject(wl), got); err != nil {
				t.Fatalf("Get Workload: %v", err)
			}
			if tc.wantWaitingForPods {
				if got.Status.Admission == nil {
					t.Error("active Pod lost its quota reservation")
				}
				beforeReconcile := time.Now()
				result, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(sts)})
				afterReconcile := time.Now()
				if err != nil {
					t.Fatalf("Reconcile() error = %v", err)
				}
				if (result.RequeueAfter > 0) != wantRecheck {
					t.Errorf("RequeueAfter = %v, want recheck = %v", result.RequeueAfter, wantRecheck)
				}
				if wantRecheck {
					deadline := now.Add(2*time.Duration(tc.graceSeconds)*time.Second - tc.deletionAge)
					// Fake clients may round metav1 timestamps to whole seconds.
					if result.RequeueAfter < deadline.Sub(afterReconcile)-time.Second || result.RequeueAfter > deadline.Sub(beforeReconcile)+time.Nanosecond {
						t.Errorf("RequeueAfter = %v, want within [%v, %v]", result.RequeueAfter,
							deadline.Sub(afterReconcile)-time.Second, deadline.Sub(beforeReconcile)+time.Nanosecond)
					}
				}
				return
			}
			if got.Status.Admission != nil {
				t.Error("inactive or absent Pods did not release quota")
			}
			cond := apimeta.FindStatusCondition(got.Status.Conditions, kueue.WorkloadQuotaReserved)
			if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != kueue.WorkloadOnHold {
				t.Errorf("QuotaReserved condition = %+v, want False/OnHold", cond)
			}
		})
	}
}

func TestHasActiveOwnedPods(t *testing.T) {
	now := time.Date(2026, time.September, 23, 12, 0, 0, 0, time.UTC)
	runningPod := func(name, ownerUID string) corev1.Pod {
		return *testingpod.MakePod(name, "ns").
			OwnerReferenceWithUID("sts", gvk, ownerUID).
			Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
			StatusPhase(corev1.PodRunning).Obj()
	}
	deletingPod := func(name string, deletionTimestamp time.Time, graceSeconds int64) corev1.Pod {
		pod := runningPod(name, "sts-uid")
		stamp := metav1.NewTime(deletionTimestamp)
		pod.DeletionTimestamp = &stamp
		pod.DeletionGracePeriodSeconds = &graceSeconds
		pod.Finalizers = []string{"test-finalizer"}
		return pod
	}
	withoutGrace := deletingPod("without-grace", now.Add(time.Minute), 30)
	withoutGrace.DeletionGracePeriodSeconds = nil
	unannotated := deletingPod("unannotated", now.Add(time.Minute), 50)
	delete(unannotated.Annotations, podconstants.SuspendedByParentAnnotation)
	annotatedForAnotherIntegration := deletingPod("other-integration", now.Add(time.Minute), 50)
	annotatedForAnotherIntegration.Annotations[podconstants.SuspendedByParentAnnotation] = "other-integration"
	finishedPod := runningPod("finished", "sts-uid")
	finishedPod.Status.Phase = corev1.PodSucceeded

	cases := map[string]struct {
		pods             []corev1.Pod
		fastQuotaRelease bool
		wantActive       bool
		wantRecheckAfter time.Duration
	}{
		"no pods are inactive": {},
		"running pod without deletion timestamp is active": {
			pods:       []corev1.Pod{runningPod("running", "sts-uid")},
			wantActive: true,
		},
		"terminating pod is active before its activity deadline": {
			pods:             []corev1.Pod{deletingPod("deleting", now.Add(30*time.Second), 50)},
			wantActive:       true,
			wantRecheckAfter: 80*time.Second + time.Nanosecond,
		},
		"unannotated terminating pod is still active": {
			pods:             []corev1.Pod{unannotated},
			wantActive:       true,
			wantRecheckAfter: 110*time.Second + time.Nanosecond,
		},
		"pod annotated for another integration is still active": {
			pods:             []corev1.Pod{annotatedForAnotherIntegration},
			wantActive:       true,
			wantRecheckAfter: 110*time.Second + time.Nanosecond,
		},
		"unannotated pod retains quota alongside an earlier deadline": {
			pods: []corev1.Pod{
				deletingPod("annotated", now.Add(-time.Second), 50),
				unannotated,
			},
			wantActive:       true,
			wantRecheckAfter: 110*time.Second + time.Nanosecond,
		},
		"multiple terminating pods recheck after the last deadline": {
			pods: []corev1.Pod{
				deletingPod("first", now.Add(-time.Second), 50),
				deletingPod("last", now.Add(30*time.Second), 80),
			},
			wantActive:       true,
			wantRecheckAfter: 110*time.Second + time.Nanosecond,
		},
		"running pod without deadline remains active": {
			pods: []corev1.Pod{
				deletingPod("deleting", now.Add(-time.Second), 50),
				runningPod("running", "sts-uid"),
			},
			wantActive: true,
		},
		"missing grace period remains active": {
			pods:       []corev1.Pod{withoutGrace},
			wantActive: true,
		},
		"activity deadline is inclusive": {
			pods:             []corev1.Pod{deletingPod("at-deadline", now.Add(-30*time.Second), 30)},
			wantActive:       true,
			wantRecheckAfter: time.Nanosecond,
		},
		"past deletion timestamp remains active during existing grace semantics": {
			pods:             []corev1.Pod{deletingPod("past-timestamp", now.Add(-10*time.Second), 90)},
			wantActive:       true,
			wantRecheckAfter: 80*time.Second + time.Nanosecond,
		},
		"expired deletion deadline is inactive": {
			pods: []corev1.Pod{deletingPod("expired", now.Add(-time.Minute), 30)},
		},
		"finished pod is inactive": {
			pods: []corev1.Pod{finishedPod},
		},
		"different StatefulSet UID is ignored": {
			pods: []corev1.Pod{runningPod("old-owner", "old-sts-uid")},
		},
		"fast quota release ignores terminating pod": {
			pods:             []corev1.Pod{deletingPod("deleting", now.Add(30*time.Second), 50)},
			fastQuotaRelease: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.FastQuotaReleaseInPodIntegration, tc.fastQuotaRelease)
			ctx := t.Context()
			sts := statefulsettesting.MakeStatefulSet("sts", "ns").UID("sts-uid").Replicas(0).Obj()
			objects := []client.Object{sts}
			for i := range tc.pods {
				objects = append(objects, &tc.pods[i])
			}
			builder := utiltesting.NewClientBuilder().WithObjects(objects...)
			if err := SetupIndexes(ctx, utiltesting.AsIndexer(builder)); err != nil {
				t.Fatalf("SetupIndexes() error = %v", err)
			}
			r := &Reconciler{client: builder.Build()}
			got, recheckAfter, err := r.hasActiveOwnedPods(ctx, sts, now)
			if err != nil {
				t.Fatalf("hasActiveOwnedPods() error = %v", err)
			}
			if got != tc.wantActive {
				t.Errorf("hasActiveOwnedPods() = %v, want %v", got, tc.wantActive)
			}
			if recheckAfter != tc.wantRecheckAfter {
				t.Errorf("recheckAfter = %v, want %v", recheckAfter, tc.wantRecheckAfter)
			}
		})
	}
}

func TestPodHandlerUpdateWakesOwnedStatefulSets(t *testing.T) {
	makePod := func(annotation, ownerName string) *corev1.Pod {
		if ownerName == "" {
			ownerName = "sts"
		}
		pod := testingpod.MakePod("pod", "ns").
			OwnerReferenceWithUID(ownerName, gvk, "sts-uid").Obj()
		if annotation != "" {
			pod.Annotations = map[string]string{podconstants.SuspendedByParentAnnotation: annotation}
		}
		return pod
	}
	cases := map[string]struct {
		oldAnnotation string
		newAnnotation string
		newOwnerName  string
		wantNames     []string
	}{
		"removed annotation still enqueues": {
			oldAnnotation: FrameworkName, wantNames: []string{"sts"},
		},
		"changed annotation still enqueues": {
			oldAnnotation: FrameworkName, newAnnotation: "other-integration", wantNames: []string{"sts"},
		},
		"unchanged annotation enqueues once": {
			oldAnnotation: FrameworkName, newAnnotation: FrameworkName, wantNames: []string{"sts"},
		},
		"added annotation enqueues once": {
			newAnnotation: FrameworkName, wantNames: []string{"sts"},
		},
		"changed controller enqueues both StatefulSets": {
			oldAnnotation: FrameworkName, newAnnotation: FrameworkName,
			newOwnerName: "replacement-sts", wantNames: []string{"replacement-sts", "sts"},
		},
		"unannotated update enqueues its owner": {
			wantNames: []string{"sts"},
		},
		"other integration annotation enqueues its owner": {
			oldAnnotation: "other-integration", newAnnotation: "other-integration", wantNames: []string{"sts"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			q := &recordingPodQueue{TypedRateLimitingInterface: workqueue.NewTypedRateLimitingQueue(
				workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())}
			defer q.ShutDown()
			(&podHandler{}).Update(t.Context(), event.UpdateEvent{
				ObjectOld: makePod(tc.oldAnnotation, ""),
				ObjectNew: makePod(tc.newAnnotation, tc.newOwnerName),
			}, q)
			if len(q.added) != len(tc.wantNames) {
				t.Fatalf("queued %d requests, want %d", len(q.added), len(tc.wantNames))
			}
			for i, req := range q.added {
				if req.Namespace != "ns" || req.Name != tc.wantNames[i] {
					t.Errorf("queued %s, want ns/%s", req.NamespacedName, tc.wantNames[i])
				}
			}
		})
	}
}

func TestPodHandlerCreateAndDeleteWakeUnannotatedOwner(t *testing.T) {
	pod := testingpod.MakePod("pod", "ns").OwnerReferenceWithUID("sts", gvk, "sts-uid").Obj()
	for name, handle := range map[string]func(*podHandler, workqueue.TypedRateLimitingInterface[reconcile.Request]){
		"create": func(h *podHandler, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			h.Create(t.Context(), event.CreateEvent{Object: pod}, q)
		},
		"delete": func(h *podHandler, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			h.Delete(t.Context(), event.DeleteEvent{Object: pod}, q)
		},
	} {
		t.Run(name, func(t *testing.T) {
			q := &recordingPodQueue{TypedRateLimitingInterface: workqueue.NewTypedRateLimitingQueue(
				workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())}
			defer q.ShutDown()
			handle(&podHandler{}, q)
			if len(q.added) != 1 || q.added[0].Namespace != "ns" || q.added[0].Name != "sts" {
				t.Errorf("queued requests = %+v, want ns/sts", q.added)
			}
		})
	}
}

type recordingPodQueue struct {
	workqueue.TypedRateLimitingInterface[reconcile.Request]
	added []reconcile.Request
}

func (q *recordingPodQueue) AddAfter(req reconcile.Request, _ time.Duration) {
	q.added = append(q.added, req)
}
