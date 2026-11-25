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

package multikueue

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	testingclock "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

func TestReconcileOversizedPartialAdmission(t *testing.T) {
	for name, tc := range map[string]struct {
		evicted, priorityChanged, tolerationAdded, boostChanged bool
	}{
		"preserves accounting without spec updates": {},
		"propagates a priority boost":               {boostChanged: true},
		"propagates manager eviction":               {evicted: true},
		"propagates priority":                       {priorityChanged: true},
		"propagates added toleration":               {tolerationAdded: true},
		"propagates priority and added toleration":  {priorityChanged: true, tolerationAdded: true},
	} {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlicesWithPartialReplicaScaleUp, true)
			features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlices, true)
			features.SetFeatureGateDuringTest(t, features.AllowAddingPodTemplateTolerations, true)
			ctx, _ := utiltesting.ContextWithLog(t)
			now := time.Now()
			local := utiltestingapi.MakeWorkload("scale", TestNamespace).
				Annotation(workloadslicing.EnabledAnnotationKey, "true").
				PodSets(*utiltestingapi.MakePodSet("workers", 6).
					Toleration(corev1.Toleration{Key: "old", Operator: corev1.TolerationOpExists}).Obj()).
				WorkloadPriorityClassRef("normal").Priority(1).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("manager").PodSets(utiltestingapi.MakePodSetAssignment("workers").Count(2).Obj()).Obj(), now).
				AdmittedAt(true, now).
				AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStateReady, LastTransitionTime: metav1.NewTime(now)}).
				ClusterName("worker1").Obj()
			local.Spec.PodSets[0].MinCount = new(int32(1))
			remote := local.DeepCopy()
			remote.Status.Admission.PodSetAssignments[0].Count = new(int32(6))
			remote.Status.AdmissionChecks = nil
			if tc.evicted {
				local.Status.Conditions = append(
					local.Status.Conditions,
					metav1.Condition{Type: kueue.WorkloadEvicted, Status: metav1.ConditionTrue, Reason: "Test", Message: "Evicted by test", LastTransitionTime: metav1.NewTime(now)},
				)
			}
			if tc.boostChanged {
				features.SetFeatureGateDuringTest(t, features.PriorityBoost, true)
				local.Spec.PriorityBoost = new(int32(200))
			}
			if tc.priorityChanged {
				local.Spec.Priority = new(int32(10))
			}
			if tc.tolerationAdded {
				local.Spec.PodSets[0].Template.Spec.Tolerations = append(local.Spec.PodSets[0].Template.Spec.Tolerations, corev1.Toleration{Key: "new", Operator: corev1.TolerationOpExists})
			}
			_, workerClient := reconcilePartialAdmissionGroup(ctx, t, local, remote)
			got := &kueue.Workload{}
			if err := workerClient.Get(ctx, client.ObjectKeyFromObject(remote), got); err != nil {
				t.Fatal(err)
			}
			if got.Spec.PodSets[0].Count != 6 || *got.Spec.PodSets[0].MinCount != 1 || *got.Status.Admission.PodSetAssignments[0].Count != 6 {
				t.Fatal("oversized remote accounting was changed before draining")
			}
			if diff := cmp.Diff(local.Spec.PriorityBoost, got.Spec.PriorityBoost); diff != "" {
				t.Errorf("priority boost not propagated (-want,+got): %s", diff)
			}
			if diff := cmp.Diff(local.Spec.Priority, got.Spec.Priority); diff != "" {
				t.Errorf("priority not propagated (-want,+got): %s", diff)
			}
			if diff := cmp.Diff(local.Spec.PodSets[0].Template.Spec.Tolerations, got.Spec.PodSets[0].Template.Spec.Tolerations); diff != "" {
				t.Errorf("tolerations not propagated (-want,+got): %s", diff)
			}
			if tc.evicted && !apimeta.IsStatusConditionTrue(got.Status.Conditions, kueue.WorkloadDeactivationTarget) {
				t.Error("manager eviction did not reach oversized remote admission")
			}
		})
	}
}

