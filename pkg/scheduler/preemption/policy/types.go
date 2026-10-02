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

package policy

import (
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/util/logging"
	stringsutils "sigs.k8s.io/kueue/pkg/util/strings"
	"sigs.k8s.io/kueue/pkg/workload"
)

// Target represents a workload selected for preemption.
type Target struct {
	WorkloadInfo *workload.Info
	Reason       string
	WorkloadCq   *scheduler.ClusterQueueSnapshot

	// ConfigurablePreemptionReasonData stores data which resulted in eviction.
	// Specified only when eviction is due to configurable preemption.
	ConfigurablePreemptionReasonData *ConfigurablePreemptionReasonData
}

type ConfigurablePreemptionReasonData struct {
	ConfigName PreemptionConfigReference
	// RuleNameToSelectorIndexes maps rule names to the indexes of selectors
	// that the workload satisfies.
	RuleNameToSelectorIndexes map[PreemptionConfigRuleReference][]int
}

// PreemptionConfigReference is a dedicated type to reference PreemptionConfig
type PreemptionConfigReference string

// PreemptionConfigRuleReference is a dedicated type to reference PreemptionConfigPreemptionRule
type PreemptionConfigRuleReference string

func (d *ConfigurablePreemptionReasonData) EvictionMessage(preemptor *kueue.Workload) string {
	return fmt.Sprintf("Preempted by %s because of preemption config %s rule %s",
		workload.Key(preemptor),
		d.ConfigName,
		stringsutils.JoinMap(d.RuleNameToSelectorIndexes, "/", ",", "; "))
}

type yieldCandidate = func(*Target) bool

// YieldFromSnapshot wraps a candidate (Target) yielder with
// logic removing the candidate from the provided snapshot.
func YieldFromSnapshot(snapshot *scheduler.Snapshot, yield yieldCandidate) yieldCandidate {
	return func(t *Target) bool {
		snapshot.RemoveWorkload(t.WorkloadInfo)
		return yield(t)
	}
}

// ensures that Target implements ObjectRefProvider interface at compile time
var _ logging.ObjectRefProvider = (*Target)(nil)

// GetObject implements the ObjectRefProvider interface.
func (t *Target) GetObject() client.Object {
	return t.WorkloadInfo.Obj
}

// PreemptionPossibility represents the result
// of a preemption simulation.
type PreemptionPossibility int

const (
	// NoCandidates were found.
	NoCandidates PreemptionPossibility = iota
	// Preemption targets were found.
	Preempt
	// Preemption targets were found, and
	// all of them are outside of preempting
	// ClusterQueue.
	Reclaim
)

func (p PreemptionPossibility) String() string {
	switch p {
	case NoCandidates:
		return "NoCandidates"
	case Preempt:
		return "Preempt"
	case Reclaim:
		return "Reclaim"
	}
	return "Unknown"
}
