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

package pod

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/util/api"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
)

type multikueueAdapter struct{}

var _ jobframework.MultiKueueAdapter = (*multikueueAdapter)(nil)

func (b *multikueueAdapter) SyncJob(ctx context.Context, localClient client.Client, remoteClient client.Client, key types.NamespacedName, workloadName, origin string) error {
	log := ctrl.LoggerFrom(ctx)

	localPod := corev1.Pod{}
	err := localClient.Get(ctx, key, &localPod)
	if err != nil {
		return err
	}

	remotePod := corev1.Pod{}
	err = remoteClient.Get(ctx, key, &remotePod)
	if client.IgnoreNotFound(err) != nil {
		return err
	}

	// the remote pod exists
	if err == nil {
		if localPod.DeletionTimestamp != nil {
			// Ensure the local pod is not terminating before updating its status; otherwise, it will fail when patching the status.
			log.V(2).Info("Skipping the sync since the local pod is terminating")
			return nil
		}
		// Patch the status of the local pod to match the remote pod
		return clientutil.PatchStatus(ctx, localClient, &localPod, func() (bool, error) {
			localPod.Status = remotePod.Status
			return true, nil
		})
	}

	remotePod = corev1.Pod{
		ObjectMeta: api.CloneObjectMetaForCreation(&localPod.ObjectMeta),
		Spec:       *localPod.Spec.DeepCopy(),
	}

	// add the prebuilt workload
	if remotePod.Labels == nil {
		remotePod.Labels = map[string]string{}
	}
	remotePod.Labels[constants.PrebuiltWorkloadLabel] = workloadName
	remotePod.Labels[kueue.MultiKueueOriginLabel] = origin

	return remoteClient.Create(ctx, &remotePod)
}

func (b *multikueueAdapter) DeleteRemoteObject(ctx context.Context, remoteClient client.Client, key types.NamespacedName) error {
	pod := corev1.Pod{}
	err := remoteClient.Get(ctx, key, &pod)
	if err != nil {
		return client.IgnoreNotFound(err)
	}
	return client.IgnoreNotFound(remoteClient.Delete(ctx, &pod))
}

func (b *multikueueAdapter) KeepAdmissionCheckPending() bool {
	return true
}

func (b *multikueueAdapter) IsJobManagedByKueue(ctx context.Context, c client.Client, key types.NamespacedName) (bool, string, error) {
	return true, "", nil
}

func (b *multikueueAdapter) GVK() schema.GroupVersionKind {
	return gvk
}

var _ jobframework.MultiKueueWatcher = (*multikueueAdapter)(nil)

func (*multikueueAdapter) GetEmptyList() client.ObjectList {
	return &corev1.PodList{}
}

func (*multikueueAdapter) WorkloadKeyFor(o runtime.Object) (types.NamespacedName, error) {
	pod, isPod := o.(*corev1.Pod)
	if !isPod {
		return types.NamespacedName{}, errors.New("not a pod")
	}

	prebuiltWl, hasPrebuiltWorkload := pod.Labels[constants.PrebuiltWorkloadLabel]
	if !hasPrebuiltWorkload {
		return types.NamespacedName{}, fmt.Errorf("no prebuilt workload found for pod: %s", klog.KObj(pod))
	}

	return types.NamespacedName{Name: prebuiltWl, Namespace: pod.Namespace}, nil
}
