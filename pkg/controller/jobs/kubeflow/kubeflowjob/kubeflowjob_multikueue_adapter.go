/*
Copyright 2024 The Kubernetes Authors.

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

package kubeflowjob

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
)

type objAsPtr[T any] interface {
	metav1.Object
	client.Object
	*T
}

type adapter[PtrT objAsPtr[T], T any] struct {
	copySpec   func(dst, src PtrT)
	copyStatus func(dst, src PtrT)
	emptyList  func() client.ObjectList
	gvk        schema.GroupVersionKind
}

type fullInterface interface {
	jobframework.MultiKueueAdapter
	jobframework.MultiKueueWatcher
}

func NewMKAdapter[PtrT objAsPtr[T], T any](
	copySpec func(dst, src PtrT),
	copyStatus func(dst, src PtrT),
	emptyList func() client.ObjectList,
	gvk schema.GroupVersionKind,
) fullInterface {
	return &adapter[PtrT, T]{
		copySpec:   copySpec,
		copyStatus: copyStatus,
		emptyList:  emptyList,
		gvk:        gvk,
	}
}

func (a adapter[PtrT, T]) GVK() schema.GroupVersionKind {
	return a.gvk
}

func (a adapter[PtrT, T]) KeepAdmissionCheckPending() bool {
	return false
}

func (a adapter[PtrT, T]) IsJobManagedByKueue(context.Context, client.Client, types.NamespacedName) (bool, string, error) {
	return true, "", nil
}

func (a adapter[PtrT, T]) SyncJob(
	ctx context.Context,
	localClient client.Client,
	remoteClient client.Client,
	key types.NamespacedName,
	workloadName, origin string) error {
	localJob := PtrT(new(T))
	err := localClient.Get(ctx, key, localJob)
	if err != nil {
		return err
	}

	remoteJob := PtrT(new(T))
	err = remoteClient.Get(ctx, key, remoteJob)
	if client.IgnoreNotFound(err) != nil {
		return err
	}

	if err == nil {
		return clientutil.PatchStatus(ctx, localClient, localJob, func() (bool, error) {
			// if the remote exists, just copy the status
			a.copyStatus(localJob, remoteJob)
			return true, nil
		})
	}

	remoteJob = PtrT(new(T))
	a.copySpec(remoteJob, localJob)

	// add the prebuilt workload
	labels := remoteJob.GetLabels()
	if remoteJob.GetLabels() == nil {
		labels = make(map[string]string, 2)
	}
	labels[constants.PrebuiltWorkloadLabel] = workloadName
	labels[kueue.MultiKueueOriginLabel] = origin
	remoteJob.SetLabels(labels)

	return remoteClient.Create(ctx, remoteJob)
}

func (a adapter[PtrT, T]) DeleteRemoteObject(ctx context.Context, remoteClient client.Client, key types.NamespacedName) error {
	job := PtrT(new(T))
	job.SetName(key.Name)
	job.SetNamespace(key.Namespace)
	return client.IgnoreNotFound(remoteClient.Delete(ctx, job))
}

func (a adapter[PtrT, T]) GetEmptyList() client.ObjectList {
	return a.emptyList()
}

func (a adapter[PtrT, T]) WorkloadKeyFor(o runtime.Object) (types.NamespacedName, error) {
	job, isTheJob := o.(PtrT)
	if !isTheJob {
		return types.NamespacedName{}, fmt.Errorf("not a %s", a.gvk.Kind)
	}

	prebuiltWl, hasPrebuiltWorkload := job.GetLabels()[constants.PrebuiltWorkloadLabel]
	if !hasPrebuiltWorkload {
		return types.NamespacedName{}, fmt.Errorf("no prebuilt workload found for %s: %s", a.gvk.Kind, klog.KObj(job))
	}

	return types.NamespacedName{Name: prebuiltWl, Namespace: job.GetNamespace()}, nil
}
