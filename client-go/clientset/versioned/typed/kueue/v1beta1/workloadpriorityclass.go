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

package v1beta1

import (
	context "context"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	types "k8s.io/apimachinery/pkg/types"
	watch "k8s.io/apimachinery/pkg/watch"
	gentype "k8s.io/client-go/gentype"
	kueuev1beta1 "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	applyconfigurationkueuev1beta1 "sigs.k8s.io/kueue/client-go/applyconfiguration/kueue/v1beta1"
	scheme "sigs.k8s.io/kueue/client-go/clientset/versioned/scheme"
)

// WorkloadPriorityClassesGetter has a method to return a WorkloadPriorityClassInterface.
// A group's client should implement this interface.
type WorkloadPriorityClassesGetter interface {
	WorkloadPriorityClasses() WorkloadPriorityClassInterface
}

// WorkloadPriorityClassInterface has methods to work with WorkloadPriorityClass resources.
type WorkloadPriorityClassInterface interface {
	Create(ctx context.Context, workloadPriorityClass *kueuev1beta1.WorkloadPriorityClass, opts v1.CreateOptions) (*kueuev1beta1.WorkloadPriorityClass, error)
	Update(ctx context.Context, workloadPriorityClass *kueuev1beta1.WorkloadPriorityClass, opts v1.UpdateOptions) (*kueuev1beta1.WorkloadPriorityClass, error)
	Delete(ctx context.Context, name string, opts v1.DeleteOptions) error
	DeleteCollection(ctx context.Context, opts v1.DeleteOptions, listOpts v1.ListOptions) error
	Get(ctx context.Context, name string, opts v1.GetOptions) (*kueuev1beta1.WorkloadPriorityClass, error)
	List(ctx context.Context, opts v1.ListOptions) (*kueuev1beta1.WorkloadPriorityClassList, error)
	Watch(ctx context.Context, opts v1.ListOptions) (watch.Interface, error)
	Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts v1.PatchOptions, subresources ...string) (result *kueuev1beta1.WorkloadPriorityClass, err error)
	Apply(ctx context.Context, workloadPriorityClass *applyconfigurationkueuev1beta1.WorkloadPriorityClassApplyConfiguration, opts v1.ApplyOptions) (result *kueuev1beta1.WorkloadPriorityClass, err error)
	WorkloadPriorityClassExpansion
}

// workloadPriorityClasses implements WorkloadPriorityClassInterface
type workloadPriorityClasses struct {
	*gentype.ClientWithListAndApply[*kueuev1beta1.WorkloadPriorityClass, *kueuev1beta1.WorkloadPriorityClassList, *applyconfigurationkueuev1beta1.WorkloadPriorityClassApplyConfiguration]
}

// newWorkloadPriorityClasses returns a WorkloadPriorityClasses
func newWorkloadPriorityClasses(c *KueueV1beta1Client) *workloadPriorityClasses {
	return &workloadPriorityClasses{
		gentype.NewClientWithListAndApply[*kueuev1beta1.WorkloadPriorityClass, *kueuev1beta1.WorkloadPriorityClassList, *applyconfigurationkueuev1beta1.WorkloadPriorityClassApplyConfiguration](
			"workloadpriorityclasses",
			c.RESTClient(),
			scheme.ParameterCodec,
			"",
			func() *kueuev1beta1.WorkloadPriorityClass { return &kueuev1beta1.WorkloadPriorityClass{} },
			func() *kueuev1beta1.WorkloadPriorityClassList { return &kueuev1beta1.WorkloadPriorityClassList{} },
		),
	}
}
