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
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	corev1 "k8s.io/api/core/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/cmd/importer/mapping"
	"sigs.k8s.io/kueue/pkg/util/resourcegroups"
	utilslices "sigs.k8s.io/kueue/pkg/util/slices"
	"sigs.k8s.io/kueue/pkg/workload"
)

var (
	ErrLQNotFound = errors.New("localqueue not found")
	ErrCQNotFound = errors.New("clusterqueue not found")
	ErrCQInvalid  = errors.New("clusterqueue invalid")
	ErrPCNotFound = errors.New("priorityclass not found")
)

type ImportCache struct {
	Namespaces          []string
	MappingRules        mapping.Rules
	LocalQueues         map[string]map[string]*kueue.LocalQueue
	ClusterQueues       map[string]*kueue.ClusterQueue
	ResourceFlavors     map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor
	PriorityClasses     map[string]*schedulingv1.PriorityClass
	AddLabels           map[string]string
	workloadInfoOptions []workload.InfoOption

	// Derived from ClusterQueues and ResourceFlavors at Load time.
	flavorValidation  map[kueue.ClusterQueueReference]error
	flavorsByResource map[kueue.ClusterQueueReference]map[corev1.ResourceName]kueue.ResourceFlavorReference
}

func Load(ctx context.Context, c client.Client, namespaces []string, mappingRules mapping.Rules, addLabels map[string]string, workloadInfoOptions []workload.InfoOption) (*ImportCache, error) {
	ret := ImportCache{
		Namespaces:          slices.Clone(namespaces),
		MappingRules:        mappingRules,
		LocalQueues:         make(map[string]map[string]*kueue.LocalQueue),
		AddLabels:           addLabels,
		workloadInfoOptions: slices.Clone(workloadInfoOptions),
	}

	cqList := &kueue.ClusterQueueList{}
	if err := c.List(ctx, cqList); err != nil {
		return nil, fmt.Errorf("loading cluster queues: %w", err)
	}
	ret.ClusterQueues = utilslices.ToRefMap(cqList.Items, func(cq *kueue.ClusterQueue) string { return cq.Name })

	for _, ns := range namespaces {
		lqList := &kueue.LocalQueueList{}
		if err := c.List(ctx, lqList, client.InNamespace(ns)); err != nil {
			return nil, fmt.Errorf("loading local queues in namespace %s: %w", ns, err)
		}
		ret.LocalQueues[ns] = utilslices.ToRefMap(lqList.Items, func(lq *kueue.LocalQueue) string { return lq.Name })
	}

	rfList := &kueue.ResourceFlavorList{}
	if err := c.List(ctx, rfList); err != nil {
		return nil, fmt.Errorf("loading resource flavors: %w", err)
	}
	ret.ResourceFlavors = utilslices.ToRefMap(rfList.Items, func(rf *kueue.ResourceFlavor) kueue.ResourceFlavorReference {
		return kueue.ResourceFlavorReference(rf.Name)
	})

	pcList := &schedulingv1.PriorityClassList{}
	if err := c.List(ctx, pcList); err != nil {
		return nil, fmt.Errorf("loading priority classes: %w", err)
	}
	ret.PriorityClasses = utilslices.ToRefMap(pcList.Items, func(pc *schedulingv1.PriorityClass) string { return pc.Name })

	ret.flavorValidation = make(map[kueue.ClusterQueueReference]error, len(cqList.Items))
	ret.flavorsByResource = make(map[kueue.ClusterQueueReference]map[corev1.ResourceName]kueue.ResourceFlavorReference, len(cqList.Items))
	for i := range cqList.Items {
		cq := &cqList.Items[i]
		rgs := resourceGroupsFrom(cq)
		cqRef := kueue.ClusterQueueReference(cq.Name)
		ret.flavorsByResource[cqRef] = flavorsByResourceFrom(rgs)
		ret.flavorValidation[cqRef] = validateFlavors(cq.Name, rgs, ret.ResourceFlavors)
	}

	return &ret, nil
}

func (ic *ImportCache) WorkloadInfoOptions() []workload.InfoOption {
	return slices.Clone(ic.workloadInfoOptions)
}

// resourceGroupsFrom converts cq's API resource groups to the ResourceGroup
// representation used by pkg/util/resourcegroups.
func resourceGroupsFrom(cq *kueue.ClusterQueue) []resourcegroups.ResourceGroup {
	rgs := make([]resourcegroups.ResourceGroup, 0, len(cq.Spec.ResourceGroups))
	for _, rg := range cq.Spec.ResourceGroups {
		if len(rg.Flavors) == 0 {
			continue
		}
		flavors := make([]kueue.ResourceFlavorReference, len(rg.Flavors))
		for i, f := range rg.Flavors {
			flavors[i] = f.Name
		}
		rgs = append(rgs, resourcegroups.ResourceGroup{
			CoveredResources: sets.New(rg.CoveredResources...),
			Flavors:          flavors,
		})
	}
	return rgs
}

// flavorsByResourceFrom returns, for every resource covered by rgs, the first
// flavor listed in its resource group.
func flavorsByResourceFrom(rgs []resourcegroups.ResourceGroup) map[corev1.ResourceName]kueue.ResourceFlavorReference {
	m := make(map[corev1.ResourceName]kueue.ResourceFlavorReference)
	for _, rg := range rgs {
		for resource := range rg.CoveredResources {
			if _, exists := m[resource]; !exists {
				m[resource] = rg.Flavors[0]
			}
		}
	}
	return m
}

// validateFlavors checks that every ResourceFlavor referenced by rgs is known.
func validateFlavors(cqName string, rgs []resourcegroups.ResourceGroup, resourceFlavors map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor) error {
	// Sorted for a deterministic error message; AllFlavors returns a set.
	flavors := sets.List(resourcegroups.AllFlavors(rgs))
	missing := make([]kueue.ResourceFlavorReference, 0)
	for _, flavor := range flavors {
		if _, found := resourceFlavors[flavor]; !found {
			missing = append(missing, flavor)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%q missing flavors %v: %w", cqName, missing, ErrCQInvalid)
	}
	return nil
}

func (ic *ImportCache) LocalQueueForPod(p *corev1.Pod) (*kueue.LocalQueue, bool, error) {
	queueName, skip, found := ic.MappingRules.QueueFor(p.Spec.PriorityClassName, p.Labels)
	if !found {
		return nil, false, mapping.ErrNoMapping
	}

	if skip {
		return nil, true, nil
	}

	nqQueues, found := ic.LocalQueues[p.Namespace]
	if !found {
		return nil, false, fmt.Errorf("%s: %w", queueName, ErrLQNotFound)
	}

	lq, found := nqQueues[queueName]
	if !found {
		return nil, false, fmt.Errorf("%s: %w", queueName, ErrLQNotFound)
	}
	return lq, false, nil
}

func (ic *ImportCache) FlavorValidationForClusterQueue(cqName kueue.ClusterQueueReference) error {
	return ic.flavorValidation[cqName]
}

func (ic *ImportCache) FlavorsByResourceForClusterQueue(cqName kueue.ClusterQueueReference) map[corev1.ResourceName]kueue.ResourceFlavorReference {
	flavorsByResource := ic.flavorsByResource[cqName]
	if flavorsByResource == nil {
		return nil
	}

	ret := make(map[corev1.ResourceName]kueue.ResourceFlavorReference, len(flavorsByResource))
	maps.Copy(ret, flavorsByResource)

	return ret
}
