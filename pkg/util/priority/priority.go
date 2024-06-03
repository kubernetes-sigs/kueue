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

package priority

import (
	"context"

	schedulingv1 "k8s.io/api/scheduling/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
)

// Priority returns priority of the given workload.
func Priority(w *kueue.Workload) int32 {
	// When priority of a running workload is nil, it means it was created at a time
	// that there was no global default priority class and the priority class
	// name of the pod was empty. So, we resolve to the static default priority.
	return ptr.Deref(w.Spec.Priority, constants.DefaultPriority)
}

// GetPriorityFromPriorityClass returns the priority populated from
// priority class. If not specified, priority will be default or
// zero if there is no default.
func GetPriorityFromPriorityClass(ctx context.Context, client client.Client,
	priorityClass string) (string, string, int32, error) {
	if len(priorityClass) == 0 {
		return getDefaultPriority(ctx, client)
	}

	pc := &schedulingv1.PriorityClass{}
	if err := client.Get(ctx, types.NamespacedName{Name: priorityClass}, pc); err != nil {
		return "", "", 0, err
	}

	return pc.Name, constants.PodPriorityClassSource, pc.Value, nil
}

// GetPriorityFromWorkloadPriorityClass returns the priority populated from
// workload priority class. If not specified, returns 0.
// DefaultPriority is not called within this function
// because k8s priority class should be  checked next.
func GetPriorityFromWorkloadPriorityClass(ctx context.Context, client client.Client,
	workloadPriorityClass string) (string, string, int32, error) {
	wpc := &kueue.WorkloadPriorityClass{}
	if err := client.Get(ctx, types.NamespacedName{Name: workloadPriorityClass}, wpc); err != nil {
		return "", "", 0, err
	}
	return wpc.Name, constants.WorkloadPriorityClassSource, wpc.Value, nil
}

func getDefaultPriority(ctx context.Context, client client.Client) (string, string, int32, error) {
	dpc, err := getDefaultPriorityClass(ctx, client)
	if err != nil {
		return "", "", 0, err
	}
	if dpc != nil {
		return dpc.Name, constants.PodPriorityClassSource, dpc.Value, nil
	}
	return "", "", int32(constants.DefaultPriority), nil
}

func getDefaultPriorityClass(ctx context.Context, client client.Client) (*schedulingv1.PriorityClass, error) {
	pcs := schedulingv1.PriorityClassList{}
	err := client.List(ctx, &pcs)
	if err != nil {
		return nil, err
	}

	// In case more than one global default priority class is added as a result of a race condition,
	// we pick the one with the lowest priority value.
	var defaultPC *schedulingv1.PriorityClass
	for _, item := range pcs.Items {
		if item.GlobalDefault {
			if defaultPC == nil || defaultPC.Value > item.Value {
				defaultPC = &item
			}
		}
	}

	return defaultPC, nil
}
