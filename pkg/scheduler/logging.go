/*
Copyright 2023 The Kubernetes Authors.

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
	"k8s.io/klog/v2"

	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption"
	"sigs.k8s.io/kueue/pkg/util/slices"
)

func logAdmissionAttemptIfVerbose(log logr.Logger, e *entry) {
	logV := log.V(3)
	if !logV.Enabled() {
		return
	}
	args := []any{
		"workload", klog.KObj(e.Obj),
		"clusterQueue", klog.KRef("", e.ClusterQueue),
		"status", e.status,
		"reason", e.inadmissibleMsg,
	}
	if log.V(4).Enabled() {
		args = append(args, "nominatedAssignment", e.assignment)
		args = append(args, "preempted", getWorkloadReferences(e.preemptionTargets))
	}
	logV.Info("Workload evaluated for admission", args...)
}

func logSnapshotIfVerbose(log logr.Logger, s *cache.Snapshot) {
	if logV := log.V(6); logV.Enabled() {
		s.Log(logV)
	}
}

func getWorkloadReferences(targets []*preemption.Target) []klog.ObjectRef {
	return slices.Map(targets, func(t **preemption.Target) klog.ObjectRef { return klog.KObj((*t).WorkloadInfo.Obj) })
}
