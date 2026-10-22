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

// Package preemption provides workload preemption types and simulation adapters
// that bridge the library's PreemptionVictim and PreemptionFilter interfaces to
// the scheduler framework's PreemptionManager and ReprieveFilter interfaces.
package preemption

import (
	"context"
	"fmt"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	fwk "k8s.io/kube-scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	upstreampreemption "sigs.k8s.io/scheduler-library/pkg/upstreamsync/preemption"
)

// EvaluateWorkloadPreemption incrementally adds victims from opts.PotentialVictims
// until the workload fits. For each victim, if opts.PreemptionFilter is set, it marks
// the victim as preempted and skips scheduler evaluation until MayFit returns true.
// When MayFit is true, it runs PodGroupPostFilter plugins, which remove all accumulated
// victims, test if the workload schedules, and reprieve victims in reverse order.
// It stops at the first fitting iteration and returns the last successful
// PodGroupAssignments along with the victims that were not reprieved.
func EvaluateWorkloadPreemption(
	ctx context.Context,
	schedFramework *upstreampreemption.PreemptionFramework,
	podGroupCycleState *framework.CycleState,
	podGroupInfo fwk.PodGroupInfo,
	baseSchedulingFunc fwk.PodGroupSchedulingFunc,
	opts WorkloadPreemptionOptions,
) (*fwk.PodGroupAssignments, []PreemptionVictim, *fwk.Status) {
	if opts.PotentialVictims == nil {
		return nil, nil, fwk.NewStatus(fwk.Unschedulable)
	}

	pm := newPrefixPreemptionManager(opts.PreemptionFilter)
	schedFramework.SetPreemptionManager(pm)
	defer schedFramework.SetPreemptionManager(&NoopPreemptionManager{})

	var lastAssignments *fwk.PodGroupAssignments
	pgSchedulingFunc := func(ctx context.Context) (*fwk.PodGroupAssignments, *fwk.Status) {
		assignments, status := baseSchedulingFunc(ctx)
		if status.IsSuccess() {
			lastAssignments = assignments
		}
		return assignments, status
	}

	for v := range opts.PotentialVictims {
		sv, err := newSimPreemptionVictim(v)
		if err != nil {
			return nil, nil, fwk.AsStatus(err)
		}
		pm.addVictim(sv)

		if opts.PreemptionFilter != nil {
			opts.PreemptionFilter.PreemptVictim(v)
			if !opts.PreemptionFilter.MayFit() {
				continue
			}
		}

		_, postFilterStatus := schedFramework.RunPodGroupPostFilterPlugins(ctx, podGroupCycleState, podGroupInfo, pgSchedulingFunc)
		if postFilterStatus.IsError() {
			return nil, nil, postFilterStatus
		}
		if postFilterStatus.IsSuccess() {
			return lastAssignments, pm.preemptedVictims(), nil
		}
	}

	return nil, nil, fwk.NewStatus(fwk.Unschedulable)
}

// simPreemptionVictim adapts a PreemptionVictim to the framework's PreemptionVictim interface.
type simPreemptionVictim struct {
	victim    PreemptionVictim
	pods      []fwk.PodInfo
	reprieved bool
}

var _ upstreampreemption.PreemptionVictim = (*simPreemptionVictim)(nil)

func newSimPreemptionVictim(v PreemptionVictim) (*simPreemptionVictim, error) {
	if v == nil || len(v.Pods()) == 0 {
		return nil, fmt.Errorf("preemption victim cannot be nil nor empty")
	}
	rawPods := v.Pods()
	podInfos := make([]fwk.PodInfo, 0, len(rawPods))
	for _, p := range rawPods {
		if p == nil {
			return nil, fmt.Errorf("preemption victim pod cannot be nil")
		}
		pi, err := framework.NewPodInfo(p)
		if err != nil {
			return nil, fmt.Errorf("failed to create PodInfo for victim pod: %w", err)
		}
		podInfos = append(podInfos, pi)
	}
	return &simPreemptionVictim{
		victim: v,
		pods:   podInfos,
	}, nil
}

func (v *simPreemptionVictim) Pods() []fwk.PodInfo {
	return v.pods
}

// TODO: Support PodDisruptionBudget violation counting during preemption simulation,
// matching kube-scheduler's FilterVictimsWithPDBViolation behavior.
func (v *simPreemptionVictim) NumPDBViolations() int {
	return 0
}

// prefixPreemptionManager provides candidate victims and creates a ReprieveFilter
// for prefix-search and reprieval preemption simulations.
type prefixPreemptionManager struct {
	NoopPreemptionManager
	victims          []*simPreemptionVictim
	preemptionFilter PreemptionFilter
}

var _ upstreampreemption.PreemptionManager = (*prefixPreemptionManager)(nil)

func newPrefixPreemptionManager(filter PreemptionFilter) *prefixPreemptionManager {
	return &prefixPreemptionManager{
		preemptionFilter: filter,
	}
}

func (pm *prefixPreemptionManager) addVictim(sv *simPreemptionVictim) {
	pm.victims = append(pm.victims, sv)
}

