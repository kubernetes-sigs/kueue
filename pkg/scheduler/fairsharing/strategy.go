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

package fairsharing

type Strategy func(preemptorNewShare, preempteeOldShare, preempteeNewShare int) bool

// LessThanOrEqualToFinalShare implements Rule S2-a in https://sigs.k8s.io/kueue/keps/1714-fair-sharing#choosing-workloads-from-clusterqueues-for-preemption
func LessThanOrEqualToFinalShare(preemptorNewShare, _, preempteeNewShare int) bool {
	return preemptorNewShare <= preempteeNewShare
}

// LessThanInitialShare implements rule S2-b in https://sigs.k8s.io/kueue/keps/1714-fair-sharing#choosing-workloads-from-clusterqueues-for-preemption
func LessThanInitialShare(preemptorNewShare, preempteeOldShare, _ int) bool {
	return preemptorNewShare < preempteeOldShare
}
