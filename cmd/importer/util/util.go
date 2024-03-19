/*
Copyright 2024 The Kubernetes Authors.

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

package util

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"

	corev1 "k8s.io/api/core/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utilslices "sigs.k8s.io/kueue/pkg/util/slices"
)

const (
	ListLength = 100
)

var (
	ErrNoMappingKey = errors.New("no mapping key found")
	ErrNoMapping    = errors.New("no mapping found")
	ErrLQNotFound   = errors.New("localqueue not found")
	ErrCQNotFound   = errors.New("clusterqueue not found")
	ErrCQInvalid    = errors.New("clusterqueue invalid")
)

func ListOptions(namespace, queueLabel, continueToken string) []client.ListOption {
	opts := []client.ListOption{
		client.InNamespace(namespace),
		client.HasLabels{queueLabel},
		client.Limit(ListLength),
		client.Continue(continueToken),
	}
	return opts
}

type ImportCache struct {
	Namespaces      []string
	QueueLabel      string
	Mapping         map[string]string
	LocalQueues     map[string]map[string]*kueue.LocalQueue
	ClusterQueues   map[string]*kueue.ClusterQueue
	ResourceFalvors map[string]*kueue.ResourceFlavor
	PriorityClasses map[string]*schedulingv1.PriorityClass
	AddLabels       map[string]string
}

func LoadImportCache(ctx context.Context, c client.Client, namespaces []string, queueLabel string, mapping, addLabels map[string]string) (*ImportCache, error) {
	ret := ImportCache{
		Namespaces:  slices.Clone(namespaces),
		QueueLabel:  queueLabel,
		Mapping:     maps.Clone(mapping),
		LocalQueues: make(map[string]map[string]*kueue.LocalQueue),
		AddLabels:   addLabels,
	}

	// get the cluster queues
	cqList := &kueue.ClusterQueueList{}
	if err := c.List(ctx, cqList); err != nil {
		return nil, fmt.Errorf("loading cluster queues: %w", err)
	}
	ret.ClusterQueues = utilslices.ToRefMap(cqList.Items, func(cq *kueue.ClusterQueue) string { return cq.Name })

	// get the local queues
	for _, ns := range namespaces {
		lqList := &kueue.LocalQueueList{}
		if err := c.List(ctx, lqList); err != nil {
			return nil, fmt.Errorf("loading local queues in namespace %s: %w", ns, err)
		}
		ret.LocalQueues[ns] = utilslices.ToRefMap(lqList.Items, func(lq *kueue.LocalQueue) string { return lq.Name })
	}

	// ResourceFlavors
	rfList := &kueue.ResourceFlavorList{}
	if err := c.List(ctx, rfList); err != nil {
		return nil, fmt.Errorf("loading resource flavors: %w", err)
	}
	ret.ResourceFalvors = utilslices.ToRefMap(rfList.Items, func(rf *kueue.ResourceFlavor) string { return rf.Name })

	// PriorityClasses
	pcList := &schedulingv1.PriorityClassList{}
	if err := c.List(ctx, pcList); err != nil {
		return nil, fmt.Errorf("loading resource flavors: %w", err)
	}
	ret.PriorityClasses = utilslices.ToRefMap(pcList.Items, func(pc *schedulingv1.PriorityClass) string { return pc.Name })
	return &ret, nil
}

func (mappingCache *ImportCache) LocalQueue(p *corev1.Pod) (*kueue.LocalQueue, error) {
	mappingKey, found := p.Labels[mappingCache.QueueLabel]
	if !found {
		return nil, ErrNoMappingKey
	}

	queueName, found := mappingCache.Mapping[mappingKey]
	if !found {
		return nil, fmt.Errorf("%s: %w", mappingKey, ErrNoMapping)
	}

	nqQueues, found := mappingCache.LocalQueues[p.Namespace]
	if !found {
		return nil, fmt.Errorf("%s: %w", queueName, ErrLQNotFound)
	}

	lq, found := nqQueues[queueName]
	if !found {
		return nil, fmt.Errorf("%s: %w", queueName, ErrLQNotFound)
	}
	return lq, nil
}

func (mappingCache *ImportCache) ClusterQueue(p *corev1.Pod) (*kueue.ClusterQueue, error) {
	lq, err := mappingCache.LocalQueue(p)
	if err != nil {
		return nil, err
	}
	queueName := string(lq.Spec.ClusterQueue)
	cq, found := mappingCache.ClusterQueues[queueName]
	if !found {
		return nil, fmt.Errorf("cluster queue: %s: %w", queueName, ErrCQNotFound)
	}
	return cq, nil
}

func PushPods(ctx context.Context, c client.Client, namespaces []string, queueLabel string, ch chan<- corev1.Pod) error {
	defer close(ch)
	for _, ns := range namespaces {
		lst := &corev1.PodList{}
		log := ctrl.LoggerFrom(ctx).WithValues("namespace", ns)
		log.V(3).Info("Begin pods list")
		defer log.V(3).Info("End pods list")
		page := 0
		for {
			err := c.List(ctx, lst, ListOptions(ns, queueLabel, lst.Continue)...)
			if err != nil {
				log.Error(err, "list")
				return fmt.Errorf("listing pods in %s, page %d: %w", ns, page, err)
			}

			for _, p := range lst.Items {
				// ignore deleted or in final Phase?
				ch <- p
			}

			page++
			if lst.Continue == "" {
				log.V(2).Info("No more pods", "pages", page)
				break
			}
		}
	}
	return nil
}

type Result struct {
	Pod string
	Err error
}

type ProcessSummary struct {
	TotalPods     int
	FailedPods    int
	ErrorsForPods map[string][]string
	Errors        []error
}

func ConcurrentProcessPod(ch <-chan corev1.Pod, jobs uint, f func(p *corev1.Pod) error) ProcessSummary {
	wg := sync.WaitGroup{}
	resultCh := make(chan Result)

	wg.Add(int(jobs))
	for i := 0; i < int(jobs); i++ {
		go func() {
			defer wg.Done()
			for pod := range ch {
				err := f(&pod)
				resultCh <- Result{Pod: client.ObjectKeyFromObject(&pod).String(), Err: err}
			}
		}()
	}
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	ps := ProcessSummary{
		ErrorsForPods: make(map[string][]string),
	}
	for result := range resultCh {
		ps.TotalPods++
		if result.Err != nil {
			ps.FailedPods++
			estr := result.Err.Error()
			if _, found := ps.ErrorsForPods[estr]; !found {
				ps.Errors = append(ps.Errors, result.Err)
			}
			ps.ErrorsForPods[estr] = append(ps.ErrorsForPods[estr], result.Pod)
		}
	}
	return ps
}
