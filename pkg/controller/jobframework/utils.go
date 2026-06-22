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

package jobframework

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/orderedgroups"
)

// PodSetReplicaSize is a minimal representation of a PodSet for the
// PodsetReplicaSizesAnnotation, containing only name and count.
type PodSetReplicaSize struct {
	Name  kueue.PodSetReference `json:"name"`
	Count int32                 `json:"count"`
}

// JobPodSets retrieves the pod sets from a GenericJob and applies environment variable
// deduplication.
func JobPodSets(ctx context.Context, job GenericJob) ([]kueue.PodSet, error) {
	podSets, err := job.PodSets(ctx)
	if err != nil {
		return nil, err
	}
	SanitizePodSets(podSets)
	return podSets, nil
}

// SanitizePodSets sanitizes all PodSets in the given slice by removing duplicate
// environment variables from each container. This function modifies the podSets slice in place.
func SanitizePodSets(podSets []kueue.PodSet) {
	for podSetIndex := range podSets {
		SanitizePodSet(&podSets[podSetIndex])
	}
}

// SanitizePodSet sanitizes a single PodSet by removing duplicate environment
// variables from all containers and initContainers in its pod template.
func SanitizePodSet(podSet *kueue.PodSet) {
	for containerIndex := range podSet.Template.Spec.Containers {
		sanitizeContainer(&podSet.Template.Spec.Containers[containerIndex])
	}

	for containerIndex := range podSet.Template.Spec.InitContainers {
		sanitizeContainer(&podSet.Template.Spec.InitContainers[containerIndex])
	}
}

// sanitizeContainer removes duplicate environment variables from the given container.
func sanitizeContainer(container *corev1.Container) {
	envVarGroups := orderedgroups.NewOrderedGroups[string, corev1.EnvVar]()
	for _, envVar := range container.Env {
		envVarGroups.Insert(envVar.Name, envVar)
	}
	container.Env = make([]corev1.EnvVar, 0, len(container.Env))
	for _, envVars := range envVarGroups.InOrder {
		container.Env = append(container.Env, envVars[len(envVars)-1])
	}
}

func PrebuiltWorkloadNameFor(obj client.Object) string {
	if features.Enabled(features.WorkloadIdentifierAnnotations) {
		if name := obj.GetAnnotations()[controllerconstants.PrebuiltWorkloadAnnotation]; name != "" {
			return name
		}
	}
	return obj.GetLabels()[controllerconstants.PrebuiltWorkloadLabel]
}

func SetPrebuiltWorkloadName(obj client.Object, workloadName string) {
	if features.Enabled(features.WorkloadIdentifierAnnotations) {
		annotations := obj.GetAnnotations()
		if annotations == nil {
			annotations = make(map[string]string, 1)
		}
		annotations[controllerconstants.PrebuiltWorkloadAnnotation] = workloadName
		obj.SetAnnotations(annotations)
	} else {
		objLabels := obj.GetLabels()
		if objLabels == nil {
			objLabels = make(map[string]string, 1)
		}
		objLabels[controllerconstants.PrebuiltWorkloadLabel] = workloadName
		obj.SetLabels(objLabels)
	}
}

// SetMultiKueueMeta sets the MultiKueue origin label and the prebuilt workload label on the given object.
func SetMultiKueueMeta(obj client.Object, workloadName, origin string) {
	objLabels := obj.GetLabels()
	if objLabels == nil {
		objLabels = make(map[string]string, 1)
	}
	objLabels[kueue.MultiKueueOriginLabel] = origin
	obj.SetLabels(objLabels)

	SetPrebuiltWorkloadName(obj, workloadName)
}

var ErrRemoteObjectNotOwnedByMultiKueue = errors.New("remote object is not owned by MultiKueue")
var ErrMultiKueueOriginEmpty = errors.New("multikueue origin is empty")

// ValidateRemoteObjectOwnership retrieves the remote object and validates it is owned by this MultiKueue origin.
// Returns (false, ErrMultiKueueOriginEmpty) if origin is empty.
// Returns (true, nil) if the object exists and is owned by this MultiKueue origin.
// Returns (false, nil) if the object does not exist.
// Returns (false, err) if there is a retrieval error or if the object is not owned by this MultiKueue origin.
func ValidateRemoteObjectOwnership(ctx context.Context, remoteClient client.Client, key types.NamespacedName, gvk schema.GroupVersionKind, origin string) (bool, error) {
	log := ctrl.LoggerFrom(ctx).WithValues("remoteObject", key, "objectType", gvk, "origin", origin)

	if origin == "" {
		log.Error(ErrMultiKueueOriginEmpty, "Remote object ownership validation failed because origin is empty")
		return false, ErrMultiKueueOriginEmpty
	}

	remoteObject := &metav1.PartialObjectMetadata{}
	remoteObject.SetGroupVersionKind(gvk)
	if err := remoteClient.Get(ctx, key, remoteObject); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return false, nil
		}
		return false, err
	}

	if objOrigin, owned := remoteObject.GetLabels()[kueue.MultiKueueOriginLabel]; !owned || objOrigin != origin {
		return false, fmt.Errorf("%w: expected %q=%q on %T %q", ErrRemoteObjectNotOwnedByMultiKueue, kueue.MultiKueueOriginLabel, origin, remoteObject, client.ObjectKeyFromObject(remoteObject))
	}

	return true, nil
}

// DeleteRemoteObjectIfOwned fetches the remote object for the given adapter's GVK and key,
// skips deletion if the object does not exist or is not owned by this MultiKueue origin,
// and otherwise delegates to adapter.DeleteRemoteObject.
// Returns ErrMultiKueueOriginEmpty if origin is empty.
func DeleteRemoteObjectIfOwned(ctx context.Context, localClient client.Client, remoteClient client.Client, adapter MultiKueueAdapter, key types.NamespacedName, origin string) error {
	log := ctrl.LoggerFrom(ctx).WithValues("remoteObject", key, "adapterGVK", adapter.GVK().String(), "origin", origin)

	if origin == "" {
		log.Error(ErrMultiKueueOriginEmpty, "Skipping remote object deletion because origin is empty")
		return ErrMultiKueueOriginEmpty
	}

	found, err := ValidateRemoteObjectOwnership(ctx, remoteClient, key, adapter.GVK(), origin)
	if err != nil {
		if errors.Is(err, ErrRemoteObjectNotOwnedByMultiKueue) {
			log.V(2).Info("Skipping remote object deletion because object is not owned by this MultiKueue origin")
			return nil
		}
		return err
	}
	if !found {
		log.V(2).Info("Skipping remote object deletion because object was not found")
		return nil
	}

	return adapter.DeleteRemoteObject(ctx, localClient, remoteClient, key)
}
