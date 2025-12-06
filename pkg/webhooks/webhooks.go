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

package webhooks

import (
	ctrl "sigs.k8s.io/controller-runtime"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
)

// SetupOption configures webhook Setup.
type SetupOption func(*setupOptions)

type setupOptions struct {
	roleTracker *roletracker.RoleTracker
}

// WithRoleTracker sets the role tracker for HA setups.
func WithRoleTracker(tracker *roletracker.RoleTracker) SetupOption {
	return func(o *setupOptions) {
		o.roleTracker = tracker
	}
}

// Setup sets up the webhooks for core controllers. It returns the name of the
// webhook that failed to create and an error, if any.
func Setup(mgr ctrl.Manager, opts ...SetupOption) (string, error) {
	options := &setupOptions{}
	for _, opt := range opts {
		opt(options)
	}

	if err := setupWebhookForWorkload(mgr, options.roleTracker); err != nil {
		return "Workload", err
	}

	if err := setupWebhookForResourceFlavor(mgr, options.roleTracker); err != nil {
		return "ResourceFlavor", err
	}

	if err := setupWebhookForClusterQueue(mgr, options.roleTracker); err != nil {
		return "ClusterQueue", err
	}

	if err := setupWebhookForCohort(mgr, options.roleTracker); err != nil {
		return "Cohort", err
	}

	if err := setupWebhookForLocalQueue(mgr, options.roleTracker); err != nil {
		return "LocalQueue", err
	}

	return "", nil
}

func setupWebhookForLocalQueue(mgr ctrl.Manager, roleTracker *roletracker.RoleTracker) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&kueue.LocalQueue{}).
		WithLogConstructor(roletracker.WebhookLogConstructor(roleTracker)).
		Complete()
}
