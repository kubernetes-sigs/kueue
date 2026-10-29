//go:build !exclude_scheduler_library

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

package was

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"sync"

	corev1 "k8s.io/api/core/v1"
	schedulingv1alpha3 "k8s.io/api/scheduling/v1alpha3"
	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/scheduler-library/pkg/preemption"
	schedlib "sigs.k8s.io/scheduler-library/pkg/upstreamsync/snapshot"

	"sigs.k8s.io/kueue/pkg/cache/scheduler/podtracker"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	"sigs.k8s.io/kueue/pkg/features"
)

const FailedReasonSkipped = "skipped"

var _ simulator.SchedulerSimulator = (*wasSimulator)(nil)

var _ schedlib.PreemptionVictim = (*preemptionVictim)(nil)

var (
	errFindFeasibleNodesNotAllowed = errors.New("FindFeasibleNodes() not allowed when SchedulerLibraryDeepIntegration enabled")
	errScheduleWorkloadNotAllowed  = errors.New("ScheduleWorkload() not allowed when SchedulerLibraryDeepIntegration disabled")
)

type wasSimulator struct {
	// wasSnapshot is the cluster as it stands, with every tracked Pod on its node.
	wasSnapshot *schedlib.ClusterSnapshot
	// pods is this cycle's tracker, including unmanaged and virtual Pods.
	// PreemptWorkload only releases Pods indexed by Workload.
	pods *podtracker.Tracker
	// emptyCluster holds the same nodes with no Pods, for callers asking what would
	// fit if nothing were running.
	emptyCluster lazyCluster
	// podGroups and compositePodGroups track groups added via AddPodGroup and
	// AddCompositePodGroup so they can be replayed onto emptyCluster when it is
	// lazily initialized.
	podGroups          map[client.ObjectKey]*schedulingv1beta1.PodGroup
	compositePodGroups map[client.ObjectKey]*schedulingv1alpha3.CompositePodGroup
}

// lazyCluster builds its cluster on first use, so cycles that never ask do not
// pay for it.
type lazyCluster struct {
	// build produces the cluster. It runs at most once.
	build func(context.Context) (*schedlib.ClusterSnapshot, error)
	once  sync.Once
	// value and err hold what build returned, and are only read after once has run.
	value *schedlib.ClusterSnapshot
	err   error
}

func (s *wasSimulator) getEmptyCluster(ctx context.Context) (*schedlib.ClusterSnapshot, error) {
	s.emptyCluster.once.Do(func() {
		cluster, err := s.emptyCluster.build(ctx)
		if err != nil {
			s.emptyCluster.err = err
			return
		}
		for _, cpg := range s.compositePodGroups {
			if err := cluster.AddCompositePodGroup(ctx, cpg); err != nil {
				s.emptyCluster.err = err
				return
			}
		}
		for _, pg := range s.podGroups {
			if err := cluster.AddPodGroup(ctx, pg); err != nil {
				s.emptyCluster.err = err
				return
			}
		}
		s.emptyCluster.value = cluster
	})
	return s.emptyCluster.value, s.emptyCluster.err
}

