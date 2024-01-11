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
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	"k8s.io/utils/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/queue"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption"
	"sigs.k8s.io/kueue/pkg/util/api"
	"sigs.k8s.io/kueue/pkg/util/limitrange"
	utilmaps "sigs.k8s.io/kueue/pkg/util/maps"
	"sigs.k8s.io/kueue/pkg/util/priority"
	"sigs.k8s.io/kueue/pkg/util/resource"
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
	// Stubs.
	applyAdmission func(context.Context, *kueue.Workload) error
}

type options struct {
}

// Option configures the reconciler.
type Option func(*options)

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

type cohortsUsage map[string]cache.FlavorResourceQuantities

func (cu *cohortsUsage) add(cohort string, assigment cache.FlavorResourceQuantities) {
	cohortUsage := (*cu)[cohort]
	if cohortUsage == nil {
		cohortUsage = make(cache.FlavorResourceQuantities, len(assigment))
	}

	for flavor, resources := range assigment {
		if _, found := cohortUsage[flavor]; found {
			cohortUsage[flavor] = utilmaps.Merge(cohortUsage[flavor], resources, func(a, b int64) int64 { return a + b })
		} else {
			cohortUsage[flavor] = maps.Clone(resources)
		}
	}
	(*cu)[cohort] = cohortUsage
}

func (cu *cohortsUsage) totalUsageForCommonFlavorResources(cohort string, assigment cache.FlavorResourceQuantities) cache.FlavorResourceQuantities {
	return utilmaps.Intersect((*cu)[cohort], assigment, func(a, b map[corev1.ResourceName]int64) map[corev1.ResourceName]int64 {
		return utilmaps.Intersect(a, b, func(a, b int64) int64 { return a + b })
	})
}

func (cu *cohortsUsage) hasCommonFlavorResources(cohort string, assigment cache.FlavorResourceQuantities) bool {
	cohortUsage, cohortFound := (*cu)[cohort]
	if !cohortFound {
		return false
	}
	for flavor, assigmentResources := range assigment {
		if cohortResources, found := cohortUsage[flavor]; found {
			for resName := range assigmentResources {
				if _, found := cohortResources[resName]; found {
					return true
				}
			}
		}
	}
	return false
}

