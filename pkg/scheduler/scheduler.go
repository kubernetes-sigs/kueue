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
	"strings"
	"testing"
	"time"

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

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/fairsharing"
	afs "sigs.k8s.io/kueue/pkg/util/admissionfairsharing"
	"sigs.k8s.io/kueue/pkg/util/api"
	"sigs.k8s.io/kueue/pkg/util/priority"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	"sigs.k8s.io/kueue/pkg/util/routine"
	"sigs.k8s.io/kueue/pkg/util/wait"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
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
	queues                  *qcache.Manager
	cache                   *schdcache.Cache
	client                  client.Client
	recorder                record.EventRecorder
	admissionRoutineWrapper routine.Wrapper
	preemptor               *preemption.Preemptor
	workloadOrdering        workload.Ordering
	fairSharing             *config.FairSharing
	admissionFairSharing    *config.AdmissionFairSharing
	clock                   clock.Clock
	roleTracker             *roletracker.RoleTracker

	// schedulingCycle identifies the number of scheduling
	// attempts since the last restart.
	schedulingCycle int64
}

type options struct {
	podsReadyRequeuingTimestamp config.RequeuingTimestamp
	fairSharing                 *config.FairSharing
	admissionFairSharing        *config.AdmissionFairSharing
	clock                       clock.Clock
	roleTracker                 *roletracker.RoleTracker
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
			o.fairSharing = fs
		}
	}
}

func WithAdmissionFairSharing(afs *config.AdmissionFairSharing) Option {
	return func(o *options) {
		o.admissionFairSharing = afs
	}
}

func WithClock(_ testing.TB, c clock.Clock) Option {
	return func(o *options) {
		o.clock = c
	}
}

// WithRoleTracker sets the role tracker for HA logging.
func WithRoleTracker(tracker *roletracker.RoleTracker) Option {
	return func(o *options) {
		o.roleTracker = tracker
	}
}

func New(queues *qcache.Manager, cache *schdcache.Cache, cl client.Client, recorder record.EventRecorder, opts ...Option) *Scheduler {
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
		preemptor:               preemption.New(cl, wo, recorder, options.fairSharing, afs.Enabled(options.admissionFairSharing), options.clock, options.roleTracker),
		admissionRoutineWrapper: routine.DefaultWrapper,
		workloadOrdering:        wo,
		clock:                   options.clock,
		admissionFairSharing:    options.admissionFairSharing,
		roleTracker:             options.roleTracker,
	}
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

func (s *Scheduler) reportSkippedPreemptions(p map[kueue.ClusterQueueReference]int) {
	for cqName, count := range p {
		metrics.AdmissionCyclePreemptionSkips.WithLabelValues(string(cqName), roletracker.GetRole(s.roleTracker)).Set(float64(count))
	}
}