func reconcilePartialAdmissionGroup(ctx context.Context, t *testing.T, local, remote *kueue.Workload) (client.Client, client.Client) {
	t.Helper()
	managerClient := getClientBuilder(ctx).WithObjects(local).WithStatusSubresource(local).Build()
	workerClient := NewNeverCachingClient(getClientBuilder(ctx).WithObjects(remote).WithStatusSubresource(remote).Build())
	group := &wlGroup{
		local: local, localClient: managerClient,
		remotes:       map[string]*kueue.Workload{"worker1": remote},
		remoteClients: map[string]*remoteClient{"worker1": {client: workerClient, origin: defaultOrigin}},
		acName:        "ac1", jobAdapter: &deferredSyncStubAdapter{},
		controllerKey: types.NamespacedName{Name: "job1", Namespace: TestNamespace},
	}
	reconciler := &wlReconciler{
		client:            managerClient,
		clock:             testingclock.NewFakeClock(time.Now()),
		origin:            defaultOrigin,
		workerLostTimeout: defaultWorkerLostTimeout,
		recorder:          &utiltesting.EventRecorder{},
		dispatcherName:    config.MultiKueueDispatcherModeAllAtOnce,
	}
	if _, err := reconciler.reconcileGroup(ctx, group); err != nil {
		t.Fatal(err)
	}
	return managerClient, workerClient
}

func TestReconcileWorkerScaleDown(t *testing.T) {
	for name, tc := range map[string]struct{ enabled, selected, managerPending, workerPending, otherDrift, wantPreserved bool }{
		"selected worker scales below the manager minimum":     {enabled: true, selected: true, wantPreserved: true},
		"selected worker scales down before manager admission": {enabled: true, selected: true, managerPending: true, wantPreserved: true},
		"pending worker replacement has no admitted count":     {enabled: true, selected: true, workerPending: true},
		"different pod template also drifted":                  {enabled: true, selected: true, otherDrift: true},
		"worker was not selected":                              {enabled: true},
		"partial scale-up disabled":                            {selected: true},
	} {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlices, true)
			features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlicesWithPartialReplicaScaleUp, tc.enabled)
			ctx, _ := utiltesting.ContextWithLog(t)
			now := time.Now()
			local := utiltestingapi.MakeWorkload("scale", TestNamespace).
				Annotation(workloadslicing.EnabledAnnotationKey, "true").
				Annotation(workloadslicing.WorkloadSliceReplacementFor, TestNamespace+"/prev").
				PodSets(*utiltestingapi.MakePodSet("workers", 6).Obj()).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("manager").PodSets(utiltestingapi.MakePodSetAssignment("workers").Count(6).Obj()).Obj(), now).
				AdmittedAt(true, now).
				AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStateReady, LastTransitionTime: metav1.NewTime(now)}).
				ClusterName("worker1").Obj()
			local.Spec.PodSets[0].MinCount = new(int32(4))
			remote := local.DeepCopy()
			remote.Status.AdmissionChecks = nil
			remote.Spec.PodSets[0].Count = 2
			remote.Spec.PodSets[0].MinCount = new(int32(2))
			if tc.workerPending {
				apimeta.FindStatusCondition(remote.Status.Conditions, kueue.WorkloadAdmitted).Status = metav1.ConditionFalse
			}
			if tc.managerPending {
				apimeta.FindStatusCondition(local.Status.Conditions, kueue.WorkloadAdmitted).Status = metav1.ConditionFalse
			}
			if !tc.selected {
				local.Status.ClusterName = new("worker2")
			}
			if tc.otherDrift {
				remote.Spec.PodSets[0].Template.Spec.NodeSelector = map[string]string{"pool": "other"}
			}
			manager, worker := reconcilePartialAdmissionGroup(ctx, t, local, remote)
			key := client.ObjectKeyFromObject(local)
			stored := &kueue.Workload{}
			err := worker.Get(ctx, key, stored)
			if !tc.wantPreserved {
				if !apierrors.IsNotFound(err) {
					t.Fatalf("expected remote removal, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("reading selected worker after reconcile: %v", err)
			}
			if ps := stored.Spec.PodSets[0]; ps.Count != 2 || !ptr.Equal(ps.MinCount, new(int32(2))) {
				t.Errorf("worker count and minimum changed: %+v", ps)
			}
			if err := manager.Get(ctx, key, stored); err != nil {
				t.Fatal(err)
			}
			wantCount, wantMin := int32(2), int32(2)
			if tc.managerPending {
				wantCount, wantMin = 6, 4
			}
			if ps := stored.Spec.PodSets[0]; ps.Count != wantCount || !ptr.Equal(ps.MinCount, &wantMin) {
				t.Errorf("manager count and minimum = %d/%v, want %d/%d", ps.Count, ptr.Deref(ps.MinCount, -1), wantCount, wantMin)
			}
			if diff := cmp.Diff(new(int32(6)), stored.Status.Admission.PodSetAssignments[0].Count); diff != "" {
				t.Errorf("manager reservation changed (-want,+got): %s", diff)
			}
		})
	}
}

