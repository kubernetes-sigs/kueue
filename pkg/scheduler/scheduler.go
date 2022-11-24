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
	"strings"
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
	corev1helpers "k8s.io/component-helpers/scheduling/corev1"
	"k8s.io/component-helpers/scheduling/corev1/nodeaffinity"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha2"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/queue"
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

	// Stubs.
	applyAdmission func(context.Context, *kueue.Workload) error
}

func New(queues *queue.Manager, cache *cache.Cache, cl client.Client, recorder record.EventRecorder) *Scheduler {
	s := &Scheduler{
		queues:                  queues,
		cache:                   cache,
		client:                  cl,
		recorder:                recorder,
		admissionRoutineWrapper: routine.DefaultWrapper,
	}
	s.applyAdmission = s.applyAdmissionWithSSA
	return s
}

func (s *Scheduler) Start(ctx context.Context) {
	log := ctrl.LoggerFrom(ctx).WithName("scheduler")
	ctx = ctrl.LoggerInto(ctx, log)
	wait.UntilWithContext(ctx, s.schedule, 0)
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
	usedCohorts := sets.NewString()
	for i := range entries {
		e := &entries[i]
		if e.status != nominated {
			continue
		}
		c := snapshot.ClusterQueues[e.ClusterQueue]
		if len(e.borrows) > 0 && c.Cohort != nil && usedCohorts.Has(c.Cohort.Name) {
			e.status = skipped
			e.inadmissibleMsg = "cohort used in this cycle"
			continue
		}
		log := log.WithValues("workload", klog.KObj(e.Obj), "clusterQueue", klog.KRef("", e.ClusterQueue))
		if err := s.admit(ctrl.LoggerInto(ctx, log), e); err == nil {
			e.status = assumed
		} else {
			e.inadmissibleMsg = fmt.Sprintf("Failed to admit workload: %v", err)
		}
		// Even if there was a failure, we shouldn't admit other workloads to this
		// cohort.
		if c.Cohort != nil {
			usedCohorts.Insert(c.Cohort.Name)
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
	// indicates if the workload was nominated but skipped in this cycle.
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
	// borrows is the resources that the workload would need to borrow from the
	// cohort if it was scheduled in the clusterQueue.
	borrows         cache.ResourceQuantities
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
		} else if status := e.assignFlavors(log, snap.ResourceFlavors, cq); !status.IsSuccess() {
			e.inadmissibleMsg = api.TruncateEventMessage(status.Message())
		} else {
			e.status = nominated
		}
		entries = append(entries, e)
	}
	return entries
}

type admissionStatus struct {
	podSet  string
	reasons []string
	err     error
}

// Message returns a concatenated message on reasons of the admissionStatus.
func (s *admissionStatus) Message() string {
	if s.IsSuccess() {
		return ""
	}
	if s.IsError() {
		return fmt.Sprintf("Couldn't assign flavors for podSet %s: %v", s.podSet, s.err)
	}
	sort.Strings(s.reasons)
	msg := strings.Join(s.reasons, "; ")
	return fmt.Sprintf("Workload's %q podSet didn't fit: %s", s.podSet, msg)
}

// AppendReason appends given reasons to the admissionStatus.
func (s *admissionStatus) AppendReason(reasons ...string) {
	s.reasons = append(s.reasons, reasons...)
}

// IsSuccess returns true if "admissionStatus" is nil.
func (s *admissionStatus) IsSuccess() bool {
	return s == nil
}

// IsError returns true if "admissionStatus" has error.
func (s *admissionStatus) IsError() bool {
	return s != nil && s.err != nil
}

// asStatus wraps an error in a admissionStatus.
func asStatus(err error) *admissionStatus {
	return &admissionStatus{
		err: err,
	}
}

