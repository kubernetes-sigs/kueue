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

package behavioral

import (
	"context"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

// ExpectWorkloadTotalActiveCount waits until expected workload count is active in a ClusterQueue
func ExpectWorkloadTotalActiveCount(ctx context.Context, k8sClient client.Client, cqKey client.ObjectKey, expectedCount int) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		cq := &kueue.ClusterQueue{}
		g.Expect(k8sClient.Get(ctx, cqKey, cq)).To(gomega.Succeed())
		g.Expect(cq.Status.FlavorsReservation).To(gomega.HaveLen(expectedCount))
	}, Timeout, Interval).Should(gomega.Succeed())
}

// ExpectWorkloadsScheduled waits until workloads are scheduled with specific ResourceFlavor and ClusterQueue
func ExpectWorkloadsScheduled(
	ctx context.Context,
	k8sClient client.Client,
	cqName string,
	flavorName string,
	wlKeys ...client.ObjectKey,
) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		for _, wlKey := range wlKeys {
			wl := &kueue.Workload{}
			g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
			g.Expect(wl.Status.Admission).ShouldNot(gomega.BeNil())
			g.Expect(string(wl.Status.Admission.ClusterQueue)).Should(gomega.Equal(cqName))
		}
	}, Timeout, Interval).Should(gomega.Succeed())
}

// ExpectWorkloadsPreempted waits until workloads are preempted
func ExpectWorkloadsPreempted(
	ctx context.Context,
	k8sClient client.Client,
	wlKeys ...client.ObjectKey,
) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		for _, wlKey := range wlKeys {
			wl := &kueue.Workload{}
			g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
			cond := FindStatusCondition(wl.Status.Conditions, kueue.WorkloadEvicted)
			g.Expect(cond).ShouldNot(gomega.BeNil())
			g.Expect(cond.Reason).Should(gomega.Equal(kueue.WorkloadEvictedByPreemption))
		}
	}, Timeout, Interval).Should(gomega.Succeed())
}
