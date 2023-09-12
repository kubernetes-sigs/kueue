/*
Copyright 2023 The Kubernetes Authors.

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

package preemption

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption"
	"sigs.k8s.io/kueue/pkg/workload"
)

const (
	controllerName  = "PreemptionController"
	throttleTimeout = 500 * time.Millisecond
)

type updateStatusFnc func(context.Context, client.Client, *kueue.Workload, bool) error

type Controller struct {
	log         logr.Logger
	cache       *cache.Cache
	client      client.Client
	recorder    record.EventRecorder
	preemptor   *preemption.Preemptor
	ctx         context.Context
	triggerChan chan struct{}

	// stub, only for test
	updateFnc updateStatusFnc
}

func NewController(cache *cache.Cache) *Controller {
	return &Controller{
		log:   ctrl.Log.WithName(controllerName),
		cache: cache,
		// keep this buffered so we can "requeue" in case of API errors
		triggerChan: make(chan struct{}, 1),
		updateFnc:   updateCheckState,
	}
}

func (c *Controller) SetupWithManager(mgr ctrl.Manager) error {
	c.client = mgr.GetClient()
	c.recorder = mgr.GetEventRecorderFor(controllerName)
	c.preemptor = preemption.New(c.client, c.recorder)
	return mgr.Add(c)
}

var _ manager.Runnable = (*Controller)(nil)

func (c *Controller) Start(ctx context.Context) error {
	c.log.V(5).Info("Staring main loop")
	c.ctx = ctx
	ticker := time.NewTicker(throttleTimeout)
	trigger := false
	timeout := false
	for {
		select {
		case <-c.triggerChan:
			trigger = true
		case <-ticker.C:
			timeout = true
		case <-ctx.Done():
			c.log.V(5).Info("End main loop")
			return nil
		}

		if trigger && timeout {
			c.run()
			trigger = false
			timeout = false
		}
	}
}

func (c *Controller) trigger(logValues ...interface{}) {
	select {
	case c.triggerChan <- struct{}{}:
		c.log.V(2).Info("Triggered", logValues...)
	default:
	}

}

func (c *Controller) NotifyWorkloadUpdate(oldWl, newWl *kueue.Workload) {
	if oldWl == nil {
		// there is noting to on worklods creation
		return
	}
	if newWl == nil {
		// deleting a workload that reserves quota can speed up the
		// transition of preemption pending workloads.
		if workload.HasQuotaReservation(oldWl) {
			c.trigger("event", "Workload with reservation was deleted", "wl", klog.KObj(oldWl))
		}
		return
	}

	// if quota gets released
	if workload.HasQuotaReservation(oldWl) && !workload.HasQuotaReservation(newWl) {
		c.trigger("event", "Quota released", "wl", klog.KObj(oldWl))
	}

	// is a preemption pending workload
	if state := workload.FindAdmissionCheck(newWl.Status.AdmissionChecks, constants.PreemptionAdmissionCheckName); state != nil && state.State == kueue.CheckStatePending {
		c.trigger("event", "Pending workload updated", "wl", klog.KObj(oldWl))
	}
}

func (c *Controller) NotifyAdmissionCheckUpdate(oldAc, newAc *kueue.AdmissionCheck) {
	// adding or removing a check will lead to a workload update later on
	// we should ignore those events here.
	if oldAc != nil && newAc != nil && ptr.Deref(oldAc.Spec.PreemptionPolicy, "") != ptr.Deref(newAc.Spec.PreemptionPolicy, "") {
		c.trigger("event", "AC policy changed", "ac", klog.KObj(oldAc))
	}
}

func (c *Controller) run() {
	c.log.V(2).Info("Start run")

	if !c.cache.ShouldRunPreemption() {
		// skip the snapshot creation
		return
	}

	snapshot := c.cache.Snapshot()
	workloads := filterWorkloads(&snapshot)
	for _, wl := range workloads {
		//1. remove it from Snapshot
		snapshot.RemoveWorkload(wl)
		//2. check if preemption is needed
		usage := totalRequestsForWorkload(wl)
		needPreemption := resourcesNeedingPreemption(wl, usage, &snapshot)
		log := c.log.WithValues("workload", klog.KObj(wl.Obj))
		if len(needPreemption) == 0 {
			// 2.1 - set the check to true
			// the preemption is done , flip the condition
			if err := c.updateFnc(c.ctx, c.client, wl.Obj, true); err != nil {
				log.V(2).Error(err, "Unable to update the check state to True")
				c.trigger("retryUpdate", klog.KObj(wl.Obj))
			} else {
				log.V(2).Info("Preemption ended")
			}
		} else {
			// 2.2 - issue eviction
			targets := c.preemptor.GetTargetsForResources(wl, needPreemption, usage, &snapshot)
			if len(targets) == 0 {
				//2.2.1 - preemption is no longer an option, flip the condition to false
				if err := c.updateFnc(c.ctx, c.client, wl.Obj, false); err != nil {
					log.V(2).Error(err, "Unable to update the check state to False")
					c.trigger("retryUpdate", klog.KObj(wl.Obj))
				} else {
					log.V(2).Info("Preemption is no longer possible")
				}
			} else {
				count, err := c.preemptor.IssuePreemptions(c.ctx, targets, snapshot.ClusterQueues[wl.ClusterQueue])
				if err != nil {
					log.V(2).Error(err, "Unable to issue preemption")
					c.trigger("retryEviction", klog.KObj(wl.Obj))
				} else {
					log.V(2).Info("Preemption triggered", "count", count)
				}
			}
		}
		// 3 add it back to the Snapshot
		snapshot.AddWorkload(wl)
	}
}

func filterWorkloads(snapsot *cache.Snapshot) []*workload.Info {
	preemptNow := []*workload.Info{}
	preemptLater := []*workload.Info{}
	for _, cq := range snapsot.ClusterQueues {
		now, later := cq.GetPreemptingWorklods(snapsot.AdmissionChecks)
		preemptNow = append(preemptNow, now...)
		preemptLater = append(preemptLater, later...)
	}

	// remove the workloads that should not evaluated now to limit the number of evictions
	// needed at the current point in time
	for _, wl := range preemptLater {
		snapsot.RemoveWorkload(wl)
	}
	return preemptNow
}

func totalRequestsForWorkload(wl *workload.Info) cache.FlavorResourceQuantities {
	usage := make(cache.FlavorResourceQuantities)
	for _, ps := range wl.TotalRequests {
		for res, q := range ps.Requests {
			flv := ps.Flavors[res]
			resUsage := usage[flv]
			if resUsage == nil {
				resUsage = make(map[corev1.ResourceName]int64)
				usage[flv] = resUsage
			}
			resUsage[res] += q
		}
	}
	return usage
}

func resourcesNeedingPreemption(wl *workload.Info, usage cache.FlavorResourceQuantities, snap *cache.Snapshot) preemption.ResourcesPerFlavor {
	ret := make(preemption.ResourcesPerFlavor)

	cq := snap.ClusterQueues[wl.ClusterQueue]
	for _, rg := range cq.ResourceGroups {
		for _, flvQuotas := range rg.Flavors {
			flvReq, found := usage[flvQuotas.Name]
			if !found {
				// Workload doesn't request this flavor.
				continue
			}
			cqResUsage := cq.Usage[flvQuotas.Name]
			var cohortResUsage, cohortResRequestable map[corev1.ResourceName]int64
			if cq.Cohort != nil {
				cohortResUsage = cq.Cohort.Usage[flvQuotas.Name]
				cohortResRequestable = cq.Cohort.RequestableResources[flvQuotas.Name]
			}
			for rName, rReq := range flvReq {
				limit := flvQuotas.Resources[rName].Nominal
				if flvQuotas.Resources[rName].BorrowingLimit != nil {
					limit += *flvQuotas.Resources[rName].BorrowingLimit
				}
				exceedsNominal := cqResUsage[rName]+rReq > limit
				exceedsBorrowing := cq.Cohort != nil && cohortResUsage[rName]+rReq > cohortResRequestable[rName]
				if exceedsNominal || exceedsBorrowing {
					if _, found := ret[flvQuotas.Name]; !found {
						ret[flvQuotas.Name] = sets.New(rName)
					} else {
						ret[flvQuotas.Name].Insert(rName)
					}
				}
			}
		}
	}
	return ret
}

func updateCheckState(ctx context.Context, c client.Client, wl *kueue.Workload, successful bool) error {
	state := kueue.AdmissionCheckState{
		Name:    constants.PreemptionAdmissionCheckName,
		State:   kueue.CheckStateReady,
		Message: "Preemption done",
	}

	if !successful {
		state.State = kueue.CheckStateRetry
		state.Message = "Preemption is not possible"
	}

	patch := workload.BaseSSAWorkload(wl)
	workload.SetAdmissionCheckState(&patch.Status.AdmissionChecks, state)
	return c.Status().Patch(ctx, patch, client.Apply, client.FieldOwner(controllerName), client.ForceOwnership)
}
