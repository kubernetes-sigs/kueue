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

package statefulset

import (
	corev1 "k8s.io/api/core/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
)

// UngatePod removes the Kueue scheduling gates from the Pod without applying a
// Workload admission, which only suits Pods that Kueue stops managing.
func UngatePod(pod *corev1.Pod) bool {
	removedSchedulingGate := utilpod.Ungate(pod, podconstants.SchedulingGateName)
	removedTopologyGate := utilpod.Ungate(pod, kueue.TopologySchedulingGate)
	return removedSchedulingGate || removedTopologyGate
}
