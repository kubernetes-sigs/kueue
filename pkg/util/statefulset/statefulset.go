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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
)

func UngateAndFinalizePod(sts *appsv1.StatefulSet, pod *corev1.Pod, force bool) bool {
	var updated bool

	if force || shouldUngatePod(sts, pod) {
		updated = utilpod.Ungate(pod, podconstants.SchedulingGateName) || utilpod.Ungate(pod, kueue.TopologySchedulingGate)
	}

	// TODO (#8571): As discussed in https://github.com/kubernetes-sigs/kueue/issues/8571,
	// this check should be removed in v0.20.
	if (force || ShouldFinalizePod(sts, pod)) && controllerutil.RemoveFinalizer(pod, podconstants.PodFinalizer) {
		updated = true
	}

	return updated
}

func ShouldFinalizePod(sts *appsv1.StatefulSet, pod *corev1.Pod) bool {
	return shouldUngatePod(sts, pod) || utilpod.IsTerminated(pod) || pod.DeletionTimestamp != nil
}

func shouldUngatePod(sts *appsv1.StatefulSet, pod *corev1.Pod) bool {
	return sts == nil || sts.Status.CurrentRevision != sts.Status.UpdateRevision &&
		sts.Status.CurrentRevision == pod.Labels[appsv1.ControllerRevisionHashLabelKey]
}
