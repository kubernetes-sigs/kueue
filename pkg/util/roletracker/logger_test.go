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
	"encoding/json"
	"testing"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestWithReplicaRole(t *testing.T) {
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
			name:     "leader tracker returns leader",
			tracker:  NewFakeRoleTracker(RoleLeader),
			expected: RoleLeader,
		},
		{
			name:     "follower tracker returns follower",
			tracker:  NewFakeRoleTracker(RoleFollower),
			expected: RoleFollower,
		},
		{
			name:     "standalone tracker returns standalone",
			tracker:  NewFakeRoleTracker(RoleStandalone),
			expected: RoleStandalone,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			capture, baseLogger := newJSONLoggerCapture()

			logger := WithReplicaRole(baseLogger, tc.tracker)
			logger.Info("test")

			entry := capture.requireSingleEntry(t)
			if got := entry["replica-role"]; got != tc.expected {
				t.Errorf("Expected replica-role %q, got %v", tc.expected, got)
			}
		})
	}
}

func TestNewLogConstructor(t *testing.T) {
	type logCall struct {
		request  *reconcile.Request
		nextRole string
	}

	tests := []struct {
		name        string
		tracker     *RoleTracker
		calls       []logCall
		wantEntries []map[string]any
	}{
		{
			name:    "nil tracker emits standalone role",
			tracker: nil,
			calls: []logCall{
				{},
			},
			wantEntries: []map[string]any{
				{"replica-role": RoleStandalone},
			},
		},
		{
			name:    "follower tracker emits follower role",
			tracker: NewFakeRoleTracker(RoleFollower),
			calls: []logCall{
				{},
			},
			wantEntries: []map[string]any{
				{"replica-role": RoleFollower},
			},
		},
		{
			name:    "role changes are reflected across calls",
			tracker: NewFakeRoleTracker(RoleFollower),
			calls: []logCall{
				{nextRole: RoleLeader},
				{},
			},
			wantEntries: []map[string]any{
				{"replica-role": RoleFollower},
				{"replica-role": RoleLeader},
			},
		},
		{
			name:    "request context adds namespace and name for namespaced requests",
			tracker: nil,
			calls: []logCall{
				{
					request: &reconcile.Request{
						NamespacedName: types.NamespacedName{Namespace: "test-ns", Name: "test-name"},
					},
				},
			},
			wantEntries: []map[string]any{
				{"replica-role": RoleStandalone, "namespace": "test-ns", "name": "test-name"},
			},
		},
		{
			name:    "request context adds only name for cluster-scoped requests",
			tracker: nil,
			calls: []logCall{
				{
					request: &reconcile.Request{
						NamespacedName: types.NamespacedName{Name: "cluster-name"},
					},
				},
			},
			wantEntries: []map[string]any{
				{"replica-role": RoleStandalone, "name": "cluster-name"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			capture, logger := newJSONLoggerCapture()
			previousLogger := ctrl.Log
			ctrl.Log = logger
			t.Cleanup(func() {
				ctrl.Log = previousLogger
			})

			constructor := NewLogConstructor(tc.tracker, "test-controller")
			for _, call := range tc.calls {
				constructor(call.request).Info("test")
				if call.nextRole != "" && tc.tracker != nil {
					tc.tracker.role.Store(call.nextRole)
				}
			}

			gotEntries := selectedLogEntries(capture.requireEntries(t, len(tc.wantEntries)))
			if diff := cmp.Diff(tc.wantEntries, gotEntries); diff != "" {
				t.Errorf("Unexpected log entries (-want,+got):\n%s", diff)
			}
		})
	}
}

type jsonLogCapture struct {
	entries  []map[string]any
	parseErr error
}

func newJSONLoggerCapture() (*jsonLogCapture, logr.Logger) {
	capture := &jsonLogCapture{}
	logger := funcr.NewJSON(func(obj string) {
		entry := make(map[string]any)
		if err := json.Unmarshal([]byte(obj), &entry); err != nil {
			capture.parseErr = err
			return
		}
		capture.entries = append(capture.entries, entry)
	}, funcr.Options{})
	return capture, logger
}

func (c *jsonLogCapture) requireSingleEntry(t *testing.T) map[string]any {
	t.Helper()
	return c.requireEntries(t, 1)[0]
}

func (c *jsonLogCapture) requireEntries(t *testing.T, want int) []map[string]any {
	t.Helper()
	if c.parseErr != nil {
		t.Fatalf("Failed to parse log entry: %v", c.parseErr)
	}
	if len(c.entries) != want {
		t.Fatalf("Expected %d log entries, got %d", want, len(c.entries))
	}
	return c.entries
}

func selectedLogEntries(entries []map[string]any) []map[string]any {
	selected := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		selectedEntry := map[string]any{
			"replica-role": entry["replica-role"],
		}
		if namespace, found := entry["namespace"]; found {
			selectedEntry["namespace"] = namespace
		}
		if name, found := entry["name"]; found {
			selectedEntry["name"] = name
		}
		selected = append(selected, selectedEntry)
	}
	return selected
}
