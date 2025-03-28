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
	"maps"
	"slices"
	"sort"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/queue"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption"
	"sigs.k8s.io/kueue/pkg/util/api"
	"sigs.k8s.io/kueue/pkg/util/priority"
	"sigs.k8s.io/kueue/pkg/util/routine"
	"sigs.k8s.io/kueue/pkg/util/wait"
	"sigs.k8s.io/kueue/pkg/workload"
)

const (
	errCouldNotAdmitWL                           = "Could not admit Workload and assign flavors in apiserver"
	errInvalidWLResources                        = "resources validation failed"
	errLimitRangeConstraintsUnsatisfiedResources = "resources didn't satisfy LimitRange constraints"
)

var (
	realClock = clock.RealClock{}
)

type Scheduler struct {
	queues                  *queue.Manager
	cache                   *cache.Cache
	client                  client.Client
	recorder                record.EventRecorder
	admissionRoutineWrapper routine.Wrapper
	preemptor               *preemption.Preemptor
	workloadOrdering        workload.Ordering
	fairSharing             config.FairSharing
	clock                   clock.Clock

	// schedulingCycle identifies the number of scheduling
	// attempts since the last restart.
	schedulingCycle int64

	// Stubs.
	applyAdmission func(context.Context, *kueue.Workload) error
}

type options struct {
	podsReadyRequeuingTimestamp config.RequeuingTimestamp
	fairSharing                 config.FairSharing
	clock                       clock.Clock
}

// Option configures the reconciler.
type Option func(*options)

var defaultOptions = options{
	podsReadyRequeuingTimestamp: config.EvictionTimestamp,
	clock:                       realClock,
}

// WithPodsReadyRequeuingTimestamp sets the timestamp that is used for ordering
// workloads that have been requeued due to the PodsReady condition.
func WithPodsReadyRequeuingTimestamp(ts config.RequeuingTimestamp) Option {
	return func(o *options) {
		o.podsReadyRequeuingTimestamp = ts
	}
}

func WithFairSharing(fs *config.FairSharing) Option {
	return func(o *options) {
		if fs != nil {
			o.fairSharing = *fs
		}
	}
}

func WithClock(_ testing.TB, c clock.Clock) Option {
	return func(o *options) {
		o.clock = c
	}
}

func New(queues *queue.Manager, cache *cache.Cache, cl client.Client, recorder record.EventRecorder, opts ...Option) *Scheduler {
	options := defaultOptions
	for _, opt := range opts {
		opt(&options)
	}
	wo := workload.Ordering{
		PodsReadyRequeuingTimestamp: options.podsReadyRequeuingTimestamp,
	}
	s := &Scheduler{
		fairSharing:             options.fairSharing,
		queues:                  queues,
		cache:                   cache,
		client:                  cl,
		recorder:                recorder,
		preemptor:               preemption.New(cl, wo, recorder, options.fairSharing, options.clock),
		admissionRoutineWrapper: routine.DefaultWrapper,
		workloadOrdering:        wo,
		clock:                   options.clock,
	}
	s.applyAdmission = s.applyAdmissionWithSSA
	return s
}

