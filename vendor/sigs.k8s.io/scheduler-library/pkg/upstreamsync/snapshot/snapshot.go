// Copyright The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package snapshot

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"math"
	"reflect"
	"slices"

	v1 "k8s.io/api/core/v1"
	schedulingv1alpha3 "k8s.io/api/scheduling/v1alpha3"
	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	fwk "k8s.io/kube-scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/backend/cache"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"sigs.k8s.io/scheduler-library/pkg/upstreamsync"
)

// ClusterSnapshot wraps a scheduler snapshot and its associated frameworks.
// All ClusterSnapshot instances created from the same ClusterState share the
// same underlying cache.Snapshot. Creating a new snapshot via ClusterState.Snapshot
// updates that shared snapshot in-place, which invalidates any previously returned
// ClusterSnapshot instance — callers must not use a prior snapshot after requesting a new one.
// A ClusterSnapshot is not safe for concurrent use.
type ClusterSnapshot struct {
	// profiles holds the scheduling framework per scheduler name. All of them share
	// schedulerSnapshot as their SnapshotSharedLister, so the plugins always see the mutations
	// performed here.
	profiles *upstreamsync.ProfileMap
	// schedulerSnapshot is the upstream snapshot holding the actual node and pod data.
	schedulerSnapshot *cache.Snapshot
	// undoLog records how to undo every mutation applied to schedulerSnapshot, so that a dry run
	// or a reverted transaction can restore the state it started from.
	undoLog undoLog
	// transactionInProgress guards against nested transactions and tells the mutating methods to
	// leave the undo log alone, as the enclosing transaction owns it.
	transactionInProgress bool
	// stateVersionForPreemption is bumped whenever a mutation makes the outstanding Unpreemption
	// handles unusable, i.e. whenever the state they would be restoring into is no longer the one
	// they were taken from. Unpreempt compares it with the value recorded in the handle.
	stateVersionForPreemption uint64
}

// undoLog is a stack of the operations reverting the mutations applied to the snapshot, most
// recent last. Every mutation pushes its revert function, and rolling back means popping and
// running them until the recorded state version is reached again.
type undoLog struct {
	// undoOperations are the revert functions, in the order their mutations were applied.
	undoOperations []func()
	// stateVersion is incremented by every registered operation and decremented by every undone
	// one. A caller records it before mutating and passes it to restoreState afterwards to undo
	// exactly its own mutations.
	stateVersion uint64
}

// registerOperation pushes the revert function of a mutation that has just been applied.
// A nil undoOperation is ignored, so that callers can pass the result of an operation that
// did not change anything.
func (ul *undoLog) registerOperation(undoOperation func()) {
	if undoOperation != nil {
		ul.undoOperations = append(ul.undoOperations, undoOperation)
		ul.stateVersion++
	}
}

// restoreState undoes the operations registered after the given state version was observed,
// in the reverse order of their registration.
func (ul *undoLog) restoreState(stateVersion uint64) {
	for ul.stateVersion != stateVersion {
		ul.undo()
	}
}

// undo pops the most recently registered operation and runs it.
func (ul *undoLog) undo() {
	ops := ul.undoOperations
	ops, undoOp := ops[:len(ops)-1], ops[len(ops)-1]
	ul.undoOperations = ops
	undoOp()
	ul.stateVersion--
}

// New creates a new ClusterSnapshot stub wrapping the provided scheduler snapshot and frameworks.
//
// Consumers should obtain a ClusterSnapshot from simulator.SchedulingSimulator instead, either via
// NewClusterSnapshot or via NewClusterState followed by state.ClusterState.Snapshot: those build
// the full plugin chain out of the KubeSchedulerConfiguration and initialize the scheduler metrics,
// which this constructor expects to have been done already.
func New(s *cache.Snapshot, profiles *upstreamsync.ProfileMap) *ClusterSnapshot {
	return &ClusterSnapshot{
		profiles:          profiles,
		schedulerSnapshot: s,
	}
}

// ResetMutations restores the snapshot to its state prior to any mutations,
// executing all accumulated undo operations in reverse order.
func (s *ClusterSnapshot) ResetMutations() error {
	if s.transactionInProgress {
		return fmt.Errorf("transaction is in progress, cannot reset mutations")
	}
	if s.undoLog.stateVersion > 0 {
		s.undoLog.restoreState(0)
		s.stateVersionForPreemption++
	}
	return nil
}

