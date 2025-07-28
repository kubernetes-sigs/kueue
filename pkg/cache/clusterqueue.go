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

package cache

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/equality"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/hierarchy"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	"sigs.k8s.io/kueue/pkg/util/api"
	"sigs.k8s.io/kueue/pkg/util/queue"
	stringsutils "sigs.k8s.io/kueue/pkg/util/strings"
	"sigs.k8s.io/kueue/pkg/workload"
)

var (
	errQueueAlreadyExists = errors.New("queue already exists")
)

// clusterQueue is the internal implementation of kueue.clusterQueue that
// holds admitted workloads.
type clusterQueue struct {
	Name              kueue.ClusterQueueReference
	ResourceGroups    []ResourceGroup
	Workloads         map[workload.Reference]*workload.Info
	WorkloadsNotReady sets.Set[workload.Reference]
	NamespaceSelector labels.Selector
	Preemption        kueue.ClusterQueuePreemption
	FairWeight        resource.Quantity
	FlavorFungibility kueue.FlavorFungibility
	// Aggregates AdmissionChecks from both .spec.AdmissionChecks and .spec.AdmissionCheckStrategy
	// Sets hold ResourceFlavors to which an AdmissionCheck should apply.
	// In case its empty, it means an AdmissionCheck should apply to all ResourceFlavor
	AdmissionChecks map[kueue.AdmissionCheckReference]sets.Set[kueue.ResourceFlavorReference]
	Status          metrics.ClusterQueueStatus
	// AllocatableResourceGeneration will be increased when some admitted workloads are
	// deleted, or the resource groups are changed.
	AllocatableResourceGeneration int64

	AdmittedUsage resources.FlavorResourceQuantities
	// localQueues by (namespace/name).
	localQueues                        map[queue.LocalQueueReference]*LocalQueue
	podsReadyTracking                  bool
	missingFlavors                     []kueue.ResourceFlavorReference
	missingAdmissionChecks             []kueue.AdmissionCheckReference
	inactiveAdmissionChecks            []kueue.AdmissionCheckReference
	multiKueueAdmissionChecks          []kueue.AdmissionCheckReference
	provisioningAdmissionChecks        []kueue.AdmissionCheckReference
	perFlavorMultiKueueAdmissionChecks []kueue.AdmissionCheckReference
	tasFlavors                         map[kueue.ResourceFlavorReference]kueue.TopologyReference
	admittedWorkloadsCount             int
	isStopped                          bool
	workloadInfoOptions                []workload.InfoOption

	resourceNode resourceNode
	hierarchy.ClusterQueue[*cohort]

	tasCache *tasCache

	workloadsNotAccountedForTAS sets.Set[workload.Reference]
	AdmissionScope              *kueue.AdmissionScope
}

func (c *clusterQueue) GetName() kueue.ClusterQueueReference {
	return c.Name
}

// implement flatResourceNode/hierarchicalResourceNode interfaces

func (c *clusterQueue) getResourceNode() resourceNode {
	return c.resourceNode
}

func (c *clusterQueue) parentHRN() hierarchicalResourceNode {
	return c.Parent()
}

func (c *clusterQueue) Active() bool {
	return c.Status == active
}

var defaultPreemption = kueue.ClusterQueuePreemption{
	ReclaimWithinCohort: kueue.PreemptionPolicyNever,
	WithinClusterQueue:  kueue.PreemptionPolicyNever,
}

var defaultFlavorFungibility = kueue.FlavorFungibility{WhenCanBorrow: kueue.Borrow, WhenCanPreempt: kueue.TryNextFlavor}

