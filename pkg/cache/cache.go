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
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/api/v1alpha1"
	"sigs.k8s.io/kueue/pkg/util/pointer"
	"sigs.k8s.io/kueue/pkg/workload"
)

const workloadCapacityKey = "spec.assignedCapacity"

var cqNotFoundErr = errors.New("cluster queue not found")

// Cache keeps track of the QueuedWorkloads that got admitted through
// ClusterQueues.
type Cache struct {
	sync.RWMutex

	client           client.Client
	clusterQueues    map[string]*ClusterQueue
	cohorts          map[string]*Cohort
	assumedWorkloads map[string]string
}

func New(client client.Client) *Cache {
	return &Cache{
		client:           client,
		clusterQueues:    make(map[string]*ClusterQueue),
		cohorts:          make(map[string]*Cohort),
		assumedWorkloads: make(map[string]string),
	}
}

type Resources map[corev1.ResourceName]map[string]int64

// Cohort is a set of ClusterQueues that can borrow resources from each other.
type Cohort struct {
	Name    string
	members map[*ClusterQueue]struct{}

	// These fields are only populated for a snapshot.
	RequestableResources Resources
	UsedResources        Resources
}

func newCohort(name string, cap int) *Cohort {
	return &Cohort{
		Name:    name,
		members: make(map[*ClusterQueue]struct{}, cap),
	}
}

// ClusterQueue is the internal implementation of kueue.ClusterQueue
type ClusterQueue struct {
	Name                 string
	Cohort               *Cohort
	RequestableResources map[corev1.ResourceName][]FlavorInfo
	UsedResources        Resources
	Workloads            map[string]*workload.Info
	// The set of key labels from all flavors of a resource.
	// Those keys define the affinity terms of a workload
	// that can be matched against the flavors.
	LabelKeys map[corev1.ResourceName]sets.String

	// TODO(#87) introduce a single heap for per Capacity.
	// QueueingStrategy indicates the queueing strategy of the workloads
	// across the queues in this Capacity.
	QueueingStrategy kueue.QueueingStrategy
}

// FlavorInfo holds processed flavor type.
type FlavorInfo struct {
	Name       string
	Guaranteed int64
	Ceiling    int64
	Taints     []corev1.Taint
	Labels     map[string]string
}

func NewClusterQueue(cap *kueue.ClusterQueue) *ClusterQueue {
	c := &ClusterQueue{
		Name:                 cap.Name,
		RequestableResources: resourcesByName(cap.Spec.RequestableResources),
		UsedResources:        make(Resources, len(cap.Spec.RequestableResources)),
		Workloads:            map[string]*workload.Info{},
		QueueingStrategy:     cap.Spec.QueueingStrategy,
	}

	labelKeys := map[corev1.ResourceName]sets.String{}
	for _, r := range cap.Spec.RequestableResources {
		if len(r.Flavors) == 0 {
			continue
		}

		resKeys := sets.NewString()
		ts := make(map[string]int64, len(r.Flavors))
		for _, t := range r.Flavors {
			for k := range t.Labels {
				resKeys.Insert(k)
			}
			ts[t.Name] = 0
		}
		if len(resKeys) != 0 {
			labelKeys[r.Name] = resKeys
		}
		c.UsedResources[r.Name] = ts
	}

	if len(labelKeys) != 0 {
		c.LabelKeys = labelKeys
	}
	return c
}

func (c *ClusterQueue) addWorkload(w *kueue.QueuedWorkload) error {
	k := workload.Key(w)
	if _, exist := c.Workloads[k]; exist {
		return fmt.Errorf("workload already exists in ClusterQueue")
	}
	wi := workload.NewInfo(w)
	c.Workloads[k] = wi
	c.updateWorkloadUsage(wi, 1)
	return nil
}

func (c *ClusterQueue) deleteWorkload(w *kueue.QueuedWorkload) {
	k := workload.Key(w)
	wi, exist := c.Workloads[k]
	if !exist {
		return
	}
	c.updateWorkloadUsage(wi, -1)
	delete(c.Workloads, k)
}

func (c *ClusterQueue) updateWorkloadUsage(wi *workload.Info, m int64) {
	for _, ps := range wi.TotalRequests {
		for wlRes, wlResFlv := range ps.Flavors {
			v, wlResExist := ps.Requests[wlRes]
			capResFlv, capResExist := c.UsedResources[wlRes]
			if capResExist && wlResExist {
				if _, capFlvExist := capResFlv[wlResFlv]; capFlvExist {
					capResFlv[wlResFlv] += v * m
				}
			}
		}
	}
}

