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

package v1beta1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
)

// LocalQueueWrapper wraps a Queue.
type LocalQueueWrapper struct{ kueue.LocalQueue }

// MakeLocalQueue creates a wrapper for a LocalQueue.
func MakeLocalQueue(name, ns string) *LocalQueueWrapper {
	return &LocalQueueWrapper{kueue.LocalQueue{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
	}}
}

// Obj returns the inner LocalQueue.
func (q *LocalQueueWrapper) Obj() *kueue.LocalQueue {
	return &q.LocalQueue
}

// ClusterQueue updates the clusterQueue the queue points to.
func (q *LocalQueueWrapper) ClusterQueue(c string) *LocalQueueWrapper {
	q.Spec.ClusterQueue = kueue.ClusterQueueReference(c)
	return q
}

// ClusterQueueWrapper wraps a ClusterQueue.
type ClusterQueueWrapper struct{ kueue.ClusterQueue }

// MakeClusterQueue creates a wrapper for a ClusterQueue with a
// select-all NamespaceSelector.
func MakeClusterQueue(name string) *ClusterQueueWrapper {
	return &ClusterQueueWrapper{kueue.ClusterQueue{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: kueue.ClusterQueueSpec{
			NamespaceSelector: &metav1.LabelSelector{},
			QueueingStrategy:  kueue.BestEffortFIFO,
			FlavorFungibility: &kueue.FlavorFungibility{
				WhenCanBorrow:  kueue.MayStopSearch,
				WhenCanPreempt: kueue.TryNextFlavor,
			},
		},
	}}
}

// Obj returns the inner ClusterQueue.
func (c *ClusterQueueWrapper) Obj() *kueue.ClusterQueue {
	return &c.ClusterQueue
}

// Cohort sets the borrowing cohort.
func (c *ClusterQueueWrapper) Cohort(cohort kueue.CohortReference) *ClusterQueueWrapper {
	c.Spec.Cohort = cohort
	return c
}

// ResourceGroup creates a ResourceGroup with the given FlavorQuotas.
func ResourceGroup(flavors ...kueue.FlavorQuotas) kueue.ResourceGroup {
	rg := kueue.ResourceGroup{
		Flavors: flavors,
	}
	if len(flavors) > 0 {
		var resources []corev1.ResourceName
		for _, r := range flavors[0].Resources {
			resources = append(resources, r.Name)
		}
		for i := 1; i < len(flavors); i++ {
			if len(flavors[i].Resources) != len(resources) {
				panic("Must list the same resources in all flavors in a ResourceGroup")
			}
			for j, r := range flavors[i].Resources {
				if r.Name != resources[j] {
					panic("Must list the same resources in all flavors in a ResourceGroup")
				}
			}
		}
		rg.CoveredResources = resources
	}
	return rg
}

// ResourceGroup adds a ResourceGroup with flavors.
func (c *ClusterQueueWrapper) ResourceGroup(flavors ...kueue.FlavorQuotas) *ClusterQueueWrapper {
	c.Spec.ResourceGroups = append(c.Spec.ResourceGroups, ResourceGroup(flavors...))
	return c
}

// Preemption sets the preemption policies.
func (c *ClusterQueueWrapper) Preemption(p kueue.ClusterQueuePreemption) *ClusterQueueWrapper {
	c.Spec.Preemption = &p
	return c
}

// FlavorQuotasWrapper wraps a FlavorQuotas object.
type FlavorQuotasWrapper struct{ kueue.FlavorQuotas }

// MakeFlavorQuotas creates a wrapper for a resource flavor.
func MakeFlavorQuotas(name string) *FlavorQuotasWrapper {
	return &FlavorQuotasWrapper{kueue.FlavorQuotas{
		Name: kueue.ResourceFlavorReference(name),
	}}
}

// Obj returns the inner flavor.
func (f *FlavorQuotasWrapper) Obj() *kueue.FlavorQuotas {
	return &f.FlavorQuotas
}