func (pm *prefixPreemptionManager) GenerateVictims(
	_ context.Context,
	_ fwk.PodGroupInfo,
) ([]upstreampreemption.PreemptionVictim, *fwk.Status) {
	res := make([]upstreampreemption.PreemptionVictim, len(pm.victims))
	for i, sv := range pm.victims {
		res[i] = sv
	}
	return res, nil
}

func (pm *prefixPreemptionManager) NewReprieveFilter(
	_ context.Context,
	_ []upstreampreemption.PreemptionVictim,
) upstreampreemption.ReprieveFilter {
	return &workloadReprieveFilter{filter: pm.preemptionFilter}
}

func (pm *prefixPreemptionManager) preemptedVictims() []PreemptionVictim {
	var res []PreemptionVictim
	for _, sv := range pm.victims {
		if !sv.reprieved {
			res = append(res, sv.victim)
		}
	}
	return res
}

// workloadReprieveFilter evaluates and tracks victim reprieval using PreemptionFilter.
type workloadReprieveFilter struct {
	filter PreemptionFilter
}

var _ upstreampreemption.ReprieveFilter = (*workloadReprieveFilter)(nil)

func (rf *workloadReprieveFilter) ShouldAttemptReprieval(
	_ context.Context,
	victim upstreampreemption.PreemptionVictim,
) (bool, error) {
	if rf.filter == nil {
		return true, nil
	}
	sv, ok := victim.(*simPreemptionVictim)
	if !ok {
		return false, fmt.Errorf("unexpected PreemptionVictim type %T", victim)
	}
	rf.filter.UnpreemptVictim(sv.victim)
	mayFit := rf.filter.MayFit()
	rf.filter.PreemptVictim(sv.victim)
	return mayFit, nil
}

func (rf *workloadReprieveFilter) OnVictimReprieved(
	_ context.Context,
	victim upstreampreemption.PreemptionVictim,
) error {
	sv, ok := victim.(*simPreemptionVictim)
	if !ok {
		return fmt.Errorf("unexpected PreemptionVictim type %T", victim)
	}
	sv.reprieved = true
	if rf.filter != nil {
		rf.filter.UnpreemptVictim(sv.victim)
	}
	return nil
}

// NoopPreemptionManager is a no-op implementation of PreemptionManager, PreemptionExecutor, and ReprieveFilter
// used as the default simulation neutralizer when workload preemption is not active.
type NoopPreemptionManager struct{}

var _ upstreampreemption.PreemptionManager = &NoopPreemptionManager{}
var _ upstreampreemption.PreemptionExecutor = &NoopPreemptionManager{}
var _ upstreampreemption.ReprieveFilter = &NoopPreemptionManager{}

// GenerateVictims is a no-op implementation of GenerateVictims from PreemptionManager interface.
func (s *NoopPreemptionManager) GenerateVictims(_ context.Context, _ fwk.PodGroupInfo) ([]upstreampreemption.PreemptionVictim, *fwk.Status) {
	return nil, nil
}

// Executor returns PreemptionExecutor from PreemptionManager interface.
func (s *NoopPreemptionManager) Executor() upstreampreemption.PreemptionExecutor {
	return s
}

// NewReprieveFilter is a no-op implementation of NewReprieveFilter from PreemptionManager interface.
func (s *NoopPreemptionManager) NewReprieveFilter(_ context.Context, _ []upstreampreemption.PreemptionVictim) upstreampreemption.ReprieveFilter {
	return s
}

// IsPodRunningPreemption is a no-op implementation of IsPodRunningPreemption from PreemptionExecutor interface.
func (s *NoopPreemptionManager) IsPodRunningPreemption(_ types.UID) bool {
	return false
}

// IsPodGroupRunningPreemption is a no-op implementation of IsPodGroupRunningPreemption from PreemptionExecutor interface.
func (s *NoopPreemptionManager) IsPodGroupRunningPreemption(_ types.UID) bool {
	return false
}

// IsPodGroupWaitingForVictims is a no-op implementation of IsPodGroupWaitingForVictims from PreemptionExecutor interface.
func (s *NoopPreemptionManager) IsPodGroupWaitingForVictims(_ fwk.PodGroupInfo) bool {
	return false
}

// ActuatePodPreemption is a no-op implementation of ActuatePodPreemption from PreemptionExecutor interface.
func (s *NoopPreemptionManager) ActuatePodPreemption(_ context.Context, _ upstreampreemption.PreemptionCandidate, _ *v1.Pod, _ string) *fwk.Status {
	return nil
}

// ActuatePodGroupPreemption is a no-op implementation of ActuatePodGroupPreemption from PreemptionExecutor interface.
func (s *NoopPreemptionManager) ActuatePodGroupPreemption(_ context.Context, _ upstreampreemption.PreemptionCandidate, _ fwk.PodGroupInfo, _ string) *fwk.Status {
	return nil
}

// ShouldAttemptReprieval is a no-op implementation of ShouldAttemptReprieval from ReprieveFilter interface.
func (s *NoopPreemptionManager) ShouldAttemptReprieval(_ context.Context, _ upstreampreemption.PreemptionVictim) (bool, error) {
	return false, nil
}

// OnVictimReprieved is a no-op implementation of OnVictimReprieved from ReprieveFilter interface.
func (s *NoopPreemptionManager) OnVictimReprieved(_ context.Context, _ upstreampreemption.PreemptionVictim) error {
	return nil
}
