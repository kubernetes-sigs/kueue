/*
Copyright 2022 The Kubernetes Authors.

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
	"sort"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha2"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/queue"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption"
	"sigs.k8s.io/kueue/pkg/util/api"
	"sigs.k8s.io/kueue/pkg/util/routine"
	"sigs.k8s.io/kueue/pkg/workload"
)

const (
	errCouldNotAdmitWL = "Could not admit Workload and assign flavors in apiserver"
)

type Scheduler struct {
	queues                  *queue.Manager
	cache                   *cache.Cache
	client                  client.Client
	recorder                record.EventRecorder
	admissionRoutineWrapper routine.Wrapper
	preemptor               *preemption.Preemptor
	waitForPodsReady        bool
	// Stubs.
	applyAdmission func(context.Context, *kueue.Workload) error
}

type options struct {
	waitForPodsReady bool
}

// Option configures the reconciler.
type Option func(*options)

// WithWaitForPodsReady indicates if the controller should wait for the
// PodsReady condition for all admitted workloads before admitting a new one.
func WithWaitForPodsReady(f bool) Option {
	return func(o *options) {
		o.waitForPodsReady = f
	}
}

var defaultOptions = options{}

func New(queues *queue.Manager, cache *cache.Cache, cl client.Client, recorder record.EventRecorder, opts ...Option) *Scheduler {
	options := defaultOptions
	for _, opt := range opts {
		opt(&options)
	}
	s := &Scheduler{
		queues:                  queues,
		cache:                   cache,
		client:                  cl,
		recorder:                recorder,
		preemptor:               preemption.New(cl, recorder),
		admissionRoutineWrapper: routine.DefaultWrapper,
		waitForPodsReady:        options.waitForPodsReady,
	}
	s.applyAdmission = s.applyAdmissionWithSSA
	return s
}

// Start implements the Runnable interface to run scheduler as a controller.
func (s *Scheduler) Start(ctx context.Context) error {
	log := ctrl.LoggerFrom(ctx).WithName("scheduler")
	ctx = ctrl.LoggerInto(ctx, log)
	go wait.UntilWithContext(ctx, s.schedule, 0)
	return nil
}

// NeedLeaderElection Implements LeaderElectionRunnable interface to make scheduler
// run in leader election mode
func (s *Scheduler) NeedLeaderElection() bool {
	return true
}

func (s *Scheduler) setAdmissionRoutineWrapper(wrapper routine.Wrapper) {
	s.admissionRoutineWrapper = wrapper
}

func (s *Scheduler) schedule(ctx context.Context) {
	log := ctrl.LoggerFrom(ctx)

	// 1. Get the heads from the queues, including their desired clusterQueue.
	// This operation blocks while the queues are empty.
	headWorkloads := s.queues.Heads(ctx)
	// No elements means the program is finishing.
	if len(headWorkloads) == 0 {
		return
	}
	startTime := time.Now()

	// 2. Take a snapshot of the cache.
	snapshot := s.cache.Snapshot()

	// 3. Calculate requirements (resource flavors, borrowing) for admitting workloads.
	entries := s.nominate(ctx, headWorkloads, snapshot)

	// 4. Sort entries based on borrowing and timestamps.
	sort.Sort(entryOrdering(entries))

	// 5. Admit entries, ensuring that no more than one workload gets
	// admitted by a cohort (if borrowing).
	// This is because there can be other workloads deeper in a clusterQueue whose
	// head got admitted that should be scheduled in the cohort before the heads
	// of other clusterQueues.
	usedCohorts := sets.New[string]()
	for i := range entries {
		e := &entries[i]
		if e.assignment.RepresentativeMode() == flavorassigner.NoFit {
			continue
		}
		cq := snapshot.ClusterQueues[e.ClusterQueue]
		if e.assignment.Borrows() && cq.Cohort != nil && usedCohorts.Has(cq.Cohort.Name) {
			e.status = skipped
			e.inadmissibleMsg = "workloads in the cohort that don't require borrowing were prioritized and admitted first"
			continue
		}
		// Even if there was a failure, we shouldn't admit other workloads to this
		// cohort.
		if cq.Cohort != nil {
			usedCohorts.Insert(cq.Cohort.Name)
		}
		log := log.WithValues("workload", klog.KObj(e.Obj), "clusterQueue", klog.KRef("", e.ClusterQueue))
		ctx := ctrl.LoggerInto(ctx, log)
		if e.assignment.RepresentativeMode() != flavorassigner.Fit {
			preempted, err := s.preemptor.Do(ctx, e.Info, e.assignment, &snapshot)
			if err != nil {
				log.Error(err, "Failed to preempt workloads")
			}
			if preempted != 0 {
				e.inadmissibleMsg += fmt.Sprintf(". Preempted %d workload(s)", preempted)
			}
			continue
		}
		if s.waitForPodsReady {
			if !s.cache.PodsReadyForAllAdmittedWorkloads(ctx) {
				log.V(5).Info("Waiting for all admitted workloads to be in the PodsReady condition")
				// Block admission until all currently admitted workloads are in
				// PodsReady condition if the waitForPodsReady is enabled
				if err := workload.UpdateStatus(ctx, s.client, e.Obj, kueue.WorkloadAdmitted, metav1.ConditionFalse, "Waiting", "waiting for all admitted workloads to be in PodsReady condition"); err != nil {
					log.Error(err, "Could not update Workload status")
				}
				s.cache.WaitForPodsReady(ctx)
				log.V(5).Info("Finished waiting for all admitted workloads to be in the PodsReady condition")
			}
		}
		e.status = nominated
		if err := s.admit(ctx, e); err != nil {
			e.inadmissibleMsg = fmt.Sprintf("Failed to admit workload: %v", err)
		}
	}

	// 6. Requeue the heads that were not scheduled.
	result := metrics.AdmissionResultInadmissible
	for _, e := range entries {
		log.V(3).Info("Workload evaluated for admission",
			"workload", klog.KObj(e.Obj),
			"clusterQueue", klog.KRef("", e.ClusterQueue),
			"status", e.status,
			"reason", e.inadmissibleMsg)
		if e.status != assumed {
			s.requeueAndUpdate(log, ctx, e)
		} else {
			result = metrics.AdmissionResultSuccess
		}
	}
	metrics.AdmissionAttempt(result, time.Since(startTime))
}

type entryStatus string

const (
	// indicates if the workload was nominated for admission.
	nominated entryStatus = "nominated"
	// indicates if the workload was skipped in this cycle.
	skipped entryStatus = "skipped"
	// indicates if the workload was assumed to have been admitted.
	assumed entryStatus = "assumed"
	// indicates that the workload was never nominated for admission.
	notNominated entryStatus = ""
)

// entry holds requirements for a workload to be admitted by a clusterQueue.
type entry struct {
	// workload.Info holds the workload from the API as well as resource usage
	// and flavors assigned.
	workload.Info
	assignment      flavorassigner.Assignment
	status          entryStatus
	inadmissibleMsg string
	requeueReason   queue.RequeueReason
}

// nominate returns the workloads with their requirements (resource flavors, borrowing) if
// they were admitted by the clusterQueues in the snapshot.
func (s *Scheduler) nominate(ctx context.Context, workloads []workload.Info, snap cache.Snapshot) []entry {
	log := ctrl.LoggerFrom(ctx)
	entries := make([]entry, 0, len(workloads))
	for _, w := range workloads {
		log := log.WithValues("workload", klog.KObj(w.Obj), "clusterQueue", klog.KRef("", w.ClusterQueue))
		cq := snap.ClusterQueues[w.ClusterQueue]
		ns := corev1.Namespace{}
		e := entry{Info: w}
		if snap.InactiveClusterQueueSets.Has(w.ClusterQueue) {
			e.inadmissibleMsg = fmt.Sprintf("ClusterQueue %s is inactive", w.ClusterQueue)
		} else if cq == nil {
			e.inadmissibleMsg = fmt.Sprintf("ClusterQueue %s not found", w.ClusterQueue)
		} else if err := s.client.Get(ctx, types.NamespacedName{Name: w.Obj.Namespace}, &ns); err != nil {
			e.inadmissibleMsg = fmt.Sprintf("Could not obtain workload namespace: %v", err)
		} else if !cq.NamespaceSelector.Matches(labels.Set(ns.Labels)) {
			e.inadmissibleMsg = "Workload namespace doesn't match ClusterQueue selector"
			e.requeueReason = queue.RequeueReasonNamespaceMismatch
		} else {
			e.assignment = flavorassigner.AssignFlavors(log, &e.Info, snap.ResourceFlavors, cq)
			e.inadmissibleMsg = e.assignment.Message()
		}
		entries = append(entries, e)
	}
	return entries
}

// admit sets the admitting clusterQueue and flavors into the workload of
// the entry, and asynchronously updates the object in the apiserver after
// assuming it in the cache.
func (s *Scheduler) admit(ctx context.Context, e *entry) error {
	log := ctrl.LoggerFrom(ctx)
	newWorkload := e.Obj.DeepCopy()
	admission := &kueue.Admission{
		ClusterQueue:  kueue.ClusterQueueReference(e.ClusterQueue),
		PodSetFlavors: e.assignment.ToAPI(),
	}
	newWorkload.Spec.Admission = admission
	if err := s.cache.AssumeWorkload(newWorkload); err != nil {
		return err
	}
	e.status = assumed
	log.V(2).Info("Workload assumed in the cache")

	s.admissionRoutineWrapper.Run(func() {
		err := s.applyAdmission(ctx, workload.AdmissionPatch(newWorkload))
		if err == nil {
			waitTime := time.Since(e.Obj.CreationTimestamp.Time)
			s.recorder.Eventf(newWorkload, corev1.EventTypeNormal, "Admitted", "Admitted by ClusterQueue %v, wait time was %.3fs", admission.ClusterQueue, waitTime.Seconds())
			metrics.AdmittedWorkload(admission.ClusterQueue, waitTime)
			log.V(2).Info("Workload successfully admitted and assigned flavors")
			return
		}
		// Ignore errors because the workload or clusterQueue could have been deleted
		// by an event.
		_ = s.cache.ForgetWorkload(newWorkload)
		if errors.IsNotFound(err) {
			log.V(2).Info("Workload not admitted because it was deleted")
			return
		}

		log.Error(err, errCouldNotAdmitWL)
		s.requeueAndUpdate(log, ctx, *e)
	})

	return nil
}

func (s *Scheduler) applyAdmissionWithSSA(ctx context.Context, w *kueue.Workload) error {
	return s.client.Patch(ctx, w, client.Apply, client.FieldOwner(constants.AdmissionName))
}

type entryOrdering []entry

func (e entryOrdering) Len() int {
	return len(e)
}

func (e entryOrdering) Swap(i, j int) {
	e[i], e[j] = e[j], e[i]
}

// Less is the ordering criteria:
// 1. request under min quota before borrowing.
// 2. FIFO on creation timestamp.
func (e entryOrdering) Less(i, j int) bool {
	a := e[i]
	b := e[j]
	// 1. Request under min quota.
	aBorrows := a.assignment.Borrows()
	bBorrows := b.assignment.Borrows()
	if aBorrows != bBorrows {
		return !aBorrows
	}
	// 2. FIFO.
	return a.Obj.CreationTimestamp.Before(&b.Obj.CreationTimestamp)
}

func (s *Scheduler) requeueAndUpdate(log logr.Logger, ctx context.Context, e entry) {
	if e.status != notNominated && e.requeueReason == queue.RequeueReasonGeneric {
		// Failed after nomination is the only reason why a workload would be requeued downstream.
		e.requeueReason = queue.RequeueReasonFailedAfterNomination
	}
	added := s.queues.RequeueWorkload(ctx, &e.Info, e.requeueReason)
	log.V(2).Info("Workload re-queued", "workload", klog.KObj(e.Obj), "clusterQueue", klog.KRef("", e.ClusterQueue), "queue", klog.KRef(e.Obj.Namespace, e.Obj.Spec.QueueName), "requeueReason", e.requeueReason, "added", added)

	if e.status == notNominated {
		err := workload.UpdateStatus(ctx, s.client, e.Obj, kueue.WorkloadAdmitted, metav1.ConditionFalse, "Pending", e.inadmissibleMsg)
		if err != nil {
			log.Error(err, "Could not update Workload status")
		}
		s.recorder.Eventf(e.Obj, corev1.EventTypeNormal, "Pending", api.TruncateEventMessage(e.inadmissibleMsg))
	}
}
