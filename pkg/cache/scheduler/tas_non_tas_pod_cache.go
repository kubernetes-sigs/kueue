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

package scheduler

import (
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	resourcehelpers "k8s.io/component-helpers/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/pkg/resources"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

// nonTasUsageCache caches pod usage, to avoid
// the hot path documented in kueue#8449.
type nonTasUsageCache struct {
	podUsage map[types.NamespacedName]podUsageValue
}

type podUsageValue struct {
	node  string
	usage resources.Requests
}

// addOrUpdate assumes that tas cache's write lock is held.
func (n *nonTasUsageCache) addOrUpdate(pod corev1.Pod, log logr.Logger) {
	// skip unscheduled pods as they don't use any capacity.
	if len(pod.Spec.NodeName) == 0 {
		if n.cached(pod) {
			log.V(5).Info("Deleting unscheduled pod", "pod", client.ObjectKeyFromObject(&pod))
			n.delete(client.ObjectKeyFromObject(&pod))
		}
		return
	}
	// delete terminated pods as they no longer use any capacity.
	if utilpod.IsTerminated(&pod) {
		if n.cached(pod) {
			log.V(5).Info("Deleting terminated", "pod", client.ObjectKeyFromObject(&pod))
			n.delete(client.ObjectKeyFromObject(&pod))
		}
		return
	}
	// skip pods accounted for by TAS.
	if utiltas.IsTAS(&pod) {
		if n.cached(pod) {
			log.V(5).Info("Deleting pod accounted for by TAS", "pod", client.ObjectKeyFromObject(&pod))
			n.delete(client.ObjectKeyFromObject(&pod))
		}
		return
	}

	requests := resources.NewRequests(
		resourcehelpers.PodRequests(&pod, resourcehelpers.PodResourcesOptions{}))
	n.podUsage[client.ObjectKeyFromObject(&pod)] = podUsageValue{
		node:  pod.Spec.NodeName,
		usage: requests,
	}
}

// delete assumes that tas cache's write lock is held.
func (n *nonTasUsageCache) delete(key client.ObjectKey) {
	delete(n.podUsage, key)
}

func (n *nonTasUsageCache) cached(pod corev1.Pod) bool {
	_, found := n.podUsage[client.ObjectKeyFromObject(&pod)]
	return found
}

// nodeUsage assumes that tas cache's read lock is held.
func (n *nonTasUsageCache) nodeUsage() map[string]resources.Requests {
	nodeUsage := make(map[string]resources.Requests)
	for _, podUsage := range n.podUsage {
		if _, found := nodeUsage[podUsage.node]; !found {
			nodeUsage[podUsage.node] = resources.Requests{}
		}
		nodeUsage[podUsage.node].Add(podUsage.usage)
		nodeUsage[podUsage.node].Add(resources.Requests{corev1.ResourcePods: 1})
	}
	return nodeUsage
}
