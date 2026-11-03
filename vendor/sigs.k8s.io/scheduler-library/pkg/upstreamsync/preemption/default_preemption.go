// Copyright The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package preemption

import (
	"context"
	"fmt"

	fwk "k8s.io/kube-scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/defaultpreemption"
	"k8s.io/kubernetes/pkg/scheduler/metrics"
)

type podGroupEvaluator interface {
	Preempt(ctx context.Context, pgInfo fwk.PodGroupInfo, podGroupSchedulingFunc fwk.PodGroupSchedulingFunc) (*fwk.PodGroupPostFilterResult, *fwk.Status)
}

var _ fwk.PodGroupPostFilterPlugin = &DefaultPreemption{}

// DefaultPreemption wraps defaultpreemption.DefaultPreemption to override PodGroupPostFilter on 1.37.
//
// Migration Plan (1.38): Delete this file and use upstream defaultpreemption.DefaultPreemption directly.
type DefaultPreemption struct {
	*defaultpreemption.DefaultPreemption
	podGroupEvaluator podGroupEvaluator
	fh                Handle
}

// NewDefaultPreemption constructs a new DefaultPreemption.
func NewDefaultPreemption(dp *defaultpreemption.DefaultPreemption, fh Handle) *DefaultPreemption {
	return &DefaultPreemption{
		DefaultPreemption: dp,
		fh:                fh,
		podGroupEvaluator: NewPodGroupEvaluator(fh),
	}
}

// PodGroupPostFilter runs a default preemption for the pod group.
func (pl *DefaultPreemption) PodGroupPostFilter(ctx context.Context, state fwk.PodGroupCycleState, pgInfo fwk.PodGroupInfo, pgSchedulingFunc fwk.PodGroupSchedulingFunc) (postFilterResult *fwk.PodGroupPostFilterResult, status *fwk.Status) {
	defer func() {
		metrics.WorkloadPreemptionAttempts.WithLabelValues(status.Code().String()).Inc()
	}()

	mutableLister := pl.fh.MutableSnapshotSharedLister()
	err := mutableLister.StartMutations()
	if err != nil {
		return nil, fwk.AsStatus(fmt.Errorf("pod group preemption: failed to start mutations: %w", err))
	}
	defer func() {
		if err := mutableLister.EndMutations(); err != nil {
			status = fwk.AsStatus(fmt.Errorf("pod group preemption: failed to end mutations: %w", err))
		}
	}()

	res, status := pl.podGroupEvaluator.Preempt(ctx, pgInfo, pgSchedulingFunc)
	msg := status.Message()
	if len(msg) > 0 {
		return res, fwk.NewStatus(status.Code(), "pod group preemption: "+msg)
	}
	return res, status
}
