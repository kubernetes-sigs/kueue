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
	rest "k8s.io/client-go/rest"
	testing "k8s.io/client-go/testing"
	v1beta1 "sigs.k8s.io/kueue/client-go/clientset/versioned/typed/kueue/v1beta1"
)

type FakeKueueV1beta1 struct {
	*testing.Fake
}

func (c *FakeKueueV1beta1) AdmissionChecks() v1beta1.AdmissionCheckInterface {
	return &FakeAdmissionChecks{c}
}

func (c *FakeKueueV1beta1) ClusterQueues() v1beta1.ClusterQueueInterface {
	return &FakeClusterQueues{c}
}

func (c *FakeKueueV1beta1) LocalQueues(namespace string) v1beta1.LocalQueueInterface {
	return &FakeLocalQueues{c, namespace}
}

func (c *FakeKueueV1beta1) MultiKueueConfigs() v1beta1.MultiKueueConfigInterface {
	return &FakeMultiKueueConfigs{c}
}

func (c *FakeKueueV1beta1) ProvisioningRequestConfigs() v1beta1.ProvisioningRequestConfigInterface {
	return &FakeProvisioningRequestConfigs{c}
}

func (c *FakeKueueV1beta1) ResourceFlavors() v1beta1.ResourceFlavorInterface {
	return &FakeResourceFlavors{c}
}

func (c *FakeKueueV1beta1) Workloads(namespace string) v1beta1.WorkloadInterface {
	return &FakeWorkloads{c, namespace}
}

func (c *FakeKueueV1beta1) WorkloadPriorityClasses() v1beta1.WorkloadPriorityClassInterface {
	return &FakeWorkloadPriorityClasses{c}
}

// RESTClient returns a RESTClient that is used to communicate
// with API server by this client implementation.
func (c *FakeKueueV1beta1) RESTClient() rest.Interface {
	var ret *rest.RESTClient
	return ret
}
