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
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/workload"
)

// ClusterQueueFilter evaluates whether a ClusterQueue is eligible to yield preemption candidates.
// ClusterQueue filtering evaluates entire ClusterQueues to prune ineligible queues,
// making unnecessary iteration over all individual workloads within those queues avoidable.
type ClusterQueueFilter interface {
	Matches(cq *schdcache.ClusterQueueSnapshot) bool
}

// WorkloadFilter evaluates whether a specific candidate workload is eligible for preemption.
type WorkloadFilter interface {
	Matches(wl *workload.Info) bool
}

// CandidateFilters contains the complete filter set compiled for a candidate selector.
type CandidateFilters struct {
	CQFilters []ClusterQueueFilter
	WLFilters []WorkloadFilter
}
