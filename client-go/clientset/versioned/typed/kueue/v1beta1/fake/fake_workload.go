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

package fake

import (
	"context"
	json "encoding/json"
	"fmt"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	labels "k8s.io/apimachinery/pkg/labels"
	types "k8s.io/apimachinery/pkg/types"
	watch "k8s.io/apimachinery/pkg/watch"
	testing "k8s.io/client-go/testing"
	v1beta1 "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	kueuev1beta1 "sigs.k8s.io/kueue/client-go/applyconfiguration/kueue/v1beta1"
)

// FakeWorkloads implements WorkloadInterface
type FakeWorkloads struct {
	Fake *FakeKueueV1beta1
	ns   string
}

var workloadsResource = v1beta1.SchemeGroupVersion.WithResource("workloads")

var workloadsKind = v1beta1.SchemeGroupVersion.WithKind("Workload")

// Get takes name of the workload, and returns the corresponding workload object, and an error if there is any.
func (c *FakeWorkloads) Get(ctx context.Context, name string, options v1.GetOptions) (result *v1beta1.Workload, err error) {
	emptyResult := &v1beta1.Workload{}
	obj, err := c.Fake.
		Invokes(testing.NewGetActionWithOptions(workloadsResource, c.ns, name, options), emptyResult)

	if obj == nil {
		return emptyResult, err
	}
	return obj.(*v1beta1.Workload), err
}

// List takes label and field selectors, and returns the list of Workloads that match those selectors.
func (c *FakeWorkloads) List(ctx context.Context, opts v1.ListOptions) (result *v1beta1.WorkloadList, err error) {
	emptyResult := &v1beta1.WorkloadList{}
	obj, err := c.Fake.
		Invokes(testing.NewListActionWithOptions(workloadsResource, workloadsKind, c.ns, opts), emptyResult)

	if obj == nil {
		return emptyResult, err
	}

	label, _, _ := testing.ExtractFromListOptions(opts)
	if label == nil {
		label = labels.Everything()
	}
	list := &v1beta1.WorkloadList{ListMeta: obj.(*v1beta1.WorkloadList).ListMeta}
	for _, item := range obj.(*v1beta1.WorkloadList).Items {
		if label.Matches(labels.Set(item.Labels)) {
			list.Items = append(list.Items, item)
		}
	}
	return list, err
}

// Watch returns a watch.Interface that watches the requested workloads.
func (c *FakeWorkloads) Watch(ctx context.Context, opts v1.ListOptions) (watch.Interface, error) {
	return c.Fake.
		InvokesWatch(testing.NewWatchActionWithOptions(workloadsResource, c.ns, opts))

}

// Create takes the representation of a workload and creates it.  Returns the server's representation of the workload, and an error, if there is any.
func (c *FakeWorkloads) Create(ctx context.Context, workload *v1beta1.Workload, opts v1.CreateOptions) (result *v1beta1.Workload, err error) {
	emptyResult := &v1beta1.Workload{}
	obj, err := c.Fake.
		Invokes(testing.NewCreateActionWithOptions(workloadsResource, c.ns, workload, opts), emptyResult)

	if obj == nil {
		return emptyResult, err
	}
	return obj.(*v1beta1.Workload), err
}

// Update takes the representation of a workload and updates it. Returns the server's representation of the workload, and an error, if there is any.
func (c *FakeWorkloads) Update(ctx context.Context, workload *v1beta1.Workload, opts v1.UpdateOptions) (result *v1beta1.Workload, err error) {
	emptyResult := &v1beta1.Workload{}
	obj, err := c.Fake.
		Invokes(testing.NewUpdateActionWithOptions(workloadsResource, c.ns, workload, opts), emptyResult)

	if obj == nil {
		return emptyResult, err
	}
	return obj.(*v1beta1.Workload), err
}

