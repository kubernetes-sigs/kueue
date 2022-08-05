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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
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

// Cache keeps track of the Workloads that got admitted through ClusterQueues.
type Cache struct {
	sync.RWMutex

	client           client.Client
	clusterQueues    map[string]*ClusterQueue
	cohorts          map[string]*Cohort
	assumedWorkloads map[string]string
	resourceFlavors  map[string]*kueue.ResourceFlavor
}

func New(client client.Client) *Cache {
	return &Cache{
		client:           client,
		clusterQueues:    make(map[string]*ClusterQueue),
		cohorts:          make(map[string]*Cohort),
		assumedWorkloads: make(map[string]string),
		resourceFlavors:  make(map[string]*kueue.ResourceFlavor),
	}
}

type ResourceQuantities map[corev1.ResourceName]map[string]int64

// Cohort is a set of ClusterQueues that can borrow resources from each other.
type Cohort struct {
	Name    string
	members map[*ClusterQueue]struct{}

	// These fields are only populated for a snapshot.
	RequestableResources ResourceQuantities
	UsedResources        ResourceQuantities
}

func newCohort(name string, size int) *Cohort {
	return &Cohort{
		Name:    name,
		members: make(map[*ClusterQueue]struct{}, size),
	}
}

type ClusterQueueStatus int

const (
	// Pending means the ClusterQueue is accepted but not yet active,
	// this can be because of a missing ResourceFlavor referenced by the ClusterQueue.
	// In this state, the ClusterQueue can't admit new workloads and its quota can't be borrowed
	// by other active ClusterQueues in the cohort.
	Pending ClusterQueueStatus = iota
	// Active means the ClusterQueue can admit new workloads and its quota
	// can be borrowed by other ClusterQueues in the cohort.
	Active
	// Terminating means the clusterQueue is in pending deletion.
	Terminating
)

// ClusterQueue is the internal implementation of kueue.ClusterQueue that
// holds admitted workloads.
type ClusterQueue struct {
	Name                 string
	Cohort               *Cohort
	RequestableResources map[corev1.ResourceName]*Resource
	UsedResources        ResourceQuantities
	Workloads            map[string]*workload.Info
	NamespaceSelector    labels.Selector
	// The set of key labels from all flavors of a resource.
	// Those keys define the affinity terms of a workload
	// that can be matched against the flavors.
	LabelKeys map[corev1.ResourceName]sets.String
	Status    ClusterQueueStatus

	// The following fields are not populated in a snapshot.

	admittedWorkloadsPerQueue map[string]int
}

type Resource struct {
	CodependentResources sets.String
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
		admittedWorkloadsPerQueue: make(map[string]int),
	}
	if err := cqImpl.update(cq, c.resourceFlavors); err != nil {
		return nil, err
	}

	return cqImpl, nil
}

