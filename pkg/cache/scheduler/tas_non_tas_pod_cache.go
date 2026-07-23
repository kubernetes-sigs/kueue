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
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/pkg/resources"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
)

// nonTasUsageCache caches pod usage, to avoid
// the hot path documented in kueue#8449.
type nonTasUsageCache struct {
	podUsage  map[types.NamespacedName]podUsageValue
	nodeUsage map[string]resources.Requests // pre-aggregated per-node totals
	lock      sync.RWMutex
}

type podUsageValue struct {
	node  string
	usage resources.Requests
}

// update may add a pod to the cache, or
// delete a terminated pod.
func (n *nonTasUsageCache) update(pod *corev1.Pod, log logr.Logger) {
	n.lock.Lock()
	defer n.lock.Unlock()

	key := client.ObjectKeyFromObject(pod)

	// delete terminated pods as they no longer use any capacity.
	if utilpod.IsTerminated(pod) {
		log.V(5).Info("Deleting terminated pod from the cache")
		if old, found := n.podUsage[key]; found {
			n.removeNodeUsage(old.node, old.usage, log)
		}
		delete(n.podUsage, key)
		return
	}

	// Remove old entry if pod already exists (handles node migration, resource resize).
	if old, found := n.podUsage[key]; found {
		n.removeNodeUsage(old.node, old.usage, log)
	}

	log.V(5).Info("Adding non-TAS pod to the cache")
	requests := resources.NewMapRequestsFromPodSpec(&pod.Spec)
	n.podUsage[key] = podUsageValue{
		node:  pod.Spec.NodeName,
		usage: requests,
	}
	n.addNodeUsage(pod.Spec.NodeName, requests)
}

func (n *nonTasUsageCache) delete(key client.ObjectKey, log logr.Logger) {
	n.lock.Lock()
	defer n.lock.Unlock()
	if old, found := n.podUsage[key]; found {
		n.removeNodeUsage(old.node, old.usage, log)
	}
	delete(n.podUsage, key)
}

// forEachNodeUsage invokes fn for each node's usage while holding the read lock.
// usage is the live cache entry, not a copy: fn must only read it, and must not
// mutate or retain it beyond the call. Clone it if a longer-lived copy is needed.
func (n *nonTasUsageCache) forEachNodeUsage(fn func(node string, usage resources.Requests)) {
	n.lock.RLock()
	defer n.lock.RUnlock()
	for node, reqs := range n.nodeUsage {
		fn(node, reqs)
	}
}

// addNodeUsage increments the pre-aggregated per-node usage.
// Must be called under write lock.
func (n *nonTasUsageCache) addNodeUsage(node string, usage resources.Requests) {
	if _, found := n.nodeUsage[node]; !found {
		n.nodeUsage[node] = resources.MapRequests{}
	}
	n.nodeUsage[node].Add(usage)
	n.nodeUsage[node].Add(resources.OnePodRequest)
}

// removeNodeUsage decrements the pre-aggregated per-node usage.
// Must be called under write lock.
func (n *nonTasUsageCache) removeNodeUsage(node string, usage resources.Requests, log logr.Logger) {
	existing, found := n.nodeUsage[node]
	if !found {
		return
	}
	existing.Sub(usage)
	existing.Sub(resources.OnePodRequest)
	if pods := existing.GetValue(corev1.ResourcePods); pods <= 0 {
		if pods < 0 {
			log.V(0).Info("Unexpected negative pod count in nodeUsage", "node", node, "podCount", pods)
		}
		delete(n.nodeUsage, node)
	}
}
