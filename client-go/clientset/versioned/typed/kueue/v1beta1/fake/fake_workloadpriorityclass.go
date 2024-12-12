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
	gentype "k8s.io/client-go/gentype"
	v1beta1 "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	kueuev1beta1 "sigs.k8s.io/kueue/client-go/applyconfiguration/kueue/v1beta1"
	typedkueuev1beta1 "sigs.k8s.io/kueue/client-go/clientset/versioned/typed/kueue/v1beta1"
)

// fakeWorkloadPriorityClasses implements WorkloadPriorityClassInterface
type fakeWorkloadPriorityClasses struct {
	*gentype.FakeClientWithListAndApply[*v1beta1.WorkloadPriorityClass, *v1beta1.WorkloadPriorityClassList, *kueuev1beta1.WorkloadPriorityClassApplyConfiguration]
	Fake *FakeKueueV1beta1
}

func newFakeWorkloadPriorityClasses(fake *FakeKueueV1beta1) typedkueuev1beta1.WorkloadPriorityClassInterface {
	return &fakeWorkloadPriorityClasses{
		gentype.NewFakeClientWithListAndApply[*v1beta1.WorkloadPriorityClass, *v1beta1.WorkloadPriorityClassList, *kueuev1beta1.WorkloadPriorityClassApplyConfiguration](
			fake.Fake,
			"",
			v1beta1.SchemeGroupVersion.WithResource("workloadpriorityclasses"),
			v1beta1.SchemeGroupVersion.WithKind("WorkloadPriorityClass"),
			func() *v1beta1.WorkloadPriorityClass { return &v1beta1.WorkloadPriorityClass{} },
			func() *v1beta1.WorkloadPriorityClassList { return &v1beta1.WorkloadPriorityClassList{} },
			func(dst, src *v1beta1.WorkloadPriorityClassList) { dst.ListMeta = src.ListMeta },
			func(list *v1beta1.WorkloadPriorityClassList) []*v1beta1.WorkloadPriorityClass {
				return gentype.ToPointerSlice(list.Items)
			},
			func(list *v1beta1.WorkloadPriorityClassList, items []*v1beta1.WorkloadPriorityClass) {
				list.Items = gentype.FromPointerSlice(items)
			},
		),
		fake,
	}
}
