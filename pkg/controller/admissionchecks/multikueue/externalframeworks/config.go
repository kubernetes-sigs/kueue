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

package externalframeworks

import (
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime/schema"
	k8serrors "k8s.io/apimachinery/pkg/util/errors"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
)

// NewAdapters creates and returns adapters from the given configurations.
func NewAdapters(configs []configapi.MultiKueueExternalFramework) ([]*Adapter, error) {
	configsMap := make(map[schema.GroupVersionKind]configapi.MultiKueueExternalFramework)
	var errs []error

	for _, config := range configs {
		gvk, err := parseGVK(config.Name)
		if err != nil {
			errs = append(errs, fmt.Errorf("invalid external framework configuration for %q: %w", config.Name, err))
			continue
		}

		if _, exists := configsMap[*gvk]; exists {
			errs = append(errs, fmt.Errorf("duplicate configuration for GVK %s", gvk))
			continue
		}

		configsMap[*gvk] = config
	}

	if len(errs) > 0 {
		return nil, k8serrors.NewAggregate(errs)
	}

	var adapters []*Adapter
	for gvk := range configsMap {
		adapters = append(adapters, &Adapter{gvk: gvk})
	}
	return adapters, nil
}

// parseGVK parses a string to a GVK
func parseGVK(name string) (*schema.GroupVersionKind, error) {
	if name == "" {
		return nil, errors.New("name is required")
	}

	gvk, _ := schema.ParseKindArg(name)
	if gvk == nil {
		return nil, fmt.Errorf("invalid GVK format '%s'", name)
	}
	return gvk, nil
}
