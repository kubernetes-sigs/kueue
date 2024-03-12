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

package cache

import (
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/workload"
)

var (
	errQueueAlreadyExists = errors.New("queue already exists")
)

// ClusterQueue is the internal implementation of kueue.ClusterQueue that
// holds admitted workloads.
type ClusterQueue struct {
	Name              string
	Cohort            *Cohort
	ResourceGroups    []ResourceGroup
	RGByResource      map[corev1.ResourceName]*ResourceGroup
	Usage             FlavorResourceQuantities
	AdmittedUsage     FlavorResourceQuantities
	Workloads         map[string]*workload.Info
	WorkloadsNotReady sets.Set[string]
	NamespaceSelector labels.Selector
	Preemption        kueue.ClusterQueuePreemption
	FlavorFungibility kueue.FlavorFungibility
	AdmissionChecks   sets.Set[string]
	Status            metrics.ClusterQueueStatus
	// GuaranteedQuota records how much resource quota the ClusterQueue reserved
	// when feature LendingLimit is enabled and flavor's lendingLimit is not nil.
	GuaranteedQuota FlavorResourceQuantities
	// AllocatableResourceGeneration will be increased when some admitted workloads are
	// deleted, or the resource groups are changed.
	AllocatableResourceGeneration int64

	// The following fields are not populated in a snapshot.

	// Key is localQueue's key (namespace/name).
	localQueues                                map[string]*queue
	podsReadyTracking                          bool
	hasMissingFlavors                          bool
	hasMissingOrInactiveAdmissionChecks        bool
	hasMultipleSingleInstanceControllersChecks bool
	admittedWorkloadsCount                     int
	isStopped                                  bool
}

// Cohort is a set of ClusterQueues that can borrow resources from each other.
type Cohort struct {
	Name    string
	Members sets.Set[*ClusterQueue]

	// These fields are only populated for a snapshot. This field equals to
	// the sum of LendingLimit when feature LendingLimit enabled.
	RequestableResources FlavorResourceQuantities
	Usage                FlavorResourceQuantities
	// This field will only be set in snapshot. This field equals to
	// the sum of allocatable generation among its members.
	AllocatableResourceGeneration int64
}

type ResourceGroup struct {
	CoveredResources sets.Set[corev1.ResourceName]
	Flavors          []FlavorQuotas
	// The set of key labels from all flavors.
	// Those keys define the affinity terms of a workload
	// that can be matched against the flavors.
	LabelKeys sets.Set[string]
}

// FlavorQuotas holds a processed ClusterQueue flavor quota.
type FlavorQuotas struct {
	Name      kueue.ResourceFlavorReference
	Resources map[corev1.ResourceName]*ResourceQuota
}

type ResourceQuota struct {
	Nominal        int64
	BorrowingLimit *int64
	LendingLimit   *int64
}

type FlavorResourceQuantities map[kueue.ResourceFlavorReference]map[corev1.ResourceName]int64

type queue struct {
	key                string
	reservingWorkloads int
	admittedWorkloads  int
	//TODO: rename this to better distinguish between reserved and "in use" quantities
	usage         FlavorResourceQuantities
	admittedUsage FlavorResourceQuantities
}

func newCohort(name string, size int) *Cohort {
	return &Cohort{
		Name:    name,
		Members: make(sets.Set[*ClusterQueue], size),
	}
}

func (c *ClusterQueue) FitInCohort(q FlavorResourceQuantities) bool {
	for flavor, qResources := range q {
		if _, flavorFound := c.Cohort.RequestableResources[flavor]; flavorFound {
			for resource, value := range qResources {
				available := c.RequestableCohortQuota(flavor, resource) - c.UsedCohortQuota(flavor, resource)
				if available < value {
					return false
				}
			}
		} else {
			return false
		}
	}
	return true
}

func (c *ClusterQueue) IsBorrowing() bool {
	if c.Cohort == nil || len(c.Usage) == 0 {
		return false
	}
	for _, rg := range c.ResourceGroups {
		for _, flvQuotas := range rg.Flavors {
			if flvUsage, isUsing := c.Usage[flvQuotas.Name]; isUsing {
				for rName, rQuota := range flvQuotas.Resources {
					used := flvUsage[rName]
					if used > rQuota.Nominal {
						return true
					}
				}
			}
		}
	}
	return false
}

func (c *ClusterQueue) Active() bool {
	return c.Status == active
}

