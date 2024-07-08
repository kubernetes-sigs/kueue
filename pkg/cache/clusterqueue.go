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
	"math"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/resources"
	utilac "sigs.k8s.io/kueue/pkg/util/admissioncheck"
	"sigs.k8s.io/kueue/pkg/workload"
)

var (
	errQueueAlreadyExists = errors.New("queue already exists")
	oneQuantity           = resource.MustParse("1")
)

// clusterQueue is the internal implementation of kueue.clusterQueue that
// holds admitted workloads.
type clusterQueue struct {
	Name              string
	Cohort            *cohort
	ResourceGroups    []ResourceGroup
	Usage             resources.FlavorResourceQuantities
	Workloads         map[string]*workload.Info
	WorkloadsNotReady sets.Set[string]
	NamespaceSelector labels.Selector
	Preemption        kueue.ClusterQueuePreemption
	FairWeight        resource.Quantity
	FlavorFungibility kueue.FlavorFungibility
	// Aggregates AdmissionChecks from both .spec.AdmissionChecks and .spec.AdmissionCheckStrategy
	// Sets hold ResourceFlavors to which an AdmissionCheck should apply.
	// In case its empty, it means an AdmissionCheck should apply to all ResourceFlavor
	AdmissionChecks map[string]sets.Set[kueue.ResourceFlavorReference]
	Status          metrics.ClusterQueueStatus
	// GuaranteedQuota records how much resource quota the ClusterQueue reserved
	// when feature LendingLimit is enabled and flavor's lendingLimit is not nil.
	GuaranteedQuota resources.FlavorResourceQuantities
	// AllocatableResourceGeneration will be increased when some admitted workloads are
	// deleted, or the resource groups are changed.
	AllocatableResourceGeneration int64

	// Lendable holds the total lendable quota for the resources of the ClusterQueue, independent of the flavor.
	Lendable map[corev1.ResourceName]int64

	AdmittedUsage resources.FlavorResourceQuantities
	// localQueues by (namespace/name).
	localQueues                                        map[string]*queue
	podsReadyTracking                                  bool
	hasMissingFlavors                                  bool
	hasMissingOrInactiveAdmissionChecks                bool
	hasMultipleSingleInstanceControllersChecks         bool
	hasFlavorIndependentAdmissionCheckAppliedPerFlavor bool
	admittedWorkloadsCount                             int
	isStopped                                          bool
	workloadInfoOptions                                []workload.InfoOption
}

