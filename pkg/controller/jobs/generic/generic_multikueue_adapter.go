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
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
)

// genericAdapter implements the MultiKueueAdapter interface for external frameworks
// based on configuration.
type genericAdapter struct {
	config configapi.ExternalFramework
	gvk    schema.GroupVersionKind
}

var _ jobframework.MultiKueueAdapter = (*genericAdapter)(nil)

// NewGenericAdapter creates a new generic adapter for the given external framework configuration.
func NewGenericAdapter(config configapi.ExternalFramework) jobframework.MultiKueueAdapter {
	return &genericAdapter{
		config: config,
		gvk: schema.GroupVersionKind{
			Group:   config.Group,
			Version: config.Version,
			Kind:    config.Kind,
		},
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

	// Apply creation patches
	if err := a.applyPatches(remoteObj, a.config.CreationPatches); err != nil {
		log.Error(err, "Failed to apply creation patches", "gvk", a.gvk, "name", localObj.GetName())
		return err
	}

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

	// Apply sync patches to the local object.
	if err := a.applySyncPatches(localObj, remoteObj, a.config.SyncPatches); err != nil {
		log.Error(err, "Failed to apply sync patches", "gvk", a.gvk, "name", localObj.GetName())
		return err
	}

	// If there are no changes, do nothing.
	if equality.Semantic.DeepEqual(localObj, originalObj) {
		return nil
	}

	// Create the patch and apply it to the local object's status subresource.
	patch := client.MergeFrom(originalObj)
	log.V(2).Info("Syncing status from remote object", "gvk", a.gvk, "name", localObj.GetName())
	return localClient.Status().Patch(ctx, localObj, patch)
}

func (a *genericAdapter) applyPatches(obj *unstructured.Unstructured, patches []configapi.JsonPatch) error {
	if len(patches) == 0 {
		return nil
	}

	// Convert object to JSON for patching
	objBytes, err := json.Marshal(obj.Object)
	if err != nil {
		return fmt.Errorf("failed to marshal object: %w", err)
	}

	// Apply each patch
	for _, patch := range patches {
		objBytes, err = a.applyJsonPatch(objBytes, patch)
		if err != nil {
			return fmt.Errorf("failed to apply patch %s %s: %w", patch.Op, patch.Path, err)
		}
	}

	// Unmarshal back to object
	var newObj map[string]interface{}
	if err := json.Unmarshal(objBytes, &newObj); err != nil {
		return fmt.Errorf("failed to unmarshal patched object: %w", err)
	}

	obj.Object = newObj
	return nil
}

func (a *genericAdapter) applySyncPatches(localObj, remoteObj *unstructured.Unstructured, patches []configapi.JsonPatch) error {
	if len(patches) == 0 {
		// Default behavior: copy entire status
		if remoteStatus, found, _ := unstructured.NestedMap(remoteObj.Object, "status"); found {
			unstructured.SetNestedMap(localObj.Object, remoteStatus, "status")
		}
		return nil
	}

	// Convert objects to JSON for patching
	localBytes, err := json.Marshal(localObj.Object)
	if err != nil {
		return fmt.Errorf("failed to marshal local object: %w", err)
	}

	remoteBytes, err := json.Marshal(remoteObj.Object)
	if err != nil {
		return fmt.Errorf("failed to marshal remote object: %w", err)
	}

	// Apply each sync patch
	for _, patch := range patches {
		if patch.From != "" {
			// For patches with "from" field, we need to extract value from remote object
			value, err := a.extractValueFromRemote(remoteBytes, patch.From)
			if err != nil {
				return fmt.Errorf("failed to extract value from remote object at %s: %w", patch.From, err)
			}
			patch.Value = value
		}

		localBytes, err = a.applyJsonPatch(localBytes, patch)
		if err != nil {
			return fmt.Errorf("failed to apply sync patch %s %s: %w", patch.Op, patch.Path, err)
		}
	}

	// Unmarshal back to object
	var newObj map[string]interface{}
	if err := json.Unmarshal(localBytes, &newObj); err != nil {
		return fmt.Errorf("failed to unmarshal patched local object: %w", err)
	}

	localObj.Object = newObj
	return nil
}

func (a *genericAdapter) applyJsonPatch(objBytes []byte, patch configapi.JsonPatch) ([]byte, error) {
	// This is a simplified implementation. In a real implementation, you would use
	// a proper JSON patch library like github.com/evanphx/json-patch
	// For now, we'll implement basic operations

	var obj map[string]interface{}
	if err := json.Unmarshal(objBytes, &obj); err != nil {
		return nil, err
	}

	switch patch.Op {
	case "replace":
		return a.applyReplacePatch(objBytes, patch)
	case "add":
		return a.applyAddPatch(objBytes, patch)
	case "remove":
		return a.applyRemovePatch(objBytes, patch)
	default:
		return nil, fmt.Errorf("unsupported patch operation: %s", patch.Op)
	}
}

func (a *genericAdapter) applyReplacePatch(objBytes []byte, patch configapi.JsonPatch) ([]byte, error) {
	var obj map[string]interface{}
	if err := json.Unmarshal(objBytes, &obj); err != nil {
		return nil, err
	}

	// Simple path implementation - assumes simple paths like "/spec/managedBy"
	path := patch.Path
	if len(path) > 0 && path[0] == '/' {
		path = path[1:]
	}

	// Split path and navigate to the target location
	parts := splitJsonPatchPath(path)
	current := obj
	for _, part := range parts[:len(parts)-1] {
		if current[part] == nil {
			current[part] = make(map[string]interface{})
		}
		if reflect.TypeOf(current[part]).Kind() == reflect.Map {
			current = current[part].(map[string]interface{})
		} else {
			return nil, fmt.Errorf("path %s is not a map", path)
		}
	}

	// Set the value
	lastPart := parts[len(parts)-1]
	current[lastPart] = patch.Value

	return json.Marshal(obj)
}

func (a *genericAdapter) applyAddPatch(objBytes []byte, patch configapi.JsonPatch) ([]byte, error) {
	// Similar to replace for now
	return a.applyReplacePatch(objBytes, patch)
}

func (a *genericAdapter) applyRemovePatch(objBytes []byte, patch configapi.JsonPatch) ([]byte, error) {
	var obj map[string]interface{}
	if err := json.Unmarshal(objBytes, &obj); err != nil {
		return nil, err
	}

	path := patch.Path
	if len(path) > 0 && path[0] == '/' {
		path = path[1:]
	}

	parts := splitJsonPatchPath(path)
	current := obj
	for _, part := range parts[:len(parts)-1] {
		if current[part] == nil {
			return nil, fmt.Errorf("path %s does not exist", path)
		}
		if reflect.TypeOf(current[part]).Kind() == reflect.Map {
			current = current[part].(map[string]interface{})
		} else {
			return nil, fmt.Errorf("path %s is not a map", path)
		}
	}

	lastPart := parts[len(parts)-1]
	delete(current, lastPart)

	return json.Marshal(obj)
}

func (a *genericAdapter) extractValueFromRemote(remoteBytes []byte, fromPath string) (interface{}, error) {
	var remoteObj map[string]interface{}
	if err := json.Unmarshal(remoteBytes, &remoteObj); err != nil {
		return nil, err
	}

	path := fromPath
	if len(path) > 0 && path[0] == '/' {
		path = path[1:]
	}

	parts := splitJsonPatchPath(path)
	current := remoteObj
	for _, part := range parts {
		if current[part] == nil {
			return nil, fmt.Errorf("path %s does not exist", fromPath)
		}
		if reflect.TypeOf(current[part]).Kind() == reflect.Map {
			current = current[part].(map[string]interface{})
		} else {
			return current[part], nil
		}
	}

	return current, nil
}

// splitJsonPatchPath splits a JSON Patch path into parts.
func splitJsonPatchPath(path string) []string {
	if len(path) == 0 {
		return []string{}
	}
	// Remove leading slash
	if path[0] == '/' {
		path = path[1:]
	}
	// Split by slashes for JSON Patch paths like "/spec/managedBy"
	return strings.Split(path, "/")
}

// splitJsonPath splits a JSON path into parts
func splitJsonPath(path string) []string {
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

	// Use the configured managedBy path or default to ".spec.managedBy"
	managedByPath := a.config.ManagedBy
	if managedByPath == "" {
		managedByPath = ".spec.managedBy"
	}

	// Extract the managedBy value using JSONPath
	managedByValue, err := a.extractManagedByValue(obj, managedByPath)
	if err != nil {
		return false, "", fmt.Errorf("failed to extract managedBy value: %w", err)
	}

	if managedByValue != kueue.MultiKueueControllerName {
		return false, fmt.Sprintf("Expecting %s to be %q not %q", managedByPath, kueue.MultiKueueControllerName, managedByValue), nil
	}

	return true, "", nil
}

func (a *genericAdapter) extractManagedByValue(obj *unstructured.Unstructured, path string) (string, error) {
	// Simple path extraction without jsonpath library
	path = strings.TrimPrefix(path, ".")
	if len(path) == 0 {
		return "", nil
	}

	parts := splitJsonPath(path)
	var current interface{} = obj.Object

	for _, part := range parts {
		if current == nil {
			return "", nil
		}
		if reflect.TypeOf(current).Kind() == reflect.Map {
			currentMap := current.(map[string]interface{})
			if val, exists := currentMap[part]; exists {
				current = val
			} else {
				return "", nil
			}
		} else {
			return "", fmt.Errorf("path %s is not a map", path)
		}
	}

	if current == nil {
		return "", nil
	}

	if reflect.TypeOf(current).Kind() == reflect.String {
		return current.(string), nil
	}

	return "", fmt.Errorf("managedBy value at %s is not a string", path)
}

func (a *genericAdapter) GVK() schema.GroupVersionKind {
	return a.gvk
}