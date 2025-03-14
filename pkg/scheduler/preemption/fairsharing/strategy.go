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

// PreemptorNewShare is the DominantResourceShare of the Preemptor
// after the incoming workload's usage has been added. It is used for
// both rules S2-a and S2-b
type PreemptorNewShare int

// TargetNewShare is the DominantResourceShare of the Preemptee after
// its preempted workload's usage has been removed. It is used for
// rule S2-a.
type TargetNewShare int

// TargetOldShare is the DominantResourceShare of the Preemptee before
// its workload has been removed. It is used for rule S2-b.
type TargetOldShare int

type Strategy func(PreemptorNewShare, TargetOldShare, TargetNewShare) bool

// LessThanOrEqualToFinalShare implements Rule S2-a in https://sigs.k8s.io/kueue/keps/1714-fair-sharing#choosing-workloads-from-clusterqueues-for-preemption
func LessThanOrEqualToFinalShare(preemptorNewShare PreemptorNewShare, _ TargetOldShare, targetNewShare TargetNewShare) bool {
	return int(preemptorNewShare) <= int(targetNewShare)
}

// LessThanInitialShare implements rule S2-b in https://sigs.k8s.io/kueue/keps/1714-fair-sharing#choosing-workloads-from-clusterqueues-for-preemption
func LessThanInitialShare(preemptorNewShare PreemptorNewShare, targetOldShare TargetOldShare, _ TargetNewShare) bool {
	return int(preemptorNewShare) < int(targetOldShare)
}
