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

package preemption

import (
	"context"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	extenderv1 "k8s.io/kube-scheduler/extender/v1"
	fwk "k8s.io/kube-scheduler/framework"
)

// PreemptionManager is an interface that allows customization of the preemption logic.
type PreemptionManager interface {
	// GenerateVictims generates candidate victims for the PodGroup preemption.
	// The preemption algorithm attempts to reprieve victims in reverse order, from last to first.
	// The preemption algorithm will pass through unsuccessful status to the caller.
	GenerateVictims(ctx context.Context, pgInfo fwk.PodGroupInfo) ([]PreemptionVictim, *fwk.Status)
	// Executor returns a [PreemptionExecutor] that can be used to actuate preemption or check preemption status.
	Executor() PreemptionExecutor

	// NewReprieveFilter returns a [ReprieveFilter] that can be used to filter out reprieve candidates.
	// It is called once per preemption evaluation pass after allVictims are removed from the snapshot,
	// and the returned instance is used for the duration of that single reprieve pass and then discarded.
	// Stateful implementations must return a fresh instance on each call.
	// allVictims contains all victims returned by [PreemptionManager.GenerateVictims] to initialize the filter's state.
	// The result should be non-nil, otherwise the preemption algorithm will abort evaluation without actuating preemption
	// and return error status to the caller.
	NewReprieveFilter(ctx context.Context, allVictims []PreemptionVictim) ReprieveFilter
}

// PreemptionVictim represents a preemption unit that abstracts individual Pods and PodGroups,
// ensuring that atomic entities are treated as a single unit during eviction.
type PreemptionVictim interface {
	// Pods returns the list of all Pods that belong to this preemption unit.
	// Evicting this unit implies evicting all Pods in this list.
	Pods() []fwk.PodInfo

	// NumPDBViolations returns the number of PDB violations that evicting this victim would cause.
	// This value is used for metrics and doesn't impact victim selection.
	NumPDBViolations() int
}

// PreemptionExecutor is an interface that provides preemption actuation and tracking operations.
type PreemptionExecutor interface {
	// IsPodRunningPreemption returns true if the pod is currently triggering preemption asynchronously.
	IsPodRunningPreemption(podUID types.UID) bool
	// IsPodGroupRunningPreemption returns true if the pod group is currently triggering preemption asynchronously.
	IsPodGroupRunningPreemption(podGroupUID types.UID) bool
	// IsPodGroupWaitingForVictims returns true if the pod group is currently waiting for victims to be removed.
	// This function is called within snapshot context.
	IsPodGroupWaitingForVictims(pgInfo fwk.PodGroupInfo) bool
	// ActuatePodPreemption actuates preemption for a single pod given the selected candidate.
	ActuatePodPreemption(ctx context.Context, candidate PreemptionCandidate, pod *v1.Pod, pluginName string) *fwk.Status
	// ActuatePodGroupPreemption actuates preemption for a pod group given the selected candidate.
	ActuatePodGroupPreemption(ctx context.Context, candidate PreemptionCandidate, pgInfo fwk.PodGroupInfo, pluginName string) *fwk.Status
}

// PreemptionCandidate represents the final set of victims that should be evicted for the preemptor to fit the node.
type PreemptionCandidate interface {
	// Victims wraps a list of to-be-preempted Pods and the number of PDB violations.
	Victims() *extenderv1.Victims
	// Name returns the target node name (or "cluster" for pod group preemption) where the preemptor gets nominated to run.
	Name() string
	// NumPodGroupDisruptions returns the number of preemption units that affect pod groups.
	// A single preemption unit can be all pods in a pod group (for DisruptionMode=all) or a single pod (for DisruptionMode=single).
	// This value is used for metrics and doesn't impact victim actuation.
	NumPodGroupDisruptions() int
}

// ReprieveFilter controls whether candidate preemption victims can be reprieved during preemption evaluation.
type ReprieveFilter interface {
	// ShouldAttemptReprieval is called before restoring the victim and running the fit check for the preemptor.
	// If an error is returned, the preemption algorithm will abort evaluation without actuating preemption,
	// and pass the error status to the caller.
	ShouldAttemptReprieval(ctx context.Context, victim PreemptionVictim) (bool, error)

	// OnVictimReprieved is called on successful victim reprieval.
	// Stateful implementations can use this function to track the currently active victims.
	// If an error is returned, the preemption algorithm will abort evaluation without actuating preemption,
	// and pass the error status to the caller.
	OnVictimReprieved(ctx context.Context, victim PreemptionVictim) error
}
