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

package scheduler

import (
	"context"
	"fmt"
	"sync"

	"sigs.k8s.io/controller-runtime/pkg/source"
)

// initialSync counts the event sources whose handlers fill the cache and
// have not yet processed their informer's initial list.
type initialSync struct {
	mu      sync.Mutex
	pending int
	// synced is closed when pending drops to zero. It is nil while nothing
	// is pending.
	synced chan struct{}
}

func (s *initialSync) add() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == 0 {
		s.synced = make(chan struct{})
	}
	s.pending++
}

func (s *initialSync) done() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending--
	if s.pending == 0 {
		close(s.synced)
		s.synced = nil
	}
}

// wait returns a channel that is closed once no source is pending.
func (s *initialSync) wait() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.synced == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return s.synced
}

// TrackInitialSync returns src wrapped so that WaitForInitialSync does not
// return before src has synced. Call it while setting up a controller whose
// event handlers add objects to this cache, before the manager starts.
//
// A source built with source.Kind syncs once its event handler has returned
// for every object in the informer's initial list, so after that the cache
// holds every object of that kind that existed when the informer synced. The
// controller waits for its sources before it starts workers, which is the
// only time it calls WaitForSync. The controllers that use it run on every
// replica (NeedLeaderElection is false), so a follower is synced before it
// takes the lease.
func (c *Cache) TrackInitialSync(src source.SyncingSource) source.SyncingSource {
	c.initialSync.add()
	return &initialSyncSource{SyncingSource: src, done: sync.OnceFunc(c.initialSync.done)}
}

// WaitForInitialSync blocks until every source passed to TrackInitialSync has
// synced, or ctx is done. It returns at once if no source is tracked.
func (c *Cache) WaitForInitialSync(ctx context.Context) error {
	select {
	case <-c.initialSync.wait():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type initialSyncSource struct {
	source.SyncingSource
	done func()
}

// WaitForSync marks the source as synced once the wrapped source has synced.
// A source that fails to sync stays pending. The controller then fails to
// start and the manager stops, which cancels the scheduler's wait.
func (s *initialSyncSource) WaitForSync(ctx context.Context) error {
	if err := s.SyncingSource.WaitForSync(ctx); err != nil {
		return err
	}
	s.done()
	return nil
}

func (s *initialSyncSource) String() string {
	return fmt.Sprintf("%v", s.SyncingSource)
}
