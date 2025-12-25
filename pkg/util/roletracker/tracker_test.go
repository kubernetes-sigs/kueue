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
	"testing"
)

func TestNewRoleTracker(t *testing.T) {
	electedChan := make(chan struct{})
	rt := NewRoleTracker(electedChan)

	if rt == nil {
		t.Fatal("NewRoleTracker returned nil")
	}

	if got := rt.GetRole(); got != RoleFollower {
		t.Errorf("NewRoleTracker initial role = %q, want %q", got, RoleFollower)
	}
}

func TestRoleTracker_StartLeaderElection(t *testing.T) {
	tests := []struct {
		name          string
		closeChannel  bool
		cancelContext bool
		expectedRole  string
	}{
		{
			name:         "becomes leader when elected",
			closeChannel: true,
			expectedRole: RoleLeader,
		},
		{
			name:          "stays follower when context cancelled",
			cancelContext: true,
			expectedRole:  RoleFollower,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			electedChan := make(chan struct{})
			rt := NewRoleTracker(electedChan)

			if got := rt.GetRole(); got != RoleFollower {
				t.Errorf("Initial role = %q, want %q", got, RoleFollower)
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			done := make(chan struct{})
			go func() {
				rt.Start(ctx)
				close(done)
			}()

			if tc.closeChannel {
				close(electedChan)
			}
			if tc.cancelContext {
				cancel()
			}

			<-done

			if got := rt.GetRole(); got != tc.expectedRole {
				t.Errorf("After election, role = %q, want %q", got, tc.expectedRole)
			}
		})
	}
}

func TestGetMetricsRole(t *testing.T) {
	tests := []struct {
		name     string
		tracker  *RoleTracker
		expected string
	}{
		{
			name:     "nil tracker returns standalone",
			tracker:  nil,
			expected: RoleStandalone,
		},
		{
			name:     "follower tracker returns follower",
			tracker:  NewFakeRoleTracker(RoleFollower),
			expected: RoleFollower,
		},
		{
			name:     "leader tracker returns leader",
			tracker:  NewFakeRoleTracker(RoleLeader),
			expected: RoleLeader,
		},
		{
			name:     "standalone tracker returns standalone",
			tracker:  NewFakeRoleTracker(RoleStandalone),
			expected: RoleStandalone,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := GetRole(tc.tracker)
			if got != tc.expected {
				t.Errorf("GetMetricsRole() = %q, want %q", got, tc.expected)
			}
		})
	}
}
