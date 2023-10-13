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
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utilindexer "sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/workload"
)

var (
	errCqNotFound          = errors.New("cluster queue not found")
	errQNotFound           = errors.New("queue not found")
	errWorkloadNotAdmitted = errors.New("workload not admitted by a ClusterQueue")
)

const (
	pending     = metrics.CQStatusPending
	active      = metrics.CQStatusActive
	terminating = metrics.CQStatusTerminating
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
	resourceFlavors   map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor
	podsReadyTracking bool
	admissionChecks   map[string]AdmissionCheck
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
		resourceFlavors:   make(map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor),
		admissionChecks:   make(map[string]AdmissionCheck),
		podsReadyTracking: options.podsReadyTracking,
	}
	c.podsReadyCond.L = &c.RWMutex
	return c
}

func (c *Cache) newClusterQueue(cq *kueue.ClusterQueue) (*ClusterQueue, error) {
	cqImpl := &ClusterQueue{
		Name:              cq.Name,
		Workloads:         make(map[string]*workload.Info),
		WorkloadsNotReady: sets.New[string](),
		localQueues:       make(map[string]*queue),
		podsReadyTracking: c.podsReadyTracking,
	}
	if err := cqImpl.update(cq, c.resourceFlavors, c.admissionChecks); err != nil {
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
	c.Lock()
	defer c.Unlock()
	c.podsReadyCond.Broadcast()
}

func (c *Cache) ReservingWorkloadsInLocalQueue(localQueue *kueue.LocalQueue) int32 {
	c.Lock()
	defer c.Unlock()
	cq, ok := c.clusterQueues[string(localQueue.Spec.ClusterQueue)]
	if !ok {
		return 0
	}
	qImpl, ok := cq.localQueues[queueKey(localQueue)]
	if !ok {
		return 0
	}
	return int32(qImpl.reservingWorkloads)
}

func (c *Cache) AdmittedWorkloadsInLocalQueue(localQueue *kueue.LocalQueue) int32 {
	c.Lock()
	defer c.Unlock()
	cq, ok := c.clusterQueues[string(localQueue.Spec.ClusterQueue)]
	if !ok {
		return 0
	}
	qImpl, ok := cq.localQueues[queueKey(localQueue)]
	if !ok {
		return 0
	}
	return int32(qImpl.admittedWorkloads)
}

func (c *Cache) updateClusterQueues() sets.Set[string] {
	cqs := sets.New[string]()

	for _, cq := range c.clusterQueues {
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
	c.admissionChecks[ac.Name] = AdmissionCheck{
		Active: apimeta.IsStatusConditionTrue(ac.Status.Conditions, kueue.AdmissionCheckActive),
	}

	return c.updateClusterQueues()
}

func (c *Cache) DeleteAdmissionCheck(ac *kueue.AdmissionCheck) sets.Set[string] {
	c.Lock()
	defer c.Unlock()
	delete(c.admissionChecks, ac.Name)
	return c.updateClusterQueues()
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
	cq := c.clusterQueues[name]
	if cq == nil {
		return metav1.ConditionFalse, "NotFound", "ClusterQueue not found"
	}
	if cq != nil && cq.Status == active {
		return metav1.ConditionTrue, "Ready", "Can admit new workloads"
	}
	reason, msg := cq.inactiveReason()
	return metav1.ConditionFalse, reason, msg
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
	if err := c.client.List(ctx, &queues, client.MatchingFields{utilindexer.QueueClusterQueueKey: cq.Name}); err != nil {
		return fmt.Errorf("listing queues that match the clusterQueue: %w", err)
	}
	for _, q := range queues.Items {
		qKey := queueKey(&q)
		qImpl := &queue{
			key:                qKey,
			reservingWorkloads: 0,
			admittedWorkloads:  0,
			usage:              make(FlavorResourceQuantities),
			admittedUsage:      make(FlavorResourceQuantities),
		}
		if err = qImpl.resetFlavorsAndResources(cqImpl.Usage, cqImpl.AdmittedUsage); err != nil {
			return err
		}
		cqImpl.localQueues[qKey] = qImpl
	}
	var workloads kueue.WorkloadList
	if err := c.client.List(ctx, &workloads, client.MatchingFields{utilindexer.WorkloadClusterQueueKey: cq.Name}); err != nil {
		return fmt.Errorf("listing workloads that match the queue: %w", err)
	}
	for i, w := range workloads.Items {
		if !workload.HasQuotaReservation(&w) {
			continue
		}
		c.addOrUpdateWorkload(&workloads.Items[i])
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
	if err := cqImpl.update(cq, c.resourceFlavors, c.admissionChecks); err != nil {
		return err
	}
	for _, qImpl := range cqImpl.localQueues {
		if qImpl == nil {
			return errQNotFound
		}
		if err := qImpl.resetFlavorsAndResources(cqImpl.Usage, cqImpl.AdmittedUsage); err != nil {
			return err
		}
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
	if !workload.HasQuotaReservation(w) {
		return false
	}

	clusterQueue, ok := c.clusterQueues[string(w.Status.Admission.ClusterQueue)]
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
		cq, ok := c.clusterQueues[string(oldWl.Status.Admission.ClusterQueue)]
		if !ok {
			return fmt.Errorf("old ClusterQueue doesn't exist")
		}
		cq.deleteWorkload(oldWl)
	}
	c.cleanupAssumedState(oldWl)

	if !workload.HasQuotaReservation(newWl) {
		return nil
	}
	cq, ok := c.clusterQueues[string(newWl.Status.Admission.ClusterQueue)]
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

	cq := c.clusterQueueForWorkload(w)
	if cq == nil {
		return errCqNotFound
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
	if cq, exists := c.clusterQueues[w.ClusterQueue]; exists {
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

	cq, ok := c.clusterQueues[string(w.Status.Admission.ClusterQueue)]
	if !ok {
		return errCqNotFound
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
		return fmt.Errorf("the workload is not assumed")
	}
	c.cleanupAssumedState(w)

	if !workload.HasQuotaReservation(w) {
		return errWorkloadNotAdmitted
	}

	cq, ok := c.clusterQueues[string(w.Status.Admission.ClusterQueue)]
	if !ok {
		return errCqNotFound
	}
	cq.deleteWorkload(w)
	if c.podsReadyTracking {
		c.podsReadyCond.Broadcast()
	}
	return nil
}

// ReservationStats reports the reserved resources and number of workloads holding them in the ClusterQueue.
func (c *Cache) ReservationStats(cqObj *kueue.ClusterQueue) ([]kueue.FlavorUsage, int, error) {
	c.RLock()
	defer c.RUnlock()

	cq := c.clusterQueues[cqObj.Name]
	if cq == nil {
		return nil, 0, errCqNotFound
	}

	return getUsage(cq.Usage, cq.ResourceGroups, cq.Cohort), len(cq.Workloads), nil
}

// UsageStats reports the used resources and number of workloads holding them in the ClusterQueue.
func (c *Cache) UsageStats(cqObj *kueue.ClusterQueue) ([]kueue.FlavorUsage, int, error) {
	c.RLock()
	defer c.RUnlock()

	cq := c.clusterQueues[cqObj.Name]
	if cq == nil {
		return nil, 0, errCqNotFound
	}

	return getUsage(cq.AdmittedUsage, cq.ResourceGroups, cq.Cohort), cq.admittedWorkloadsCount(), nil
}

func getUsage(frq FlavorResourceQuantities, rgs []ResourceGroup, cohort *Cohort) []kueue.FlavorUsage {
	usage := make([]kueue.FlavorUsage, 0, len(frq))
	for _, rg := range rgs {
		for _, flvQuotas := range rg.Flavors {
			flvUsage := frq[flvQuotas.Name]
			outFlvUsage := kueue.FlavorUsage{
				Name:      flvQuotas.Name,
				Resources: make([]kueue.ResourceUsage, 0, len(flvQuotas.Resources)),
			}
			for rName, rQuota := range flvQuotas.Resources {
				used := flvUsage[rName]
				rUsage := kueue.ResourceUsage{
					Name:  rName,
					Total: workload.ResourceQuantity(rName, used),
				}
				// Enforce `borrowed=0` if the clusterQueue doesn't belong to a cohort.
				if cohort != nil {
					borrowed := used - rQuota.Nominal
					if borrowed > 0 {
						rUsage.Borrowed = workload.ResourceQuantity(rName, borrowed)
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

func (c *Cache) LocalQueueReservations(qObj *kueue.LocalQueue) ([]kueue.LocalQueueFlavorUsage, error) {
	c.RLock()
	defer c.RUnlock()

	cqImpl, ok := c.clusterQueues[string(qObj.Spec.ClusterQueue)]
	if !ok {
		return nil, nil
	}
	qImpl, ok := cqImpl.localQueues[queueKey(qObj)]
	if !ok {
		return nil, errQNotFound
	}

	return filterLocalQueueUsage(qImpl.usage, cqImpl.ResourceGroups), nil
}

func (c *Cache) LocalQueueUsage(qObj *kueue.LocalQueue) ([]kueue.LocalQueueFlavorUsage, error) {
	c.RLock()
	defer c.RUnlock()

	cqImpl, ok := c.clusterQueues[string(qObj.Spec.ClusterQueue)]
	if !ok {
		return nil, nil
	}
	qImpl, ok := cqImpl.localQueues[queueKey(qObj)]
	if !ok {
		return nil, errQNotFound
	}

	return filterLocalQueueUsage(qImpl.admittedUsage, cqImpl.ResourceGroups), nil
}

func filterLocalQueueUsage(orig FlavorResourceQuantities, resourceGroups []ResourceGroup) []kueue.LocalQueueFlavorUsage {
	qFlvUsages := make([]kueue.LocalQueueFlavorUsage, 0, len(orig))
	for _, rg := range resourceGroups {
		for _, flvQuotas := range rg.Flavors {
			flvUsage := orig[flvQuotas.Name]
			outFlvUsage := kueue.LocalQueueFlavorUsage{
				Name:      flvQuotas.Name,
				Resources: make([]kueue.LocalQueueResourceUsage, 0, len(flvQuotas.Resources)),
			}
			for rName := range flvQuotas.Resources {
				outFlvUsage.Resources = append(outFlvUsage.Resources, kueue.LocalQueueResourceUsage{
					Name:  rName,
					Total: workload.ResourceQuantity(rName, flvUsage[rName]),
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
		// one, then we should also cleanup the assumed one.
		if workload.HasQuotaReservation(w) && assumedCQName != string(w.Status.Admission.ClusterQueue) {
			if assumedCQ, exist := c.clusterQueues[assumedCQName]; exist {
				assumedCQ.deleteWorkload(w)
			}
		}
		delete(c.assumedWorkloads, k)
	}
}

func (c *Cache) clusterQueueForWorkload(w *kueue.Workload) *ClusterQueue {
	if workload.HasQuotaReservation(w) {
		return c.clusterQueues[string(w.Status.Admission.ClusterQueue)]
	}
	wKey := workload.Key(w)
	for _, cq := range c.clusterQueues {
		if cq.Workloads[wKey] != nil {
			return cq
		}
	}
	return nil
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

func (c *Cache) ClusterQueuesUsingAdmissionCheck(ac string) []string {
	c.RLock()
	defer c.RUnlock()
	var cqs []string

	for _, cq := range c.clusterQueues {
		if cq.AdmissionChecks.Has(ac) {
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

// Key is the key used to index the queue.
func queueKey(q *kueue.LocalQueue) string {
	return fmt.Sprintf("%s/%s", q.Namespace, q.Name)
}
