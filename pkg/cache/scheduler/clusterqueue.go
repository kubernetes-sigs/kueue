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

package scheduler

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/equality"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"

	cfg "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/hierarchy"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	"sigs.k8s.io/kueue/pkg/util/api"
	utilmath "sigs.k8s.io/kueue/pkg/util/math"
	"sigs.k8s.io/kueue/pkg/util/queue"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
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
	FairWeight        float64
	FlavorFungibility kueue.FlavorFungibility
	// Aggregates AdmissionChecks from both .spec.AdmissionChecks and .spec.AdmissionCheckStrategy
	// Sets hold ResourceFlavors to which an AdmissionCheck should apply.
	AdmissionChecks workload.AdmissionChecks
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
	resourceFormatter                  *resources.ResourceFormatter

	resourceNode resourceNode
	hierarchy.ClusterQueue[*cohort]

	tasCache *tasCache

	// isTASSynced determines if the TAS cached is synced, ie: initialized,
	// and all TAS Workloads are accounted in the cache. The distintion between
	// initialized and synced is introduced to make sure all pre-existing
	// TAS Workloads are accounted again when TAS cache becomes initialized.
	isTASSynced bool

	AdmissionScope *kueue.AdmissionScope

	ConcurrentAdmissionPolicy *kueue.ConcurrentAdmissionPolicy

	roleTracker *roletracker.RoleTracker

	// allows access to values extracted from K8s labels/annotations, used as custom Prometheus metric labels
	customLabels *metrics.CustomLabels

	lqMetrics *metrics.LocalQueueMetricsConfig
}

func (c *clusterQueue) GetName() kueue.ClusterQueueReference {
	return c.Name
}

func (c *clusterQueue) GetCustomLabelValues() []string {
	return c.customLabels.CQGet(c.Name)
}

