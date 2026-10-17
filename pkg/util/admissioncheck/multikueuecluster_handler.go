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

package admissioncheck

import (
	"context"
	"slices"
	"time"

	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
)

// NewMultiKueueClusterHandler enqueues workloads whose dispatch configuration
// references a cluster when its cordon policy changes.
func NewMultiKueueClusterHandler(c client.Client, eventsBatchPeriod time.Duration) handler.EventHandler {
	return handler.Funcs{
		UpdateFunc: func(ctx context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			if !features.Enabled(features.MultiKueueClusterCordon) {
				return
			}
			oldCluster, oldOK := e.ObjectOld.(*kueue.MultiKueueCluster)
			newCluster, newOK := e.ObjectNew.(*kueue.MultiKueueCluster)
			if !oldOK || !newOK || ptr.Deref(oldCluster.Spec.Unschedulable, false) == ptr.Deref(newCluster.Spec.Unschedulable, false) {
				return
			}
			if err := queueWorkloadsForMultiKueueCluster(ctx, c, newCluster.Name, eventsBatchPeriod, q); err != nil {
				ctrl.LoggerFrom(ctx).Error(err, "Queueing workloads after cluster cordon policy change", "cluster", newCluster.Name)
			}
		},
	}
}

func queueWorkloadsForMultiKueueCluster(ctx context.Context, c client.Client, clusterName string, delay time.Duration, q workqueue.TypedRateLimitingInterface[reconcile.Request]) error {
	configs := &kueue.MultiKueueConfigList{}
	if err := c.List(ctx, configs); err != nil {
		return err
	}
	configNames := make(map[string]bool)
	for _, config := range configs.Items {
		if slices.Contains(config.Spec.Clusters, clusterName) {
			configNames[config.Name] = true
		}
	}
	checks := &kueue.AdmissionCheckList{}
	if err := c.List(ctx, checks); err != nil {
		return err
	}
	checkNames := make(map[kueue.AdmissionCheckReference]bool)
	for _, check := range checks.Items {
		ref := check.Spec.Parameters
		if check.Spec.ControllerName == kueue.MultiKueueControllerName && ref != nil &&
			ref.APIGroup == kueue.SchemeGroupVersion.Group && ref.Kind == "MultiKueueConfig" && configNames[ref.Name] {
			checkNames[kueue.AdmissionCheckReference(check.Name)] = true
		}
	}
	workloads := &kueue.WorkloadList{}
	if err := c.List(ctx, workloads); err != nil {
		return err
	}
	for _, wl := range workloads.Items {
		for _, check := range wl.Status.AdmissionChecks {
			if checkNames[check.Name] {
				q.AddAfter(reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&wl)}, delay)
				break
			}
		}
	}
	return nil
}