// assignFlavors calculates the flavors that should be assigned to this entry
// if admitted by this clusterQueue, including details of how much it needs to
// borrow from the cohort.
// It returns admissionStatus indicating whether the entry fits. If it doesn't fit,
// the entry is unmodified.
func (e *entry) assignFlavors(log logr.Logger, resourceFlavors map[string]*kueue.ResourceFlavor, cq *cache.ClusterQueue) *admissionStatus {
	flavoredRequests := make([]workload.PodSetResources, 0, len(e.TotalRequests))
	wUsed := make(cache.ResourceQuantities)
	wBorrows := make(cache.ResourceQuantities)
	for i, podSet := range e.TotalRequests {
		assignedFlavors := make(map[corev1.ResourceName]string, len(podSet.Requests))
		for resName := range podSet.Requests {
			if assignedFlavors[resName] != "" {
				// This resource got assigned the same flavor as a codependent resource.
				// No need to compute again.
				continue
			}
			if _, ok := cq.RequestableResources[resName]; !ok {
				return &admissionStatus{
					reasons: []string{fmt.Sprintf("resource %s unavailable in ClusterQueue", resName)},
				}
			}
			codepResources := cq.RequestableResources[resName].CodependentResources
			if codepResources.Len() == 0 {
				codepResources = sets.NewString(string(resName))
			}
			codepReq := filterRequestedResources(podSet.Requests, codepResources)
			rFlavor, borrows, status := findFlavorForCodepResources(log, codepReq, wUsed, resourceFlavors, cq, &e.Obj.Spec.PodSets[i].Spec)
			if !status.IsSuccess() {
				status.podSet = e.Obj.Spec.PodSets[i].Name
				return status
			}
			for codepRes, codepVal := range codepReq {
				if b := borrows[codepRes]; b > 0 {
					if wBorrows[codepRes] == nil {
						wBorrows[codepRes] = make(map[string]int64)
					}
					// Don't accumulate borrowing. The returned `borrow` already considers
					// usage from previous pod sets.
					wBorrows[codepRes][rFlavor] = b
				}
				if wUsed[codepRes] == nil {
					wUsed[codepRes] = make(map[string]int64)
				}
				wUsed[codepRes][rFlavor] += codepVal
				assignedFlavors[codepRes] = rFlavor
			}
		}
		flavoredRequests = append(flavoredRequests, workload.PodSetResources{
			Name:     podSet.Name,
			Requests: podSet.Requests,
			Flavors:  assignedFlavors,
		})
	}
	e.TotalRequests = flavoredRequests
	if len(wBorrows) > 0 {
		e.borrows = wBorrows
	}
	return nil
}

