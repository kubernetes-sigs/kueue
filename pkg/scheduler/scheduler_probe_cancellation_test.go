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
	"testing"
	"time"

	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/component-base/featuregate"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadfinish "sigs.k8s.io/kueue/pkg/workload/finish"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

var probeCancellationJobGVK = batchv1.SchemeGroupVersion.WithKind("Job")

// sliceTestClusterQueue is the ClusterQueue of the workload slice tests, with 8 CPUs of the "default" flavor.
func sliceTestClusterQueue() *utiltestingapi.ClusterQueueWrapper {
	return utiltestingapi.MakeClusterQueue("cq").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "8").Obj())
}

// newSliceTestScheduler builds the scheduler cache, the queue manager and the scheduler of the workload slice tests on cl.
// lq, when set, is added to the queue manager.
func newSliceTestScheduler(ctx context.Context, t *testing.T, cl client.Client, cq *kueue.ClusterQueue, lq *kueue.LocalQueue,
	newQueues func(*schdcache.Cache) *qcache.Manager, opts ...Option) (*schdcache.Cache, *qcache.Manager, *Scheduler) {
	t.Helper()
	cqCache := schdcache.New(cl)
	cqCache.AddOrUpdateResourceFlavor(logr.FromContextOrDiscard(ctx), utiltestingapi.MakeResourceFlavor("default").Obj())
	if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("Inserting clusterQueue in cache: %v", err)
	}
	qManager := newQueues(cqCache)
	if err := qManager.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("Inserting clusterQueue in manager: %v", err)
	}
	if lq != nil {
		if err := qManager.AddLocalQueue(ctx, lq); err != nil {
			t.Fatalf("Inserting localQueue in manager: %v", err)
		}
	}
	opts = append(opts, WithPreemptionExpectations(preemptexpectations.New()))
	return cqCache, qManager, New(qManager, cqCache, cl, &utiltesting.EventRecorder{}, opts...)
}

// syncAdmissionWrapper runs the admission routine in the scheduling goroutine, after an optional hook.
// The hook runs once the workload is assumed and before its admission is written, which is where a concurrent writer races with it.
type syncAdmissionWrapper struct {
	beforeWrite func()
}

func (w *syncAdmissionWrapper) Run(f func()) {
	if hook := w.beforeWrite; hook != nil {
		w.beforeWrite = nil
		hook()
	}
	f()
}

// probeCancellationEnv drives the scheduler and the workload slice manager of the job reconciler on one fake API server.
type probeCancellationEnv struct {
	t         *testing.T
	ctx       context.Context
	log       logr.Logger
	cl        client.Client
	cqCache   *schdcache.Cache
	qManager  *qcache.Manager
	scheduler *Scheduler
	admission *syncAdmissionWrapper
	slices    *workloadslicing.Manager
	job       *batchv1.Job
	// staleList, when set, is what the job reconciler lists, as an informer cache that has not caught up would.
	staleList *kueue.WorkloadList
	// beforeDelete, when set, runs once right before a workload delete request reaches the fake API server.
	beforeDelete func()
	// setCancelled, called with true, makes the job reconciler cancel the probe, and called with false makes it keep the probe again.
	setCancelled func(cancel bool)
}

