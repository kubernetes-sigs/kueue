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

package externalframeworks

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/api"
)

// Adapter implements the MultiKueueAdapter interface for external frameworks
// with hardcoded default behavior as specified in the KEP.
type Adapter struct {
	gvk schema.GroupVersionKind
}

var (
	_ jobframework.MultiKueueAdapter = (*Adapter)(nil)
	_ jobframework.MultiKueueWatcher = (*Adapter)(nil)
)

// NewAdapter creates a new adapter for the given GVK.
func NewAdapter(gvk schema.GroupVersionKind) jobframework.MultiKueueAdapter {
	return &Adapter{
		gvk: gvk,
	}
}

// SyncJob synchronizes a job resource between the local and remote clusters.
// It ensures that the remote cluster has a corresponding object for the local job,
// creating it if necessary, and updates the status of the local object based on the remote object.
func (a *Adapter) SyncJob(ctx context.Context, localClient client.Client, remoteClient client.Client, key types.NamespacedName, workloadName, origin string) (deferred bool, err error) {
	// Get the local object
	localObj := &unstructured.Unstructured{}
	localObj.SetGroupVersionKind(a.gvk)
	err = localClient.Get(ctx, key, localObj)
	if err != nil {
		return false, err
	}

	// Check if remote object already exists
	remoteObj := &unstructured.Unstructured{}
	remoteObj.SetGroupVersionKind(a.gvk)
	err = remoteClient.Get(ctx, key, remoteObj)
	if err != nil && !apierrors.IsNotFound(err) {
		return false, err
	}

	if apierrors.IsNotFound(err) {
		// Create new remote object
		return false, a.createRemoteObject(ctx, remoteClient, localObj, workloadName, origin)
	}

	// Update existing remote object status
	return false, a.syncStatus(ctx, localClient, localObj, remoteObj)
}

func (a *Adapter) createRemoteObject(ctx context.Context, remoteClient client.Client, localObj *unstructured.Unstructured, workloadName, origin string) error {
	log := ctrl.LoggerFrom(ctx)

	remoteObj, err := cloneUnstructuredForCreation(localObj)
	if err != nil {
		return err
	}

	// Apply default transformation: remove the managedBy field
	a.removeManagedByField(remoteObj)

	// Add prebuilt workload name and multikueue origin
	jobframework.SetMultiKueueMeta(remoteObj, workloadName, origin)

	// Create the object in the remote cluster
	log.V(2).Info("Creating remote object", "gvk", a.gvk, "name", remoteObj.GetName(), "namespace", remoteObj.GetNamespace())
	return remoteClient.Create(ctx, remoteObj)
}

// cloneUnstructuredForCreation creates a copy of obj containing only the GVK, name,
// namespace, labels, annotations and spec, the unstructured analogue of
// api.CloneObjectMetaForCreation, which it reuses for the metadata. Dropping the rest
// (notably ownerReferences, whose UIDs don't exist remotely) avoids the remote GC
// deleting the copy in a create-delete loop.
func cloneUnstructuredForCreation(obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	var meta metav1.ObjectMeta
	if metaMap, found, err := unstructured.NestedMap(obj.Object, "metadata"); err != nil {
		return nil, fmt.Errorf("reading metadata: %w", err)
	} else if found {
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(metaMap, &meta); err != nil {
			return nil, fmt.Errorf("converting metadata to ObjectMeta: %w", err)
		}
	}

	cleaned := api.CloneObjectMetaForCreation(&meta)
	cleanedMeta, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&cleaned)
	if err != nil {
		return nil, fmt.Errorf("converting cleaned metadata to unstructured: %w", err)
	}

	remote := &unstructured.Unstructured{Object: map[string]any{}}
	remote.SetGroupVersionKind(obj.GroupVersionKind())
	remote.Object["metadata"] = cleanedMeta
	if spec, found, _ := unstructured.NestedFieldCopy(obj.Object, "spec"); found {
		remote.Object["spec"] = spec
	}
	return remote, nil
}

