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
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/go-logr/logr"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/hierarchy"
	utilindexer "sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/util/queue"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	"sigs.k8s.io/kueue/pkg/workload"
)

var (
	ErrCohortNotFound = errors.New("cohort not found")
	ErrCohortHasCycle = errors.New("cohort has a cycle")
	ErrCqNotFound     = errors.New("cluster queue not found")
	errQNotFound      = errors.New("queue not found")
)

const (
	pending     = metrics.CQStatusPending
	active      = metrics.CQStatusActive
	terminating = metrics.CQStatusTerminating
)

// Option configures the reconciler.
type Option func(*Cache)

// WithPodsReadyTracking indicates the cache controller tracks the PodsReady
// condition for admitted workloads, and allows to block admission of new
// workloads until all admitted workloads are in the PodsReady condition.
func WithPodsReadyTracking(f bool) Option {
	return func(c *Cache) {
		c.podsReadyTracking = f
	}
}

func WithExcludedResourcePrefixes(excludedPrefixes []string) Option {
	return func(c *Cache) {
		c.workloadInfoOptions = append(c.workloadInfoOptions, workload.WithExcludedResourcePrefixes(excludedPrefixes))
	}
}

// WithResourceTransformations sets the resource transformations.
func WithResourceTransformations(transforms []config.ResourceTransformation) Option {
	return func(c *Cache) {
		c.workloadInfoOptions = append(c.workloadInfoOptions, workload.WithResourceTransformations(transforms))
	}
}

func WithFairSharing(enabled bool) Option {
	return func(c *Cache) {
		c.fairSharingEnabled = enabled
	}
}

func WithAdmissionFairSharing(afs *config.AdmissionFairSharing) Option {
	return func(c *Cache) {
		c.admissionFairSharing = afs
	}
}

// WithRoleTracker sets the roleTracker for HA metrics.
func WithRoleTracker(tracker *roletracker.RoleTracker) Option {
	return func(c *Cache) {
		c.roleTracker = tracker
	}
}

// Cache keeps track of the Workloads that got admitted through ClusterQueues.
type Cache struct {
	sync.RWMutex
	podsReadyCond sync.Cond

	client               client.Client
	resourceFlavors      map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor
	podsReadyTracking    bool
	admissionChecks      map[kueue.AdmissionCheckReference]AdmissionCheck
	workloadInfoOptions  []workload.InfoOption
	fairSharingEnabled   bool
	admissionFairSharing *config.AdmissionFairSharing
	// Tracks Workload's ClusterQueue assignment throughout its presence in the cache, which is when they reserve quota (`QuotaReserved=True`).
	workloadAssignedQueues map[workload.Reference]kueue.ClusterQueueReference

	hm hierarchy.Manager[*clusterQueue, *cohort]

	tasCache tasCache

	roleTracker *roletracker.RoleTracker
}

func New(client client.Client, options ...Option) *Cache {
	cache := &Cache{
		client:                 client,
		resourceFlavors:        make(map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor),
		admissionChecks:        make(map[kueue.AdmissionCheckReference]AdmissionCheck),
		workloadAssignedQueues: make(map[workload.Reference]kueue.ClusterQueueReference),
		hm:                     hierarchy.NewManager(newCohort),
		tasCache:               NewTASCache(client),
	}
	for _, option := range options {
		option(cache)
	}
	cache.podsReadyCond.L = &cache.RWMutex
	return cache
}

