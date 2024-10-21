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

package e2e

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/test/util"
)

var (
	kjobctlPath string
	k8sClient   client.Client
	ctx         context.Context
)

// Run e2e tests using the Ginkgo runner.
func TestE2E(t *testing.T) {
	suiteName := "End To End Suite"
	gomega.RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t, suiteName)
}

var _ = ginkgo.BeforeSuite(func() {
	dir, _ := util.GetProjectDir()
	kjobctlPath = filepath.Join(dir, "bin", "kubectl-kjob")
	k8sClient, _ = util.CreateClientUsingCluster("")
	ctx = context.Background()
})
