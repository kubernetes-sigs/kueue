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
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/component-base/featuregate"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	tasindexer "sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	"sigs.k8s.io/kueue/pkg/util/routine"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

type workloadSliceCycle struct {
	featureGates map[featuregate.Feature]bool
	fairSharing  bool
	flavor       *kueue.ResourceFlavor
	topology     *kueue.Topology
	nodes        []corev1.Node
	cqs          []*kueue.ClusterQueue
	lqs          []kueue.LocalQueue
	workloads    []kueue.Workload
}

// run runs one scheduling cycle and reports the cache's admissions and what is
// left in the queues.
func (c workloadSliceCycle) run(t *testing.T, now time.Time) (map[workload.Reference]kueue.Admission, map[kueue.ClusterQueueReference][]workload.Reference) {
	t.Helper()
	features.SetFeatureGatesDuringTest(t, c.featureGates)
	ctx, log := utiltesting.ContextWithLog(t)

	lists := []client.ObjectList{
		&kueue.WorkloadList{Items: c.workloads},
		&corev1.NodeList{Items: c.nodes},
		&kueue.LocalQueueList{Items: c.lqs},
	}
	if c.topology != nil {
		lists = append(lists, &kueue.TopologyList{Items: []kueue.Topology{*c.topology}})
	}
	clientBuilder := utiltesting.NewClientBuilder().
		WithLists(lists...).
		WithObjects(utiltesting.MakeNamespace("default")).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceApply: utiltesting.TreatSSAAsStrategicMergeForApplyConfiguration,
		}).
		WithStatusSubresource(&kueue.Workload{}, &kueue.ClusterQueue{}, &kueue.LocalQueue{})
	_ = tasindexer.SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder))
	cl := clientBuilder.Build()

	cqCache := schdcache.New(cl)
	fakeClock := testingclock.NewFakeClock(now)
	qManager := qcache.NewManagerForUnitTests(cl, cqCache, qcache.WithClock(fakeClock))
	for i := range c.nodes {
		cqCache.TASCache().SyncNode(&c.nodes[i])
	}
	cqCache.AddOrUpdateResourceFlavor(log, c.flavor.DeepCopy())
	if c.topology != nil {
		cqCache.AddOrUpdateTopology(log, c.topology.DeepCopy())
	}
	for _, cq := range c.cqs {
		if err := cqCache.AddClusterQueue(ctx, cq.DeepCopy()); err != nil {
			t.Fatalf("Inserting clusterQueue %s in cache: %v", cq.Name, err)
		}
		if err := qManager.AddClusterQueue(ctx, cq.DeepCopy()); err != nil {
			t.Fatalf("Inserting clusterQueue %s in manager: %v", cq.Name, err)
		}
	}
	for i := range c.lqs {
		if err := qManager.AddLocalQueue(ctx, c.lqs[i].DeepCopy()); err != nil {
			t.Fatalf("Inserting queue %s/%s in manager: %v", c.lqs[i].Namespace, c.lqs[i].Name, err)
		}
	}

	opts := []Option{WithClock(t, fakeClock), WithPreemptionExpectations(preemptexpectations.New())}
	if c.fairSharing {
		opts = append(opts, WithFairSharing(&config.FairSharing{}))
	}
	scheduler := New(qManager, cqCache, cl, &utiltesting.EventRecorder{}, opts...)
	wg := sync.WaitGroup{}
	scheduler.setAdmissionRoutineWrapper(routine.NewWrapper(
		func() { wg.Add(1) },
		func() { wg.Done() },
	))

	ctx, cancel := context.WithTimeout(ctx, queueingTimeout)
	go qManager.CleanUpOnContext(ctx)
	defer cancel()

	scheduler.schedule(ctx)
	wg.Wait()

	snapshot, err := cqCache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("unexpected error while building snapshot: %v", err)
	}
	gotAssignments := make(map[workload.Reference]kueue.Admission)
	for _, cq := range snapshot.ClusterQueues() {
		for name, w := range cq.Workloads {
			if workload.HasQuotaReservation(w.Obj) {
				gotAssignments[name] = *w.Obj.Status.Admission
			}
		}
	}
	return gotAssignments, qManager.Dump()
}