func (c *clusterQueue) updateClusterQueue(log logr.Logger, in *kueue.ClusterQueue, resourceFlavors map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor, admissionChecks map[kueue.AdmissionCheckReference]AdmissionCheck, oldParent *cohort) error {
	if c.updateQuotasAndResourceGroups(in.Spec.ResourceGroups) || oldParent != c.Parent() {
		if oldParent != nil && oldParent != c.Parent() {
			updateCohortTreeResourcesIfNoCycle(oldParent)
		}
		if c.HasParent() {
			// clusterQueue will be updated as part of tree update.
			if err := updateCohortTreeResources(c.Parent()); err != nil {
				return err
			}
		} else {
			// since ClusterQueue has no parent, it won't be updated
			// as part of tree update.
			updateClusterQueueResourceNode(c)
		}
	}

	nsSelector, err := metav1.LabelSelectorAsSelector(in.Spec.NamespaceSelector)
	if err != nil {
		return err
	}
	c.NamespaceSelector = nsSelector

	c.isStopped = ptr.Deref(in.Spec.StopPolicy, kueue.None) != kueue.None

	c.AdmissionChecks = admissioncheck.NewAdmissionChecks(in)

	if in.Spec.Preemption != nil {
		c.Preemption = *in.Spec.Preemption
	} else {
		c.Preemption = defaultPreemption
	}

	c.UpdateWithFlavors(log, resourceFlavors)
	c.updateWithAdmissionChecks(log, admissionChecks)

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

	c.FairWeight = parseFairWeight(in.Spec.FairSharing)
	c.AdmissionScope = in.Spec.AdmissionScope
	return nil
}

func createdResourceGroups(kueueRgs []kueue.ResourceGroup) []ResourceGroup {
	rgs := make([]ResourceGroup, len(kueueRgs))
	for i, kueueRg := range kueueRgs {
		rgs[i] = ResourceGroup{
			CoveredResources: sets.New(kueueRg.CoveredResources...),
			Flavors:          make([]kueue.ResourceFlavorReference, 0, len(kueueRg.Flavors)),
		}
		for _, fIn := range kueueRg.Flavors {
			rgs[i].Flavors = append(rgs[i].Flavors, fIn.Name)
		}
	}
	return rgs
}

// updateQuotasAndResourceGroups updates Quotas and ResourceGroups.
// It returns true if any changes were made.
func (c *clusterQueue) updateQuotasAndResourceGroups(in []kueue.ResourceGroup) bool {
	oldRG := c.ResourceGroups
	oldQuotas := c.resourceNode.Quotas
	c.ResourceGroups = createdResourceGroups(in)
	c.resourceNode.Quotas = createResourceQuotas(in)

	// Start at 1, for backwards compatibility.
	return c.AllocatableResourceGeneration == 0 ||
		!equality.Semantic.DeepEqual(oldRG, c.ResourceGroups) ||
		!equality.Semantic.DeepEqual(oldQuotas, c.resourceNode.Quotas)
}

func (c *clusterQueue) updateQueueStatus(log logr.Logger) {
	if features.Enabled(features.TopologyAwareScheduling) &&
		len(c.tasFlavors) > 0 &&
		len(c.workloadsNotAccountedForTAS) > 0 &&
		c.isTASSynced() {
		log.V(2).Info("Delayed accounting for TAS usage for workloads", "count", len(c.workloadsNotAccountedForTAS))
		// There are some workloads which are not accounted yet for TAS.
		// We re-add them as not the tasCache is initialized (synced).
		for k, w := range c.Workloads {
			if c.workloadsNotAccountedForTAS.Has(k) {
				c.addOrUpdateWorkload(log, w.Obj)
				c.workloadsNotAccountedForTAS.Delete(k)
			}
		}
	}
	status := active
	if c.isStopped ||
		len(c.missingFlavors) > 0 ||
		len(c.missingAdmissionChecks) > 0 ||
		len(c.inactiveAdmissionChecks) > 0 ||
		c.isTASViolated() ||
		// one multikueue admission check is allowed
		len(c.multiKueueAdmissionChecks) > 1 ||
		len(c.perFlavorMultiKueueAdmissionChecks) > 0 {
		status = pending
	}
	if c.Status == terminating {
		status = terminating
	}
	if status != c.Status {
		log.V(3).Info("Updating status in cache", "clusterQueue", c.Name, "newStatus", status, "oldStatus", c.Status)
		c.Status = status
		metrics.ReportClusterQueueStatus(c.Name, c.Status)
	}
}

func (c *clusterQueue) isTASSynced() bool {
	for tasFlavor := range c.tasFlavors {
		if c.tasCache.Get(tasFlavor) == nil {
			return false
		}
	}
	return true
}

