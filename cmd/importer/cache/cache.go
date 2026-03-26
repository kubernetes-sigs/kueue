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
	"slices"

	corev1 "k8s.io/api/core/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/cmd/importer/mapping"
	utilslices "sigs.k8s.io/kueue/pkg/util/slices"
)

var (
	ErrLQNotFound = errors.New("localqueue not found")
	ErrCQNotFound = errors.New("clusterqueue not found")
	ErrCQInvalid  = errors.New("clusterqueue invalid")
	ErrPCNotFound = errors.New("priorityclass not found")
)

type ImportCache struct {
	Namespaces      []string
	MappingRules    mapping.Rules
	LocalQueues     map[string]map[string]*kueue.LocalQueue
	ClusterQueues   map[string]*kueue.ClusterQueue
	ResourceFlavors map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor
	PriorityClasses map[string]*schedulingv1.PriorityClass
	AddLabels       map[string]string
}

func Load(ctx context.Context, c client.Client, namespaces []string, mappingRules mapping.Rules, addLabels map[string]string) (*ImportCache, error) {
	ret := ImportCache{
		Namespaces:   slices.Clone(namespaces),
		MappingRules: mappingRules,
		LocalQueues:  make(map[string]map[string]*kueue.LocalQueue),
		AddLabels:    addLabels,
	}

	cqList := &kueue.ClusterQueueList{}
	if err := c.List(ctx, cqList); err != nil {
		return nil, fmt.Errorf("loading cluster queues: %w", err)
	}
	ret.ClusterQueues = utilslices.ToRefMap(cqList.Items, func(cq *kueue.ClusterQueue) string { return cq.Name })

	for _, ns := range namespaces {
		lqList := &kueue.LocalQueueList{}
		if err := c.List(ctx, lqList); err != nil {
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
	return &ret, nil
}

func (ic *ImportCache) LocalQueue(p *corev1.Pod) (*kueue.LocalQueue, bool, error) {
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

func (ic *ImportCache) ClusterQueue(p *corev1.Pod) (*kueue.ClusterQueue, bool, error) {
	lq, skip, err := ic.LocalQueue(p)
	if skip || err != nil {
		return nil, skip, err
	}
	queueName := string(lq.Spec.ClusterQueue)
	cq, found := ic.ClusterQueues[queueName]
	if !found {
		return nil, false, fmt.Errorf("cluster queue: %s: %w", queueName, ErrCQNotFound)
	}
	return cq, false, nil
}
