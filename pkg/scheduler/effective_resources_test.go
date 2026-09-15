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

package scheduler

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	testingclock "k8s.io/utils/clock/testing"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestAssumeKeepsEffectiveResourceSnapshot(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, true)
	ctx, log := utiltesting.ContextWithLog(t)
	lr := utiltesting.MakeLimitRange("defaults", "ns").WithValue("DefaultRequest", corev1.ResourceCPU, "1").Obj()
	cl := utiltesting.NewClientBuilder().WithObjects(lr).
		WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType).Build()
	cache := schdcache.New(cl)
	rf := utiltestingapi.MakeResourceFlavor("rf").Obj()
	cache.AddOrUpdateResourceFlavor(log, rf)
	cq := utiltestingapi.MakeClusterQueue("cq").ResourceGroup(*utiltestingapi.MakeFlavorQuotas("rf").Resource(corev1.ResourceCPU, "10").Obj()).Obj()
	if err := cache.AddClusterQueue(ctx, cq); err != nil {
		t.Fatal(err)
	}
	wl := utiltestingapi.MakeWorkload("wl", "ns").Obj()
	info := workload.NewInfoFromClient(ctx, cl, wl)
	lr.Spec.Limits[0].DefaultRequest[corev1.ResourceCPU] = resource.MustParse("2")
	if err := cl.Update(ctx, lr); err != nil {
		t.Fatal(err)
	}
	admission := &kueue.Admission{ClusterQueue: "cq", PodSetAssignments: []kueue.PodSetAssignment{{
		Name: "main", Count: new(int32(1)), Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{corev1.ResourceCPU: "rf"},
		ResourceUsage:      corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
		TopologyAssignment: utiltestingapi.MakeTopologyAssignment([]string{"host"}).Domain(tas.TopologyDomainAssignment{Count: 1, Values: []string{"node"}}).Obj(),
	}}}
	sched := &Scheduler{cache: cache, clock: testingclock.NewFakeClock(time.Now())}
	e := &entry{Head: qcache.Head{Info: *info}}
	if _, err := sched.assumeWorkload(log, e, &schdcache.ClusterQueueSnapshot{}, admission); err != nil {
		t.Fatal(err)
	}
	snapshot, err := cache.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cached := snapshot.ClusterQueue("cq").Workloads[workload.Key(wl)]
	domain := cached.TotalRequests[0].TopologyRequest.DomainRequests[0]
	if got := domain.SinglePodRequests.ResourceValue(corev1.ResourceCPU); got != 1000 {
		t.Errorf("assumed TAS CPU = %d, want the scheduling snapshot's 1000", got)
	}
	if got := cached.TotalRequests[0].Requests.ResourceValue(corev1.ResourceCPU); got != 1000 {
		t.Errorf("reserved quota CPU = %d, want 1000", got)
	}
	if len(cached.Obj.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Requests) != 0 {
		t.Fatal("cache raw Workload contains injected defaults")
	}
}