func (c *Cache) newClusterQueue(log logr.Logger, cq *kueue.ClusterQueue) (*clusterQueue, error) {
	cqImpl := &clusterQueue{
		Name:                kueue.ClusterQueueReference(cq.Name),
		Workloads:           make(map[workload.Reference]*workload.Info),
		WorkloadsNotReady:   sets.New[workload.Reference](),
		localQueues:         make(map[queue.LocalQueueReference]*LocalQueue),
		podsReadyTracking:   c.podsReadyTracking,
		workloadInfoOptions: c.workloadInfoOptions,
		AdmittedUsage:       make(resources.FlavorResourceQuantities),
		resourceNode:        NewResourceNode(),
		tasCache:            &c.tasCache,
		AdmissionScope:      cq.Spec.AdmissionScope,

		roleTracker: c.roleTracker,
	}
	c.hm.AddClusterQueue(cqImpl)
	c.hm.UpdateClusterQueueEdge(kueue.ClusterQueueReference(cq.Name), cq.Spec.CohortName)
	if err := cqImpl.updateClusterQueue(log, cq, c.resourceFlavors, c.admissionChecks, nil); err != nil {
		return nil, err
	}

	return cqImpl, nil
}

// WaitForPodsReady waits for all admitted workloads to be in the PodsReady condition
// if podsReadyTracking is enabled, otherwise returns immediately.
func (c *Cache) WaitForPodsReady(ctx context.Context) {
	if !c.podsReadyTracking {
		return
	}

	c.Lock()
	defer c.Unlock()

	log := ctrl.LoggerFrom(ctx)
	for {
		if c.podsReadyForAllAdmittedWorkloads(log) {
			return
		}
		log.V(3).Info("Blocking admission as not all workloads are in the PodsReady condition")
		select {
		case <-ctx.Done():
			log.V(5).Info("Context cancelled when waiting for pods to be ready; returning")
			return
		default:
			// wait releases the lock and acquires again when awaken
			c.podsReadyCond.Wait()
		}
	}
}

func (c *Cache) PodsReadyForAllAdmittedWorkloads(log logr.Logger) bool {
	if !c.podsReadyTracking {
		return true
	}
	c.Lock()
	defer c.Unlock()
	return c.podsReadyForAllAdmittedWorkloads(log)
}

func (c *Cache) podsReadyForAllAdmittedWorkloads(log logr.Logger) bool {
	for _, cq := range c.hm.ClusterQueues() {
		if len(cq.WorkloadsNotReady) > 0 {
			log.V(3).Info("There is a ClusterQueue with not ready workloads", "clusterQueue", klog.KRef("", string(cq.Name)))
			return false
		}
	}
	log.V(5).Info("All workloads are in the PodsReady condition")
	return true
}

// CleanUpOnContext tracks the context. When closed, it wakes routines waiting
// on the podsReady condition. It should be called before doing any calls to
// cache.WaitForPodsReady.
func (c *Cache) CleanUpOnContext(ctx context.Context) {
	<-ctx.Done()
	c.Lock()
	defer c.Unlock()
	c.podsReadyCond.Broadcast()
}

func (c *Cache) updateClusterQueues(log logr.Logger) sets.Set[kueue.ClusterQueueReference] {
	cqs := sets.New[kueue.ClusterQueueReference]()

	for _, cq := range c.hm.ClusterQueues() {
		prevStatus := cq.Status
		// We call update on all ClusterQueues irrespective of which CQ actually use this flavor
		// because it is not expensive to do so, and is not worth tracking which ClusterQueues use
		// which flavors.
		cq.UpdateWithFlavors(log, c.resourceFlavors)
		cq.updateWithAdmissionChecks(log, c.admissionChecks)
		curStatus := cq.Status
		if prevStatus == pending && curStatus == active {
			cqs.Insert(cq.Name)
		}
	}
	return cqs
}

func (c *Cache) ActiveClusterQueues() sets.Set[kueue.ClusterQueueReference] {
	c.RLock()
	defer c.RUnlock()
	cqs := sets.New[kueue.ClusterQueueReference]()
	for _, cq := range c.hm.ClusterQueues() {
		if cq.Status == active {
			cqs.Insert(cq.Name)
		}
	}
	return cqs
}

func (c *Cache) TASCache() *tasCache {
	return &c.tasCache
}