// Transaction executes the provided function within a transaction.
// It rolls back operations if the function returns Revert or an error.
// Only a single active transaction is supported at any given time;
// attempting to start a nested transaction will return an error.
// Committed operations or operations made outside of transaction scope
// can only be reverted by [ClusterSnapshot.ResetMutations].
func (s *ClusterSnapshot) Transaction(ctx context.Context, transactionFn func() (TransactionResult, error)) error {
	if s.transactionInProgress {
		return fmt.Errorf("a transaction is already in progress")
	}

	s.transactionInProgress = true
	defer func() { s.transactionInProgress = false }()

	initialStateVersion := s.undoLog.stateVersion
	initialStateVersionForPreemption := s.stateVersionForPreemption
	s.stateVersionForPreemption++

	result, err := transactionFn()

	if err != nil || result == Revert {
		s.undoLog.restoreState(initialStateVersion)
		s.stateVersionForPreemption = initialStateVersionForPreemption
	} else {
		// invalidate preemptions done within the transaction
		s.stateVersionForPreemption++
	}

	if err != nil {
		return fmt.Errorf("transaction failed: %w", err)
	}
	return nil
}

// CanSchedulePod checks feasibility of a single pod on the specified nodes by running
// PreFilter and Filter plugins. Returns the names of nodes on which the pod can be scheduled,
// the framework.Diagnosis for rejected nodes, and any error.
func (s *ClusterSnapshot) CanSchedulePod(ctx context.Context, pod *v1.Pod, placement *fwk.Placement) ([]string, *framework.Diagnosis, error) {
	if placement == nil || len(placement.Nodes) == 0 {
		return nil, nil, nil
	}
	schedFramework, err := s.profiles.FrameworkForPod(pod)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get framework: %w", err)
	}
	state := framework.NewCycleState()
	podInfo, err := framework.NewPodInfo(pod)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create pod info: %w", err)
	}
	pendingPod := &upstreamsync.PendingPod{
		PodInfo:    podInfo,
		CycleState: state,
	}

	feasibleNodes := make([]string, 0)
	var diagnosis framework.Diagnosis
	sched := upstreamsync.NewScheduler(s.schedulerSnapshot, 0, 0, math.MaxInt32, nil)
	err = s.schedulerSnapshot.AssumePlacement(placement)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to assume placement: %w", err)
	}
	defer s.schedulerSnapshot.ForgetPlacement()
	nodes, diag, _, err := sched.FindAllNodesThatFitPod(ctx, schedFramework, pendingPod)
	diagnosis = diag
	for _, node := range nodes {
		feasibleNodes = append(feasibleNodes, node.Node().Name)
	}
	if err != nil {
		return nil, &diagnosis, fmt.Errorf("failed to find nodes that fit pod: %w", err)
	}

	return feasibleNodes, &diagnosis, nil
}

func schedulingResult(algRes *upstreamsync.AlgorithmResult) SchedulingResult {
	return SchedulingResult{
		Pod:              algRes.GetPod(),
		Status:           algRes.GetStatus(),
		SelectedNodeName: algRes.GetNodeName(),
		CycleState:       algRes.GetCycleState(),
	}
}

// SchedulePods schedules the given pods onto the specified placement using PreFilter and Filter plugins.
// StopOnFailure controls whether the first unschedulable pod stops the loop. Note that
// All unexpected execution errors always propagate immediately regardless of StopOnFailure, as they
// indicate a programming error rather than a scheduling failure.
// The pods passed in are left untouched. Each result carries the library's own copy of the pod the
// attempt was made for, with Spec.NodeName set to the selected node when it was scheduled; that
// copy is what PreemptPods takes to remove the pod again. On a pod that was not scheduled
// Spec.NodeName is left as it came in, so it is empty unless the caller already set one.
func (s *ClusterSnapshot) SchedulePods(ctx context.Context, pods []*v1.Pod, placement *fwk.Placement, opts SchedulePodsOptions) ([]SchedulingResult, error) {
	return s.schedulePods(ctx, ownedCopies(pods), placement, opts)
}

