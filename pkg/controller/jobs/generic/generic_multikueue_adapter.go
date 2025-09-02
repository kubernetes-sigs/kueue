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

package generic

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
)

// genericAdapter implements the MultiKueueAdapter interface for external frameworks
// with hardcoded default behavior as specified in the KEP.
type genericAdapter struct {
	gvk schema.GroupVersionKind
}

var _ jobframework.MultiKueueAdapter = (*genericAdapter)(nil)

// NewGenericAdapter creates a new generic adapter for the given GVK.
func NewGenericAdapter(gvk schema.GroupVersionKind) jobframework.MultiKueueAdapter {
	return &genericAdapter{
		gvk: gvk,
	}
}

func (a *genericAdapter) SyncJob(ctx context.Context, localClient client.Client, remoteClient client.Client, key types.NamespacedName, workloadName, origin string) error {
	// Get the local object
	localObj := &unstructured.Unstructured{}
	localObj.SetGroupVersionKind(a.gvk)
	err := localClient.Get(ctx, key, localObj)
	if err != nil {
		return err
	}

	// Check if remote object already exists
	remoteObj := &unstructured.Unstructured{}
	remoteObj.SetGroupVersionKind(a.gvk)
	err = remoteClient.Get(ctx, key, remoteObj)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	if apierrors.IsNotFound(err) {
		// Create new remote object
		return a.createRemoteObject(ctx, remoteClient, localObj, workloadName, origin)
	}

	// Update existing remote object status
	return a.syncStatus(ctx, localClient, remoteClient, localObj, remoteObj)
}

func (a *genericAdapter) createRemoteObject(ctx context.Context, remoteClient client.Client, localObj *unstructured.Unstructured, workloadName, origin string) error {
	log := ctrl.LoggerFrom(ctx)

	// Create a copy of the local object for the remote cluster
	remoteObj := localObj.DeepCopy()
	remoteObj.SetResourceVersion("")

	// Apply default transformation: remove the managedBy field
	a.removeManagedByField(remoteObj)

	// Add MultiKueue labels
	labels := remoteObj.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}
	labels[constants.PrebuiltWorkloadLabel] = workloadName
	labels[kueue.MultiKueueOriginLabel] = origin
	remoteObj.SetLabels(labels)

	// Create the object in the remote cluster
	log.V(2).Info("Creating remote object", "gvk", a.gvk, "name", remoteObj.GetName(), "namespace", remoteObj.GetNamespace())
	return remoteClient.Create(ctx, remoteObj)
}

func (a *genericAdapter) syncStatus(ctx context.Context, localClient client.Client, remoteClient client.Client, localObj, remoteObj *unstructured.Unstructured) error {
	log := ctrl.LoggerFrom(ctx)

	// Create a deep copy of the original object to calculate the patch against.
	originalObj := localObj.DeepCopy()

	// Apply default sync: copy entire status from remote to local
	a.copyStatusFromRemote(localObj, remoteObj)

	// If there are no changes, do nothing.
	if equality.Semantic.DeepEqual(localObj, originalObj) {
		return nil
	}

	// Create the patch and apply it to the local object's status subresource.
	patch := client.MergeFrom(originalObj)
	log.V(2).Info("Syncing status from remote object", "gvk", a.gvk, "name", localObj.GetName())
	return localClient.Status().Patch(ctx, localObj, patch)
}

// removeManagedByField removes the .spec.managedBy field from the object
func (a *genericAdapter) removeManagedByField(obj *unstructured.Unstructured) {
	spec, exists, err := unstructured.NestedMap(obj.Object, "spec")
	if !exists || err != nil {
		return
	}

	delete(spec, "managedBy")
	obj.Object["spec"] = spec
}

// copyStatusFromRemote copies the entire status from remote object to local object
func (a *genericAdapter) copyStatusFromRemote(localObj, remoteObj *unstructured.Unstructured) {
	remoteStatus, exists, err := unstructured.NestedMap(remoteObj.Object, "status")
	if !exists || err != nil {
		return
	}

	// Set the status in the local object
	localObj.Object["status"] = remoteStatus
}

func (a *genericAdapter) DeleteRemoteObject(ctx context.Context, remoteClient client.Client, key types.NamespacedName) error {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(a.gvk)
	err := remoteClient.Get(ctx, key, obj)
	if err != nil {
		return client.IgnoreNotFound(err)
	}
	return client.IgnoreNotFound(remoteClient.Delete(ctx, obj, client.PropagationPolicy(metav1.DeletePropagationBackground)))
}

func (a *genericAdapter) KeepAdmissionCheckPending() bool {
	return false
}

func (a *genericAdapter) IsJobManagedByKueue(ctx context.Context, c client.Client, key types.NamespacedName) (bool, string, error) {
	if !features.Enabled(features.MultiKueueAdaptersForCustomJobs) {
		return false, "MultiKueueAdaptersForCustomJobs feature gate is disabled", nil
	}

	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(a.gvk)
	err := c.Get(ctx, key, obj)
	if err != nil {
		return false, "", err
	}

	// Use the default managedBy path: .spec.managedBy
	managedByValue, err := a.extractManagedByValue(obj, ".spec.managedBy")
	if err != nil {
		return false, "", fmt.Errorf("failed to extract managedBy value: %w", err)
	}

	if managedByValue != kueue.MultiKueueControllerName {
		return false, fmt.Sprintf("Expecting .spec.managedBy to be %q not %q", kueue.MultiKueueControllerName, managedByValue), nil
	}

	return true, "", nil
}

func (a *genericAdapter) extractManagedByValue(obj *unstructured.Unstructured, path string) (string, error) {
	// Split the path into parts (e.g., ".spec.managedBy" -> ["spec", "managedBy"])
	parts := a.splitJsonPath(path)
	if len(parts) == 0 {
		return "", fmt.Errorf("invalid path: %s", path)
	}

	// Navigate to the target field
	current := obj.Object
	for i, part := range parts {
		if i == len(parts)-1 {
			// Last part, extract the value
			if value, exists := current[part]; exists {
				if strValue, ok := value.(string); ok {
					return strValue, nil
				}
				return "", fmt.Errorf("managedBy value is not a string: %v", value)
			}
			// Return empty string when field is not found (no error)
			return "", nil
		}

		// Navigate deeper
		if next, exists := current[part]; exists {
			if mapValue, ok := next.(map[string]interface{}); ok {
				current = mapValue
			} else {
				return "", fmt.Errorf("path component %s is not a map", part)
			}
		} else {
			return "", fmt.Errorf("path component %s not found", part)
		}
	}

	return "", fmt.Errorf("unexpected path traversal end")
}

// splitJsonPath splits a JSON path into parts
func (a *genericAdapter) splitJsonPath(path string) []string {
	if len(path) == 0 {
		return []string{}
	}
	// Remove leading dot
	if path[0] == '.' {
		path = path[1:]
	}
	// Split by dots for JSON paths like "spec.managedBy"
	return strings.Split(path, ".")
}

func (a *genericAdapter) GVK() schema.GroupVersionKind {
	return a.gvk
}
