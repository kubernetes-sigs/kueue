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
	"encoding/json"
	"errors"
	"fmt"
	"maps"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
)

var ErrRemoteObjectAccessUnsupported = errors.New("remote object access cannot be identity-guarded")
var ErrRemoteObjectWriteUnsupported = errors.New("remote object write cannot be identity-guarded")

// MultiKueueManagerObjectUIDsForWorkload returns the manager execution-object
// identities persisted in a Workload's owner references for adapterGVK.
func MultiKueueManagerObjectUIDsForWorkload(workload *kueue.Workload, adapterGVK schema.GroupVersionKind) (map[string]types.UID, error) {
	if workload == nil {
		return nil, ErrMultiKueueWorkloadNameEmpty
	}
	uids := make(map[string]types.UID)
	for _, owner := range workload.OwnerReferences {
		if schema.FromAPIVersionAndKind(owner.APIVersion, owner.Kind) != adapterGVK || owner.UID == "" {
			continue
		}
		if existing := uids[owner.Name]; existing != "" && existing != owner.UID {
			return nil, fmt.Errorf(
				"%w: manager Workload %q has conflicting UIDs %q and %q for %s %q",
				ErrRemoteObjectNotOwnedByMultiKueue,
				client.ObjectKeyFromObject(workload),
				existing,
				owner.UID,
				adapterGVK.Kind,
				owner.Name,
			)
		}
		uids[owner.Name] = owner.UID
	}
	return uids, nil
}

func managerObjectUIDForWorkload(workload *kueue.Workload, adapterGVK schema.GroupVersionKind, key types.NamespacedName) (types.UID, map[string]types.UID, error) {
	managerObjectUIDs, err := MultiKueueManagerObjectUIDsForWorkload(workload, adapterGVK)
	if err != nil {
		return "", nil, err
	}
	referenceUID := managerObjectUIDs[key.Name]
	labelUID := types.UID(workload.Labels[controllerconstants.JobUIDLabel])
	ownerUID := types.UID("")
	if owner := metav1.GetControllerOf(workload); owner != nil {
		ownerUID = owner.UID
	}
	for _, candidate := range []types.UID{referenceUID, labelUID, ownerUID} {
		if candidate == "" {
			continue
		}
		if referenceUID != "" && candidate != referenceUID {
			return "", nil, fmt.Errorf(
				"%w: manager Workload %q has conflicting controller UIDs %q and %q",
				ErrRemoteObjectNotOwnedByMultiKueue,
				client.ObjectKeyFromObject(workload),
				referenceUID,
				candidate,
			)
		}
		referenceUID = candidate
	}
	if referenceUID != "" {
		managerObjectUIDs[key.Name] = referenceUID
		return referenceUID, managerObjectUIDs, nil
	}
	return "", nil, fmt.Errorf("%w: manager Workload %q has no UID for %s %q", ErrRemoteObjectNotOwnedByMultiKueue, client.ObjectKeyFromObject(workload), adapterGVK.Kind, key.Name)
}

func validateRemoteObjectManagerUID(obj client.Object, expected types.UID) error {
	actual := types.UID(obj.GetAnnotations()[kueue.MultiKueueOriginUIDAnnotation])
	if expected == "" || actual != expected {
		return fmt.Errorf("%w: expected %q=%q on %T %q, got %q", ErrRemoteObjectNotOwnedByMultiKueue, kueue.MultiKueueOriginUIDAnnotation, expected, obj, client.ObjectKeyFromObject(obj), actual)
	}
	return nil
}

type remoteObjectOwnershipClient struct {
	delegate                client.Client
	localClient             client.Client
	localReader             client.Reader
	gvk                     schema.GroupVersionKind
	controllerKey           types.NamespacedName
	association             MultiKueueObjectAssociation
	managerObjectUIDs       map[string]types.UID
	expectedRemoteObjectUID types.UID
	multiWorkload           bool
	workloadReassignment    MultiKueueAdapterWithWorkloadReassignment
	allowCreate             bool
}

var _ client.Client = (*remoteObjectOwnershipClient)(nil)

func (c *remoteObjectOwnershipClient) isAdapterObject(obj client.Object) (bool, error) {
	gvk, err := apiutil.GVKForObject(obj, c.Scheme())
	if err != nil {
		return false, err
	}
	return gvk == c.gvk, nil
}