func newProbeCancellationEnv(t *testing.T) *probeCancellationEnv {
	t.Helper()
	ctx, log := utiltesting.ContextWithLog(t)
	now := time.Now().Truncate(time.Second)
	fakeClock := testingclock.NewFakeClock(now)

	cq := sliceTestClusterQueue().Obj()
	lq := utiltestingapi.MakeLocalQueue("lq", metav1.NamespaceDefault).ClusterQueue("cq").Obj()
	job := &batchv1.Job{}
	job.Name, job.Namespace, job.UID = "job", metav1.NamespaceDefault, "job-uid"
	predecessor := utiltestingapi.MakeWorkload("old", metav1.NamespaceDefault).
		OwnerReference(probeCancellationJobGVK, job.Name, string(job.UID)).
		Finalizers(kueue.ResourceInUseFinalizerName).
		Queue("lq").
		Creation(now.Add(-time.Minute)).
		Annotation(kueue.WorkloadSliceNameAnnotation, "old").
		Annotation(constants.PartialPreemptionAnnotation, "true").
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").Obj()).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(
			utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
				Assignment(corev1.ResourceCPU, "default", "2").Count(2).Obj(),
		).Obj(), now).
		AdmittedAt(true, now).
		Obj()
	probe := utiltestingapi.MakeWorkload("new", metav1.NamespaceDefault).
		OwnerReference(probeCancellationJobGVK, job.Name, string(job.UID)).
		Finalizers(kueue.ResourceInUseFinalizerName).
		Queue("lq").
		Creation(now).
		Annotation(kueue.WorkloadSliceNameAnnotation, "old").
		Annotation(workloadslicing.WorkloadSliceReplacementFor, "default/old").
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 4).Request(corev1.ResourceCPU, "1").Obj()).
		Obj()

	env := &probeCancellationEnv{t: t, ctx: ctx, log: log, job: job, admission: &syncAdmissionWrapper{}}
	env.cl = utiltesting.NewClientBuilder().
		WithLists(&kueue.WorkloadList{Items: []kueue.Workload{*predecessor, *probe}}, &kueue.LocalQueueList{Items: []kueue.LocalQueue{*lq}}).
		WithObjects(utiltesting.MakeNamespace(metav1.NamespaceDefault), utiltestingapi.MakeResourceFlavor("default").Obj(), cq).
		WithStatusSubresource(&kueue.Workload{}).
		WithIndex(&kueue.Workload{}, indexer.OwnerReferenceIndexKey(probeCancellationJobGVK), indexer.WorkloadOwnerIndexFunc(probeCancellationJobGVK)).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if wlList, ok := list.(*kueue.WorkloadList); ok && env.staleList != nil {
					env.staleList.DeepCopyInto(wlList)
					return nil
				}
				return c.List(ctx, list, opts...)
			},
			Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				if hook := env.beforeDelete; hook != nil {
					env.beforeDelete = nil
					hook()
				}
				return c.Delete(ctx, obj, opts...)
			},
			SubResourceApply: utiltesting.TreatSSAAsStrategicMergeForApplyConfiguration,
		}).
		Build()

	env.cqCache, env.qManager, env.scheduler = newSliceTestScheduler(ctx, t, env.cl, cq, lq, func(c *schdcache.Cache) *qcache.Manager {
		return qcache.NewManagerForUnitTests(env.cl, c, qcache.WithClock(fakeClock), qcache.WithPreemptionExpectations(preemptexpectations.New()))
	}, WithClock(t, fakeClock))
	if !env.cqCache.AddOrUpdateWorkload(ctx, log, predecessor) {
		t.Fatal("Inserting the predecessor in cache failed")
	}
	env.scheduler.setAdmissionRoutineWrapper(env.admission)
	env.slices = &workloadslicing.Manager{Client: env.cl, Clock: fakeClock}
	return env
}

// runCycle runs one scheduling cycle on the probe, which must be waiting in the queue.
// A cycle with nothing queued would wait for heads, so that is reported as a failure instead.
func (env *probeCancellationEnv) runCycle() {
	env.t.Helper()
	if got := env.qManager.Dump()["cq"]; len(got) != 1 || got[0] != "default/new" {
		env.t.Fatalf("The probe is not waiting in the queue: active %v, inadmissible %v", env.qManager.Dump(), env.qManager.DumpInadmissible())
	}
	ctx, cancel := context.WithTimeout(env.ctx, 5*time.Second)
	defer cancel()
	env.scheduler.schedule(ctx)
}

// reconcileJob runs the workload slice step of the job reconciler for a job that asks for 4 Pods.
func (env *probeCancellationEnv) reconcileJob() (*kueue.Workload, error) {
	podSets := []kueue.PodSet{*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 4).Request(corev1.ResourceCPU, "1").Obj()}
	wl, _, err := env.slices.EnsureWorkloadSlices(env.ctx, podSets, env.job, probeCancellationJobGVK)
	return wl, err
}

// setPredecessorReclaimPending requests, or completes, a partial preemption of the predecessor down to 1 Pod.
// While it is pending, the job reconciler withdraws the scale-up probe, see normalizeActiveSlices.
func (env *probeCancellationEnv) setPredecessorReclaimPending(pending bool) {
	env.t.Helper()
	wl := env.get("old")
	wl.Status.Admission.PodSetAssignments[0].PartialPreemption = nil
	if pending {
		wl.Status.Admission.PodSetAssignments[0].PartialPreemption = &kueue.PodSetPartialPreemptionStatus{RetainedCount: new(int32(1))}
	}
	if err := env.cl.Status().Update(env.ctx, wl); err != nil {
		env.t.Fatalf("Updating the partial preemption of the predecessor: %v", err)
	}
}

// setPredecessorActive deactivates or reactivates the predecessor, as a user would.
func (env *probeCancellationEnv) setPredecessorActive(active bool) {
	env.t.Helper()
	wl := env.get("old")
	wl.Spec.Active = &active
	if err := env.cl.Update(env.ctx, wl); err != nil {
		env.t.Fatalf("Updating spec.active of the predecessor: %v", err)
	}
}

func (env *probeCancellationEnv) get(name string) *kueue.Workload {
	env.t.Helper()
	wl := &kueue.Workload{}
	if err := env.cl.Get(env.ctx, client.ObjectKey{Namespace: metav1.NamespaceDefault, Name: name}, wl); err != nil {
		env.t.Fatalf("Getting workload %q: %v", name, err)
	}
	return wl
}

