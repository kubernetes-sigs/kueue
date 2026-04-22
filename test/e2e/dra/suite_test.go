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
	"cmp"
	"context"
	"os"
	"testing"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/test/util"
)

var (
	k8sClient client.WithWatch
	ctx       context.Context
)

func TestAPIs(t *testing.T) {
	util.RunE2ESuite(t, "End To End DRA Integration Suite")
}

var _ = ginkgo.BeforeSuite(func() {
	util.SetupLogger()

	var err error
	k8sClient, _, err = util.CreateClientUsingCluster("")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	ctx = ginkgo.GinkgoT().Context()

	waitForAvailableStart := time.Now()
	util.WaitForKueueAvailability(ctx, k8sClient)
	clusterName := cmp.Or(os.Getenv("KIND_CLUSTER_NAME"), "kind")
	util.WaitForDRAExampleDriverAvailability(ctx, k8sClient, clusterName)
	ginkgo.GinkgoLogr.Info(
		"Kueue and DRA example driver are available in the cluster",
		"clusterName", clusterName,
		"waitingTime", time.Since(waitForAvailableStart),
	)
})
