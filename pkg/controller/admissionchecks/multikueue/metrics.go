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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
)

// reportWorkerClusterStatuses emits the Active status of every worker cluster the
// ClusterQueue references. The ClusterQueue's series are replaced as a whole, so a
// cluster removed from the MultiKueueConfig stops being reported.
//
// Only a cluster that has a MultiKueueCluster object is reported, because the metric
// mirrors that object's Active condition. A cluster the configuration names but that
// does not exist has no status to mirror, and is surfaced by the AdmissionCheck as a
// missing cluster instead. Unknown is reported while the object exists but its Active
// condition has not been set yet.
func (r *cqReconciler) reportWorkerClusterStatuses(ctx context.Context, cq *kueue.ClusterQueue, acName kueue.AdmissionCheckReference) error {
	log := ctrl.LoggerFrom(ctx)
	cqName := kueue.ClusterQueueReference(cq.Name)

	clusterNames, err := admissioncheck.GetRemoteClusters(ctx, r.helper, acName)
	// A configuration that cannot name any worker cluster is not a reconcile failure:
	// the parameters reference is missing or malformed, the object it points to does
	// not exist, or the MultiKueueConfig lists no clusters. There is nothing to report.
	if apierrors.IsNotFound(err) || errors.Is(err, admissioncheck.ErrNilParametersRef) ||
		errors.Is(err, admissioncheck.ErrBadParametersRef) || errors.Is(err, admissioncheck.ErrNoActiveClusters) {
		metrics.ClearMultiKueueClusterQueueMetrics(cqName)
		return nil
	}
	if err != nil {
		return err
	}

	// The statuses are collected before any of them is reported, so that a failed read
	// leaves the series reported so far in place instead of a partial set.
	statuses := make(map[string]metav1.ConditionStatus, len(clusterNames))
	for _, clusterName := range clusterNames {
		cluster := &kueue.MultiKueueCluster{}
		if err := r.client.Get(ctx, types.NamespacedName{Name: clusterName}, cluster); err != nil {
			if client.IgnoreNotFound(err) != nil {
				log.Error(err, "reading cluster", "multiKueueCluster", clusterName)
				return err
			}
			log.V(3).Info("Referenced MultiKueueCluster not found", "multiKueueCluster", clusterName)
			continue
		}
		// The condition is absent until the cluster is reconciled for the first time.
		status := metav1.ConditionUnknown
		if cond := apimeta.FindStatusCondition(cluster.Status.Conditions, kueue.MultiKueueClusterActive); cond != nil {
			status = cond.Status
		}
		statuses[clusterName] = status
	}

	metrics.ClearMultiKueueClusterQueueMetrics(cqName)
	for clusterName, status := range statuses {
		metrics.ReportMultiKueueClusterStatus(cqName, clusterName, status, r.roleTracker)
	}
	return nil
}
