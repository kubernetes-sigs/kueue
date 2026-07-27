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
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
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
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

// BenchmarkSchedulerFairSharing measures one scheduling cycle over a cohort of
// contending ClusterQueues with Fair Sharing enabled.
//
// Look-ahead raises both the cost of a cycle and the number of workloads it
// admits, so ns/admit is the comparable number rather than ns/op. The benefit the
// feature is for is a cross-cycle property and is not visible here.
//
// Only the gate off -> on delta within one topology is meaningful: the tournament
// cost per pop scales with the number of ClusterQueues under the winning root, so
// the three topologies have structurally different baselines.
func BenchmarkSchedulerFairSharing(b *testing.B) {
	cases := []struct {
		// topology is one of:
		//   flat    - one root cohort, every ClusterQueue a direct child.
		//   roots   - independent root cohorts.
		//   subtree - one root cohort with intermediate sub-cohorts.
		topology string
		// roots is the number of top-level cohorts.
		roots int
		// cqsPerRoot is the number of ClusterQueues under each top-level cohort;
		// half are lenders (spare quota, no pending work) and half are borrowers.
		cqsPerRoot int
		// pendingPerCQ is the number of pending workloads queued in each borrower.
		pendingPerCQ int
	}{
		{topology: "flat", roots: 1, cqsPerRoot: 32, pendingPerCQ: 6},
		{topology: "roots", roots: 8, cqsPerRoot: 4, pendingPerCQ: 6},
		{topology: "subtree", roots: 8, cqsPerRoot: 4, pendingPerCQ: 6},
	}

	for _, tc := range cases {
		fixture := makeFairSharingFixture(tc.topology, tc.roots, tc.cqsPerRoot, tc.pendingPerCQ)
		for _, lookAhead := range []bool{false, true} {
			name := fmt.Sprintf("topology=%s/cqs=%d/lookAhead=%t",
				tc.topology, tc.roots*tc.cqsPerRoot, lookAhead)
			b.Run(name, func(b *testing.B) {
				features.SetFeatureGateDuringTest(b, features.FairSharingLookAhead, lookAhead)
				// Look-ahead doubles the number of logged entries, so the test
				// logger's default verbosity would charge the treatment for its own
				// formatting.
				ctx := ctrl.LoggerInto(b.Context(), logr.Discard())
				log := logr.Discard()

				b.ReportAllocs()
				totalAdmits := 0
				for b.Loop() {
					b.StopTimer()

					// Nothing enforces that the scheduler leaves these unmutated, and a
					// change that did would corrupt later iterations silently.
					pending := make([]kueue.Workload, len(fixture.pendingWorkloads))
					for i := range fixture.pendingWorkloads {
						fixture.pendingWorkloads[i].DeepCopyInto(&pending[i])
					}
					admitted := make([]kueue.Workload, len(fixture.admittedWorkloads))
					for i := range fixture.admittedWorkloads {
						fixture.admittedWorkloads[i].DeepCopyInto(&admitted[i])
					}

					cl := utiltesting.NewClientBuilder(kueue.AddToScheme, corev1.AddToScheme).
						WithObjects(utiltesting.MakeNamespaceWrapper("default").Obj()).
						WithLists(
							&kueue.WorkloadList{Items: pending},
							&kueue.LocalQueueList{Items: fixture.localQueues},
							&kueue.ClusterQueueList{Items: fixture.clusterQueues},
						).
						WithStatusSubresource(&kueue.Workload{}).
						WithInterceptorFuncs(interceptor.Funcs{
							SubResourcePatch: func(ctx context.Context, client client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
								return nil // discard status updates to speed up the bench loop
							},
						}).
						Build()

					recorder := &utiltesting.EventRecorder{}
					cqCache := schdcache.New(cl, schdcache.WithFairSharing(true))
					expStore := preemptexpectations.New()
					qManager := qcache.NewManagerForUnitTests(cl, cqCache,
						qcache.WithFairSharing(true),
						qcache.WithPreemptionExpectations(expStore))

					cqCache.AddOrUpdateResourceFlavor(log, fixture.flavor)
					for i := range fixture.cohorts {
						if err := cqCache.AddOrUpdateCohort(&fixture.cohorts[i]); err != nil {
							b.Fatalf("Failed to add Cohort to cqCache: %v", err)
						}
					}
					for i := range fixture.clusterQueues {
						if err := cqCache.AddClusterQueue(ctx, &fixture.clusterQueues[i]); err != nil {
							b.Fatalf("Failed to add ClusterQueue to cqCache: %v", err)
						}
						if err := qManager.AddClusterQueue(ctx, &fixture.clusterQueues[i]); err != nil {
							b.Fatalf("Failed to add ClusterQueue to qManager: %v", err)
						}
					}
					for i := range fixture.localQueues {
						if err := qManager.AddLocalQueue(ctx, &fixture.localQueues[i]); err != nil {
							b.Fatalf("Failed to add LocalQueue to qManager: %v", err)
						}
					}
					// These put the borrowers above their nominal quota, which is what
					// makes their dominant resource shares non-zero.
					for i := range admitted {
						if !cqCache.AddOrUpdateWorkload(log, &admitted[i]) {
							b.Fatalf("Failed to add workload %s to cqCache", admitted[i].Name)
						}
					}
					for i := range pending {
						if err := qManager.AddOrUpdateWorkload(log, &pending[i]); err != nil {
							b.Fatalf("Failed to add workload %s to qManager: %v", pending[i].Name, err)
						}
					}

					scheduler := New(qManager, cqCache, cl, recorder,
						WithFairSharing(&config.FairSharing{}),
						WithPreemptionExpectations(expStore))
					var wg sync.WaitGroup
					scheduler.setAdmissionRoutineWrapper(routine.NewWrapper(
						func() { wg.Add(1) },
						func() { wg.Done() },
					))

					// The setup above allocates heavily; collect it now so the GC does
					// not run inside the measurement.
					runtime.GC()

					b.StartTimer()
					scheduler.schedule(ctx)
					// Admission finishes on a separate goroutine; look-ahead causes
					// more of it, so excluding it would bias the comparison.
					wg.Wait()
					b.StopTimer()

					admits := 0
					for _, event := range recorder.RecordedEvents {
						if event.Reason == "Admitted" {
							admits++
						}
					}
					// A cycle that admits nothing is measuring an empty pass.
					if admits == 0 {
						b.Fatal("Expected at least one admission per cycle, but found none")
					}
					totalAdmits += admits
					b.StartTimer()
				}

				b.ReportMetric(float64(totalAdmits)/float64(b.N), "admits/cycle")
				b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(totalAdmits), "ns/admit")
			})
		}
	}
}

