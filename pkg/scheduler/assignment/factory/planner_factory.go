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

package factory

import (
	"fmt"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schedcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/scheduler/assignment"
	"sigs.k8s.io/kueue/pkg/scheduler/assignment/native"
	"sigs.k8s.io/kueue/pkg/scheduler/assignment/was"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption"
	"sigs.k8s.io/kueue/pkg/workload"
)

func NewPlanner(
	wl *workload.Info,
	snapshot *schedcache.Snapshot,
	preemptor *preemption.Preemptor,
	assigner *flavorassigner.FlavorAssigner,
) (assignment.Planner, error) {
	usePlanner := kueue.KueueNative
	if features.Enabled(features.SchedulerLibraryDeepIntegration) {
		cq := snapshot.ClusterQueue(wl.ClusterQueue)
		if cq == nil {
			return nil, fmt.Errorf("ClusterQueue(%s) not found", wl.ClusterQueue)
		}
		usePlanner = cq.SchedulingSimulationStrategy
	}
	switch usePlanner {
	case kueue.KueueNative:
		return native.NewPlanner(wl, snapshot, preemptor, assigner), nil
	case kueue.KubernetesSchedulerLibrary:
		return was.NewPlanner(wl, snapshot, preemptor), nil
	default:
		return nil, fmt.Errorf("unknown scheduling simulation strategy: %s", usePlanner)
	}
}
