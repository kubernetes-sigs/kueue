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

	kueue "sigs.k8s.io/kueue/api/v1alpha1"
	"sigs.k8s.io/kueue/pkg/util/pointer"
	"sigs.k8s.io/kueue/pkg/workload"
)

const workloadClusterQueueKey = "spec.admission.clusterQueue"

var errCqNotFound = errors.New("cluster queue not found")

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

type Resources map[corev1.ResourceName]map[string]int64

// Cohort is a set of ClusterQueues that can borrow resources from each other.
type Cohort struct {
	Name    string
	members map[*ClusterQueue]struct{}

	// These fields are only populated for a snapshot.
	RequestableResources Resources
	UsedResources        Resources
}

func newCohort(name string, size int) *Cohort {
	return &Cohort{
		Name:    name,
		members: make(map[*ClusterQueue]struct{}, size),
	}
}

// ClusterQueue is the internal implementation of kueue.ClusterQueue that
// holds admitted workloads.
type ClusterQueue struct {
	Name                 string
	Cohort               *Cohort
	RequestableResources map[corev1.ResourceName][]FlavorLimits
	UsedResources        Resources
	Workloads            map[string]*workload.Info
	NamespaceSelector    labels.Selector
	// The set of key labels from all flavors of a resource.
	// Those keys define the affinity terms of a workload
	// that can be matched against the flavors.
	LabelKeys map[corev1.ResourceName]sets.String
}

// FlavorLimits holds a processed ClusterQueue flavor quota.
type FlavorLimits struct {
	Name       string
	Guaranteed int64
	Ceiling    *int64
}

func (c *Cache) newClusterQueue(cq *kueue.ClusterQueue) (*ClusterQueue, error) {
	cqImpl := &ClusterQueue{
		Name:      cq.Name,
		Workloads: map[string]*workload.Info{},
	}
	if err := cqImpl.update(cq, c.resourceFlavors); err != nil {
		return nil, err
	}

	return cqImpl, nil
}

func (c *ClusterQueue) update(in *kueue.ClusterQueue, resourceFlavors map[string]*kueue.ResourceFlavor) error {
	c.RequestableResources = resourceLimitsByName(in.Spec.RequestableResources)
	nsSelector, err := metav1.LabelSelectorAsSelector(in.Spec.NamespaceSelector)
	if err != nil {
		return err
	}
	c.NamespaceSelector = nsSelector

	usedResources := make(Resources, len(in.Spec.RequestableResources))
	for _, r := range in.Spec.RequestableResources {
		if len(r.Flavors) == 0 {
			continue
		}

		existingUsedFlavors := c.UsedResources[r.Name]
		usedFlavors := make(map[string]int64, len(r.Flavors))
		for _, f := range r.Flavors {
			usedFlavors[string(f.ResourceFlavor)] = existingUsedFlavors[string(f.ResourceFlavor)]
		}
		usedResources[r.Name] = usedFlavors
	}
	c.UsedResources = usedResources
	c.UpdateLabelKeys(resourceFlavors)
	return nil
}

// UpdateLabelKeys updates a ClusterQueue's LabelKeys based on the passed ResourceFlavors set.
// Exported only for testing.
func (c *ClusterQueue) UpdateLabelKeys(flavors map[string]*kueue.ResourceFlavor) {
	labelKeys := map[corev1.ResourceName]sets.String{}
	for rName, flvLimits := range c.RequestableResources {
		if len(flvLimits) == 0 {
			continue
		}
		resKeys := sets.NewString()
		for _, l := range flvLimits {
			if flv, exist := flavors[l.Name]; exist {
				for k := range flv.Labels {
					resKeys.Insert(k)
				}
			}
			// We don't error here on missing flavor since ResourceFlavor add events may
			// come after ClusterQueue add/update.
		}

		if len(resKeys) != 0 {
			labelKeys[rName] = resKeys
		}
	}

	c.LabelKeys = nil
	if len(labelKeys) != 0 {
		c.LabelKeys = labelKeys
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
}

func (c *Cache) AddOrUpdateResourceFlavor(rf *kueue.ResourceFlavor) {
	c.Lock()
	c.resourceFlavors[rf.Name] = rf
	for _, cq := range c.clusterQueues {
		// We call update on all ClusterQueues irrespective of which CQ actually use this flavor
		// because it is not expensive to do so, and is not worth tracking which ClusterQueues use
		// which flavors.
		cq.UpdateLabelKeys(c.resourceFlavors)
	}
	c.Unlock()
}

func (c *Cache) DeleteResourceFlavor(rf *kueue.ResourceFlavor) {
	c.Lock()
	delete(c.resourceFlavors, rf.Name)
	c.Unlock()
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
	// add workload events, and so here we explicitly list and add existing workloads.
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
		return fmt.Errorf("workload not admitted through a ClusterQueue")
	}

	qc, ok := c.clusterQueues[string(w.Spec.Admission.ClusterQueue)]
	if !ok {
		return errCqNotFound
	}

	c.cleanupAssumedState(w)

	qc.deleteWorkload(w)
	return nil
}

func (c *Cache) AssumeWorkload(w *kueue.Workload) error {
	c.Lock()
	defer c.Unlock()

	if w.Spec.Admission == nil {
		return fmt.Errorf("workload not admitted by a ClusterQueue")
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
		return fmt.Errorf("workload was not admitted by a ClusterQueue")
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

func resourceLimitsByName(in []kueue.Resource) map[corev1.ResourceName][]FlavorLimits {
	out := make(map[corev1.ResourceName][]FlavorLimits, len(in))
	for _, r := range in {
		flavors := make([]FlavorLimits, len(r.Flavors))
		for i := range flavors {
			f := &r.Flavors[i]
			fLimits := FlavorLimits{
				Name:       string(f.ResourceFlavor),
				Guaranteed: workload.ResourceValue(r.Name, f.Quota.Guaranteed),
			}
			if f.Quota.Ceiling != nil {
				fLimits.Ceiling = pointer.Int64(workload.ResourceValue(r.Name, *f.Quota.Ceiling))
			}
			flavors[i] = fLimits

		}
		out[r.Name] = flavors
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
