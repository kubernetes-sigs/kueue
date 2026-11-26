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

package plannerfactory

import (
	"fmt"

	"github.com/go-logr/logr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schedcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/scheduler/assignment"
	"sigs.k8s.io/kueue/pkg/scheduler/assignment/native"
	"sigs.k8s.io/kueue/pkg/scheduler/assignment/was"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption"
	"sigs.k8s.io/kueue/pkg/workload"
)

func NewPlanner(
	log logr.Logger,
	wl *workload.Info,
	cq *schedcache.ClusterQueueSnapshot,
	snapshot *schedcache.Snapshot,
	preemptor *preemption.Preemptor,
	assigner *flavorassigner.FlavorAssigner,
) (planner assignment.Planner, err error) {
	switch cq.AssignmentPlanner {
	case kueue.KueueNative:
		if snapshot.SchedulerSimulator != nil {
			planner = native.NewPlanner(wl, snapshot, preemptor, assigner, snapshot.SchedulerSimulator)
		} else {
			planner = native.NewPlanner(wl, snapshot, preemptor, assigner, snapshot.NativeNodeEvaluator)
		}
	case kueue.KubeSchedulerDeepIntegration:
		planner = was.NewPlanner(wl, snapshot, preemptor)
	default:
		err = fmt.Errorf("unknown assignment planner requested: %s", cq.AssignmentPlanner)
	}
	if err == nil {
		log.V(3).Info("Using Planner", "planner", cq.AssignmentPlanner)
	}
	return
}
