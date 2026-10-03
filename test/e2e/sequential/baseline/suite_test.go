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

package baseline

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/e2e"
)

var (
	k8sClient       client.WithWatch
	cfg             *rest.Config
	restClient      *rest.RESTClient
	ctx             context.Context
	defaultKueueCfg *config.Configuration
	kueueNS         = e2e.GetKueueNamespace()
	kindClusterName = os.Getenv("KIND_CLUSTER_NAME")
)

func TestAPIs(t *testing.T) {
	e2e.RunE2ESuite(t, "End To End Sequential Baseline Suite")
}

var _ = ginkgo.BeforeSuite(func() {
	behavioral.SetupLogger()

	var err error
	k8sClient, cfg, err = e2e.CreateClientUsingCluster("")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	restClient = e2e.CreateRestClient(cfg)
	ctx = ginkgo.GinkgoT().Context()

	waitForAvailableStart := time.Now()
	e2e.WaitForKueueAvailability(ctx, k8sClient)
	ginkgo.GinkgoLogr.Info(
		"Kueue is available in the cluster",
		"waitingTime", time.Since(waitForAvailableStart),
	)
	defaultKueueCfg = e2e.GetKueueConfiguration(ctx, k8sClient)
})

var _ = ginkgo.AfterSuite(func() {
	if e2e.IsE2EModeDev() {
		e2e.UpdateKueueConfigurationAndRestart(ctx, k8sClient, defaultKueueCfg, kindClusterName)
	}
})
