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
	"os"
	"slices"
	"sync"

	corev1 "k8s.io/api/core/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utilslices "sigs.k8s.io/kueue/pkg/util/slices"
)

const (
	ListLength = 100
)

var (
	ErrNoMapping  = errors.New("no mapping found")
	ErrLQNotFound = errors.New("localqueue not found")
	ErrCQNotFound = errors.New("clusterqueue not found")
	ErrCQInvalid  = errors.New("clusterqueue invalid")
	ErrPCNotFound = errors.New("priorityclass not found")
)

func listOptions(namespace, continueToken string) []client.ListOption {
	opts := []client.ListOption{
		client.InNamespace(namespace),
		client.Limit(ListLength),
		client.Continue(continueToken),
	}

	return opts
}

type ImportCache struct {
	Namespaces      []string
	MappingRules    MappingRules
	LocalQueues     map[string]map[string]*kueue.LocalQueue
	ClusterQueues   map[string]*kueue.ClusterQueue
	ResourceFlavors map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor
	PriorityClasses map[string]*schedulingv1.PriorityClass
	AddLabels       map[string]string
}

type MappingMatch struct {
	PriorityClassName string            `json:"priorityClassName"`
	Labels            map[string]string `json:"labels"`
}

func (mm *MappingMatch) Match(priorityClassName string, labels map[string]string) bool {
	if mm.PriorityClassName != "" && priorityClassName != mm.PriorityClassName {
		return false
	}
	for l, lv := range mm.Labels {
		if labels[l] != lv {
			return false
		}
	}
	return true
}

type MappingRule struct {
	Match        MappingMatch `json:"match"`
	ToLocalQueue string       `json:"toLocalQueue"`
	Skip         bool         `json:"skip"`
}

type MappingRules []MappingRule

func (mr MappingRules) QueueFor(priorityClassName string, labels map[string]string) (string, bool, bool) {
	for i := range mr {
		if mr[i].Match.Match(priorityClassName, labels) {
			return mr[i].ToLocalQueue, mr[i].Skip, true
		}
	}
	return "", false, false
}

func MappingRulesFromFile(mappingFile string) (MappingRules, error) {
	ret := MappingRules{}
	yamlFile, err := os.ReadFile(mappingFile)
	if err != nil {
		return nil, err
	}
	err = yaml.Unmarshal(yamlFile, &ret)
	if err != nil {
		return nil, fmt.Errorf("decoding %q: %w", mappingFile, err)
	}
	return ret, nil
}

func MappingRulesForLabel(label string, m map[string]string) MappingRules {
	ret := make(MappingRules, 0, len(m))
	for labelValue, queue := range m {
		ret = append(ret, MappingRule{
			Match: MappingMatch{
				Labels: map[string]string{
					label: labelValue,
				},
			},
			ToLocalQueue: queue,
		})
	}
	return ret
}

func LoadImportCache(ctx context.Context, c client.Client, namespaces []string, mappingRules MappingRules, addLabels map[string]string) (*ImportCache, error) {
	ret := ImportCache{
		Namespaces:   slices.Clone(namespaces),
		MappingRules: mappingRules,
		LocalQueues:  make(map[string]map[string]*kueue.LocalQueue),
		AddLabels:    addLabels,
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
	ret.ResourceFlavors = utilslices.ToRefMap(rfList.Items, func(rf *kueue.ResourceFlavor) kueue.ResourceFlavorReference {
		return kueue.ResourceFlavorReference(rf.Name)
	})

	// PriorityClasses
	pcList := &schedulingv1.PriorityClassList{}
	if err := c.List(ctx, pcList); err != nil {
		return nil, fmt.Errorf("loading resource flavors: %w", err)
	}
	ret.PriorityClasses = utilslices.ToRefMap(pcList.Items, func(pc *schedulingv1.PriorityClass) string { return pc.Name })
	return &ret, nil
}

func (mappingCache *ImportCache) LocalQueue(p *corev1.Pod) (*kueue.LocalQueue, bool, error) {
	queueName, skip, found := mappingCache.MappingRules.QueueFor(p.Spec.PriorityClassName, p.Labels)
	if !found {
		return nil, false, ErrNoMapping
	}

	if skip {
		return nil, true, nil
	}

	nqQueues, found := mappingCache.LocalQueues[p.Namespace]
	if !found {
		return nil, false, fmt.Errorf("%s: %w", queueName, ErrLQNotFound)
	}

	lq, found := nqQueues[queueName]
	if !found {
		return nil, false, fmt.Errorf("%s: %w", queueName, ErrLQNotFound)
	}
	return lq, false, nil
}

func (mappingCache *ImportCache) ClusterQueue(p *corev1.Pod) (*kueue.ClusterQueue, bool, error) {
	lq, skip, err := mappingCache.LocalQueue(p)
	if skip || err != nil {
		return nil, skip, err
	}
	queueName := string(lq.Spec.ClusterQueue)
	cq, found := mappingCache.ClusterQueues[queueName]
	if !found {
		return nil, false, fmt.Errorf("cluster queue: %s: %w", queueName, ErrCQNotFound)
	}
	return cq, false, nil
}

func PushPods(ctx context.Context, c client.Client, namespaces []string, ch chan<- corev1.Pod) error {
	defer close(ch)
	for _, ns := range namespaces {
		lst := &corev1.PodList{}
		log := ctrl.LoggerFrom(ctx).WithValues("namespace", ns)
		log.V(3).Info("Begin pods list")
		defer log.V(3).Info("End pods list")
		page := 0
		for {
			err := c.List(ctx, lst, listOptions(ns, lst.Continue)...)
			if err != nil {
				log.Error(err, "list")
				return fmt.Errorf("listing pods in %s, page %d: %w", ns, page, err)
			}

			for _, p := range lst.Items {
				if p.Status.Phase == corev1.PodFailed || p.Status.Phase == corev1.PodSucceeded {
					log.V(2).Info("Skip pod", "pod", klog.KObj(&p), "phase", p.Status.Phase)
				} else {
					ch <- p
				}
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
	Pod  string
	Err  error
	Skip bool
}

type ProcessSummary struct {
	TotalPods     int
	SkippedPods   int
	FailedPods    int
	ErrorsForPods map[string][]string
	Errors        []error
}

func ConcurrentProcessPod(ch <-chan corev1.Pod, jobs uint, f func(p *corev1.Pod) (bool, error)) ProcessSummary {
	wg := sync.WaitGroup{}
	resultCh := make(chan Result)

	wg.Add(int(jobs))
	for i := 0; i < int(jobs); i++ {
		go func() {
			defer wg.Done()
			for pod := range ch {
				skip, err := f(&pod)
				resultCh <- Result{Pod: client.ObjectKeyFromObject(&pod).String(), Err: err, Skip: skip}
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
		if result.Skip {
			ps.SkippedPods++
		}
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
