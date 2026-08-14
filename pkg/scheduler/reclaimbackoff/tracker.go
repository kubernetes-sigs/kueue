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

// Package reclaimbackoff implements a per-(ClusterQueue, FlavorResource) reclaim
// backoff. After a ClusterQueue's borrowed resource is reclaimed by preemption,
// the scheduler defers, for an exponentially growing cooldown, only the flavor
// assignments that would borrow that same resource again. This breaks the
// "admitted then immediately reclaimed" loop without blocking assignments that
// fit within nominal quota or that use other resources.
//
// The state is held in memory on the scheduler. A controller restart clears it,
// which briefly loses debouncing during the restart window; this is an accepted
// trade-off, since preemption storms are a seconds-scale phenomenon and
// persisting the state to ClusterQueue status would add API write pressure.
package reclaimbackoff

import (
	"sync"
	"time"

	"k8s.io/utils/clock"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/util/wait"
)

// key identifies a single (ClusterQueue, FlavorResource) backoff entry.
type key struct {
	cq kueue.ClusterQueueReference
	fr resources.FlavorResource
}

// entry holds the backoff state for one key. count and the timestamps are
// always updated together under the Tracker mutex, so they cannot drift.
type entry struct {
	count        int32
	backoffUntil time.Time
	lastReclaim  time.Time
}

// Tracker records reclaim events per (ClusterQueue, FlavorResource) and answers
// whether a given pair is currently in its backoff cooldown. It is safe for
// concurrent use.
type Tracker struct {
	mu    sync.Mutex
	state map[key]entry

	base  time.Duration
	max   time.Duration
	reset time.Duration
	clock clock.Clock
}

// New returns a Tracker configured with the given backoff parameters. reset is
// the quiet period after which a pair's consecutive-reclaim counter is cleared;
// it should be noticeably larger than base, otherwise the counter resets within
// a single base window and the backoff never grows.
func New(base, max, reset time.Duration, c clock.Clock) *Tracker {
	return &Tracker{
		state: make(map[key]entry),
		base:  base,
		max:   max,
		reset: reset,
		clock: c,
	}
}

// RecordReclaim registers a reclaim of fr on cq and returns the cooldown
// duration now in effect for that pair. If the pair was quiet for longer than
// the reset window, the consecutive-reclaim counter restarts from one.
func (t *Tracker) RecordReclaim(cq kueue.ClusterQueueReference, fr resources.FlavorResource) time.Duration {
	now := t.clock.Now()
	backoff := wait.NewBackoff(t.base, t.max, 2, 0.0001)

	t.mu.Lock()
	defer t.mu.Unlock()

	// Prune entries whose cooldown has expired and whose reset window has also
	// passed: a subsequent reclaim would reset their count anyway, so they are
	// indistinguishable from no entry at all. This keeps the map bounded by the
	// number of pairs reclaimed within a reset window instead of growing with
	// every pair ever reclaimed. Entries still backing off are kept even past
	// the reset window, since max may be configured larger than reset.
	for k, e := range t.state {
		if !now.Before(e.backoffUntil) && now.Sub(e.lastReclaim) > t.reset {
			delete(t.state, k)
		}
	}

	k := key{cq: cq, fr: fr}
	e := t.state[k]
	if !e.lastReclaim.IsZero() && now.Sub(e.lastReclaim) > t.reset {
		e.count = 0
	}
	e.count++
	cooldown := backoff.WaitTime(int(e.count))
	e.backoffUntil = now.Add(cooldown)
	e.lastReclaim = now
	t.state[k] = e
	return cooldown
}

// IsBackingOff reports whether fr on cq is currently within its cooldown window.
func (t *Tracker) IsBackingOff(cq kueue.ClusterQueueReference, fr resources.FlavorResource) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	e, ok := t.state[key{cq: cq, fr: fr}]
	if !ok {
		return false
	}
	return t.clock.Now().Before(e.backoffUntil)
}

// MaxRemaining returns the longest time until any FlavorResource on cq leaves
// its cooldown, or zero if nothing on cq is currently backing off. It is used to
// schedule the next retry of a deferred workload.
func (t *Tracker) MaxRemaining(cq kueue.ClusterQueueReference) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.clock.Now()
	var maxRemaining time.Duration
	for k, e := range t.state {
		if k.cq != cq {
			continue
		}
		if remaining := e.backoffUntil.Sub(now); remaining > maxRemaining {
			maxRemaining = remaining
		}
	}
	return maxRemaining
}
