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
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
)

// podSetPlacementSummary represents the spread of Pods on Nodes grouped by PodSet.
type podSetPlacementSummary map[kueue.PodSetReference]podsPerNode

// podsPerNode represents the count of Pods assigned to each recorded Node.
type podsPerNode map[types.NodeName]int32

// podSetFailureSummary maps PodSets to a list of errors and failures
// reported by Pods when trying to schedule.
type podSetFailureSummary map[kueue.PodSetReference]*failureSummary

type failureSummary struct {
	errs    []error
	reasons []string
	// unclassified is true when at least one Pod of the PodSet failed for a
	// reason other than being rejected by the scheduler, such as a plugin error
	// or being skipped by the scheduler-library.
	unclassified bool
}

func (s podSetPlacementSummary) countPodPlacement(psRef kueue.PodSetReference, node types.NodeName) {
	if s[psRef] == nil {
		s[psRef] = make(podsPerNode)
	}
	s[psRef][node]++
}

func (s podSetFailureSummary) add(psRef kueue.PodSetReference, pod client.ObjectKey, err error, reasons []string, rejected bool) {
	if _, ok := s[psRef]; !ok {
		s[psRef] = &failureSummary{}
	}
	s[psRef].record(pod, err, reasons, rejected)
}

// noFitReason classifies the failures of a scheduling attempt.
// It returns TopologyPlacementFailed only when every failed Pod was rejected by
// the scheduler, which means the workload cannot be placed under its topology
// and node constraints. Otherwise it falls back to SchedulingSimulationFailed.
// The classification applies to the whole workload, because a failure of one
// Pod makes the scheduler reject the other Pods of the same pod group hierarchy,
// including the Pods of other PodSets.
func (s podSetFailureSummary) noFitReason() string {
	if s.allRejected() {
		return kueue.WorkloadQuotaReservedReasonTopologyPlacementFailed
	}
	return kueue.WorkloadQuotaReservedReasonSimulationFailed
}

// allRejected reports whether every failed Pod was rejected by the scheduler.
func (s podSetFailureSummary) allRejected() bool {
	for _, fs := range s {
		if fs.unclassified {
			return false
		}
	}
	return true
}
func (fs *failureSummary) record(pod client.ObjectKey, err error, reasons []string, rejected bool) {
	if !rejected {
		fs.unclassified = true
	}
	if err != nil {
		fs.errs = append(fs.errs, fmt.Errorf("pod: %s, error: %w", pod, err))
	}
	for _, reason := range reasons {
		fs.reasons = append(fs.reasons, fmt.Sprintf("pod: %s, failure reason: %s", pod, reason))
	}
}

func (fs *failureSummary) asStatus() *flavorassigner.Status {
	return flavorassigner.NewStatus(fs.reasons...).WithError(errors.Join(fs.errs...))
}
