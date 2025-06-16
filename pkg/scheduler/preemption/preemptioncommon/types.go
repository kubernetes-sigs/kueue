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

package preemptioncommon

type PreemptionPossibility int

// Preemption Oracle returns one of three possible
// results of the preemption simulation.
const (
	// Preemption in the given resource flavor is
	// impossible
	None PreemptionPossibility = iota
	// Priority-based preemption may be possible
	// but reclaim is impossible
	PriorityBased
	// Reclaim may be possible
	Reclaim
)
