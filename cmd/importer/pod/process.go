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

package pod

import (
	"context"
	"fmt"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
)

const (
	ListLength = 100
)

func listOptions(namespace, continueToken string) []client.ListOption {
	opts := []client.ListOption{
		client.InNamespace(namespace),
		client.Limit(ListLength),
		client.Continue(continueToken),
	}
	return opts
}

type ProcessResult struct {
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

func ListPods(ctx context.Context, c client.Client, namespaces []string, ch chan<- corev1.Pod) error {
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
				if utilpod.IsTerminated(&p) {
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

func ProcessConcurrently(ch <-chan corev1.Pod, jobs uint, f func(p *corev1.Pod) (bool, error)) ProcessSummary {
	wg := sync.WaitGroup{}
	resultCh := make(chan ProcessResult)

	for range jobs {
		wg.Go(func() {
			for pod := range ch {
				skip, err := f(&pod)
				resultCh <- ProcessResult{Pod: client.ObjectKeyFromObject(&pod).String(), Err: err, Skip: skip}
			}
		})
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