func (c *Cache) AddClusterQueue(ctx context.Context, cq *kueue.ClusterQueue) error {
	c.Lock()
	defer c.Unlock()

	if _, ok := c.clusterQueues[cq.Name]; ok {
		return fmt.Errorf("ClusterQueue already exists")
	}
	cqImpl := NewClusterQueue(cq)
	c.addClusterQueueToCohort(cqImpl, cq.Spec.Cohort)
	c.clusterQueues[cq.Name] = cqImpl
	// On controller restart, an add ClusterQueue event may come after
	// add workload events, and so here we explicitly list and add existing workloads.
	var workloads kueue.QueuedWorkloadList
	if err := c.client.List(ctx, &workloads, client.MatchingFields{workloadCapacityKey: cq.Name}); err != nil {
		return fmt.Errorf("listing workloads that match the queue: %w", err)
	}
	for i, w := range workloads.Items {
		// Checking ClusterQueue name again because the field index is not available in tests.
		if w.Spec.Admission == nil || string(w.Spec.Admission.ClusterQueue) != cq.Name {
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
		return cqNotFoundErr
	}
	cqImpl.RequestableResources = resourcesByName(cq.Spec.RequestableResources)
	if cqImpl.Cohort != nil {
		if cqImpl.Cohort.Name != cq.Spec.Cohort {
			c.deleteClusterQueueFromCohort(cqImpl)
			c.addClusterQueueToCohort(cqImpl, cq.Spec.Cohort)
		}
	} else {
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
}

func (c *Cache) AddOrUpdateWorkload(w *kueue.QueuedWorkload) bool {
	c.Lock()
	defer c.Unlock()
	return c.addOrUpdateWorkload(w)
}

func (c *Cache) addOrUpdateWorkload(w *kueue.QueuedWorkload) bool {
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

func (c *Cache) UpdateWorkload(oldWl, newWl *kueue.QueuedWorkload) error {
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

func (c *Cache) DeleteWorkload(w *kueue.QueuedWorkload) error {
	c.Lock()
	defer c.Unlock()
	if w.Spec.Admission == nil {
		return fmt.Errorf("workload not admitted through a ClusterQueue")
	}

	qc, ok := c.clusterQueues[string(w.Spec.Admission.ClusterQueue)]
	if !ok {
		return cqNotFoundErr
	}

	c.cleanupAssumedState(w)

	qc.deleteWorkload(w)
	return nil
}

func (c *Cache) AssumeWorkload(w *kueue.QueuedWorkload) error {
	c.Lock()
	defer c.Unlock()

	if w.Spec.Admission == nil {
		return fmt.Errorf("workload not admitted by a ClusterQueue")
	}

	k := workload.Key(w)
	assumedCap, assumed := c.assumedWorkloads[k]
	if assumed {
		return fmt.Errorf("the workload is already assumed to ClusterQueue %q", assumedCap)
	}

	cq, ok := c.clusterQueues[string(w.Spec.Admission.ClusterQueue)]
	if !ok {
		return cqNotFoundErr
	}

	if err := cq.addWorkload(w); err != nil {
		return err
	}
	c.assumedWorkloads[k] = string(w.Spec.Admission.ClusterQueue)
	return nil
}

func (c *Cache) ForgetWorkload(w *kueue.QueuedWorkload) error {
	c.Lock()
	defer c.Unlock()

	if _, assumed := c.assumedWorkloads[workload.Key(w)]; !assumed {
		return fmt.Errorf("the workload is not assumed")
	}
	c.cleanupAssumedState(w)

	if w.Spec.Admission == nil {
		return fmt.Errorf("workload was not admitted by a ClusterQueue")
	}

	cq, ok := c.clusterQueues[string(w.Spec.Admission.ClusterQueue)]
	if !ok {
		return cqNotFoundErr
	}
	cq.deleteWorkload(w)
	return nil
}

// Usage reports the used resources and number of workloads admitted by the ClusterQueue.
func (c *Cache) Usage(capObj *kueue.ClusterQueue) (kueue.UsedResources, int, error) {
	c.RLock()
	defer c.RUnlock()

	cq := c.clusterQueues[capObj.Name]
	if cq == nil {
		return nil, 0, cqNotFoundErr
	}
	usage := make(kueue.UsedResources, len(cq.UsedResources))
	for rName, usedRes := range cq.UsedResources {
		rUsage := make(map[string]kueue.Usage)
		requestable := cq.RequestableResources[rName]
		for _, flavor := range requestable {
			used := usedRes[flavor.Name]
			fUsage := kueue.Usage{
				Total: pointer.Quantity(workload.ResourceQuantity(rName, used)),
			}
			borrowing := used - flavor.Guaranteed
			if borrowing > 0 {
				fUsage.Borrowed = pointer.Quantity(workload.ResourceQuantity(rName, borrowing))
			}
			rUsage[flavor.Name] = fUsage
		}
		usage[rName] = rUsage
	}
	return usage, len(cq.Workloads), nil
}

func (c *Cache) cleanupAssumedState(w *kueue.QueuedWorkload) {
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

func resourcesByName(in []kueue.Resource) map[corev1.ResourceName][]FlavorInfo {
	out := make(map[corev1.ResourceName][]FlavorInfo, len(in))
	for _, r := range in {
		flavors := make([]FlavorInfo, len(r.Flavors))
		for i := range flavors {
			f := &r.Flavors[i]
			fInfo := FlavorInfo{
				Name:       f.Name,
				Guaranteed: workload.ResourceValue(r.Name, f.Quota.Guaranteed),
				Ceiling:    workload.ResourceValue(r.Name, f.Quota.Ceiling),
				Taints:     append([]corev1.Taint(nil), f.Taints...),
			}
			if len(f.Labels) != 0 {
				fInfo.Labels = make(map[string]string, len(f.Labels))
				for k, v := range f.Labels {
					fInfo.Labels[k] = v
				}
			}
			flavors[i] = fInfo

		}
		out[r.Name] = flavors
	}
	return out
}

func SetupIndexes(indexer client.FieldIndexer) error {
	return indexer.IndexField(context.Background(), &kueue.QueuedWorkload{}, workloadCapacityKey, func(o client.Object) []string {
		wl := o.(*kueue.QueuedWorkload)
		if wl.Spec.Admission == nil {
			return nil
		}
		return []string{string(wl.Spec.Admission.ClusterQueue)}
	})
}
