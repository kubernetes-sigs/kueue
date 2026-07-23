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

// This file is part of the centralized-TAS spike (non-production). It teaches
// the MultiKueue manager to build a single, cluster-qualified view of every
// worker's physical capacity by feeding remote Nodes and Pods into the same
// scheduler TAS cache the single-cluster TAS controllers use. Nothing here is
// gated on a feature gate; it is opt-in via the KUEUE_CENTRALIZED_TAS_SPIKE
// environment variable so it stays inert in normal deployments.

import (
	"context"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	toolscache "k8s.io/client-go/tools/cache"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/util/centralizedtas"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadpatching "sigs.k8s.io/kueue/pkg/workload/patching"
)

// centralizedTASSpikeEnabled reports whether the centralized-TAS spike is on.
func centralizedTASSpikeEnabled() bool {
	return centralizedtas.Enabled()
}

// startTASInventoryWatchers registers Node and Pod event handlers on the remote
// worker's cache and feeds them into the manager's scheduler TAS cache. It
// deliberately reuses the very same cache mutators the single-cluster TAS
// controllers call (TASCache().SyncNode / DeleteNodeByName for nodes and
// Update / DeletePodByKey for non-TAS usage) and the shared
// utiltas.BelongsToNonTASCache predicate, so single- and multi-cluster ingest
// share one code path rather than duplicating the TAS accounting logic.
func (rc *remoteClient) startTASInventoryWatchers(ctx context.Context) error {
	tasCache := rc.schedulerCache.TASCache()
	log := ctrl.LoggerFrom(ctx).WithValues("clusterName", rc.clusterName)

	syncNode := func(obj any) {
		node, ok := obj.(*corev1.Node)
		if !ok {
			return
		}
		// Stamp the cluster label so the manager sees this node under the
		// worker's literal topology level, then sync it verbatim.
		nodeCopy := node.DeepCopy()
		if nodeCopy.Labels == nil {
			nodeCopy.Labels = map[string]string{}
		}
		nodeCopy.Labels[centralizedtas.ClusterLabel] = rc.clusterName
		tasCache.SyncNode(nodeCopy)
	}

	if _, err := rc.client.AddCacheEventHandler(ctx, &corev1.Node{}, toolscache.ResourceEventHandlerFuncs{
		AddFunc:    syncNode,
		UpdateFunc: func(_, newObj any) { syncNode(newObj) },
		DeleteFunc: func(obj any) {
			if node, err := deletedObjectState[*corev1.Node](obj); err == nil {
				tasCache.DeleteNodeByName(node.Name)
			}
		},
	}); err != nil {
		return err
	}

	syncPod := func(obj any) {
		pod, ok := obj.(*corev1.Pod)
		if !ok {
			return
		}
		if utiltas.BelongsToNonTASCache(pod) {
			tasCache.Update(pod, log)
		} else {
			tasCache.DeletePodByKey(podKey(pod), log)
		}
	}

	if _, err := rc.client.AddCacheEventHandler(ctx, &corev1.Pod{}, toolscache.ResourceEventHandlerFuncs{
		AddFunc:    syncPod,
		UpdateFunc: func(_, newObj any) { syncPod(newObj) },
		DeleteFunc: func(obj any) {
			if pod, err := deletedObjectState[*corev1.Pod](obj); err == nil {
				tasCache.DeletePodByKey(podKey(pod), log)
			}
		},
	}); err != nil {
		return err
	}

	log.V(2).Info("Started centralized-TAS remote inventory watchers")
	return nil
}

func podKey(pod *corev1.Pod) client.ObjectKey {
	return types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}
}