// UpdateStatus was generated because the type contains a Status member.
// Add a +genclient:noStatus comment above the type to avoid generating UpdateStatus().
func (c *FakeWorkloads) UpdateStatus(ctx context.Context, workload *v1beta1.Workload, opts v1.UpdateOptions) (result *v1beta1.Workload, err error) {
	emptyResult := &v1beta1.Workload{}
	obj, err := c.Fake.
		Invokes(testing.NewUpdateSubresourceActionWithOptions(workloadsResource, "status", c.ns, workload, opts), emptyResult)

	if obj == nil {
		return emptyResult, err
	}
	return obj.(*v1beta1.Workload), err
}

// Delete takes name of the workload and deletes it. Returns an error if one occurs.
func (c *FakeWorkloads) Delete(ctx context.Context, name string, opts v1.DeleteOptions) error {
	_, err := c.Fake.
		Invokes(testing.NewDeleteActionWithOptions(workloadsResource, c.ns, name, opts), &v1beta1.Workload{})

	return err
}

// DeleteCollection deletes a collection of objects.
func (c *FakeWorkloads) DeleteCollection(ctx context.Context, opts v1.DeleteOptions, listOpts v1.ListOptions) error {
	action := testing.NewDeleteCollectionActionWithOptions(workloadsResource, c.ns, opts, listOpts)

	_, err := c.Fake.Invokes(action, &v1beta1.WorkloadList{})
	return err
}

// Patch applies the patch and returns the patched workload.
func (c *FakeWorkloads) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts v1.PatchOptions, subresources ...string) (result *v1beta1.Workload, err error) {
	emptyResult := &v1beta1.Workload{}
	obj, err := c.Fake.
		Invokes(testing.NewPatchSubresourceActionWithOptions(workloadsResource, c.ns, name, pt, data, opts, subresources...), emptyResult)

	if obj == nil {
		return emptyResult, err
	}
	return obj.(*v1beta1.Workload), err
}

// Apply takes the given apply declarative configuration, applies it and returns the applied workload.
func (c *FakeWorkloads) Apply(ctx context.Context, workload *kueuev1beta1.WorkloadApplyConfiguration, opts v1.ApplyOptions) (result *v1beta1.Workload, err error) {
	if workload == nil {
		return nil, fmt.Errorf("workload provided to Apply must not be nil")
	}
	data, err := json.Marshal(workload)
	if err != nil {
		return nil, err
	}
	name := workload.Name
	if name == nil {
		return nil, fmt.Errorf("workload.Name must be provided to Apply")
	}
	emptyResult := &v1beta1.Workload{}
	obj, err := c.Fake.
		Invokes(testing.NewPatchSubresourceActionWithOptions(workloadsResource, c.ns, *name, types.ApplyPatchType, data, opts.ToPatchOptions()), emptyResult)

	if obj == nil {
		return emptyResult, err
	}
	return obj.(*v1beta1.Workload), err
}

// ApplyStatus was generated because the type contains a Status member.
// Add a +genclient:noStatus comment above the type to avoid generating ApplyStatus().
func (c *FakeWorkloads) ApplyStatus(ctx context.Context, workload *kueuev1beta1.WorkloadApplyConfiguration, opts v1.ApplyOptions) (result *v1beta1.Workload, err error) {
	if workload == nil {
		return nil, fmt.Errorf("workload provided to Apply must not be nil")
	}
	data, err := json.Marshal(workload)
	if err != nil {
		return nil, err
	}
	name := workload.Name
	if name == nil {
		return nil, fmt.Errorf("workload.Name must be provided to Apply")
	}
	emptyResult := &v1beta1.Workload{}
	obj, err := c.Fake.
		Invokes(testing.NewPatchSubresourceActionWithOptions(workloadsResource, c.ns, *name, types.ApplyPatchType, data, opts.ToPatchOptions(), "status"), emptyResult)

	if obj == nil {
		return emptyResult, err
	}
	return obj.(*v1beta1.Workload), err
}