func (c *remoteObjectOwnershipClient) expectedManagerUID(ctx context.Context, key client.ObjectKey) (types.UID, error) {
	expected := c.managerObjectUIDs[key.Name]
	if c.localReader == nil {
		if expected == "" && key == c.controllerKey {
			expected = c.association.ManagerObjectUID
		}
		if expected == "" {
			return "", fmt.Errorf("%w: no trusted manager UID for remote object %q", ErrRemoteObjectNotOwnedByMultiKueue, key)
		}
		return expected, nil
	}
	if expected == "" {
		return "", fmt.Errorf("%w: manager Workload has no trusted UID for remote object %q", ErrRemoteObjectNotOwnedByMultiKueue, key)
	}
	localObject := &metav1.PartialObjectMetadata{}
	localObject.SetGroupVersionKind(c.gvk)
	if err := c.localReader.Get(ctx, key, localObject); err != nil {
		return "", fmt.Errorf("%w: reading manager object %q: %v", ErrRemoteObjectNotOwnedByMultiKueue, key, err)
	}
	if localObject.UID != expected {
		return "", fmt.Errorf("%w: manager object %q UID %q does not match Workload-bound UID %q", ErrRemoteObjectNotOwnedByMultiKueue, key, localObject.UID, expected)
	}
	return expected, nil
}

func (c *remoteObjectOwnershipClient) validateAdapterObject(ctx context.Context, obj client.Object, allowReassignment bool) error {
	key := client.ObjectKeyFromObject(obj)
	if key.Namespace != c.controllerKey.Namespace {
		return fmt.Errorf("%w: expected Namespace %q on %T %q, got %q", ErrRemoteObjectNotOwnedByMultiKueue, c.controllerKey.Namespace, obj, key, key.Namespace)
	}
	if obj.GetLabels()[kueue.MultiKueueOriginLabel] != c.association.Origin {
		return fmt.Errorf("%w: unexpected MultiKueue origin on %T %q", ErrRemoteObjectNotOwnedByMultiKueue, obj, key)
	}
	expectedManagerUID, err := c.expectedManagerUID(ctx, key)
	if err != nil {
		return err
	}
	if err := validateRemoteObjectManagerUID(obj, expectedManagerUID); err != nil {
		return err
	}
	if c.multiWorkload {
		return nil
	}
	objectWorkload, err := MultiKueueWorkloadNameFor(obj)
	if err != nil {
		return err
	}
	if objectWorkload == c.association.WorkloadName {
		return nil
	}
	if allowReassignment && c.workloadReassignment != nil && key == c.controllerKey {
		allowed, err := c.workloadReassignment.CanReassignWorkload(ctx, c.localClient, key)
		if err != nil {
			return err
		}
		if allowed {
			return nil
		}
	}
	return fmt.Errorf("%w: %T %q belongs to Workload %q, expected %q", ErrRemoteObjectNotOwnedByMultiKueue, obj, key, objectWorkload, c.association.WorkloadName)
}

func (c *remoteObjectOwnershipClient) getAndValidateAdapterObject(ctx context.Context, key client.ObjectKey, allowReassignment bool) (*metav1.PartialObjectMetadata, error) {
	remoteObject := &metav1.PartialObjectMetadata{}
	remoteObject.SetGroupVersionKind(c.gvk)
	if err := c.delegate.Get(ctx, key, remoteObject); err != nil {
		return nil, err
	}
	if err := c.validateAdapterObject(ctx, remoteObject, allowReassignment); err != nil {
		return nil, err
	}
	if key == c.controllerKey && c.expectedRemoteObjectUID != "" && remoteObject.UID != c.expectedRemoteObjectUID {
		return nil, fmt.Errorf("%w: expected remote UID %q on %T %q, got %q", ErrRemoteObjectNotOwnedByMultiKueue, c.expectedRemoteObjectUID, remoteObject, key, remoteObject.UID)
	}
	return remoteObject, nil
}

func (c *remoteObjectOwnershipClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	isAdapterObject, err := c.isAdapterObject(obj)
	if err != nil {
		return err
	}
	if !isAdapterObject {
		return fmt.Errorf("%w: Get on %T %q", ErrRemoteObjectAccessUnsupported, obj, key)
	}
	if err := c.delegate.Get(ctx, key, obj, opts...); err != nil {
		return err
	}
	return c.validateAdapterObject(ctx, obj, true)
}

