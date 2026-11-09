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
	extenderv1 "k8s.io/kube-scheduler/extender/v1"
)

// candidate represents a nominated node on which the preemptor can be scheduled,
// along with the list of victims that should be evicted for the preemptor to fit the node.
type candidate struct {
	// victims wraps a list of to-be-preempted Pods and the number of PDB violation.
	victims *extenderv1.Victims
	// name returns the target domain(for pod group)/node name where the preemptor gets nominated to run.
	name string
	// numPodGroupDisruptions returns the number of preemption units that affect pod groups.
	// A single preemption unit can be all pods in a pod group (for DisruptionMode=all) or a single pod (for DisruptionMode=single).
	numPodGroupDisruptions int
}

var _ PreemptionCandidate = &candidate{}

// Victims returns s.victims.
func (s *candidate) Victims() *extenderv1.Victims {
	return s.victims
}

// Name returns s.name.
func (s *candidate) Name() string {
	return s.name
}

// NumPodGroupDisruptions returns s.numPodGroupDisruptions.
func (s *candidate) NumPodGroupDisruptions() int {
	return s.numPodGroupDisruptions
}
