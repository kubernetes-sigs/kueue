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
	"maps"
	"sort"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	"k8s.io/utils/field"
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
	"sigs.k8s.io/kueue/pkg/util/limitrange"
	utilmaps "sigs.k8s.io/kueue/pkg/util/maps"
	"sigs.k8s.io/kueue/pkg/util/priority"
	"sigs.k8s.io/kueue/pkg/util/resource"
	"sigs.k8s.io/kueue/pkg/util/routine"
	"sigs.k8s.io/kueue/pkg/util/wait"
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
	workloadOrdering        workload.Ordering
	fairSharing             config.FairSharing

	// attemptCount identifies the number of scheduling attempt in logs, from the last restart.
	attemptCount int64

	// Stubs.
	applyAdmission func(context.Context, *kueue.Workload) error
}

type options struct {
	podsReadyRequeuingTimestamp config.RequeuingTimestamp
	fairSharing                 config.FairSharing
}

// Option configures the reconciler.
type Option func(*options)

var defaultOptions = options{
	podsReadyRequeuingTimestamp: config.EvictionTimestamp,
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
		preemptor:               preemption.New(cl, wo, recorder, options.fairSharing),
		admissionRoutineWrapper: routine.DefaultWrapper,
		workloadOrdering:        wo,
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

type cohortsUsage map[string]resources.FlavorResourceQuantitiesFlat

func (cu cohortsUsage) add(cohort string, assignment resources.FlavorResourceQuantitiesFlat) {
	if cu[cohort] == nil {
		cu[cohort] = make(resources.FlavorResourceQuantitiesFlat, len(assignment))
	}

	for fr, v := range assignment {
		cu[cohort][fr] += v
	}
}

func (cu cohortsUsage) totalUsageForCommonFlavorResources(cohort string, assignment resources.FlavorResourceQuantitiesFlat) resources.FlavorResourceQuantitiesFlat {
	return utilmaps.Intersect(cu[cohort], assignment, func(a, b int64) int64 { return a + b })
}

func (cu cohortsUsage) hasCommonFlavorResources(cohort string, assignment resources.FlavorResourceQuantitiesFlat) bool {
	cohortUsage, cohortFound := cu[cohort]
	if !cohortFound {
		return false
	}
	for fr := range assignment {
		if _, found := cohortUsage[fr]; found {
			return true
		}
	}
	return false
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

func (s *Scheduler) schedule(ctx context.Context) wait.SpeedSignal {
	s.attemptCount++
	log := ctrl.LoggerFrom(ctx).WithValues("attemptCount", s.attemptCount)
	ctx = ctrl.LoggerInto(ctx, log)

	// 1. Get the heads from the queues, including their desired clusterQueue.
	// This operation blocks while the queues are empty.
	headWorkloads := s.queues.Heads(ctx)
	// If there are no elements, it means that the program is finishing.
	if len(headWorkloads) == 0 {
		return wait.KeepGoing
	}
	startTime := time.Now()

	// 2. Take a snapshot of the cache.
	snapshot := s.cache.Snapshot()
	logSnapshotIfVerbose(log, &snapshot)

	// 3. Calculate requirements (resource flavors, borrowing) for admitting workloads.
	entries := s.nominate(ctx, headWorkloads, snapshot)

	// 4. Sort entries based on borrowing, priorities (if enabled) and timestamps.
	sort.Sort(entryOrdering{
		enableFairSharing: s.fairSharing.Enable,
		entries:           entries,
		workloadOrdering:  s.workloadOrdering,
	})

	// 5. Admit entries, ensuring that no more than one workload gets
	// admitted by a cohort (if borrowing).
	// This is because there can be other workloads deeper in a clusterQueue whose
	// head got admitted that should be scheduled in the cohort before the heads
	// of other clusterQueues.
	cycleCohortsUsage := cohortsUsage{}
	cycleCohortsSkipPreemption := sets.New[string]()
	preemptedWorkloads := sets.New[string]()
	for i := range entries {
		e := &entries[i]
		mode := e.assignment.RepresentativeMode()
		if mode == flavorassigner.NoFit {
			continue
		}

		cq := snapshot.ClusterQueues[e.ClusterQueue]
		log := log.WithValues("workload", klog.KObj(e.Obj), "clusterQueue", klog.KRef("", e.ClusterQueue))
		ctx := ctrl.LoggerInto(ctx, log)

		if features.Enabled(features.MultiplePreemptions) {
			if mode == flavorassigner.Preempt && len(e.preemptionTargets) == 0 {
				log.V(2).Info("Workload requires preemption, but there are no candidate workloads allowed for preemption", "preemption", cq.Preemption)
				// we use resourcesToReserve to block capacity up to either the nominal capacity,
				// or the borrowing limit when borrowing, so that a lower priority workload cannot
				// admit before us.
				cq.AddUsage(resourcesToReserve(e, cq))
				continue
			}

			// We skip multiple-preemptions per cohort if any of the targets are overlapping
			pendingPreemptions := make([]string, 0, len(e.preemptionTargets))
			for _, target := range e.preemptionTargets {
				pendingPreemptions = append(pendingPreemptions, workload.Key(target.WorkloadInfo.Obj))
			}
			if preemptedWorkloads.HasAny(pendingPreemptions...) {
				setSkipped(e, "Workload has overlapping preemption targets with another workload")
				continue
			}

			usage := e.netUsage()
			if !cq.Fits(usage) {
				setSkipped(e, "Workload no longer fits after processing another workload")
				continue
			}
			preemptedWorkloads.Insert(pendingPreemptions...)
			cq.AddUsage(usage)
		} else if cq.Cohort != nil {
			sum := cycleCohortsUsage.totalUsageForCommonFlavorResources(cq.Cohort.Name, e.assignment.Usage)
			// Check whether there was an assignment in this cycle that could render the next assignments invalid:
			// - If the workload no longer fits in the cohort.
			// - If there was another assignment in the cohort, then the preemption calculation is no longer valid.
			if cycleCohortsUsage.hasCommonFlavorResources(cq.Cohort.Name, e.assignment.Usage) {
				if mode == flavorassigner.Fit && !cq.FitInCohort(sum) {
					setSkipped(e, "Workload no longer fits after processing another workload")
					continue
				}
				if mode == flavorassigner.Preempt && cycleCohortsSkipPreemption.Has(cq.Cohort.Name) {
					setSkipped(e, "Workload skipped because its premption calculations were invalidated by another workload")
					continue
				}
			}
			// Even if the workload will not be admitted after this point, due to preemption pending or other failures,
			// we should still account for its usage.
			cycleCohortsUsage.add(cq.Cohort.Name, resourcesToReserve(e, cq))
		}
		if e.assignment.RepresentativeMode() != flavorassigner.Fit {
			if len(e.preemptionTargets) != 0 {
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
				if cq.Cohort != nil {
					cycleCohortsSkipPreemption.Insert(cq.Cohort.Name)
				}
			} else {
				log.V(2).Info("Workload requires preemption, but there are no candidate workloads allowed for preemption", "preemption", cq.Preemption)
			}
			continue
		}
		if !s.cache.PodsReadyForAllAdmittedWorkloads(log) {
			log.V(5).Info("Waiting for all admitted workloads to be in the PodsReady condition")
			// If WaitForPodsReady is enabled and WaitForPodsReady.BlockAdmission is true
			// Block admission until all currently admitted workloads are in
			// PodsReady condition if the waitForPodsReady is enabled
			workload.UnsetQuotaReservationWithCondition(e.Obj, "Waiting", "waiting for all admitted workloads to be in PodsReady condition")
			if err := workload.ApplyAdmissionStatus(ctx, s.client, e.Obj, false); err != nil {
				log.Error(err, "Could not update Workload status")
			}
			s.cache.WaitForPodsReady(ctx)
			log.V(5).Info("Finished waiting for all admitted workloads to be in the PodsReady condition")
		}
		e.status = nominated
		if err := s.admit(ctx, e, cq); err != nil {
			e.inadmissibleMsg = fmt.Sprintf("Failed to admit workload: %v", err)
		}
		if cq.Cohort != nil {
			cycleCohortsSkipPreemption.Insert(cq.Cohort.Name)
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
	metrics.AdmissionAttempt(result, time.Since(startTime))
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
	dominantResourceShare int
	dominantResourceName  corev1.ResourceName
	assignment            flavorassigner.Assignment
	status                entryStatus
	inadmissibleMsg       string
	requeueReason         queue.RequeueReason
	preemptionTargets     []*preemption.Target
}

// netUsage returns how much capacity this entry will require from the ClusterQueue/Cohort.
// When a workload is preempting, it subtracts the preempted resources from the resources
// required, as the remaining quota is all we need from the CQ/Cohort.
func (e *entry) netUsage() resources.FlavorResourceQuantitiesFlat {
	if e.assignment.RepresentativeMode() == flavorassigner.Fit {
		return e.assignment.Usage
	}

	usage := maps.Clone(e.assignment.Usage)
	for target := range e.preemptionTargets {
		for fr, v := range e.preemptionTargets[target].WorkloadInfo.FlavorResourceUsage() {
			if _, uses := usage[fr]; !uses {
				continue
			}
			usage[fr] = max(0, usage[fr]-v)
		}
	}
	return usage
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
		if s.cache.IsAssumedOrAdmittedWorkload(w) {
			log.Info("Workload skipped from admission because it's already assumed or admitted", "workload", klog.KObj(w.Obj))
			continue
		} else if workload.HasRetryChecks(w.Obj) || workload.HasRejectedChecks(w.Obj) {
			e.inadmissibleMsg = "The workload has failed admission checks"
		} else if snap.InactiveClusterQueueSets.Has(w.ClusterQueue) {
			e.inadmissibleMsg = fmt.Sprintf("ClusterQueue %s is inactive", w.ClusterQueue)
		} else if cq == nil {
			e.inadmissibleMsg = fmt.Sprintf("ClusterQueue %s not found", w.ClusterQueue)
		} else if err := s.client.Get(ctx, types.NamespacedName{Name: w.Obj.Namespace}, &ns); err != nil {
			e.inadmissibleMsg = fmt.Sprintf("Could not obtain workload namespace: %v", err)
		} else if !cq.NamespaceSelector.Matches(labels.Set(ns.Labels)) {
			e.inadmissibleMsg = "Workload namespace doesn't match ClusterQueue selector"
			e.requeueReason = queue.RequeueReasonNamespaceMismatch
		} else if err := s.validateResources(&w); err != nil {
			e.inadmissibleMsg = err.Error()
		} else if err := s.validateLimitRange(ctx, &w); err != nil {
			e.inadmissibleMsg = err.Error()
		} else {
			e.assignment, e.preemptionTargets = s.getAssignments(log, &e.Info, &snap)
			e.inadmissibleMsg = e.assignment.Message()
			e.Info.LastAssignment = &e.assignment.LastState
			if s.fairSharing.Enable && e.assignment.RepresentativeMode() != flavorassigner.NoFit {
				e.dominantResourceShare, e.dominantResourceName = cq.DominantResourceShareWith(e.assignment.TotalRequestsFor(&w))
			}
		}
		entries = append(entries, e)
	}
	return entries
}

// resourcesToReserve calculates how much of the available resources in cq/cohort assignment should be reserved.
func resourcesToReserve(e *entry, cq *cache.ClusterQueueSnapshot) resources.FlavorResourceQuantitiesFlat {
	if e.assignment.RepresentativeMode() != flavorassigner.Preempt {
		return e.assignment.Usage
	}
	reservedUsage := make(resources.FlavorResourceQuantitiesFlat)
	for fr, usage := range e.assignment.Usage {
		cqQuota := cq.QuotaFor(fr)
		if e.assignment.Borrowing {
			if cqQuota.BorrowingLimit == nil {
				reservedUsage[fr] = usage
			} else {
				reservedUsage[fr] = min(usage, cqQuota.Nominal+*cqQuota.BorrowingLimit-cq.Usage.For(fr))
			}
		} else {
			reservedUsage[fr] = max(0, min(usage, cqQuota.Nominal-cq.Usage.For(fr)))
		}
	}
	return reservedUsage
}

type partialAssignment struct {
	assignment        flavorassigner.Assignment
	preemptionTargets []*preemption.Target
}

func (s *Scheduler) getAssignments(log logr.Logger, wl *workload.Info, snap *cache.Snapshot) (flavorassigner.Assignment, []*preemption.Target) {
	cq := snap.ClusterQueues[wl.ClusterQueue]
	flvAssigner := flavorassigner.New(wl, cq, snap.ResourceFlavors, s.fairSharing.Enable)
	fullAssignment := flvAssigner.Assign(log, nil)
	var faPreemptionTargets []*preemption.Target

	arm := fullAssignment.RepresentativeMode()
	if arm == flavorassigner.Fit {
		return fullAssignment, nil
	}

	if arm == flavorassigner.Preempt {
		faPreemptionTargets = s.preemptor.GetTargets(log, *wl, fullAssignment, snap)
	}

	// if the feature gate is not enabled or we can preempt
	if !features.Enabled(features.PartialAdmission) || len(faPreemptionTargets) > 0 {
		return fullAssignment, faPreemptionTargets
	}

	if wl.CanBePartiallyAdmitted() {
		reducer := flavorassigner.NewPodSetReducer(wl.Obj.Spec.PodSets, func(nextCounts []int32) (*partialAssignment, bool) {
			assignment := flvAssigner.Assign(log, nextCounts)
			if assignment.RepresentativeMode() == flavorassigner.Fit {
				return &partialAssignment{assignment: assignment}, true
			}
			preemptionTargets := s.preemptor.GetTargets(log, *wl, assignment, snap)
			if len(preemptionTargets) > 0 {
				return &partialAssignment{assignment: assignment, preemptionTargets: preemptionTargets}, true
			}
			return nil, false
		})
		if pa, found := reducer.Search(); found {
			return pa.assignment, pa.preemptionTargets
		}
	}
	return fullAssignment, nil
}

// validateResources validates that requested resources are less or equal
// to limits.
func (s *Scheduler) validateResources(wi *workload.Info) error {
	podsetsPath := field.NewPath("podSets")
	// requests should be less than limits.
	allReasons := []string{}
	for i := range wi.Obj.Spec.PodSets {
		ps := &wi.Obj.Spec.PodSets[i]
		psPath := podsetsPath.Child(ps.Name)
		for i := range ps.Template.Spec.InitContainers {
			c := ps.Template.Spec.InitContainers[i]
			if list := resource.GetGreaterKeys(c.Resources.Requests, c.Resources.Limits); len(list) > 0 {
				allReasons = append(allReasons, fmt.Sprintf("%s[%s] requests exceed it's limits",
					psPath.Child("initContainers").Index(i).String(),
					strings.Join(list, ", ")))
			}
		}

		for i := range ps.Template.Spec.Containers {
			c := ps.Template.Spec.Containers[i]
			if list := resource.GetGreaterKeys(c.Resources.Requests, c.Resources.Limits); len(list) > 0 {
				allReasons = append(allReasons, fmt.Sprintf("%s[%s] requests exceed it's limits",
					psPath.Child("containers").Index(i).String(),
					strings.Join(list, ", ")))
			}
		}
	}
	if len(allReasons) > 0 {
		return fmt.Errorf("resource validation failed: %s", strings.Join(allReasons, "; "))
	}
	return nil
}

// validateLimitRange validates that the requested resources fit into the namespace defined
// limitRanges.
func (s *Scheduler) validateLimitRange(ctx context.Context, wi *workload.Info) error {
	podsetsPath := field.NewPath("podSets")
	// get the range summary from the namespace.
	list := corev1.LimitRangeList{}
	if err := s.client.List(ctx, &list, &client.ListOptions{Namespace: wi.Obj.Namespace}); err != nil {
		return err
	}
	if len(list.Items) == 0 {
		return nil
	}
	summary := limitrange.Summarize(list.Items...)

	// verify
	allReasons := []string{}
	for i := range wi.Obj.Spec.PodSets {
		ps := &wi.Obj.Spec.PodSets[i]
		allReasons = append(allReasons, summary.ValidatePodSpec(&ps.Template.Spec, podsetsPath.Child(ps.Name))...)
	}
	if len(allReasons) > 0 {
		return fmt.Errorf("didn't satisfy LimitRange constraints: %s", strings.Join(allReasons, "; "))
	}
	return nil
}

// admit sets the admitting clusterQueue and flavors into the workload of
// the entry, and asynchronously updates the object in the apiserver after
// assuming it in the cache.
func (s *Scheduler) admit(ctx context.Context, e *entry, cq *cache.ClusterQueueSnapshot) error {
	log := ctrl.LoggerFrom(ctx)
	newWorkload := e.Obj.DeepCopy()
	admission := &kueue.Admission{
		ClusterQueue:      kueue.ClusterQueueReference(e.ClusterQueue),
		PodSetAssignments: e.assignment.ToAPI(),
	}

	workload.SetQuotaReservation(newWorkload, admission)
	if workload.HasAllChecks(newWorkload, workload.AdmissionChecksForWorkload(log, newWorkload, cq.AdmissionChecks)) {
		// sync Admitted, ignore the result since an API update is always done.
		_ = workload.SyncAdmittedCondition(newWorkload)
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
			if workload.IsAdmitted(newWorkload) {
				s.recorder.Eventf(newWorkload, corev1.EventTypeNormal, "Admitted", "Admitted by ClusterQueue %v, wait time since reservation was 0s", admission.ClusterQueue)
				metrics.AdmittedWorkload(admission.ClusterQueue, waitTime)
				if len(newWorkload.Status.AdmissionChecks) > 0 {
					metrics.AdmissionChecksWaitTime(admission.ClusterQueue, 0)
				}
			}
			log.V(2).Info("Workload successfully admitted and assigned flavors", "assignments", admission.PodSetAssignments)
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
		s.requeueAndUpdate(ctx, *e)
	})

	return nil
}

func (s *Scheduler) applyAdmissionWithSSA(ctx context.Context, w *kueue.Workload) error {
	return workload.ApplyAdmissionStatus(ctx, s.client, w, false)
}

type entryOrdering struct {
	enableFairSharing bool
	entries           []entry
	workloadOrdering  workload.Ordering
}

func (e entryOrdering) Len() int {
	return len(e.entries)
}

func (e entryOrdering) Swap(i, j int) {
	e.entries[i], e.entries[j] = e.entries[j], e.entries[i]
}

// Less is the ordering criteria:
// 1. request under nominal quota before borrowing.
// 2. higher priority first.
// 3. FIFO on eviction or creation timestamp.
func (e entryOrdering) Less(i, j int) bool {
	a := e.entries[i]
	b := e.entries[j]

	// 1. Request under nominal quota.
	aBorrows := a.assignment.Borrows()
	bBorrows := b.assignment.Borrows()
	if aBorrows != bBorrows {
		return !aBorrows
	}

	// 2. Fair share, if enabled.
	if e.enableFairSharing && a.dominantResourceShare != b.dominantResourceShare {
		return a.dominantResourceShare < b.dominantResourceShare
	}

	// 3. Higher priority first if not disabled.
	if features.Enabled(features.PrioritySortingWithinCohort) {
		p1 := priority.Priority(a.Obj)
		p2 := priority.Priority(b.Obj)
		if p1 != p2 {
			return p1 > p2
		}
	}

	// 4. FIFO.
	aComparisonTimestamp := e.workloadOrdering.GetQueueOrderTimestamp(a.Obj)
	bComparisonTimestamp := e.workloadOrdering.GetQueueOrderTimestamp(b.Obj)
	return aComparisonTimestamp.Before(bComparisonTimestamp)
}

func (s *Scheduler) requeueAndUpdate(ctx context.Context, e entry) {
	log := ctrl.LoggerFrom(ctx)
	if e.status != notNominated && e.requeueReason == queue.RequeueReasonGeneric {
		// Failed after nomination is the only reason why a workload would be requeued downstream.
		e.requeueReason = queue.RequeueReasonFailedAfterNomination
	}
	added := s.queues.RequeueWorkload(ctx, &e.Info, e.requeueReason)
	log.V(2).Info("Workload re-queued", "workload", klog.KObj(e.Obj), "clusterQueue", klog.KRef("", e.ClusterQueue), "queue", klog.KRef(e.Obj.Namespace, e.Obj.Spec.QueueName), "requeueReason", e.requeueReason, "added", added, "status", e.status)

	if e.status == notNominated || e.status == skipped {
		patch := workload.AdmissionStatusPatch(e.Obj, true)
		if workload.UnsetQuotaReservationWithCondition(patch, "Pending", e.inadmissibleMsg) {
			if err := workload.ApplyAdmissionStatusPatch(ctx, s.client, patch); err != nil {
				log.Error(err, "Could not update Workload status")
			}
		}
		s.recorder.Eventf(e.Obj, corev1.EventTypeNormal, "Pending", api.TruncateEventMessage(e.inadmissibleMsg))
	}
}