func (c *remoteObjectOwnershipClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	gvk, err := apiutil.GVKForObject(list, c.Scheme())
	if err != nil {
		return err
	}
	if gvk.GroupVersion() != c.gvk.GroupVersion() || gvk.Kind != c.gvk.Kind+"List" {
		return fmt.Errorf("%w: List on %T", ErrRemoteObjectAccessUnsupported, list)
	}
	if err := c.delegate.List(ctx, list, opts...); err != nil {
		return err
	}
	items, err := apimeta.ExtractList(list)
	if err != nil {
		return err
	}
	filtered := make([]runtime.Object, 0, len(items))
	for _, item := range items {
		obj, ok := item.(client.Object)
		if !ok {
			return fmt.Errorf("validating remote list item %T: object metadata is unavailable", item)
		}
		isAdapterObject, err := c.isAdapterObject(obj)
		if err != nil {
			return err
		}
		if !isAdapterObject {
			return fmt.Errorf("%w: List returned unexpected %T", ErrRemoteObjectAccessUnsupported, obj)
		}
		if err := c.validateAdapterObject(ctx, obj, false); err != nil {
			if errors.Is(err, ErrRemoteObjectNotOwnedByMultiKueue) {
				continue
			}
			return err
		}
		filtered = append(filtered, item)
	}
	return apimeta.SetList(list, filtered)
}

func (c *remoteObjectOwnershipClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	isAdapterObject, err := c.isAdapterObject(obj)
	if err != nil {
		return err
	}
	if !isAdapterObject {
		return fmt.Errorf("%w: Create on %T %q", ErrRemoteObjectWriteUnsupported, obj, client.ObjectKeyFromObject(obj))
	}
	if !c.allowCreate {
		return fmt.Errorf("%w: Create on %T %q", ErrRemoteObjectWriteUnsupported, obj, client.ObjectKeyFromObject(obj))
	}
	expectedManagerUID, err := c.expectedManagerUID(ctx, client.ObjectKeyFromObject(obj))
	if err != nil {
		return err
	}
	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string, 1)
	}
	if actual := types.UID(annotations[kueue.MultiKueueOriginUIDAnnotation]); actual != "" && actual != expectedManagerUID {
		return fmt.Errorf("%w: refusing to replace manager UID %q on %T %q", ErrRemoteObjectNotOwnedByMultiKueue, actual, obj, client.ObjectKeyFromObject(obj))
	}
	annotations[kueue.MultiKueueOriginUIDAnnotation] = string(expectedManagerUID)
	obj.SetAnnotations(annotations)
	if err := c.validateAdapterObject(ctx, obj, false); err != nil {
		return err
	}
	err = c.delegate.Create(ctx, obj, opts...)
	if !apierrors.IsAlreadyExists(err) {
		return err
	}

	// Some multi-Workload adapters intentionally ignore AlreadyExists because
	// several Workloads can race to create the same controller object. Do not
	// let that compatibility behavior turn a foreign pre-created object into a
	// successful sync: authenticate the object that won the create race before
	// returning the error to the adapter.
	remoteObject := &metav1.PartialObjectMetadata{}
	remoteObject.SetGroupVersionKind(c.gvk)
	if getErr := c.delegate.Get(ctx, client.ObjectKeyFromObject(obj), remoteObject); getErr != nil {
		return getErr
	}
	if validateErr := c.validateAdapterObject(ctx, remoteObject, false); validateErr != nil {
		return validateErr
	}
	return err
}

func (c *remoteObjectOwnershipClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	isAdapterObject, err := c.isAdapterObject(obj)
	if err != nil {
		return err
	}
	if !isAdapterObject {
		return fmt.Errorf("%w: Delete on %T %q", ErrRemoteObjectWriteUnsupported, obj, client.ObjectKeyFromObject(obj))
	}
	remoteObject, err := c.getAndValidateAdapterObject(ctx, client.ObjectKeyFromObject(obj), false)
	if err != nil {
		if client.IgnoreNotFound(err) == nil {
			return nil
		}
		return err
	}
	uid := remoteObject.UID
	return c.delegate.Delete(ctx, obj, append(opts, client.Preconditions{UID: &uid})...)
}