func (c *clusterQueue) inactiveReason() (string, string) {
	switch c.Status {
	case terminating:
		return kueue.ClusterQueueActiveReasonTerminating, "Can't admit new workloads; clusterQueue is terminating"
	case pending:
		reasons := make([]string, 0, 3)
		messages := make([]string, 0, 3)
		if c.isStopped {
			reasons = append(reasons, kueue.ClusterQueueActiveReasonStopped)
			messages = append(messages, "is stopped")
		}
		if len(c.missingFlavors) > 0 {
			reasons = append(reasons, kueue.ClusterQueueActiveReasonFlavorNotFound)
			messages = append(messages, fmt.Sprintf("references missing ResourceFlavor(s): %v", stringsutils.Join(c.missingFlavors, ",")))
		}
		if len(c.missingAdmissionChecks) > 0 {
			reasons = append(reasons, kueue.ClusterQueueActiveReasonAdmissionCheckNotFound)
			messages = append(messages, fmt.Sprintf("references missing AdmissionCheck(s): %v", stringsutils.Join(c.missingAdmissionChecks, ",")))
		}
		if len(c.inactiveAdmissionChecks) > 0 {
			reasons = append(reasons, kueue.ClusterQueueActiveReasonAdmissionCheckInactive)
			messages = append(messages, fmt.Sprintf("references inactive AdmissionCheck(s): %v", stringsutils.Join(c.inactiveAdmissionChecks, ",")))
		}

		if len(c.multiKueueAdmissionChecks) > 1 {
			reasons = append(reasons, kueue.ClusterQueueActiveReasonMultipleMultiKueueAdmissionChecks)
			messages = append(messages, fmt.Sprintf("Cannot use multiple MultiKueue AdmissionChecks on the same ClusterQueue, found: %v", stringsutils.Join(c.multiKueueAdmissionChecks, ",")))
		}

		if len(c.perFlavorMultiKueueAdmissionChecks) > 0 {
			reasons = append(reasons, kueue.ClusterQueueActiveReasonMultiKueueAdmissionCheckAppliedPerFlavor)
			messages = append(messages, fmt.Sprintf("Cannot specify MultiKueue AdmissionCheck per flavor, found: %s", stringsutils.Join(c.perFlavorMultiKueueAdmissionChecks, ",")))
		}

		if features.Enabled(features.TopologyAwareScheduling) && len(c.tasFlavors) > 0 {
			if len(c.multiKueueAdmissionChecks) > 0 {
				reasons = append(reasons, kueue.ClusterQueueActiveReasonNotSupportedWithTopologyAwareScheduling)
				messages = append(messages, "TAS is not supported with MultiKueue admission check")
			}
			for tasFlavor, topology := range c.tasFlavors {
				if c.tasCache.Get(tasFlavor) == nil {
					reasons = append(reasons, kueue.ClusterQueueActiveReasonTopologyNotFound)
					messages = append(messages, fmt.Sprintf("there is no Topology %q for TAS flavor %q", topology, tasFlavor))
				}
			}
		}

		if len(reasons) == 0 {
			return kueue.ClusterQueueActiveReasonUnknown, "Can't admit new workloads."
		}

		return reasons[0], api.TruncateConditionMessage(fmt.Sprintf("Can't admit new workloads: %v.", strings.Join(messages, ", ")))
	}
	return kueue.ClusterQueueActiveReasonReady, "Can admit new workloads"
}

func (c *clusterQueue) isTASViolated() bool {
	if !features.Enabled(features.TopologyAwareScheduling) || len(c.tasFlavors) == 0 {
		return false
	}
	if !c.isTASSynced() {
		return true
	}
	return len(c.multiKueueAdmissionChecks) > 0
}

// UpdateWithFlavors updates a ClusterQueue based on the passed ResourceFlavors set.
// Exported only for testing.
func (c *clusterQueue) UpdateWithFlavors(log logr.Logger, flavors map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor) {
	c.updateLabelKeys(flavors)
	c.updateQueueStatus(log)
}

func (c *clusterQueue) updateLabelKeys(flavors map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor) {
	c.missingFlavors = nil
	c.tasFlavors = nil
	for i := range c.ResourceGroups {
		rg := &c.ResourceGroups[i]
		if len(rg.Flavors) == 0 {
			rg.LabelKeys = nil
			continue
		}
		keys := sets.New[string]()
		for _, fName := range rg.Flavors {
			if flv, exist := flavors[fName]; exist {
				for k := range flv.Spec.NodeLabels {
					keys.Insert(k)
				}
				if flv.Spec.TopologyName != nil {
					if c.tasFlavors == nil {
						c.tasFlavors = make(map[kueue.ResourceFlavorReference]kueue.TopologyReference, 1)
					}
					c.tasFlavors[fName] = *flv.Spec.TopologyName
				}
			} else {
				c.missingFlavors = append(c.missingFlavors, fName)
			}
		}

		if keys.Len() > 0 {
			rg.LabelKeys = keys
		}
	}
}

