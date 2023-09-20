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

// Controller manages the preemption process of the workloads after quota reservation taking into account
// its admission checks states. It will issue eviction for other workloads, and mark the preemption as
// successful or not based on the current state of the resources pool. Check run() for more details.
// The controller will react to workload and admission check changes, check NotifyWorkloadUpdate()
// and NotifyAdmissionCheckUpdate() for details.
type Controller struct {
	log         logr.Logger
	cache       *cache.Cache
	client      client.Client
	recorder    record.EventRecorder
	preemptor   *preemption.Preemptor
	triggerChan chan struct{}

	// stub, only for test
	updateCheckStatus func(context.Context, client.Client, *kueue.Workload, bool) error
}

func NewController(cache *cache.Cache) *Controller {
	return &Controller{
		log:   ctrl.Log.WithName(controllerName),
		cache: cache,
		// keep this buffered so we can "requeue" in case of API errors
		triggerChan:       make(chan struct{}, 1),
		updateCheckStatus: updateCheckState,
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
	ctx = ctrl.LoggerInto(ctx, c.log)
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

		// If a run was triggered and at least throttle timeout has passed.
		if trigger && timeout {
			c.run(ctx)
			trigger = false
			timeout = false
		}
	}
}

// trigger - triggers a controller run if not already pending.
func (c *Controller) trigger(logValues ...interface{}) {
	select {
	case c.triggerChan <- struct{}{}:
		// The run was triggered.
		c.log.V(2).Info("Triggered", logValues...)
	default:
		// The channel is already full, a run in already pending.
	}

}

func (c *Controller) NotifyWorkloadUpdate(oldWl, newWl *kueue.Workload) {
	if oldWl == nil {
		// There is noting to on workloads creation.
		return
	}
	if newWl == nil {
		// Deleting a workload that reserves quota can speed up the
		// transition of preemption pending workloads.
		if workload.HasQuotaReservation(oldWl) {
			c.trigger("event", "Workload with reservation was deleted", "workload", klog.KObj(oldWl))
		}
		return
	}

	// If quota gets released.
	if workload.HasQuotaReservation(oldWl) && !workload.HasQuotaReservation(newWl) {
		c.trigger("event", "Quota released", "workload", klog.KObj(oldWl))
	}

	// Is a preemption pending workload.
	if state := workload.FindAdmissionCheck(newWl.Status.AdmissionChecks, constants.PreemptionAdmissionCheckName); state != nil && state.State == kueue.CheckStatePending {
		c.trigger("event", "Pending workload updated", "workload", klog.KObj(oldWl))
	}
}

func (c *Controller) NotifyAdmissionCheckUpdate(oldAc, newAc *kueue.AdmissionCheck) {
	// Adding or removing a check will lead to a workload update later on
	// we should ignore those events here.
	if oldAc != nil && newAc != nil && !ptr.Equal(oldAc.Spec.PreemptionPolicy, newAc.Spec.PreemptionPolicy) {
		c.trigger("event", "Admission check preemption policy changed", "admissionCheck", klog.KObj(oldAc))
	}
}

func (c *Controller) run(ctx context.Context) {
	c.log.V(2).Info("Start run")

	// If there is nothing to do at this point.
	if !c.cache.ShouldCheckWorkloadsPreemption() {
		// skip the snapshot creation and exit
		return
	}

	snapshot := c.cache.Snapshot()
	workloads := filterWorkloads(&snapshot)
	for _, wl := range workloads {
		// 1. remove the workload from the snapshot
		snapshot.RemoveWorkload(wl)
		usage := totalRequestsForWorkload(wl)
		needPreemption := resourcesNeedingPreemption(wl, usage, &snapshot)
		log := c.log.WithValues("workload", klog.KObj(wl.Obj))
		// 2. check if preemption is sill needed
		if len(needPreemption) == 0 {
			// No more resources need to be freed to accommodate this workload.
			// The preemption is successful.
			if err := c.updateCheckStatus(ctx, c.client, wl.Obj, true); err != nil {
				log.V(2).Error(err, "Unable to update the check state to True")
				c.trigger("retryUpdate", klog.KObj(wl.Obj))
			} else {
				log.V(2).Info("Preemption ended")
			}
		} else {
			// Additional resources need to be freed.
			targets := c.preemptor.GetTargetsForResources(wl, needPreemption, usage, &snapshot)
			if len(targets) == 0 {
				// There are no candidates for eviction, the preemption can no linger be done.
				// The preemption failed.
				if err := c.updateCheckStatus(ctx, c.client, wl.Obj, false); err != nil {
					log.V(2).Error(err, "Unable to update the check state to False")
					c.trigger("retryUpdate", klog.KObj(wl.Obj))
				} else {
					log.V(2).Info("Preemption is no longer possible")
				}
			} else {
				// Issue the evictions, the controller will run again once the evicted workloads are fully stopped.
				count, err := c.preemptor.IssuePreemptions(ctx, targets, snapshot.ClusterQueues[wl.ClusterQueue])
				if err != nil {
					log.V(2).Error(err, "Unable to issue preemption")
					c.trigger("retryEviction", klog.KObj(wl.Obj))
				} else {
					log.V(2).Info("Preemption triggered", "count", count)
				}
			}
		}
		// 3. add it back to the Snapshot
		snapshot.AddWorkload(wl)
	}
}

func filterWorkloads(snapshot *cache.Snapshot) []*workload.Info {
	preemptNow := []*workload.Info{}
	preemptLater := []*workload.Info{}
	for _, cq := range snapshot.ClusterQueues {
		now, later := cq.GetPreemptingWorkloads(snapshot.AdmissionChecks)
		preemptNow = append(preemptNow, now...)
		preemptLater = append(preemptLater, later...)
	}

	// remove the workloads that should not evaluated now to limit the number of evictions
	// needed at the current point in time
	for _, wl := range preemptLater {
		snapshot.RemoveWorkload(wl)
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
				resourceQuota := ptr.Deref(flvQuotas.Resources[rName], cache.ResourceQuota{})
				queueLimit := resourceQuota.Nominal + ptr.Deref(resourceQuota.BorrowingLimit, 0)

				exceedsQueueLimit := cqResUsage[rName]+rReq > queueLimit
				exceedsCohortLimit := cq.Cohort != nil && cohortResUsage[rName]+rReq > cohortResRequestable[rName]
				if exceedsQueueLimit || exceedsCohortLimit {
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
		Name: constants.PreemptionAdmissionCheckName,
	}

	if successful {
		state.State = kueue.CheckStateReady
		state.Message = "Preemption done"
	} else {
		state.State = kueue.CheckStateRetry
		state.Message = "Preemption is not possible"
	}

	patch := workload.BaseSSAWorkload(wl)
	workload.SetAdmissionCheckState(&patch.Status.AdmissionChecks, state)
	return c.Status().Patch(ctx, patch, client.Apply, client.FieldOwner(controllerName), client.ForceOwnership)
}