func (c *remoteObjectOwnershipClient) validateAdapterWrite(ctx context.Context, obj client.Object) error {
	if err := c.validateAdapterObject(ctx, obj, true); err != nil {
		return err
	}
	remoteObject, err := c.getAndValidateAdapterObject(ctx, client.ObjectKeyFromObject(obj), true)
	if err != nil {
		return err
	}
	if obj.GetUID() == "" || obj.GetUID() != remoteObject.UID {
		return fmt.Errorf("%w: expected UID %q on %T %q, got %q", ErrRemoteObjectNotOwnedByMultiKueue, remoteObject.UID, obj, client.ObjectKeyFromObject(obj), obj.GetUID())
	}
	if obj.GetResourceVersion() == "" || obj.GetResourceVersion() != remoteObject.ResourceVersion {
		return fmt.Errorf(
			"%w: expected resourceVersion %q on %T %q, got %q",
			ErrRemoteObjectNotOwnedByMultiKueue,
			remoteObject.ResourceVersion,
			obj,
			client.ObjectKeyFromObject(obj),
			obj.GetResourceVersion(),
		)
	}
	return nil
}

func (c *remoteObjectOwnershipClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	isAdapterObject, err := c.isAdapterObject(obj)
	if err != nil {
		return err
	}
	if !isAdapterObject {
		return fmt.Errorf("%w: Update on %T %q", ErrRemoteObjectWriteUnsupported, obj, client.ObjectKeyFromObject(obj))
	}
	if err := c.validateAdapterWrite(ctx, obj); err != nil {
		return err
	}
	return c.delegate.Update(ctx, obj, opts...)
}

func patchResourceVersion(obj client.Object, patch client.Patch) (string, error) {
	data, err := patch.Data(obj)
	if err != nil {
		return "", err
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		return "", fmt.Errorf("patch has no enforceable resourceVersion: %w", err)
	}
	metadata, ok := document["metadata"].(map[string]any)
	if !ok {
		return "", errors.New("patch has no enforceable metadata.resourceVersion")
	}
	resourceVersion, ok := metadata["resourceVersion"].(string)
	if !ok || resourceVersion == "" {
		return "", errors.New("patch has no enforceable metadata.resourceVersion")
	}
	return resourceVersion, nil
}

func (c *remoteObjectOwnershipClient) validateAdapterPatch(ctx context.Context, obj client.Object, patch client.Patch) error {
	if err := c.validateAdapterWrite(ctx, obj); err != nil {
		return err
	}
	resourceVersion, err := patchResourceVersion(obj, patch)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRemoteObjectWriteUnsupported, err)
	}
	if resourceVersion != obj.GetResourceVersion() {
		return fmt.Errorf("%w: patch resourceVersion %q does not match object resourceVersion %q", ErrRemoteObjectNotOwnedByMultiKueue, resourceVersion, obj.GetResourceVersion())
	}
	return nil
}

func (c *remoteObjectOwnershipClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	isAdapterObject, err := c.isAdapterObject(obj)
	if err != nil {
		return err
	}
	if !isAdapterObject {
		return fmt.Errorf("%w: Patch on %T %q", ErrRemoteObjectWriteUnsupported, obj, client.ObjectKeyFromObject(obj))
	}
	if err := c.validateAdapterPatch(ctx, obj, patch); err != nil {
		return err
	}
	return c.delegate.Patch(ctx, obj, patch, opts...)
}

func (c *remoteObjectOwnershipClient) Apply(_ context.Context, obj runtime.ApplyConfiguration, _ ...client.ApplyOption) error {
	return fmt.Errorf("%w: Apply on %T", ErrRemoteObjectWriteUnsupported, obj)
}

