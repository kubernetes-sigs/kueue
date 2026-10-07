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
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/component-base/featuregate"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/was"
	tasindexer "sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	"sigs.k8s.io/kueue/pkg/util/routine"
	"sigs.k8s.io/kueue/pkg/util/slices"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
)

// tasScheduleForTASCase is the shared case definition for TestScheduleForTAS and
// TestScheduleForTASSchedulerLibrary.
type tasScheduleForTASCase struct {
	resourceTransformations []config.ResourceTransformation
	nodes                   []corev1.Node
	pods                    []corev1.Pod
	topologies              []kueue.Topology
	admissionChecks         []kueue.AdmissionCheck
	resourceFlavors         []kueue.ResourceFlavor
	clusterQueues           []kueue.ClusterQueue
	workloads               []kueue.Workload
	patchStatusErr          error

	// wantNewAssignments is a summary of all new admissions in the cache after this cycle.
	wantNewAssignments map[workload.Reference]kueue.Admission
	// wantLeft is the workload keys that are left in the queues after this cycle.
	wantLeft map[kueue.ClusterQueueReference][]workload.Reference
	// wantInadmissibleLeft is the workload keys that are left in the inadmissible state after this cycle.
	wantInadmissibleLeft map[kueue.ClusterQueueReference][]workload.Reference
	// wantEvents asserts on the events, the comparison options are passed by eventCmpOpts
	wantEvents []utiltesting.EventRecord
	// eventCmpOpts are the comparison options for the events
	eventCmpOpts cmp.Options

	featureGates map[featuregate.Feature]bool
}