func (c *Cache) AddOrUpdateResourceFlavor(log logr.Logger, rf *kueue.ResourceFlavor) sets.Set[kueue.ClusterQueueReference] {
	c.Lock()
	defer c.Unlock()
	c.resourceFlavors[kueue.ResourceFlavorReference(rf.Name)] = rf
	if handleTASFlavor(rf) {
		c.tasCache.AddFlavor(rf)
	}
	return c.updateClusterQueues(log)
}

func (c *Cache) DeleteResourceFlavor(log logr.Logger, rf *kueue.ResourceFlavor) sets.Set[kueue.ClusterQueueReference] {
	c.Lock()
	defer c.Unlock()
	delete(c.resourceFlavors, kueue.ResourceFlavorReference(rf.Name))
	if handleTASFlavor(rf) {
		c.tasCache.DeleteFlavor(kueue.ResourceFlavorReference(rf.Name))
	}
	return c.updateClusterQueues(log)
}

func (c *Cache) AddOrUpdateTopology(log logr.Logger, topology *kueue.Topology) sets.Set[kueue.ClusterQueueReference] {
	c.Lock()
	defer c.Unlock()
	c.tasCache.AddTopology(topology)
	return c.updateClusterQueues(log)
}

func (c *Cache) DeleteTopology(log logr.Logger, name kueue.TopologyReference) sets.Set[kueue.ClusterQueueReference] {
	c.Lock()
	defer c.Unlock()
	c.tasCache.DeleteTopology(name)
	return c.updateClusterQueues(log)
}

func (c *Cache) CloneTASCache() map[kueue.ResourceFlavorReference]*TASFlavorCache {
	c.RLock()
	defer c.RUnlock()
	return c.tasCache.Clone()
}

func (c *Cache) AddOrUpdateAdmissionCheck(log logr.Logger, ac *kueue.AdmissionCheck) sets.Set[kueue.ClusterQueueReference] {
	c.Lock()
	defer c.Unlock()

	newAC := AdmissionCheck{
		Active:     apimeta.IsStatusConditionTrue(ac.Status.Conditions, kueue.AdmissionCheckActive),
		Controller: ac.Spec.ControllerName,
	}
	c.admissionChecks[kueue.AdmissionCheckReference(ac.Name)] = newAC

	return c.updateClusterQueues(log)
}

func (c *Cache) DeleteAdmissionCheck(log logr.Logger, ac *kueue.AdmissionCheck) sets.Set[kueue.ClusterQueueReference] {
	c.Lock()
	defer c.Unlock()
	delete(c.admissionChecks, kueue.AdmissionCheckReference(ac.Name))
	return c.updateClusterQueues(log)
}

func (c *Cache) AdmissionChecksForClusterQueue(cqName kueue.ClusterQueueReference) []AdmissionCheck {
	c.RLock()
	defer c.RUnlock()
	cq := c.hm.ClusterQueue(cqName)
	if cq == nil || len(cq.AdmissionChecks) == 0 {
		return nil
	}
	acs := make([]AdmissionCheck, 0, len(cq.AdmissionChecks))
	for acName := range cq.AdmissionChecks {
		if ac, ok := c.admissionChecks[acName]; ok {
			acs = append(acs, ac)
		}
	}
	return acs
}

func (c *Cache) ClusterQueueActive(name kueue.ClusterQueueReference) bool {
	return c.clusterQueueInStatus(name, active)
}

func (c *Cache) ClusterQueueTerminating(name kueue.ClusterQueueReference) bool {
	return c.clusterQueueInStatus(name, terminating)
}

func (c *Cache) ClusterQueueReadiness(name kueue.ClusterQueueReference) (metav1.ConditionStatus, string, string) {
	c.RLock()
	defer c.RUnlock()
	cq := c.hm.ClusterQueue(name)
	if cq == nil {
		return metav1.ConditionFalse, "NotFound", "ClusterQueue not found"
	}
	if cq.Status == active {
		return metav1.ConditionTrue, "Ready", "Can admit new workloads"
	}
	reason, msg := cq.inactiveReason()
	return metav1.ConditionFalse, reason, msg
}

