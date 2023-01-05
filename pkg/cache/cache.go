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
	"sync"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha2"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/pointer"
	"sigs.k8s.io/kueue/pkg/workload"
)

const (
	workloadClusterQueueKey = "spec.admission.clusterQueue"
	queueClusterQueueKey    = "spec.clusterQueue"
)

var (
	errQueueAlreadyExists  = errors.New("queue already exists")
	errCqNotFound          = errors.New("cluster queue not found")
	errWorkloadNotAdmitted = errors.New("workload not admitted by a ClusterQueue")
)

type options struct {
	podsReadyTracking bool
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

var defaultOptions = options{}

// Cache keeps track of the Workloads that got admitted through ClusterQueues.
type Cache struct {
	sync.RWMutex
	podsReadyCond sync.Cond

	client            client.Client
	clusterQueues     map[string]*ClusterQueue
	cohorts           map[string]*Cohort
	assumedWorkloads  map[string]string
	resourceFlavors   map[string]*kueue.ResourceFlavor
	podsReadyTracking bool
}

func New(client client.Client, opts ...Option) *Cache {
	options := defaultOptions
	for _, opt := range opts {
		opt(&options)
	}
	c := &Cache{
		client:            client,
		clusterQueues:     make(map[string]*ClusterQueue),
		cohorts:           make(map[string]*Cohort),
		assumedWorkloads:  make(map[string]string),
		resourceFlavors:   make(map[string]*kueue.ResourceFlavor),
		podsReadyTracking: options.podsReadyTracking,
	}
	c.podsReadyCond.L = &c.RWMutex
	return c
}

type ResourceQuantities map[corev1.ResourceName]map[string]int64

// Cohort is a set of ClusterQueues that can borrow resources from each other.
type Cohort struct {
	Name    string
	Members sets.Set[*ClusterQueue]

	// These fields are only populated for a snapshot.
	RequestableResources ResourceQuantities
	UsedResources        ResourceQuantities
}

func newCohort(name string, size int) *Cohort {
	return &Cohort{
		Name:    name,
		Members: make(sets.Set[*ClusterQueue], size),
	}
}

const (
	pending     = metrics.CQStatusPending
	active      = metrics.CQStatusActive
	terminating = metrics.CQStatusTerminating
)

// ClusterQueue is the internal implementation of kueue.ClusterQueue that
// holds admitted workloads.
type ClusterQueue struct {
	Name                 string
	Cohort               *Cohort
	RequestableResources map[corev1.ResourceName]*Resource
	UsedResources        ResourceQuantities
	Workloads            map[string]*workload.Info
	WorkloadsNotReady    sets.Set[string]
	NamespaceSelector    labels.Selector
	// The set of key labels from all flavors of a resource.
	// Those keys define the affinity terms of a workload
	// that can be matched against the flavors.
	LabelKeys map[corev1.ResourceName]sets.Set[string]
	Status    metrics.ClusterQueueStatus

	// The following fields are not populated in a snapshot.

	admittedWorkloadsPerQueue map[string]int
	podsReadyTracking         bool
}

type Resource struct {
	CodependentResources sets.Set[corev1.ResourceName]
	Flavors              []FlavorLimits
}

func (r *Resource) matchesFlavors(other *Resource) bool {
	if len(r.Flavors) != len(other.Flavors) {
		return false
	}
	for i := range r.Flavors {
		if r.Flavors[i].Name != other.Flavors[i].Name {
			return false
		}
	}
	return true
}

// FlavorLimits holds a processed ClusterQueue flavor quota.
type FlavorLimits struct {
	Name string
	Min  int64
	Max  *int64
}

func (c *Cache) newClusterQueue(cq *kueue.ClusterQueue) (*ClusterQueue, error) {
	cqImpl := &ClusterQueue{
		Name:                      cq.Name,
		Workloads:                 make(map[string]*workload.Info),
		WorkloadsNotReady:         sets.New[string](),
		admittedWorkloadsPerQueue: make(map[string]int),
		podsReadyTracking:         c.podsReadyTracking,
	}
	if err := cqImpl.update(cq, c.resourceFlavors); err != nil {
		return nil, err
	}

	return cqImpl, nil
}

// WaitForPodsReady waits for all admitted workloads to be in the PodsReady condition
// if podsReadyTracking is enabled. Otherwise returns immediately.
func (c *Cache) WaitForPodsReady(ctx context.Context) {
	if !c.podsReadyTracking {
		return
	}

	c.Lock()
	defer c.Unlock()

	log := ctrl.LoggerFrom(ctx)
	for {
		if c.podsReadyForAllAdmittedWorkloads(ctx) {
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

func (c *Cache) PodsReadyForAllAdmittedWorkloads(ctx context.Context) bool {
	c.Lock()
	defer c.Unlock()
	return c.podsReadyForAllAdmittedWorkloads(ctx)
}

func (c *Cache) podsReadyForAllAdmittedWorkloads(ctx context.Context) bool {
	log := ctrl.LoggerFrom(ctx)
	for _, cq := range c.clusterQueues {
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
	c.podsReadyCond.Broadcast()
}

func (c *Cache) AdmittedWorkloadsInLocalQueue(localQueue *kueue.LocalQueue) int32 {
	c.Lock()
	defer c.Unlock()
	cq, ok := c.clusterQueues[string(localQueue.Spec.ClusterQueue)]
	if !ok {
		return 0
	}
	qKey := queueKey(localQueue)
	return int32(cq.admittedWorkloadsPerQueue[qKey])
}

func (c *ClusterQueue) Active() bool {
	return c.Status == active
}

func (c *ClusterQueue) update(in *kueue.ClusterQueue, resourceFlavors map[string]*kueue.ResourceFlavor) error {
	c.RequestableResources = resourcesByName(in.Spec.Resources)
	c.UpdateCodependentResources()
	nsSelector, err := metav1.LabelSelectorAsSelector(in.Spec.NamespaceSelector)
	if err != nil {
		return err
	}
	c.NamespaceSelector = nsSelector

	usedResources := make(ResourceQuantities, len(in.Spec.Resources))
	for _, r := range in.Spec.Resources {
		if len(r.Flavors) == 0 {
			continue
		}

		existingUsedFlavors := c.UsedResources[r.Name]
		usedFlavors := make(map[string]int64, len(r.Flavors))
		for _, f := range r.Flavors {
			usedFlavors[string(f.Name)] = existingUsedFlavors[string(f.Name)]
		}
		usedResources[r.Name] = usedFlavors
	}
	c.UsedResources = usedResources
	c.UpdateWithFlavors(resourceFlavors)
	return nil
}

func (c *ClusterQueue) UpdateCodependentResources() {
	for iName, iRes := range c.RequestableResources {
		if len(iRes.CodependentResources) > 0 {
			// Already matched with other resources.
			continue
		}
		codep := sets.New[corev1.ResourceName]()
		for jName, jRes := range c.RequestableResources {
			if iName == jName || iRes.matchesFlavors(jRes) {
				codep.Insert(jName)
			}
		}
		if len(codep) > 1 {
			for name := range codep {
				c.RequestableResources[corev1.ResourceName(name)].CodependentResources = codep
			}
		}
	}
}

// UpdateWithFlavors updates a ClusterQueue based on the passed ResourceFlavors set.
// Exported only for testing.
func (c *ClusterQueue) UpdateWithFlavors(flavors map[string]*kueue.ResourceFlavor) {
	status := active
	if flavorNotFound := c.updateLabelKeys(flavors); flavorNotFound {
		status = pending
	}

	if c.Status != terminating {
		c.Status = status
	}
	metrics.ReportClusterQueueStatus(c.Name, c.Status)
}

func (c *ClusterQueue) updateLabelKeys(flavors map[string]*kueue.ResourceFlavor) bool {
	var flavorNotFound bool
	labelKeys := make(map[corev1.ResourceName]sets.Set[string])
	for rName, res := range c.RequestableResources {
		if len(res.Flavors) == 0 {
			continue
		}
		resKeys := sets.New[string]()
		for _, rf := range res.Flavors {
			if flv, exist := flavors[rf.Name]; exist {
				for k := range flv.NodeSelector {
					resKeys.Insert(k)
				}
			} else {
				flavorNotFound = true
			}
		}

		if len(resKeys) != 0 {
			labelKeys[rName] = resKeys
		}
	}

	c.LabelKeys = nil
	if len(labelKeys) != 0 {
		c.LabelKeys = labelKeys
	}

	return flavorNotFound
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
	reportAdmittedActiveWorkloads(wi.ClusterQueue, len(c.Workloads))
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
	delete(c.Workloads, k)
	reportAdmittedActiveWorkloads(wi.ClusterQueue, len(c.Workloads))
}

func (c *ClusterQueue) updateWorkloadUsage(wi *workload.Info, m int64) {
	for _, ps := range wi.TotalRequests {
		for wlRes, wlResFlv := range ps.Flavors {
			v, wlResExist := ps.Requests[wlRes]
			cqResFlv, cqResExist := c.UsedResources[wlRes]
			if cqResExist && wlResExist {
				if _, cqFlvExist := cqResFlv[wlResFlv]; cqFlvExist {
					cqResFlv[wlResFlv] += v * m
				}
			}
		}
	}
	qKey := workload.QueueKey(wi.Obj)
	if _, ok := c.admittedWorkloadsPerQueue[qKey]; ok {
		c.admittedWorkloadsPerQueue[qKey] += int(m)
	}
}

func (c *ClusterQueue) addLocalQueue(q *kueue.LocalQueue) error {
	qKey := queueKey(q)
	if _, ok := c.admittedWorkloadsPerQueue[qKey]; ok {
		return errQueueAlreadyExists
	}
	// We need to count the workloads, because they could have been added before
	// receiving the queue add event.
	workloads := 0
	for _, wl := range c.Workloads {
		if workloadBelongsToLocalQueue(wl.Obj, q) {
			workloads++
		}
	}
	c.admittedWorkloadsPerQueue[qKey] = workloads
	return nil
}

func (c *ClusterQueue) deleteLocalQueue(q *kueue.LocalQueue) {
	qKey := queueKey(q)
	delete(c.admittedWorkloadsPerQueue, qKey)
}

func (c *ClusterQueue) flavorInUse(flavor string) bool {
	for _, r := range c.RequestableResources {
		for _, f := range r.Flavors {
			if flavor == f.Name {
				return true
			}
		}
	}
	return false
}

func (c *Cache) updateClusterQueues() sets.Set[string] {
	cqs := sets.New[string]()

	for _, cq := range c.clusterQueues {
		prevStatus := cq.Status
		// We call update on all ClusterQueues irrespective of which CQ actually use this flavor
		// because it is not expensive to do so, and is not worth tracking which ClusterQueues use
		// which flavors.
		cq.UpdateWithFlavors(c.resourceFlavors)
		curStatus := cq.Status
		if prevStatus == pending && curStatus == active {
			cqs.Insert(cq.Name)
		}
	}
	return cqs
}

func (c *Cache) AddOrUpdateResourceFlavor(rf *kueue.ResourceFlavor) sets.Set[string] {
	c.Lock()
	defer c.Unlock()
	c.resourceFlavors[rf.Name] = rf
	return c.updateClusterQueues()
}

func (c *Cache) DeleteResourceFlavor(rf *kueue.ResourceFlavor) sets.Set[string] {
	c.Lock()
	defer c.Unlock()
	delete(c.resourceFlavors, rf.Name)
	return c.updateClusterQueues()
}

func (c *Cache) ClusterQueueActive(name string) bool {
	return c.clusterQueueInStatus(name, active)
}

func (c *Cache) ClusterQueueTerminating(name string) bool {
	return c.clusterQueueInStatus(name, terminating)
}

func (c *Cache) clusterQueueInStatus(name string, status metrics.ClusterQueueStatus) bool {
	c.RLock()
	defer c.RUnlock()

	cq, exists := c.clusterQueues[name]
	if !exists {
		return false
	}
	return cq != nil && cq.Status == status
}

func (c *Cache) TerminateClusterQueue(name string) {
	c.Lock()
	defer c.Unlock()
	if cq, exists := c.clusterQueues[name]; exists {
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
	cq, exists := c.clusterQueues[name]
	if !exists {
		return true
	}
	return len(cq.Workloads) == 0
}

func (c *Cache) AddClusterQueue(ctx context.Context, cq *kueue.ClusterQueue) error {
	c.Lock()
	defer c.Unlock()

	if _, ok := c.clusterQueues[cq.Name]; ok {
		return fmt.Errorf("ClusterQueue already exists")
	}
	cqImpl, err := c.newClusterQueue(cq)
	if err != nil {
		return err
	}
	c.addClusterQueueToCohort(cqImpl, cq.Spec.Cohort)
	c.clusterQueues[cq.Name] = cqImpl

	// On controller restart, an add ClusterQueue event may come after
	// add queue and workload, so here we explicitly list and add existing queues
	// and workloads.
	var queues kueue.LocalQueueList
	if err := c.client.List(ctx, &queues, client.MatchingFields{queueClusterQueueKey: cq.Name}); err != nil {
		return fmt.Errorf("listing queues that match the clusterQueue: %w", err)
	}
	for _, q := range queues.Items {
		// Checking ClusterQueue name again because the field index is not available in tests.
		if string(q.Spec.ClusterQueue) == cq.Name {
			cqImpl.admittedWorkloadsPerQueue[queueKey(&q)] = 0
		}
	}
	var workloads kueue.WorkloadList
	if err := c.client.List(ctx, &workloads, client.MatchingFields{workloadClusterQueueKey: cq.Name}); err != nil {
		return fmt.Errorf("listing workloads that match the queue: %w", err)
	}
	for i, w := range workloads.Items {
		// Checking ClusterQueue name again because the field index is not available in tests.
		if w.Spec.Admission == nil || string(w.Spec.Admission.ClusterQueue) != cq.Name {
			continue
		}
		c.addOrUpdateWorkload(&workloads.Items[i])
		if _, ok := cqImpl.admittedWorkloadsPerQueue[w.Spec.QueueName]; ok {
			cqImpl.admittedWorkloadsPerQueue[w.Spec.QueueName]++
		}
	}

	return nil
}

func (c *Cache) UpdateClusterQueue(cq *kueue.ClusterQueue) error {
	c.Lock()
	defer c.Unlock()
	cqImpl, ok := c.clusterQueues[cq.Name]
	if !ok {
		return errCqNotFound
	}
	if err := cqImpl.update(cq, c.resourceFlavors); err != nil {
		return err
	}

	if cqImpl.Cohort == nil {
		c.addClusterQueueToCohort(cqImpl, cq.Spec.Cohort)
		return nil
	}

	if cqImpl.Cohort.Name != cq.Spec.Cohort {
		c.deleteClusterQueueFromCohort(cqImpl)
		c.addClusterQueueToCohort(cqImpl, cq.Spec.Cohort)
	}
	return nil
}

func (c *Cache) DeleteClusterQueue(cq *kueue.ClusterQueue) {
	c.Lock()
	defer c.Unlock()
	cqImpl, ok := c.clusterQueues[cq.Name]
	if !ok {
		return
	}
	c.deleteClusterQueueFromCohort(cqImpl)
	delete(c.clusterQueues, cq.Name)
	metrics.ClearCacheMetrics(cq.Name)
}

func (c *Cache) AddLocalQueue(q *kueue.LocalQueue) error {
	c.Lock()
	defer c.Unlock()
	cq, ok := c.clusterQueues[string(q.Spec.ClusterQueue)]
	if !ok {
		return nil
	}
	return cq.addLocalQueue(q)
}

func (c *Cache) DeleteLocalQueue(q *kueue.LocalQueue) {
	c.Lock()
	defer c.Unlock()
	cq, ok := c.clusterQueues[string(q.Spec.ClusterQueue)]
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
	cq, ok := c.clusterQueues[string(oldQ.Spec.ClusterQueue)]
	if ok {
		cq.deleteLocalQueue(oldQ)
	}
	cq, ok = c.clusterQueues[string(newQ.Spec.ClusterQueue)]
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
	if w.Spec.Admission == nil {
		return false
	}

	clusterQueue, ok := c.clusterQueues[string(w.Spec.Admission.ClusterQueue)]
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
	if oldWl.Spec.Admission != nil {
		cq, ok := c.clusterQueues[string(oldWl.Spec.Admission.ClusterQueue)]
		if !ok {
			return fmt.Errorf("old ClusterQueue doesn't exist")
		}
		cq.deleteWorkload(oldWl)
	}
	c.cleanupAssumedState(oldWl)

	if newWl.Spec.Admission == nil {
		return nil
	}
	cq, ok := c.clusterQueues[string(newWl.Spec.Admission.ClusterQueue)]
	if !ok {
		return fmt.Errorf("new ClusterQueue doesn't exist")
	}
	if c.podsReadyTracking {
		c.podsReadyCond.Broadcast()
	}
	return cq.addWorkload(newWl)
}

func (c *Cache) DeleteWorkload(w *kueue.Workload) error {
	c.Lock()
	defer c.Unlock()
	if w.Spec.Admission == nil {
		return errWorkloadNotAdmitted
	}

	cq, ok := c.clusterQueues[string(w.Spec.Admission.ClusterQueue)]
	if !ok {
		return errCqNotFound
	}

	c.cleanupAssumedState(w)

	cq.deleteWorkload(w)
	if c.podsReadyTracking {
		c.podsReadyCond.Broadcast()
	}
	return nil
}

func (c *Cache) AssumeWorkload(w *kueue.Workload) error {
	c.Lock()
	defer c.Unlock()

	if w.Spec.Admission == nil {
		return errWorkloadNotAdmitted
	}

	k := workload.Key(w)
	assumedCq, assumed := c.assumedWorkloads[k]
	if assumed {
		return fmt.Errorf("the workload is already assumed to ClusterQueue %q", assumedCq)
	}

	cq, ok := c.clusterQueues[string(w.Spec.Admission.ClusterQueue)]
	if !ok {
		return errCqNotFound
	}

	if err := cq.addWorkload(w); err != nil {
		return err
	}
	c.assumedWorkloads[k] = string(w.Spec.Admission.ClusterQueue)
	return nil
}

func (c *Cache) ForgetWorkload(w *kueue.Workload) error {
	c.Lock()
	defer c.Unlock()

	if _, assumed := c.assumedWorkloads[workload.Key(w)]; !assumed {
		return fmt.Errorf("the workload is not assumed")
	}
	c.cleanupAssumedState(w)

	if w.Spec.Admission == nil {
		return errWorkloadNotAdmitted
	}

	cq, ok := c.clusterQueues[string(w.Spec.Admission.ClusterQueue)]
	if !ok {
		return errCqNotFound
	}
	cq.deleteWorkload(w)
	if c.podsReadyTracking {
		c.podsReadyCond.Broadcast()
	}
	return nil
}

// Usage reports the used resources and number of workloads admitted by the ClusterQueue.
func (c *Cache) Usage(cqObj *kueue.ClusterQueue) (kueue.UsedResources, int, error) {
	c.RLock()
	defer c.RUnlock()

	cq := c.clusterQueues[cqObj.Name]
	if cq == nil {
		return nil, 0, errCqNotFound
	}

	usage := make(kueue.UsedResources, len(cq.UsedResources))
	for rName, usedRes := range cq.UsedResources {
		rUsage := make(map[string]kueue.Usage)
		requestable := cq.RequestableResources[rName]
		for _, flavor := range requestable.Flavors {
			used := usedRes[flavor.Name]
			fUsage := kueue.Usage{
				Total: pointer.Quantity(workload.ResourceQuantity(rName, used)),
			}
			borrowing := used - flavor.Min
			if borrowing > 0 {
				fUsage.Borrowed = pointer.Quantity(workload.ResourceQuantity(rName, borrowing))
			}
			rUsage[flavor.Name] = fUsage
		}
		usage[rName] = rUsage
	}
	return usage, len(cq.Workloads), nil
}

func (c *Cache) cleanupAssumedState(w *kueue.Workload) {
	k := workload.Key(w)
	assumedCQName, assumed := c.assumedWorkloads[k]
	if assumed {
		// If the workload's assigned ClusterQueue is different from the assumed
		// one, then we should also cleanup the assumed one.
		if w.Spec.Admission != nil && assumedCQName != string(w.Spec.Admission.ClusterQueue) {
			if assumedCQ, exist := c.clusterQueues[assumedCQName]; exist {
				assumedCQ.deleteWorkload(w)
			}
		}
		delete(c.assumedWorkloads, k)
	}
}

func (c *Cache) addClusterQueueToCohort(cq *ClusterQueue, cohortName string) {
	if cohortName == "" {
		return
	}
	cohort, ok := c.cohorts[cohortName]
	if !ok {
		cohort = newCohort(cohortName, 1)
		c.cohorts[cohortName] = cohort
	}
	cohort.Members.Insert(cq)
	cq.Cohort = cohort
}

func (c *Cache) deleteClusterQueueFromCohort(cq *ClusterQueue) {
	if cq.Cohort == nil {
		return
	}
	cq.Cohort.Members.Delete(cq)
	if cq.Cohort.Members.Len() == 0 {
		delete(c.cohorts, cq.Cohort.Name)
	}
	cq.Cohort = nil
}

func (c *Cache) ClusterQueuesUsingFlavor(flavor string) []string {
	c.RLock()
	defer c.RUnlock()
	var cqs []string

	for _, cq := range c.clusterQueues {
		if cq.flavorInUse(flavor) {
			cqs = append(cqs, cq.Name)
		}
	}
	return cqs
}

func (c *Cache) MatchingClusterQueues(nsLabels map[string]string) sets.Set[string] {
	c.RLock()
	defer c.RUnlock()

	cqs := sets.New[string]()
	for _, cq := range c.clusterQueues {
		if cq.NamespaceSelector.Matches(labels.Set(nsLabels)) {
			cqs.Insert(cq.Name)
		}
	}

	return cqs
}

func resourcesByName(in []kueue.Resource) map[corev1.ResourceName]*Resource {
	out := make(map[corev1.ResourceName]*Resource, len(in))
	for _, r := range in {
		flavors := make([]FlavorLimits, len(r.Flavors))
		for i := range flavors {
			f := &r.Flavors[i]
			fLimits := FlavorLimits{
				Name: string(f.Name),
				Min:  workload.ResourceValue(r.Name, f.Quota.Min),
			}
			if f.Quota.Max != nil {
				fLimits.Max = pointer.Int64(workload.ResourceValue(r.Name, *f.Quota.Max))
			}
			flavors[i] = fLimits

		}
		out[r.Name] = &Resource{
			Flavors: flavors,
		}
	}
	return out
}

func SetupIndexes(indexer client.FieldIndexer) error {
	return indexer.IndexField(context.Background(), &kueue.Workload{}, workloadClusterQueueKey, func(o client.Object) []string {
		wl := o.(*kueue.Workload)
		if wl.Spec.Admission == nil {
			return nil
		}
		return []string{string(wl.Spec.Admission.ClusterQueue)}
	})
}

func workloadBelongsToLocalQueue(wl *kueue.Workload, q *kueue.LocalQueue) bool {
	return wl.Namespace == q.Namespace && wl.Spec.QueueName == q.Name
}

// Key is the key used to index the queue.
func queueKey(q *kueue.LocalQueue) string {
	return fmt.Sprintf("%s/%s", q.Namespace, q.Name)
}

func reportAdmittedActiveWorkloads(cqName string, val int) {
	metrics.AdmittedActiveWorkloads.WithLabelValues(cqName).Set(float64(val))
}
