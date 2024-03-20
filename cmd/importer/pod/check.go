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

package pod

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/cmd/importer/util"
)

func Check(ctx context.Context, c client.Client, cache *util.ImportCache, jobs uint) error {
	ch := make(chan corev1.Pod)
	go func() {
		err := util.PushPods(ctx, c, cache.Namespaces, cache.QueueLabel, ch)
		if err != nil {
			ctrl.LoggerFrom(ctx).Error(err, "Listing pods")
		}
	}()
	summary := util.ConcurrentProcessPod(ch, jobs, func(p *corev1.Pod) error {
		log := ctrl.LoggerFrom(ctx).WithValues("pod", klog.KObj(p))
		log.V(3).Info("Checking")

		cq, err := cache.ClusterQueue(p)
		if err != nil {
			return err
		}

		if len(cq.Spec.ResourceGroups) == 0 {
			return fmt.Errorf("%q has no resource groups: %w", cq.Name, util.ErrCQInvalid)
		}

		if len(cq.Spec.ResourceGroups[0].Flavors) == 0 {
			return fmt.Errorf("%q has no resource groups flavors: %w", cq.Name, util.ErrCQInvalid)
		}

		rfName := string(cq.Spec.ResourceGroups[0].Flavors[0].Name)
		rf, rfFound := cache.ResourceFalvors[rfName]
		if !rfFound {
			return fmt.Errorf("%q flavor %q: %w", cq.Name, rfName, util.ErrCQInvalid)
		}

		log.V(2).Info("Successfully checked", "pod", klog.KObj(p), "clusterQueue", klog.KObj(cq), "resourceFalvor", klog.KObj(rf))
		return nil
	})

	log := ctrl.LoggerFrom(ctx)
	log.Info("Check done", "checked", summary.TotalPods, "failed", summary.FailedPods)
	for e, pods := range summary.ErrorsForPods {
		log.Info("Validation failed for Pods", "err", e, "occurrences", len(pods), "obsevedFirstIn", pods[0])
	}
	return errors.Join(summary.Errors...)
}