func (c *Cache) clusterQueueInStatus(name kueue.ClusterQueueReference, status metrics.ClusterQueueStatus) bool {
	c.RLock()
	defer c.RUnlock()

	cq := c.hm.ClusterQueue(name)
	if cq == nil {
		return false
	}
	return cq.Status == status
}

func (c *Cache) TerminateClusterQueue(name kueue.ClusterQueueReference) {
	c.Lock()
	defer c.Unlock()
	if cq := c.hm.ClusterQueue(name); cq != nil {
		cq.Status = terminating
		metrics.ReportClusterQueueStatus(cq.Name, cq.Status, c.roleTracker)
	}
}

// ClusterQueueEmpty indicates whether there's any active workload admitted by
// the provided clusterQueue.
// Return true if the clusterQueue doesn't exist.
func (c *Cache) ClusterQueueEmpty(name kueue.ClusterQueueReference) bool {
	c.RLock()
	defer c.RUnlock()
	cq := c.hm.ClusterQueue(name)
	if cq == nil {
		return true
	}
	return len(cq.Workloads) == 0
}

func (c *Cache) AddClusterQueue(ctx context.Context, cq *kueue.ClusterQueue) error {
	c.Lock()
	defer c.Unlock()

	if oldCq := c.hm.ClusterQueue(kueue.ClusterQueueReference(cq.Name)); oldCq != nil {
		return errors.New("ClusterQueue already exists")
	}
	log := ctrl.LoggerFrom(ctx)
	cqImpl, err := c.newClusterQueue(log, cq)
	if err != nil {
		return err
	}

	// On controller restart, an add ClusterQueue event may come after
	// add queue and workload, so here we explicitly list and add existing queues
	// and workloads.
	var queues kueue.LocalQueueList
	if err := c.client.List(ctx, &queues, client.MatchingFields{utilindexer.QueueClusterQueueKey: cq.Name}); err != nil {
		return fmt.Errorf("listing queues that match the clusterQueue: %w", err)
	}
	for _, q := range queues.Items {
		qKey := queueKey(&q)
		qImpl := &LocalQueue{
			key:                qKey,
			reservingWorkloads: 0,
			admittedWorkloads:  0,
			totalReserved:      make(resources.FlavorResourceQuantities),
			admittedUsage:      make(resources.FlavorResourceQuantities),
		}
		qImpl.resetFlavorsAndResources(cqImpl.resourceNode.Usage, cqImpl.AdmittedUsage)
		cqImpl.localQueues[qKey] = qImpl
	}
	var workloads kueue.WorkloadList
	if err := c.client.List(ctx, &workloads, client.MatchingFields{utilindexer.WorkloadClusterQueueKey: cq.Name}); err != nil {
		return fmt.Errorf("listing workloads that match the queue: %w", err)
	}
	for i, w := range workloads.Items {
		log := log.WithValues("workload", workload.Key(&w))
		if !workload.HasQuotaReservation(&w) || workload.IsFinished(&w) {
			continue
		}
		if _, err := c.addOrUpdateWorkloadWithoutLock(log, &workloads.Items[i]); err != nil {
			log.Error(err, "Workload found to be matching the ClusterQueue but failed to be added to it")
			return err
		}
	}

	return nil
}

func (c *Cache) UpdateClusterQueue(log logr.Logger, cq *kueue.ClusterQueue) error {
	c.Lock()
	defer c.Unlock()
	cqImpl := c.hm.ClusterQueue(kueue.ClusterQueueReference(cq.Name))
	if cqImpl == nil {
		return ErrCqNotFound
	}
	oldParent := cqImpl.Parent()
	c.hm.UpdateClusterQueueEdge(kueue.ClusterQueueReference(cq.Name), cq.Spec.CohortName)
	if err := cqImpl.updateClusterQueue(log, cq, c.resourceFlavors, c.admissionChecks, oldParent); err != nil {
		return err
	}
	for _, qImpl := range cqImpl.localQueues {
		if qImpl == nil {
			return errQNotFound
		}
		qImpl.resetFlavorsAndResources(cqImpl.resourceNode.Usage, cqImpl.AdmittedUsage)
	}
	return nil
}

