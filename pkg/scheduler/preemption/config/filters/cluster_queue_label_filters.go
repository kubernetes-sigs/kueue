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

package filters

import (
	"k8s.io/apimachinery/pkg/labels"

	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
)

type clusterQueueLabelFilter struct {
	selector labels.Selector
}

// newClusterQueueLabelFilter creates a ClusterQueueFilter to evaluate candidate ClusterQueues
// based on whether their labels match the specified label selector.
func newClusterQueueLabelFilter(selector labels.Selector) ClusterQueueFilter {
	return &clusterQueueLabelFilter{
		selector: selector,
	}
}

// Matches evaluates a candidate ClusterQueue's labels against the selector.
func (f *clusterQueueLabelFilter) Matches(cq *schdcache.ClusterQueueSnapshot) bool {
	return f.selector.Matches(labels.Set(cq.Labels))
}
