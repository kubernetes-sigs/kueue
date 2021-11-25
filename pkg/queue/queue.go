/*
Copyright 2021 Google LLC.

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

package queue

import (
	"fmt"
	"sync"

	kueue "gke-internal.googlesource.com/gke-batch/kueue/api/v1alpha1"
)

type Manager struct {
	sync.Mutex

	queues map[string]*Queue
}

func NewManager() *Manager {
	return &Manager{
		queues: make(map[string]*Queue),
	}
}

// Queue is the internal implementation of kueue.Queue.
type Queue struct {
	Priority   int64
	Capacities []string
	// TODO: workloads
}

func (s *Manager) AddQueue(q *kueue.Queue) error {
	s.Lock()
	defer s.Unlock()

	if _, ok := s.queues[q.Name]; ok {
		return fmt.Errorf("queue %q already exists", q.Name)
	}
	s.queues[q.Name] = &Queue{
		Priority:   q.Spec.Priority,
		Capacities: derefCapacities(q.Spec.Capacities),
	}
	return nil
}

func (s *Manager) UpdateQueue(q *kueue.Queue) error {
	s.Lock()
	defer s.Unlock()
	qImpl, ok := s.queues[q.Name]
	if !ok {
		return fmt.Errorf("queue %q doesn't exist", q.Name)
	}
	qImpl.Priority = q.Spec.Priority
	qImpl.Capacities = derefCapacities(q.Spec.Capacities)
	return nil
}

func (s *Manager) DeleteQueue(q *kueue.Queue) {
	s.Lock()
	delete(s.queues, q.Name)
	s.Unlock()
}

func derefCapacities(refs []kueue.CapacityReference) []string {
	capacities := make([]string, len(refs))
	for i, c := range refs {
		capacities[i] = string(c)
	}
	return capacities
}
