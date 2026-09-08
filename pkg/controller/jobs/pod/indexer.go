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

package pod

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"

	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
)

const (
	PodGroupNameCacheKey           = "PodGroupNameCacheKey"
	multiKueuePodGroupNameCacheKey = "MultiKueuePodGroupNameCacheKey"
)

func IndexPodGroupName(o client.Object) []string {
	pod, ok := o.(*corev1.Pod)
	if !ok {
		return nil
	}

	if groupName := utilpod.GetPodGroupName(pod); groupName != "" {
		return []string{groupName}
	}
	return nil
}

// indexMultiKueuePodGroupName indexes both supported group-name representations
// so MultiKueue can finish an in-flight dispatch across feature-gate changes.
func indexMultiKueuePodGroupName(o client.Object) []string {
	pod, ok := o.(*corev1.Pod)
	if !ok {
		return nil
	}

	groupNames := sets.New[string]()
	if groupName := pod.Labels[podconstants.GroupNameLabel]; groupName != "" {
		groupNames.Insert(groupName)
	}
	if groupName := pod.Annotations[podconstants.GroupNameAnnotation]; groupName != "" {
		groupNames.Insert(groupName)
	}
	if groupNames.Len() == 0 {
		return nil
	}
	return groupNames.UnsortedList()
}
