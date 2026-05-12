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

package extended

import (
	"context"
	"testing"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/test/util"
)

var (
	k8sClient  client.WithWatch
	cfg        *rest.Config
	restClient *rest.RESTClient
	ctx        context.Context
)

func TestAPIs(t *testing.T) {
	util.RunE2ESuite(t, "End To End Extended Suite")
}

var _ = ginkgo.BeforeSuite(func() {
	util.SetupLogger()

	var err error
	k8sClient, cfg, err = util.CreateClientUsingCluster("")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	restClient = util.CreateRestClient(cfg)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	ctx = ginkgo.GinkgoT().Context()

	waitForAvailableStart := time.Now()
	util.WaitForKueueAvailability(ctx, k8sClient)
	labelFilter := ginkgo.GinkgoLabelFilter()
	if ginkgo.Label("feature:jobset", "feature:tas", "feature:trainjob").MatchesLabelFilter(labelFilter) {
		util.WaitForJobSetAvailability(ctx, k8sClient)
	}
	if ginkgo.Label("feature:leaderworkerset").MatchesLabelFilter(labelFilter) {
		util.WaitForLeaderWorkerSetAvailability(ctx, k8sClient)
	}
	if ginkgo.Label("feature:appwrapper").MatchesLabelFilter(labelFilter) {
		util.WaitForAppWrapperAvailability(ctx, k8sClient)
	}
	if ginkgo.Label("feature:jaxjob", "feature:pytorchjob").MatchesLabelFilter(labelFilter) {
		util.WaitForKubeFlowTrainingOperatorAvailability(ctx, k8sClient)
	}
	if ginkgo.Label("feature:kuberay").MatchesLabelFilter(labelFilter) {
		util.WaitForKubeRayOperatorAvailability(ctx, k8sClient)
	}
	if ginkgo.Label("feature:tas", "feature:trainjob").MatchesLabelFilter(labelFilter) {
		util.WaitForKubeFlowTrainnerControllerManagerAvailability(ctx, k8sClient)
	}
	ginkgo.GinkgoLogr.Info(
		"Kueue and all required operators are available in the cluster",
		"waitingTime", time.Since(waitForAvailableStart),
	)
})
