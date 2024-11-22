/*
Copyright 2022 The Kubernetes Authors.

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
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utilindexer "sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/hierarchy"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/workload"
)

var (
	ErrCqNotFound          = errors.New("cluster queue not found")
	errQNotFound           = errors.New("queue not found")
	errWorkloadNotAdmitted = errors.New("workload not admitted by a ClusterQueue")
)

const (
	pending     = metrics.CQStatusPending
	active      = metrics.CQStatusActive
	terminating = metrics.CQStatusTerminating
)

type options struct {
	workloadInfoOptions []workload.InfoOption
	podsReadyTracking   bool
	fairSharingEnabled  bool
}

// Option configures the reconciler.
type Option func(*options)

// WithPodsReadyTracking indicates the cache controller tracks the PodsReady
// condition for admitted workloads, and allows to block admission of new
// workloads until all admitted workloads are in the PodsReady condition.
func WithPodsReadyTracking(f bool) Option {
	return func(o *options) {
		o.podsReadyTracking = f
	}
}

func WithExcludedResourcePrefixes(excludedPrefixes []string) Option {
	return func(o *options) {
		o.workloadInfoOptions = append(o.workloadInfoOptions, workload.WithExcludedResourcePrefixes(excludedPrefixes))
	}
}

// WithResourceTransformations sets the resource transformations.
func WithResourceTransformations(transforms []config.ResourceTransformation) Option {
	return func(o *options) {
		o.workloadInfoOptions = append(o.workloadInfoOptions, workload.WithResourceTransformations(transforms))
	}
}

func WithFairSharing(enabled bool) Option {
	return func(o *options) {
		o.fairSharingEnabled = enabled
	}
}

var defaultOptions = options{}

// Cache keeps track of the Workloads that got admitted through ClusterQueues.
type Cache struct {
	sync.RWMutex
	podsReadyCond sync.Cond

	client              client.Client
	assumedWorkloads    map[string]string
	resourceFlavors     map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor
	podsReadyTracking   bool
	admissionChecks     map[string]AdmissionCheck
	workloadInfoOptions []workload.InfoOption
	fairSharingEnabled  bool

	hm hierarchy.Manager[*clusterQueue, *cohort]

	tasCache TASCache
}

func New(client client.Client, opts ...Option) *Cache {
	options := defaultOptions
	for _, opt := range opts {
		opt(&options)
	}
	c := &Cache{
		client:              client,
		assumedWorkloads:    make(map[string]string),
		resourceFlavors:     make(map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor),
		admissionChecks:     make(map[string]AdmissionCheck),
		podsReadyTracking:   options.podsReadyTracking,
		workloadInfoOptions: options.workloadInfoOptions,
		fairSharingEnabled:  options.fairSharingEnabled,
		hm:                  hierarchy.NewManager[*clusterQueue, *cohort](newCohort),
		tasCache:            NewTASCache(client),
	}
	c.podsReadyCond.L = &c.RWMutex
	return c
}

func (c *Cache) newClusterQueue(cq *kueue.ClusterQueue) (*clusterQueue, error) {
	cqImpl := &clusterQueue{
		Name:                cq.Name,
		Workloads:           make(map[string]*workload.Info),
		WorkloadsNotReady:   sets.New[string](),
		localQueues:         make(map[string]*queue),
		podsReadyTracking:   c.podsReadyTracking,
		workloadInfoOptions: c.workloadInfoOptions,
		AdmittedUsage:       make(resources.FlavorResourceQuantities),
		resourceNode:        NewResourceNode(),
		tasCache:            &c.tasCache,
	}
	c.hm.AddClusterQueue(cqImpl)
	c.hm.UpdateClusterQueueEdge(cq.Name, cq.Spec.Cohort)
	if err := cqImpl.updateClusterQueue(c.hm.CycleChecker, cq, c.resourceFlavors, c.admissionChecks, nil); err != nil {
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
	for _, cq := range c.hm.ClusterQueues {
		if len(cq.WorkloadsNotReady) > 0 {
			log.V(3).Info("There is a ClusterQueue with not ready workloads", "clusterQueue", klog.KRef("", cq.Name))
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

func (c *Cache) updateClusterQueues() sets.Set[string] {
	cqs := sets.New[string]()

	for _, cq := range c.hm.ClusterQueues {
		prevStatus := cq.Status
		// We call update on all ClusterQueues irrespective of which CQ actually use this flavor
		// because it is not expensive to do so, and is not worth tracking which ClusterQueues use
		// which flavors.
		cq.UpdateWithFlavors(c.resourceFlavors)
		cq.updateWithAdmissionChecks(c.admissionChecks)
		curStatus := cq.Status
		if prevStatus == pending && curStatus == active {
			cqs.Insert(cq.Name)
		}
	}
	return cqs
}

func (c *Cache) ActiveClusterQueues() sets.Set[string] {
	c.RLock()
	defer c.RUnlock()
	cqs := sets.New[string]()
	for _, cq := range c.hm.ClusterQueues {
		if cq.Status == active {
			cqs.Insert(cq.Name)
		}
	}
	return cqs
}

func (c *Cache) TASCache() *TASCache {
	return &c.tasCache
}

func (c *Cache) AddOrUpdateResourceFlavor(rf *kueue.ResourceFlavor) sets.Set[string] {
	c.Lock()
	defer c.Unlock()
	c.resourceFlavors[kueue.ResourceFlavorReference(rf.Name)] = rf
	return c.updateClusterQueues()
}

func (c *Cache) DeleteResourceFlavor(rf *kueue.ResourceFlavor) sets.Set[string] {
	c.Lock()
	defer c.Unlock()
	delete(c.resourceFlavors, kueue.ResourceFlavorReference(rf.Name))
	return c.updateClusterQueues()
}

func (c *Cache) AddOrUpdateAdmissionCheck(ac *kueue.AdmissionCheck) sets.Set[string] {
	c.Lock()
	defer c.Unlock()

	newAC := AdmissionCheck{
		Active:     apimeta.IsStatusConditionTrue(ac.Status.Conditions, kueue.AdmissionCheckActive),
		Controller: ac.Spec.ControllerName,
	}
	if features.Enabled(features.AdmissionCheckValidationRules) {
		newAC.SingleInstanceInClusterQueue = apimeta.IsStatusConditionTrue(ac.Status.Conditions, kueue.AdmissionChecksSingleInstanceInClusterQueue)
		newAC.FlavorIndependent = apimeta.IsStatusConditionTrue(ac.Status.Conditions, kueue.FlavorIndependentAdmissionCheck)
	} else if ac.Spec.ControllerName == kueue.MultiKueueControllerName {
		newAC.SingleInstanceInClusterQueue = true
		newAC.FlavorIndependent = true
	}
	c.admissionChecks[ac.Name] = newAC

	return c.updateClusterQueues()
}

func (c *Cache) DeleteAdmissionCheck(ac *kueue.AdmissionCheck) sets.Set[string] {
	c.Lock()
	defer c.Unlock()
	delete(c.admissionChecks, ac.Name)
	return c.updateClusterQueues()
}

func (c *Cache) AdmissionChecksForClusterQueue(cqName string) []AdmissionCheck {
	c.RLock()
	defer c.RUnlock()
	cq, ok := c.hm.ClusterQueues[cqName]
	if !ok || len(cq.AdmissionChecks) == 0 {
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

func (c *Cache) ClusterQueueActive(name string) bool {
	return c.clusterQueueInStatus(name, active)
}

func (c *Cache) ClusterQueueTerminating(name string) bool {
	return c.clusterQueueInStatus(name, terminating)
}

func (c *Cache) ClusterQueueReadiness(name string) (metav1.ConditionStatus, string, string) {
	c.RLock()
	defer c.RUnlock()
	cq := c.hm.ClusterQueues[name]
	if cq == nil {
		return metav1.ConditionFalse, "NotFound", "ClusterQueue not found"
	}
	if cq.Status == active {
		return metav1.ConditionTrue, "Ready", "Can admit new workloads"
	}
	reason, msg := cq.inactiveReason()
	return metav1.ConditionFalse, reason, msg
}

func (c *Cache) clusterQueueInStatus(name string, status metrics.ClusterQueueStatus) bool {
	c.RLock()
	defer c.RUnlock()

	cq, exists := c.hm.ClusterQueues[name]
	if !exists {
		return false
	}
	return cq != nil && cq.Status == status
}

func (c *Cache) TerminateClusterQueue(name string) {
	c.Lock()
	defer c.Unlock()
	if cq, exists := c.hm.ClusterQueues[name]; exists {
		cq.Status = terminating
		metrics.ReportClusterQueueStatus(cq.Name, cq.Status)
	}
}

// ClusterQueueEmpty indicates whether there's any active workload admitted by
// the provided clusterQueue.
// Return true if the clusterQueue doesn't exist.
func (c *Cache) ClusterQueueEmpty(name string) bool {
	c.RLock()
	defer c.RUnlock()
	cq, exists := c.hm.ClusterQueues[name]
	if !exists {
		return true
	}
	return len(cq.Workloads) == 0
}

func (c *Cache) AddClusterQueue(ctx context.Context, cq *kueue.ClusterQueue) error {
	c.Lock()
	defer c.Unlock()

	if _, ok := c.hm.ClusterQueues[cq.Name]; ok {
		return errors.New("ClusterQueue already exists")
	}
	cqImpl, err := c.newClusterQueue(cq)
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
		qImpl := &queue{
			key:                qKey,
			reservingWorkloads: 0,
			admittedWorkloads:  0,
			//TODO: rename this to better distinguish between reserved and in use quantities
			usage:         make(resources.FlavorResourceQuantities),
			admittedUsage: make(resources.FlavorResourceQuantities),
		}
		qImpl.resetFlavorsAndResources(cqImpl.resourceNode.Usage, cqImpl.AdmittedUsage)
		cqImpl.localQueues[qKey] = qImpl
	}
	var workloads kueue.WorkloadList
	if err := c.client.List(ctx, &workloads, client.MatchingFields{utilindexer.WorkloadClusterQueueKey: cq.Name}); err != nil {
		return fmt.Errorf("listing workloads that match the queue: %w", err)
	}
	for i, w := range workloads.Items {
		if !workload.HasQuotaReservation(&w) || workload.IsFinished(&w) {
			continue
		}
		c.addOrUpdateWorkload(&workloads.Items[i])
	}

	return nil
}

func (c *Cache) UpdateClusterQueue(cq *kueue.ClusterQueue) error {
	c.Lock()
	defer c.Unlock()
	cqImpl, ok := c.hm.ClusterQueues[cq.Name]
	if !ok {
		return ErrCqNotFound
	}
	oldParent := cqImpl.Parent()
	c.hm.UpdateClusterQueueEdge(cq.Name, cq.Spec.Cohort)
	if err := cqImpl.updateClusterQueue(c.hm.CycleChecker, cq, c.resourceFlavors, c.admissionChecks, oldParent); err != nil {
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
	_, ok := c.hm.ClusterQueues[cq.Name]
	if !ok {
		return
	}
	c.hm.DeleteClusterQueue(cq.Name)
	metrics.ClearCacheMetrics(cq.Name)
}

func (c *Cache) AddOrUpdateCohort(apiCohort *kueuealpha.Cohort) error {
	c.Lock()
	defer c.Unlock()
	c.hm.AddCohort(apiCohort.Name)
	cohort := c.hm.Cohorts[apiCohort.Name]
	oldParent := cohort.Parent()
	c.hm.UpdateCohortEdge(apiCohort.Name, apiCohort.Spec.Parent)
	return cohort.updateCohort(c.hm.CycleChecker, apiCohort, oldParent)
}

func (c *Cache) DeleteCohort(cohortName string) {
	c.Lock()
	defer c.Unlock()
	c.hm.DeleteCohort(cohortName)

	// If the cohort still exists after deletion, it means
	// that it has one or more children referencing it.
	// We need to run update algorithm.
	if cohort, ok := c.hm.Cohorts[cohortName]; ok {
		updateCohortResourceNode(cohort)
	}
}

func (c *Cache) AddLocalQueue(q *kueue.LocalQueue) error {
	c.Lock()
	defer c.Unlock()
	cq, ok := c.hm.ClusterQueues[string(q.Spec.ClusterQueue)]
	if !ok {
		return nil
	}
	return cq.addLocalQueue(q)
}

func (c *Cache) DeleteLocalQueue(q *kueue.LocalQueue) {
	c.Lock()
	defer c.Unlock()
	cq, ok := c.hm.ClusterQueues[string(q.Spec.ClusterQueue)]
	if !ok {
		return
	}
	cq.deleteLocalQueue(q)
}

func (c *Cache) UpdateLocalQueue(oldQ, newQ *kueue.LocalQueue) error {
	if oldQ.Spec.ClusterQueue == newQ.Spec.ClusterQueue {
		return nil
	}
	c.Lock()
	defer c.Unlock()
	cq, ok := c.hm.ClusterQueues[string(oldQ.Spec.ClusterQueue)]
	if ok {
		cq.deleteLocalQueue(oldQ)
	}
	cq, ok = c.hm.ClusterQueues[string(newQ.Spec.ClusterQueue)]
	if ok {
		return cq.addLocalQueue(newQ)
	}
	return nil
}

func (c *Cache) AddOrUpdateWorkload(w *kueue.Workload) bool {
	c.Lock()
	defer c.Unlock()
	return c.addOrUpdateWorkload(w)
}

func (c *Cache) addOrUpdateWorkload(w *kueue.Workload) bool {
	if !workload.HasQuotaReservation(w) {
		return false
	}

	clusterQueue, ok := c.hm.ClusterQueues[string(w.Status.Admission.ClusterQueue)]
	if !ok {
		return false
	}

	c.cleanupAssumedState(w)

	if _, exist := clusterQueue.Workloads[workload.Key(w)]; exist {
		clusterQueue.deleteWorkload(w)
	}

	if c.podsReadyTracking {
		c.podsReadyCond.Broadcast()
	}
	return clusterQueue.addWorkload(w) == nil
}

func (c *Cache) UpdateWorkload(oldWl, newWl *kueue.Workload) error {
	c.Lock()
	defer c.Unlock()
	if workload.HasQuotaReservation(oldWl) {
		cq, ok := c.hm.ClusterQueues[string(oldWl.Status.Admission.ClusterQueue)]
		if !ok {
			return errors.New("old ClusterQueue doesn't exist")
		}
		cq.deleteWorkload(oldWl)
	}
	c.cleanupAssumedState(oldWl)

	if !workload.HasQuotaReservation(newWl) {
		return nil
	}
	cq, ok := c.hm.ClusterQueues[string(newWl.Status.Admission.ClusterQueue)]
	if !ok {
		return errors.New("new ClusterQueue doesn't exist")
	}
	if c.podsReadyTracking {
		c.podsReadyCond.Broadcast()
	}
	return cq.addWorkload(newWl)
}

func (c *Cache) DeleteWorkload(w *kueue.Workload) error {
	c.Lock()
	defer c.Unlock()

	cq := c.clusterQueueForWorkload(w)
	if cq == nil {
		return ErrCqNotFound
	}

	c.cleanupAssumedState(w)

	cq.deleteWorkload(w)
	if c.podsReadyTracking {
		c.podsReadyCond.Broadcast()
	}
	return nil
}

func (c *Cache) IsAssumedOrAdmittedWorkload(w workload.Info) bool {
	c.RLock()
	defer c.RUnlock()

	k := workload.Key(w.Obj)
	if _, assumed := c.assumedWorkloads[k]; assumed {
		return true
	}
	if cq, exists := c.hm.ClusterQueues[w.ClusterQueue]; exists {
		if _, admitted := cq.Workloads[k]; admitted {
			return true
		}
	}
	return false
}

func (c *Cache) AssumeWorkload(w *kueue.Workload) error {
	c.Lock()
	defer c.Unlock()

	if !workload.HasQuotaReservation(w) {
		return errWorkloadNotAdmitted
	}

	k := workload.Key(w)
	assumedCq, assumed := c.assumedWorkloads[k]
	if assumed {
		return fmt.Errorf("the workload is already assumed to ClusterQueue %q", assumedCq)
	}

	cq, ok := c.hm.ClusterQueues[string(w.Status.Admission.ClusterQueue)]
	if !ok {
		return ErrCqNotFound
	}

	if err := cq.addWorkload(w); err != nil {
		return err
	}
	c.assumedWorkloads[k] = string(w.Status.Admission.ClusterQueue)
	return nil
}

func (c *Cache) ForgetWorkload(w *kueue.Workload) error {
	c.Lock()
	defer c.Unlock()

	if _, assumed := c.assumedWorkloads[workload.Key(w)]; !assumed {
		return errors.New("the workload is not assumed")
	}
	c.cleanupAssumedState(w)

	if !workload.HasQuotaReservation(w) {
		return errWorkloadNotAdmitted
	}

	cq, ok := c.hm.ClusterQueues[string(w.Status.Admission.ClusterQueue)]
	if !ok {
		return ErrCqNotFound
	}
	cq.deleteWorkload(w)
	if c.podsReadyTracking {
		c.podsReadyCond.Broadcast()
	}
	return nil
}

type ClusterQueueUsageStats struct {
	ReservedResources  []kueue.FlavorUsage
	ReservingWorkloads int
	AdmittedResources  []kueue.FlavorUsage
	AdmittedWorkloads  int
	WeightedShare      int64
}

// Usage reports the reserved and admitted resources and number of workloads holding them in the ClusterQueue.
func (c *Cache) Usage(cqObj *kueue.ClusterQueue) (*ClusterQueueUsageStats, error) {
	c.RLock()
	defer c.RUnlock()

	cq := c.hm.ClusterQueues[cqObj.Name]
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
		weightedShare, _ := dominantResourceShare(cq, nil, 0)
		stats.WeightedShare = int64(weightedShare)
	}

	return stats, nil
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
				rQuota := cq.QuotaFor(fr)
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
			sort.Slice(outFlvUsage.Resources, func(i, j int) bool {
				return outFlvUsage.Resources[i].Name < outFlvUsage.Resources[j].Name
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
	Flavors            []kueue.LocalQueueFlavorStatus
}

func (c *Cache) LocalQueueUsage(qObj *kueue.LocalQueue) (*LocalQueueUsageStats, error) {
	c.RLock()
	defer c.RUnlock()

	cqImpl, ok := c.hm.ClusterQueues[string(qObj.Spec.ClusterQueue)]
	if !ok {
		return &LocalQueueUsageStats{}, nil
	}
	qImpl, ok := cqImpl.localQueues[queueKey(qObj)]
	if !ok {
		return nil, errQNotFound
	}

	var flavors []kueue.LocalQueueFlavorStatus

	if features.Enabled(features.ExposeFlavorsInLocalQueue) {
		resourcesInFlavor := make(map[kueue.ResourceFlavorReference][]corev1.ResourceName)
		for _, rg := range cqImpl.ResourceGroups {
			for _, rgFlavor := range rg.Flavors {
				if _, ok := resourcesInFlavor[rgFlavor]; !ok {
					resourcesInFlavor[rgFlavor] = make([]corev1.ResourceName, 0, len(rg.CoveredResources))
				}
				resourcesInFlavor[rgFlavor] = append(resourcesInFlavor[rgFlavor], sets.List(rg.CoveredResources)...)
			}
		}

		for _, rg := range cqImpl.ResourceGroups {
			for _, rgFlavor := range rg.Flavors {
				flavor := kueue.LocalQueueFlavorStatus{Name: rgFlavor}
				if rif, ok := resourcesInFlavor[rgFlavor]; ok {
					flavor.Resources = append(flavor.Resources, rif...)
				}
				if rf, ok := c.resourceFlavors[rgFlavor]; ok {
					flavor.NodeLabels = rf.Spec.NodeLabels
					flavor.NodeTaints = rf.Spec.NodeTaints
				}
				flavors = append(flavors, flavor)
			}
		}
	}

	return &LocalQueueUsageStats{
		ReservedResources:  filterLocalQueueUsage(qImpl.usage, cqImpl.ResourceGroups),
		ReservingWorkloads: qImpl.reservingWorkloads,
		AdmittedResources:  filterLocalQueueUsage(qImpl.admittedUsage, cqImpl.ResourceGroups),
		AdmittedWorkloads:  qImpl.admittedWorkloads,
		Flavors:            flavors,
	}, nil
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
			sort.Slice(outFlvUsage.Resources, func(i, j int) bool {
				return outFlvUsage.Resources[i].Name < outFlvUsage.Resources[j].Name
			})
			qFlvUsages = append(qFlvUsages, outFlvUsage)
		}
	}
	return qFlvUsages
}

func (c *Cache) cleanupAssumedState(w *kueue.Workload) {
	k := workload.Key(w)
	assumedCQName, assumed := c.assumedWorkloads[k]
	if assumed {
		// If the workload's assigned ClusterQueue is different from the assumed
		// one, then we should also clean up the assumed one.
		if workload.HasQuotaReservation(w) && assumedCQName != string(w.Status.Admission.ClusterQueue) {
			if assumedCQ, exist := c.hm.ClusterQueues[assumedCQName]; exist {
				assumedCQ.deleteWorkload(w)
			}
		}
		delete(c.assumedWorkloads, k)
	}
}

func (c *Cache) clusterQueueForWorkload(w *kueue.Workload) *clusterQueue {
	if workload.HasQuotaReservation(w) {
		return c.hm.ClusterQueues[string(w.Status.Admission.ClusterQueue)]
	}
	wKey := workload.Key(w)
	for _, cq := range c.hm.ClusterQueues {
		if cq.Workloads[wKey] != nil {
			return cq
		}
	}
	return nil
}

func (c *Cache) ClusterQueuesUsingFlavor(flavor kueue.ResourceFlavorReference) []string {
	c.RLock()
	defer c.RUnlock()
	var cqs []string

	for _, cq := range c.hm.ClusterQueues {
		if cq.flavorInUse(flavor) {
			cqs = append(cqs, cq.Name)
		}
	}
	return cqs
}

func (c *Cache) ClusterQueuesUsingAdmissionCheck(ac string) []string {
	c.RLock()
	defer c.RUnlock()
	var cqs []string

	for _, cq := range c.hm.ClusterQueues {
		if _, found := cq.AdmissionChecks[ac]; found {
			cqs = append(cqs, cq.Name)
		}
	}
	return cqs
}

func (c *Cache) MatchingClusterQueues(nsLabels map[string]string) sets.Set[string] {
	c.RLock()
	defer c.RUnlock()

	cqs := sets.New[string]()
	for _, cq := range c.hm.ClusterQueues {
		if cq.NamespaceSelector.Matches(labels.Set(nsLabels)) {
			cqs.Insert(cq.Name)
		}
	}
	return cqs
}

// Key is the key used to index the queue.
func queueKey(q *kueue.LocalQueue) string {
	return fmt.Sprintf("%s/%s", q.Namespace, q.Name)
}
