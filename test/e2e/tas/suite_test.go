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

package tase2e

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clientutil "sigs.k8s.io/kueue/pkg/util/client"
	"sigs.k8s.io/kueue/test/util"
)

var (
	k8sClient client.WithWatch
	ctx       context.Context
)

func TestAPIs(t *testing.T) {
	suiteName := "End To End TAS Suite"
	if ver, found := os.LookupEnv("E2E_KIND_VERSION"); found {
		suiteName = fmt.Sprintf("%s: %s", suiteName, ver)
	}
	gomega.RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t,
		suiteName,
	)
}

var _ = ginkgo.BeforeSuite(func() {
	ctrl.SetLogger(util.NewTestingLogger(ginkgo.GinkgoWriter, -3))

	k8sClient, _ = util.CreateClientUsingCluster("")
	ctx = context.Background()

	waitForAvailableStart := time.Now()
	util.WaitForKueueAvailability(ctx, k8sClient)
	util.WaitForJobSetAvailability(ctx, k8sClient)
	util.WaitForKubeFlowTrainingOperatorAvailability(ctx, k8sClient)
	util.WaitForKubeFlowMPIOperatorAvailability(ctx, k8sClient)
	ginkgo.GinkgoLogr.Info(
		"Kueue, JobSet, KubeFlow Training and KubeFlow MPI operators are available in the cluster",
		"waitingTime", time.Since(waitForAvailableStart),
	)

	nodes := &corev1.NodeList{}
	requiredLabels := client.MatchingLabels{}
	requiredLabelKeys := client.HasLabels{tasNodeGroupLabel}
	err := k8sClient.List(ctx, nodes, requiredLabels, requiredLabelKeys)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "failed to list nodes for TAS")

	for _, n := range nodes.Items {
		err := clientutil.PatchStatus(ctx, k8sClient, &n, func() (bool, error) {
			n.Status.Capacity[extraResource] = resource.MustParse("1")
			n.Status.Allocatable[extraResource] = resource.MustParse("1")
			return true, nil
		})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}
})
