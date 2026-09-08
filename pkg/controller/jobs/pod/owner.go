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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// findParentDeployment traverses the owner reference chain
// Pod → ReplicaSet → Deployment. Returns nil when the pod is not
// owned by a Deployment or the parent cannot be resolved.
func findParentDeployment(ctx context.Context, c client.Client, pod *corev1.Pod) (*appsv1.Deployment, error) {
	owner := metav1.GetControllerOf(pod)
	if owner == nil || owner.Kind != "ReplicaSet" || owner.APIVersion != "apps/v1" {
		return nil, nil
	}

	var rs appsv1.ReplicaSet
	if err := c.Get(ctx, types.NamespacedName{Name: owner.Name, Namespace: pod.Namespace}, &rs); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("getting ReplicaSet %s: %w", owner.Name, err)
	}
	if rs.UID != owner.UID {
		return nil, nil
	}

	rsOwner := metav1.GetControllerOf(&rs)
	if rsOwner == nil || rsOwner.Kind != "Deployment" || rsOwner.APIVersion != "apps/v1" {
		return nil, nil
	}

	var deploy appsv1.Deployment
	if err := c.Get(ctx, types.NamespacedName{Name: rsOwner.Name, Namespace: pod.Namespace}, &deploy); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("getting Deployment %s: %w", rsOwner.Name, err)
	}
	if deploy.UID != rsOwner.UID {
		return nil, nil
	}

	return &deploy, nil
}

// hasGatedSiblings returns true when any pod matching the Deployment's label
// selector still carries the Kueue scheduling gate.
func hasGatedSiblings(ctx context.Context, c client.Client, deploy *appsv1.Deployment) (bool, error) {
	selector, err := metav1.LabelSelectorAsSelector(deploy.Spec.Selector)
	if err != nil {
		return false, fmt.Errorf("parsing Deployment selector: %w", err)
	}

	var podList corev1.PodList
	if err := c.List(ctx, &podList,
		client.InNamespace(deploy.Namespace),
		client.MatchingLabelsSelector{Selector: selector},
	); err != nil {
		return false, fmt.Errorf("listing pods for Deployment %s: %w", deploy.Name, err)
	}

	for i := range podList.Items {
		if isGated(&podList.Items[i]) {
			return true, nil
		}
	}
	return false, nil
}