// ownedCopies yields a copy of every pod, so that the simulation records the placement it made on a
// pod of its own rather than on one the caller passed in and still owns. The pods generated from a
// template need no such copy, as nothing outside the library holds them.
// The pods are copied one at a time rather than the whole slice up front, so a run that stops early
// never copies the pods it does not attempt.
func ownedCopies(pods []*v1.Pod) iter.Seq[*v1.Pod] {
	return func(yield func(*v1.Pod) bool) {
		for _, pod := range pods {
			if !yield(pod.DeepCopy()) {
				return
			}
		}
	}
}

// SchedulePodsByTemplate attempts to schedule as many pods matching the template as possible.
// It assumes nodes in the placement are feasible and moves to the next node only if the pod is unschedulable on the current node.
func (s *ClusterSnapshot) SchedulePodsByTemplate(ctx context.Context, template *v1.PodTemplateSpec, placement *fwk.Placement, maxPods int, opts SchedulePodsByTemplateOptions) ([]SchedulingResult, error) {
	if maxPods <= 0 {
		return nil, nil
	}

	podIterator := func(yield func(*v1.Pod) bool) {
		for i := range maxPods {
			pod := createPodFromTemplate(template, i)
			if !yield(pod) {
				return
			}
		}
	}

	scheduleOptions := SchedulePodsOptions{
		CommonSchedulingOptions: opts.CommonSchedulingOptions,
		StopOnFailure:           true,
	}

	return s.schedulePods(ctx, podIterator, placement, scheduleOptions)
}

func (s *ClusterSnapshot) schedulePods(ctx context.Context, pods iter.Seq[*v1.Pod], placement *fwk.Placement, opts SchedulePodsOptions) (_ []SchedulingResult, err error) {
	if placement == nil || len(placement.Nodes) == 0 {
		return nil, nil
	}

	initialStateVersion := s.undoLog.stateVersion

	defer func() {
		if err != nil || opts.DryRun {
			s.undoLog.restoreState(initialStateVersion)
		}
		if initialStateVersion != s.undoLog.stateVersion {
			s.stateVersionForPreemption++
		}
	}()

	result := make([]SchedulingResult, 0)

	currentCycle := int64(0)

	err = s.schedulerSnapshot.AssumePlacement(placement)
	if err != nil {
		return nil, fmt.Errorf("error assuming placement: %w", err)
	}
	defer s.schedulerSnapshot.ForgetPlacement()
	for pod := range pods {
		sched := upstreamsync.NewScheduler(s.schedulerSnapshot, currentCycle, 0, 1, nil)

		res, revertFn, err := scheduleOnePod(ctx, s.profiles, sched, pod)

		if err != nil {
			return result, err
		}

		if res.GetStatus().IsSuccess() {
			// The pod object is a copy made within the simulation library. It is not modified by
			// scheduleOnePod, but it is returned in the result object. To make the result placement
			// visible to the caller, pod.Spec.NodeName needs to be set.
			pod.Spec.NodeName = res.GetNodeName()
		}

		if revertFn != nil {
			s.undoLog.registerOperation(revertFn)
		}
		result = append(result, schedulingResult(res))

		if !res.GetStatus().IsSuccess() {
			if opts.StopOnFailure {
				return result, nil
			}
		}

		currentCycle++
	}

	return result, nil
}

// MakePlacement creates a framework.Placement containing NodeInfo structures for each candidate node name.
func (s *ClusterSnapshot) MakePlacement(candidateNodeNames []string) (*fwk.Placement, error) {
	nodes := make([]fwk.NodeInfo, 0, len(candidateNodeNames))
	for _, name := range candidateNodeNames {
		ni, err := s.schedulerSnapshot.NodeInfos().Get(name)
		if err != nil {
			return nil, fmt.Errorf("error getting %s from snapshot: %w", name, err)
		}
		nodes = append(nodes, ni)
	}
	return &fwk.Placement{Nodes: nodes}, nil
}

