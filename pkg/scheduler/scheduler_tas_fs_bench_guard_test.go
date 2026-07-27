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
	"strings"
	"sync"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	"sigs.k8s.io/kueue/pkg/util/routine"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

// recordingSink captures log messages so a test can assert that a code path was
// reached.
type recordingSink struct {
	mu   *sync.Mutex
	msgs *[]string
}

func newRecordingLogger() (logr.Logger, func() []string) {
	var mu sync.Mutex
	msgs := make([]string, 0, 1024)
	sink := &recordingSink{mu: &mu, msgs: &msgs}
	return logr.New(sink), func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := make([]string, len(msgs))
		copy(out, msgs)
		return out
	}
}

func (s *recordingSink) Init(logr.RuntimeInfo) {}

// maxRecordedVerbosity stops below V(6), where the scheduler dumps the whole
// snapshot: that dump nil-dereferences on any TAS leaf domain without usage yet
// (tasUsagePerDomain in pkg/cache/scheduler/tas_flavor_snapshot.go clones
// leafDomain.tasUsage unguarded), which reproduces on upstream main. The messages
// asserted below are at V(4).
const maxRecordedVerbosity = 6

func (s *recordingSink) Enabled(level int) bool         { return level < maxRecordedVerbosity }
func (s *recordingSink) WithName(string) logr.LogSink   { return s }
func (s *recordingSink) WithValues(...any) logr.LogSink { return s }

func (s *recordingSink) Info(_ int, msg string, _ ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	*s.msgs = append(*s.msgs, msg)
}

func (s *recordingSink) Error(_ error, msg string, _ ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	*s.msgs = append(*s.msgs, msg)
}

// TestTASFairSharingFixtureExercisesTheFeature asserts the properties
// BenchmarkSchedulerTASFairSharing depends on. A fixture that silently stops
// reaching the code under test keeps producing plausible numbers, so the
// properties are asserted rather than assumed.
func TestTASFairSharingFixtureExercisesTheFeature(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.FairSharingLookAhead, true)

	f := makeTASFairSharingFixture(1000, 8, 4, 3, 10, 50, 10, 100)

	recLog, dump := newRecordingLogger()
	ctx := ctrl.LoggerInto(t.Context(), recLog)

	objs := []client.Object{
		utiltesting.MakeNamespaceWrapper("default").Obj(),
		f.topology,
	}
	for i := range f.nodes {
		objs = append(objs, &f.nodes[i])
	}
	cl := utiltesting.NewClientBuilder(kueue.AddToScheme, corev1.AddToScheme).
		WithObjects(objs...).
		WithLists(
			&kueue.WorkloadList{Items: f.pendingWorkloads},
			&kueue.LocalQueueList{Items: f.localQueues},
			&kueue.ClusterQueueList{Items: f.clusterQueues},
		).
		WithStatusSubresource(&kueue.Workload{}).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourcePatch: func(ctx context.Context, client client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
				return nil
			},
		}).
		Build()

	recorder := &utiltesting.EventRecorder{}
	cqCache := schdcache.New(cl, schdcache.WithFairSharing(true))
	expStore := preemptexpectations.New()
	qManager := qcache.NewManagerForUnitTests(cl, cqCache,
		qcache.WithFairSharing(true),
		qcache.WithPreemptionExpectations(expStore))

	cqCache.AddOrUpdateTopology(recLog, f.topology)
	cqCache.AddOrUpdateResourceFlavor(recLog, f.flavor)
	if err := cqCache.AddOrUpdateCohort(f.cohort); err != nil {
		t.Fatalf("adding cohort: %v", err)
	}
	for i := range f.clusterQueues {
		if err := cqCache.AddClusterQueue(ctx, &f.clusterQueues[i]); err != nil {
			t.Fatalf("adding ClusterQueue: %v", err)
		}
		if err := qManager.AddClusterQueue(ctx, &f.clusterQueues[i]); err != nil {
			t.Fatalf("adding ClusterQueue to manager: %v", err)
		}
	}
	for i := range f.localQueues {
		if err := qManager.AddLocalQueue(ctx, &f.localQueues[i]); err != nil {
			t.Fatalf("adding LocalQueue: %v", err)
		}
	}
	for i := range f.nodes {
		cqCache.TASCache().SyncNode(&f.nodes[i])
	}
	for i := range f.admittedWorkloads {
		if !cqCache.AddOrUpdateWorkload(recLog, &f.admittedWorkloads[i]) {
			t.Fatalf("adding admitted workload %s", f.admittedWorkloads[i].Name)
		}
	}
	for i := range f.pendingWorkloads {
		if err := qManager.AddOrUpdateWorkload(recLog, &f.pendingWorkloads[i]); err != nil {
			t.Fatalf("adding pending workload %s: %v", f.pendingWorkloads[i].Name, err)
		}
	}

	t.Logf("fixture: %d nodes, %d admitted, %d pending",
		len(f.nodes), len(f.admittedWorkloads), len(f.pendingWorkloads))

	// Equal shares would mean the tournament is ordering by FIFO instead.
	snapshot, err := cqCache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	shares := make(map[float64]int)
	for name, cq := range snapshot.ClusterQueues() {
		share := cq.DominantResourceShare().PreciseWeightedShare()
		shares[share]++
		t.Logf("%s share=%v", name, share)
	}
	if len(shares) < 2 {
		t.Fatalf("expected ClusterQueues with different dominant resource shares, got %v", shares)
	}

	scheduler := New(qManager, cqCache, cl, recorder,
		WithFairSharing(&config.FairSharing{}),
		WithPreemptionExpectations(expStore))
	var wg sync.WaitGroup
	scheduler.setAdmissionRoutineWrapper(routine.NewWrapper(
		func() { wg.Add(1) },
		func() { wg.Done() },
	))
	scheduler.schedule(ctx)
	wg.Wait()

	admits, preemptions := 0, 0
	for _, event := range recorder.RecordedEvents {
		switch event.Reason {
		case "Admitted":
			admits++
		case "Preempted":
			preemptions++
		}
	}
	t.Logf("cycle: %d admits, %d preemptions", admits, preemptions)

	// Without preemption the nominations are not paying for the TAS simulation this
	// benchmark exists to measure.
	if preemptions == 0 {
		t.Errorf("expected the cycle to preempt: without preemption the nominations are not "+
			"exercising the TAS hot loop this benchmark exists to measure (admits=%d)", admits)
	}

	// No other benchmark in this package reaches the look-ahead recompute.
	const recomputeMsg = "Re-computing the assignment as it no longer fits"
	recomputes := 0
	for _, msg := range dump() {
		if strings.Contains(msg, recomputeMsg) {
			recomputes++
		}
	}
	t.Logf("look-ahead recomputes: %d", recomputes)
	if recomputes == 0 {
		t.Errorf("expected updateAssignmentIfNeeded to re-compute at least one assignment; " +
			"the fixture never reaches the look-ahead recompute branch, so the benchmark " +
			"does not measure it")
	}
}
