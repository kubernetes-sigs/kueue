/*
Copyright 2022 Google LLC.

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
	"context"

	"github.com/go-logr/logr"
	ctrl "sigs.k8s.io/controller-runtime"

	"gke-internal.googlesource.com/gke-batch/kueue/pkg/capacity"
	"gke-internal.googlesource.com/gke-batch/kueue/pkg/queue"
	"k8s.io/apimachinery/pkg/util/wait"
)

type Scheduler struct {
	queues        *queue.Manager
	capacityCache *capacity.Cache
}

func New(queues *queue.Manager, cache *capacity.Cache) *Scheduler {
	return &Scheduler{
		queues:        queues,
		capacityCache: cache,
	}
}

func (s *Scheduler) Start(ctx context.Context) {
	ctx = logr.NewContext(ctx, ctrl.Log.WithName("scheduler"))
	wait.UntilWithContext(ctx, s.schedule, 0)
}

func (s *Scheduler) schedule(ctx context.Context) {
	headWorkloads := s.queues.Heads(ctx)
	if len(headWorkloads) == 0 {
		return
	}
	// schedule
}
