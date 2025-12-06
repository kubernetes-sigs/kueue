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
	"github.com/go-logr/logr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// WithReplicaRole adds the replica-role field to the logger.
// Uses "standalone" if tracker is nil.
func WithReplicaRole(log logr.Logger, tracker *RoleTracker) logr.Logger {
	return log.WithValues("replica-role", GetMetricsRole(tracker))
}

// NewLogConstructor creates a LogConstructor that injects replica-role into logs.
// Uses "standalone" if tracker is nil.
func NewLogConstructor(tracker *RoleTracker, baseName string) func(*reconcile.Request) logr.Logger {
	return func(req *reconcile.Request) logr.Logger {
		return ctrl.Log.WithName(baseName).WithValues("replica-role", GetMetricsRole(tracker))
	}
}

// GetMetricsRole returns the role for metrics emission.
// Returns "standalone" if tracker is nil (no HA setup), otherwise returns tracker's current role.
func GetMetricsRole(tracker *RoleTracker) string {
	if tracker == nil {
		return RoleStandalone
	}
	return tracker.GetRole()
}
