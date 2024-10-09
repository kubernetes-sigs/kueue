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
	"slices"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/jobset"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Setup Controllers", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	ginkgo.BeforeAll(func() {
		fwk = &framework.Framework{CRDPath: crdPath}
		cfg = fwk.Init()
		ctx, _ = fwk.SetupClient(cfg)
		fwk.StartManager(ctx, cfg, managerSetup(jobframework.WithEnabledFrameworks([]string{jobset.FrameworkName})))
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
		fwk.Teardown()
	})

	ginkgo.It("Should setup controller and webhook after CRD installation", framework.SlowSpec, func() {
		ginkgo.By("Check that integration is not enabled", func() {
			gomega.Expect(slices.Contains(jobframework.GetEnabledIntegrationsList(), jobset.FrameworkName)).To(gomega.BeFalse())
		})

		ginkgo.By("Install CRDs", func() {
			options := envtest.CRDInstallOptions{
				Paths:              []string{jobsetCrdPath},
				ErrorIfPathMissing: true,
				CleanUpAfterUse:    true,
			}
			_, err := envtest.InstallCRDs(cfg, options)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.By("Check that integration is enabled", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(slices.Contains(jobframework.GetEnabledIntegrationsList(), jobset.FrameworkName)).To(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