// admit sets the admitting clusterQueue and flavors into the workload of
// the entry, and asynchronously updates the object in the apiserver after
// assuming it in the cache.
func (s *Scheduler) admit(ctx context.Context, e *entry) error {
	log := ctrl.LoggerFrom(ctx)
	newWorkload := e.Obj.DeepCopy()
	admission := &kueue.Admission{
		ClusterQueue:  kueue.ClusterQueueReference(e.ClusterQueue),
		PodSetFlavors: make([]kueue.PodSetFlavors, len(e.TotalRequests)),
	}
	for i := range e.TotalRequests {
		admission.PodSetFlavors[i] = kueue.PodSetFlavors{
			Name:    e.Obj.Spec.PodSets[i].Name,
			Flavors: e.TotalRequests[i].Flavors,
		}
	}
	newWorkload.Spec.Admission = admission
	if err := s.cache.AssumeWorkload(newWorkload); err != nil {
		return err
	}
	log.V(2).Info("Workload assumed in the cache")

	s.admissionRoutineWrapper.Run(func() {
		err := s.applyAdmission(ctx, workloadAdmissionFrom(newWorkload))
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

// workloadAdmissionFrom returns only the fields necessary for admission using
// ServerSideApply.
func workloadAdmissionFrom(w *kueue.Workload) *kueue.Workload {
	wlCopy := &kueue.Workload{
		ObjectMeta: metav1.ObjectMeta{
			UID:        w.UID,
			Name:       w.Name,
			Namespace:  w.Namespace,
			Generation: w.Generation, // Produce a conflict if there was a change in the spec.
		},
		TypeMeta: w.TypeMeta,
		Spec: kueue.WorkloadSpec{
			Admission: w.Spec.Admission.DeepCopy(),
		},
	}
	if wlCopy.APIVersion == "" {
		wlCopy.APIVersion = kueue.GroupVersion.String()
	}
	if wlCopy.Kind == "" {
		wlCopy.Kind = "Workload"
	}
	return wlCopy
}

// findFlavorForCodepResources returns a flavor which can satisfy the resource request,
// given that wUsed is the usage of flavors by previous podsets.
// If it finds a flavor, also returns any borrowing required.
func findFlavorForCodepResources(
	log logr.Logger,
	requests workload.Requests,
	wUsed cache.ResourceQuantities,
	resourceFlavors map[string]*kueue.ResourceFlavor,
	cq *cache.ClusterQueue,
	spec *corev1.PodSpec) (string, map[corev1.ResourceName]int64, *admissionStatus) {
	var status admissionStatus

	// Keep any resource name as an anchor to gather flavors for.
	var rName corev1.ResourceName
	for rName = range requests {
		break
	}

	// We will only check against the flavors' labels for the resource.
	// Since all the resources share the same flavors, they use the same selector.
	selector := flavorSelector(spec, cq.LabelKeys[rName])
	for i, flvLimit := range cq.RequestableResources[rName].Flavors {
		flavor, exist := resourceFlavors[flvLimit.Name]
		if !exist {
			log.Error(nil, "Flavor not found", "Flavor", flvLimit.Name)
			status.AppendReason(fmt.Sprintf("flavor %s not found", flvLimit.Name))
			continue
		}
		taint, untolerated := corev1helpers.FindMatchingUntoleratedTaint(flavor.Taints, spec.Tolerations, func(t *corev1.Taint) bool {
			return t.Effect == corev1.TaintEffectNoSchedule || t.Effect == corev1.TaintEffectNoExecute
		})
		if untolerated {
			status.AppendReason(fmt.Sprintf("untolerated taint %s in flavor %s", taint, flvLimit.Name))
			continue
		}
		if match, err := selector.Match(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Labels: flavor.NodeSelector}}); !match || err != nil {
			if err != nil {
				return "", nil, asStatus(fmt.Errorf("matching affinity flavor %s: %w", flvLimit.Name, err))
			}
			status.AppendReason(fmt.Sprintf("flavor %s doesn't match with node affinity", flvLimit.Name))
			continue
		}

		fitsAll := true
		borrows := make(map[corev1.ResourceName]int64, len(requests))
		for name, val := range requests {
			codepFlvLimit := cq.RequestableResources[name].Flavors[i]
			// Check considering the flavor usage by previous pod sets.
			borrow, s := fitsFlavorLimits(name, val+wUsed[name][flavor.Name], cq, &codepFlvLimit)
			if s.IsError() {
				return "", nil, s
			}
			if !s.IsSuccess() {
				fitsAll = false
				status.AppendReason(s.reasons...)
				break
			}
			borrows[name] = borrow
		}
		if fitsAll {
			return flavor.Name, borrows, nil
		}
	}
	return "", nil, &status
}

func flavorSelector(spec *corev1.PodSpec, allowedKeys sets.String) nodeaffinity.RequiredNodeAffinity {
	// This function generally replicates the implementation of kube-scheduler's NodeAffintiy
	// Filter plugin as of v1.24.
	var specCopy corev1.PodSpec

	// Remove affinity constraints with irrelevant keys.
	if len(spec.NodeSelector) != 0 {
		specCopy.NodeSelector = map[string]string{}
		for k, v := range spec.NodeSelector {
			if allowedKeys.Has(k) {
				specCopy.NodeSelector[k] = v
			}
		}
	}

	affinity := spec.Affinity
	if affinity != nil && affinity.NodeAffinity != nil && affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution != nil {
		var termsCopy []corev1.NodeSelectorTerm
		for _, t := range affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
			var expCopy []corev1.NodeSelectorRequirement
			for _, e := range t.MatchExpressions {
				if allowedKeys.Has(e.Key) {
					expCopy = append(expCopy, e)
				}
			}
			// If a term becomes empty, it means node affinity matches any flavor since those terms are ORed,
			// and so matching gets reduced to spec.NodeSelector
			if len(expCopy) == 0 {
				termsCopy = nil
				break
			}
			termsCopy = append(termsCopy, corev1.NodeSelectorTerm{MatchExpressions: expCopy})
		}
		if len(termsCopy) != 0 {
			specCopy.Affinity = &corev1.Affinity{
				NodeAffinity: &corev1.NodeAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
						NodeSelectorTerms: termsCopy,
					},
				},
			}
		}
	}
	return nodeaffinity.GetRequiredNodeAffinity(&corev1.Pod{Spec: specCopy})
}

