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

package roletracker

import (
	"context"
	"sync/atomic"

	"github.com/go-logr/logr"
)

const (
	// RoleLeader indicates this replica is the leader in an HA setup
	RoleLeader = "leader"
	// RoleFollower indicates this replica is a follower in an HA setup
	RoleFollower = "follower"
	// RoleStandalone indicates single-replica mode (no HA/leader election)
	RoleStandalone = "standalone"
)

// RoleTracker monitors the leader election channel and provides
// thread-safe access to the current role ("leader" or "follower").
type RoleTracker struct {
	role        atomic.Value
	electedChan <-chan struct{}
}

// NewRoleTracker creates a RoleTracker monitoring the given election channel.
// Starts as "follower", transitions to "leader" when the channel closes.
func NewRoleTracker(electedChan <-chan struct{}) *RoleTracker {
	rt := &RoleTracker{
		electedChan: electedChan,
	}
	// Start as follower
	rt.role.Store(RoleFollower)
	return rt
}

// NewFakeRoleTracker creates a RoleTracker with a fixed role for testing.
func NewFakeRoleTracker(role string) *RoleTracker {
	rt := &RoleTracker{
		electedChan: nil,
	}
	rt.role.Store(role)
	return rt
}

// Start monitors the election channel. Blocks until leadership
// is acquired or context is cancelled. Run in a goroutine.
func (rt *RoleTracker) Start(ctx context.Context, log logr.Logger) {
	select {
	case <-rt.electedChan:
		rt.role.Store(RoleLeader)
		log.Info("RoleTracker: transitioned to leader")
	case <-ctx.Done():
		return
	}
}

// GetRole returns the current role.
func (rt *RoleTracker) GetRole() string {
	return rt.role.Load().(string)
}
