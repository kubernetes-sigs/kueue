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

package priority

import (
	"context"
	"strconv"

	"github.com/go-logr/logr"
	schedulingv1 "k8s.io/api/scheduling/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
)

// Priority returns priority of the given workload.
func Priority(w *kueue.Workload) int32 {
	// When the priority of a running workload is nil, it means it was created at a time
	// that there was no global default priority class and the priority class
	// name of the pod was empty. So, we resolve to the static default priority.
	return ptr.Deref(w.Spec.Priority, constants.DefaultPriority)
}

// PriorityBoost returns the priority boost read from the workload annotation.
// Missing annotation is treated as 0. When the PriorityBoost feature gate is
// disabled, the annotation is ignored and 0 is returned.
// Invalid values cause the Workload webhook to reject create/update.
func PriorityBoost(w *kueue.Workload) (int32, error) {
	if !features.Enabled(features.PriorityBoost) {
		return 0, nil
	}
	value := w.Annotations[controllerconstants.PriorityBoostAnnotationKey]
	if value == "" {
		return 0, nil
	}
	boost, err := strconv.ParseInt(value, 10, 32)
	if err != nil {
		return 0, err
	}
	return int32(boost), nil
}

// ParseEffectivePriority returns priority adjusted by the priority boost
// annotation and reports an error if the annotation cannot be parsed.
func ParseEffectivePriority(w *kueue.Workload) (int64, error) {
	basePriority := int64(Priority(w))
	boost, err := PriorityBoost(w)
	if err != nil {
		return basePriority, err
	}
	return basePriority + int64(boost), nil
}

// EffectivePriority returns priority adjusted by the priority boost annotation.
// Invalid values are rejected at admission by the webhook; as defense-in-depth,
// any parse error here is logged and the base priority is returned.
func EffectivePriority(log logr.Logger, w *kueue.Workload) int64 {
	effectivePriority, err := ParseEffectivePriority(w)
	if err != nil {
		log.V(5).Info("Invalid priority-boost annotation, defaulting to base priority",
			"workload", w.Namespace+"/"+w.Name,
			"annotation", w.Annotations[controllerconstants.PriorityBoostAnnotationKey],
			"error", err)
	}
	return effectivePriority
}

// GetPriorityFromPriorityClass returns the priority populated from
// priority class. If not specified, the priority will be default or
// zero if there is no default.
func GetPriorityFromPriorityClass(ctx context.Context, client client.Client, priorityClass string) (*kueue.PriorityClassRef, int32, error) {
	if len(priorityClass) == 0 {
		return getDefaultPriority(ctx, client)
	}

	pc := &schedulingv1.PriorityClass{}
	if err := client.Get(ctx, types.NamespacedName{Name: priorityClass}, pc); err != nil {
		return nil, 0, err
	}

	return kueue.NewPodPriorityClassRef(pc.Name), pc.Value, nil
}

// GetPriorityFromWorkloadPriorityClass returns the priority populated from
// workload priority class. If not specified, returns 0.
// DefaultPriority is not called within this function
// because k8s priority class should be checked next.
func GetPriorityFromWorkloadPriorityClass(ctx context.Context, client client.Client, workloadPriorityClass string) (*kueue.PriorityClassRef, int32, error) {
	wpc := &kueue.WorkloadPriorityClass{}
	if err := client.Get(ctx, types.NamespacedName{Name: workloadPriorityClass}, wpc); err != nil {
		return nil, 0, err
	}
	return kueue.NewWorkloadPriorityClassRef(wpc.Name), wpc.Value, nil
}

func getDefaultPriority(ctx context.Context, client client.Client) (*kueue.PriorityClassRef, int32, error) {
	dpc, err := getDefaultPriorityClass(ctx, client)
	if err != nil {
		return nil, 0, err
	}
	if dpc != nil {
		return kueue.NewPodPriorityClassRef(dpc.Name), dpc.Value, nil
	}
	return nil, constants.DefaultPriority, nil
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
