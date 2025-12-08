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
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// WithReplicaRole adds replica-role to the logger. Returns "standalone" if tracker is nil.
func WithReplicaRole(log logr.Logger, tracker *RoleTracker) logr.Logger {
	return log.WithValues("replica-role", GetRole(tracker))
}

// WithRequestContext adds namespace and name to the logger.
func WithRequestContext(log logr.Logger, namespace, name string) logr.Logger {
	if namespace != "" {
		return log.WithValues("namespace", namespace, "name", name)
	}
	return log.WithValues("name", name)
}

// NewLogConstructor creates a controller LogConstructor with replica-role and request context.
func NewLogConstructor(tracker *RoleTracker, baseName string) func(*reconcile.Request) logr.Logger {
	return func(req *reconcile.Request) logr.Logger {
		log := ctrl.Log.WithName(baseName).WithValues("replica-role", GetRole(tracker))
		if req == nil {
			return log
		}
		return WithRequestContext(log, req.Namespace, req.Name)
	}
}

// GetRole returns the role, or "standalone" if tracker is nil.
func GetRole(tracker *RoleTracker) string {
	if tracker == nil {
		return RoleStandalone
	}
	return tracker.GetRole()
}

// WebhookLogConstructor wraps admission.DefaultLogConstructor and adds replica-role.
func WebhookLogConstructor(tracker *RoleTracker) func(logr.Logger, *admission.Request) logr.Logger {
	return func(base logr.Logger, req *admission.Request) logr.Logger {
		log := admission.DefaultLogConstructor(base, req)
		return log.WithValues("replica-role", GetRole(tracker))
	}
}
