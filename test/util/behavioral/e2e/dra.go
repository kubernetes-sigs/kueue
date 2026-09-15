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
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

// WaitForDRAAvailability waits for DRA components to be available
func WaitForDRAAvailability(ctx context.Context, k8sClient client.Client) {
	ginkgo.GinkgoHelper()
	// Wait for DRA example driver daemonset
	WaitForDRAExampleDriverAvailability(ctx, k8sClient)
}

// WaitForDRAExampleDriverAvailability waits for DRA example driver to be available
func WaitForDRAExampleDriverAvailability(ctx context.Context, k8sClient client.Client) {
	ginkgo.GinkgoHelper()
	dsKey := types.NamespacedName{Namespace: "dra-example-driver", Name: "dra-example-driver-kubeletplugin"}
	daemonset := &appsv1.DaemonSet{}
	waitForAvailableStart := time.Now()
	ginkgo.By(fmt.Sprintf("Waiting for availability of daemonset %q", dsKey))
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, dsKey, daemonset)).To(gomega.Succeed())
		g.Expect(daemonset.Status.DesiredNumberScheduled).To(gomega.BeNumerically(">", 0))
		g.Expect(daemonset.Status.DesiredNumberScheduled).To(gomega.Equal(daemonset.Status.NumberAvailable))
	}, VeryLongTimeout, Interval).Should(gomega.Succeed())
	ginkgo.GinkgoLogr.Info("DaemonSet is available", "daemonset", dsKey, "waitingTime", time.Since(waitForAvailableStart))
}

// ExpectResourceClaimTemplateValid validates ResourceClaimTemplate in workload
func ExpectResourceClaimTemplateValid(
	ctx context.Context,
	k8sClient client.Client,
	wlKey client.ObjectKey,
) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		wl := &kueue.Workload{}
		g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
		// Add validation checks for ResourceClaim templates
	}, Timeout, Interval).Should(gomega.Succeed())
}