func (a *Adapter) syncStatus(ctx context.Context, localClient client.Client, localObj, remoteObj *unstructured.Unstructured) error {
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
func (a *Adapter) removeManagedByField(obj *unstructured.Unstructured) {
	spec, exists, err := unstructured.NestedMap(obj.Object, "spec")
	if !exists || err != nil {
		return
	}

	delete(spec, "managedBy")
	obj.Object["spec"] = spec
}

// copyStatusFromRemote copies the entire status from remote object to local object
func (a *Adapter) copyStatusFromRemote(localObj, remoteObj *unstructured.Unstructured) {
	remoteStatus, exists, err := unstructured.NestedMap(remoteObj.Object, "status")
	if !exists || err != nil {
		return
	}

	// Set the status in the local object
	localObj.Object["status"] = remoteStatus
}

// DeleteRemoteObject deletes the remote object identified by the given key from the remote cluster.
// It first attempts to retrieve the object, and if found, deletes it with background propagation policy.
func (a *Adapter) DeleteRemoteObject(ctx context.Context, _ client.Client, remoteClient client.Client, key types.NamespacedName) error {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(a.gvk)
	obj.SetName(key.Name)
	obj.SetNamespace(key.Namespace)
	return client.IgnoreNotFound(remoteClient.Delete(ctx, obj, client.PropagationPolicy(metav1.DeletePropagationBackground)))
}

// IsJobManagedByKueue checks if the job object identified by the given key is managed by Kueue.
// It returns:
// - a bool indicating if the job is managed by Kueue
// - a reason indicating why the job is not managed by Kueue
// - any API error encountered during the check
func (a *Adapter) IsJobManagedByKueue(ctx context.Context, c client.Client, key types.NamespacedName) (bool, string, error) {
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
	managedByValue, _, err := unstructured.NestedString(obj.Object, "spec", "managedBy")
	if err != nil {
		return false, "", fmt.Errorf("failed to read .spec.managedBy: %w", err)
	}

	if managedByValue != kueue.MultiKueueControllerName {
		return false, fmt.Sprintf("Expecting .spec.managedBy to be %q not %q", kueue.MultiKueueControllerName, managedByValue), nil
	}

	return true, "", nil
}

// GVK returns the GroupVersionKind for this adapter.
func (a *Adapter) GVK() schema.GroupVersionKind {
	return a.gvk
}

// GetEmptyList returns an empty unstructured list object with the GroupVersionKind
// set to the adapter's GVK with "List" appended to the Kind. This is useful for
// performing list operations on the resource type managed by the adapter.
func (a *Adapter) GetEmptyList() client.ObjectList {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   a.gvk.Group,
		Version: a.gvk.Version,
		Kind:    a.gvk.Kind + "List",
	})
	return list
}

// WorkloadKeysFor returns the keys of the workloads of interest for the given object.
// It checks the labels of the object for the prebuilt workload label and
// returns the namespaced name of the workload.
func (a *Adapter) WorkloadKeysFor(o runtime.Object) ([]types.NamespacedName, error) {
	unstructuredObj, isUnstructured := o.(*unstructured.Unstructured)
	if !isUnstructured {
		return nil, fmt.Errorf("not an unstructured object, got type: %T", o)
	}

	objGVK := unstructuredObj.GroupVersionKind()
	if objGVK.Group != a.gvk.Group || objGVK.Version != a.gvk.Version || objGVK.Kind != a.gvk.Kind {
		return nil, fmt.Errorf("unexpected GVK: expected %s, got %s for object %s", a.gvk, objGVK, klog.KObj(unstructuredObj))
	}

	prebuiltWorkload := jobframework.PrebuiltWorkloadNameFor(unstructuredObj)
	if prebuiltWorkload == "" {
		return nil, fmt.Errorf("no prebuilt workload found for %s: %s", a.gvk.Kind, klog.KObj(unstructuredObj))
	}

	return []types.NamespacedName{{Name: prebuiltWorkload, Namespace: unstructuredObj.GetNamespace()}}, nil
}
