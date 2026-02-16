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

package jobframework

import (
	"context"

	corev1 "k8s.io/api/core/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/util/orderedgroups"
)

// JobPodSets retrieves the pod sets from a GenericJob and applies environment variable
// deduplication.
func JobPodSets(ctx context.Context, job GenericJob) ([]kueue.PodSet, error) {
	podSets, err := job.PodSets(ctx)
	if err != nil {
		return nil, err
	}
	SanitizePodSets(podSets)
	return podSets, nil
}

// SanitizePodSets sanitizes all PodSets in the given slice by removing duplicate
// environment variables from each container. This function modifies the podSets slice in place.
func SanitizePodSets(podSets []kueue.PodSet) {
	for podSetIndex := range podSets {
		SanitizePodSet(&podSets[podSetIndex])
	}
}

// SanitizePodSet sanitizes a single PodSet by removing duplicate environment
// variables from all containers and initContainers in its pod template.
func SanitizePodSet(podSet *kueue.PodSet) {
	for containerIndex := range podSet.Template.Spec.Containers {
		sanitizeContainer(&podSet.Template.Spec.Containers[containerIndex])
	}

	for containerIndex := range podSet.Template.Spec.InitContainers {
		sanitizeContainer(&podSet.Template.Spec.InitContainers[containerIndex])
	}
}

// sanitizeContainer removes duplicate environment variables from the given container.
func sanitizeContainer(container *corev1.Container) {
	envVarGroups := orderedgroups.NewOrderedGroups[string, corev1.EnvVar]()
	for _, envVar := range container.Env {
		envVarGroups.Insert(envVar.Name, envVar)
	}
	container.Env = make([]corev1.EnvVar, 0, len(container.Env))
	for _, envVars := range envVarGroups.InOrder {
		container.Env = append(container.Env, envVars[len(envVars)-1])
	}
}
