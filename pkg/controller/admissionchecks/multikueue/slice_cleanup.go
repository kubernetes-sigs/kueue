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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
)

// sliceCleanupAdapter scopes deletion to the remote job's prebuilt workload and
// waits for that job to disappear before removing the remote workload.
type sliceCleanupAdapter interface {
	CleanupRemoteSlice(ctx context.Context, remoteClient client.Client, key types.NamespacedName, workloadName, origin string) (bool, error)
}

var errSliceCleanupPending = errors.New("remote slice deletion is still pending")

// A resize normally reuses the remote job. After eviction, however, the old
// job must be removed first, even if its workload has already finished/Replaced.
// Use the remote job's existing marker rather than introducing handover state.
func (w *wlReconciler) waitForRemoteEvictedSlice(ctx context.Context, group *wlGroup) (bool, error) {
	if _, supported := group.jobAdapter.(sliceCleanupAdapter); !supported {
		return false, nil
	}
	reader := w.apiReader
	if reader == nil {
		reader = w.client
	}
	for _, remote := range group.remoteClients {
		job := &metav1.PartialObjectMetadata{}
		job.SetGroupVersionKind(group.jobAdapter.GVK())
		if err := remote.getClient().Get(ctx, group.controllerKey, job); err != nil {
			if client.IgnoreNotFound(err) != nil {
				return false, err
			}
			continue
		}
		predecessorName := jobframework.PrebuiltWorkloadNameFor(job)
		if predecessorName == "" || predecessorName == group.local.Name {
			continue
		}
		predecessor := &kueue.Workload{}
		if err := reader.Get(ctx, types.NamespacedName{Namespace: group.local.Namespace, Name: predecessorName}, predecessor); err != nil {
			if client.IgnoreNotFound(err) != nil {
				return false, err
			}
			continue
		}
		if workloadevict.IsEvicted(predecessor) {
			ctrl.LoggerFrom(ctx).V(1).Info("Waiting for evicted remote slice deletion", "workload", klog.KObj(group.local), "predecessor", klog.KObj(predecessor))
			return true, nil
		}
	}
	return false, nil
}
