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

package multikueue

import (
	"context"
	"errors"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
)

// reportWorkerClusterStatuses replaces the metric snapshot from current references.
// Refreshing the whole snapshot also removes series for deleted or unlinked queues,
// without retaining a second copy of their former AdmissionCheck relationships.
func (a *ACReconciler) reportWorkerClusterStatuses(ctx context.Context) error {
	a.metricsMu.Lock()
	defer a.metricsMu.Unlock()

	queues := &kueue.ClusterQueueList{}
	if err := a.client.List(ctx, queues); err != nil {
		return err
	}
	checks := &kueue.AdmissionCheckList{}
	if err := a.client.List(ctx, checks, client.MatchingFields{AdmissionCheckControllerNameKey: kueue.MultiKueueControllerName}); err != nil {
		return err
	}
	checksByName := make(map[kueue.AdmissionCheckReference]struct{}, len(checks.Items))
	for _, check := range checks.Items {
		checksByName[kueue.AdmissionCheckReference(check.Name)] = struct{}{}
	}
	queuesByCheck := make(map[kueue.AdmissionCheckReference][]kueue.ClusterQueueReference)
	for _, queue := range queues.Items {
		if queue.Spec.AdmissionChecksStrategy == nil {
			continue
		}
		for _, rule := range queue.Spec.AdmissionChecksStrategy.AdmissionChecks {
			if _, found := checksByName[rule.Name]; found {
				queuesByCheck[rule.Name] = append(queuesByCheck[rule.Name], kueue.ClusterQueueReference(queue.Name))
				break
			}
		}
	}
	snapshot := make(map[kueue.ClusterQueueReference]map[string]metav1.ConditionStatus)
	// A worker shared by several checks is read only once; nil denotes a missing object.
	workers := make(map[string]*metav1.ConditionStatus)
	for checkName, queueNames := range queuesByCheck {
		clusterNames, err := admissioncheck.GetRemoteClusters(ctx, a.helper, checkName)
		if apierrors.IsNotFound(err) || errors.Is(err, admissioncheck.ErrNilParametersRef) ||
			errors.Is(err, admissioncheck.ErrBadParametersRef) || errors.Is(err, admissioncheck.ErrNoActiveClusters) {
			continue
		}
		if err != nil {
			return err
		}
		statuses := make(map[string]metav1.ConditionStatus, len(clusterNames))
		for _, name := range clusterNames {
			status, found := workers[name]
			if !found {
				cluster := &kueue.MultiKueueCluster{}
				if err := a.client.Get(ctx, types.NamespacedName{Name: name}, cluster); err != nil {
					if !apierrors.IsNotFound(err) {
						return err
					}
					workers[name] = nil
					continue
				}
				value := metav1.ConditionUnknown
				if condition := apimeta.FindStatusCondition(cluster.Status.Conditions, kueue.MultiKueueClusterActive); condition != nil {
					value = condition.Status
				}
				status = &value
				workers[name] = status
			}
			if status != nil {
				statuses[name] = *status
			}
		}
		for _, name := range queueNames {
			snapshot[name] = statuses
		}
	}
	// A failed read above leaves the last complete snapshot in place.
	metrics.MultiKueueClusterByStatus.Reset()
	for queue, statuses := range snapshot {
		for cluster, status := range statuses {
			metrics.ReportMultiKueueClusterStatus(queue, cluster, status, a.roleTracker)
		}
	}
	return nil
}