// parentAndRootCohort returns the names of the CQ's parent cohort and root cohort.
// If the CQ has no parent or the parent cohort has a cycle, both are returned as empty.
func (c *clusterQueue) parentAndRootCohort() (parent, root kueue.CohortReference) {
	if !c.HasParent() || hierarchy.HasCycle(c.Parent()) {
		return
	}
	return c.Parent().GetName(), c.Parent().getRootUnsafe().Name
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

var defaultFlavorFungibility = kueue.FlavorFungibility{WhenCanBorrow: kueue.MayStopSearch, WhenCanPreempt: kueue.TryNextFlavor}

func (c *clusterQueue) updateClusterQueue(
	log logr.Logger,
	in *kueue.ClusterQueue,
	resourceFlavors map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor,
	admissionChecks map[kueue.AdmissionCheckReference]AdmissionCheck,
	oldParent *cohort,
) error {
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
	if features.Enabled(features.ConcurrentAdmission) {
		c.ConcurrentAdmissionPolicy = in.Spec.ConcurrentAdmissionPolicy
	}
	return nil
}

func (c *clusterQueue) ConcurrentAdmissionEnabled() bool {
	if !features.Enabled(features.ConcurrentAdmission) {
		return false
	}
	return c.ConcurrentAdmissionPolicy != nil
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
	// Use maps.EqualFunc with ResourceQuota.Equal for the Quotas map: it holds
	// resources.Amount values whose unexported fields cause k8s
	// equality.Semantic.DeepEqual (a forked reflect-based DeepEqual) to panic.
	return c.AllocatableResourceGeneration == 0 ||
		!equality.Semantic.DeepEqual(oldRG, c.ResourceGroups) ||
		!maps.EqualFunc(oldQuotas, c.resourceNode.Quotas, ResourceQuota.Equal)
}

func (c *clusterQueue) updateQueueStatus(log logr.Logger) {
	c.ensureTASIsSynced(log)
	status := active
	if c.isStopped ||
		len(c.missingFlavors) > 0 ||
		len(c.missingAdmissionChecks) > 0 ||
		len(c.inactiveAdmissionChecks) > 0 ||
		c.isTASViolated() ||
		// one multikueue admission check is allowed
		len(c.multiKueueAdmissionChecks) > 1 ||
		len(c.perFlavorMultiKueueAdmissionChecks) > 0 ||
		// provisioning requests should not be used on manager cluster with multikueue
		(len(c.multiKueueAdmissionChecks) > 0 && len(c.provisioningAdmissionChecks) > 0) {
		status = pending
	}
	if c.Status == terminating {
		status = terminating
	}
	if status != c.Status {
		log.V(3).Info("Updating status in cache", "clusterQueue", c.Name, "newStatus", status, "oldStatus", c.Status)
		c.Status = status
		metrics.ReportClusterQueueStatus(c.Name, c.Status, c.GetCustomLabelValues(), c.roleTracker)
	}
}

// ensureTASIsSynced makes sure all TAS workloads are accounted (TAS cache is synced),
// if TAS cache is initialized.
func (c *clusterQueue) ensureTASIsSynced(log logr.Logger) {
	if !features.Enabled(features.TopologyAwareScheduling) || len(c.tasFlavors) == 0 {
		return
	}
	if !c.isTASInitialized() {
		c.isTASSynced = false
		return
	}
	if c.isTASSynced {
		return
	}
	log.V(2).Info("Syncing TAS usage initilized TAS cache", "workloads", len(c.Workloads))
	for _, w := range c.Workloads {
		c.addOrUpdateWorkload(log, w.Obj)
	}
	c.isTASSynced = true
}

// isTASInitialized determines if the TAS cache for a specific flavor is initiatilzed, ie.
// the ResourceFlavor and the referenced Topology exist.
func (c *clusterQueue) isTASInitialized() bool {
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

		if len(c.multiKueueAdmissionChecks) > 0 && len(c.provisioningAdmissionChecks) > 0 {
			reasons = append(reasons, kueue.ClusterQueueActiveReasonMultiKueueWithProvisioningRequest)
			messages = append(messages, fmt.Sprintf("Cannot use both MultiKueue and ProvisioningRequest AdmissionChecks together, found: %s, %s",
				stringsutils.Join(c.multiKueueAdmissionChecks, ","), stringsutils.Join(c.provisioningAdmissionChecks, ",")))
		}

		if features.Enabled(features.TopologyAwareScheduling) && len(c.tasFlavors) > 0 {
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
	// Skip TAS cache validation when MultiKueue is enabled; topology runs on worker clusters.
	if c.hasMultiKueueAdmissionCheck() {
		return false
	}
	if !c.isTASInitialized() {
		return true
	}
	return false
}

// UpdateWithFlavors updates a ClusterQueue based on the passed ResourceFlavors set.
// Exported only for testing.
func (c *clusterQueue) UpdateWithFlavors(log logr.Logger, flavors map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor) {
	c.updateFlavorMetadata(flavors)
	c.updateQueueStatus(log)
}

func (c *clusterQueue) updateFlavorMetadata(flavors map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor) {
	c.missingFlavors = nil
	c.tasFlavors = nil
	for i := range c.ResourceGroups {
		rg := &c.ResourceGroups[i]
		for _, fName := range rg.Flavors {
			if flv, exist := flavors[fName]; exist {
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
				if !flavors.Equal(AllFlavors(c.ResourceGroups)) {
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
		c.deleteWorkload(log, k)
	}
	wi := workload.NewInfo(w, c.workloadInfoOptions...)
	wi.UpdateSchedulingHash(log)
	c.Workloads[k] = wi
	if features.Enabled(features.CustomMetricLabels) {
		c.customLabels.Store(cfg.SourceKindWorkload, string(k), w.Labels, w.Annotations)
	}
	c.updateWorkloadUsage(log, wi, add)
	if c.podsReadyTracking && !apimeta.IsStatusConditionTrue(w.Status.Conditions, kueue.WorkloadPodsReady) {
		c.WorkloadsNotReady.Insert(k)
	}
	c.reportActiveWorkloads()
}

func (c *clusterQueue) forgetWorkload(log logr.Logger, wlKey workload.Reference) {
	c.deleteWorkload(log, wlKey)
}

func (c *clusterQueue) deleteWorkload(log logr.Logger, wlKey workload.Reference) {
	wi, exist := c.Workloads[wlKey]
	if !exist {
		return
	}
	c.updateWorkloadUsage(log, wi, subtract)
	if c.podsReadyTracking {
		c.WorkloadsNotReady.Delete(wlKey)
	}
	// we only increase the AllocatableResourceGeneration cause the add of workload won't make more
	// workloads fit in ClusterQueue.
	c.AllocatableResourceGeneration++

	delete(c.Workloads, wlKey)
	if features.Enabled(features.CustomMetricLabels) {
		c.customLabels.Delete(cfg.SourceKindWorkload, string(wlKey))
	}
	c.reportActiveWorkloads()
}

func (c *clusterQueue) reportActiveWorkloads() {
	clVals := c.GetCustomLabelValues()
	for ancestor := range c.Parent().PathSelfToRoot() {
		metrics.ReportCohortSubtreeAdmittedActiveWorkloads(ancestor.Name, ancestor.admittedWorkloadsCount, clVals, c.roleTracker)
	}
	metrics.ReportReservingActiveWorkloads(c.Name, len(c.Workloads), clVals, c.roleTracker)
}

func (c *clusterQueue) resyncAdmittedActiveWorkloads() {
	for wlRef, wl := range c.Workloads {
		if workload.IsActive(wl.Obj) && workload.IsAdmitted(wl.Obj) {
			metrics.ReportAdmittedActiveWorkloads(c.Name, 1, c.getLabelValuesFor(wlRef), c.roleTracker)
		}
	}
}

func (c *clusterQueue) reportResourceMetrics(fairSharingEnabled bool) {
	var cohort kueue.CohortReference
	if c.HasParent() {
		cohort = c.Parent().GetName()
	}
	cqName := string(c.Name)
	clVals := c.GetCustomLabelValues()
	for fr, quota := range c.resourceNode.Quotas {
		fName, rName := string(fr.Flavor), string(fr.Resource)
		nominal := resourceFloat(c.resourceFormatter, fr.Resource, quota.Nominal.Int64())
		var borrowing, lending float64
		if quota.BorrowingLimit == nil {
			borrowing = math.Inf(1)
		} else {
			borrowing = resourceFloat(c.resourceFormatter, fr.Resource, quota.BorrowingLimit.Int64())
		}
		if quota.LendingLimit == nil {
			lending = math.Inf(1)
		} else {
			lending = resourceFloat(c.resourceFormatter, fr.Resource, quota.LendingLimit.Int64())
		}
		metrics.ReportClusterQueueQuotas(cohort, cqName, fName, rName, nominal, borrowing, lending, clVals, c.roleTracker)
		metrics.ReportClusterQueueResourceReservations(cohort, cqName, fName, rName, resourceFloat(c.resourceFormatter, fr.Resource, c.resourceNode.Usage[fr].Int64()), clVals, c.roleTracker)
		metrics.ReportClusterQueueResourceUsage(cohort, cqName, fName, rName, resourceFloat(c.resourceFormatter, fr.Resource, c.AdmittedUsage[fr].Int64()), clVals, c.roleTracker)
	}
	if fairSharingEnabled {
		c.reportWeightedShare(cohort)
	}
}

func (c *clusterQueue) reportWeightedShare(cohort kueue.CohortReference) {
	drs := dominantResourceShare(c, nil)
	weightedShare := drs.PreciseWeightedShare()
	if weightedShare == math.Inf(1) {
		weightedShare = math.NaN()
	}
	metrics.ReportClusterQueueWeightedShare(c.Name, cohort, weightedShare, c.GetCustomLabelValues(), c.roleTracker)
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

		incr := op.asSignedOne()
		c.Parent().updateAdmittedWorkloadsCount(incr)
		c.admittedWorkloadsCount += incr

		wlRef := workload.Key(wi.Obj)
		metrics.ReportAdmittedActiveWorkloads(c.Name, incr, c.getLabelValuesFor(wlRef), c.roleTracker)
	}
	qKey := queue.KeyFromWorkload(wi.Obj)
	if lq, ok := c.localQueues[qKey]; ok {
		updateFlavorUsage(frUsage, lq.totalReserved, op)
		lq.reservingWorkloads += op.asSignedOne()
		if admitted {
			lq.updateAdmittedUsage(frUsage, op)
			lq.admittedWorkloads += op.asSignedOne()
		}
		if lq.shouldExposeMetrics(c.lqMetrics) {
			lq.reportActiveWorkloads(c.roleTracker)
		}
	}
}

func (c *clusterQueue) getLabelValuesFor(wlRef workload.Reference) []string {
	return c.customLabels.GetFor(map[cfg.SourceKind]string{
		cfg.SourceKindWorkload:     string(wlRef),
		cfg.SourceKindClusterQueue: string(c.Name),
	})
}

func (c *clusterQueue) updateWorkloadTASUsage(log logr.Logger, wi *workload.Info, op usageOp) {
	if !features.Enabled(features.TopologyAwareScheduling) || !wi.IsUsingTAS() {
		return
	}
	key := workload.Key(wi.Obj)
	log = log.WithValues("workload", key)
	for tasFlavor, tasUsage := range wi.TASUsage() {
		tasFlvCache := c.tasCache.Get(tasFlavor)
		switch {
		case tasFlvCache == nil:
			log.V(2).Info("TAS flavor used by workload not found in cache", "tasFlavor", tasFlavor)
		case op == add:
			tasFlvCache.addUsage(log, key, tasUsage)
		case op == subtract:
			tasFlvCache.removeUsage(log, key)
		}
	}
}

func updateFlavorUsage(newUsage resources.FlavorResourceQuantities, oldUsage resources.FlavorResourceQuantities, op usageOp) {
	sign := int64(op.asSignedOne())
	for fr, q := range newUsage {
		oldUsage[fr] = oldUsage[fr].AddInt64(utilmath.SaturatingMul(sign, q.Int64()))
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
		customLabels:       c.customLabels,
		labels:             q.GetLabels(),
		resourceFormatter:  c.resourceFormatter,
	}
	if features.Enabled(features.CustomMetricLabels) {
		c.customLabels.LQStore(qKey, q.Labels, q.Annotations)
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
	if c.lqMetrics.ShouldExposeLocalQueueMetrics(q.GetLabels()) {
		qImpl.reportActiveWorkloads(c.roleTracker)
	}
	return nil
}

func (c *clusterQueue) deleteLocalQueue(q *kueue.LocalQueue) {
	qKey := queueKey(q)
	if c.lqMetrics.IsEnabled() {
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

func (c *clusterQueue) fairWeight() float64 {
	return c.FairWeight
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
		flvs.Insert(c.AdmissionChecks[ac].UnsortedList()...)
	}
	return flvs
}

func (c *clusterQueue) hasMultiKueueAdmissionCheck() bool {
	return len(c.multiKueueAdmissionChecks) > 0
}