// PreemptPods removes pods from the snapshot.
// It supports transaction rollbacks if called inside a transaction.
// If any pod fails to be preempted, all previously preempted pods in this call
// are automatically restored and an error is returned.
func (s *ClusterSnapshot) PreemptPods(ctx context.Context, pods []*v1.Pod) (_ *Unpreemption, err error) {
	// Validate all pods before making any changes.
	for _, pod := range pods {
		if pod.Spec.NodeName == "" {
			return nil, fmt.Errorf("pod %s has no node name", klog.KObj(pod))
		}
	}

	initialStateVersion := s.undoLog.stateVersion

	defer func() {
		if err != nil {
			s.undoLog.restoreState(initialStateVersion)
		}
	}()

	mutatingSnapshot := upstreamsync.NewMutatingSnapshot(s.schedulerSnapshot)

	unpreemptFns := []func() error{}

	for _, pod := range pods {
		revertFn, err := removePodFromNode(ctx, mutatingSnapshot, pod)
		if err != nil {
			return nil, fmt.Errorf("failed to unreserve and forget pod %s: %w", klog.KObj(pod), err)
		}
		s.undoLog.registerOperation(revertFn)
		// Putting the pod back is a snapshot mutation like any other, so it goes through
		// addPodToNode and registers its own revert function, undoing it re-preempts the pod.
		unpreemptFns = append(unpreemptFns, func() error {
			repreemptFn, err := addPodToNode(ctx, mutatingSnapshot, pod, pod.Spec.NodeName)
			if err != nil {
				return fmt.Errorf("failed to unpreempt pod %s: %w", klog.KObj(pod), err)
			}
			s.undoLog.registerOperation(repreemptFn)
			return nil
		})
	}

	unpreemptFn := func() error {
		var errs []error
		for _, unpreempt := range slices.Backward(unpreemptFns) {
			// Keep going on failure, so that as many pods as possible are put back.
			if err := unpreempt(); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}

	return &Unpreemption{
		pods:                   pods,
		revertFn:               unpreemptFn,
		validPreemptionVersion: s.stateVersionForPreemption,
	}, nil
}

// Unpreempt undos the preemption done by the PreemptPods.
// The handle is consumed even if putting some of the pods back fails, in which case the pods that
// were restored are still registered in the undo log and are rolled back with the transaction.
func (s *ClusterSnapshot) Unpreempt(u *Unpreemption) ([]*v1.Pod, error) {
	if u == nil {
		return nil, fmt.Errorf("preemption handle is nil")
	}
	if s.stateVersionForPreemption != u.validPreemptionVersion {
		return nil, fmt.Errorf("preemption handle is invalid: snapshot has been permanently mutated since preemption")
	}
	if u.reverted {
		return nil, fmt.Errorf("preemption handle is invalid: already unpreempted")
	}

	defer func() {
		u.reverted = true
	}()

	err := u.revertFn()
	if err != nil {
		return nil, err
	}

	return u.pods, nil
}

// ScheduleWorkload schedules the given pods belonging to the same hierarchy using the workload-aware scheduling algorithm.
// If the pods do not belong to the same hierarchy, it returns an error.
// The order of the returned SchedulingResult slice is non-deterministic with respect to the input pods order.
//
// All PodGroup and CompositePodGroup objects in the hierarchy must exist in the snapshot before calling this method.
// Callers can register virtual groups (groups not present in the cluster) with AddPodGroup and AddCompositePodGroup.
//
// Note: Virtual pods (input pods not present in the snapshot beforehand) are assumed on nodes,
// but not added to internal pod group sets (allPods/assumedPods). Consequently,
// PodGroupState.ScheduledPods() does not include virtual pods from previous ScheduleWorkload calls.
// This is safe when evaluating an entire virtual workload in a single call (such as Kueue admission).
// However, when scheduling a hierarchy incrementally across multiple ScheduleWorkload calls, plugins
// that query ScheduledPods() (such as TAS) do not observe virtual pods from earlier calls.
func (s *ClusterSnapshot) ScheduleWorkload(ctx context.Context, pods []*v1.Pod, opts ScheduleWorkloadOptions) (_ []SchedulingResult, err error) {
	if len(pods) == 0 {
		return nil, nil
	}

	initialStateVersion := s.undoLog.stateVersion

	defer func() {
		if err != nil || opts.DryRun {
			s.undoLog.restoreState(initialStateVersion)
		}
		if initialStateVersion != s.undoLog.stateVersion {
			s.stateVersionForPreemption++
		}
	}()

	podGroupInfo, err := buildPodGroupHierarchy(s.schedulerSnapshot, pods)
	if err != nil {
		return nil, fmt.Errorf("failed to build pod group hierarchy: %w", err)
	}

	schedFramework, err := s.profiles.FrameworkForPodGroup(podGroupInfo)
	if err != nil {
		return nil, fmt.Errorf("failed to get framework for pod group: %w", err)
	}

	sched := upstreamsync.NewScheduler(s.schedulerSnapshot, 0, 0, 1, nil)
	podGroupCycleState := framework.NewCycleState()
	algResultsMap, revertFn := sched.RunRootSchedulingAlgorithm(ctx, schedFramework, podGroupCycleState, podGroupInfo)

	if revertFn != nil {
		s.undoLog.registerOperation(revertFn)
	}

	rootKey := getEntityKey(podGroupInfo)
	rootResult := algResultsMap[rootKey]
	isRootSuccess := rootResult.Status.IsSuccess()

	var results []SchedulingResult
	for _, groupResult := range algResultsMap {
		for _, pRes := range groupResult.PodResults {
			status := pRes.GetStatus()
			nodeName := pRes.GetNodeName()
			pod := pRes.GetPod()
			if isRootSuccess && status.IsSuccess() {
				pod.Spec.NodeName = nodeName
			} else {
				nodeName = ""
				if !isRootSuccess && status.IsSuccess() {
					status = rootResult.Status
				}
			}
			results = append(results, SchedulingResult{
				Pod:              pod,
				Status:           status,
				SelectedNodeName: nodeName,
			})
		}
	}

	return results, nil
}

// AddPodGroup adds a pod group object to the snapshot, linking it to its parent
// composite pod group if CompositePodGroup is enabled.
func (s *ClusterSnapshot) AddPodGroup(ctx context.Context, pg *schedulingv1beta1.PodGroup) error {
	if pg == nil {
		return fmt.Errorf("pod group is nil")
	}
	pgCopy := pg.DeepCopy()
	if err := s.addPodGroup(pgCopy); err != nil {
		return err
	}
	logger := klog.FromContext(ctx)
	s.undoLog.registerOperation(func() {
		if _, err := s.removePodGroup(pgCopy); err != nil {
			logger.Error(err, "failed to remove pod group during state revert", "podGroup", klog.KObj(pgCopy))
		}
	})
	s.stateVersionForPreemption++
	return nil
}

// RemovePodGroup removes a pod group object from the snapshot, unlinking it from its
// parent composite pod group if CompositePodGroup is enabled.
func (s *ClusterSnapshot) RemovePodGroup(ctx context.Context, pg *schedulingv1beta1.PodGroup) error {
	if pg == nil {
		return fmt.Errorf("pod group is nil")
	}
	removedPG, err := s.removePodGroup(pg)
	if err != nil {
		return err
	}
	logger := klog.FromContext(ctx)
	s.undoLog.registerOperation(func() {
		if err := s.addPodGroup(removedPG); err != nil {
			logger.Error(err, "failed to restore pod group during state revert", "podGroup", klog.KObj(removedPG))
		}
	})
	s.stateVersionForPreemption++
	return nil
}

// AddCompositePodGroup adds a composite pod group object to the snapshot, linking it
// to its parent composite pod group if present.
func (s *ClusterSnapshot) AddCompositePodGroup(ctx context.Context, cpg *schedulingv1alpha3.CompositePodGroup) error {
	if cpg == nil {
		return fmt.Errorf("composite pod group is nil")
	}
	cpgCopy := cpg.DeepCopy()
	if err := s.addCompositePodGroup(cpgCopy); err != nil {
		return err
	}
	logger := klog.FromContext(ctx)
	s.undoLog.registerOperation(func() {
		if _, err := s.removeCompositePodGroup(cpgCopy); err != nil {
			logger.Error(err, "failed to remove composite pod group during state revert", "compositePodGroup", klog.KObj(cpgCopy))
		}
	})
	s.stateVersionForPreemption++
	return nil
}

// RemoveCompositePodGroup removes a composite pod group object from the snapshot,
// unlinking it from its parent composite pod group if present.
func (s *ClusterSnapshot) RemoveCompositePodGroup(ctx context.Context, cpg *schedulingv1alpha3.CompositePodGroup) error {
	if cpg == nil {
		return fmt.Errorf("composite pod group is nil")
	}
	removedCPG, err := s.removeCompositePodGroup(cpg)
	if err != nil {
		return err
	}
	logger := klog.FromContext(ctx)
	s.undoLog.registerOperation(func() {
		if err := s.addCompositePodGroup(removedCPG); err != nil {
			logger.Error(err, "failed to restore composite pod group during state revert", "compositePodGroup", klog.KObj(removedCPG))
		}
	})
	s.stateVersionForPreemption++
	return nil
}

// UPSTREAM-DIFF: Upstream kubernetes PR #142177 (https://github.com/kubernetes/kubernetes/pull/142177)
// adds AddGenericPodGroup and RemoveGenericPodGroup directly to cache.Snapshot.
//
// Migration Plan:
// When go.mod updates k8s.io/kubernetes to a version that includes PR #142177:
// 1. Delete all reflection helper functions below:
//    - writableField
//    - addPodGroup
//    - removePodGroup
//    - addCompositePodGroup
//    - removeCompositePodGroup
//    - addChildToParent
//    - removeChildFromParent
//    - getOrCreatePodGroupState
//    - getOrCreateCompositePodGroupState
//    - podGroupEmpty
//    - compositePodGroupEmpty
// 2. Change AddPodGroup and AddCompositePodGroup to call s.schedulerSnapshot.AddGenericPodGroup.
//    Preserve DeepCopy() on input objects because AddGenericPodGroup does not copy the passed object.
// 3. Change RemovePodGroup and RemoveCompositePodGroup to look up the existing object in the
//    snapshot first, pass that existing object to s.schedulerSnapshot.RemoveGenericPodGroup,
//    and store existing.DeepCopy() in the undo log.
// 4. Keep the public ClusterSnapshot methods unchanged so callers do not break.

func writableField(v reflect.Value, name string) reflect.Value {
	f := v.FieldByName(name)
	return reflect.NewAt(f.Type(), f.Addr().UnsafePointer()).Elem()
}

func (s *ClusterSnapshot) addPodGroup(pg *schedulingv1beta1.PodGroup) error {
	key := fwk.PodGroupKey(pg.Namespace, pg.Name)
	rv := reflect.ValueOf(s.schedulerSnapshot).Elem()

	pgs := s.getOrCreatePodGroupState(rv, key)
	pgField := writableField(pgs.Elem(), "podGroup")
	if !pgField.IsNil() {
		return fmt.Errorf("pod group %s already exists in snapshot", key)
	}
	pgField.Set(reflect.ValueOf(pg))

	s.addChildToParent(rv, key, pg.Namespace, pg.Spec.ParentCompositePodGroupName)
	return nil
}

func (s *ClusterSnapshot) removePodGroup(pg *schedulingv1beta1.PodGroup) (*schedulingv1beta1.PodGroup, error) {
	key := fwk.PodGroupKey(pg.Namespace, pg.Name)
	rv := reflect.ValueOf(s.schedulerSnapshot).Elem()
	pgsMap := writableField(rv, "podGroupStates")
	keyVal := reflect.ValueOf(key)

	pgs := pgsMap.MapIndex(keyVal)
	if !pgs.IsValid() || pgs.Elem().FieldByName("podGroup").IsNil() {
		return nil, fmt.Errorf("pod group %s not found in snapshot", key)
	}
	existingPG := writableField(pgs.Elem(), "podGroup").Interface().(*schedulingv1beta1.PodGroup)
	writableField(pgs.Elem(), "podGroup").SetZero()
	if podGroupEmpty(pgs) {
		pgsMap.SetMapIndex(keyVal, reflect.Value{})
	}

	s.removeChildFromParent(rv, key, existingPG.Namespace, existingPG.Spec.ParentCompositePodGroupName)
	return existingPG.DeepCopy(), nil
}

func (s *ClusterSnapshot) addCompositePodGroup(cpg *schedulingv1alpha3.CompositePodGroup) error {
	key := fwk.CompositePodGroupKey(cpg.Namespace, cpg.Name)
	rv := reflect.ValueOf(s.schedulerSnapshot).Elem()

	cpgs := s.getOrCreateCompositePodGroupState(rv, key)
	cpgField := writableField(cpgs.Elem(), "compositePodGroup")
	if !cpgField.IsNil() {
		return fmt.Errorf("composite pod group %s already exists in snapshot", key)
	}
	cpgField.Set(reflect.ValueOf(cpg))

	s.addChildToParent(rv, key, cpg.Namespace, cpg.Spec.ParentCompositePodGroupName)
	return nil
}

func (s *ClusterSnapshot) removeCompositePodGroup(cpg *schedulingv1alpha3.CompositePodGroup) (*schedulingv1alpha3.CompositePodGroup, error) {
	key := fwk.CompositePodGroupKey(cpg.Namespace, cpg.Name)
	rv := reflect.ValueOf(s.schedulerSnapshot).Elem()
	cpgsMap := writableField(rv, "compositePodGroupStates")
	keyVal := reflect.ValueOf(key)

	cpgs := cpgsMap.MapIndex(keyVal)
	if !cpgs.IsValid() || cpgs.Elem().FieldByName("compositePodGroup").IsNil() {
		return nil, fmt.Errorf("composite pod group %s not found in snapshot", key)
	}
	existingCPG := writableField(cpgs.Elem(), "compositePodGroup").Interface().(*schedulingv1alpha3.CompositePodGroup)
	writableField(cpgs.Elem(), "compositePodGroup").SetZero()
	if compositePodGroupEmpty(cpgs) {
		cpgsMap.SetMapIndex(keyVal, reflect.Value{})
	}

	s.removeChildFromParent(rv, key, existingCPG.Namespace, existingCPG.Spec.ParentCompositePodGroupName)
	return existingCPG.DeepCopy(), nil
}

func (s *ClusterSnapshot) addChildToParent(rv reflect.Value, childKey fwk.EntityKey, namespace string, parentName *string) {
	if !rv.FieldByName("compositePodGroupEnabled").Bool() || parentName == nil || *parentName == "" {
		return
	}
	parentKey := fwk.CompositePodGroupKey(namespace, *parentName)
	parent := s.getOrCreateCompositePodGroupState(rv, parentKey)
	children := writableField(parent.Elem(), "children").Interface().(sets.Set[fwk.EntityKey])
	children.Insert(childKey)
}

func (s *ClusterSnapshot) removeChildFromParent(rv reflect.Value, childKey fwk.EntityKey, namespace string, parentName *string) {
	if !rv.FieldByName("compositePodGroupEnabled").Bool() || parentName == nil || *parentName == "" {
		return
	}
	parentKey := fwk.CompositePodGroupKey(namespace, *parentName)
	cpgsMap := writableField(rv, "compositePodGroupStates")
	parentKeyVal := reflect.ValueOf(parentKey)
	if parent := cpgsMap.MapIndex(parentKeyVal); parent.IsValid() {
		children := writableField(parent.Elem(), "children").Interface().(sets.Set[fwk.EntityKey])
		children.Delete(childKey)
		if compositePodGroupEmpty(parent) {
			cpgsMap.SetMapIndex(parentKeyVal, reflect.Value{})
		}
	}
}

func (s *ClusterSnapshot) getOrCreatePodGroupState(rv reflect.Value, key fwk.EntityKey) reflect.Value {
	pgsMap := writableField(rv, "podGroupStates")
	if pgsMap.IsNil() {
		pgsMap.Set(reflect.MakeMap(pgsMap.Type()))
	}
	keyVal := reflect.ValueOf(key)
	pgs := pgsMap.MapIndex(keyVal)
	if !pgs.IsValid() {
		pgs = reflect.New(pgsMap.Type().Elem().Elem())
		for _, field := range []string{"allPods", "unscheduledPods", "assumedPods", "assignedPods"} {
			f := writableField(pgs.Elem(), field)
			f.Set(reflect.MakeMap(f.Type()))
		}
		pgsMap.SetMapIndex(keyVal, pgs)
	}
	return pgs
}

func (s *ClusterSnapshot) getOrCreateCompositePodGroupState(rv reflect.Value, key fwk.EntityKey) reflect.Value {
	cpgsMap := writableField(rv, "compositePodGroupStates")
	if cpgsMap.IsNil() {
		cpgsMap.Set(reflect.MakeMap(cpgsMap.Type()))
	}
	keyVal := reflect.ValueOf(key)
	cpgs := cpgsMap.MapIndex(keyVal)
	if !cpgs.IsValid() {
		cpgs = reflect.New(cpgsMap.Type().Elem().Elem())
		writableField(cpgs.Elem(), "children").Set(reflect.ValueOf(sets.New[fwk.EntityKey]()))
		cpgsMap.SetMapIndex(keyVal, cpgs)
	}
	return cpgs
}

func podGroupEmpty(pgs reflect.Value) bool {
	return pgs.Elem().FieldByName("podGroup").IsNil() &&
		pgs.Elem().FieldByName("allPods").Len() == 0
}

func compositePodGroupEmpty(cpgs reflect.Value) bool {
	return cpgs.Elem().FieldByName("compositePodGroup").IsNil() &&
		cpgs.Elem().FieldByName("children").Len() == 0
}