func (c *remoteObjectOwnershipClient) DeleteAllOf(ctx context.Context, obj client.Object, opts ...client.DeleteAllOfOption) error {
	isAdapterObject, err := c.isAdapterObject(obj)
	if err != nil {
		return err
	}
	if !isAdapterObject {
		return fmt.Errorf("%w: DeleteAllOf on %T", ErrRemoteObjectWriteUnsupported, obj)
	}
	deleteOptions := (&client.DeleteAllOfOptions{}).ApplyOptions(opts)
	if deleteOptions.Namespace != c.controllerKey.Namespace {
		return fmt.Errorf("%w: expected Namespace %q when deleting all %T, got %q", ErrRemoteObjectNotOwnedByMultiKueue, c.controllerKey.Namespace, obj, deleteOptions.Namespace)
	}
	list := &metav1.PartialObjectMetadataList{}
	list.SetGroupVersionKind(c.gvk.GroupVersion().WithKind(c.gvk.Kind + "List"))
	if err := c.List(ctx, list, &deleteOptions.ListOptions); err != nil {
		return err
	}
	return apimeta.EachListItem(list, func(item runtime.Object) error {
		remoteObject, ok := item.(client.Object)
		if !ok {
			return fmt.Errorf("deleting remote list item %T: object metadata is unavailable", item)
		}
		return c.Delete(ctx, remoteObject, &deleteOptions.DeleteOptions)
	})
}

type remoteObjectOwnershipSubResourceClient struct {
	delegate client.SubResourceClient
	parent   *remoteObjectOwnershipClient
}

var _ client.SubResourceClient = (*remoteObjectOwnershipSubResourceClient)(nil)

func (c *remoteObjectOwnershipSubResourceClient) isAdapterObject(obj client.Object) (bool, error) {
	return c.parent.isAdapterObject(obj)
}

func (c *remoteObjectOwnershipSubResourceClient) Get(ctx context.Context, obj client.Object, subResource client.Object, opts ...client.SubResourceGetOption) error {
	isAdapterObject, err := c.isAdapterObject(obj)
	if err != nil {
		return err
	}
	if !isAdapterObject {
		return fmt.Errorf("%w: subresource Get on %T %q", ErrRemoteObjectAccessUnsupported, obj, client.ObjectKeyFromObject(obj))
	}
	return fmt.Errorf("%w: subresource Get on %T %q has no atomic identity check", ErrRemoteObjectAccessUnsupported, obj, client.ObjectKeyFromObject(obj))
}

func (c *remoteObjectOwnershipSubResourceClient) Create(ctx context.Context, obj client.Object, subResource client.Object, opts ...client.SubResourceCreateOption) error {
	if _, err := c.isAdapterObject(obj); err != nil {
		return err
	}
	return fmt.Errorf("%w: subresource Create on %T %q", ErrRemoteObjectWriteUnsupported, obj, client.ObjectKeyFromObject(obj))
}

func (c *remoteObjectOwnershipSubResourceClient) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	isAdapterObject, err := c.isAdapterObject(obj)
	if err != nil {
		return err
	}
	if !isAdapterObject {
		return fmt.Errorf("%w: subresource Update on %T %q", ErrRemoteObjectWriteUnsupported, obj, client.ObjectKeyFromObject(obj))
	}
	updateOptions := (&client.SubResourceUpdateOptions{}).ApplyOptions(opts)
	if updateOptions.SubResourceBody != nil {
		return fmt.Errorf("%w: subresource Update on %T %q uses an alternate unvalidated body", ErrRemoteObjectWriteUnsupported, obj, client.ObjectKeyFromObject(obj))
	}
	if err := c.parent.validateAdapterWrite(ctx, obj); err != nil {
		return err
	}
	return c.delegate.Update(ctx, obj, opts...)
}

func (c *remoteObjectOwnershipSubResourceClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	isAdapterObject, err := c.isAdapterObject(obj)
	if err != nil {
		return err
	}
	if !isAdapterObject {
		return fmt.Errorf("%w: subresource Patch on %T %q", ErrRemoteObjectWriteUnsupported, obj, client.ObjectKeyFromObject(obj))
	}
	patchOptions := (&client.SubResourcePatchOptions{}).ApplyOptions(opts)
	if patchOptions.SubResourceBody != nil {
		return fmt.Errorf("%w: subresource Patch on %T %q uses an alternate unvalidated body", ErrRemoteObjectWriteUnsupported, obj, client.ObjectKeyFromObject(obj))
	}
	if err := c.parent.validateAdapterPatch(ctx, obj, patch); err != nil {
		return err
	}
	return c.delegate.Patch(ctx, obj, patch, opts...)
}

func (c *remoteObjectOwnershipSubResourceClient) Apply(_ context.Context, obj runtime.ApplyConfiguration, _ ...client.SubResourceApplyOption) error {
	return fmt.Errorf("%w: subresource Apply on %T", ErrRemoteObjectWriteUnsupported, obj)
}