// Start implements the Runnable interface to run scheduler as a controller.
func (s *Scheduler) Start(ctx context.Context) error {
	log := ctrl.LoggerFrom(ctx).WithName("scheduler")
	ctx = ctrl.LoggerInto(ctx, log)
	go wait.UntilWithBackoff(ctx, s.schedule)
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

func setSkipped(e *entry, inadmissibleMsg string) {
	e.status = skipped
	e.inadmissibleMsg = inadmissibleMsg
	// Reset assignment so that we retry all flavors
	// after skipping due to Fit no longer fitting,
	// or Preempt being skipped due to an overlapping
	// earlier admission.
	e.LastAssignment = nil
}

func reportSkippedPreemptions(p map[kueue.ClusterQueueReference]int) {
	for cqName, count := range p {
		metrics.AdmissionCyclePreemptionSkips.WithLabelValues(string(cqName)).Set(float64(count))
	}
}

func (s *Scheduler) schedule(ctx context.Context) wait.SpeedSignal {
	s.schedulingCycle++
	log := ctrl.LoggerFrom(ctx).WithValues("schedulingCycle", s.schedulingCycle)
	ctx = ctrl.LoggerInto(ctx, log)

	// 1. Get the heads from the queues, including their desired clusterQueue.
	// This operation blocks while the queues are empty.
	headWorkloads := s.queues.Heads(ctx)
	// If there are no elements, it means that the program is finishing.
	if len(headWorkloads) == 0 {
		return wait.KeepGoing
	}
	startTime := s.clock.Now()

	// 2. Take a snapshot of the cache.
	snapshot, err := s.cache.Snapshot(ctx)
	if err != nil {
		log.Error(err, "failed to build snapshot for scheduling")
		return wait.SlowDown
	}
	logSnapshotIfVerbose(log, snapshot)

	// 3. Calculate requirements (resource flavors, borrowing) for admitting workloads.
	entries := s.nominate(ctx, headWorkloads, snapshot)

	// 4. Create iterator which returns ordered entries.
	iterator := makeIterator(ctx, entries, s.workloadOrdering, s.fairSharing.Enable)

	// 5. Admit entries, ensuring that no more than one workload gets
	// admitted by a cohort (if borrowing).
	// This is because there can be other workloads deeper in a clusterQueue whose
	// head got admitted that should be scheduled in the cohort before the heads
	// of other clusterQueues.
	preemptedWorkloads := make(preemption.PreemptedWorkloads)
	skippedPreemptions := make(map[kueue.ClusterQueueReference]int)
	for iterator.hasNext() {
		e := iterator.pop()

		cq := snapshot.ClusterQueue(e.ClusterQueue)
		log := log.WithValues("workload", klog.KObj(e.Obj), "clusterQueue", klog.KRef("", string(e.ClusterQueue)))
		if cq.HasParent() {
			log = log.WithValues("parentCohort", klog.KRef("", string(cq.Parent().GetName())), "rootCohort", klog.KRef("", string(cq.Parent().Root().GetName())))
		}
		ctx := ctrl.LoggerInto(ctx, log)

		mode := e.assignment.RepresentativeMode()
		if mode == flavorassigner.NoFit {
			log.V(3).Info("Skipping workload as FlavorAssigner assigned NoFit mode")
			continue
		}
		log.V(2).Info("Attempting to schedule workload")

		if mode == flavorassigner.Preempt && len(e.preemptionTargets) == 0 {
			log.V(2).Info("Workload requires preemption, but there are no candidate workloads allowed for preemption", "preemption", cq.Preemption)
			// we reserve capacity if we are uncertain
			// whether we can reclaim the capacity
			// later. Otherwise, we allow other workloads
			// in the Cohort to borrow this capacity,
			// confident we can reclaim it later.
			if !preemption.CanAlwaysReclaim(cq) {
				// reserve capacity up to the
				// borrowing limit, so that
				// lower-priority workloads in another
				// Cohort cannot admit before us.
				cq.AddUsage(resourcesToReserve(e, cq))
			}
			continue
		}

		// We skip multiple-preemptions per cohort if any of the targets are overlapping
		if preemptedWorkloads.HasAny(e.preemptionTargets) {
			setSkipped(e, "Workload has overlapping preemption targets with another workload")
			skippedPreemptions[cq.Name]++
			continue
		}

		usage := e.assignmentUsage()
		if !fits(cq, &usage, preemptedWorkloads, e.preemptionTargets) {
			setSkipped(e, "Workload no longer fits after processing another workload")
			if mode == flavorassigner.Preempt {
				skippedPreemptions[cq.Name]++
			}
			continue
		}
		preemptedWorkloads.Insert(e.preemptionTargets)
		cq.AddUsage(usage)

		if e.assignment.RepresentativeMode() == flavorassigner.Preempt {
			// If preemptions are issued, the next attempt should try all the flavors.
			e.LastAssignment = nil
			preempted, err := s.preemptor.IssuePreemptions(ctx, &e.Info, e.preemptionTargets)
			if err != nil {
				log.Error(err, "Failed to preempt workloads")
			}
			if preempted != 0 {
				e.inadmissibleMsg += fmt.Sprintf(". Pending the preemption of %d workload(s)", preempted)
				e.requeueReason = queue.RequeueReasonPendingPreemption
			}
			continue
		}
		if !s.cache.PodsReadyForAllAdmittedWorkloads(log) {
			log.V(5).Info("Waiting for all admitted workloads to be in the PodsReady condition")
			// If WaitForPodsReady is enabled and WaitForPodsReady.BlockAdmission is true
			// Block admission until all currently admitted workloads are in
			// PodsReady condition if the waitForPodsReady is enabled
			workload.UnsetQuotaReservationWithCondition(e.Obj, "Waiting", "waiting for all admitted workloads to be in PodsReady condition", s.clock.Now())
			if err := workload.ApplyAdmissionStatus(ctx, s.client, e.Obj, false, s.clock); err != nil {
				log.Error(err, "Could not update Workload status")
			}
			s.cache.WaitForPodsReady(ctx)
			log.V(5).Info("Finished waiting for all admitted workloads to be in the PodsReady condition")
		}
		e.status = nominated
		if err := s.admit(ctx, e, cq); err != nil {
			e.inadmissibleMsg = fmt.Sprintf("Failed to admit workload: %v", err)
		}
	}

	// 6. Requeue the heads that were not scheduled.
	result := metrics.AdmissionResultInadmissible
	for _, e := range entries {
		logAdmissionAttemptIfVerbose(log, &e)
		if e.status != assumed {
			s.requeueAndUpdate(ctx, e)
		} else {
			result = metrics.AdmissionResultSuccess
		}
	}
	reportSkippedPreemptions(skippedPreemptions)
	metrics.AdmissionAttempt(result, s.clock.Since(startTime))
	if result != metrics.AdmissionResultSuccess {
		return wait.SlowDown
	}
	return wait.KeepGoing
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
	assignment           flavorassigner.Assignment
	status               entryStatus
	inadmissibleMsg      string
	requeueReason        queue.RequeueReason
	preemptionTargets    []*preemption.Target
	clusterQueueSnapshot *cache.ClusterQueueSnapshot
}

func (e *entry) assignmentUsage() workload.Usage {
	return e.assignment.Usage
}

// nominate returns the workloads with their requirements (resource flavors, borrowing) if
// they were admitted by the clusterQueues in the snapshot.
func (s *Scheduler) nominate(ctx context.Context, workloads []workload.Info, snap *cache.Snapshot) []entry {
	log := ctrl.LoggerFrom(ctx)
	entries := make([]entry, 0, len(workloads))
	for _, w := range workloads {
		log := log.WithValues("workload", klog.KObj(w.Obj), "clusterQueue", klog.KRef("", string(w.ClusterQueue)))
		ns := corev1.Namespace{}
		e := entry{Info: w}
		e.clusterQueueSnapshot = snap.ClusterQueue(w.ClusterQueue)
		if s.cache.IsAssumedOrAdmittedWorkload(w) {
			log.Info("Workload skipped from admission because it's already assumed or admitted", "workload", klog.KObj(w.Obj))
			continue
		} else if workload.HasRetryChecks(w.Obj) || workload.HasRejectedChecks(w.Obj) {
			e.inadmissibleMsg = "The workload has failed admission checks"
		} else if snap.InactiveClusterQueueSets.Has(w.ClusterQueue) {
			e.inadmissibleMsg = fmt.Sprintf("ClusterQueue %s is inactive", w.ClusterQueue)
		} else if e.clusterQueueSnapshot == nil {
			e.inadmissibleMsg = fmt.Sprintf("ClusterQueue %s not found", w.ClusterQueue)
		} else if err := s.client.Get(ctx, types.NamespacedName{Name: w.Obj.Namespace}, &ns); err != nil {
			e.inadmissibleMsg = fmt.Sprintf("Could not obtain workload namespace: %v", err)
		} else if !e.clusterQueueSnapshot.NamespaceSelector.Matches(labels.Set(ns.Labels)) {
			e.inadmissibleMsg = "Workload namespace doesn't match ClusterQueue selector"
			e.requeueReason = queue.RequeueReasonNamespaceMismatch
		} else if err := workload.ValidateResources(&w); err != nil {
			e.inadmissibleMsg = fmt.Sprintf("%s: %v", errInvalidWLResources, err.ToAggregate())
		} else if err := workload.ValidateLimitRange(ctx, s.client, &w); err != nil {
			e.inadmissibleMsg = fmt.Sprintf("%s: %v", errLimitRangeConstraintsUnsatisfiedResources, err.ToAggregate())
		} else {
			e.assignment, e.preemptionTargets = s.getAssignments(log, &e.Info, snap)
			e.inadmissibleMsg = e.assignment.Message()
			e.Info.LastAssignment = &e.assignment.LastState
		}
		entries = append(entries, e)
	}
	return entries
}

func fits(cq *cache.ClusterQueueSnapshot, usage *workload.Usage, preemptedWorkloads preemption.PreemptedWorkloads, newTargets []*preemption.Target) bool {
	workloads := slices.Collect(maps.Values(preemptedWorkloads))
	for _, target := range newTargets {
		workloads = append(workloads, target.WorkloadInfo)
	}
	revertUsage := cq.SimulateWorkloadRemoval(workloads)
	defer revertUsage()
	return cq.Fits(*usage)
}

// resourcesToReserve calculates how much of the available resources in cq/cohort assignment should be reserved.
func resourcesToReserve(e *entry, cq *cache.ClusterQueueSnapshot) workload.Usage {
	return workload.Usage{
		Quota: quotaResourcesToReserve(e, cq),
		TAS:   e.assignment.Usage.TAS,
	}
}

func quotaResourcesToReserve(e *entry, cq *cache.ClusterQueueSnapshot) resources.FlavorResourceQuantities {
	if e.assignment.RepresentativeMode() != flavorassigner.Preempt {
		return e.assignment.Usage.Quota
	}
	reservedUsage := make(resources.FlavorResourceQuantities)
	for fr, usage := range e.assignment.Usage.Quota {
		cqQuota := cq.QuotaFor(fr)
		if e.assignment.Borrowing {
			if cqQuota.BorrowingLimit == nil {
				reservedUsage[fr] = usage
			} else {
				reservedUsage[fr] = min(usage, cqQuota.Nominal+*cqQuota.BorrowingLimit-cq.ResourceNode.Usage[fr])
			}
		} else {
			reservedUsage[fr] = max(0, min(usage, cqQuota.Nominal-cq.ResourceNode.Usage[fr]))
		}
	}
	return reservedUsage
}

type partialAssignment struct {
	assignment        flavorassigner.Assignment
	preemptionTargets []*preemption.Target
}

func (s *Scheduler) getAssignments(log logr.Logger, wl *workload.Info, snap *cache.Snapshot) (flavorassigner.Assignment, []*preemption.Target) {
	assignment, targets := s.getInitialAssignments(log, wl, snap)
	cq := snap.ClusterQueue(wl.ClusterQueue)
	updateAssignmentForTAS(cq, wl, &assignment, targets)
	return assignment, targets
}

func (s *Scheduler) getInitialAssignments(log logr.Logger, wl *workload.Info, snap *cache.Snapshot) (flavorassigner.Assignment, []*preemption.Target) {
	cq := snap.ClusterQueue(wl.ClusterQueue)
	flvAssigner := flavorassigner.New(wl, cq, snap.ResourceFlavors, s.fairSharing.Enable, preemption.NewOracle(s.preemptor, snap))
	fullAssignment := flvAssigner.Assign(log, nil)

	arm := fullAssignment.RepresentativeMode()
	if arm == flavorassigner.Fit {
		return fullAssignment, nil
	}

	if arm == flavorassigner.Preempt {
		faPreemptionTargets := s.preemptor.GetTargets(log, *wl, fullAssignment, snap)
		if len(faPreemptionTargets) > 0 {
			return fullAssignment, faPreemptionTargets
		}
	}

	if features.Enabled(features.PartialAdmission) && wl.CanBePartiallyAdmitted() {
		reducer := flavorassigner.NewPodSetReducer(wl.Obj.Spec.PodSets, func(nextCounts []int32) (*partialAssignment, bool) {
			assignment := flvAssigner.Assign(log, nextCounts)
			mode := assignment.RepresentativeMode()
			if mode == flavorassigner.Fit {
				return &partialAssignment{assignment: assignment}, true
			}

			if mode == flavorassigner.Preempt {
				preemptionTargets := s.preemptor.GetTargets(log, *wl, assignment, snap)
				if len(preemptionTargets) > 0 {
					return &partialAssignment{assignment: assignment, preemptionTargets: preemptionTargets}, true
				}
			}
			return nil, false
		})
		if pa, found := reducer.Search(); found {
			return pa.assignment, pa.preemptionTargets
		}
	}
	return fullAssignment, nil
}

func updateAssignmentForTAS(cq *cache.ClusterQueueSnapshot, wl *workload.Info, assignment *flavorassigner.Assignment, targets []*preemption.Target) {
	if features.Enabled(features.TopologyAwareScheduling) && assignment.RepresentativeMode() == flavorassigner.Preempt && (wl.IsRequestingTAS() || cq.IsTASOnly()) {
		tasRequests := assignment.WorkloadsTopologyRequests(wl, cq)
		var tasResult cache.TASAssignmentsResult
		if len(targets) > 0 {
			var targetWorkloads []*workload.Info
			for _, target := range targets {
				targetWorkloads = append(targetWorkloads, target.WorkloadInfo)
			}
			revertUsage := cq.SimulateWorkloadRemoval(targetWorkloads)
			tasResult = cq.FindTopologyAssignmentsForWorkload(tasRequests, false)
			revertUsage()
		} else {
			// In this scenario we don't have any preemption candidates, yet we need
			// to reserve the TAS resources to avoid the situation when a lower
			// priority workload further in the queue gets admitted and preempted
			// in the next scheduling cycle by the waiting workload. To obtain
			// a TAS assignment for reserving the resources we run the algorithm
			// assuming the cluster is empty.
			tasResult = cq.FindTopologyAssignmentsForWorkload(tasRequests, true)
		}
		assignment.UpdateForTASResult(tasResult)
	}
}

// admit sets the admitting clusterQueue and flavors into the workload of
// the entry, and asynchronously updates the object in the apiserver after
// assuming it in the cache.
func (s *Scheduler) admit(ctx context.Context, e *entry, cq *cache.ClusterQueueSnapshot) error {
	log := ctrl.LoggerFrom(ctx)
	newWorkload := e.Obj.DeepCopy()
	admission := &kueue.Admission{
		ClusterQueue:      e.ClusterQueue,
		PodSetAssignments: e.assignment.ToAPI(),
	}

	workload.SetQuotaReservation(newWorkload, admission, s.clock)
	if workload.HasAllChecks(newWorkload, workload.AdmissionChecksForWorkload(log, newWorkload, cq.AdmissionChecks)) {
		// sync Admitted, ignore the result since an API update is always done.
		_ = workload.SyncAdmittedCondition(newWorkload, s.clock.Now())
	}
	if err := s.cache.AssumeWorkload(newWorkload); err != nil {
		return err
	}
	e.status = assumed
	log.V(2).Info("Workload assumed in the cache")

	s.admissionRoutineWrapper.Run(func() {
		err := s.applyAdmission(ctx, newWorkload)
		if err == nil {
			waitTime := workload.QueuedWaitTime(newWorkload)
			s.recorder.Eventf(newWorkload, corev1.EventTypeNormal, "QuotaReserved", "Quota reserved in ClusterQueue %v, wait time since queued was %.0fs", admission.ClusterQueue, waitTime.Seconds())
			metrics.QuotaReservedWorkload(admission.ClusterQueue, waitTime)
			if features.Enabled(features.LocalQueueMetrics) {
				metrics.LocalQueueQuotaReservedWorkload(metrics.LQRefFromWorkload(newWorkload), waitTime)
			}
			if workload.IsAdmitted(newWorkload) {
				s.recorder.Eventf(newWorkload, corev1.EventTypeNormal, "Admitted", "Admitted by ClusterQueue %v, wait time since reservation was 0s", admission.ClusterQueue)
				metrics.AdmittedWorkload(admission.ClusterQueue, waitTime)
				if features.Enabled(features.LocalQueueMetrics) {
					metrics.LocalQueueAdmittedWorkload(metrics.LQRefFromWorkload(newWorkload), waitTime)
				}
				if len(newWorkload.Status.AdmissionChecks) > 0 {
					metrics.AdmissionChecksWaitTime(admission.ClusterQueue, 0)
					if features.Enabled(features.LocalQueueMetrics) {
						metrics.LocalQueueAdmissionChecksWaitTime(metrics.LQRefFromWorkload(newWorkload), 0)
					}
				}
			}
			log.V(2).Info("Workload successfully admitted and assigned flavors", "assignments", admission.PodSetAssignments)
			return
		}
		// Ignore errors because the workload or clusterQueue could have been deleted
		// by an event.
		_ = s.cache.ForgetWorkload(newWorkload)
		if apierrors.IsNotFound(err) {
			log.V(2).Info("Workload not admitted because it was deleted")
			return
		}

		log.Error(err, errCouldNotAdmitWL)
		s.requeueAndUpdate(ctx, *e)
	})

	return nil
}

func (s *Scheduler) applyAdmissionWithSSA(ctx context.Context, w *kueue.Workload) error {
	return workload.ApplyAdmissionStatus(ctx, s.client, w, false, s.clock)
}

type entryOrdering struct {
	entries          []entry
	workloadOrdering workload.Ordering
}

func (e entryOrdering) Len() int {
	return len(e.entries)
}

func (e entryOrdering) Swap(i, j int) {
	e.entries[i], e.entries[j] = e.entries[j], e.entries[i]
}

// Less is the ordering criteria
func (e entryOrdering) Less(i, j int) bool {
	a := e.entries[i]
	b := e.entries[j]

	// 1. Request under nominal quota.
	aBorrows := a.assignment.Borrows()
	bBorrows := b.assignment.Borrows()
	if aBorrows != bBorrows {
		return !aBorrows
	}

	// 2. Higher priority first if not disabled.
	if features.Enabled(features.PrioritySortingWithinCohort) {
		p1 := priority.Priority(a.Obj)
		p2 := priority.Priority(b.Obj)
		if p1 != p2 {
			return p1 > p2
		}
	}

	// 3. FIFO.
	aComparisonTimestamp := e.workloadOrdering.GetQueueOrderTimestamp(a.Obj)
	bComparisonTimestamp := e.workloadOrdering.GetQueueOrderTimestamp(b.Obj)
	return aComparisonTimestamp.Before(bComparisonTimestamp)
}

// entryInterator defines order that entries are returned.
// pop->nil IFF hasNext->False
type entryIterator interface {
	pop() *entry
	hasNext() bool
}

func makeIterator(ctx context.Context, entries []entry, workloadOrdering workload.Ordering, enableFairSharing bool) entryIterator {
	if enableFairSharing {
		return makeFairSharingIterator(ctx, entries, workloadOrdering)
	}
	return makeClassicalIterator(entries, workloadOrdering)
}

// classicalIterator returns entries ordered on:
// 1. request under nominal quota before borrowing.
// 2. Fair Sharing: lower DominantResourceShare first.
// 3. higher priority first.
// 4. FIFO on eviction or creation timestamp.
type classicalIterator struct {
	entries []entry
}

func (co *classicalIterator) hasNext() bool {
	return len(co.entries) > 0
}

func (co *classicalIterator) pop() *entry {
	head := &co.entries[0]
	co.entries = co.entries[1:]
	return head
}

func makeClassicalIterator(entries []entry, workloadOrdering workload.Ordering) *classicalIterator {
	sort.Sort(entryOrdering{
		entries:          entries,
		workloadOrdering: workloadOrdering,
	})
	return &classicalIterator{
		entries: entries,
	}
}

func (s *Scheduler) requeueAndUpdate(ctx context.Context, e entry) {
	log := ctrl.LoggerFrom(ctx)
	if e.status != notNominated && e.requeueReason == queue.RequeueReasonGeneric {
		// Failed after nomination is the only reason why a workload would be requeued downstream.
		e.requeueReason = queue.RequeueReasonFailedAfterNomination
	}
	added := s.queues.RequeueWorkload(ctx, &e.Info, e.requeueReason)
	log.V(2).Info("Workload re-queued", "workload", klog.KObj(e.Obj), "clusterQueue", klog.KRef("", string(e.ClusterQueue)), "queue", klog.KRef(e.Obj.Namespace, e.Obj.Spec.QueueName), "requeueReason", e.requeueReason, "added", added, "status", e.status)

	if e.status == notNominated || e.status == skipped {
		patch := workload.BaseSSAWorkload(e.Obj)
		workload.AdmissionStatusPatch(e.Obj, patch, true)
		reservationIsChanged := workload.UnsetQuotaReservationWithCondition(patch, "Pending", e.inadmissibleMsg, s.clock.Now())
		resourceRequestsIsChanged := workload.PropagateResourceRequests(patch, &e.Info)
		if reservationIsChanged || resourceRequestsIsChanged {
			if err := workload.ApplyAdmissionStatusPatch(ctx, s.client, patch); err != nil {
				log.Error(err, "Could not update Workload status")
			}
		}
		s.recorder.Eventf(e.Obj, corev1.EventTypeWarning, "Pending", api.TruncateEventMessage(e.inadmissibleMsg))
	}
}