var defaultPreemption = kueue.ClusterQueuePreemption{
	ReclaimWithinCohort: kueue.PreemptionPolicyNever,
	WithinClusterQueue:  kueue.PreemptionPolicyNever,
}

var defaultFlavorFungibility = kueue.FlavorFungibility{WhenCanBorrow: kueue.Borrow, WhenCanPreempt: kueue.TryNextFlavor}

func (c *ClusterQueue) update(in *kueue.ClusterQueue, resourceFlavors map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor, admissionChecks map[string]AdmissionCheck) error {
	c.updateResourceGroups(in.Spec.ResourceGroups)
	nsSelector, err := metav1.LabelSelectorAsSelector(in.Spec.NamespaceSelector)
	if err != nil {
		return err
	}
	c.NamespaceSelector = nsSelector

	c.isStopped = ptr.Deref(in.Spec.StopPolicy, kueue.None) != kueue.None

	c.AdmissionChecks = sets.New(in.Spec.AdmissionChecks...)

	c.Usage = filterQuantities(c.Usage, in.Spec.ResourceGroups)
	c.AdmittedUsage = filterQuantities(c.AdmittedUsage, in.Spec.ResourceGroups)
	c.UpdateWithFlavors(resourceFlavors)
	c.updateWithAdmissionChecks(admissionChecks)

	if in.Spec.Preemption != nil {
		c.Preemption = *in.Spec.Preemption
	} else {
		c.Preemption = defaultPreemption
	}

	if in.Spec.FlavorFungibility != nil {
		c.FlavorFungibility = *in.Spec.FlavorFungibility
		if c.FlavorFungibility.WhenCanBorrow == "" {
			c.FlavorFungibility.WhenCanBorrow = defaultFlavorFungibility.WhenCanBorrow
		}
		if c.FlavorFungibility.WhenCanPreempt == "" {
			c.FlavorFungibility.WhenCanPreempt = defaultFlavorFungibility.WhenCanPreempt
		}
	} else {
		c.FlavorFungibility = defaultFlavorFungibility
	}

	if features.Enabled(features.LendingLimit) {
		var guaranteedQuota FlavorResourceQuantities
		for _, rg := range c.ResourceGroups {
			for _, flvQuotas := range rg.Flavors {
				for rName, rQuota := range flvQuotas.Resources {
					if rQuota.LendingLimit != nil {
						if guaranteedQuota == nil {
							guaranteedQuota = make(FlavorResourceQuantities)
						}
						if guaranteedQuota[flvQuotas.Name] == nil {
							guaranteedQuota[flvQuotas.Name] = make(map[corev1.ResourceName]int64)
						}
						guaranteedQuota[flvQuotas.Name][rName] = rQuota.Nominal - *rQuota.LendingLimit
					}
				}
			}
		}
		c.GuaranteedQuota = guaranteedQuota
	}

	return nil
}

func filterQuantities(orig FlavorResourceQuantities, resourceGroups []kueue.ResourceGroup) FlavorResourceQuantities {
	ret := make(FlavorResourceQuantities)
	for _, rg := range resourceGroups {
		for _, f := range rg.Flavors {
			existingUsedResources := orig[f.Name]
			usedResources := make(map[corev1.ResourceName]int64, len(f.Resources))
			for _, r := range f.Resources {
				usedResources[r.Name] = existingUsedResources[r.Name]
			}
			ret[f.Name] = usedResources
		}
	}
	return ret
}

func (c *ClusterQueue) updateResourceGroups(in []kueue.ResourceGroup) {
	oldRG := c.ResourceGroups
	c.ResourceGroups = make([]ResourceGroup, len(in))
	for i, rgIn := range in {
		rg := &c.ResourceGroups[i]
		*rg = ResourceGroup{
			CoveredResources: sets.New(rgIn.CoveredResources...),
			Flavors:          make([]FlavorQuotas, 0, len(rgIn.Flavors)),
		}
		for i := range rgIn.Flavors {
			fIn := &rgIn.Flavors[i]
			fQuotas := FlavorQuotas{
				Name:      fIn.Name,
				Resources: make(map[corev1.ResourceName]*ResourceQuota, len(fIn.Resources)),
			}
			for _, rIn := range fIn.Resources {
				rQuota := ResourceQuota{
					Nominal: workload.ResourceValue(rIn.Name, rIn.NominalQuota),
				}
				if rIn.BorrowingLimit != nil {
					rQuota.BorrowingLimit = ptr.To(workload.ResourceValue(rIn.Name, *rIn.BorrowingLimit))
				}
				if features.Enabled(features.LendingLimit) && rIn.LendingLimit != nil {
					rQuota.LendingLimit = ptr.To(workload.ResourceValue(rIn.Name, *rIn.LendingLimit))
				}
				fQuotas.Resources[rIn.Name] = &rQuota
			}
			rg.Flavors = append(rg.Flavors, fQuotas)
		}
	}
	// Start at 1, for backwards compatibility.
	if c.AllocatableResourceGeneration == 0 || !equality.Semantic.DeepEqual(oldRG, c.ResourceGroups) {
		c.AllocatableResourceGeneration++
	}
	c.UpdateRGByResource()
}

