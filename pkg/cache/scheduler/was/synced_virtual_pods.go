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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
)

// cachedVirtualPods holds the generated virtual pods for an admitted Workload
// along with the Workload's ResourceVersion at the time of generation
type cachedVirtualPods struct {
	// resourceVersion of the parent Workload. When the Workload's ResourceVersion
	// changes (e.g. status or admission changes), the cached pods are invalidated
	resourceVersion string
	pods            []*corev1.Pod
}

type syncedVirtualPods struct {
	lock sync.RWMutex
	pods map[types.UID]cachedVirtualPods
}

func newSyncedVirtualPods() *syncedVirtualPods {
	return &syncedVirtualPods{
		pods: make(map[types.UID]cachedVirtualPods),
	}
}

func (s *syncedVirtualPods) get(uid types.UID) (cachedVirtualPods, bool) {
	s.lock.RLock()
	defer s.lock.RUnlock()
	entry, ok := s.pods[uid]
	return entry, ok
}

func (s *syncedVirtualPods) set(uid types.UID, entry cachedVirtualPods) {
	s.lock.Lock()
	defer s.lock.Unlock()
	s.pods[uid] = entry
}

func (s *syncedVirtualPods) delete(uid types.UID) {
	s.lock.Lock()
	defer s.lock.Unlock()
	delete(s.pods, uid)
}

func (s *syncedVirtualPods) prune(activeUIDs sets.Set[types.UID]) {
	s.lock.Lock()
	defer s.lock.Unlock()
	for uid := range s.pods {
		if !activeUIDs.Has(uid) {
			delete(s.pods, uid)
		}
	}
}

func (s *syncedVirtualPods) len() int {
	s.lock.RLock()
	defer s.lock.RUnlock()
	return len(s.pods)
}