func (c *Cache) DeleteClusterQueue(cq *kueue.ClusterQueue) {
	c.Lock()
	defer c.Unlock()
	cqName := kueue.ClusterQueueReference(cq.Name)
	curCq := c.hm.ClusterQueue(cqName)
	if curCq == nil {
		return
	}
	if features.Enabled(features.LocalQueueMetrics) {
		for _, q := range curCq.localQueues {
			namespace, lqName := queue.MustParseLocalQueueReference(q.key)
			metrics.ClearLocalQueueCacheMetrics(metrics.LocalQueueReference{
				Name:      lqName,
				Namespace: namespace,
			})
		}
	}

	parent := curCq.Parent()

	c.hm.DeleteClusterQueue(cqName)
	metrics.ClearCacheMetrics(cq.Name)

	// Update cohort resources after deletion
	if parent != nil {
		updateCohortTreeResourcesIfNoCycle(parent)
	}
}

func (c *Cache) AddOrUpdateCohort(apiCohort *kueue.Cohort) error {
	c.Lock()
	defer c.Unlock()
	cohortName := kueue.CohortReference(apiCohort.Name)
	c.hm.AddCohort(cohortName)
	cohort := c.hm.Cohort(cohortName)
	oldParent := cohort.Parent()
	c.hm.UpdateCohortEdge(cohortName, apiCohort.Spec.ParentName)
	return cohort.updateCohort(apiCohort, oldParent)
}

func (c *Cache) DeleteCohort(cohortName kueue.CohortReference) {
	c.Lock()
	defer c.Unlock()
	c.hm.DeleteCohort(cohortName)

	// If the cohort still exists after deletion, it means
	// that it has one or more children referencing it.
	// We need to run update algorithm.
	if cohort := c.hm.Cohort(cohortName); cohort != nil {
		updateCohortResourceNode(cohort)
	}
}

func (c *Cache) AddLocalQueue(q *kueue.LocalQueue) error {
	c.Lock()
	defer c.Unlock()
	cq := c.hm.ClusterQueue(q.Spec.ClusterQueue)
	if cq == nil {
		return nil
	}
	return cq.addLocalQueue(q)
}

func (c *Cache) DeleteLocalQueue(q *kueue.LocalQueue) {
	c.Lock()
	defer c.Unlock()
	cq := c.hm.ClusterQueue(q.Spec.ClusterQueue)
	if cq == nil {
		return
	}
	cq.deleteLocalQueue(q)
}

func (c *Cache) GetCacheLocalQueue(cqName kueue.ClusterQueueReference, lqKey queue.LocalQueueReference) (*LocalQueue, error) {
	c.RLock()
	defer c.RUnlock()
	cq := c.hm.ClusterQueue(cqName)
	if cq == nil {
		return nil, ErrCqNotFound
	}
	if cacheLq, ok := cq.localQueues[lqKey]; ok {
		return cacheLq, nil
	}
	return nil, errQNotFound
}

func (c *Cache) ClusterQueueUsesAdmissionFairSharing(cqName kueue.ClusterQueueReference) bool {
	c.RLock()
	defer c.RUnlock()
	cq := c.hm.ClusterQueue(cqName)
	if cq == nil || cq.AdmissionScope == nil {
		return false
	}
	return cq.AdmissionScope.AdmissionMode == kueue.UsageBasedAdmissionFairSharing
}

