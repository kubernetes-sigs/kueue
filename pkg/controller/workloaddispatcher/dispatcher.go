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

package workloaddispatcher

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadfinish "sigs.k8s.io/kueue/pkg/workload/finish"
	workloadpatching "sigs.k8s.io/kueue/pkg/workload/patching"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

var realClock = clock.RealClock{}

// dispatcher holds what the built-in dispatchers share: their dependencies, which
// Workloads they nominate worker clusters for, and how they record a nomination.
// Each dispatcher embeds it and decides which clusters to nominate.
type dispatcher struct {
	client      client.Client
	helper      *admissioncheck.MultiKueueStoreHelper
	clock       clock.Clock
	roleTracker *roletracker.RoleTracker
}

func newDispatcher(c client.Client, helper *admissioncheck.MultiKueueStoreHelper, roleTracker *roletracker.RoleTracker) dispatcher {
	return dispatcher{client: c, helper: helper, clock: realClock, roleTracker: roleTracker}
}

// workloadToNominate fetches the Workload named by req and applies the eligibility
// checks shared by the built-in dispatchers. It returns the Workload together with the
// clusters of its MultiKueueConfig, or a nil Workload (and no error) when nothing should
// be nominated right now. onDone, if set, is called when any nomination round of the
// Workload is over: it is not found, being deleted, finished, or holds no quota
// reservation. A Workload without quota may need nominating again once it gets quota.
func (d *dispatcher) workloadToNominate(ctx context.Context, req ctrl.Request, onDone func(types.NamespacedName)) (*kueue.Workload, []string, error) {
	log := ctrl.LoggerFrom(ctx)
	done := func() {
		if onDone != nil {
			onDone(req.NamespacedName)
		}
	}

	wl := &kueue.Workload{}
	if err := d.client.Get(ctx, req.NamespacedName, wl); err != nil {
		if apierrors.IsNotFound(err) {
			// The Workload was deleted between enqueue and reconcile; nothing to
			// nominate, and requeueing would only back off on a missing object.
			log.V(3).Info("Workload not found, skip the reconciliation")
			done()
			return nil, nil, nil
		}
		log.Error(err, "Failed to retrieve Workload")
		return nil, nil, err
	}

	if !wl.DeletionTimestamp.IsZero() {
		log.V(3).Info("Workload is deleted, skip the reconciliation")
		done()
		return nil, nil, nil
	}

	mkAc, err := admissioncheck.GetMultiKueueAdmissionCheck(ctx, d.client, wl)
	if err != nil {
		log.Error(err, "Can not get MultiKueue AdmissionCheckState")
		return nil, nil, err
	}

	if workload.ShouldSkipClusterNomination(mkAc, wl, workloadslicing.IsElasticWorkload(wl)) {
		log.V(3).Info("Skipping cluster nomination phase")
		return nil, nil, nil
	}

	// Only a new slice of an elastic Workload gets here with a cluster: it keeps the
	// cluster of the slice it replaces, and the MultiKueue workload reconciler syncs
	// it there. The API rejects nominated clusters next to a cluster name.
	if wl.Status.ClusterName != nil {
		log.V(3).Info("Workload is already assigned to a cluster, skip the reconciliation")
		return nil, nil, nil
	}

	remoteClusters, err := admissioncheck.GetRemoteClusters(ctx, d.helper, mkAc.Name)
	if err != nil {
		log.Error(err, "Failed to get the clusters of the MultiKueueConfig", "admissionCheck", mkAc.Name)
		return nil, nil, err
	}

	if workloadfinish.IsFinished(wl) || !workload.HasQuotaReservation(wl) {
		log.V(3).Info("Workload is already finished or has no quota reserved, skip the reconciliation")
		done()
		return nil, nil, nil
	}

	return wl, remoteClusters, nil
}

// nominate records clusters as the Workload's nominated worker clusters.
func (d *dispatcher) nominate(ctx context.Context, wl *kueue.Workload, clusters []string) error {
	err := workloadpatching.PatchAdmissionStatus(ctx, d.client, wl, d.clock, func(wl *kueue.Workload) (bool, error) {
		wl.Status.NominatedClusterNames = clusters
		return true, nil
	})
	if err != nil {
		ctrl.LoggerFrom(ctx).V(2).Error(err, "Failed to patch nominated clusters")
	}
	return err
}