func (c *ClusterQueue) UpdateRGByResource() {
	c.RGByResource = make(map[corev1.ResourceName]*ResourceGroup)
	for i := range c.ResourceGroups {
		rg := &c.ResourceGroups[i]
		for rName := range rg.CoveredResources {
			c.RGByResource[rName] = rg
		}
	}
}

func (c *ClusterQueue) updateQueueStatus() {
	status := active
	if c.hasMissingFlavors || c.hasMissingOrInactiveAdmissionChecks || c.isStopped || c.hasMultipleSingleInstanceControllersChecks {
		status = pending
	}
	if c.Status == terminating {
		status = terminating
	}
	if status != c.Status {
		c.Status = status
		metrics.ReportClusterQueueStatus(c.Name, c.Status)
	}
}

func (c *ClusterQueue) inactiveReason() (string, string) {
	switch c.Status {
	case terminating:
		return "Terminating", "Can't admit new workloads; clusterQueue is terminating"
	case pending:
		reasons := make([]string, 0, 3)
		if c.isStopped {
			reasons = append(reasons, "Stopped")
		}
		if c.hasMissingFlavors {
			reasons = append(reasons, "FlavorNotFound")
		}
		if c.hasMissingOrInactiveAdmissionChecks {
			reasons = append(reasons, "CheckNotFoundOrInactive")
		}

		if c.hasMultipleSingleInstanceControllersChecks {
			reasons = append(reasons, "MultipleSingleInstanceControllerChecks")
		}

		if len(reasons) == 0 {
			return "Unknown", "Can't admit new workloads."
		}

		return reasons[0], strings.Join([]string{"Can't admit new workloads:", strings.Join(reasons, ", ")}, " ")
	}
	return "Ready", "Can admit new flavors"
}

// UpdateWithFlavors updates a ClusterQueue based on the passed ResourceFlavors set.
// Exported only for testing.
func (c *ClusterQueue) UpdateWithFlavors(flavors map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor) {
	c.hasMissingFlavors = c.updateLabelKeys(flavors)
	c.updateQueueStatus()
}

func (c *ClusterQueue) updateLabelKeys(flavors map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor) bool {
	var flavorNotFound bool
	for i := range c.ResourceGroups {
		rg := &c.ResourceGroups[i]
		if len(rg.Flavors) == 0 {
			rg.LabelKeys = nil
			continue
		}
		keys := sets.New[string]()
		for _, rf := range rg.Flavors {
			if flv, exist := flavors[rf.Name]; exist {
				for k := range flv.Spec.NodeLabels {
					keys.Insert(k)
				}
			} else {
				flavorNotFound = true
			}
		}

		if keys.Len() > 0 {
			rg.LabelKeys = keys
		}
	}

	return flavorNotFound
}

// updateWithAdmissionChecks updates a ClusterQueue based on the passed AdmissionChecks set.
func (c *ClusterQueue) updateWithAdmissionChecks(checks map[string]AdmissionCheck) {
	hasMissing := false
	checksPerController := make(map[string]int, len(c.AdmissionChecks))
	singleInstanceControllers := sets.New[string]()
	for acName := range c.AdmissionChecks {
		if ac, found := checks[acName]; !found {
			hasMissing = true
		} else {
			if !ac.Active {
				hasMissing = true
			}
			checksPerController[ac.Controller]++
			if ac.SingleInstanceInClusterQueue {
				singleInstanceControllers.Insert(ac.Controller)
			}
		}
	}

	update := false
	if hasMissing != c.hasMissingOrInactiveAdmissionChecks {
		c.hasMissingOrInactiveAdmissionChecks = hasMissing
		update = true
	}

	hasMultipleSICC := false
	for controller, checks := range checksPerController {
		if singleInstanceControllers.Has(controller) && checks > 1 {
			hasMultipleSICC = true
		}
	}

	if c.hasMultipleSingleInstanceControllersChecks != hasMultipleSICC {
		c.hasMultipleSingleInstanceControllersChecks = hasMultipleSICC
		update = true
	}

	if update {
		c.updateQueueStatus()
	}
}

