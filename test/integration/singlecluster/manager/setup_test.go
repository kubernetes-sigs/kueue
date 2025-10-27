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

package manager

import (
	"context"
	"os"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"

	"sigs.k8s.io/kueue/pkg/manager"
)

var _ = ginkgo.Describe("SetupManager", func() {
	var (
		testCtx    context.Context
		cancel     context.CancelFunc
		configFile *os.File
	)

	ginkgo.BeforeEach(func() {
		testCtx, cancel = context.WithTimeout(ctx, 30*time.Second)

		prev := manager.SetGetConfigForTest((func() *rest.Config { return cfg }))
		ginkgo.DeferCleanup(func() { manager.SetGetConfigForTest(prev) })

		var err error
		configFile, err = os.CreateTemp(ginkgo.GinkgoT().TempDir(), "kueue-config-*.yaml")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		configContent := `apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
leaderElection:
  leaderElect: false
health:
  healthProbeBindAddress: :0
metrics:
  bindAddress: :0
`
		_, err = configFile.WriteString(configContent)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		err = configFile.Close()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	})

	ginkgo.AfterEach(func() {
		if cancel != nil {
			cancel()
		}
		if configFile != nil {
			_ = os.Remove(configFile.Name())
		}
	})

	ginkgo.It("successfully initializes all components", func() {
		ms, err := manager.SetupManager(testCtx, configFile.Name(), "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		gomega.Expect(ms).NotTo(gomega.BeNil())
		gomega.Expect(ms.Mgr).NotTo(gomega.BeNil(), "manager should be initialized")
		gomega.Expect(ms.CCache).NotTo(gomega.BeNil(), "scheduler cache should be initialized")
		gomega.Expect(ms.Queues).NotTo(gomega.BeNil(), "queue manager should be initialized")
		gomega.Expect(ms.CertsReady).NotTo(gomega.BeNil(), "certsReady channel should be initialized")
		gomega.Expect(ms.ServerVersionFetcher).NotTo(gomega.BeNil(), "server version fetcher should be initialized")
		gomega.Expect(ms.Config).NotTo(gomega.BeNil(), "config should be initialized")

		opts := ms.Config.Options
		gomega.Expect(opts.Scheme).NotTo(gomega.BeNil(), "scheme should be configured")
		gomega.Expect(opts.HealthProbeBindAddress).NotTo(gomega.BeEmpty(), "health probe bind address should be set")
		gomega.Expect(opts.Metrics.BindAddress).NotTo(gomega.BeEmpty(), "metrics bind address should be set")
		gomega.Expect(opts.LeaderElection).To(gomega.BeFalse(), "leader election should be disabled")

		gomega.Expect(ms.Config.Apiconf.LeaderElection).NotTo(gomega.BeNil(), "leader election config should be loaded")
		gomega.Expect(ms.Config.Apiconf.LeaderElection.LeaderElect).NotTo(gomega.BeNil(), "leader elect setting should be loaded")
		gomega.Expect(*ms.Config.Apiconf.LeaderElection.LeaderElect).To(gomega.BeFalse(), "leader elect should be false")

		gomega.Expect(ms.Config.Apiconf.Namespace).NotTo(gomega.BeNil(), "namespace should be set")
		gomega.Expect(*ms.Config.Apiconf.Namespace).NotTo(gomega.BeEmpty(), "namespace should not be empty")

		version := ms.ServerVersionFetcher.GetServerVersion()
		gomega.Expect(version.String()).NotTo(gomega.BeEmpty(), "server version should be fetched")

		ginkgo.By("SetupManager successfully initialized all components")
	})

	ginkgo.It("returns error for invalid config", func() {
		_, err := manager.SetupManager(testCtx, "/nonexistent/config.yaml", "")
		gomega.Expect(err).To(gomega.HaveOccurred(), "should return error for non-existent config file")
	})
})

var _ = ginkgo.Describe("SetupIndexes", func() {
	var (
		cancel     context.CancelFunc
		configFile *os.File
		mgr        ctrl.Manager
	)

	ginkgo.BeforeEach(func() {
		_, cancel = context.WithTimeout(ctx, 30*time.Second)

		prev := manager.SetGetConfigForTest(func() *rest.Config { return cfg })
		ginkgo.DeferCleanup(func() { manager.SetGetConfigForTest(prev) })

		var err error
		configFile, err = os.CreateTemp(ginkgo.GinkgoT().TempDir(), "kueue-config-*.yaml")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		configContent := `apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
leaderElection:
  leaderElect: false
health:
  healthProbeBindAddress: :0
metrics:
  bindAddress: :0
`
		_, err = configFile.WriteString(configContent)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		err = configFile.Close()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		managerConfig := manager.NewConfig()
		err = managerConfig.Apply(configFile.Name())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		managerConfig.Options.LeaderElection = false
		managerConfig.Options.HealthProbeBindAddress = ":0"
		mgr, err = ctrl.NewManager(cfg, managerConfig.Options)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	})

	ginkgo.AfterEach(func() {
		if cancel != nil {
			cancel()
		}
		if configFile != nil {
			_ = os.Remove(configFile.Name())
		}
	})

	ginkgo.It("successfully sets up field indexes", func() {
		managerConfig := manager.NewConfig()
		err := managerConfig.Apply(configFile.Name())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		err = managerConfig.SetupIndexes(ctx, mgr)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		gomega.Expect(mgr.GetFieldIndexer()).NotTo(gomega.BeNil(), "field indexer should be available")
	})
})

var _ = ginkgo.Describe("SetupServerVersionFetcher", func() {
	var (
		cancel     context.CancelFunc
		configFile *os.File
		mgr        ctrl.Manager
	)

	ginkgo.BeforeEach(func() {
		_, cancel = context.WithTimeout(ctx, 30*time.Second)

		prev := manager.SetGetConfigForTest(func() *rest.Config { return cfg })
		ginkgo.DeferCleanup(func() { manager.SetGetConfigForTest(prev) })

		var err error
		configFile, err = os.CreateTemp(ginkgo.GinkgoT().TempDir(), "kueue-config-*.yaml")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		configContent := `apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
leaderElection:
  leaderElect: false
health:
  healthProbeBindAddress: :0
metrics:
  bindAddress: :0
`
		_, err = configFile.WriteString(configContent)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		err = configFile.Close()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		managerConfig := manager.NewConfig()
		err = managerConfig.Apply(configFile.Name())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		managerConfig.Options.LeaderElection = false
		managerConfig.Options.HealthProbeBindAddress = ":0"
		mgr, err = ctrl.NewManager(cfg, managerConfig.Options)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	})

	ginkgo.AfterEach(func() {
		if cancel != nil {
			cancel()
		}
		if configFile != nil {
			_ = os.Remove(configFile.Name())
		}
	})

	ginkgo.It("successfully fetches server version", func() {
		managerConfig := manager.NewConfig()
		err := managerConfig.Apply(configFile.Name())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		fetcher, err := managerConfig.SetupServerVersionFetcher(mgr, cfg)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(fetcher).NotTo(gomega.BeNil())

		version := fetcher.GetServerVersion()
		gomega.Expect(version.String()).NotTo(gomega.BeEmpty(), "server version should be fetched")
	})
})
