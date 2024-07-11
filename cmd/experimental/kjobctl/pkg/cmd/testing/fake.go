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
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/dynamic"
	k8s "k8s.io/client-go/kubernetes"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	kueueversioned "sigs.k8s.io/kueue/client-go/clientset/versioned"
	kjobctlversioned "sigs.k8s.io/kueue/cmd/experimental/kjobctl/client-go/clientset/versioned"
	kjobctlfake "sigs.k8s.io/kueue/cmd/experimental/kjobctl/client-go/clientset/versioned/fake"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd/util"
)

type TestClientGetter struct {
	util.ClientGetter

	k8sClientset     k8s.Interface
	kueueClientset   kueueversioned.Interface
	kjobctlClientset kjobctlversioned.Interface
	dynamicClient    dynamic.Interface

	restConfig *rest.Config

	configFlags *genericclioptions.TestConfigFlags
}

func NewTestClientGetter() *TestClientGetter {
	clientConfig := &clientcmd.DeferredLoadingClientConfig{}
	restConfig := &rest.Config{}

	configFlags := genericclioptions.NewTestConfigFlags().
		WithClientConfig(clientConfig).
		WithNamespace(metav1.NamespaceDefault)
	return &TestClientGetter{
		ClientGetter:     util.NewClientGetter(configFlags),
		kjobctlClientset: kjobctlfake.NewSimpleClientset(),
		k8sClientset:     k8sfake.NewSimpleClientset(),
		restConfig:       restConfig,
		configFlags:      configFlags,
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

func (cg *TestClientGetter) WithRESTConfig(config *rest.Config) *TestClientGetter {
	cg.restConfig = config
	return cg
}

func (cg *TestClientGetter) ToRESTConfig() (*rest.Config, error) {
	return cg.restConfig, nil
}

func (cg *TestClientGetter) WithK8sClientset(clientset k8s.Interface) *TestClientGetter {
	cg.k8sClientset = clientset
	return cg
}

func (cg *TestClientGetter) WithKueueClientset(clientset kueueversioned.Interface) *TestClientGetter {
	cg.kueueClientset = clientset
	return cg
}

func (cg *TestClientGetter) WithKjobctlClientset(clientset kjobctlversioned.Interface) *TestClientGetter {
	cg.kjobctlClientset = clientset
	return cg
}

func (cg *TestClientGetter) WithDynamicClient(dynamicClient dynamic.Interface) *TestClientGetter {
	cg.dynamicClient = dynamicClient
	return cg
}

func (cg *TestClientGetter) K8sClientset() (k8s.Interface, error) {
	return cg.k8sClientset, nil
}

func (cg *TestClientGetter) KueueClientset() (kueueversioned.Interface, error) {
	return cg.kueueClientset, nil
}

func (cg *TestClientGetter) KjobctlClientset() (kjobctlversioned.Interface, error) {
	return cg.kjobctlClientset, nil
}

func (cg *TestClientGetter) DynamicClient() (dynamic.Interface, error) {
	return cg.dynamicClient, nil
}
