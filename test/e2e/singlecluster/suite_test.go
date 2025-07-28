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

package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"k8s.io/client-go/rest"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"

	visibilityv1beta1 "sigs.k8s.io/kueue/client-go/clientset/versioned/typed/visibility/v1beta1"
	"sigs.k8s.io/kueue/test/util"
)

var (
	kueuectlPath                 = filepath.Join("..", "..", "..", "bin", "kubectl-kueue")
	realClock                    = clock.RealClock{}
	k8sClient                    client.WithWatch
	cfg                          *rest.Config
	restClient                   *rest.RESTClient
	ctx                          context.Context
	visibilityClient             visibilityv1beta1.VisibilityV1beta1Interface
	impersonatedVisibilityClient visibilityv1beta1.VisibilityV1beta1Interface
	kueueNS                      = util.GetKueueNamespace()
)

func TestAPIs(t *testing.T) {
	suiteName := "End To End Suite"
	if ver, found := os.LookupEnv("E2E_KIND_VERSION"); found {
		suiteName = fmt.Sprintf("%s: %s", suiteName, ver)
	}
	gomega.RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t,
		suiteName,
	)
}

var _ = ginkgo.BeforeSuite(func() {
	util.SetupLogger()

	var err error
	k8sClient, cfg, err = util.CreateClientUsingCluster("")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	restClient = util.CreateRestClient(cfg)
	visibilityClient, err = util.CreateVisibilityClient("")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	impersonatedVisibilityClient, err = util.CreateVisibilityClient(fmt.Sprintf("system:serviceaccount:%s:default", kueueNS))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	ctx = ginkgo.GinkgoT().Context()

	waitForAvailableStart := time.Now()
	util.WaitForKueueAvailability(ctx, k8sClient)
	util.WaitForJobSetAvailability(ctx, k8sClient)
	util.WaitForLeaderWorkerSetAvailability(ctx, k8sClient)
	util.WaitForAppWrapperAvailability(ctx, k8sClient)
	util.WaitForKubeFlowTrainingOperatorAvailability(ctx, k8sClient)
	ginkgo.GinkgoLogr.Info(
		"Kueue and all required operators are available in the cluster",
		"waitingTime", time.Since(waitForAvailableStart),
	)
})
