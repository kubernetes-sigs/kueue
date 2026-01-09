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

package dra

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/test/util"
)

var (
	k8sClient client.WithWatch
	ctx       context.Context
)

func TestAPIs(t *testing.T) {
	suiteName := "End To End DRA Integration Suite"
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
	k8sClient, _, err = util.CreateClientUsingCluster("")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	ctx = ginkgo.GinkgoT().Context()

	waitForAvailableStart := time.Now()
	util.WaitForKueueAvailability(ctx, k8sClient)
	waitForDRAExampleDriverAvailability(ctx, k8sClient)
	ginkgo.GinkgoLogr.Info(
		"Kueue and DRA example driver are available in the cluster",
		"waitingTime", time.Since(waitForAvailableStart),
	)
})

func waitForDRAExampleDriverAvailability(ctx context.Context, k8sClient client.Client) {
	dsKey := types.NamespacedName{Namespace: "dra-example-driver", Name: "dra-example-driver-kubeletplugin"}
	daemonset := &appsv1.DaemonSet{}
	waitForAvailableStart := time.Now()
	ginkgo.By(fmt.Sprintf("Waiting for availability of daemonset: %q", dsKey))
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, dsKey, daemonset)).To(gomega.Succeed())
		g.Expect(daemonset.Status.DesiredNumberScheduled).To(gomega.BeNumerically(">", 0))
		g.Expect(daemonset.Status.DesiredNumberScheduled).To(gomega.Equal(daemonset.Status.NumberAvailable))
	}, util.StartUpTimeout, util.Interval).Should(gomega.Succeed())
	ginkgo.GinkgoLogr.Info("DaemonSet is available in the cluster", "daemonset", dsKey, "waitingTime", time.Since(waitForAvailableStart))
}
