/*
Copyright 2022 The Kubernetes Authors.

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

package discovery

import (
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
)

// HasAPIResourceForGVK checks whether the API resource is available for the provided GVK.
func HasAPIResourceForGVK(dc discovery.DiscoveryInterface, gvk schema.GroupVersionKind) (bool, error) {
	gv, kind := gvk.ToAPIVersionAndKind()
	if resources, err := dc.ServerResourcesForGroupVersion(gv); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	} else {
		for _, res := range resources.APIResources {
			if res.Kind == kind {
				return true, nil
			}
		}
	}
	return false, nil
}
