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

package was

import (
	"sync"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
)

type virtualPodsPerWorkload struct {
	lock               sync.RWMutex
	podsPerWorkloadUID map[types.UID]VirtualPods
}

func newVirtualPodsPerWorkload() *virtualPodsPerWorkload {
	return &virtualPodsPerWorkload{
		podsPerWorkloadUID: make(map[types.UID]VirtualPods),
	}
}

func (s *virtualPodsPerWorkload) get(uid types.UID) (VirtualPods, bool) {
	s.lock.RLock()
	defer s.lock.RUnlock()
	entry, ok := s.podsPerWorkloadUID[uid]
	return entry, ok
}

func (s *virtualPodsPerWorkload) set(uid types.UID, entry VirtualPods) {
	s.lock.Lock()
	defer s.lock.Unlock()
	s.podsPerWorkloadUID[uid] = entry
}

func (s *virtualPodsPerWorkload) delete(uid types.UID) {
	s.lock.Lock()
	defer s.lock.Unlock()
	delete(s.podsPerWorkloadUID, uid)
}

func (s *virtualPodsPerWorkload) prune(activeUIDs sets.Set[types.UID]) {
	s.lock.Lock()
	defer s.lock.Unlock()
	for uid := range s.podsPerWorkloadUID {
		if !activeUIDs.Has(uid) {
			delete(s.podsPerWorkloadUID, uid)
		}
	}
}

func (s *virtualPodsPerWorkload) len() int {
	s.lock.RLock()
	defer s.lock.RUnlock()
	return len(s.podsPerWorkloadUID)
}