func (s *wasSimulator) FindFeasibleNodes(
	ctx context.Context,
	candidates iter.Seq[simulator.Candidate],
	requirements *simulator.PodRequirements,
	stats *simulator.NodeExclusionStats,
) ([]simulator.MatchedCandidate, error) {
	if features.Enabled(features.SchedulerLibraryDeepIntegration) {
		return nil, errFindFeasibleNodesNotAllowed
	}
	var candidateLeaves = make(map[string]simulator.MatchedCandidate)
	var candidateNodeNames []string
	var feasibleCandidates []simulator.MatchedCandidate

	for candidate := range candidates {
		matchedCandidate, ok := candidate.(simulator.MatchedCandidate)
		if !ok {
			return nil, fmt.Errorf("failed to cast candidate %T to simulator.MatchedCandidate", candidate)
		}

		stats.TotalNodes++
		nodeObj := candidate.GetNode()
		candidateNodeNames = append(candidateNodeNames, nodeObj.Name)
		candidateLeaves[nodeObj.Name] = matchedCandidate
	}

	dummyPod := &corev1.Pod{
		ObjectMeta: requirements.PodTemplate.ObjectMeta,
		Spec:       requirements.PodTemplate.Spec,
	}
	// The simulator builds one profile, so judge the Pod by it rather than by the scheduler it names.
	dummyPod.Spec.SchedulerName = corev1.DefaultSchedulerName
	cluster := s.wasSnapshot
	if requirements.SimulateEmpty {
		var err error
		if cluster, err = s.getEmptyCluster(ctx); err != nil {
			return nil, err
		}
	}
	placement, err := cluster.MakePlacement(candidateNodeNames)
	if err != nil {
		return nil, err
	}
	feasibleNodeNames, _, err := cluster.CanSchedulePod(ctx, dummyPod, placement)
	if err != nil {
		return nil, err
	}

	for _, nodeName := range feasibleNodeNames {
		leaf := candidateLeaves[nodeName]
		feasibleCandidates = append(feasibleCandidates, leaf)
		if features.Enabled(features.TASRespectNodeAffinityPreferred) && requirements.PreferredSchedulingTerms != nil {
			newAffinityScore := leaf.GetAffinityScore() + requirements.PreferredSchedulingTerms.Score(leaf.GetNode())
			leaf.SetAffinityScore(newAffinityScore)
		}
	}
	stats.SchedulerLibraryNoFit = len(candidateNodeNames) - len(feasibleNodeNames)

	return feasibleCandidates, nil
}

func (s *wasSimulator) PreemptWorkload(ctx context.Context, wlKey client.ObjectKey) (func() error, error) {
	// Pods with indeterminate workloads are not indexed by Workload and are omitted from preemptions.
	// This means the simulation may be more restrictive than the real scheduler would be,
	// if the preempted workload has pods that do not identify with it directly.
	unpreempt, err := s.wasSnapshot.PreemptPods(ctx, s.pods.PodsForWorkload(wlKey))
	if err != nil {
		return nil, fmt.Errorf("failed to preempt workload's pods from WAS snapshot: %w", err)
	}

	return func() error {
		_, err := s.wasSnapshot.Unpreempt(unpreempt)
		return err
	}, nil
}

func (s *wasSimulator) Simulate(ctx context.Context, fn func()) error {
	return s.wasSnapshot.Transaction(ctx, func() (schedlib.TransactionResult, error) {
		fn()
		return schedlib.Revert, nil
	})
}

func (s *wasSimulator) ScheduleWorkload(ctx context.Context, workloadPods []*corev1.Pod, opts ...simulator.ScheduleOption) simulator.SchedulingResult {
	if !features.Enabled(features.SchedulerLibraryDeepIntegration) {
		return simulator.SchedulingResult{Error: errScheduleWorkloadNotAllowed}
	}

	scheduleOpts, err := simulator.BuildScheduleOptions(opts)
	if err != nil {
		return simulator.SchedulingResult{Error: fmt.Errorf("ScheduleWorkload() error when processing ScheduleOptions: %w", err)}
	}

	cluster, err := s.clusterForSchedule(ctx, scheduleOpts)
	if err != nil {
		return simulator.SchedulingResult{Error: err}
	}

	wasResult := cluster.ScheduleWorkload(ctx, copyWithDefaultScheduler(workloadPods...), s.buildScheduleWorkloadOptions(scheduleOpts))
	return toSchedulingResult(workloadPods, wasResult)
}

func (s *wasSimulator) clusterForSchedule(ctx context.Context, scheduleOpts *simulator.ScheduleOptions) (*schedlib.ClusterSnapshot, error) {
	if scheduleOpts.SimulateEmpty {
		return s.getEmptyCluster(ctx)
	}
	return s.wasSnapshot, nil
}

func (s *wasSimulator) buildScheduleWorkloadOptions(scheduleOpts *simulator.ScheduleOptions) schedlib.ScheduleWorkloadOptions {
	libOpts := schedlib.ScheduleWorkloadOptions{
		DryRun: true,
	}
	if scheduleOpts.PreemptionFilter != nil {
		libOpts.PreemptionFilter = &preemptionFilterAdapter{PreemptionFilter: scheduleOpts.PreemptionFilter}
	}
	if !scheduleOpts.SimulateEmpty {
		libOpts.PotentialVictims = s.potentialVictims(scheduleOpts.PreemptionCandidates)
		libOpts.CommittedVictims = s.committedVictims(scheduleOpts.CommittedPreemptionVictims)
	}
	return libOpts
}

