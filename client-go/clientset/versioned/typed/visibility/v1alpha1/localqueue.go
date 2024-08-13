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
// Code generated by client-gen. DO NOT EDIT.

package v1alpha1

import (
	"context"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	types "k8s.io/apimachinery/pkg/types"
	watch "k8s.io/apimachinery/pkg/watch"
	gentype "k8s.io/client-go/gentype"
	v1alpha1 "sigs.k8s.io/kueue/apis/visibility/v1alpha1"
	visibilityv1alpha1 "sigs.k8s.io/kueue/client-go/applyconfiguration/visibility/v1alpha1"
	scheme "sigs.k8s.io/kueue/client-go/clientset/versioned/scheme"
)

// LocalQueuesGetter has a method to return a LocalQueueInterface.
// A group's client should implement this interface.
type LocalQueuesGetter interface {
	LocalQueues(namespace string) LocalQueueInterface
}

// LocalQueueInterface has methods to work with LocalQueue resources.
type LocalQueueInterface interface {
	Create(ctx context.Context, localQueue *v1alpha1.LocalQueue, opts v1.CreateOptions) (*v1alpha1.LocalQueue, error)
	Update(ctx context.Context, localQueue *v1alpha1.LocalQueue, opts v1.UpdateOptions) (*v1alpha1.LocalQueue, error)
	Delete(ctx context.Context, name string, opts v1.DeleteOptions) error
	DeleteCollection(ctx context.Context, opts v1.DeleteOptions, listOpts v1.ListOptions) error
	Get(ctx context.Context, name string, opts v1.GetOptions) (*v1alpha1.LocalQueue, error)
	List(ctx context.Context, opts v1.ListOptions) (*v1alpha1.LocalQueueList, error)
	Watch(ctx context.Context, opts v1.ListOptions) (watch.Interface, error)
	Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts v1.PatchOptions, subresources ...string) (result *v1alpha1.LocalQueue, err error)
	Apply(ctx context.Context, localQueue *visibilityv1alpha1.LocalQueueApplyConfiguration, opts v1.ApplyOptions) (result *v1alpha1.LocalQueue, err error)
	GetPendingWorkloadsSummary(ctx context.Context, localQueueName string, options v1.GetOptions) (*v1alpha1.PendingWorkloadsSummary, error)

	LocalQueueExpansion
}

// localQueues implements LocalQueueInterface
type localQueues struct {
	*gentype.ClientWithListAndApply[*v1alpha1.LocalQueue, *v1alpha1.LocalQueueList, *visibilityv1alpha1.LocalQueueApplyConfiguration]
}

// newLocalQueues returns a LocalQueues
func newLocalQueues(c *VisibilityV1alpha1Client, namespace string) *localQueues {
	return &localQueues{
		gentype.NewClientWithListAndApply[*v1alpha1.LocalQueue, *v1alpha1.LocalQueueList, *visibilityv1alpha1.LocalQueueApplyConfiguration](
			"localqueues",
			c.RESTClient(),
			scheme.ParameterCodec,
			namespace,
			func() *v1alpha1.LocalQueue { return &v1alpha1.LocalQueue{} },
			func() *v1alpha1.LocalQueueList { return &v1alpha1.LocalQueueList{} }),
	}
}

// GetPendingWorkloadsSummary takes name of the localQueue, and returns the corresponding v1alpha1.PendingWorkloadsSummary object, and an error if there is any.
func (c *localQueues) GetPendingWorkloadsSummary(ctx context.Context, localQueueName string, options v1.GetOptions) (result *v1alpha1.PendingWorkloadsSummary, err error) {
	result = &v1alpha1.PendingWorkloadsSummary{}
	err = c.GetClient().Get().
		Namespace(c.GetNamespace()).
		Resource("localqueues").
		Name(localQueueName).
		SubResource("pendingworkloads").
		VersionedParams(&options, scheme.ParameterCodec).
		Do(ctx).
		Into(result)
	return
}
