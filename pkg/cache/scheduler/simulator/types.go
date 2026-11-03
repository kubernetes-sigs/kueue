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

package simulator

import (
	"iter"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-helpers/scheduling/corev1/nodeaffinity"
	"sigs.k8s.io/controller-runtime/pkg/client"

	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

// Candidate represents an abstract scheduleable domain.
type Candidate interface {
	GetID() utiltas.TopologyDomainID
	GetNode() *corev1.Node
}

// Candidate represents an abstract scheduleable domain.
type MatchedCandidate interface {
	Candidate
	GetAffinityScore() int64
	SetAffinityScore(int64)
}

// NodeExclusionStats tracks why nodes were excluded during TAS scheduling.
type NodeExclusionStats struct {
	Taints       map[string]int
	NodeSelector int
	Affinity     int
	TotalNodes   int

	// Temporary non-granular reason for SchedulerLibraryIntegration, equivalent to the total number of not matching nodes.
	// TODO(#13283): Long term the granular information should be taken in a structured form from the `scheduler-library`.
	SchedulerLibraryNoFit int

	// DRANoFit counts nodes that cannot satisfy the Pod's ResourceClaims. The device
	// check is layered on top of the simulator rather than being one of its filters,
	// so folding it into SchedulerLibraryNoFit would misreport it.
	DRANoFit int
}

// PodRequirements stores pod-driven scheduling filters and
// resource inputs that are only needed while filling per-domain counts.
type PodRequirements struct {
	Tolerations              []corev1.Toleration
	Selector                 labels.Selector
	AffinitySelector         *nodeaffinity.NodeSelector
	PreferredSchedulingTerms *nodeaffinity.PreferredSchedulingTerms

	// Used for SchedulerLibraryIntegration to compose the requirements in
	// the form of `corev1.Pod`, which is accepted by the `scheduler-library`.
	PodTemplate *corev1.PodTemplateSpec

	// SimulateEmpty asks what would fit if no Workload were running. TAS uses it
	// to check whether preemption could help, and to reserve capacity for a
	// Workload that is waiting for preemption candidates.
	SimulateEmpty bool
}

// PreemptionFilter tracks external constraints (such as ClusterQueue and Cohort quota)
// alongside node-level simulation during preemption candidate selection and reprieval.
type PreemptionFilter interface {
	// PreemptWorkload notifies the filter that the candidate workload with wlKey
	// has been tentatively preempted during the simulation.
	PreemptWorkload(wlKey client.ObjectKey)
	// UnpreemptWorkload notifies the filter that the candidate workload with wlKey
	// has been reprieved and is no longer preempted in the simulation.
	UnpreemptWorkload(wlKey client.ObjectKey)
	// MayFit reports whether the external constraints are satisfied given the
	// currently preempted workloads.
	MayFit() bool
}

type ScheduleOptions struct {
	// PreemptionCandidates is the ordered sequence of candidate workloads
	// that may be preempted to make room for the scheduled workload.
	PreemptionCandidates iter.Seq[client.ObjectKey]
	// PreemptionFilter evaluates whether preempting candidate workloads
	// satisfies non-node constraints (such as quota).
	PreemptionFilter PreemptionFilter
	// SimulateEmpty configures the simulator to schedule the workload against
	// a cluster snapshot containing only Pods not managed by Kueue.
	SimulateEmpty bool
	// CommittedPreemptionVictims lists the ObjectKeys of workloads that have
	// already been established as preemption targets/victims.
	// The list should never contain duplicate keys.
	CommittedPreemptionVictims []client.ObjectKey
}

type ScheduleOption func(*ScheduleOptions)

// BuildScheduleOptions applies the given options and returns the resulting ScheduleOptions.
func BuildScheduleOptions(options ...ScheduleOption) *ScheduleOptions {
	opts := &ScheduleOptions{}
	for _, opt := range options {
		if opt != nil {
			opt(opts)
		}
	}
	return opts
}

// WithPreemptionCandidates configures the ordered sequence of candidate workloads
// that may be preempted to make room for the scheduled workload.
func WithPreemptionCandidates(candidates iter.Seq[client.ObjectKey]) ScheduleOption {
	return func(o *ScheduleOptions) {
		o.PreemptionCandidates = candidates
	}
}

// WithPreemptionFilter configures the filter used to evaluate whether preempting
// candidate workloads satisfies non-node constraints (such as quota).
func WithPreemptionFilter(filter PreemptionFilter) ScheduleOption {
	return func(o *ScheduleOptions) {
		o.PreemptionFilter = filter
	}
}

// WithSimulateEmpty configures the simulator to schedule the workload against
// a cluster snapshot containing only Pods not managed by Kueue when simulateEmpty
// is true, or against the full cluster snapshot when false.
func WithSimulateEmpty(simulateEmpty bool) ScheduleOption {
	return func(o *ScheduleOptions) {
		o.SimulateEmpty = simulateEmpty
	}
}

// WithCommittedPreemptionVictims configures the simulator to recognize Pods that
// are still running on the cluster but are meant for preemption.
// Takes the ObjectKeys representing Victim Workloads as input.
// All Workloads listed as input must be absent from the ScheduleSimulator's cache.
func WithCommittedPreemptionVictims(targets []client.ObjectKey) ScheduleOption {
	return func(opts *ScheduleOptions) {
		opts.CommittedPreemptionVictims = targets
	}
}

// SchedulingResult encapsulates the node assignments for the pods of the Workload.
type SchedulingResult struct {
	PodPlacements     PodPlacements
	PreemptionVictims []client.ObjectKey
	Error             error
}

type PodPlacements map[client.ObjectKey]*PodPlacement

// PodPlacement represents the assignment of a Pod to a specific node.
// The placement is considered a success only if:
//   - Node is not empty,
//   - Error is not set, and
//   - Reasons list is empty.
//
// If the pleacement is not successful, Node is expected to be empty.
type PodPlacement struct {
	// Node is the assigned Node.
	Node types.NodeName
	// Error lists the error returned for a Pod assignment, if present.
	Error error
	// Reasons explain why the Simulator failed to assign the Pod.
	Reasons []string
}

func (p *PodPlacement) IsSuccess() bool {
	return p.Node != "" && p.Error == nil && len(p.Reasons) == 0
}

type NodeExclusionType int

const (
	ExclusionTaints NodeExclusionType = iota
	ExclusionNodeSelector
	ExclusionAffinity
)

func (s *NodeExclusionStats) RecordExclusion(exclusionType NodeExclusionType, taint *corev1.Taint) {
	switch exclusionType {
	case ExclusionTaints:
		if taint != nil {
			if s.Taints == nil {
				s.Taints = make(map[string]int)
			}
			s.Taints[taint.ToString()]++
		}
	case ExclusionNodeSelector:
		s.NodeSelector++
	case ExclusionAffinity:
		s.Affinity++
	}
}
