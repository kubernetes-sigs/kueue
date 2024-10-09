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

package job

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/test/integration/framework"
)

var (
	cfg           *rest.Config
	ctx           context.Context
	fwk           *framework.Framework
	crdPath       = filepath.Join("..", "..", "..", "..", "..", "config", "components", "crd", "bases")
	jobsetCrdPath = filepath.Join("..", "..", "..", "..", "..", "dep-crds", "jobset-operator")
)

func TestAPIs(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)

	ginkgo.RunSpecs(t,
		"Setup Controllers Suite",
	)
}

func managerSetup(opts ...jobframework.Option) framework.ManagerSetup {
	return func(ctx context.Context, mgr manager.Manager) {
		gomega.Expect(jobframework.SetupIndexes(ctx, mgr.GetFieldIndexer(), opts...)).NotTo(gomega.HaveOccurred())
		gomega.Expect(jobframework.SetupControllers(ctx, mgr, ginkgo.GinkgoLogr, opts...)).NotTo(gomega.HaveOccurred())
	}
}
