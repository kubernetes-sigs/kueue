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

package kueuectl

import (
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/client-go/rest"
)

func CreateConfigFlagsWithRestConfig(cfg *rest.Config, streams genericiooptions.IOStreams) *genericclioptions.ConfigFlags {
	return genericclioptions.
		NewConfigFlags(true).
		WithDiscoveryQPS(50.0).
		WithWarningPrinter(streams).
		WithWrapConfigFn(func(*rest.Config) *rest.Config {
			return cfg
		})
}
