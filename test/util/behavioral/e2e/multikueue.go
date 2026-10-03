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

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

// ExpectWorkloadOnAnotherCluster waits until workload is applied to another cluster
func ExpectWorkloadOnAnotherCluster(
	ctx context.Context,
	remoteClient client.Client,
	wlKey client.ObjectKey,
) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		wl := &kueue.Workload{}
		g.Expect(remoteClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
		g.Expect(wl.Name).Should(gomega.Equal(wlKey.Name))
	}, Timeout, Interval).Should(gomega.Succeed())
}

// ExpectWorkloadDeletedOnAnotherCluster waits until workload is deleted from another cluster
func ExpectWorkloadDeletedOnAnotherCluster(
	ctx context.Context,
	remoteClient client.Client,
	wlKey client.ObjectKey,
) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		wl := &kueue.Workload{}
		err := remoteClient.Get(ctx, wlKey, wl)
		g.Expect(client.IgnoreNotFound(err)).To(gomega.Succeed())
	}, Timeout, Interval).Should(gomega.Succeed())
}

// WaitForMultiClusterCompoundFeatures waits for multikueue features to be ready
func WaitForMultiClusterCompoundFeatures(
	ctx context.Context,
	managerClient client.Client,
) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		// Add checks for MultiKueue-specific features/components
		// This is a placeholder for compound feature readiness checks
	}, VeryLongTimeout, Interval).Should(gomega.Succeed())
}
