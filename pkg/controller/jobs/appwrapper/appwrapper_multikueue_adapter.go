/*
Copyright 2025 The Kubernetes Authors.

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

package appwrapper

import (
	"context"
	"errors"
	"fmt"

	awv1beta2 "github.com/project-codeflare/appwrapper/api/v1beta2"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/util/api"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
)

type multiKueueAdapter struct{}

var _ jobframework.MultiKueueAdapter = (*multiKueueAdapter)(nil)

func (b *multiKueueAdapter) SyncJob(ctx context.Context, localClient client.Client, remoteClient client.Client, key types.NamespacedName, workloadName, origin string) error {
	log := ctrl.LoggerFrom(ctx)

	localAppWrapper := awv1beta2.AppWrapper{}
	err := localClient.Get(ctx, key, &localAppWrapper)
	if err != nil {
		return err
	}

	remoteAppWrapper := awv1beta2.AppWrapper{}
	err = remoteClient.Get(ctx, key, &remoteAppWrapper)
	if client.IgnoreNotFound(err) != nil {
		return err
	}

	// if the remote exists, just copy the status
	if err == nil {
		if localAppWrapper.Spec.Suspend {
			// Ensure the appwrapper is unsuspended before updating its status; otherwise, it will fail when patching the spec.
			log.V(2).Info("Skipping the sync since the local appwrapper is still suspended")
			return nil
		}
		return clientutil.PatchStatus(ctx, localClient, &localAppWrapper, func() (bool, error) {
			localAppWrapper.Status = remoteAppWrapper.Status
			return true, nil
		})
	}

	// Make a copy of the local AppWrapper
	remoteAppWrapper = awv1beta2.AppWrapper{
		ObjectMeta: api.CloneObjectMetaForCreation(&localAppWrapper.ObjectMeta),
		Spec:       *localAppWrapper.Spec.DeepCopy(),
	}

	// add the prebuilt workload
	if remoteAppWrapper.Labels == nil {
		remoteAppWrapper.Labels = map[string]string{}
	}
	remoteAppWrapper.Labels[constants.PrebuiltWorkloadLabel] = workloadName
	remoteAppWrapper.Labels[kueue.MultiKueueOriginLabel] = origin

	// clear the managedBy to enable the remote AppWrapper controller to take over
	remoteAppWrapper.Spec.ManagedBy = nil

	return remoteClient.Create(ctx, &remoteAppWrapper)
}

func (b *multiKueueAdapter) DeleteRemoteObject(ctx context.Context, remoteClient client.Client, key types.NamespacedName) error {
	aw := awv1beta2.AppWrapper{}
	err := remoteClient.Get(ctx, key, &aw)
	if err != nil {
		return client.IgnoreNotFound(err)
	}
	return client.IgnoreNotFound(remoteClient.Delete(ctx, &aw))
}

func (b *multiKueueAdapter) GVK() schema.GroupVersionKind {
	return gvk
}

func (b *multiKueueAdapter) KeepAdmissionCheckPending() bool {
	return false
}

func (b *multiKueueAdapter) IsJobManagedByKueue(ctx context.Context, c client.Client, key types.NamespacedName) (bool, string, error) {
	aw := awv1beta2.AppWrapper{}
	err := c.Get(ctx, key, &aw)
	if err != nil {
		return false, "", err
	}
	awControllerName := ptr.Deref(aw.Spec.ManagedBy, "")
	if awControllerName != kueue.MultiKueueControllerName {
		return false, fmt.Sprintf("Expecting spec.managedBy to be %q not %q", kueue.MultiKueueControllerName, awControllerName), nil
	}
	return true, "", nil
}

var _ jobframework.MultiKueueWatcher = (*multiKueueAdapter)(nil)

func (*multiKueueAdapter) GetEmptyList() client.ObjectList {
	return &awv1beta2.AppWrapperList{}
}

func (*multiKueueAdapter) WorkloadKeyFor(o runtime.Object) (types.NamespacedName, error) {
	aw, ok := o.(*awv1beta2.AppWrapper)
	if !ok {
		return types.NamespacedName{}, errors.New("not an appwrapper")
	}

	prebuiltWl, hasPrebuiltWorkload := aw.Labels[constants.PrebuiltWorkloadLabel]
	if !hasPrebuiltWorkload {
		return types.NamespacedName{}, fmt.Errorf("no prebuilt workload found for appwrapper: %s", klog.KObj(aw))
	}

	return types.NamespacedName{Name: prebuiltWl, Namespace: aw.Namespace}, nil
}