// updateWithAdmissionChecks updates a ClusterQueue based on the passed AdmissionChecks set.
func (c *clusterQueue) updateWithAdmissionChecks(log logr.Logger, checks map[kueue.AdmissionCheckReference]AdmissionCheck) {
	checksPerController := make(map[string][]kueue.AdmissionCheckReference, len(c.AdmissionChecks))
	singleInstanceControllers := sets.New[string]()
	multiKueueAdmissionChecks := sets.New[kueue.AdmissionCheckReference]()
	provisioningAdmissionChecks := sets.New[kueue.AdmissionCheckReference]()
	var missing []kueue.AdmissionCheckReference
	var inactive []kueue.AdmissionCheckReference
	var perFlavorMultiKueueChecks []kueue.AdmissionCheckReference
	for acName, flavors := range c.AdmissionChecks {
		if ac, found := checks[acName]; !found {
			missing = append(missing, acName)
		} else {
			if !ac.Active {
				inactive = append(inactive, acName)
			}
			checksPerController[ac.Controller] = append(checksPerController[ac.Controller], acName)
			if ac.Controller == kueue.ProvisioningRequestControllerName {
				provisioningAdmissionChecks.Insert(acName)
			}
			if ac.Controller == kueue.MultiKueueControllerName {
				// MultiKueue Admission Checks has extra constraints:
				// - cannot use multiple MultiKueue AdmissionChecks on the same ClusterQueue
				// - cannot use specify MultiKueue AdmissionCheck per flavor
				multiKueueAdmissionChecks.Insert(acName)
				if flavors.Len() != 0 {
					perFlavorMultiKueueChecks = append(perFlavorMultiKueueChecks, acName)
				}
			}
		}
	}

	// sort the lists since c.AdmissionChecks is a map
	slices.Sort(missing)
	slices.Sort(inactive)
	slices.Sort(perFlavorMultiKueueChecks)
	multiKueueChecks := sets.List(multiKueueAdmissionChecks)
	provisioningChecks := sets.List(provisioningAdmissionChecks)

	update := false
	if !slices.Equal(c.missingAdmissionChecks, missing) {
		c.missingAdmissionChecks = missing
		update = true
	}

	if !slices.Equal(c.inactiveAdmissionChecks, inactive) {
		c.inactiveAdmissionChecks = inactive
		update = true
	}

	// remove the controllers which don't have more then one AC or are not single instance.
	maps.DeleteFunc(checksPerController, func(controller string, acs []kueue.AdmissionCheckReference) bool {
		return len(acs) < 2 || !singleInstanceControllers.Has(controller)
	})

	// sort the remaining set
	for c := range checksPerController {
		slices.Sort(checksPerController[c])
	}

	if !slices.Equal(c.multiKueueAdmissionChecks, multiKueueChecks) {
		c.multiKueueAdmissionChecks = multiKueueChecks
		update = true
	}

	if !slices.Equal(c.provisioningAdmissionChecks, provisioningChecks) {
		c.provisioningAdmissionChecks = provisioningChecks
		update = true
	}

	if !slices.Equal(c.perFlavorMultiKueueAdmissionChecks, perFlavorMultiKueueChecks) {
		c.perFlavorMultiKueueAdmissionChecks = perFlavorMultiKueueChecks
		update = true
	}

	if update {
		c.updateQueueStatus(log)
	}
}

func (c *clusterQueue) addOrUpdateWorkload(log logr.Logger, w *kueue.Workload) {
	k := workload.Key(w)
	if _, exist := c.Workloads[k]; exist {
		c.deleteWorkload(log, w)
	}
	wi := workload.NewInfo(w, c.workloadInfoOptions...)
	c.Workloads[k] = wi
	c.updateWorkloadUsage(log, wi, add)
	if c.podsReadyTracking && !apimeta.IsStatusConditionTrue(w.Status.Conditions, kueue.WorkloadPodsReady) {
		c.WorkloadsNotReady.Insert(k)
	}
	c.reportActiveWorkloads()
}