func (s *Scheduler) schedule(ctx context.Context) wait.SpeedSignal {
	s.schedulingCycle++
	log := roletracker.WithReplicaRole(ctrl.LoggerFrom(ctx), s.roleTracker).WithValues("schedulingCycle", s.schedulingCycle)
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
	var snapshotOpts []schdcache.SnapshotOption
	if afs.Enabled(s.admissionFairSharing) {
		snapshotOpts = append(snapshotOpts, schdcache.WithAfsEntryPenalties(s.queues.AfsEntryPenalties))
		snapshotOpts = append(snapshotOpts, schdcache.WithAfsConsumedResources(s.queues.AfsConsumedResources))
	}
	snapshot, err := s.cache.Snapshot(ctx, snapshotOpts...)
	if err != nil {
		log.Error(err, "failed to build snapshot for scheduling")
		return wait.SlowDown
	}
	logSnapshotIfVerbose(log, snapshot)

	// 3. Calculate requirements (resource flavors, borrowing) for admitting workloads.
	entries, inadmissibleEntries := s.nominate(ctx, headWorkloads, snapshot)

	// 4. Create iterator which returns ordered entries.
	iterator := makeIterator(ctx, entries, s.workloadOrdering, fairsharing.Enabled(s.fairSharing))

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
		log.V(2).Info("Attempting to schedule workload")

		mode := e.assignment.RepresentativeMode()

		if features.Enabled(features.TASFailedNodeReplacementFailFast) && workload.HasTopologyAssignmentWithUnhealthyNode(e.Obj) && mode != flavorassigner.Fit {
			// evict workload we couldn't find the replacement for
			if err := s.evictWorkloadAfterFailedTASReplacement(ctx, log, e.Obj.DeepCopy()); client.IgnoreNotFound(err) != nil {
				log.V(2).Error(err, "Failed to evict workload")
				continue
			}
			e.status = evicted
			continue
		}

		if mode == flavorassigner.NoFit {
			log.V(3).Info("Skipping workload as FlavorAssigner assigned NoFit mode")
			continue
		}

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
		if !fits(snapshot, cq, &usage, preemptedWorkloads, e.preemptionTargets) {
			setSkipped(e, "Workload no longer fits after processing another workload")
			if mode == flavorassigner.Preempt {
				skippedPreemptions[cq.Name]++
			}
			continue
		}
		preemptedWorkloads.Insert(e.preemptionTargets)
		cq.AddUsage(usage)

		// Filter out the old workload slice from the preemption targets.
		// The old workload slice is initially included in the preemption targets because it is treated
		// as a preemptible target during flavor assignment. However, it should be evicted rather than preempted.
		// Note: it is valid for either or both preemptionTargets and oldWorkloadSlice to be nil.
		preemptionTargets, oldWorkloadSlice := workloadslicing.FindReplacedSliceTarget(e.Obj, e.preemptionTargets)

		if e.assignment.RepresentativeMode() == flavorassigner.Preempt {
			// If preemptions are issued, the next attempt should try all the flavors.
			e.LastAssignment = nil
			preempted, errors, err := s.preemptor.IssuePreemptions(ctx, &e.Info, preemptionTargets, e.clusterQueueSnapshot)
			if err != nil {
				log.Error(err, "Failed to preempt workloads")
			}
			if preempted != 0 {
				e.inadmissibleMsg += fmt.Sprintf(". Pending the preemption of %d workload(s)", preempted)
				e.requeueReason = qcache.RequeueReasonPendingPreemption
			} else if errors > 0 {
				e.inadmissibleMsg += fmt.Sprintf(". Preempting %d workload(s) failed, will retry.", errors)
				e.requeueReason = qcache.RequeueReasonPreemptionFailed
			}
			continue
		}

		if !s.cache.PodsReadyForAllAdmittedWorkloads(log) {
			log.V(5).Info("Waiting for all admitted workloads to be in the PodsReady condition")
			// If WaitForPodsReady is enabled and WaitForPodsReady.BlockAdmission is true
			// Block admission until all currently admitted workloads are in
			// PodsReady condition if the waitForPodsReady is enabled
			wl := e.Obj.DeepCopy()
			if err := workload.PatchAdmissionStatus(ctx, s.client, wl, s.clock, func(wl *kueue.Workload) (bool, error) {
				return workload.UnsetQuotaReservationWithCondition(wl, "Waiting", "waiting for all admitted workloads to be in PodsReady condition", s.clock.Now()), nil
			}, workload.WithLooseOnApply(), workload.WithRetryOnConflictForPatch()); err != nil {
				log.Error(err, "Could not update Workload status")
			}
			s.cache.WaitForPodsReady(ctx)
			log.V(5).Info("Finished waiting for all admitted workloads to be in the PodsReady condition")
		}

		// Evict the old workload slice if present.
		// oldWorkloadSlice is non-nil only when workload slicing is enabled
		// and there is an existing slice to evict.
		//
		// Copy the clusterName value from the old workload into the new workload
		// to ensure consistent placement in a MultiKueue context.
		// This status update will be persisted during the workload admission step below.
		//
		// If the admission step fails, we may end up in a state where:
		// - the old workload is marked Finished, and
		// - the new workload is not admitted.
		// In a single-cluster context, this should lead to Job suspension.
		// In a MultiKueue context, this should also trigger removal of remote workload/Job objects.
		if features.Enabled(features.ElasticJobsViaWorkloadSlices) && oldWorkloadSlice != nil {
			e.Obj.Status.ClusterName = oldWorkloadSlice.WorkloadInfo.Obj.Status.ClusterName
			if err := s.replaceWorkloadSlice(ctx, oldWorkloadSlice.WorkloadInfo.ClusterQueue, e.Obj, oldWorkloadSlice.WorkloadInfo.Obj.DeepCopy()); err != nil {
				log.Error(err, "Failed to replace workload slice")
				continue
			}
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
		// When the workload is evicted by scheduler we skip requeueAndUpdate.
		// The eviction process will be finalized by the workload controller.
		if e.status != assumed && e.status != evicted {
			s.requeueAndUpdate(ctx, e)
		} else {
			result = metrics.AdmissionResultSuccess
		}
	}
	for _, e := range inadmissibleEntries {
		logAdmissionAttemptIfVerbose(log, &e)
		s.requeueAndUpdate(ctx, e)
	}

	s.reportSkippedPreemptions(skippedPreemptions)
	metrics.AdmissionAttempt(result, s.clock.Since(startTime), s.roleTracker)
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
	// indicates if the workload was evicted in this cycle.
	evicted entryStatus = "evicted"
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
	requeueReason        qcache.RequeueReason
	preemptionTargets    []*preemption.Target
	clusterQueueSnapshot *schdcache.ClusterQueueSnapshot
}

