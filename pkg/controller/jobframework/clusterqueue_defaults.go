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

package jobframework

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
)

// ClusterQueueDefaults holds fields that a ClusterQueue can default onto a Workload.
type ClusterQueueDefaults struct {
	MaximumExecutionTimeSeconds *int32
}

// GetClusterQueueDefaults resolves the ClusterQueue backing the given LocalQueue
// and extracts defaulting fields. Returns nil defaults with no error when the queue
// name is empty or when the LocalQueue/ClusterQueue is not found. Returns a
// non-nil error for transient failures so the caller can requeue.
func GetClusterQueueDefaults(ctx context.Context, c client.Client, queueName kueue.LocalQueueName, namespace string) (*ClusterQueueDefaults, error) {
	if !features.Enabled(features.ClusterQueueMaxExecutionTime) {
		return nil, nil
	}

	if queueName == "" {
		return nil, nil
	}

	var lq kueue.LocalQueue
	if err := c.Get(ctx, types.NamespacedName{Name: string(queueName), Namespace: namespace}, &lq); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}

	if lq.Spec.ClusterQueue == "" {
		return nil, nil
	}

	var cq kueue.ClusterQueue
	if err := c.Get(ctx, types.NamespacedName{Name: string(lq.Spec.ClusterQueue)}, &cq); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}

	defaults := &ClusterQueueDefaults{}
	if cq.Spec.WorkloadDefaults != nil {
		defaults.MaximumExecutionTimeSeconds = cq.Spec.WorkloadDefaults.MaximumExecutionTimeSeconds
	}
	return defaults, nil
}

// ApplyClusterQueueDefaults applies ClusterQueue defaults to the workload.
// Fields already set on the workload (e.g. from job labels) are not overridden.
func ApplyClusterQueueDefaults(defaults *ClusterQueueDefaults, wl *kueue.Workload) {
	if defaults == nil {
		return
	}

	if wl.Spec.MaximumExecutionTimeSeconds == nil && defaults.MaximumExecutionTimeSeconds != nil {
		wl.Spec.MaximumExecutionTimeSeconds = defaults.MaximumExecutionTimeSeconds
	}
}
