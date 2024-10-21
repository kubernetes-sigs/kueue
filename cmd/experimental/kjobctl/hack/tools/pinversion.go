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

package tools

// Keep a reference to the code generators, so they are not removed by go mod tidy
import (
	_ "github.com/golangci/golangci-lint/pkg/exitcodes"
	_ "github.com/onsi/ginkgo/v2/ginkgo/command"
	_ "github.com/onsi/ginkgo/v2/ginkgo/run"
	_ "gotest.tools/gotestsum/cmd"
	_ "k8s.io/code-generator"
	_ "sigs.k8s.io/controller-runtime/tools/setup-envtest/env"

	// since verify will error when referencing a cmd package
	// we need to reference individual dependencies used by it
	_ "sigs.k8s.io/controller-tools/pkg/crd"
	_ "sigs.k8s.io/controller-tools/pkg/genall/help/pretty"
	_ "sigs.k8s.io/kind/pkg/cmd"
	_ "sigs.k8s.io/kustomize/kustomize/v5/commands/edit/listbuiltin"
)
