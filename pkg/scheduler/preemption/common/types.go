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
	"sigs.k8s.io/controller-runtime/pkg/client"

	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/util/logging"
	"sigs.k8s.io/kueue/pkg/workload"
)

// Target represents a workload selected for preemption.
type Target struct {
	WorkloadInfo *workload.Info
	Reason       string
	WorkloadCq   *schdcache.ClusterQueueSnapshot
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
