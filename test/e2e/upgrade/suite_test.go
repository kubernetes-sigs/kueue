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

package upgrade

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/test/util"
)

var (
	k8sClient   client.WithWatch
	cfg         *rest.Config
	ctx         context.Context
	kueueNS     = util.GetKueueNamespace()
	upgradeFrom = os.Getenv("KUEUE_UPGRADE_FROM_VERSION")
)

func TestUpgrade(t *testing.T) {
	suiteName := "Upgrade Test Suite"
	if upgradeFrom != "" {
		suiteName = fmt.Sprintf("%s: %s -> current", suiteName, upgradeFrom)
	}
	if ver, found := os.LookupEnv("E2E_KIND_VERSION"); found {
		suiteName = fmt.Sprintf("%s (K8s %s)", suiteName, ver)
	}
	gomega.RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t, suiteName)
}

var _ = ginkgo.BeforeSuite(func() {
	util.SetupLogger()

	if upgradeFrom == "" {
		ginkgo.Fail("KUEUE_UPGRADE_FROM_VERSION environment variable must be set")
	}

	ginkgo.GinkgoLogr.Info("Starting upgrade test suite",
		"upgradeFrom", upgradeFrom,
		"namespace", kueueNS)

	var err error

	k8sClient, cfg, err = util.CreateClientUsingCluster("")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	ctx = ginkgo.GinkgoT().Context()

	ginkgo.GinkgoLogr.Info("Waiting for Kueue to be available", "version", upgradeFrom)
	waitForAvailableStart := time.Now()
	util.WaitForKueueAvailabilityNoRestartCountCheck(ctx, k8sClient)
	ginkgo.GinkgoLogr.Info(
		"Kueue is available",
		"version", upgradeFrom,
		"waitingTime", time.Since(waitForAvailableStart),
	)
})