func (c *clusterQueue) forgetWorkload(log logr.Logger, w *kueue.Workload) {
	c.deleteWorkload(log, w)
	delete(c.workloadsNotAccountedForTAS, workload.Key(w))
}

func (c *clusterQueue) deleteWorkload(log logr.Logger, w *kueue.Workload) {
	k := workload.Key(w)
	wi, exist := c.Workloads[k]
	if !exist {
		return
	}
	c.updateWorkloadUsage(log, wi, subtract)
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
	metrics.AdmittedActiveWorkloads.WithLabelValues(string(c.Name)).Set(float64(c.admittedWorkloadsCount))
	metrics.ReservingActiveWorkloads.WithLabelValues(string(c.Name)).Set(float64(len(c.Workloads)))
}

func (q *LocalQueue) reportActiveWorkloads() {
	namespace, name := queue.MustParseLocalQueueReference(q.key)
	metrics.LocalQueueAdmittedActiveWorkloads.WithLabelValues(string(name), namespace).Set(float64(q.admittedWorkloads))
	metrics.LocalQueueReservingActiveWorkloads.WithLabelValues(string(name), namespace).Set(float64(q.reservingWorkloads))
}

// updateWorkloadUsage updates the usage of the ClusterQueue for the workload
// and the number of admitted workloads for local queues.
func (c *clusterQueue) updateWorkloadUsage(log logr.Logger, wi *workload.Info, op usageOp) {
	admitted := workload.IsAdmitted(wi.Obj)
	frUsage := wi.FlavorResourceUsage()
	for fr, q := range frUsage {
		if op == add {
			addUsage(c, fr, q)
		}
		if op == subtract {
			removeUsage(c, fr, q)
		}
	}
	c.updateWorkloadTASUsage(log, wi, op)
	if admitted {
		updateFlavorUsage(frUsage, c.AdmittedUsage, op)
		c.admittedWorkloadsCount += op.asSignedOne()
	}
	qKey := queue.KeyFromWorkload(wi.Obj)
	if lq, ok := c.localQueues[qKey]; ok {
		updateFlavorUsage(frUsage, lq.totalReserved, op)
		lq.reservingWorkloads += op.asSignedOne()
		if admitted {
			lq.updateAdmittedUsage(frUsage, op)
			lq.admittedWorkloads += op.asSignedOne()
		}
		if features.Enabled(features.LocalQueueMetrics) {
			lq.reportActiveWorkloads()
		}
	}
}

func (c *clusterQueue) updateWorkloadTASUsage(log logr.Logger, wi *workload.Info, op usageOp) {
	if !features.Enabled(features.TopologyAwareScheduling) || !wi.IsUsingTAS() {
		return
	}
	key := workload.Key(wi.Obj)
	log = log.WithValues("workload", key)
	if !c.isTASSynced() {
		log.V(2).Info("Delaying accounting of the TAS usage, because TAS cache is not synced yet")
		// TAS cache is not synced yet so we defer accounting for TAS usage.
		c.workloadsNotAccountedForTAS.Insert(key)
		return
	}
	for tasFlavor, tasUsage := range wi.TASUsage() {
		tasFlvCache := c.tasCache.Get(tasFlavor)
		switch {
		case tasFlvCache == nil:
			log.V(2).Info("TAS flavor used by workload not found in cache", "tasFlavor", tasFlavor)
		case op == add:
			tasFlvCache.addUsage(tasUsage)
		case op == subtract:
			// If the workload is not accounted for TAS, we haven't called
			// addUsage on startup, and so we don't subtract the capacity now.
			if c.workloadsNotAccountedForTAS.Has(key) {
				log.V(2).Info("Skip subtracting TAS usage because we've never accounted for it")
			} else {
				tasFlvCache.removeUsage(tasUsage)
			}
		}
	}
	// We just accounted for TAS usage so drop it from the set.
	c.workloadsNotAccountedForTAS.Delete(key)
}

func updateFlavorUsage(newUsage resources.FlavorResourceQuantities, oldUsage resources.FlavorResourceQuantities, op usageOp) {
	for fr, q := range newUsage {
		oldUsage[fr] += q * int64(op.asSignedOne())
	}
}

