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

package util

import (
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/resource"
	"k8s.io/client-go/dynamic"
	k8s "k8s.io/client-go/kubernetes"

	"sigs.k8s.io/kueue/client-go/clientset/versioned"

	_ "k8s.io/client-go/plugin/pkg/client/auth"
)

type ClientGetter interface {
	genericclioptions.RESTClientGetter

	KueueClientSet() (versioned.Interface, error)
	K8sClientSet() (k8s.Interface, error)
	DynamicClient() (dynamic.Interface, error)
	NewResourceBuilder() *resource.Builder
}

type clientGetterImpl struct {
	genericclioptions.RESTClientGetter
}

var _ ClientGetter = (*clientGetterImpl)(nil)

func NewClientGetter(clientGetter genericclioptions.RESTClientGetter) ClientGetter {
	return &clientGetterImpl{
		RESTClientGetter: clientGetter,
	}
}

func (cg *clientGetterImpl) KueueClientSet() (versioned.Interface, error) {
	config, err := cg.ToRESTConfig()
	if err != nil {
		return nil, err
	}

	clientset, err := versioned.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	return clientset, nil
}

func (cg *clientGetterImpl) K8sClientSet() (k8s.Interface, error) {
	config, err := cg.ToRESTConfig()
	if err != nil {
		return nil, err
	}

	config.ContentType = runtime.ContentTypeProtobuf
	clientset, err := k8s.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	return clientset, nil
}

func (cg *clientGetterImpl) DynamicClient() (dynamic.Interface, error) {
	config, err := cg.ToRESTConfig()
	if err != nil {
		return nil, err
	}

	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	return dynamicClient, nil
}

func (cg *clientGetterImpl) NewResourceBuilder() *resource.Builder {
	return resource.NewBuilder(cg.RESTClientGetter)
}
