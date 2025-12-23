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

	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	RoleLeader     = "leader"
	RoleFollower   = "follower"
	RoleStandalone = "standalone"
)

// RoleTracker provides thread-safe access to the current HA role.
type RoleTracker struct {
	role        atomic.Value
	electedChan <-chan struct{}
}

// NewRoleTracker creates a RoleTracker that starts as follower and becomes leader when electedChan closes.
func NewRoleTracker(electedChan <-chan struct{}) *RoleTracker {
	rt := &RoleTracker{
		electedChan: electedChan,
	}
	rt.role.Store(RoleFollower)
	return rt
}

// NewFakeRoleTracker creates a RoleTracker with a fixed role for testing.
func NewFakeRoleTracker(role string) *RoleTracker {
	rt := &RoleTracker{}
	rt.role.Store(role)
	return rt
}

// Start blocks until leadership is acquired or context is cancelled.
func (rt *RoleTracker) Start(ctx context.Context) {
	log := log.FromContext(ctx)
	select {
	case <-rt.electedChan:
		rt.role.Store(RoleLeader)
		log.Info("RoleTracker: transitioned to leader")
	case <-ctx.Done():
		return
	}
}

func (rt *RoleTracker) GetRole() string {
	return rt.role.Load().(string)
}