func (c *remoteObjectOwnershipClient) Status() client.SubResourceWriter {
	return c.SubResource("status")
}

func (c *remoteObjectOwnershipClient) SubResource(subResource string) client.SubResourceClient {
	return &remoteObjectOwnershipSubResourceClient{delegate: c.delegate.SubResource(subResource), parent: c}
}

func (c *remoteObjectOwnershipClient) Scheme() *runtime.Scheme {
	return c.delegate.Scheme()
}

func (c *remoteObjectOwnershipClient) RESTMapper() apimeta.RESTMapper {
	return c.delegate.RESTMapper()
}

func (c *remoteObjectOwnershipClient) GroupVersionKindFor(obj runtime.Object) (schema.GroupVersionKind, error) {
	return c.delegate.GroupVersionKindFor(obj)
}

func (c *remoteObjectOwnershipClient) IsObjectNamespaced(obj runtime.Object) (bool, error) {
	return c.delegate.IsObjectNamespaced(obj)
}

func newRemoteObjectOwnershipClientForSync(
	localClient client.Client,
	localReader client.Reader,
	remoteClient client.Client,
	adapter MultiKueueAdapter,
	key types.NamespacedName,
	localWorkload *kueue.Workload,
	origin string,
) (*remoteObjectOwnershipClient, error) {
	if origin == "" {
		return nil, ErrMultiKueueOriginEmpty
	}
	if localWorkload == nil || localWorkload.Name == "" {
		return nil, ErrMultiKueueWorkloadNameEmpty
	}
	if localReader == nil {
		return nil, fmt.Errorf("%w: manager object API reader is unavailable", ErrRemoteObjectAccessUnsupported)
	}
	managerObjectUID, managerObjectUIDs, err := managerObjectUIDForWorkload(localWorkload, adapter.GVK(), key)
	if err != nil {
		return nil, err
	}
	reassignmentAdapter, _ := adapter.(MultiKueueAdapterWithWorkloadReassignment)
	_, multiWorkload := adapter.(MultiKueueMultiWorkloadAdapter)
	return &remoteObjectOwnershipClient{
		delegate:             remoteClient,
		localClient:          localClient,
		localReader:          localReader,
		gvk:                  adapter.GVK(),
		controllerKey:        key,
		association:          MultiKueueObjectAssociation{Origin: origin, WorkloadName: localWorkload.Name, ManagerObjectUID: managerObjectUID},
		managerObjectUIDs:    managerObjectUIDs,
		multiWorkload:        multiWorkload,
		workloadReassignment: reassignmentAdapter,
		allowCreate:          true,
	}, nil
}

func newRemoteObjectOwnershipClientForCleanup(
	remoteClient client.Client,
	adapter MultiKueueAdapter,
	key types.NamespacedName,
	cleanupContext MultiKueueRemoteObjectCleanupContext,
) (*remoteObjectOwnershipClient, error) {
	if cleanupContext.Association.Origin == "" {
		return nil, ErrMultiKueueOriginEmpty
	}
	if cleanupContext.Association.WorkloadName == "" {
		return nil, ErrMultiKueueWorkloadNameEmpty
	}
	_, multiWorkload := adapter.(MultiKueueMultiWorkloadAdapter)
	return &remoteObjectOwnershipClient{
		delegate:                remoteClient,
		gvk:                     adapter.GVK(),
		controllerKey:           key,
		association:             cleanupContext.Association,
		managerObjectUIDs:       maps.Clone(cleanupContext.ManagerObjectUIDs),
		expectedRemoteObjectUID: cleanupContext.RemoteObjectUID,
		multiWorkload:           multiWorkload,
	}, nil
}

// SyncJobWithRemoteObjectOwnership runs the adapter with a remote client that
// authenticates every adapter object at the read and write boundary.
func SyncJobWithRemoteObjectOwnership(
	ctx context.Context,
	localClient client.Client,
	localReader client.Reader,
	remoteClient client.Client,
	adapter MultiKueueAdapter,
	key types.NamespacedName,
	localWorkload *kueue.Workload,
	origin string,
) (bool, error) {
	validatingClient, err := newRemoteObjectOwnershipClientForSync(localClient, localReader, remoteClient, adapter, key, localWorkload, origin)
	if err != nil {
		return false, err
	}
	return adapter.SyncJob(ctx, localClient, validatingClient, key, localWorkload.Name, origin)
}