func TestCloneForCreateBoundsPartialScaleUp(t *testing.T) {
	for name, tc := range map[string]struct {
		enabled, elastic, partial, reclaim bool
		grant, want                        int32
	}{
		"manager admits fewer than requested": {true, true, true, false, 2, 2},
		"full grant":                          {true, true, true, false, 6, 6},
		"gate disabled":                       {false, true, true, false, 2, 6},
		"non elastic":                         {true, false, true, false, 2, 6},
		"atomic":                              {true, true, false, false, 2, 6},
		"reclaimed pods are not subtracted from the spec": {true, true, true, true, 6, 6},
	} {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlicesWithPartialReplicaScaleUp, tc.enabled)
			features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlices, true)
			ps := kueue.PodSet{Name: "workers", Count: 6}
			if tc.partial {
				ps.MinCount = new(int32(1))
			}
			local := utiltestingapi.MakeWorkload("scale", "ns").PodSets(kueue.PodSet{Name: "head", Count: 1}, ps).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("manager").PodSets(
					utiltestingapi.MakePodSetAssignment("head").Count(1).Obj(),
					utiltestingapi.MakePodSetAssignment("workers").Count(tc.grant).Obj(),
				).Obj(), time.Now()).Obj()
			if tc.elastic {
				local.Annotations = map[string]string{workloadslicing.EnabledAnnotationKey: "true"}
			}
			if tc.reclaim {
				local.Status.ReclaimablePods = []kueue.ReclaimablePod{{Name: "workers", Count: 1}}
			}
			before := local.DeepCopy()
			remote := cloneForCreate(local, "test", false)
			if got := remote.Spec.PodSets[1].Count; got != tc.want {
				t.Errorf("remote workers=%d, want %d", got, tc.want)
			}
			if remote.Spec.PodSets[0].Count != 1 {
				t.Error("head count changed")
			}
			if diff := cmp.Diff(before, local); diff != "" {
				t.Errorf("input changed (-want,+got): %s", diff)
			}
		})
	}
}

