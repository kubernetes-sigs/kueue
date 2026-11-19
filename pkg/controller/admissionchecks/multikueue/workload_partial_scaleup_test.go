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
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

func TestOversizedPartialAdmissionStillPropagatesManagerEviction(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlicesWithPartialReplicaScaleUp, true)
	features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlices, true)
	ctx, _ := utiltesting.ContextWithLog(t)
	now := time.Now()
	local := utiltestingapi.MakeWorkload("scale", TestNamespace).
		Annotation(workloadslicing.EnabledAnnotationKey, "true").
		PodSets(kueue.PodSet{Name: "workers", Count: 6, MinCount: new(int32(1))}).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("manager").PodSets(utiltestingapi.MakePodSetAssignment("workers").Count(2).Obj()).Obj(), now).
		AdmittedAt(true, now).
		AdmissionCheck(kueue.AdmissionCheckState{Name: "ac1", State: kueue.CheckStateReady, LastTransitionTime: metav1.NewTime(now)}).
		ClusterName("worker1").Obj()
	remote := local.DeepCopy()
	remote.Status.Admission.PodSetAssignments[0].Count = new(int32(6))
	remote.Status.AdmissionChecks = nil
	local.Status.Conditions = append(local.Status.Conditions, metav1.Condition{Type: kueue.WorkloadEvicted, Status: metav1.ConditionTrue, Reason: "Test", Message: "Evicted by test", LastTransitionTime: metav1.NewTime(now)})
	managerClient := getClientBuilder(ctx).WithObjects(local).WithStatusSubresource(local).Build()
	workerClient := NewNeverCachingClient(getClientBuilder(ctx).WithObjects(remote).WithStatusSubresource(remote).Build())
	group := &wlGroup{
		local: local, localClient: managerClient,
		remotes:       map[string]*kueue.Workload{"worker1": remote},
		remoteClients: map[string]*remoteClient{"worker1": {client: workerClient, origin: defaultOrigin}},
		acName:        "ac1", jobAdapter: &deferredSyncStubAdapter{},
		controllerKey: types.NamespacedName{Name: "job1", Namespace: TestNamespace},
	}
	reconciler := &wlReconciler{client: managerClient, clock: testingclock.NewFakeClock(now), origin: defaultOrigin, workerLostTimeout: defaultWorkerLostTimeout, recorder: &utiltesting.EventRecorder{}, dispatcherName: config.MultiKueueDispatcherModeAllAtOnce}
	if _, err := reconciler.reconcileGroup(ctx, group); err != nil {
		t.Fatal(err)
	}
	got := &kueue.Workload{}
	if err := workerClient.Get(ctx, client.ObjectKeyFromObject(remote), got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.PodSets[0].Count != 6 || *got.Status.Admission.PodSetAssignments[0].Count != 6 {
		t.Fatal("oversized remote accounting was reduced before draining")
	}
	for _, condition := range got.Status.Conditions {
		if condition.Type == kueue.WorkloadDeactivationTarget && condition.Status == metav1.ConditionTrue {
			return
		}
	}
	t.Fatal("manager eviction did not reach oversized remote admission")
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
		enabled, reclaim  bool
		remoteGrant, want int32
	}{
		"worker admits only part of manager reservation": {true, false, 2, 2},
		"full worker admission":                          {true, false, 6, 6},
		"gate disabled":                                  {false, false, 2, 6},
		"reclaimed pods are not subtracted twice":        {true, true, 6, 6},
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
			remote.Status.Admission.PodSetAssignments[0].Count = new(tc.remoteGrant)
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
	if diff := cmp.Diff(before, remote, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("worker accounting changed: %s", diff)
	}
}
