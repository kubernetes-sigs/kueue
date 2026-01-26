//go:build tools
// +build tools

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

import (
	_ "github.com/gohugoio/hugo/common"
	_ "github.com/golangci/golangci-lint/v2/cmd/golangci-lint"
	_ "github.com/helm-unittest/helm-unittest/pkg/unittest"
	_ "github.com/kubernetes-sigs/reference-docs/genref"
	_ "github.com/mikefarah/yq/v4"
	_ "github.com/norwoodj/helm-docs/cmd/helm-docs"
	_ "github.com/onsi/ginkgo/v2/ginkgo"
	_ "go.uber.org/mock/mockgen"
	_ "gotest.tools/gotestsum"
	_ "helm.sh/helm/v4/cmd/helm"
	_ "k8s.io/code-generator"
	_ "sigs.k8s.io/cluster-inventory-api/cmd/secretreader-plugin"
	_ "sigs.k8s.io/controller-runtime/tools/setup-envtest"
	_ "sigs.k8s.io/controller-tools/cmd/controller-gen"
	_ "sigs.k8s.io/kind"
	_ "sigs.k8s.io/kustomize/kustomize/v5"
	_ "sigs.k8s.io/mdtoc"
)