// Resource takes ResourceName, followed by the optional NominalQuota, BorrowingLimit, LendingLimit.
func (f *FlavorQuotasWrapper) Resource(name corev1.ResourceName, qs ...string) *FlavorQuotasWrapper {
	resourceWrapper := f.ResourceQuotaWrapper(name)
	if len(qs) > 0 {
		resourceWrapper.NominalQuota(qs[0])
	}
	if len(qs) > 1 && len(qs[1]) > 0 {
		resourceWrapper.BorrowingLimit(qs[1])
	}
	if len(qs) > 2 && len(qs[2]) > 0 {
		resourceWrapper.LendingLimit(qs[2])
	}
	if len(qs) > 3 {
		panic("Must have at most 3 quantities for nominalQuota, borrowingLimit and lendingLimit")
	}
	return resourceWrapper.Append()
}

// ResourceQuotaWrapper allows creation the creation of a Resource in a type-safe manner.
func (f *FlavorQuotasWrapper) ResourceQuotaWrapper(name corev1.ResourceName) *ResourceQuotaWrapper {
	rq := kueue.ResourceQuota{
		Name: name,
	}
	return &ResourceQuotaWrapper{parent: f, ResourceQuota: rq}
}

// ResourceQuotaWrapper wraps a ResourceQuota object.
type ResourceQuotaWrapper struct {
	parent *FlavorQuotasWrapper
	kueue.ResourceQuota
}

func (rq *ResourceQuotaWrapper) NominalQuota(quantity string) *ResourceQuotaWrapper {
	rq.ResourceQuota.NominalQuota = resource.MustParse(quantity)
	return rq
}

func (rq *ResourceQuotaWrapper) BorrowingLimit(quantity string) *ResourceQuotaWrapper {
	rq.ResourceQuota.BorrowingLimit = ptr.To(resource.MustParse(quantity))
	return rq
}

func (rq *ResourceQuotaWrapper) LendingLimit(quantity string) *ResourceQuotaWrapper {
	rq.ResourceQuota.LendingLimit = ptr.To(resource.MustParse(quantity))
	return rq
}

// Append appends the ResourceQuotaWrapper to its parent
func (rq *ResourceQuotaWrapper) Append() *FlavorQuotasWrapper {
	rq.parent.Resources = append(rq.parent.Resources, rq.ResourceQuota)
	return rq.parent
}

// ResourceFlavorWrapper wraps a ResourceFlavor.
type ResourceFlavorWrapper struct{ kueue.ResourceFlavor }

// MakeResourceFlavor creates a wrapper for a ResourceFlavor.
func MakeResourceFlavor(name string) *ResourceFlavorWrapper {
	return &ResourceFlavorWrapper{kueue.ResourceFlavor{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: kueue.ResourceFlavorSpec{
			NodeLabels: make(map[string]string),
		},
	}}
}

// Obj returns the inner ResourceFlavor.
func (rf *ResourceFlavorWrapper) Obj() *kueue.ResourceFlavor {
	return &rf.ResourceFlavor
}

// NodeLabel add a label kueue and value pair to the ResourceFlavor.
func (rf *ResourceFlavorWrapper) NodeLabel(k, v string) *ResourceFlavorWrapper {
	rf.Spec.NodeLabels[k] = v
	return rf
}

// WorkloadPriorityClassWrapper wraps a WorkloadPriorityClass.
type WorkloadPriorityClassWrapper struct {
	kueue.WorkloadPriorityClass
}

// MakeWorkloadPriorityClass creates a wrapper for a WorkloadPriorityClass.
func MakeWorkloadPriorityClass(name string) *WorkloadPriorityClassWrapper {
	return &WorkloadPriorityClassWrapper{kueue.WorkloadPriorityClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		}},
	}
}

// PriorityValue updates value of WorkloadPriorityClass.
func (p *WorkloadPriorityClassWrapper) PriorityValue(v int32) *WorkloadPriorityClassWrapper {
	p.Value = v
	return p
}

// Obj returns the inner WorkloadPriorityClass.
func (p *WorkloadPriorityClassWrapper) Obj() *kueue.WorkloadPriorityClass {
	return &p.WorkloadPriorityClass
}