// fairSharingFixture is the static object graph shared by every iteration of a
// benchmark case.
type fairSharingFixture struct {
	flavor            *kueue.ResourceFlavor
	cohorts           []kueue.Cohort
	clusterQueues     []kueue.ClusterQueue
	localQueues       []kueue.LocalQueue
	admittedWorkloads []kueue.Workload
	pendingWorkloads  []kueue.Workload
}

// DominantResourceShare is zero for any node whose usage stays within its own
// SubtreeQuota, so a fixture where every queue fits inside its nominal quota
// degenerates into FIFO. Borrowers are given a tiny quota and enough pre-admitted
// usage to exceed it; lenders hold the spare capacity.
const (
	lenderNominalCPU   = "20"
	borrowerNominalCPU = "1"
)

func makeFairSharingFixture(topology string, roots, cqsPerRoot, pendingPerCQ int) *fairSharingFixture {
	now := time.Now()
	f := &fairSharingFixture{
		flavor: utiltestingapi.MakeResourceFlavor("default").Obj(),
	}

	// cqCohorts[i] is the cohort that ClusterQueue i is attached to.
	var cqCohorts []kueue.CohortReference
	switch topology {
	case "flat":
		f.cohorts = append(f.cohorts, *utiltestingapi.MakeCohort("root").Obj())
		for range roots * cqsPerRoot {
			cqCohorts = append(cqCohorts, "root")
		}
	case "roots":
		for r := range roots {
			name := kueue.CohortReference(fmt.Sprintf("root-%d", r))
			f.cohorts = append(f.cohorts, *utiltestingapi.MakeCohort(name).Obj())
			for range cqsPerRoot {
				cqCohorts = append(cqCohorts, name)
			}
		}
	case "subtree":
		f.cohorts = append(f.cohorts, *utiltestingapi.MakeCohort("root").Obj())
		for r := range roots {
			name := kueue.CohortReference(fmt.Sprintf("sub-%d", r))
			f.cohorts = append(f.cohorts, *utiltestingapi.MakeCohort(name).Parent("root").Obj())
			for range cqsPerRoot {
				cqCohorts = append(cqCohorts, name)
			}
		}
	default:
		panic("unknown topology " + topology)
	}

	// Timestamps must be distinct across queues, not only within one: identical
	// timestamps make the FIFO tiebreak degenerate and hand the decision to map
	// iteration order.
	created := 0

	for i := range roots * cqsPerRoot {
		cqName := fmt.Sprintf("cq-%d", i)
		lqName := fmt.Sprintf("lq-%d", i)
		borrower := i%2 == 1

		nominal := lenderNominalCPU
		if borrower {
			nominal = borrowerNominalCPU
		}
		f.clusterQueues = append(f.clusterQueues, *utiltestingapi.MakeClusterQueue(cqName).
			FairWeight(resource.MustParse("1")).
			Cohort(cqCohorts[i]).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, nominal).Obj()).
			Obj())

		f.localQueues = append(f.localQueues, *utiltestingapi.MakeLocalQueue(lqName, "default").
			ClusterQueue(cqName).Obj())

		if !borrower {
			continue
		}

		// Vary the borrowed amount so the shares, and therefore the ordering,
		// differ between borrowers.
		usage := 2 + (i/2)%4
		for a := range usage {
			name := fmt.Sprintf("admitted-%d-%d", i, a)
			f.admittedWorkloads = append(f.admittedWorkloads, *utiltestingapi.MakeWorkload(name, "default").
				UID(types.UID(name)).
				Queue(kueue.LocalQueueName(lqName)).
				Request(corev1.ResourceCPU, "1").
				ReserveQuotaAt(utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(cqName)).
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "default", "1").Obj()).
					Obj(), now).
				AdmittedAt(true, now).
				Obj())
		}

		for range pendingPerCQ {
			created++
			name := fmt.Sprintf("pending-%d-%d", i, created)
			f.pendingWorkloads = append(f.pendingWorkloads, *utiltestingapi.MakeWorkload(name, "default").
				UID(types.UID(name)).
				Queue(kueue.LocalQueueName(lqName)).
				Request(corev1.ResourceCPU, "1").
				Creation(now.Add(time.Duration(created) * time.Millisecond)).
				Obj())
		}
	}

	return f
}
