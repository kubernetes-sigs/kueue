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
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/cmd/importer/cache"
	"sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	"sigs.k8s.io/kueue/pkg/workload"
)

func Check(ctx context.Context, c client.Client, importCache *cache.ImportCache, jobs uint) error {
	ch := make(chan corev1.Pod)
	go func() {
		err := ListPods(ctx, c, importCache.Namespaces, ch)
		if err != nil {
			ctrl.LoggerFrom(ctx).Error(err, "Listing pods")
		}
	}()
	summary := ProcessConcurrently(ch, jobs, func(p *corev1.Pod) (bool, error) {
		log := ctrl.LoggerFrom(ctx).WithValues("pod", klog.KObj(p))
		log.V(3).Info("Checking")

		cq, skip, err := importCache.ClusterQueue(p)
		if skip || err != nil {
			return skip, err
		}

		if len(cq.Spec.ResourceGroups) == 0 {
			return false, fmt.Errorf("%q has no resource groups: %w", cq.Name, cache.ErrCQInvalid)
		}
		if err := validateKnownClusterQueueFlavors(cq, importCache.ResourceFlavors); err != nil {
			return false, err
		}

		kp := pod.FromObject(p)
		wl, err := kp.ConstructComposableWorkload(ctx, c, nil, nil, nil)
		if err != nil {
			return false, fmt.Errorf("construct workload: %w", err)
		}

		info := workload.NewInfo(wl)
		if len(info.TotalRequests) == 0 {
			return false, fmt.Errorf("workload has no total requests: %w", cache.ErrPodInvalid)
		}

		if _, err := flavorAssignmentsForRequests(cq, info.TotalRequests[0].Requests); err != nil {
			return false, err
		}
		var pv int32
		if pc, found := importCache.PriorityClasses[p.Spec.PriorityClassName]; found {
			pv = pc.Value
		} else if p.Spec.PriorityClassName != "" {
			return false, fmt.Errorf("%q: %w", p.Spec.PriorityClassName, cache.ErrPCNotFound)
		}

		log.V(2).Info("Successfully checked", "clusterQueue", klog.KObj(cq), "priority", pv)
		return false, nil
	})

	log := ctrl.LoggerFrom(ctx)
	log.Info("Check done", "checked", summary.TotalPods, "skipped", summary.SkippedPods, "failed", summary.FailedPods)
	for e, pods := range summary.ErrorsForPods {
		log.Info("Validation failed for Pods", "err", e, "occurrences", len(pods), "observedFirstIn", pods[0])
	}
	return errors.Join(summary.Errors...)
}

func validateKnownClusterQueueFlavors(cq *kueue.ClusterQueue, known map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor) error {
	for _, rg := range cq.Spec.ResourceGroups {
		for _, flavor := range rg.Flavors {
			if _, found := known[flavor.Name]; !found {
				return fmt.Errorf("%q flavor %q: %w", cq.Name, flavor.Name, cache.ErrCQInvalid)
			}
		}
	}
	return nil
}