func (c *ClusterQueue) addWorkload(w *kueue.Workload) error {
	k := workload.Key(w)
	if _, exist := c.Workloads[k]; exist {
		return fmt.Errorf("workload already exists in ClusterQueue")
	}
	wi := workload.NewInfo(w)
	c.Workloads[k] = wi
	c.updateWorkloadUsage(wi, 1)
	if c.podsReadyTracking && !apimeta.IsStatusConditionTrue(w.Status.Conditions, kueue.WorkloadPodsReady) {
		c.WorkloadsNotReady.Insert(k)
	}
	c.reportActiveWorkloads()
	return nil
}

func (c *ClusterQueue) deleteWorkload(w *kueue.Workload) {
	k := workload.Key(w)
	wi, exist := c.Workloads[k]
	if !exist {
		return
	}
	c.updateWorkloadUsage(wi, -1)
	if c.podsReadyTracking && !apimeta.IsStatusConditionTrue(w.Status.Conditions, kueue.WorkloadPodsReady) {
		c.WorkloadsNotReady.Delete(k)
	}
	// we only increase the AllocatableResourceGeneration cause the add of workload won't make more
	// workloads fit in ClusterQueue.
	c.AllocatableResourceGeneration++

	delete(c.Workloads, k)
	c.reportActiveWorkloads()
}

func (c *ClusterQueue) reportActiveWorkloads() {
	metrics.AdmittedActiveWorkloads.WithLabelValues(c.Name).Set(float64(c.admittedWorkloadsCount))
	metrics.ReservingActiveWorkloads.WithLabelValues(c.Name).Set(float64(len(c.Workloads)))
}

// updateWorkloadUsage updates the usage of the ClusterQueue for the workload
// and the number of admitted workloads for local queues.
func (c *ClusterQueue) updateWorkloadUsage(wi *workload.Info, m int64) {
	admitted := workload.IsAdmitted(wi.Obj)
	updateUsage(wi, c.Usage, m)
	if admitted {
		updateUsage(wi, c.AdmittedUsage, m)
		c.admittedWorkloadsCount += int(m)
	}
	qKey := workload.QueueKey(wi.Obj)
	if lq, ok := c.localQueues[qKey]; ok {
		updateUsage(wi, lq.usage, m)
		lq.reservingWorkloads += int(m)
		if admitted {
			updateUsage(wi, lq.admittedUsage, m)
			lq.admittedWorkloads += int(m)
		}
	}
}

func updateUsage(wi *workload.Info, flvUsage FlavorResourceQuantities, m int64) {
	for _, ps := range wi.TotalRequests {
		for wlRes, wlResFlv := range ps.Flavors {
			v, wlResExist := ps.Requests[wlRes]
			flv, flvExist := flvUsage[wlResFlv]
			if flvExist && wlResExist {
				if _, exists := flv[wlRes]; exists {
					flv[wlRes] += v * m
				}
			}
		}
	}
}

func updateCohortUsage(wi *workload.Info, cq *ClusterQueue, m int64) {
	for _, ps := range wi.TotalRequests {
		for wlRes, wlResFlv := range ps.Flavors {
			v, wlResExist := ps.Requests[wlRes]
			flv, flvExist := cq.Cohort.Usage[wlResFlv]
			if flvExist && wlResExist {
				if _, exists := flv[wlRes]; exists {
					after := cq.Usage[wlResFlv][wlRes] - cq.guaranteedQuota(wlResFlv, wlRes)
					// rollback update cq.Usage
					before := after - v*m
					if before > 0 {
						flv[wlRes] -= before
					}
					// simulate updating cq.Usage
					if after > 0 {
						flv[wlRes] += after
					}
				}
			}
		}
	}
}

func (c *ClusterQueue) addLocalQueue(q *kueue.LocalQueue) error {
	qKey := queueKey(q)
	if _, ok := c.localQueues[qKey]; ok {
		return errQueueAlreadyExists
	}
	// We need to count the workloads, because they could have been added before
	// receiving the queue add event.
	qImpl := &queue{
		key:                qKey,
		reservingWorkloads: 0,
		usage:              make(FlavorResourceQuantities),
	}
	if err := qImpl.resetFlavorsAndResources(c.Usage, c.AdmittedUsage); err != nil {
		return err
	}
	for _, wl := range c.Workloads {
		if workloadBelongsToLocalQueue(wl.Obj, q) {
			updateUsage(wl, qImpl.usage, 1)
			qImpl.reservingWorkloads++
			if workload.IsAdmitted(wl.Obj) {
				updateUsage(wl, qImpl.admittedUsage, 1)
				qImpl.admittedWorkloads++
			}
		}
	}
	c.localQueues[qKey] = qImpl
	return nil
}

