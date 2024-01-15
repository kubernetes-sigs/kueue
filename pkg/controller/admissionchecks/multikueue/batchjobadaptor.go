/*
Copyright 2023 The Kubernetes Authors.

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
package multikueue

import (
	"context"

	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/pkg/controller/constants"
)

type batchJobAdaptor struct{}

var _ jobAdaptor = (*batchJobAdaptor)(nil)

var (
	// The list of labels that need to be removed before creating the job in the worker cluster.
	cleanupLabels = []string{"job-name", "controller-uid", "batch.kubernetes.io/job-name",
		"batch.kubernetes.io/controller-uid"}
)

func (b *batchJobAdaptor) CreateRemoteObject(ctx context.Context, localClient client.Client, remoteClient client.Client, key types.NamespacedName, workloadName string) error {
	localJob := batchv1.Job{}
	err := localClient.Get(ctx, key, &localJob)
	if err != nil {
		return err
	}

	remoteJob := batchv1.Job{
		ObjectMeta: cleanObjectMeta(&localJob.ObjectMeta),
		Spec:       *localJob.Spec.DeepCopy(),
	}

	// cleanup
	// drop the selector
	remoteJob.Spec.Selector = nil
	// drop the templates cleanup labels
	for _, cl := range cleanupLabels {
		delete(remoteJob.Spec.Template.Labels, cl)
	}

	// add the prebuilt workload
	if remoteJob.Labels == nil {
		remoteJob.Labels = map[string]string{}
	}
	remoteJob.Labels[constants.PrebuiltWorkloadLabel] = workloadName

	return remoteClient.Create(ctx, &remoteJob)
}

func (b *batchJobAdaptor) CopyStatusRemoteObject(ctx context.Context, localClient client.Client, remoteClient client.Client, key types.NamespacedName) error {
	localJob := batchv1.Job{}
	err := localClient.Get(ctx, key, &localJob)
	if err != nil {
		return client.IgnoreNotFound(err)
	}

	remoteJob := batchv1.Job{}
	err = remoteClient.Get(ctx, key, &remoteJob)
	if err != nil {
		return err
	}
	localJob.Status = remoteJob.Status
	return localClient.Status().Update(ctx, &localJob)
}

func (b *batchJobAdaptor) DeleteRemoteObject(ctx context.Context, remoteClient client.Client, key types.NamespacedName) error {
	job := batchv1.Job{}
	err := remoteClient.Get(ctx, key, &job)
	if err != nil {
		return client.IgnoreNotFound(err)
	}
	return client.IgnoreNotFound(remoteClient.Delete(ctx, &job))
}