func (c *Cache) UpdateLocalQueue(oldQ, newQ *kueue.LocalQueue) error {
	if oldQ.Spec.ClusterQueue == newQ.Spec.ClusterQueue {
		return nil
	}
	c.Lock()
	defer c.Unlock()
	cq := c.hm.ClusterQueue(oldQ.Spec.ClusterQueue)
	if cq != nil {
		cq.deleteLocalQueue(oldQ)
	}
	cq = c.hm.ClusterQueue(newQ.Spec.ClusterQueue)
	if cq != nil {
		return cq.addLocalQueue(newQ)
	}
	return nil
}

func (c *Cache) AddOrUpdateWorkload(log logr.Logger, w *kueue.Workload) bool {
	c.Lock()
	defer c.Unlock()
	updated, err := c.addOrUpdateWorkloadWithoutLock(log, w)
	if err != nil {
		log.Error(err, "Updating workload in cache")
	}
	return updated
}

func (c *Cache) addOrUpdateWorkloadWithoutLock(log logr.Logger, wl *kueue.Workload) (bool, error) {
	wlKey := workload.Key(wl)
	assignedCqName, assigned := c.workloadAssignedQueues[wlKey]

	if !workload.HasQuotaReservation(wl) {
		if assigned {
			c.deleteFromQueueIfPresent(log, wlKey, assignedCqName)
			delete(c.workloadAssignedQueues, wlKey)
		}
		return false, nil
	}

	cq := c.hm.ClusterQueue(wl.Status.Admission.ClusterQueue)
	if cq == nil {
		return false, ErrCqNotFound
	}

	if assigned && assignedCqName != cq.Name {
		c.deleteFromQueueIfPresent(log, wlKey, assignedCqName)
	}

	if c.podsReadyTracking {
		c.podsReadyCond.Broadcast()
	}

	c.workloadAssignedQueues[wlKey] = cq.Name
	cq.addOrUpdateWorkload(log, wl)

	return true, nil
}

func (c *Cache) deleteFromQueueIfPresent(log logr.Logger, wlKey workload.Reference, cqName kueue.ClusterQueueReference) {
	if cq := c.hm.ClusterQueue(cqName); cq != nil {
		cq.deleteWorkload(log, wlKey)
	}
}

func (c *Cache) DeleteWorkload(log logr.Logger, wlKey workload.Reference) error {
	c.Lock()
	defer c.Unlock()

	cqName, assigned := c.workloadAssignedQueues[wlKey]
	if !assigned {
		return nil
	}

	cq := c.hm.ClusterQueue(cqName)
	if cq == nil {
		return ErrCqNotFound
	}

	cq.forgetWorkload(log, wlKey)
	delete(c.workloadAssignedQueues, wlKey)

	if c.podsReadyTracking {
		c.podsReadyCond.Broadcast()
	}

	return nil
}

func (c *Cache) IsAdded(w workload.Info) bool {
	c.RLock()
	defer c.RUnlock()

	k := workload.Key(w.Obj)
	if cq := c.hm.ClusterQueue(w.ClusterQueue); cq != nil {
		if _, admitted := cq.Workloads[k]; admitted {
			return true
		}
	}
	return false
}

type ClusterQueueUsageStats struct {
	ReservedResources  []kueue.FlavorUsage
	ReservingWorkloads int
	AdmittedResources  []kueue.FlavorUsage
	AdmittedWorkloads  int
	WeightedShare      float64
}

// Usage reports the reserved and admitted resources and number of workloads holding them in the ClusterQueue.
func (c *Cache) Usage(cqObj *kueue.ClusterQueue) (*ClusterQueueUsageStats, error) {
	c.RLock()
	defer c.RUnlock()

	cq := c.hm.ClusterQueue(kueue.ClusterQueueReference(cqObj.Name))
	if cq == nil {
		return nil, ErrCqNotFound
	}

	stats := &ClusterQueueUsageStats{
		ReservedResources:  getUsage(cq.resourceNode.Usage, cq),
		ReservingWorkloads: len(cq.Workloads),
		AdmittedResources:  getUsage(cq.AdmittedUsage, cq),
		AdmittedWorkloads:  cq.admittedWorkloadsCount,
	}

	if c.fairSharingEnabled {
		drs := dominantResourceShare(cq, nil)
		stats.WeightedShare = drs.PreciseWeightedShare()
	}
	return stats, nil
}

