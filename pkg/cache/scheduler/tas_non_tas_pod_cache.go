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

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/resources"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
)

// podMetricCoord holds the pre-computed metric coordinates for one flavor that
// matched a non-TAS pod's node at add time. Storing these avoids a nodesCache
// lookup at subtract time, so the subtract is correct even if the node has since
// left nodesCache (e.g. marked unschedulable by a cluster autoscaler after TAS
// workload pods departed).
type podMetricCoord struct {
	flavorName kueue.ResourceFlavorReference
	levels     []string
	values     []string
}

// nonTasUsageCache caches pod usage, to avoid
// the hot path documented in kueue#8449.
type nonTasUsageCache struct {
	podUsage  map[types.NamespacedName]podUsageValue
	nodeUsage map[string]resources.Requests // pre-aggregated per-node totals
	lock      sync.RWMutex
}

type podUsageValue struct {
	node string
	// coords holds pre-computed metric coordinates per matching flavor, set when
	// the pod is first added. nil means the node was absent from nodesCache at add
	// time; in that case no metric is emitted for add or subtract.
	coords []podMetricCoord
	usage  resources.Requests
}

// update may add a pod to the cache, or delete a terminated pod.
// coords should be the pre-computed metric coordinates for the pod's node (see
// podMetricCoord). Returns the removed entry (if any) and the added entry (if any)
// so the caller can emit metric deltas without re-reading the cache.
func (n *nonTasUsageCache) update(pod *corev1.Pod, coords []podMetricCoord, log logr.Logger) (removed, added *podUsageValue) {
	n.lock.Lock()
	defer n.lock.Unlock()

	key := client.ObjectKeyFromObject(pod)

	if utilpod.IsTerminated(pod) {
		log.V(5).Info("Deleting terminated pod from the cache")
		if old, found := n.podUsage[key]; found {
			delete(n.podUsage, key)
			return &old, nil
		}
		return nil, nil
	}

	log.V(5).Info("Adding non-TAS pod to the cache")
	old, existed := n.podUsage[key]

	c := coords
	if existed && len(old.coords) > 0 {
		// Preserve coords from when the pod was first cached; topology coordinates
		// are stable for the lifetime of a scheduled pod.
		c = old.coords
	}

	newEntry := podUsageValue{
		node:   pod.Spec.NodeName,
		coords: c,
		usage:  resources.NewRequestsFromPodSpec(&pod.Spec),
	}
	n.podUsage[key] = newEntry
	if existed {
		return &old, &newEntry
	}
	return nil, &newEntry
}

// delete removes a pod from the cache and returns its entry, or nil if absent.
func (n *nonTasUsageCache) delete(key client.ObjectKey) *podUsageValue {
	n.lock.Lock()
	defer n.lock.Unlock()
	if old, found := n.podUsage[key]; found {
		delete(n.podUsage, key)
		return &old
	}
	return nil
}

func (n *nonTasUsageCache) usagePerNode() map[string]resources.Requests {
	n.lock.RLock()
	defer n.lock.RUnlock()
	usage := make(map[string]resources.Requests, len(n.nodeUsage))
	for node, reqs := range n.nodeUsage {
		usage[node] = reqs.Clone()
	}
	return usage
}

// addNodeUsage increments the pre-aggregated per-node usage.
// Must be called under write lock.
func (n *nonTasUsageCache) addNodeUsage(node string, usage resources.Requests) {
	if _, found := n.nodeUsage[node]; !found {
		n.nodeUsage[node] = resources.Requests{}
	}
	n.nodeUsage[node].Add(usage)
	n.nodeUsage[node][corev1.ResourcePods]++
}

// removeNodeUsage decrements the pre-aggregated per-node usage.
// Must be called under write lock.
func (n *nonTasUsageCache) removeNodeUsage(node string, usage resources.Requests, log logr.Logger) {
	existing, found := n.nodeUsage[node]
	if !found {
		return
	}
	existing.Sub(usage)
	existing[corev1.ResourcePods]--
	if pods := existing[corev1.ResourcePods]; pods <= 0 {
		if pods < 0 {
			log.V(0).Info("Unexpected negative pod count in nodeUsage", "node", node, "podCount", pods)
		}
		delete(n.nodeUsage, node)
	}
}