func (e *entry) assignmentUsage() workload.Usage {
	return netUsage(e, e.assignment.Usage.Quota)
}

// nominate returns the workloads with their requirements (resource flavors, borrowing) if
// they were admitted by the clusterQueues in the snapshot. The second return value
// is the list of inadmissibleEntries.
func (s *Scheduler) nominate(ctx context.Context, workloads []workload.Info, snap *schdcache.Snapshot) ([]entry, []entry) {
	log := ctrl.LoggerFrom(ctx)
	entries := make([]entry, 0, len(workloads))
	var inadmissibleEntries []entry
	for _, w := range workloads {
		log := log.WithValues("workload", klog.KObj(w.Obj), "clusterQueue", klog.KRef("", string(w.ClusterQueue)))
		ns := corev1.Namespace{}
		e := entry{Info: w}
		e.clusterQueueSnapshot = snap.ClusterQueue(w.ClusterQueue)
		if !workload.NeedsSecondPass(w.Obj) && s.cache.IsAdded(w) {
			log.Info("Workload skipped from admission because it's already accounted in cache, and it does not need second pass", "workload", klog.KObj(w.Obj))
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
			e.requeueReason = qcache.RequeueReasonNamespaceMismatch
		} else if err := workload.ValidateResources(&w); err != nil {
			e.inadmissibleMsg = fmt.Sprintf("%s: %v", errInvalidWLResources, err.ToAggregate())
		} else if err := workload.ValidateLimitRange(ctx, s.client, &w); err != nil {
			e.inadmissibleMsg = fmt.Sprintf("%s: %v", errLimitRangeConstraintsUnsatisfiedResources, err.ToAggregate())
		} else {
			e.assignment, e.preemptionTargets = s.getAssignments(log, &e.Info, snap)
			e.inadmissibleMsg = e.assignment.Message()
			e.LastAssignment = &e.assignment.LastState
			entries = append(entries, e)
			continue
		}
		inadmissibleEntries = append(inadmissibleEntries, e)
	}
	return entries, inadmissibleEntries
}