// elasticWorkload builds a workload slice as the job controllers create it.
func elasticWorkload(name, lq string, pods int, creation time.Time) *utiltestingapi.WorkloadWrapper {
	return utiltestingapi.MakeWorkload(name, "default").
		Queue(kueue.LocalQueueName(lq)).
		Creation(creation).
		Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
		PodSets(*utiltestingapi.MakePodSet("one", pods).
			Request(corev1.ResourceCPU, "1").
			Obj())
}

// TestScheduleWorkloadSliceReplacementQuota pins that a workload slice
// replacement moves quota from the replaced slice to itself exactly once in the
// scheduling cycle, for its own fit and for every entry after it.
//
// Without fair sharing, the cohort has 10 CPUs and "grow" replaces the 6 CPU
// slice "old" with 8, leaving 2 CPUs for "b1", which asks for 4.
func TestScheduleWorkloadSliceReplacementQuota(t *testing.T) {
	now := time.Now().Truncate(time.Second)

	clusterQueue := func(name, nominal, borrowingLimit string) *kueue.ClusterQueue {
		return utiltestingapi.MakeClusterQueue(name).Cohort("cohort").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, nominal, borrowingLimit).Obj()).
			Obj()
	}
	admission := func(cq string, pods int32) kueue.Admission {
		return *utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(cq)).PodSets(
			utiltestingapi.MakePodSetAssignment("one").
				Assignment(corev1.ResourceCPU, "default", resource.NewQuantity(int64(pods), resource.DecimalSI).String()).
				Count(pods).
				Obj(),
		).Obj()
	}
	old := elasticWorkload("old", "lq-a", 6, now.Add(-4*time.Minute)).
		ReserveQuotaAt(new(admission("cq-a", 6)), now.Add(-4*time.Minute)).
		Obj()
	grow := elasticWorkload("grow", "lq-a", 8, now.Add(-3*time.Minute)).
		Annotation(workloadslicing.WorkloadSliceReplacementFor, "default/old").
		Obj()
	b1 := utiltestingapi.MakeWorkload("b1", "default").
		Queue("lq-b").
		Creation(now.Add(-2 * time.Minute)).
		PodSets(*utiltestingapi.MakePodSet("one", 4).Request(corev1.ResourceCPU, "1").Obj()).
		Obj()
	grow2 := utiltestingapi.MakeWorkload("grow", "default").
		Queue("lq-a").
		Creation(now.Add(-3 * time.Minute)).
		PodSets(*utiltestingapi.MakePodSet("one", 2).Request(corev1.ResourceCPU, "1").Obj()).
		Obj()

	cases := map[string]struct {
		fairSharing     bool
		cqs             []*kueue.ClusterQueue
		workloads       []kueue.Workload
		wantAssignments map[workload.Reference]kueue.Admission
		wantLeft        map[kueue.ClusterQueueReference][]workload.Reference
	}{
		// cq-b only borrows, so the replacement is processed first and b1
		// must see the 2 CPUs the cohort really has left.
		"a later entry does not reuse the replaced slice's quota": {
			cqs:       []*kueue.ClusterQueue{clusterQueue("cq-a", "10", ""), clusterQueue("cq-b", "0", "10")},
			workloads: []kueue.Workload{*old, *grow, *b1},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/old":  admission("cq-a", 6),
				"default/grow": admission("cq-a", 8),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-b": {"default/b1"},
			},
		},
		"control: a later entry after an ordinary workload of the same net size": {
			cqs:       []*kueue.ClusterQueue{clusterQueue("cq-a", "10", ""), clusterQueue("cq-b", "0", "10")},
			workloads: []kueue.Workload{*old, *grow2, *b1},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/old":  admission("cq-a", 6),
				"default/grow": admission("cq-a", 2),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-b": {"default/b1"},
			},
		},
		// b1 fits within cq-b's nominal quota, so it is processed first and
		// the replacement's own fit check sees a cohort with no room left.
		"the replacement's own fit does not reuse the replaced slice's quota": {
			cqs:       []*kueue.ClusterQueue{clusterQueue("cq-a", "6", ""), clusterQueue("cq-b", "4", "10")},
			workloads: []kueue.Workload{*old, *grow, *b1},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/old": admission("cq-a", 6),
				"default/b1":  admission("cq-b", 4),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-a": {"default/grow"},
			},
		},
		// ClusterQueues are ranked by their share after admission: cq-a would
		// borrow 2 for grow's increase and cq-b 3 for b1, so grow goes first
		// and b1 no longer fits.
		"fair sharing ranks the replacement by its increase": {
			fairSharing: true,
			cqs: []*kueue.ClusterQueue{
				clusterQueue("cq-a", "4", ""),
				clusterQueue("cq-b", "3", ""),
				clusterQueue("cq-c", "3", ""),
			},
			workloads: []kueue.Workload{
				*elasticWorkload("old", "lq-a", 4, now.Add(-4*time.Minute)).
					ReserveQuotaAt(new(admission("cq-a", 4)), now.Add(-4*time.Minute)).
					Obj(),
				*elasticWorkload("grow", "lq-a", 6, now.Add(-3*time.Minute)).
					Annotation(workloadslicing.WorkloadSliceReplacementFor, "default/old").
					Obj(),
				*utiltestingapi.MakeWorkload("b1", "default").
					Queue("lq-b").
					Creation(now.Add(-5 * time.Minute)).
					PodSets(*utiltestingapi.MakePodSet("one", 6).Request(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/old":  admission("cq-a", 4),
				"default/grow": admission("cq-a", 6),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-b": {"default/b1"},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gotAssignments, gotLeft := workloadSliceCycle{
				featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
				fairSharing:  tc.fairSharing,
				flavor:       utiltestingapi.MakeResourceFlavor("default").Obj(),
				cqs:          tc.cqs,
				lqs: []kueue.LocalQueue{
					*utiltestingapi.MakeLocalQueue("lq-a", "default").ClusterQueue("cq-a").Obj(),
					*utiltestingapi.MakeLocalQueue("lq-b", "default").ClusterQueue("cq-b").Obj(),
				},
				workloads: tc.workloads,
			}.run(t, now)
			if diff := cmp.Diff(tc.wantAssignments, gotAssignments); diff != "" {
				t.Errorf("Unexpected assignments (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantLeft, gotLeft, cmpDump...); diff != "" {
				t.Errorf("Unexpected elements left in the queue (-want,+got):\n%s", diff)
			}
		})
	}
}