func (s *wasSimulator) committedVictims(victimWorkloads []client.ObjectKey) (cv []preemption.PreemptionVictim) {
	for _, preemptedWlKey := range victimWorkloads {
		if victimPods := s.pods.PodsForWorkload(preemptedWlKey); victimPods != nil {
			cv = append(cv, &preemptionVictim{
				wlKey: preemptedWlKey,
				pods:  victimPods,
			})
		}
	}
	return
}

func toSchedulingResult(workloadPods []*corev1.Pod, wasResult schedlib.WorkloadSchedulingResult) simulator.SchedulingResult {
	if wasResult.Status.IsError() {
		return simulator.SchedulingResult{Error: wasResult.Status.AsError()}
	}

	// Parse scheduler-library results
	placements := make(simulator.PodPlacements, len(workloadPods))
	for _, podResult := range wasResult.PodResults {
		podKey := client.ObjectKeyFromObject(podResult.Pod)
		status := podResult.Status
		if status.IsSuccess() {
			placements[podKey] = &simulator.PodPlacement{Node: types.NodeName(podResult.SelectedNodeName)}
		} else {
			placements[podKey] = &simulator.PodPlacement{
				Error:   status.AsError(),
				Reasons: status.Reasons(),
			}
		}
	}

	// Add entries for pods not scheduled by the scheduler-library
	for _, wlPod := range workloadPods {
		podKey := client.ObjectKeyFromObject(wlPod)
		if _, placed := placements[podKey]; !placed {
			placements[podKey] = &simulator.PodPlacement{Reasons: []string{FailedReasonSkipped}}
		}
	}

	return simulator.SchedulingResult{
		PodPlacements:     placements,
		PreemptionVictims: toPreemptionVictims(wasResult.PreemptionVictims),
	}
}

func (s *wasSimulator) AddPodGroup(ctx context.Context, pg *schedulingv1beta1.PodGroup) error {
	if err := s.wasSnapshot.AddPodGroup(ctx, pg); err != nil {
		return err
	}
	if s.emptyCluster.value != nil {
		if err := s.emptyCluster.value.AddPodGroup(ctx, pg); err != nil {
			return err
		}
	}
	s.podGroups[client.ObjectKeyFromObject(pg)] = pg
	return nil
}

func (s *wasSimulator) RemovePodGroup(ctx context.Context, pg *schedulingv1beta1.PodGroup) error {
	if err := s.wasSnapshot.RemovePodGroup(ctx, pg); err != nil {
		return err
	}
	if s.emptyCluster.value != nil {
		if err := s.emptyCluster.value.RemovePodGroup(ctx, pg); err != nil {
			return err
		}
	}
	delete(s.podGroups, client.ObjectKeyFromObject(pg))
	return nil
}

func (s *wasSimulator) AddCompositePodGroup(ctx context.Context, cpg *schedulingv1alpha3.CompositePodGroup) error {
	if err := s.wasSnapshot.AddCompositePodGroup(ctx, cpg); err != nil {
		return err
	}
	if s.emptyCluster.value != nil {
		if err := s.emptyCluster.value.AddCompositePodGroup(ctx, cpg); err != nil {
			return err
		}
	}
	s.compositePodGroups[client.ObjectKeyFromObject(cpg)] = cpg
	return nil
}

func (s *wasSimulator) RemoveCompositePodGroup(ctx context.Context, cpg *schedulingv1alpha3.CompositePodGroup) error {
	if err := s.wasSnapshot.RemoveCompositePodGroup(ctx, cpg); err != nil {
		return err
	}
	if s.emptyCluster.value != nil {
		if err := s.emptyCluster.value.RemoveCompositePodGroup(ctx, cpg); err != nil {
			return err
		}
	}
	delete(s.compositePodGroups, client.ObjectKeyFromObject(cpg))
	return nil
}

func copyWithDefaultScheduler(pods ...*corev1.Pod) []*corev1.Pod {
	copiedPods := make([]*corev1.Pod, len(pods))
	for i, pod := range pods {
		copiedPods[i] = pod.DeepCopy()
		// The simulator builds one profile, so judge the Pod by it rather than by the scheduler it names.
		copiedPods[i].Spec.SchedulerName = corev1.DefaultSchedulerName
	}
	return copiedPods
}
