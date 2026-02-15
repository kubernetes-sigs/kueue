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

package certmanager

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	prometheusv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	visibility "sigs.k8s.io/kueue/client-go/clientset/versioned/typed/visibility/v1beta2"
	"sigs.k8s.io/kueue/test/util"
)

var (
	k8sClient        client.WithWatch
	ctx              context.Context
	cfg              *rest.Config
	restClient       *rest.RESTClient
	prometheusClient prometheusv1.API
	kueueNS          = util.GetKueueNamespace()
	visibilityClient visibility.VisibilityV1beta2Interface
)

func TestAPIs(t *testing.T) {
	suiteName := "End To End Cert Manager Integration Suite"
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
	visibilityClient = util.CreateVisibilityClient("")
	ctx = ginkgo.GinkgoT().Context()

	waitForAvailableStart := time.Now()
	util.WaitForKueueAvailability(ctx, k8sClient)
	labelFilter := ginkgo.GinkgoLabelFilter()
	if ginkgo.Label("feature:prometheus").MatchesLabelFilter(labelFilter) {
		prometheusClient = util.CreatePrometheusClient(cfg)
		util.WaitForPrometheusAvailability(ctx, k8sClient)
	}
	ginkgo.GinkgoLogr.Info(
		"Kueue and all required operators are available in the cluster",
		"waitingTime", time.Since(waitForAvailableStart),
	)
})