type CohortUsageStats struct {
	WeightedShare float64
}

func (c *Cache) CohortStats(cohortObj *kueue.Cohort) (*CohortUsageStats, error) {
	c.RLock()
	defer c.RUnlock()

	cohort := c.hm.Cohort(kueue.CohortReference(cohortObj.Name))
	if cohort == nil {
		return nil, ErrCohortNotFound
	}

	stats := &CohortUsageStats{}
	if c.fairSharingEnabled {
		drs := dominantResourceShare(cohort, nil)
		stats.WeightedShare = drs.PreciseWeightedShare()
	}

	return stats, nil
}

// ClusterQueueAncestors returns all ancestors (Cohorts), excluding the root,
// for a given ClusterQueue. If the ClusterQueue contains a Cohort cycle, it
// returns ErrCohortHasCycle.
func (c *Cache) ClusterQueueAncestors(cqObj *kueue.ClusterQueue) ([]kueue.CohortReference, error) {
	c.RLock()
	defer c.RUnlock()

	if cqObj.Spec.CohortName == "" {
		return nil, nil
	}

	cohort := c.hm.Cohort(cqObj.Spec.CohortName)
	if cohort == nil {
		return nil, nil
	}
	if hierarchy.HasCycle(cohort) {
		return nil, ErrCohortHasCycle
	}

	var ancestors []kueue.CohortReference

	for ancestor := range cohort.PathSelfToRoot() {
		ancestors = append(ancestors, ancestor.Name)
	}

	return ancestors[:len(ancestors)-1], nil
}

func getUsage(frq resources.FlavorResourceQuantities, cq *clusterQueue) []kueue.FlavorUsage {
	usage := make([]kueue.FlavorUsage, 0, len(frq))
	for _, rg := range cq.ResourceGroups {
		for _, fName := range rg.Flavors {
			outFlvUsage := kueue.FlavorUsage{
				Name:      fName,
				Resources: make([]kueue.ResourceUsage, 0, len(rg.CoveredResources)),
			}
			for rName := range rg.CoveredResources {
				fr := resources.FlavorResource{Flavor: fName, Resource: rName}
				rQuota := cq.resourceNode.Quotas[fr]
				used := frq[fr]
				rUsage := kueue.ResourceUsage{
					Name:  rName,
					Total: resources.ResourceQuantity(rName, used),
				}
				// Enforce `borrowed=0` if the clusterQueue doesn't belong to a cohort.
				if cq.HasParent() {
					borrowed := used - rQuota.Nominal
					if borrowed > 0 {
						rUsage.Borrowed = resources.ResourceQuantity(rName, borrowed)
					}
				}
				outFlvUsage.Resources = append(outFlvUsage.Resources, rUsage)
			}
			// The resourceUsages should be in a stable order to avoid endless creation of update events.
			slices.SortFunc(outFlvUsage.Resources, func(a, b kueue.ResourceUsage) int {
				return cmp.Compare(a.Name, b.Name)
			})
			usage = append(usage, outFlvUsage)
		}
	}
	return usage
}

type LocalQueueUsageStats struct {
	ReservedResources  []kueue.LocalQueueFlavorUsage
	ReservingWorkloads int
	AdmittedResources  []kueue.LocalQueueFlavorUsage
	AdmittedWorkloads  int
}

func (c *Cache) LocalQueueUsage(qObj *kueue.LocalQueue) (*LocalQueueUsageStats, error) {
	c.RLock()
	defer c.RUnlock()

	cqImpl := c.hm.ClusterQueue(qObj.Spec.ClusterQueue)
	if cqImpl == nil {
		return &LocalQueueUsageStats{}, nil
	}
	qImpl, ok := cqImpl.localQueues[queueKey(qObj)]
	if !ok {
		return nil, errQNotFound
	}

	return &LocalQueueUsageStats{
		ReservedResources:  filterLocalQueueUsage(qImpl.totalReserved, cqImpl.ResourceGroups),
		ReservingWorkloads: qImpl.reservingWorkloads,
		AdmittedResources:  filterLocalQueueUsage(qImpl.admittedUsage, cqImpl.ResourceGroups),
		AdmittedWorkloads:  qImpl.admittedWorkloads,
	}, nil
}

