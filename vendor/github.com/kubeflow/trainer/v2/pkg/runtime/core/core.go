/*
Copyright 2024 The Kubeflow Authors.

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

package core

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "github.com/kubeflow/trainer/v2/pkg/apis/config/v1alpha1"
	"github.com/kubeflow/trainer/v2/pkg/runtime"
)

// +kubebuilder:rbac:groups=trainer.kubeflow.org,resources=trainingruntimes,verbs=get;list;watch
// +kubebuilder:rbac:groups=trainer.kubeflow.org,resources=clustertrainingruntimes,verbs=get;list;watch

var runtimes map[string]runtime.Runtime

func New(ctx context.Context, client client.Client, indexer client.FieldIndexer, cfg *configapi.Configuration) (map[string]runtime.Runtime, error) {
	registry := NewRuntimeRegistry()
	newRuntimes := make(map[string]runtime.Runtime, len(registry))
	for name, registrar := range registry {
		for _, dep := range registrar.dependencies {
			depRegistrar, depExist := registry[dep]
			_, depRegistered := newRuntimes[dep]
			if depExist && !depRegistered {
				r, err := depRegistrar.factory(ctx, client, indexer, cfg)
				if err != nil {
					return nil, fmt.Errorf("initializing runtime %q on which %q depends: %w", dep, name, err)
				}
				newRuntimes[dep] = r
			}
		}
		if _, ok := newRuntimes[name]; !ok {
			r, err := registrar.factory(ctx, client, indexer, cfg)
			if err != nil {
				return nil, fmt.Errorf("initializing runtime %q: %w", name, err)
			}
			newRuntimes[name] = r
		}
	}
	runtimes = newRuntimes
	return newRuntimes, nil
}

func Runtimes() map[string]runtime.Runtime {
	runtimesCopy := make(map[string]runtime.Runtime, len(runtimes))
	for d, r := range runtimes {
		runtimesCopy[d] = r
	}
	return runtimesCopy
}