// TestScheduleWorkloadSliceReplacementTAS pins the topology side of the same
// transition. It needs refill: only a successor nominated mid-cycle reads the
// topology usage the replacement leaves behind.
func TestScheduleWorkloadSliceReplacementTAS(t *testing.T) {
	now := time.Now().Truncate(time.Second)

	topology := utiltestingapi.MakeTopology("tas-single-level").
		Levels(corev1.LabelHostname).
		Obj()
	node := func(name, cpu string) corev1.Node {
		return *testingnode.MakeNode(name).
			Label("tas-node", "true").
			Label(corev1.LabelHostname, name).
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:  resource.MustParse(cpu),
				corev1.ResourcePods: resource.MustParse("10"),
			}).
			Ready().
			Obj()
	}
	tasWorkload := func(name string, pods int, creation time.Time) *utiltestingapi.WorkloadWrapper {
		return utiltestingapi.MakeWorkload(name, "default").
			Queue("lq").
			Creation(creation).
			Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			PodSets(*utiltestingapi.MakePodSet("one", pods).
				UnconstrainedTopologyRequest().
				Request(corev1.ResourceCPU, "1").
				Obj())
	}
	type domain struct {
		host string
		pods int32
	}
	admission := func(domains ...domain) kueue.Admission {
		var pods int32
		ta := utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname})
		for _, d := range domains {
			pods += d.pods
			ta = ta.Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{d.host}, d.pods).Obj())
		}
		return *utiltestingapi.MakeAdmission("cq").PodSets(
			utiltestingapi.MakePodSetAssignment("one").
				Assignment(corev1.ResourceCPU, "tas-default", resource.NewQuantity(int64(pods), resource.DecimalSI).String()).
				Count(pods).
				TopologyAssignment(ta.Obj()).
				Obj(),
		).Obj()
	}
	oldSlice := func(pods int32) kueue.Workload {
		return *tasWorkload("old", int(pods), now.Add(-3*time.Minute)).
			ReserveQuotaAt(new(admission(domain{"x1", pods})), now.Add(-3*time.Minute)).
			Obj()
	}
	old := oldSlice(2)
	replacement := func(name string, pods int, creation time.Time) kueue.Workload {
		return *tasWorkload(name, pods, creation).
			Annotation(workloadslicing.WorkloadSliceReplacementFor, "default/old").
			Obj()
	}
	succ := *tasWorkload("succ", 1, now.Add(-time.Minute)).Obj()

	cases := map[string]struct {
		sliceAwareTAS   bool
		nodes           []corev1.Node
		workloads       []kueue.Workload
		wantAssignments map[workload.Reference]kueue.Admission
		wantLeft        map[kueue.ClusterQueueReference][]workload.Reference
	}{
		// The node fits grow and succ only if old's pods are no longer counted.
		"a refilled successor sees the replaced slice's domains released": {
			sliceAwareTAS: true,
			nodes:         []corev1.Node{node("x1", "4")},
			workloads:     []kueue.Workload{old, replacement("grow", 3, now.Add(-2*time.Minute)), succ},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/old":  admission(domain{"x1", 2}),
				"default/grow": admission(domain{"x1", 3}),
				"default/succ": admission(domain{"x1", 1}),
			},
		},
		"scale down: a refilled successor sees the replaced slice's domains released": {
			sliceAwareTAS: true,
			nodes:         []corev1.Node{node("x1", "3")},
			workloads:     []kueue.Workload{oldSlice(3), replacement("grow", 2, now.Add(-2*time.Minute)), succ},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/old":  admission(domain{"x1", 3}),
				"default/grow": admission(domain{"x1", 2}),
				"default/succ": admission(domain{"x1", 1}),
			},
		},
		// Without slice-aware placement the replacement is placed on x2 as
		// if it were new, while old's pods keep running on x1.
		"without slice-aware placement the replaced slice's domains stay occupied": {
			sliceAwareTAS: false,
			nodes:         []corev1.Node{node("x1", "2"), node("x2", "3")},
			workloads:     []kueue.Workload{old, replacement("grow", 3, now.Add(-2*time.Minute)), succ},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/old":  admission(domain{"x1", 2}),
				"default/grow": admission(domain{"x2", 3}),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq": {"default/succ"},
			},
		},
		// Only refill can bring a second replacement of the same slice into
		// the cycle, and it must still conflict with the first one.
		"a second replacement of the same slice is not admitted in the same cycle": {
			sliceAwareTAS: true,
			nodes:         []corev1.Node{node("x1", "20")},
			workloads: []kueue.Workload{
				old,
				replacement("grow", 3, now.Add(-2*time.Minute)),
				replacement("fork", 3, now.Add(-2*time.Minute+time.Second)),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/old":  admission(domain{"x1", 2}),
				"default/grow": admission(domain{"x1", 3}),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq": {"default/fork"},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gotAssignments, gotLeft := workloadSliceCycle{
				featureGates: map[featuregate.Feature]bool{
					features.ElasticJobsViaWorkloadSlices:        true,
					features.ElasticJobsViaWorkloadSlicesWithTAS: tc.sliceAwareTAS,
					features.FairSharingRefill:                   true,
				},
				fairSharing: true,
				flavor: utiltestingapi.MakeResourceFlavor("tas-default").
					NodeLabel("tas-node", "true").
					TopologyName("tas-single-level").
					Obj(),
				topology: topology,
				nodes:    tc.nodes,
				cqs: []*kueue.ClusterQueue{utiltestingapi.MakeClusterQueue("cq").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-default").
						Resource(corev1.ResourceCPU, "100").Obj()).
					Obj()},
				lqs:       []kueue.LocalQueue{*utiltestingapi.MakeLocalQueue("lq", "default").ClusterQueue("cq").Obj()},
				workloads: tc.workloads,
			}.run(t, now)
			if diff := cmp.Diff(tc.wantAssignments, gotAssignments); diff != "" {
				t.Errorf("Unexpected assignments (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantLeft, gotLeft, cmpDump...); diff != "" {
				t.Errorf("Unexpected elements left in the queue (-want,+got):\n%s", diff)
			}
		})
	}
}