func (c *ClusterQueue) deleteLocalQueue(q *kueue.LocalQueue) {
	qKey := queueKey(q)
	delete(c.localQueues, qKey)
}

func (c *ClusterQueue) flavorInUse(flavor string) bool {
	for _, rg := range c.ResourceGroups {
		for _, f := range rg.Flavors {
			if kueue.ResourceFlavorReference(flavor) == f.Name {
				return true
			}
		}
	}
	return false
}

func (q *queue) resetFlavorsAndResources(cqUsage FlavorResourceQuantities, cqAdmittedUsage FlavorResourceQuantities) error {
	// Clean up removed flavors or resources.
	q.usage = resetUsage(q.usage, cqUsage)
	q.admittedUsage = resetUsage(q.admittedUsage, cqAdmittedUsage)
	return nil
}

func resetUsage(lqUsage FlavorResourceQuantities, cqUsage FlavorResourceQuantities) FlavorResourceQuantities {
	usedFlavorResources := make(FlavorResourceQuantities)
	for cqFlv, cqRes := range cqUsage {
		existingUsedResources := lqUsage[cqFlv]
		usedResources := make(map[corev1.ResourceName]int64, len(cqRes))
		for rName := range cqRes {
			usedResources[rName] = existingUsedResources[rName]
		}
		usedFlavorResources[cqFlv] = usedResources
	}
	return usedFlavorResources
}

func workloadBelongsToLocalQueue(wl *kueue.Workload, q *kueue.LocalQueue) bool {
	return wl.Namespace == q.Namespace && wl.Spec.QueueName == q.Name
}

// RequestableCohortQuota returns the total available quota by the flavor and resource name in the cohort.
// LendingLimit will also be counted here if feature LendingLimit enabled.
// Please note that for different clusterQueues, the requestable quota is different,
// they should be calculated dynamically.
func (c *ClusterQueue) RequestableCohortQuota(fName kueue.ResourceFlavorReference, rName corev1.ResourceName) (val int64) {
	if c.Cohort.RequestableResources == nil || c.Cohort.RequestableResources[fName] == nil {
		return 0
	}
	requestableCohortQuota := c.Cohort.RequestableResources[fName][rName]

	// When feature LendingLimit enabled, cohort.requestableResource accumulated the lendingLimit if not null
	// rather than the flavor's quota, then the total available quota should include its own guaranteed resources.
	requestableCohortQuota += c.guaranteedQuota(fName, rName)

	return requestableCohortQuota
}

func (c *ClusterQueue) guaranteedQuota(fName kueue.ResourceFlavorReference, rName corev1.ResourceName) (val int64) {
	if !features.Enabled(features.LendingLimit) {
		return 0
	}
	if c.GuaranteedQuota == nil || c.GuaranteedQuota[fName] == nil {
		return 0
	}
	return c.GuaranteedQuota[fName][rName]
}

// UsedCohortQuota returns the used quota by the flavor and resource name in the cohort.
// Note that when LendingLimit enabled, the usage is not equal to the total used quota but the one
// minus the guaranteed resources, this is only for judging whether workloads fit in the cohort.
func (c *ClusterQueue) UsedCohortQuota(fName kueue.ResourceFlavorReference, rName corev1.ResourceName) (val int64) {
	if c.Cohort.Usage == nil || c.Cohort.Usage[fName] == nil {
		return 0
	}

	cohortUsage := c.Cohort.Usage[fName][rName]

	// When feature LendingLimit enabled, cohortUsage is the sum of usage in LendingLimit.
	// If cqUsage < c.guaranteedQuota, it means the cq is not using all its guaranteedQuota,
	// need to count the cqUsage in, otherwise need to count the guaranteedQuota in.
	if features.Enabled(features.LendingLimit) {
		cqUsage := c.Usage[fName][rName]
		if cqUsage < c.guaranteedQuota(fName, rName) {
			cohortUsage += cqUsage
		} else {
			cohortUsage += c.guaranteedQuota(fName, rName)
		}
	}

	return cohortUsage
}