// cohort is a set of ClusterQueues that can borrow resources from each other.
type cohort struct {
	Name    string
	Members sets.Set[*clusterQueue]
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

type queue struct {
	key                string
	reservingWorkloads int
	admittedWorkloads  int
	//TODO: rename this to better distinguish between reserved and "in use" quantities
	usage         resources.FlavorResourceQuantities
	admittedUsage resources.FlavorResourceQuantities
}

func newCohort(name string, size int) *cohort {
	return &cohort{
		Name:    name,
		Members: make(sets.Set[*clusterQueue], size),
	}
}

func (c *cohort) CalculateLendable() map[corev1.ResourceName]int64 {
	lendable := make(map[corev1.ResourceName]int64)
	for member := range c.Members {
		for res, v := range member.Lendable {
			lendable[res] += v
		}
	}
	return lendable
}

func (c *ClusterQueueSnapshot) FitInCohort(q resources.FlavorResourceQuantities) bool {
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

func (c *clusterQueue) Active() bool {
	return c.Status == active
}

var defaultPreemption = kueue.ClusterQueuePreemption{
	ReclaimWithinCohort: kueue.PreemptionPolicyNever,
	WithinClusterQueue:  kueue.PreemptionPolicyNever,
}

var defaultFlavorFungibility = kueue.FlavorFungibility{WhenCanBorrow: kueue.Borrow, WhenCanPreempt: kueue.TryNextFlavor}

func (c *clusterQueue) update(in *kueue.ClusterQueue, resourceFlavors map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor, admissionChecks map[string]AdmissionCheck) error {
	c.updateResourceGroups(in.Spec.ResourceGroups)
	nsSelector, err := metav1.LabelSelectorAsSelector(in.Spec.NamespaceSelector)
	if err != nil {
		return err
	}
	c.NamespaceSelector = nsSelector

	c.isStopped = ptr.Deref(in.Spec.StopPolicy, kueue.None) != kueue.None

	c.AdmissionChecks = utilac.NewAdmissionChecks(in)

	c.Usage = filterFlavorQuantities(c.Usage, in.Spec.ResourceGroups)
	c.AdmittedUsage = filterFlavorQuantities(c.AdmittedUsage, in.Spec.ResourceGroups)
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

	c.FairWeight = oneQuantity
	if fs := in.Spec.FairSharing; fs != nil && fs.Weight != nil {
		c.FairWeight = *fs.Weight
	}

	if features.Enabled(features.LendingLimit) {
		var guaranteedQuota resources.FlavorResourceQuantities
		for _, rg := range c.ResourceGroups {
			for _, flvQuotas := range rg.Flavors {
				for rName, rQuota := range flvQuotas.Resources {
					if rQuota.LendingLimit != nil {
						if guaranteedQuota == nil {
							guaranteedQuota = make(resources.FlavorResourceQuantities)
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

func filterFlavorQuantities(orig resources.FlavorResourceQuantities, resourceGroups []kueue.ResourceGroup) resources.FlavorResourceQuantities {
	ret := make(resources.FlavorResourceQuantities)
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

func (c *clusterQueue) updateResourceGroups(in []kueue.ResourceGroup) {
	oldRG := c.ResourceGroups
	c.ResourceGroups = make([]ResourceGroup, len(in))
	c.Lendable = make(map[corev1.ResourceName]int64)
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
				nominal := workload.ResourceValue(rIn.Name, rIn.NominalQuota)
				rQuota := ResourceQuota{
					Nominal: nominal,
				}
				if rIn.BorrowingLimit != nil {
					rQuota.BorrowingLimit = ptr.To(workload.ResourceValue(rIn.Name, *rIn.BorrowingLimit))
				}
				if features.Enabled(features.LendingLimit) && rIn.LendingLimit != nil {
					rQuota.LendingLimit = ptr.To(workload.ResourceValue(rIn.Name, *rIn.LendingLimit))
					c.Lendable[rIn.Name] += *rQuota.LendingLimit
				} else {
					c.Lendable[rIn.Name] += nominal
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
}

func (c *clusterQueue) updateQueueStatus() {
	status := active
	if c.hasMissingFlavors || c.hasMissingOrInactiveAdmissionChecks || c.isStopped || c.hasMultipleSingleInstanceControllersChecks || c.hasFlavorIndependentAdmissionCheckAppliedPerFlavor {
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

func (c *clusterQueue) inactiveReason() (string, string) {
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

		if c.hasFlavorIndependentAdmissionCheckAppliedPerFlavor {
			reasons = append(reasons, "FlavorIndependentAdmissionCheckAppliedPerFlavor")
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
func (c *clusterQueue) UpdateWithFlavors(flavors map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor) {
	c.hasMissingFlavors = c.updateLabelKeys(flavors)
	c.updateQueueStatus()
}

func (c *clusterQueue) updateLabelKeys(flavors map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor) bool {
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
func (c *clusterQueue) updateWithAdmissionChecks(checks map[string]AdmissionCheck) {
	hasMissing := false
	hasSpecificChecks := false
	checksPerController := make(map[string]int, len(c.AdmissionChecks))
	singleInstanceControllers := sets.New[string]()
	for acName, flavors := range c.AdmissionChecks {
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
			if ac.FlavorIndependent && flavors.Len() != 0 {
				hasSpecificChecks = true
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

	if c.hasFlavorIndependentAdmissionCheckAppliedPerFlavor != hasSpecificChecks {
		c.hasFlavorIndependentAdmissionCheckAppliedPerFlavor = hasSpecificChecks
		update = true
	}

	if update {
		c.updateQueueStatus()
	}
}

func (c *clusterQueue) addWorkload(w *kueue.Workload) error {
	k := workload.Key(w)
	if _, exist := c.Workloads[k]; exist {
		return errors.New("workload already exists in ClusterQueue")
	}
	wi := workload.NewInfo(w, c.workloadInfoOptions...)
	c.Workloads[k] = wi
	c.updateWorkloadUsage(wi, 1)
	if c.podsReadyTracking && !apimeta.IsStatusConditionTrue(w.Status.Conditions, kueue.WorkloadPodsReady) {
		c.WorkloadsNotReady.Insert(k)
	}
	c.reportActiveWorkloads()
	return nil
}

func (c *clusterQueue) deleteWorkload(w *kueue.Workload) {
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

func (c *clusterQueue) reportActiveWorkloads() {
	metrics.AdmittedActiveWorkloads.WithLabelValues(c.Name).Set(float64(c.admittedWorkloadsCount))
	metrics.ReservingActiveWorkloads.WithLabelValues(c.Name).Set(float64(len(c.Workloads)))
}

// updateWorkloadUsage updates the usage of the ClusterQueue for the workload
// and the number of admitted workloads for local queues.
func (c *clusterQueue) updateWorkloadUsage(wi *workload.Info, m int64) {
	admitted := workload.IsAdmitted(wi.Obj)
	updateFlavorUsage(wi, c.Usage, m)
	if admitted {
		updateFlavorUsage(wi, c.AdmittedUsage, m)
		c.admittedWorkloadsCount += int(m)
	}
	qKey := workload.QueueKey(wi.Obj)
	if lq, ok := c.localQueues[qKey]; ok {
		updateFlavorUsage(wi, lq.usage, m)
		lq.reservingWorkloads += int(m)
		if admitted {
			updateFlavorUsage(wi, lq.admittedUsage, m)
			lq.admittedWorkloads += int(m)
		}
	}
}

func updateFlavorUsage(wi *workload.Info, flvUsage resources.FlavorResourceQuantities, m int64) {
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

func updateCohortUsage(wi *workload.Info, cq *ClusterQueueSnapshot, m int64) {
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

func (c *clusterQueue) addLocalQueue(q *kueue.LocalQueue) error {
	qKey := queueKey(q)
	if _, ok := c.localQueues[qKey]; ok {
		return errQueueAlreadyExists
	}
	// We need to count the workloads, because they could have been added before
	// receiving the queue add event.
	qImpl := &queue{
		key:                qKey,
		reservingWorkloads: 0,
		usage:              make(resources.FlavorResourceQuantities),
	}
	if err := qImpl.resetFlavorsAndResources(c.Usage, c.AdmittedUsage); err != nil {
		return err
	}
	for _, wl := range c.Workloads {
		if workloadBelongsToLocalQueue(wl.Obj, q) {
			updateFlavorUsage(wl, qImpl.usage, 1)
			qImpl.reservingWorkloads++
			if workload.IsAdmitted(wl.Obj) {
				updateFlavorUsage(wl, qImpl.admittedUsage, 1)
				qImpl.admittedWorkloads++
			}
		}
	}
	c.localQueues[qKey] = qImpl
	return nil
}

func (c *clusterQueue) deleteLocalQueue(q *kueue.LocalQueue) {
	qKey := queueKey(q)
	delete(c.localQueues, qKey)
}

func (c *clusterQueue) flavorInUse(flavor string) bool {
	for _, rg := range c.ResourceGroups {
		for _, f := range rg.Flavors {
			if kueue.ResourceFlavorReference(flavor) == f.Name {
				return true
			}
		}
	}
	return false
}

func (q *queue) resetFlavorsAndResources(cqUsage resources.FlavorResourceQuantities, cqAdmittedUsage resources.FlavorResourceQuantities) error {
	// Clean up removed flavors or resources.
	q.usage = resetUsage(q.usage, cqUsage)
	q.admittedUsage = resetUsage(q.admittedUsage, cqAdmittedUsage)
	return nil
}

func resetUsage(lqUsage resources.FlavorResourceQuantities, cqUsage resources.FlavorResourceQuantities) resources.FlavorResourceQuantities {
	usedFlavorResources := make(resources.FlavorResourceQuantities)
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
func (c *ClusterQueueSnapshot) RequestableCohortQuota(fName kueue.ResourceFlavorReference, rName corev1.ResourceName) (val int64) {
	if c.Cohort.RequestableResources == nil || c.Cohort.RequestableResources[fName] == nil {
		return 0
	}
	requestableCohortQuota := c.Cohort.RequestableResources[fName][rName]

	// When feature LendingLimit enabled, cohort.requestableResource accumulated the lendingLimit if not null
	// rather than the flavor's quota, then the total available quota should include its own guaranteed resources.
	requestableCohortQuota += c.guaranteedQuota(fName, rName)

	return requestableCohortQuota
}

func (c *ClusterQueueSnapshot) guaranteedQuota(fName kueue.ResourceFlavorReference, rName corev1.ResourceName) (val int64) {
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
func (c *ClusterQueueSnapshot) UsedCohortQuota(fName kueue.ResourceFlavorReference, rName corev1.ResourceName) (val int64) {
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

// The methods below implement several interfaces. See
// dominantResourceShareNode, resourceGroupNode, and netQuotaNode.

func (c *clusterQueue) hasCohort() bool {
	return c.Cohort != nil
}

func (c *clusterQueue) fairWeight() *resource.Quantity {
	return &c.FairWeight
}

func (c *clusterQueue) lendableResourcesInCohort() map[corev1.ResourceName]int64 {
	return c.Cohort.CalculateLendable()
}

func (c *clusterQueue) usageFor(fr resources.FlavorResource) int64 {
	return c.Usage.For(fr)
}

func (c *clusterQueue) resourceGroups() []ResourceGroup {
	return c.ResourceGroups
}

// DominantResourceShare returns a value from 0 to 1,000,000 representing the maximum of the ratios
// of usage above nominal quota to the lendable resources in the cohort, among all the resources
// provided by the ClusterQueue, and divided by the weight.
// If zero, it means that the usage of the ClusterQueue is below the nominal quota.
// The function also returns the resource name that yielded this value.
// Also for a weight of zero, this will return 9223372036854775807.
func (c *ClusterQueueSnapshot) DominantResourceShare() (int, corev1.ResourceName) {
	return dominantResourceShare(c, nil, 0)
}

func (c *ClusterQueueSnapshot) DominantResourceShareWith(wlReq resources.FlavorResourceQuantities) (int, corev1.ResourceName) {
	return dominantResourceShare(c, wlReq, 1)
}

func (c *ClusterQueueSnapshot) DominantResourceShareWithout(w *workload.Info) (int, corev1.ResourceName) {
	return dominantResourceShare(c, w.FlavorResourceUsage(), -1)
}

type dominantResourceShareNode interface {
	hasCohort() bool
	fairWeight() *resource.Quantity
	lendableResourcesInCohort() map[corev1.ResourceName]int64

	netQuotaNode
}

func dominantResourceShare(node dominantResourceShareNode, wlReq resources.FlavorResourceQuantities, m int64) (int, corev1.ResourceName) {
	if !node.hasCohort() {
		return 0, ""
	}
	if node.fairWeight().IsZero() {
		return math.MaxInt, ""
	}

	borrowing := make(map[corev1.ResourceName]int64)
	for fr, quota := range remainingQuota(node) {
		b := m*wlReq[fr.Flavor][fr.Resource] - quota
		if b > 0 {
			borrowing[fr.Resource] += b
		}
	}
	if len(borrowing) == 0 {
		return 0, ""
	}

	var drs int64 = -1
	var dRes corev1.ResourceName

	lendable := node.lendableResourcesInCohort()
	for rName, b := range borrowing {
		if lr := lendable[rName]; lr > 0 {
			ratio := b * 1000 / lr
			// Use alphabetical order to get a deterministic resource name.
			if ratio > drs || (ratio == drs && rName < dRes) {
				drs = ratio
				dRes = rName
			}
		}
	}
	dws := drs * 1000 / node.fairWeight().MilliValue()
	return int(dws), dRes
}