func TestReconcilePartialScaleUpCounts(t *testing.T) {
	for name, tc := range map[string]struct {
		enabled, reclaim                       bool
		remoteGrant, want, managerMin, wantMin int32
	}{
		"worker admits only part of manager reservation": {true, false, 2, 2, 1, 1},
		"worker grant falls below the manager minimum":   {true, false, 2, 2, 4, 2},
		"full worker admission":                          {true, false, 6, 6, 1, 1},
		"gate disabled":                                  {false, false, 2, 6, 4, 4},
		"reclaimed pods are not subtracted twice":        {true, true, 6, 6, 1, 1},
	} {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlicesWithPartialReplicaScaleUp, tc.enabled)
			features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlices, true)
			ctx, _ := utiltesting.ContextWithLog(t)
			now := time.Now()
			local := utiltestingapi.MakeWorkload("scale", "ns").
				Annotation(workloadslicing.EnabledAnnotationKey, "true").
				PodSets(kueue.PodSet{Name: "workers", Count: 6, MinCount: new(int32(1))}).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("manager").PodSets(utiltestingapi.MakePodSetAssignment("workers").Count(6).Obj()).Obj(), now).
				AdmittedAt(true, now).Obj()
			remote := local.DeepCopy()
			local.Spec.PodSets[0].MinCount = new(tc.managerMin)
			remote.Status.Admission.PodSetAssignments[0].Count = new(tc.remoteGrant)
			if tc.remoteGrant < tc.managerMin {
				// The admitted worker scaled down after its original grant of six.
				remote.Spec.PodSets[0].Count = tc.remoteGrant
				remote.Spec.PodSets[0].MinCount = new(tc.remoteGrant)
				remote.Status.Admission.PodSetAssignments[0].Count = new(int32(6))
			}
			if tc.reclaim {
				local.Status.ReclaimablePods = []kueue.ReclaimablePod{{Name: "workers", Count: 1}}
				remote.Status.ReclaimablePods = []kueue.ReclaimablePod{{Name: "workers", Count: 1}}
			}
			cl := utiltesting.NewClientBuilder().WithObjects(local).Build()
			r := wlReconciler{client: cl}
			for range 2 {
				if err := r.reconcilePartialScaleUpCounts(ctx, local, remote); err != nil {
					t.Fatal(err)
				}
				if err := cl.Get(ctx, client.ObjectKeyFromObject(local), local); err != nil {
					t.Fatal(err)
				}
			}
			if local.Spec.PodSets[0].Count != tc.want {
				t.Errorf("effective spec count=%d, want %d", local.Spec.PodSets[0].Count, tc.want)
			}
			if diff := cmp.Diff(new(tc.wantMin), local.Spec.PodSets[0].MinCount); diff != "" {
				t.Errorf("manager minimum (-want,+got): %s", diff)
			}
			if *local.Status.Admission.PodSetAssignments[0].Count != 6 {
				t.Error("immutable admission was rewritten")
			}
			if tc.reclaim && workload.ExtractGrantedPodSetCountsAfterReclaim(local)["workers"] != 5 {
				t.Error("reclaimed pods subtracted twice")
			}
		})
	}
}

func TestExistingOversizedPartialAdmissionIsPreserved(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlicesWithPartialReplicaScaleUp, true)
	features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlices, true)
	now := time.Now()
	local := utiltestingapi.MakeWorkload("scale", "ns").Annotation(workloadslicing.EnabledAnnotationKey, "true").
		PodSets(kueue.PodSet{Name: "workers", Count: 6, MinCount: new(int32(1))}).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("manager").PodSets(utiltestingapi.MakePodSetAssignment("workers").Count(2).Obj()).Obj(), now).Obj()
	remote := local.DeepCopy()
	remote.Status.Admission.PodSetAssignments[0].Count = new(int32(6))
	before := remote.DeepCopy()
	if err := validateExistingPartialAdmission(local, remote); err == nil {
		t.Fatal("expected existing oversized admission to require a safe drain")
	}
	local.Spec.PodSets[0].Count = 2
	local.Status.Admission.PodSetAssignments[0].Count = nil
	if err := validateExistingPartialAdmission(local, remote); err == nil {
		t.Fatal("expected omitted assignment count to use the manager PodSet count")
	}

	if diff := cmp.Diff(before, remote, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("worker accounting changed: %s", diff)
	}
}