func (c *ClusterQueue) Active() bool {
	return c.Status == Active
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
		codep := sets.NewString()
		for jName, jRes := range c.RequestableResources {
			if iName == jName || iRes.matchesFlavors(jRes) {
				codep.Insert(string(jName))
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
	status := Active
	if flavorNotFound := c.updateLabelKeys(flavors); flavorNotFound {
		status = Pending
	}

	if c.Status != Terminating {
		c.Status = status
	}
}

func (c *ClusterQueue) updateLabelKeys(flavors map[string]*kueue.ResourceFlavor) bool {
	var flavorNotFound bool
	labelKeys := map[corev1.ResourceName]sets.String{}
	for rName, res := range c.RequestableResources {
		if len(res.Flavors) == 0 {
			continue
		}
		resKeys := sets.NewString()
		for _, rf := range res.Flavors {
			if flv, exist := flavors[rf.Name]; exist {
				for k := range flv.Labels {
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

func (c *ClusterQueue) addQueue(q *kueue.Queue) error {
	qKey := queueKey(q)
	if _, ok := c.admittedWorkloadsPerQueue[qKey]; ok {
		return errQueueAlreadyExists
	}
	// We need to count the workloads, because they could have been added before
	// receiving the queue add event.
	workloads := 0
	for _, wl := range c.Workloads {
		if workloadBelongsToQueue(wl.Obj, q) {
			workloads++
		}
	}
	c.admittedWorkloadsPerQueue[qKey] = workloads
	return nil
}

func (c *ClusterQueue) deleteQueue(q *kueue.Queue) {
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

func (c *Cache) updateClusterQueues() sets.String {
	cqs := sets.NewString()

	for _, cq := range c.clusterQueues {
		prevStatus := cq.Status
		// We call update on all ClusterQueues irrespective of which CQ actually use this flavor
		// because it is not expensive to do so, and is not worth tracking which ClusterQueues use
		// which flavors.
		cq.UpdateWithFlavors(c.resourceFlavors)
		curStatus := cq.Status
		if prevStatus == Pending && curStatus == Active {
			cqs.Insert(cq.Name)
		}
	}
	return cqs
}

func (c *Cache) AddOrUpdateResourceFlavor(rf *kueue.ResourceFlavor) sets.String {
	c.Lock()
	defer c.Unlock()
	c.resourceFlavors[rf.Name] = rf
	return c.updateClusterQueues()
}

func (c *Cache) DeleteResourceFlavor(rf *kueue.ResourceFlavor) sets.String {
	c.Lock()
	defer c.Unlock()
	delete(c.resourceFlavors, rf.Name)
	return c.updateClusterQueues()
}

func (c *Cache) ClusterQueueActive(name string) bool {
	return c.clusterQueueInStatus(name, Active)
}

func (c *Cache) ClusterQueueTerminating(name string) bool {
	return c.clusterQueueInStatus(name, Terminating)
}

func (c *Cache) clusterQueueInStatus(name string, status ClusterQueueStatus) bool {
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
		cq.Status = Terminating
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
	var queues kueue.QueueList
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
	metrics.AdmittedActiveWorkloads.DeleteLabelValues(cq.Name)
}

func (c *Cache) AddQueue(q *kueue.Queue) error {
	c.Lock()
	defer c.Unlock()
	cq, ok := c.clusterQueues[string(q.Spec.ClusterQueue)]
	if !ok {
		return nil
	}
	return cq.addQueue(q)
}

func (c *Cache) DeleteQueue(q *kueue.Queue) {
	c.Lock()
	defer c.Unlock()
	cq, ok := c.clusterQueues[string(q.Spec.ClusterQueue)]
	if !ok {
		return
	}
	cq.deleteQueue(q)
}

func (c *Cache) UpdateQueue(oldQ, newQ *kueue.Queue) error {
	if oldQ.Spec.ClusterQueue == newQ.Spec.ClusterQueue {
		return nil
	}
	c.Lock()
	defer c.Unlock()
	cq, ok := c.clusterQueues[string(oldQ.Spec.ClusterQueue)]
	if ok {
		cq.deleteQueue(oldQ)
	}
	cq, ok = c.clusterQueues[string(newQ.Spec.ClusterQueue)]
	if ok {
		return cq.addQueue(newQ)
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
	cohort.members[cq] = struct{}{}
	cq.Cohort = cohort
}

func (c *Cache) deleteClusterQueueFromCohort(cq *ClusterQueue) {
	if cq.Cohort == nil {
		return
	}
	delete(cq.Cohort.members, cq)
	if len(cq.Cohort.members) == 0 {
		delete(c.cohorts, cq.Cohort.Name)
	}
	cq.Cohort = nil
}

func (c *Cache) FlavorInUse(flavor string) (string, bool) {
	c.RLock()
	defer c.RUnlock()

	for _, cq := range c.clusterQueues {
		if cq.flavorInUse(flavor) {
			return cq.Name, true
		}
	}
	return "", false
}

func (c *Cache) MatchingClusterQueues(nsLabels map[string]string) sets.String {
	c.RLock()
	defer c.RUnlock()

	cqs := sets.NewString()
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

func workloadBelongsToQueue(wl *kueue.Workload, q *kueue.Queue) bool {
	return wl.Namespace == q.Namespace && wl.Spec.QueueName == q.Name
}

// Key is the key used to index the queue.
func queueKey(q *kueue.Queue) string {
	return fmt.Sprintf("%s/%s", q.Namespace, q.Name)
}

func reportAdmittedActiveWorkloads(cqName string, val int) {
	metrics.AdmittedActiveWorkloads.WithLabelValues(cqName).Set(float64(val))
}