func (c *clusterQueue) addLocalQueue(q *kueue.LocalQueue) error {
	qKey := queueKey(q)
	if _, ok := c.localQueues[qKey]; ok {
		return errQueueAlreadyExists
	}
	// We need to count the workloads, because they could have been added before
	// receiving the queue add event.
	qImpl := &LocalQueue{
		key:                qKey,
		reservingWorkloads: 0,
		totalReserved:      make(resources.FlavorResourceQuantities),
	}
	qImpl.resetFlavorsAndResources(c.resourceNode.Usage, c.AdmittedUsage)
	for _, wl := range c.Workloads {
		if workloadBelongsToLocalQueue(wl.Obj, q) {
			frq := wl.FlavorResourceUsage()
			updateFlavorUsage(frq, qImpl.totalReserved, add)
			qImpl.reservingWorkloads++
			if workload.IsAdmitted(wl.Obj) {
				qImpl.updateAdmittedUsage(frq, add)
				qImpl.admittedWorkloads++
			}
		}
	}
	c.localQueues[qKey] = qImpl
	if features.Enabled(features.LocalQueueMetrics) {
		qImpl.reportActiveWorkloads()
	}
	return nil
}

func (c *clusterQueue) deleteLocalQueue(q *kueue.LocalQueue) {
	qKey := queueKey(q)
	if features.Enabled(features.LocalQueueMetrics) {
		namespace, lqName := queue.MustParseLocalQueueReference(qKey)
		metrics.ClearLocalQueueCacheMetrics(metrics.LocalQueueReference{
			Name:      lqName,
			Namespace: namespace,
		})
	}
	delete(c.localQueues, qKey)
}

func (c *clusterQueue) flavorInUse(flavor kueue.ResourceFlavorReference) bool {
	for _, rg := range c.ResourceGroups {
		if slices.Contains(rg.Flavors, flavor) {
			return true
		}
	}
	return false
}

func (q *LocalQueue) resetFlavorsAndResources(cqUsage resources.FlavorResourceQuantities, cqAdmittedUsage resources.FlavorResourceQuantities) {
	// Clean up removed flavors or resources.
	q.Lock()
	defer q.Unlock()
	q.totalReserved = resetUsage(q.totalReserved, cqUsage)
	q.admittedUsage = resetUsage(q.admittedUsage, cqAdmittedUsage)
}

func resetUsage(lqUsage resources.FlavorResourceQuantities, cqUsage resources.FlavorResourceQuantities) resources.FlavorResourceQuantities {
	usedFlavorResources := make(resources.FlavorResourceQuantities, len(cqUsage))
	for fr := range cqUsage {
		usedFlavorResources[fr] = lqUsage[fr]
	}
	return usedFlavorResources
}

func workloadBelongsToLocalQueue(wl *kueue.Workload, q *kueue.LocalQueue) bool {
	return wl.Namespace == q.Namespace && string(wl.Spec.QueueName) == q.Name
}

// Implements dominantResourceShareNode interface.

func (c *clusterQueue) fairWeight() *resource.Quantity {
	return &c.FairWeight
}

func (c *clusterQueue) isTASOnly() bool {
	for _, rg := range c.ResourceGroups {
		for _, fName := range rg.Flavors {
			if _, found := c.tasFlavors[fName]; !found {
				return false
			}
		}
	}
	return true
}

func (c *clusterQueue) flavorsWithProvReqAdmissionCheck() sets.Set[kueue.ResourceFlavorReference] {
	flvs := sets.New[kueue.ResourceFlavorReference]()
	for _, ac := range c.provisioningAdmissionChecks {
		flvs.Insert(c.flavorsForAdmissionCheck(ac).UnsortedList()...)
	}
	return flvs
}

func (c *clusterQueue) flavorsForAdmissionCheck(ac kueue.AdmissionCheckReference) sets.Set[kueue.ResourceFlavorReference] {
	flvs := sets.New(c.AdmissionChecks[ac].UnsortedList()...)
	if len(c.AdmissionChecks[ac]) == 0 {
		for _, rg := range c.ResourceGroups {
			flvs.Insert(rg.Flavors...)
		}
	}
	return flvs
}
