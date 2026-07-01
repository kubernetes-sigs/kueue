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

	corev1 "k8s.io/api/core/v1"
	schedulingv1alpha3 "k8s.io/api/scheduling/v1alpha3"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
)

func nativePodGroupsAvailability(restMapper apimeta.RESTMapper) (enabled bool, reason string) {
	if !features.Enabled(features.WASPodGroups) {
		return false, "feature gate disabled"
	}
	if _, err := restMapper.RESTMapping(schedulingv1alpha3.SchemeGroupVersion.WithKind("PodGroup").GroupKind(), schedulingv1alpha3.SchemeGroupVersion.Version); err != nil {
		return false, fmt.Sprintf("REST mapping unavailable: %v", err)
	}
	return true, "native PodGroups supported"
}

func nativePodGroupNameForPod(p *corev1.Pod) string {
	if p.Spec.SchedulingGroup == nil || p.Spec.SchedulingGroup.PodGroupName == nil {
		return ""
	}
	return *p.Spec.SchedulingGroup.PodGroupName
}

func wantsWASPodGroup(p *corev1.Pod) bool {
	return p.Annotations[podconstants.WASPodGroupAnnotation] == "true"
}

func shouldDefaultNativePodGroup(enabled bool, p *corev1.Pod) bool {
	return enabled && wantsWASPodGroup(p) && utilpod.GetPodGroupName(p) != "" && nativePodGroupNameForPod(p) == ""
}

func setNativePodGroupName(p *corev1.Pod, podGroupName string) {
	p.Spec.SchedulingGroup = &corev1.PodSchedulingGroup{
		PodGroupName: &podGroupName,
	}
}

func (p *Pod) shouldEnsureNativePodGroup() bool {
	return p.nativePodGroupsEnabled && p.isGroup && wantsWASPodGroup(&p.pod) && nativePodGroupNameForPod(&p.pod) != ""
}

func (p *Pod) ensureNativePodGroup(ctx context.Context, c client.Client, wl *kueue.Workload, recorder events.EventRecorder) error {
	if !p.shouldEnsureNativePodGroup() {
		return nil
	}

	podGroupName := nativePodGroupNameForPod(&p.pod)
	podGroup := &schedulingv1alpha3.PodGroup{}
	key := client.ObjectKey{Namespace: p.pod.Namespace, Name: podGroupName}
	if err := c.Get(ctx, key, podGroup); err == nil {
		if podGroup.Labels[constants.ManagedByKueueLabelKey] != constants.ManagedByKueueLabelValue {
			ctrl.LoggerFrom(ctx).V(3).Info("Native PodGroup already exists and is not managed by Kueue, reusing it", "podGroup", key)
			if recorder != nil {
				recorder.Eventf(wl, nil, corev1.EventTypeNormal, podconstants.ReasonNativePodGroupReused, "NativePodGroupReused", "Reused existing native PodGroup %s", podGroupName)
			}
			return nil
		}
		// Kueue-managed PodGroup exists — verify it belongs to this workload.
		if !metav1.IsControlledBy(podGroup, wl) {
			return fmt.Errorf("native PodGroup %q is managed by Kueue but owned by a different workload", podGroupName)
		}
		return nil
	} else if !apierrors.IsNotFound(err) {
		return err
	}

	groupTotalCount, err := p.groupTotalCount()
	if err != nil {
		return err
	}

	podGroup = &schedulingv1alpha3.PodGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podGroupName,
			Namespace: p.pod.Namespace,
			Labels: map[string]string{
				constants.ManagedByKueueLabelKey: constants.ManagedByKueueLabelValue,
			},
		},
		Spec: schedulingv1alpha3.PodGroupSpec{
			SchedulingPolicy: schedulingv1alpha3.PodGroupSchedulingPolicy{
				Gang: &schedulingv1alpha3.GangSchedulingPolicy{MinCount: int32(groupTotalCount)},
			},
		},
	}
	if err := controllerutil.SetOwnerReference(wl, podGroup, c.Scheme()); err != nil {
		return fmt.Errorf("setting owner reference on native PodGroup: %w", err)
	}
	if err := c.Create(ctx, podGroup); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	ctrl.LoggerFrom(ctx).V(3).Info("Created native PodGroup for plain Pod group", "podGroup", key)
	if recorder != nil {
		recorder.Eventf(wl, nil, corev1.EventTypeNormal, podconstants.ReasonNativePodGroupCreated, "NativePodGroupCreated", "Created native PodGroup %s", podGroupName)
	}
	return nil
}