// fitsFlavorLimits returns whether a requested resource fits in a specific flavor's quota limits.
// If it fits, also returns any borrowing required.
func fitsFlavorLimits(rName corev1.ResourceName, val int64, cq *cache.ClusterQueue, flavor *cache.FlavorLimits) (int64, *admissionStatus) {
	var status admissionStatus
	used := cq.UsedResources[rName][flavor.Name]
	if flavor.Max != nil && used+val > *flavor.Max {
		status.AppendReason(fmt.Sprintf("borrowing limit for %s flavor %s exceeded", rName, flavor.Name))
		return 0, &status
	}
	cohortUsed := used
	cohortTotal := flavor.Min
	if cq.Cohort != nil {
		cohortUsed = cq.Cohort.UsedResources[rName][flavor.Name]
		cohortTotal = cq.Cohort.RequestableResources[rName][flavor.Name]
	}
	borrow := used + val - flavor.Min
	if borrow < 0 {
		borrow = 0
	}

	lack := cohortUsed + val - cohortTotal
	if lack > 0 {
		lackQuantity := workload.ResourceQuantity(rName, lack)
		if cq.Cohort == nil {
			status.AppendReason(fmt.Sprintf("insufficient quota for %s flavor %s, %s more needed", rName, flavor.Name, &lackQuantity))
		} else {
			status.AppendReason(fmt.Sprintf("insufficient quota for %s flavor %s, %s more needed after borrowing", rName, flavor.Name, &lackQuantity))
		}
		// TODO(PostMVP): preemption could help if borrow == 0
		return 0, &status
	}
	return borrow, nil
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
	aMin := len(a.borrows) == 0
	bMin := len(b.borrows) == 0
	if aMin != bMin {
		return aMin
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
	log.V(2).Info("Workload re-queued", "workload", klog.KObj(e.Obj), "clusterQueue", e.ClusterQueue, "queue", klog.KRef(e.Obj.Namespace, e.Obj.Spec.QueueName), "requeueReason", e.requeueReason, "added", added)

	if e.status == notNominated {
		err := workload.UpdateStatus(ctx, s.client, e.Obj, kueue.WorkloadAdmitted, metav1.ConditionFalse, "Pending", e.inadmissibleMsg)
		if err != nil {
			log.Error(err, "Could not update Workload status")
		}
		s.recorder.Eventf(e.Obj, corev1.EventTypeNormal, "Pending", e.inadmissibleMsg)
	}
}

func filterRequestedResources(req workload.Requests, allowList sets.String) workload.Requests {
	filtered := make(workload.Requests)
	for n, v := range req {
		if allowList.Has(string(n)) {
			filtered[n] = v
		}
	}
	return filtered
}
