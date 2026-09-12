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
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
)

// findParentDeployment traverses the owner reference chain
// Pod → ReplicaSet → Deployment. Returns nil when the pod is not
// owned by a Kueue-managed Deployment or the parent cannot be resolved.
func findParentDeployment(ctx context.Context, c client.Client, pod *corev1.Pod) (*appsv1.Deployment, error) {
	if pod.Annotations[podconstants.SuspendedByParentAnnotation] != "deployment" {
		return nil, nil
	}

	var rsName string
	for _, ref := range pod.OwnerReferences {
		if ref.Kind == "ReplicaSet" && ref.APIVersion == "apps/v1" {
			rsName = ref.Name
			break
		}
	}
	if rsName == "" {
		return nil, nil
	}

	var rs appsv1.ReplicaSet
	if err := c.Get(ctx, types.NamespacedName{Name: rsName, Namespace: pod.Namespace}, &rs); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("getting ReplicaSet %s: %w", rsName, err)
	}

	var deployName string
	for _, ref := range rs.OwnerReferences {
		if ref.Kind == "Deployment" && ref.APIVersion == "apps/v1" {
			deployName = ref.Name
			break
		}
	}
	if deployName == "" {
		return nil, nil
	}

	var deploy appsv1.Deployment
	if err := c.Get(ctx, types.NamespacedName{Name: deployName, Namespace: pod.Namespace}, &deploy); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("getting Deployment %s: %w", deployName, err)
	}

	return &deploy, nil
}