func (env *probeCancellationEnv) probeExists() bool {
	env.t.Helper()
	err := env.cl.Get(env.ctx, client.ObjectKey{Namespace: metav1.NamespaceDefault, Name: "new"}, &kueue.Workload{})
	if err != nil && !apierrors.IsNotFound(err) {
		env.t.Fatalf("Getting the probe: %v", err)
	}
	return err == nil
}

// refreshQueuedProbe hands the queue the probe as stored, as the workload informer would.
func (env *probeCancellationEnv) refreshQueuedProbe() {
	env.t.Helper()
	if err := env.qManager.AddOrUpdateWorkload(env.ctx, env.log, env.get("new")); err != nil {
		env.t.Fatalf("Updating the probe in the queue: %v", err)
	}
}

func assertProbeProtected(t *testing.T, probe *kueue.Workload) {
	t.Helper()
	if !workload.HasQuotaReservation(probe) {
		t.Error("The admitted probe lost its quota reservation")
	}
	if !controllerutil.ContainsFinalizer(probe, kueue.ResourceInUseFinalizerName) {
		t.Error("The admitted probe lost its finalizer")
	}
	if !probe.DeletionTimestamp.IsZero() {
		t.Error("The admitted probe is being deleted")
	}
}

func fmtWorkload(wl *kueue.Workload) string {
	return fmt.Sprintf("reserved=%v deleting=%v finalizers=%v", workload.HasQuotaReservation(wl), !wl.DeletionTimestamp.IsZero(), wl.Finalizers)
}