func (s *Scheduler) schedule(ctx context.Context) {
	log := ctrl.LoggerFrom(ctx)

	// 1. Get the heads from the queues, including their desired clusterQueue.
	// This operation blocks while the queues are empty.
	headWorkloads := s.queues.Heads(ctx)
	// If there are no elements, it means that the program is finishing.
	if len(headWorkloads) == 0 {
		return
	}
	startTime := time.Now()

	// 2. Take a snapshot of the cache.
	snapshot := s.cache.Snapshot()
	logSnapshotIfVerbose(log, &snapshot)

	// 3. Calculate requirements (resource flavors, borrowing) for admitting workloads.
	entries := s.nominate(ctx, headWorkloads, snapshot)

	// 4. Sort entries based on borrowing, priorities (if enabled) and timestamps.
	sort.Sort(entryOrdering(entries))

	// 5. Admit entries, ensuring that no more than one workload gets
	// admitted by a cohort (if borrowing).
	// This is because there can be other workloads deeper in a clusterQueue whose
	// head got admitted that should be scheduled in the cohort before the heads
	// of other clusterQueues.
	cycleCohortsUsage := cohortsUsage{}
	for i := range entries {
		e := &entries[i]
		if e.assignment.RepresentativeMode() == flavorassigner.NoFit {
			continue
		}

		cq := snapshot.ClusterQueues[e.ClusterQueue]
		if cq.Cohort != nil {
			sum := cycleCohortsUsage.totalUsageForCommonFlavorResources(cq.Cohort.Name, e.assignment.Usage)
			// If the workload uses resources that were potentially assumed in this cycle and will no longer fit in the
			// cohort. If a resource of a flavor is used only once or for the first time in the cycle the checks done by
			// the flavorassigner are still valid.
			if cycleCohortsUsage.hasCommonFlavorResources(cq.Cohort.Name, e.assignment.Usage) && !cq.Cohort.CanFit(sum) {
				e.status = skipped
				e.inadmissibleMsg = "other workloads in the cohort were prioritized"
				// When the workload needs borrowing and there is another workload in cohort doesn't
				// need borrowing, the workload needborrowing will come again. In this case we should
				// not skip the previous flavors.
				e.LastAssignment = nil
				continue
			}
			// Even if the workload will not be admitted after this point, due to preemption pending or other failures,
			// we should still account for its usage.
			cycleCohortsUsage.add(cq.Cohort.Name, resourcesToReserve(e, cq))
		}
		log := log.WithValues("workload", klog.KObj(e.Obj), "clusterQueue", klog.KRef("", e.ClusterQueue))
		ctx := ctrl.LoggerInto(ctx, log)
		if e.assignment.RepresentativeMode() != flavorassigner.Fit {
			if len(e.preemptionTargets) != 0 {
				// If preemptions are issued, the next attempt should try all the flavors.
				e.LastAssignment = nil
				preempted, err := s.preemptor.IssuePreemptions(ctx, e.preemptionTargets, cq)
				if err != nil {
					log.Error(err, "Failed to preempt workloads")
				}
				if preempted != 0 {
					e.inadmissibleMsg += fmt.Sprintf(". Pending the preemption of %d workload(s)", preempted)
					e.requeueReason = queue.RequeueReasonPendingPreemption
				}
			} else {
				log.V(2).Info("Workload requires preemption, but there are no candidate workloads allowed for preemption", "preemptionReclaimWithinCohort", cq.Preemption.ReclaimWithinCohort, "preemptionWithinClusterQueue", cq.Preemption.WithinClusterQueue)
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
		if err := s.admit(ctx, e, cq.AdmissionChecks); err != nil {
			e.inadmissibleMsg = fmt.Sprintf("Failed to admit workload: %v", err)
		}
	}

	// 6. Requeue the heads that were not scheduled.
	result := metrics.AdmissionResultInadmissible
	for _, e := range entries {
		logAdmissionAttemptIfVerbose(log, &e)
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
	assignment        flavorassigner.Assignment
	status            entryStatus
	inadmissibleMsg   string
	requeueReason     queue.RequeueReason
	preemptionTargets []*workload.Info
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
		} else if workload.HasRetryOrRejectedChecks(w.Obj) {
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
		}
		entries = append(entries, e)
	}
	return entries
}

// resourcesToReserve calculates how much of the available resources in cq/cohort assignment should be reserved.
func resourcesToReserve(e *entry, cq *cache.ClusterQueue) cache.FlavorResourceQuantities {
	if e.assignment.RepresentativeMode() != flavorassigner.Preempt {
		return e.assignment.Usage
	}
	reservedUsage := make(cache.FlavorResourceQuantities)
	for flavor, resourceUsage := range e.assignment.Usage {
		reservedUsage[flavor] = make(map[corev1.ResourceName]int64)
		for resource, usage := range resourceUsage {
			rg := cq.RGByResource[resource]
			cqQuota := cache.ResourceQuota{}
			for _, cqFlavor := range rg.Flavors {
				if cqFlavor.Name == flavor {
					cqQuota = *cqFlavor.Resources[resource]
					break
				}
			}
			if !e.assignment.Borrowing {
				reservedUsage[flavor][resource] = max(0, min(usage, cqQuota.Nominal-cq.Usage[flavor][resource]))
			} else {
				reservedUsage[flavor][resource] = usage
			}

		}
	}
	return reservedUsage
}

type partialAssignment struct {
	assignment        flavorassigner.Assignment
	preemptionTargets []*workload.Info
}

func (s *Scheduler) getAssignments(log logr.Logger, wl *workload.Info, snap *cache.Snapshot) (flavorassigner.Assignment, []*workload.Info) {
	cq := snap.ClusterQueues[wl.ClusterQueue]
	fullAssignment := flavorassigner.AssignFlavors(log, wl, snap.ResourceFlavors, cq, nil)
	var faPreemtionTargets []*workload.Info

	arm := fullAssignment.RepresentativeMode()
	if arm == flavorassigner.Fit {
		return fullAssignment, nil
	}

	if arm == flavorassigner.Preempt {
		faPreemtionTargets = s.preemptor.GetTargets(*wl, fullAssignment, snap)
	}

	// if the feature gate is not enabled or we can preempt
	if !features.Enabled(features.PartialAdmission) || len(faPreemtionTargets) > 0 {
		return fullAssignment, faPreemtionTargets
	}

	if wl.CanBePartiallyAdmitted() {
		reducer := flavorassigner.NewPodSetReducer(wl.Obj.Spec.PodSets, func(nextCounts []int32) (*partialAssignment, bool) {
			assignment := flavorassigner.AssignFlavors(log, wl, snap.ResourceFlavors, cq, nextCounts)
			if assignment.RepresentativeMode() == flavorassigner.Fit {
				return &partialAssignment{assignment: assignment}, true
			}
			preemptionTargets := s.preemptor.GetTargets(*wl, assignment, snap)
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
func (s *Scheduler) admit(ctx context.Context, e *entry, mustHaveChecks sets.Set[string]) error {
	log := ctrl.LoggerFrom(ctx)
	newWorkload := e.Obj.DeepCopy()
	admission := &kueue.Admission{
		ClusterQueue:      kueue.ClusterQueueReference(e.ClusterQueue),
		PodSetAssignments: e.assignment.ToAPI(),
	}

	workload.SetQuotaReservation(newWorkload, admission)
	if workload.HasAllChecks(newWorkload, mustHaveChecks) {
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
			waitStarted := e.Obj.CreationTimestamp.Time
			if c := apimeta.FindStatusCondition(e.Obj.Status.Conditions, kueue.WorkloadEvicted); c != nil {
				waitStarted = c.LastTransitionTime.Time
			}
			waitTime := time.Since(waitStarted)
			s.recorder.Eventf(newWorkload, corev1.EventTypeNormal, "QuotaReserved", "Quota reserved in ClusterQueue %v, wait time since queued was %.0fs", admission.ClusterQueue, waitTime.Seconds())
			if workload.IsAdmitted(newWorkload) {
				s.recorder.Eventf(newWorkload, corev1.EventTypeNormal, "Admitted", "Admitted by ClusterQueue %v, wait time since reservation was 0s ", admission.ClusterQueue)
			}
			metrics.AdmittedWorkload(admission.ClusterQueue, waitTime)
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
		s.requeueAndUpdate(log, ctx, *e)
	})

	return nil
}

func (s *Scheduler) applyAdmissionWithSSA(ctx context.Context, w *kueue.Workload) error {
	return workload.ApplyAdmissionStatus(ctx, s.client, w, false)
}

type entryOrdering []entry

func (e entryOrdering) Len() int {
	return len(e)
}

func (e entryOrdering) Swap(i, j int) {
	e[i], e[j] = e[j], e[i]
}

// Less is the ordering criteria:
// 1. request under nominal quota before borrowing.
// 2. higher priority first.
// 3. FIFO on eviction or creation timestamp.
func (e entryOrdering) Less(i, j int) bool {
	a := e[i]
	b := e[j]

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
	aComparisonTimestamp := workload.GetQueueOrderTimestamp(a.Obj)
	bComparisonTimestamp := workload.GetQueueOrderTimestamp(b.Obj)
	return aComparisonTimestamp.Before(bComparisonTimestamp)
}

func (s *Scheduler) requeueAndUpdate(log logr.Logger, ctx context.Context, e entry) {
	if e.status != notNominated && e.requeueReason == queue.RequeueReasonGeneric {
		// Failed after nomination is the only reason why a workload would be requeued downstream.
		e.requeueReason = queue.RequeueReasonFailedAfterNomination
	}
	added := s.queues.RequeueWorkload(ctx, &e.Info, e.requeueReason)
	log.V(2).Info("Workload re-queued", "workload", klog.KObj(e.Obj), "clusterQueue", klog.KRef("", e.ClusterQueue), "queue", klog.KRef(e.Obj.Namespace, e.Obj.Spec.QueueName), "requeueReason", e.requeueReason, "added", added)

	if e.status == notNominated || e.status == skipped {
		workload.UnsetQuotaReservationWithCondition(e.Obj, "Pending", e.inadmissibleMsg)
		err := workload.ApplyAdmissionStatus(ctx, s.client, e.Obj, true)
		if err != nil {
			log.Error(err, "Could not update Workload status")
		}
		s.recorder.Eventf(e.Obj, corev1.EventTypeNormal, "Pending", api.TruncateEventMessage(e.inadmissibleMsg))
	}
}
