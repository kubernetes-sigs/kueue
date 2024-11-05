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

package testing

import (
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/resource"
	"k8s.io/client-go/dynamic"
	k8s "k8s.io/client-go/kubernetes"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"

	"sigs.k8s.io/kueue/client-go/clientset/versioned"
	kueuefake "sigs.k8s.io/kueue/client-go/clientset/versioned/fake"
	"sigs.k8s.io/kueue/cmd/kueuectl/app/util"
)

type TestClientGetter struct {
	util.ClientGetter

	kueueClientset versioned.Interface
	k8sClientset   k8s.Interface
	restClient     resource.RESTClient
	dynamicClient  dynamic.Interface

	configFlags *genericclioptions.TestConfigFlags
}

var _ util.ClientGetter = (*TestClientGetter)(nil)

func NewTestClientGetter() *TestClientGetter {
	clientConfig := &clientcmd.DeferredLoadingClientConfig{}
	configFlags := genericclioptions.NewTestConfigFlags().
		WithClientConfig(clientConfig).
		WithNamespace(metav1.NamespaceDefault)
	return &TestClientGetter{
		ClientGetter:   util.NewClientGetter(configFlags),
		kueueClientset: kueuefake.NewSimpleClientset(),
		k8sClientset:   k8sfake.NewSimpleClientset(),
		configFlags:    configFlags,
	}
}

func (cg *TestClientGetter) WithNamespace(ns string) *TestClientGetter {
	cg.configFlags.WithNamespace(ns)
	return cg
}

func (cg *TestClientGetter) WithRESTMapper(mapper meta.RESTMapper) *TestClientGetter {
	cg.configFlags.WithRESTMapper(mapper)
	return cg
}

func (cg *TestClientGetter) WithKueueClientset(clientset versioned.Interface) *TestClientGetter {
	cg.kueueClientset = clientset
	return cg
}

func (cg *TestClientGetter) KueueClientSet() (versioned.Interface, error) {
	return cg.kueueClientset, nil
}

func (cg *TestClientGetter) WithK8sClientset(clientset k8s.Interface) *TestClientGetter {
	cg.k8sClientset = clientset
	return cg
}

func (cg *TestClientGetter) K8sClientSet() (k8s.Interface, error) {
	return cg.k8sClientset, nil
}

func (cg *TestClientGetter) WithRESTClient(restClient resource.RESTClient) *TestClientGetter {
	cg.restClient = restClient
	return cg
}

func (cg *TestClientGetter) WithDynamicClient(dynamicClient dynamic.Interface) *TestClientGetter {
	cg.dynamicClient = dynamicClient
	return cg
}

func (cg *TestClientGetter) DynamicClient() (dynamic.Interface, error) {
	return cg.dynamicClient, nil
}

func (cg *TestClientGetter) NewResourceBuilder() *resource.Builder {
	return resource.NewFakeBuilder(
		func(version schema.GroupVersion) (resource.RESTClient, error) {
			return cg.restClient, nil
		},
		cg.ToRESTMapper,
		func() (restmapper.CategoryExpander, error) {
			return resource.FakeCategoryExpander, nil
		},
	)
}
