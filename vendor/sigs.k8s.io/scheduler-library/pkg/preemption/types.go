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
	"iter"

	v1 "k8s.io/api/core/v1"
)

// PreemptionVictim is an atomic unit of preemption, which can contain 1 or more pods.
type PreemptionVictim interface {
	// Pods can contain 1 or more pods.
	// The returned pods must not overlap across victims and must be present in the
	// snapshot with Spec.NodeName set.
	Pods() []*v1.Pod
}

// PreemptionFilter provides a fast pre-check to skip expensive scheduling evaluations
// (including the initial scheduling attempt) when the current set of preempted victims
// is known to be insufficient.
type PreemptionFilter interface {
	// UnpreemptVictim is called whenever a victim has been removed from the current victim set.
	UnpreemptVictim(PreemptionVictim)

	// PreemptVictim is called whenever a victim has been added to the current victim set.
	PreemptVictim(PreemptionVictim)

	// MayFit reports whether the preemptor could potentially fit given
	// the current victim set. It is called before the initial scheduling
	// attempt (before any victims are preempted) as well as during prefix
	// search and reprieval.
	//
	// If it returns true, the scheduler runs a scheduling evaluation for the preemptor.
	// If it returns false, the current victim set is treated as insufficient and
	// the scheduling evaluation (including the initial scheduling attempt) is skipped.
	//
	// Implementations must rely only on internal state updated via
	// [PreemptionFilter.PreemptVictim] and [PreemptionFilter.UnpreemptVictim],
	// and must not read from the cluster snapshot directly.
	//
	// The result must be monotonic with respect to the victim set:
	//   - [PreemptionFilter.PreemptVictim] must never change the result from true to false.
	//   - [PreemptionFilter.UnpreemptVictim] must never change the result from false to true.
	MayFit() bool
}

// WorkloadPreemptionOptions configures victim selection and filtering for workload preemption.
//
// When a workload cannot be scheduled directly, preemption runs in two stages:
//  1. Prefix search: victims from [WorkloadPreemptionOptions.PotentialVictims] are preempted
//     in order (least to most valuable) until the workload fits.
//  2. Reprieval: victims in that prefix are tested in reverse order (most to least valuable)
//     and restored to the cluster if the workload still fits without preempting them.
type WorkloadPreemptionOptions struct {
	// PotentialVictims yields candidate victims that may be preempted to make room for the
	// workload, ordered from least to most valuable.
	// It is consumed during stage 1 to find the smallest prefix of victims needed for the
	// workload to fit, after which stage 2 attempts to reprieve victims from that prefix.
	// If nil or empty, no additional victims are considered during stage 1.
	PotentialVictims iter.Seq[PreemptionVictim]

	// CommittedVictims are victims that are preempted after the initial scheduling attempt
	// fails (or is skipped by [PreemptionFilter.MayFit]), but before stage 1 begins.
	// Unlike PotentialVictims, they are never candidates for reprieval in stage 2.
	// Each committed victim is passed to [PreemptionFilter.PreemptVictim] before stage 1.
	// If nil or empty, no victims are preempted upfront.
	CommittedVictims []PreemptionVictim

	// PreemptionFilter is an optional fast pre-check used before the initial scheduling
	// attempt and during both prefix search and reprieval to skip scheduling evaluations
	// when the current victim set is known to be insufficient.
	// If nil, no pre-check filter is applied.
	PreemptionFilter PreemptionFilter
}
