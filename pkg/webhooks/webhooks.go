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
)

// Setup sets up the webhooks for core controllers. It returns the name of the
// webhook that failed to create and an error, if any.
func Setup(mgr ctrl.Manager, dispatcherName string) (string, error) {
	if err := setupWebhookForWorkload(mgr, dispatcherName); err != nil {
		return "Workload", err
	}

	if err := setupWebhookForResourceFlavor(mgr); err != nil {
		return "ResourceFlavor", err
	}

	if err := setupWebhookForClusterQueue(mgr); err != nil {
		return "ClusterQueue", err
	}

	if err := setupWebhookForCohort(mgr); err != nil {
		return "Cohort", err
	}

	return "", nil
}
