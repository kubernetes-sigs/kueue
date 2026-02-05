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
	"sync"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	resourcehelpers "k8s.io/component-helpers/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/pkg/resources"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
)

// nonTasUsageCache caches pod usage, to avoid
// the hot path documented in kueue#8449.
type nonTasUsageCache struct {
	podUsage map[types.NamespacedName]podUsageValue
	lock     sync.RWMutex
}

type podUsageValue struct {
	node  string
	usage resources.Requests
}

// update adds a pod to the cache or removes it if terminated.
// Returns true and the node name only on first-time removal of a terminated pod.
func (n *nonTasUsageCache) update(pod *corev1.Pod, log logr.Logger) (removed bool, nodeName string) {
	n.lock.Lock()
	defer n.lock.Unlock()

	key := client.ObjectKeyFromObject(pod)
	if utilpod.IsTerminated(pod) {
		if usage, exists := n.podUsage[key]; exists {
			log.V(5).Info("Deleting terminated pod from the cache")
			delete(n.podUsage, key)
			return true, usage.node
		}
		return false, ""
	}

	log.V(5).Info("Adding non-TAS pod to the cache")
	requests := resources.NewRequests(
		resourcehelpers.PodRequests(pod, resourcehelpers.PodResourcesOptions{}))
	n.podUsage[key] = podUsageValue{
		node:  pod.Spec.NodeName,
		usage: requests,
	}
	return false, ""
}

// delete removes a pod from the cache and returns the node it was on (if any).
func (n *nonTasUsageCache) delete(key client.ObjectKey) string {
	n.lock.Lock()
	defer n.lock.Unlock()
	if usage, ok := n.podUsage[key]; ok {
		delete(n.podUsage, key)
		return usage.node
	}
	return ""
}

func (n *nonTasUsageCache) usagePerNode() map[string]resources.Requests {
	n.lock.RLock()
	defer n.lock.RUnlock()
	usage := make(map[string]resources.Requests)
	for _, podUsage := range n.podUsage {
		if _, found := usage[podUsage.node]; !found {
			usage[podUsage.node] = resources.Requests{}
		}
		usage[podUsage.node].Add(podUsage.usage)
		usage[podUsage.node].Add(resources.Requests{corev1.ResourcePods: 1})
	}
	return usage
}