// TestScheduleWorkloadSliceReplacementDeferredFit pins the same transition for
// a replacement deferred until another workload's preemption completes.
//
// The cohort has 10 CPUs and "v" borrows 6 of them. "x" reclaims from v, and
// "grow" overlaps x's targets, so grow is deferred until v is gone. "l"
// borrows 2 and comes last.
func TestScheduleWorkloadSliceReplacementDeferredFit(t *testing.T) {
	now := time.Now().Truncate(time.Second)

	clusterQueue := func(name, nominal string) *kueue.ClusterQueue {
		return utiltestingapi.MakeClusterQueue(name).Cohort("cohort").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, nominal).Obj()).
			Preemption(kueue.ClusterQueuePreemption{
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
			}).
			Obj()
	}
	admission := func(cq string, pods int32) kueue.Admission {
		return *utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(cq)).PodSets(
			utiltestingapi.MakePodSetAssignment("one").
				Assignment(corev1.ResourceCPU, "default", resource.NewQuantity(int64(pods), resource.DecimalSI).String()).
				Count(pods).
				Obj(),
		).Obj()
	}
	plainWorkload := func(name, lq string, pods int, creation time.Time) *utiltestingapi.WorkloadWrapper {
		return utiltestingapi.MakeWorkload(name, "default").
			Queue(kueue.LocalQueueName(lq)).
			Creation(creation).
			PodSets(*utiltestingapi.MakePodSet("one", pods).Request(corev1.ResourceCPU, "1").Obj())
	}
	localQueue := func(name, cq string) kueue.LocalQueue {
		return *utiltestingapi.MakeLocalQueue(name, "default").ClusterQueue(cq).Obj()
	}

	cases := map[string]struct {
		oldPods, xPods  int32
		wantAssignments map[workload.Reference]kueue.Admission
		wantLeft        map[kueue.ClusterQueueReference][]workload.Reference
	}{
		// After the preemptions x takes 3 and grow 5, leaving room for l.
		"a later entry does not see the replaced slice twice": {
			oldPods: 2,
			xPods:   3,
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/v":   admission("cq-v", 6),
				"default/old": admission("cq-a", 2),
				"default/l":   admission("cq-l", 2),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-x": {"default/x"},
				"cq-a": {"default/grow"},
			},
		},
		// After the preemptions x takes 4 and grow 5, leaving 1 for l.
		"a later entry sees the replacement's full quota": {
			oldPods: 1,
			xPods:   4,
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/v":   admission("cq-v", 6),
				"default/old": admission("cq-a", 1),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq-x": {"default/x"},
				"cq-a": {"default/grow"},
				"cq-l": {"default/l"},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gotAssignments, gotLeft := workloadSliceCycle{
				featureGates: map[featuregate.Feature]bool{
					features.ElasticJobsViaWorkloadSlices:                    true,
					features.RecomputeAssignmentUponPreemptionTargetsOverlap: true,
				},
				flavor: utiltestingapi.MakeResourceFlavor("default").Obj(),
				cqs: []*kueue.ClusterQueue{
					clusterQueue("cq-v", "0"),
					clusterQueue("cq-x", "4"),
					clusterQueue("cq-a", "6"),
					clusterQueue("cq-l", "0"),
				},
				lqs: []kueue.LocalQueue{
					localQueue("lq-v", "cq-v"),
					localQueue("lq-x", "cq-x"),
					localQueue("lq-a", "cq-a"),
					localQueue("lq-l", "cq-l"),
				},
				workloads: []kueue.Workload{
					*plainWorkload("v", "lq-v", 6, now.Add(-10*time.Minute)).
						ReserveQuotaAt(new(admission("cq-v", 6)), now.Add(-10*time.Minute)).
						Obj(),
					*elasticWorkload("old", "lq-a", int(tc.oldPods), now.Add(-9*time.Minute)).
						ReserveQuotaAt(new(admission("cq-a", tc.oldPods)), now.Add(-9*time.Minute)).
						Obj(),
					*plainWorkload("x", "lq-x", int(tc.xPods), now.Add(-3*time.Minute)).Obj(),
					*elasticWorkload("grow", "lq-a", 5, now.Add(-2*time.Minute)).
						Annotation(workloadslicing.WorkloadSliceReplacementFor, "default/old").
						Obj(),
					*plainWorkload("l", "lq-l", 2, now.Add(-time.Minute)).Obj(),
				},
			}.run(t, now)
			if diff := cmp.Diff(tc.wantAssignments, gotAssignments); diff != "" {
				t.Errorf("Unexpected assignments (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantLeft, gotLeft, cmpDump...); diff != "" {
				t.Errorf("Unexpected elements left in the queue (-want,+got):\n%s", diff)
			}
		})
	}
}