func fits(snapshot *schdcache.Snapshot, cq *schdcache.ClusterQueueSnapshot, usage *workload.Usage, preemptedWorkloads preemption.PreemptedWorkloads, newTargets []*preemption.Target) bool {
	workloads := slices.Collect(maps.Values(preemptedWorkloads))
	for _, target := range newTargets {
		workloads = append(workloads, target.WorkloadInfo)
	}
	revertUsage := snapshot.SimulateWorkloadRemoval(workloads)
	defer revertUsage()
	return cq.Fits(*usage)
}

// resourcesToReserve calculates how much of the available resources in cq/cohort assignment should be reserved.
func resourcesToReserve(e *entry, cq *schdcache.ClusterQueueSnapshot) workload.Usage {
	return netUsage(e, quotaResourcesToReserve(e, cq))
}

// netUsage calculates the net usage for quota and TAS to reserve
func netUsage(e *entry, netQuota resources.FlavorResourceQuantities) workload.Usage {
	result := workload.Usage{}
	if features.Enabled(features.TopologyAwareScheduling) {
		result.TAS = e.assignment.ComputeTASNetUsage(e.Obj.Status.Admission)
	}
	if !workload.HasQuotaReservation(e.Obj) {
		result.Quota = netQuota
	}
	return result
}