func handleTASFlavor(rf *kueue.ResourceFlavor) bool {
	return features.Enabled(features.TopologyAwareScheduling) && rf.Spec.TopologyName != nil
}

func filterLocalQueueUsage(orig resources.FlavorResourceQuantities, resourceGroups []ResourceGroup) []kueue.LocalQueueFlavorUsage {
	qFlvUsages := make([]kueue.LocalQueueFlavorUsage, 0, len(orig))
	for _, rg := range resourceGroups {
		for _, fName := range rg.Flavors {
			outFlvUsage := kueue.LocalQueueFlavorUsage{
				Name:      fName,
				Resources: make([]kueue.LocalQueueResourceUsage, 0, len(rg.CoveredResources)),
			}
			for rName := range rg.CoveredResources {
				fr := resources.FlavorResource{Flavor: fName, Resource: rName}
				outFlvUsage.Resources = append(outFlvUsage.Resources, kueue.LocalQueueResourceUsage{
					Name:  rName,
					Total: resources.ResourceQuantity(rName, orig[fr]),
				})
			}
			// The resourceUsages should be in a stable order to avoid endless creation of update events.
			slices.SortFunc(outFlvUsage.Resources, func(a, b kueue.LocalQueueResourceUsage) int {
				return cmp.Compare(a.Name, b.Name)
			})
			qFlvUsages = append(qFlvUsages, outFlvUsage)
		}
	}
	return qFlvUsages
}

func (c *Cache) ClusterQueuesUsingFlavor(flavor kueue.ResourceFlavorReference) []kueue.ClusterQueueReference {
	c.RLock()
	defer c.RUnlock()
	var cqs []kueue.ClusterQueueReference

	for _, cq := range c.hm.ClusterQueues() {
		if cq.flavorInUse(flavor) {
			cqs = append(cqs, cq.Name)
		}
	}
	return cqs
}

func (c *Cache) ClusterQueuesUsingTopology(tName kueue.TopologyReference) []kueue.ClusterQueueReference {
	c.RLock()
	defer c.RUnlock()
	var cqs []kueue.ClusterQueueReference

	for _, cq := range c.hm.ClusterQueues() {
		for _, tRef := range cq.tasFlavors {
			if tRef == tName {
				cqs = append(cqs, cq.Name)
			}
		}
	}
	return cqs
}

func (c *Cache) ClusterQueuesUsingAdmissionCheck(ac kueue.AdmissionCheckReference) []kueue.ClusterQueueReference {
	c.RLock()
	defer c.RUnlock()
	var cqs []kueue.ClusterQueueReference

	for _, cq := range c.hm.ClusterQueues() {
		if _, found := cq.AdmissionChecks[ac]; found {
			cqs = append(cqs, cq.Name)
		}
	}
	return cqs
}

func (c *Cache) MatchingClusterQueues(nsLabels map[string]string) sets.Set[kueue.ClusterQueueReference] {
	c.RLock()
	defer c.RUnlock()

	cqs := sets.New[kueue.ClusterQueueReference]()
	for _, cq := range c.hm.ClusterQueues() {
		if cq.NamespaceSelector.Matches(labels.Set(nsLabels)) {
			cqs.Insert(cq.Name)
		}
	}
	return cqs
}

// Key is the key used to index the queue.
func queueKey(q *kueue.LocalQueue) queue.LocalQueueReference {
	return queue.NewLocalQueueReference(q.Namespace, kueue.LocalQueueName(q.Name))
}