// runScheduleForTASCases runs the shared "build client → schedule → assert" procedure for
// TestScheduleForTAS and TestScheduleForTASSchedulerLibrary.
func runScheduleForTASCases(t *testing.T, queues []kueue.LocalQueue, now time.Time, cases map[string]tasScheduleForTASCase) {
	t.Helper()

	scenarios := []map[featuregate.Feature]bool{
		{
			features.WorkloadRequestUseMergePatch:     false,
			features.UnadmittedWorkloadsObservability: false,
			features.TASCacheNodeMatchResults:         true,
			features.TASCachingRemainingResources:     true,
		},
		{
			features.WorkloadRequestUseMergePatch:     false,
			features.UnadmittedWorkloadsObservability: true,
			features.TASCacheNodeMatchResults:         true,
			features.TASCachingRemainingResources:     true,
		},
		{
			features.WorkloadRequestUseMergePatch:     true,
			features.UnadmittedWorkloadsObservability: false,
			features.TASCacheNodeMatchResults:         true,
			features.TASCachingRemainingResources:     true,
		},
		{
			features.WorkloadRequestUseMergePatch:     true,
			features.UnadmittedWorkloadsObservability: true,
			features.TASCacheNodeMatchResults:         true,
			features.TASCachingRemainingResources:     true,
		},
		{
			features.WorkloadRequestUseMergePatch:     false,
			features.UnadmittedWorkloadsObservability: false,
			features.TASCacheNodeMatchResults:         false,
			features.TASCachingRemainingResources:     false,
		},
	}

	for name, tc := range cases {
		for _, scenario := range scenarios {
			t.Run(
				fmt.Sprintf("%s WorkloadRequestUseMergePatch:%t observability:%t cacheMatchResults:%t cachingRemainingResources:%t",
					name,
					scenario[features.WorkloadRequestUseMergePatch],
					scenario[features.UnadmittedWorkloadsObservability],
					scenario[features.TASCacheNodeMatchResults],
					scenario[features.TASCachingRemainingResources],
				),
				func(t *testing.T) {
					features.SetFeatureGatesDuringTest(t, scenario)
					features.SetFeatureGatesDuringTest(t, tc.featureGates)
					ctx, log := utiltesting.ContextWithLog(t)
					testWls := make([]kueue.Workload, 0, len(tc.workloads))
					for _, wl := range tc.workloads {
						testWls = append(testWls, *wl.DeepCopy())
					}

					clientBuilder := utiltesting.NewClientBuilder().
						WithLists(
							&kueue.AdmissionCheckList{Items: tc.admissionChecks},
							&kueue.WorkloadList{Items: testWls},
							&kueue.TopologyList{Items: tc.topologies},
							&corev1.PodList{Items: tc.pods},
							&corev1.NodeList{Items: tc.nodes},
							&kueue.LocalQueueList{Items: queues}).
						WithObjects(utiltesting.MakeNamespace("default")).
						WithInterceptorFuncs(interceptor.Funcs{
							SubResourcePatch: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
								if tc.patchStatusErr != nil {
									return tc.patchStatusErr
								}
								return utiltesting.TreatSSAAsStrategicMerge(ctx, c, subResourceName, obj, patch, opts...)
							},
						}).
						WithStatusSubresource(&kueue.Workload{}, &kueue.ClusterQueue{}, &kueue.LocalQueue{})

					for _, ac := range tc.admissionChecks {
						clientBuilder = clientBuilder.WithStatusSubresource(ac.DeepCopy())
					}
					_ = tasindexer.SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder))
					cl := clientBuilder.Build()
					recorder := &utiltesting.EventRecorder{}
					cacheOptions := []schdcache.Option{schdcache.WithResourceTransformations(tc.resourceTransformations)}
					if features.Enabled(features.SchedulerLibraryIntegration) {
						simulatorFactory, err := was.NewWASSimulatorFactoryForTest(ctx)
						if err != nil {
							t.Fatalf("Failed to initialize WAS scheduling simulator: %v", err)
						}
						cacheOptions = append(cacheOptions, schdcache.WithSimulatorFactory(simulatorFactory))
					}
					cqCache := schdcache.New(cl, cacheOptions...)
					fakeClock := testingclock.NewFakeClock(now)
					qManager := qcache.NewManagerForUnitTests(cl, cqCache,
						qcache.WithClock(fakeClock), qcache.WithResourceTransformations(tc.resourceTransformations))
					topologyByName := slices.ToMap(tc.topologies, func(i int) (kueue.TopologyReference, kueue.Topology) {
						return kueue.TopologyReference(tc.topologies[i].Name), tc.topologies[i]
					})
					for i := range tc.nodes {
						cqCache.TASCache().SyncNode(&tc.nodes[i])
					}
					for _, ac := range tc.admissionChecks {
						cqCache.AddOrUpdateAdmissionCheck(log, &ac)
					}
					for _, flavor := range tc.resourceFlavors {
						cqCache.AddOrUpdateResourceFlavor(log, &flavor)
						if flavor.Spec.TopologyName != nil {
							t := topologyByName[*flavor.Spec.TopologyName]
							cqCache.AddOrUpdateTopology(log, &t)
						}
					}
					for _, cq := range tc.clusterQueues {
						if err := cqCache.AddClusterQueue(ctx, &cq); err != nil {
							t.Fatalf("Inserting clusterQueue %s in cache: %v", cq.Name, err)
						}
						if err := qManager.AddClusterQueue(ctx, &cq); err != nil {
							t.Fatalf("Inserting clusterQueue %s in manager: %v", cq.Name, err)
						}
						if err := cl.Create(ctx, &cq); err != nil {
							t.Fatalf("couldn't create the cluster queue: %v", err)
						}
					}
					for _, q := range queues {
						if err := qManager.AddLocalQueue(ctx, &q); err != nil {
							t.Fatalf("Inserting queue %s/%s in manager: %v", q.Namespace, q.Name, err)
						}
					}
					for _, pod := range tc.pods {
						cqCache.TASCache().Update(&pod, log)
					}
					initiallyAdmittedWorkloads := sets.New[workload.Reference]()
					for _, w := range testWls {
						if workload.IsAdmitted(&w) && !workload.HasUnhealthyNodes(&w) {
							initiallyAdmittedWorkloads.Insert(workload.Key(&w))
						}
					}
					// Reserved workloads must contribute their usage to the snapshot, mirroring production.
					for i := range testWls {
						if workload.HasQuotaReservation(&testWls[i]) {
							cqCache.AddOrUpdateWorkload(log, &testWls[i])
						}
					}
					for _, w := range testWls {
						if qManager.QueueSecondPassIfNeeded(ctx, &w, 0) {
							fakeClock.Step(time.Second)
						}
					}
					scheduler := New(qManager, cqCache, cl, recorder, WithClock(t, fakeClock), WithPreemptionExpectations(preemptexpectations.New()))
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
					for cqName, c := range snapshot.ClusterQueues() {
						for name, w := range c.Workloads {
							if initiallyAdmittedWorkloads.Has(workload.Key(w.Obj)) {
								continue
							}
							switch {
							case !workload.HasQuotaReservation(w.Obj):
								t.Fatalf("Workload %s is not admitted by a clusterQueue, but it is found as member of clusterQueue %s in the cache", name, cqName)
							case w.Obj.Status.Admission.ClusterQueue != cqName:
								t.Fatalf("Workload %s is admitted by clusterQueue %s, but it is found as member of clusterQueue %s in the cache", name, w.Obj.Status.Admission.ClusterQueue, cqName)
							default:
								gotAssignments[name] = *w.Obj.Status.Admission
							}
						}
					}
					if diff := cmp.Diff(tc.wantNewAssignments, gotAssignments, cmpopts.EquateEmpty()); diff != "" {
						t.Errorf("Unexpected assigned clusterQueues in cache (-want,+got):\n%s", diff)
					}
					qDump := qManager.Dump()
					if diff := cmp.Diff(tc.wantLeft, qDump, cmpDump...); diff != "" {
						t.Errorf("Unexpected elements left in the queue (-want,+got):\n%s", diff)
					}
					qDumpInadmissible := qManager.DumpInadmissible()
					if diff := cmp.Diff(tc.wantInadmissibleLeft, qDumpInadmissible, cmpDump...); diff != "" {
						t.Errorf("Unexpected elements left in inadmissible workloads (-want,+got):\n%s", diff)
					}
					var wantEvents []utiltesting.EventRecord
					if tc.wantEvents != nil {
						wantEvents = make([]utiltesting.EventRecord, len(tc.wantEvents))
						copy(wantEvents, tc.wantEvents)
						if !scenario[features.UnadmittedWorkloadsObservability] {
							utiltesting.AdjustEventsForDisabledObservabilityInScheduler(wantEvents)
						}
					}
					if diff := cmp.Diff(wantEvents, recorder.RecordedEvents, tc.eventCmpOpts...); diff != "" {
						t.Errorf("unexpected events (-want/+got):\n%s", diff)
					}
				},
			)
		}
	}
}
