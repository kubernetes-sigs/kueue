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
// Code generated by lister-gen. DO NOT EDIT.

package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/cache"
	v1alpha1 "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
)

// MultiKueueConfigLister helps list MultiKueueConfigs.
// All objects returned here must be treated as read-only.
type MultiKueueConfigLister interface {
	// List lists all MultiKueueConfigs in the indexer.
	// Objects returned here must be treated as read-only.
	List(selector labels.Selector) (ret []*v1alpha1.MultiKueueConfig, err error)
	// Get retrieves the MultiKueueConfig from the index for a given name.
	// Objects returned here must be treated as read-only.
	Get(name string) (*v1alpha1.MultiKueueConfig, error)
	MultiKueueConfigListerExpansion
}

// multiKueueConfigLister implements the MultiKueueConfigLister interface.
type multiKueueConfigLister struct {
	indexer cache.Indexer
}

// NewMultiKueueConfigLister returns a new MultiKueueConfigLister.
func NewMultiKueueConfigLister(indexer cache.Indexer) MultiKueueConfigLister {
	return &multiKueueConfigLister{indexer: indexer}
}

// List lists all MultiKueueConfigs in the indexer.
func (s *multiKueueConfigLister) List(selector labels.Selector) (ret []*v1alpha1.MultiKueueConfig, err error) {
	err = cache.ListAll(s.indexer, selector, func(m interface{}) {
		ret = append(ret, m.(*v1alpha1.MultiKueueConfig))
	})
	return ret, err
}

// Get retrieves the MultiKueueConfig from the index for a given name.
func (s *multiKueueConfigLister) Get(name string) (*v1alpha1.MultiKueueConfig, error) {
	obj, exists, err := s.indexer.GetByKey(name)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, errors.NewNotFound(v1alpha1.Resource("multikueueconfig"), name)
	}
	return obj.(*v1alpha1.MultiKueueConfig), nil
}
