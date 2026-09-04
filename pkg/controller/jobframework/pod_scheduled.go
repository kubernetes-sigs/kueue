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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// HasPodScheduledTrue reports whether the pod has PodScheduled=True.
func HasPodScheduledTrue(conds []corev1.PodCondition) bool {
	for i := range conds {
		c := conds[i]
		if c.Type == corev1.PodScheduled {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// AllListedPodsScheduled returns true when at least minCount non-terminating pods are
// listed and all of them have PodScheduled=True.
func AllListedPodsScheduled(pods []corev1.Pod, minCount int) bool {
	active := 0
	for i := range pods {
		if pods[i].DeletionTimestamp != nil {
			continue
		}
		active++
		if !HasPodScheduledTrue(pods[i].Status.Conditions) {
			return false
		}
	}
	return active >= minCount
}

// PodsScheduledByLabels lists pods in namespace matching matchLabels and checks scheduling.
// When matchLabels is empty, returns (false, nil) because scheduling cannot be verified yet.
func PodsScheduledByLabels(ctx context.Context, c client.Client, namespace string, matchLabels map[string]string, minCount int) (bool, error) {
	if len(matchLabels) == 0 {
		return false, nil
	}
	var podList corev1.PodList
	if err := c.List(ctx, &podList, client.InNamespace(namespace), client.MatchingLabels(matchLabels)); err != nil {
		return false, err
	}
	return AllListedPodsScheduled(podList.Items, minCount), nil
}

// PodsScheduledBySelector lists pods in namespace matching selector and checks scheduling.
// When selector is empty, returns (false, nil) because scheduling cannot be verified yet.
func PodsScheduledBySelector(ctx context.Context, c client.Client, namespace, selector string, minCount int) (bool, error) {
	if selector == "" {
		return false, nil
	}
	labelSelector, err := labels.Parse(selector)
	if err != nil {
		return false, fmt.Errorf("parsing pod label selector %q: %w", selector, err)
	}
	var podList corev1.PodList
	if err := c.List(ctx, &podList, client.InNamespace(namespace), client.MatchingLabelsSelector{Selector: labelSelector}); err != nil {
		return false, err
	}
	return AllListedPodsScheduled(podList.Items, minCount), nil
}
