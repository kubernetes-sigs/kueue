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

package preemption

import (
	"github.com/go-logr/logr"
	"k8s.io/klog/v2"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/fairsharing"
	"sigs.k8s.io/kueue/pkg/workload"
)

// fsEvaluationLogEntry is the result of evaluating the first
// FairSharing strategy against a single candidate workload.
type fsEvaluationLogEntry struct {
	TargetWorkload string `json:"targetWorkload"`
	TargetNewShare string `json:"targetNewShare"`
	StrategyPassed bool   `json:"strategyPassed"`
}

// fsStrategyLog accumulates the first FairSharing strategy's
// evaluations for a single candidate ClusterQueue, so that they can be
// emitted as one log entry holding an array of results.
//
// The preemptor's and the target's shares, and the target ClusterQueue
// itself, are constant for the whole iteration, so they are logged
// once. Only the candidate workload and its resulting share vary. This
// keeps the volume of this log proportional to the number of candidate
// ClusterQueues rather than to the number of candidate workloads
// evaluated.
//
// Nothing is accumulated when the verbosity level is disabled.
type fsStrategyLog struct {
	logV              logr.Logger
	enabled           bool
	targetCq          kueue.ClusterQueueReference
	preemptorNewShare fairsharing.PreemptorNewShare
	targetOldShare    fairsharing.TargetOldShare
	entries           []fsEvaluationLogEntry
}

func newFsStrategyLog(log logr.Logger, candCQ *fairsharing.TargetClusterQueue, preemptorNewShare fairsharing.PreemptorNewShare, targetOldShare fairsharing.TargetOldShare) fsStrategyLog {
	logV := log.V(4)
	return fsStrategyLog{
		logV:              logV,
		enabled:           logV.Enabled(),
		targetCq:          candCQ.GetTargetCq().Name,
		preemptorNewShare: preemptorNewShare,
		targetOldShare:    targetOldShare,
	}
}

func (l *fsStrategyLog) record(candWl *workload.Info, targetNewShare fairsharing.TargetNewShare, passed bool) {
	if !l.enabled {
		return
	}
	l.entries = append(l.entries, fsEvaluationLogEntry{
		TargetWorkload: string(workload.Key(candWl.Obj)),
		TargetNewShare: schdcache.DRS(targetNewShare).PreciseWeightedShareSerialized(),
		StrategyPassed: passed,
	})
}

// flush emits the accumulated evaluations as a single log entry. It is
// a no-op when the verbosity level is disabled or when nothing was
// recorded, and it is safe to call more than once.
func (l *fsStrategyLog) flush() {
	if !l.enabled || len(l.entries) == 0 {
		return
	}
	l.logV.Info("Evaluating FairSharing strategy",
		"preemptorNewShare", schdcache.DRS(l.preemptorNewShare).PreciseWeightedShareSerialized(),
		"targetClusterQueue", klog.KRef("", string(l.targetCq)),
		"targetOldShare", schdcache.DRS(l.targetOldShare).PreciseWeightedShareSerialized(),
		"strategyEvaluations", l.entries)
	l.entries = nil
}