// TestProbeCancellationRaces covers the admission of a pending scale-up probe racing with its cancellation by the job reconciler.
// Each scenario runs with both admission patch modes, and with each way the job reconciler is made to cancel the probe.
func TestProbeCancellationRaces(t *testing.T) {
	triggers := map[string]func(env *probeCancellationEnv, cancel bool){
		"withdrawn": (*probeCancellationEnv).setPredecessorReclaimPending,
		"deactivated": func(env *probeCancellationEnv, cancel bool) {
			env.setPredecessorActive(!cancel)
		},
	}
	scenarios := map[string]struct {
		// noTrigger runs the scenario once per patch mode, without a cancellation.
		noTrigger bool
		run       func(t *testing.T, env *probeCancellationEnv)
	}{
		// An admission lands before the job reconciler cancels the probe.
		// The job reconciler decided from a copy that it read before the admission, as an informer cache that has not caught up would.
		// Its deletion is conditioned on that copy, so it fails and the admitted probe keeps its reservation and finalizer.
		"AdmissionWinsOverCancellation": {run: func(t *testing.T, env *probeCancellationEnv) {
			env.admission.beforeWrite = func() {
				env.setCancelled(true)
				stale := &kueue.WorkloadList{}
				if err := env.cl.List(env.ctx, stale); err != nil {
					t.Fatalf("Listing workloads: %v", err)
				}
				env.staleList = stale
			}
			env.runCycle()

			if probe := env.get("new"); !workload.HasQuotaReservation(probe) {
				t.Fatalf("The probe was not admitted: %s", fmtWorkload(probe))
			}
			if _, err := env.reconcileJob(); !apierrors.IsConflict(err) {
				t.Errorf("Cancelling the admitted probe from a stale copy returned %v, want a Conflict", err)
			}
			env.staleList = nil
			assertProbeProtected(t, env.get("new"))
		}},
		// An admission from a fresh scheduling cycle lands after the job reconciler read the probe.
		// The cancellation trigger is undone in between, so the scheduler admits the probe again.
		// The deletion requested right after is conditioned on the copy that was read, so the admitted probe keeps its reservation and finalizer.
		// Removing the finalizer before the delete request would let that fresh admission land on an unprotected probe, which the delete request then removes.
		"AdmissionWinsOverCancellationAfterUndo": {run: func(t *testing.T, env *probeCancellationEnv) {
			env.setCancelled(true)
			env.beforeDelete = func() {
				env.setCancelled(false)
				env.refreshQueuedProbe()
				env.runCycle()
				if probe := env.get("new"); !workload.HasQuotaReservation(probe) {
					t.Fatalf("The probe was not admitted after the cancellation trigger was undone: %s", fmtWorkload(probe))
				}
			}
			if _, err := env.reconcileJob(); !apierrors.IsConflict(err) {
				t.Errorf("Cancelling the admitted probe returned %v, want a Conflict", err)
			}
			assertProbeProtected(t, env.get("new"))
		}},
		// A cancellation lands while the admission of the probe is in flight.
		// The admission was computed from the copy that the scheduler nominated, so it fails with Conflict instead of reserving a probe that is being deleted.
		// The job reconciler then removes the finalizer, and the probe is gone.
		"CancellationInvalidatesInflightAdmission": {run: func(t *testing.T, env *probeCancellationEnv) {
			env.admission.beforeWrite = func() {
				env.setCancelled(true)
				if _, err := env.reconcileJob(); err != nil {
					t.Fatalf("Cancelling the probe: %v", err)
				}
				if probe := env.get("new"); probe.DeletionTimestamp.IsZero() || !controllerutil.ContainsFinalizer(probe, kueue.ResourceInUseFinalizerName) {
					t.Fatalf("The cancellation did not delete the probe while keeping its finalizer: %s", fmtWorkload(probe))
				}
			}
			env.runCycle()

			probe := env.get("new")
			if workload.HasQuotaReservation(probe) {
				t.Fatalf("The admission landed on a probe that is being deleted: %s", fmtWorkload(probe))
			}
			if env.cqCache.IsAdded(*workload.NewInfo(env.log, probe)) {
				t.Error("The scheduler cache still holds the assumed probe")
			}
			if _, err := env.reconcileJob(); err != nil {
				t.Fatalf("Finishing the cancellation: %v", err)
			}
			if env.probeExists() {
				t.Errorf("The probe still exists after its cancellation: %s", fmtWorkload(env.get("new")))
			}
		}},
		// A cancellation stops after the deletion request, and its trigger is then undone.
		// The deletion cannot be undone, so the probe stays out of admission.
		// The next reconcile of the job finishes the cancellation instead of taking the deleted probe as the job's workload.
		// The job reconciler then creates a new probe, which is admitted as usual.
		"CancellationInterruptedAndUndone": {run: func(t *testing.T, env *probeCancellationEnv) {
			env.setCancelled(true)
			if _, err := env.reconcileJob(); err != nil {
				t.Fatalf("Cancelling the probe: %v", err)
			}
			env.setCancelled(false)
			env.refreshQueuedProbe()
			env.runCycle()

			if probe := env.get("new"); workload.HasQuotaReservation(probe) {
				t.Fatalf("A fresh admission reserved a probe that is being deleted: %s", fmtWorkload(probe))
			}
			wl, err := env.reconcileJob()
			if err != nil {
				t.Fatalf("Finishing the cancellation: %v", err)
			}
			// The predecessor is admitted with 2 Pods and nothing cancels a scale-up anymore, so the job reconciler asks for a new probe.
			if wl != nil {
				t.Errorf("The job reconciler got workload %q, want none so that a new probe is created", wl.Name)
			}
			if env.probeExists() {
				t.Errorf("The probe still exists after its cancellation: %s", fmtWorkload(env.get("new")))
			}
		}},
		// The probe changes between its nomination and its admission write for a reason unrelated to cancellation.
		// The computed admission is dropped, and the next cycle nominates the probe again from a fresh copy and admits it.
		"AdmissionRetriedAfterBenignConflict": {noTrigger: true, run: func(t *testing.T, env *probeCancellationEnv) {
			env.admission.beforeWrite = func() {
				probe := env.get("new")
				metav1.SetMetaDataLabel(&probe.ObjectMeta, "unrelated", "change")
				if err := env.cl.Update(env.ctx, probe); err != nil {
					t.Fatalf("Updating the probe: %v", err)
				}
			}
			env.runCycle()

			if probe := env.get("new"); workload.HasQuotaReservation(probe) {
				t.Fatalf("The admission computed from an outdated copy landed: %s", fmtWorkload(probe))
			}
			env.runCycle()

			assertProbeProtected(t, env.get("new"))
			if !workloadfinish.IsFinished(env.get("old")) {
				t.Error("The predecessor was not finished after its replacement was admitted")
			}
		}},
	}
	modes := map[string]map[featuregate.Feature]bool{
		"server-side apply": {features.WorkloadRequestUseMergePatch: false},
		"merge patch":       {features.WorkloadRequestUseMergePatch: true},
	}
	for modeName, gates := range modes {
		for name, scenario := range scenarios {
			scenarioTriggers := triggers
			if scenario.noTrigger {
				scenarioTriggers = map[string]func(*probeCancellationEnv, bool){"none": nil}
			}
			for triggerName, trigger := range scenarioTriggers {
				t.Run(modeName+"/"+name+"/"+triggerName, func(t *testing.T) {
					features.SetFeatureGatesDuringTest(t, gates)
					features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlices, true)
					features.SetFeatureGateDuringTest(t, features.PartialPreemption, true)
					env := newProbeCancellationEnv(t)
					env.setCancelled = func(cancel bool) { trigger(env, cancel) }
					scenario.run(t, env)
				})
			}
		}
	}
}