// projectAdmissionForWorker translates the manager's node-level admission into
// the admission the worker must execute verbatim. The manager assignment uses
// the worker cluster as its top (literal) topology level and the node hostname
// as its lowest level; the projection strips the cluster level and collapses
// the assignment to a host-exact placement (Levels=[hostname]) that the worker
// ungater applies as plain node selectors on real worker nodes.
//
// It returns the chosen worker cluster (the shared top-level value), the
// projected Admission, and ok=false when the manager has not yet computed a
// node-level assignment (so the caller falls back to normal MultiKueue).
func projectAdmissionForWorker(local *kueue.Workload) (string, *kueue.Admission, bool) {
	if local.Status.Admission == nil {
		return "", nil, false
	}
	out := local.Status.Admission.DeepCopy()
	clusterName := ""
	sawTopology := false
	for i := range out.PodSetAssignments {
		psa := &out.PodSetAssignments[i]
		if psa.TopologyAssignment == nil {
			continue
		}
		internal := utiltas.InternalFrom(psa.TopologyAssignment)
		if len(internal.Levels) < 2 {
			// Expect at least [cluster-label, ..., hostname]; without a cluster
			// level we cannot pick a worker, so treat as not-yet-ready.
			return "", nil, false
		}
		sawTopology = true

		countByHost := make(map[string]int32)
		var hostOrder []string
		for _, d := range internal.Domains {
			cluster := d.Values[0]
			if clusterName == "" {
				clusterName = cluster
			} else if clusterName != cluster {
				// Spike invariant: a workload is pinned to a single cluster.
				return "", nil, false
			}
			host := d.Values[len(d.Values)-1]
			if _, seen := countByHost[host]; !seen {
				hostOrder = append(hostOrder, host)
			}
			countByHost[host] += d.Count
		}

		worker := &utiltas.TopologyAssignment{Levels: []string{corev1.LabelHostname}}
		for _, host := range hostOrder {
			worker.Domains = append(worker.Domains, utiltas.TopologyDomainAssignment{
				Values: []string{host},
				Count:  countByHost[host],
			})
		}
		psa.TopologyAssignment = utiltas.V1Beta2From(worker)
		psa.DelayedTopologyRequest = nil
	}
	if !sawTopology || clusterName == "" {
		return "", nil, false
	}
	return clusterName, out, true
}

// centralizedTASNominate implements the spike's manager-authoritative dispatch:
// the manager has already computed a full node-level assignment, so instead of
// letting the worker schedule, it pins the workload to the cluster chosen by the
// assignment and stamps the projected admission onto the remote workload. The
// worker then skips scheduling (already admitted) and its ungater executes the
// manager's host-exact placement. It returns handled=false when the manager has
// not yet produced a node-level assignment, so the caller runs normal dispatch.
func (w *wlReconciler) centralizedTASNominate(ctx context.Context, group *wlGroup) (reconcile.Result, bool, error) {
	log := ctrl.LoggerFrom(ctx).WithValues("op", "centralizedTASNominate")

	clusterName, admission, ok := projectAdmissionForWorker(group.local)
	if !ok {
		return reconcile.Result{}, false, nil
	}
	log = log.WithValues("chosenCluster", clusterName)

	rc, found := group.remoteClients[clusterName]
	if !found {
		return reconcile.Result{}, true, fmt.Errorf("centralized-TAS: chosen cluster %q is not an available worker", clusterName)
	}

	if !slices.Equal(group.local.Status.NominatedClusterNames, []string{clusterName}) {
		if err := workloadpatching.PatchAdmissionStatus(ctx, w.client, group.local, w.clock, func(wl *kueue.Workload) (bool, error) {
			wl.Status.NominatedClusterNames = []string{clusterName}
			return true, nil
		}); err != nil {
			return reconcile.Result{}, true, err
		}
	}

	// Ensure only the chosen cluster holds a remote workload (create it if needed).
	if _, err := w.syncToSingleCluster(ctx, log, group, clusterName); err != nil {
		return reconcile.Result{}, true, err
	}

	remoteCl := rc.getClient()
	remoteWl := &kueue.Workload{}
	if err := remoteCl.Get(ctx, client.ObjectKeyFromObject(group.local), remoteWl); err != nil {
		// The remote workload was just created and may not be visible yet; retry.
		return reconcile.Result{}, true, client.IgnoreNotFound(err)
	}

	if workload.IsAdmitted(remoteWl) {
		// Already stamped; the normal reserving-remote path takes over.
		return reconcile.Result{RequeueAfter: w.workerLostTimeout}, true, nil
	}

	// Stamp the manager's admission so the worker executes it without scheduling.
	workload.SetQuotaReservation(remoteWl, admission, w.clock)
	workload.SetAdmittedCondition(remoteWl, w.clock.Now(), "CentralizedTAS", "Admitted by centralized-TAS manager")
	if err := remoteCl.Status().Update(ctx, remoteWl); err != nil {
		return reconcile.Result{}, true, fmt.Errorf("centralized-TAS: stamping remote admission: %w", err)
	}
	log.V(2).Info("Stamped manager admission onto remote workload", "podSets", len(admission.PodSetAssignments))
	return reconcile.Result{RequeueAfter: w.workerLostTimeout}, true, nil
}
