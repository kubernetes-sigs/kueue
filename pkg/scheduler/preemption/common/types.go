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

package common

import (
	"fmt"
	"strconv"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/util/logging"
	"sigs.k8s.io/kueue/pkg/workload"
)

// Target represents a workload selected for preemption.
type Target struct {
	WorkloadInfo *workload.Info
	Reason       string
	WorkloadCq   *schdcache.ClusterQueueSnapshot

	// ConfigurablePreemptionReasonData stores data which resulted in eviction.
	// Specified only when eviction is due to configurable preemption.
	ConfigurablePreemptionReasonData *ConfigurablePreemptionReasonData
}

type ConfigurablePreemptionReasonData struct {
	ConfigName                string
	RuleNameToSelectorIndexes map[string][]int
}

func (d *ConfigurablePreemptionReasonData) EvictionMessage(preemptor *kueue.Workload) string {
	joinRules := func(m map[string][]int) string {
		var builder strings.Builder
		for key, values := range m {
			if builder.Len() > 0 {
				builder.WriteString("; ")
			}
			builder.WriteString(key)
			builder.WriteRune('/')
			for index, value := range values {
				if index > 0 {
					builder.WriteRune(',')
				}
				builder.WriteString(strconv.Itoa(value))
			}
		}
		return builder.String()
	}

	return fmt.Sprintf("Preempted by %s because of preemption config %s rule %s",
		workload.Key(preemptor),
		d.ConfigName,
		joinRules(d.RuleNameToSelectorIndexes))
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
