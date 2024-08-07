package common

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"

	"sigs.k8s.io/controller-runtime/pkg/client"
	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
)

type objAsPtr[T any] interface {
	metav1.Object
	client.Object
	*T
}

func SyncJob[PtrT objAsPtr[T], T any](
	ctx context.Context,
	localClient client.Client,
	remoteClient client.Client,
	key types.NamespacedName,
	workloadName, origin string,
	b jobframework.UpdateRemoteJob) error {

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
			b.UpdateRemoteJobStatus(localJob, remoteJob)
			return true, nil
		})
	}

	remoteJob = PtrT(new(T))
	b.UpdateRemoteJobSpec(localJob, remoteJob)

	// add the prebuilt workload
	labels := remoteJob.GetLabels()
	if remoteJob.GetLabels() == nil {
		labels = make(map[string]string, 2)
	}
	labels[constants.PrebuiltWorkloadLabel] = workloadName
	labels[kueuealpha.MultiKueueOriginLabel] = origin
	remoteJob.SetLabels(labels)

	return remoteClient.Create(ctx, remoteJob)
}

func DeleteRemoteObject[PtrT objAsPtr[T], T any](ctx context.Context, remoteClient client.Client, key types.NamespacedName) error {
	job := PtrT(new(T))
	err := remoteClient.Get(ctx, key, job)
	if err != nil {
		return client.IgnoreNotFound(err)
	}
	return client.IgnoreNotFound(remoteClient.Delete(ctx, job))
}

func WorkloadKeyFor[PtrT objAsPtr[T], T any](o runtime.Object, JobName string) (types.NamespacedName, error) {
	job, isTheJob := o.(PtrT)
	if !isTheJob {
		return types.NamespacedName{}, fmt.Errorf("not a %s", JobName)
	}

	prebuiltWl, hasPrebuiltWorkload := job.GetLabels()[constants.PrebuiltWorkloadLabel]
	if !hasPrebuiltWorkload {
		return types.NamespacedName{}, fmt.Errorf("no prebuilt workload found for %s: %s", JobName, klog.KObj(job))
	}

	return types.NamespacedName{Name: prebuiltWl, Namespace: job.GetNamespace()}, nil
}