func quotaResourcesToReserve(e *entry, cq *schdcache.ClusterQueueSnapshot) resources.FlavorResourceQuantities {
	if e.assignment.RepresentativeMode() != flavorassigner.Preempt {
		return e.assignment.Usage.Quota
	}
	reservedUsage := make(resources.FlavorResourceQuantities)
	for fr, usage := range e.assignment.Usage.Quota {
		cqQuota := cq.QuotaFor(fr)
		if e.assignment.Borrowing > 0 {
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

func (s *Scheduler) getAssignments(log logr.Logger, wl *workload.Info, snap *schdcache.Snapshot) (flavorassigner.Assignment, []*preemption.Target) {
	assignment, targets := s.getInitialAssignments(log, wl, snap)
	cq := snap.ClusterQueue(wl.ClusterQueue)
	updateAssignmentForTAS(snap, cq, wl, &assignment, targets)
	return assignment, targets
}

// getInitialAssignments computes the initial resource flavor assignment and any required preemption targets
// for a workload slice.
//
// The function attempts to assign resources to the provided workload slice using the current
// snapshot of the scheduling state. It proceeds in the following steps:
//
//  1. It first checks for any preemptible workload slices that workload may replace, using an annotation-based lookup.
//  2. It creates a flavor assigner to compute a full assignment scale-adjusted for preemptable workload slice targets
//     based on either:
//     - direct fit (no preemption needed), or
//     - preemption (if needed and possible).
//  3. If direct assignment isn't possible but preemption is enabled and viable, it includes any additional
//     preemption targets obtained through the configured preemptor.
//  4. If partial admission is enabled and the workload allows it, the function attempts to reduce pod counts
//     across PodSets to find an assignable configuration—again checking for preemption if needed.
//
// Returns:
//   - A flavorassigner.Assignment representing the selected (possibly reduced) flavor allocation.
//   - A slice of preemption targets, which may include both explicitly annotated slices and those
//     identified during scheduling.
//
// If no valid assignment can be made, returns the original full assignment with no preemption targets.
func (s *Scheduler) getInitialAssignments(log logr.Logger, wl *workload.Info, snap *schdcache.Snapshot) (flavorassigner.Assignment, []*preemption.Target) {
	cq := snap.ClusterQueue(wl.ClusterQueue)

	preemptionTargets, replaceableWorkloadSlice := workloadslicing.ReplacedWorkloadSlice(wl, snap)

	flvAssigner := flavorassigner.New(wl, cq, snap.ResourceFlavors, fairsharing.Enabled(s.fairSharing), preemption.NewOracle(s.preemptor, snap), replaceableWorkloadSlice)
	fullAssignment := flvAssigner.Assign(log, nil)

	arm := fullAssignment.RepresentativeMode()
	if arm == flavorassigner.Fit {
		return fullAssignment, preemptionTargets
	}

	if arm == flavorassigner.Preempt {
		faPreemptionTargets := s.preemptor.GetTargets(log, *wl, fullAssignment, snap)
		if len(faPreemptionTargets) > 0 {
			return fullAssignment, append(preemptionTargets, faPreemptionTargets...)
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
			return pa.assignment, append(preemptionTargets, pa.preemptionTargets...)
		}
	}
	return fullAssignment, nil
}

func (s *Scheduler) evictWorkloadAfterFailedTASReplacement(ctx context.Context, log logr.Logger, wl *kueue.Workload) error {
	unhealthyNodes := workload.UnhealthyNodeNames(wl)
	unhealthyNodesCsv := strings.Join(unhealthyNodes, ",")
	log.V(3).Info("Evicting workload after failed try to find a node replacement; TASFailedNodeReplacementFailFast enabled", "unhealthyNodes", unhealthyNodes)
	msg := fmt.Sprintf("Workload was evicted as there was no replacement for unhealthy node(s): %s", unhealthyNodesCsv)
	if err := workload.Evict(
		ctx, s.client, s.recorder, wl, kueue.WorkloadEvictedDueToNodeFailures, msg, "", s.clock, s.roleTracker,
		workload.EvictWithLooseOnApply(), workload.EvictWithRetryOnConflictForPatch(),
	); err != nil {
		return fmt.Errorf("failed to evict workload after failed try to find a replacement for unhealthy nodes: %s, %w", unhealthyNodesCsv, err)
	}
	return nil
}

func updateAssignmentForTAS(snapshot *schdcache.Snapshot, cq *schdcache.ClusterQueueSnapshot, wl *workload.Info, assignment *flavorassigner.Assignment, targets []*preemption.Target) {
	if features.Enabled(features.TopologyAwareScheduling) && assignment.RepresentativeMode() == flavorassigner.Preempt &&
		(workload.IsExplicitlyRequestingTAS(wl.Obj.Spec.PodSets...) || cq.IsTASOnly()) && !workload.HasTopologyAssignmentWithUnhealthyNode(wl.Obj) {
		tasRequests := assignment.WorkloadsTopologyRequests(wl, cq)
		var tasResult schdcache.TASAssignmentsResult
		if len(targets) > 0 {
			var targetWorkloads []*workload.Info
			for _, target := range targets {
				targetWorkloads = append(targetWorkloads, target.WorkloadInfo)
			}
			revertUsage := snapshot.SimulateWorkloadRemoval(targetWorkloads)
			tasResult = cq.FindTopologyAssignmentsForWorkload(tasRequests)
			revertUsage()
		} else {
			// In this scenario we don't have any preemption candidates, yet we need
			// to reserve the TAS resources to avoid the situation when a lower
			// priority workload further in the queue gets admitted and preempted
			// in the next scheduling cycle by the waiting workload. To obtain
			// a TAS assignment for reserving the resources we run the algorithm
			// assuming the cluster is empty.
			tasResult = cq.FindTopologyAssignmentsForWorkload(tasRequests, schdcache.WithSimulateEmpty(true))
		}
		assignment.UpdateForTASResult(tasResult)
	}
}

// admit sets the admitting clusterQueue and flavors into the workload of
// the entry, and asynchronously updates the object in the apiserver after
// assuming it in the cache.
func (s *Scheduler) admit(ctx context.Context, e *entry, cq *schdcache.ClusterQueueSnapshot) error {
	log := ctrl.LoggerFrom(ctx)
	admission := &kueue.Admission{
		ClusterQueue:      e.ClusterQueue,
		PodSetAssignments: e.assignment.ToAPI(),
	}

	consideredStr := flavorassigner.FormatFlavorAssignmentAttemptsForEvents(e.assignment)
	cacheWl, err := s.assumeWorkload(log, e, cq, admission)
	if err != nil {
		return err
	}

	newWorkload := e.Obj.DeepCopy()
	s.admissionRoutineWrapper.Run(func() {
		err := workload.PatchAdmissionStatus(ctx, s.client, newWorkload, s.clock, func(wl *kueue.Workload) (bool, error) {
			s.prepareWorkload(log, wl, cq, admission)
			if features.Enabled(features.TopologyAwareScheduling) && workload.HasUnhealthyNodes(e.Obj) {
				log.V(5).Info("Clearing the topology assignment recovery field from the workload status after successful recovery")
				wl.Status.UnhealthyNodes = nil
			}
			return true, nil
		}, workload.WithLooseOnApply(), workload.WithRetryOnConflictForPatch())
		if err == nil {
			// Record metrics and events for quota reservation and admission
			s.recordWorkloadAdmissionMetrics(newWorkload, e.Obj, admission, consideredStr)

			log.V(2).Info("Workload successfully admitted and assigned flavors", "assignments", admission.PodSetAssignments)
			return
		}
		// Ignore errors because the workload or clusterQueue could have been deleted
		// by an event.
		_ = s.cache.DeleteWorkload(log, workload.Key(cacheWl))
		if afs.Enabled(s.admissionFairSharing) {
			s.updateEntryPenalty(log, e, subtract)
		}
		if apierrors.IsNotFound(err) {
			log.V(2).Info("Workload not admitted because it was deleted")
			return
		}

		log.Error(err, errCouldNotAdmitWL)
		s.requeueAndUpdate(ctx, *e)
	})

	return nil
}

func (s *Scheduler) prepareWorkload(log logr.Logger, wl *kueue.Workload, cq *schdcache.ClusterQueueSnapshot, admission *kueue.Admission) {
	workload.SetQuotaReservation(wl, admission, s.clock)
	if workload.HasAllChecks(wl, workload.AdmissionChecksForWorkload(log, wl, cq.AdmissionChecks, schdcache.AllFlavors(cq.ResourceGroups))) {
		// sync Admitted, ignore the result since an API update is always done.
		_ = workload.SyncAdmittedCondition(wl, s.clock.Now())
	}
}

func (s *Scheduler) assumeWorkload(log logr.Logger, e *entry, cq *schdcache.ClusterQueueSnapshot, admission *kueue.Admission) (*kueue.Workload, error) {
	cacheWl := e.Obj.DeepCopy()
	s.prepareWorkload(log, cacheWl, cq, admission)
	if added := s.cache.AddOrUpdateWorkload(log, cacheWl); !added {
		return nil, fmt.Errorf("workload %s/%s could not be added to the cache", cacheWl.Namespace, cacheWl.Name)
	}

	e.status = assumed
	log.V(2).Info("Workload assumed in the cache")

	if afs.Enabled(s.admissionFairSharing) {
		s.updateEntryPenalty(log, e, add)
		// Trigger LocalQueue reconciler to apply any pending penalties
		s.queues.NotifyWorkloadUpdateWatchers(e.Obj, cacheWl)
	}
	return cacheWl, nil
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

	// First process workloads which already have quota reserved. Such workload
	// may be considered if this is their second pass.
	aHasQuota := workload.HasQuotaReservation(a.Obj)
	bHasQuota := workload.HasQuotaReservation(b.Obj)
	if aHasQuota != bHasQuota {
		return aHasQuota
	}

	// 1. Request under nominal quota.
	aBorrows := a.assignment.Borrows()
	bBorrows := b.assignment.Borrows()
	if aBorrows != bBorrows {
		return aBorrows < bBorrows
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
	if e.status != notNominated && e.requeueReason == qcache.RequeueReasonGeneric {
		// Failed after nomination is the only reason why a workload would be requeued downstream.
		e.requeueReason = qcache.RequeueReasonFailedAfterNomination
	}

	if s.queues.QueueSecondPassIfNeeded(ctx, e.Obj, e.SecondPassIteration) {
		log.V(2).Info("Workload re-queued for second pass", "workload", klog.KObj(e.Obj), "clusterQueue", klog.KRef("", string(e.ClusterQueue)), "queue", klog.KRef(e.Obj.Namespace, string(e.Obj.Spec.QueueName)), "requeueReason", e.requeueReason, "status", e.status)
		s.recorder.Eventf(e.Obj, corev1.EventTypeWarning, "SecondPassFailed", api.TruncateEventMessage(e.inadmissibleMsg))
		return
	}

	added := s.queues.RequeueWorkload(ctx, &e.Info, e.requeueReason)
	log.V(2).Info("Workload re-queued", "workload", klog.KObj(e.Obj), "clusterQueue", klog.KRef("", string(e.ClusterQueue)), "queue", klog.KRef(e.Obj.Namespace, string(e.Obj.Spec.QueueName)), "requeueReason", e.requeueReason, "added", added, "status", e.status)
	if e.status == notNominated || e.status == skipped {
		wl := e.Obj.DeepCopy()
		if err := workload.PatchAdmissionStatus(ctx, s.client, wl, s.clock, func(wl *kueue.Workload) (bool, error) {
			updated := workload.UnsetQuotaReservationWithCondition(wl, "Pending", e.inadmissibleMsg, s.clock.Now())
			if workload.PropagateResourceRequests(wl, &e.Info) {
				updated = true
			}
			return updated, nil
		}, workload.WithLooseOnApply(), workload.WithRetryOnConflictForPatch()); err != nil {
			log.Error(err, "Could not update Workload status")
		}
		s.recorder.Eventf(e.Obj, corev1.EventTypeWarning, "Pending", api.TruncateEventMessage(e.inadmissibleMsg))
	}
}

// recordWorkloadAdmissionMetrics records metrics and events for workload admission process
func (s *Scheduler) recordWorkloadAdmissionMetrics(newWorkload, originalWorkload *kueue.Workload, admission *kueue.Admission, consideredFlavors string) {
	waitTime := workload.QueuedWaitTime(newWorkload, s.clock)

	s.recordQuotaReservationMetrics(newWorkload, originalWorkload, admission, waitTime, consideredFlavors)
	s.recordWorkloadAdmissionEvents(newWorkload, originalWorkload, admission, waitTime)
}

// recordQuotaReservationMetrics records metrics and events for quota reservation
func (s *Scheduler) recordQuotaReservationMetrics(newWorkload, originalWorkload *kueue.Workload, admission *kueue.Admission, waitTime time.Duration, consideredFlavors string) {
	if workload.HasQuotaReservation(originalWorkload) {
		return
	}

	quotaReservedEventMessage := fmt.Sprintf("Quota reserved in ClusterQueue %v, wait time since queued was %.0fs", admission.ClusterQueue, waitTime.Seconds())
	if consideredFlavors != "" {
		quotaReservedEventMessage += fmt.Sprintf("; Flavors considered: %s", consideredFlavors)
	}

	s.recorder.Event(newWorkload, corev1.EventTypeNormal, "QuotaReserved", api.TruncateEventMessage(quotaReservedEventMessage))

	priorityClassName := workload.PriorityClassName(newWorkload)
	metrics.QuotaReservedWorkload(admission.ClusterQueue, priorityClassName, waitTime, s.roleTracker)
	if features.Enabled(features.LocalQueueMetrics) {
		metrics.LocalQueueQuotaReservedWorkload(metrics.LQRefFromWorkload(newWorkload), priorityClassName, waitTime, s.roleTracker)
	}
}

// recordWorkloadAdmissionEvents records metrics and events for workload admission
func (s *Scheduler) recordWorkloadAdmissionEvents(newWorkload, originalWorkload *kueue.Workload, admission *kueue.Admission, waitTime time.Duration) {
	if !workload.IsAdmitted(newWorkload) || workload.HasUnhealthyNodes(originalWorkload) {
		return
	}

	s.recorder.Eventf(newWorkload, corev1.EventTypeNormal, "Admitted", "Admitted by ClusterQueue %v, wait time since reservation was 0s", admission.ClusterQueue)

	priorityClassName := workload.PriorityClassName(newWorkload)
	metrics.AdmittedWorkload(admission.ClusterQueue, priorityClassName, waitTime, s.roleTracker)
	if features.Enabled(features.LocalQueueMetrics) {
		metrics.LocalQueueAdmittedWorkload(metrics.LQRefFromWorkload(newWorkload), priorityClassName, waitTime, s.roleTracker)
	}

	if len(newWorkload.Status.AdmissionChecks) > 0 {
		metrics.ReportAdmissionChecksWaitTime(admission.ClusterQueue, priorityClassName, 0, s.roleTracker)
		if features.Enabled(features.LocalQueueMetrics) {
			metrics.ReportLocalQueueAdmissionChecksWaitTime(metrics.LQRefFromWorkload(newWorkload), priorityClassName, 0, s.roleTracker)
		}
	}
}

// replaceWorkloadSlice handles the replacement of a workload slice by deactivating the old slice and
// marking it as finished. It logs the replacement operation, records an event, and reports metrics.
//
// This function performs the following steps:
//  1. Checks if the old workload slice is already finished by inspecting the "Finished" condition in its status.
//     If the slice is already finished, the function logs a message and returns early.
//  2. If the old slice is not finished, it deactivates the old slice and marks it with a "Finished" condition,
//     indicating that the slice was replaced to accommodate the new workload slice.
//  3. The function logs details about the replacement, including the reason for the removal and the associated message.
//  4. An event is recorded for the old slice to indicate that the slice was aggregated (replaced) by the new slice.
//  5. The function reports metrics for the aggregation of workload slices for the old queue.
func (s *Scheduler) replaceWorkloadSlice(ctx context.Context, oldQueue kueue.ClusterQueueReference, newSlice, oldSlice *kueue.Workload) error {
	log := ctrl.LoggerFrom(ctx)
	if workload.IsFinished(oldSlice) {
		log.V(3).Info("Workload slice already finished", "old-slice", klog.KObj(oldSlice), "new-slice", klog.KObj(newSlice))
		return nil
	}
	reason := kueue.WorkloadSliceReplaced
	message := fmt.Sprintf("Replaced to accommodate a workload (UID: %s, JobUID: %s) due to workload slice aggregation", newSlice.UID, newSlice.Labels[controllerconstants.JobUIDLabel])
	if err := workload.Finish(ctx, s.client, oldSlice, reason, message, s.clock, s.roleTracker); err != nil {
		return fmt.Errorf("failed to replace workload slice: %w", err)
	}

	log.V(3).Info("Replaced", "old slice", klog.KObj(oldSlice), "new slice", klog.KObj(newSlice), "reason", reason, "message", message, "old-queue", klog.KRef("", string(oldQueue)))
	s.recorder.Eventf(oldSlice, corev1.EventTypeNormal, reason, message)
	metrics.ReportReplacedWorkloadSlices(oldQueue, s.roleTracker)
	return nil
}

type usageOp int

const (
	// add penalty
	add usageOp = iota
	// subtract penalty
	subtract
)

func (s *Scheduler) updateEntryPenalty(log logr.Logger, e *entry, op usageOp) {
	lqKey := utilqueue.NewLocalQueueReference(e.Obj.Namespace, e.Obj.Spec.QueueName)
	lqObjRef := klog.KRef(e.Obj.Namespace, string(e.Obj.Spec.QueueName))
	penalty := afs.CalculateEntryPenalty(e.SumTotalRequests(), s.admissionFairSharing)

	switch op {
	case add:
		s.queues.AfsEntryPenalties.Push(lqKey, penalty)
		log.V(3).Info("Entry penalty added to localQueue", "localQueue", lqObjRef, "penalty", penalty)
	case subtract:
		s.queues.AfsEntryPenalties.Sub(lqKey, penalty)
		log.V(3).Info("Entry penalty subtracted from localQueue", "localQueue", lqObjRef, "penalty", penalty)
	}
}
