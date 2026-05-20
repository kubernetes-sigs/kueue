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

package ray

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
)

// Duplicated here to avoid a circular import with raycluster package.
const (
	rayclusterPodsetReplicaSizesAnnotation = "kueue.x-k8s.io/raycluster-podset-replica-sizes"
	rayclusterGenerationAnnotation         = "kueue.x-k8s.io/raycluster-generation"
)

var rayclusterAutoscalingAnnotations = []string{
	rayclusterGenerationAnnotation,
	rayclusterPodsetReplicaSizesAnnotation,
}

type objAsPtr[T any] interface {
	metav1.Object
	client.Object
	*T
}

type adapter[PtrT objAsPtr[T], T any] struct {
	copySpec     func(dst, src PtrT)
	copyStatus   func(dst, src PtrT)
	emptyList    func() client.ObjectList
	gvk          schema.GroupVersionKind
	getManagedBy func(PtrT) *string
	setManagedBy func(PtrT, *string)
}

type fullInterface interface {
	jobframework.MultiKueueAdapter
	jobframework.MultiKueueWatcher
}

// NewMKAdapter creates a generic MultiKueue adapter for Ray job types.
// It follows the same pattern as kubeflowjob.NewMKAdapter but adapted for
// Ray types (RayCluster, RayJob, RayService) which share an identical
// MultiKueue adapter structure.
func NewMKAdapter[PtrT objAsPtr[T], T any](
	copySpec func(dst, src PtrT),
	copyStatus func(dst, src PtrT),
	emptyList func() client.ObjectList,
	gvk schema.GroupVersionKind,
	getManagedBy func(PtrT) *string,
	setManagedBy func(PtrT, *string),
) fullInterface {
	return &adapter[PtrT, T]{
		copySpec:     copySpec,
		copyStatus:   copyStatus,
		emptyList:    emptyList,
		gvk:          gvk,
		getManagedBy: getManagedBy,
		setManagedBy: setManagedBy,
	}
}

func (a *adapter[PtrT, T]) GVK() schema.GroupVersionKind {
	return a.gvk
}

func (a *adapter[PtrT, T]) IsJobManagedByKueue(ctx context.Context, c client.Client, key types.NamespacedName) (bool, string, error) {
	job := PtrT(new(T))
	err := c.Get(ctx, key, job)
	if err != nil {
		return false, "", err
	}

	jobControllerName := ptr.Deref(a.getManagedBy(job), "")
	if jobControllerName != kueue.MultiKueueControllerName {
		return false, fmt.Sprintf("Expecting spec.managedBy to be %q not %q", kueue.MultiKueueControllerName, jobControllerName), nil
	}
	return true, "", nil
}

func (a *adapter[PtrT, T]) SyncJob(
	ctx context.Context,
	localClient client.Client,
	remoteClient client.Client,
	key types.NamespacedName,
	workloadName, origin string,
) error {
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

	// if the remote exists, copy the status and sync annotations
	if err == nil {
		if err := clientutil.PatchStatus(ctx, localClient, localJob, func() (bool, error) {
			a.copyStatus(localJob, remoteJob)
			return true, nil
		}); err != nil {
			return err
		}

		// Sync Ray autoscaling annotations from remote to local so the
		// management cluster can monitor autoscaling events on the worker.
		if err := syncAutoscalingAnnotations(ctx, localClient, localJob, remoteJob); err != nil {
			return err
		}

		// Update the prebuilt workload label on the remote job if the workload name
		// has changed (e.g., due to workload slice replacement on scale-up).
		// This allows the worker's reconciler to discover the new workload.
		if currentLabel := remoteJob.GetLabels()[constants.PrebuiltWorkloadLabel]; currentLabel != workloadName {
			return clientutil.Patch(ctx, remoteClient, remoteJob, func() (bool, error) {
				labels := remoteJob.GetLabels()
				if labels == nil {
					labels = make(map[string]string)
				}
				labels[constants.PrebuiltWorkloadLabel] = workloadName
				remoteJob.SetLabels(labels)
				return true, nil
			})
		}

		return nil
	}

	remoteJob = PtrT(new(T))
	a.copySpec(remoteJob, localJob)

	// add the prebuilt workload
	labels := remoteJob.GetLabels()
	if labels == nil {
		labels = make(map[string]string, 2)
	}
	labels[constants.PrebuiltWorkloadLabel] = workloadName
	labels[kueue.MultiKueueOriginLabel] = origin
	remoteJob.SetLabels(labels)

	// clearing the managedBy enables the controller to take over
	a.setManagedBy(remoteJob, nil)

	return remoteClient.Create(ctx, remoteJob)
}

func (a *adapter[PtrT, T]) DeleteRemoteObject(ctx context.Context, remoteClient client.Client, key types.NamespacedName) error {
	job := PtrT(new(T))
	job.SetName(key.Name)
	job.SetNamespace(key.Namespace)
	return client.IgnoreNotFound(remoteClient.Delete(ctx, job))
}

func (a *adapter[PtrT, T]) GetEmptyList() client.ObjectList {
	return a.emptyList()
}

func (a *adapter[PtrT, T]) WorkloadKeysFor(o runtime.Object) ([]types.NamespacedName, error) {
	job, isTheJob := o.(PtrT)
	if !isTheJob {
		return nil, fmt.Errorf("not a %s", a.gvk.Kind)
	}

	prebuiltWl, hasPrebuiltWorkload := job.GetLabels()[constants.PrebuiltWorkloadLabel]
	if !hasPrebuiltWorkload {
		return nil, fmt.Errorf("no prebuilt workload found for %s: %s", a.gvk.Kind, klog.KObj(job))
	}

	return []types.NamespacedName{{Name: prebuiltWl, Namespace: job.GetNamespace()}}, nil
}

// syncAutoscalingAnnotations syncs Ray autoscaling annotations from a remote
// job to a local job. This allows the management cluster to monitor autoscaling
// state changes that occur on the worker cluster.
func syncAutoscalingAnnotations(ctx context.Context, localClient client.Client, localJob, remoteJob metav1.Object) error {
	remoteAnnotations := remoteJob.GetAnnotations()
	localAnnotations := localJob.GetAnnotations()

	needsPatch := false
	for _, annotationKey := range rayclusterAutoscalingAnnotations {
		if remoteAnnotations[annotationKey] != localAnnotations[annotationKey] {
			needsPatch = true
			break
		}
	}
	if !needsPatch {
		return nil
	}

	return clientutil.Patch(ctx, localClient, localJob.(client.Object), func() (bool, error) {
		annotations := localJob.GetAnnotations()
		if annotations == nil {
			annotations = make(map[string]string)
		}
		for _, annotationKey := range rayclusterAutoscalingAnnotations {
			if remoteAnnotation := remoteAnnotations[annotationKey]; remoteAnnotation == "" {
				delete(annotations, annotationKey)
			} else {
				annotations[annotationKey] = remoteAnnotation
			}
		}
		localJob.SetAnnotations(annotations)
		return true, nil
	})
}
