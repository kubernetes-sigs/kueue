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
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	visibility "sigs.k8s.io/kueue/apis/visibility/v1beta1"
)

// ExpectMultiKueueGuardRailValidation waits for MultiKueueGuardRail status
func ExpectMultiKueueGuardRailValidation(
	ctx context.Context,
	k8sClient client.Client,
	cqKey client.ObjectKey,
	valid bool,
) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		cq := &kueue.ClusterQueue{}
		g.Expect(k8sClient.Get(ctx, cqKey, cq)).To(gomega.Succeed())

		g.Expect(apimeta.IsStatusConditionTrue(cq.Status.Conditions, kueue.ClusterQueueActive)).Should(gomega.Equal(valid))
	}, Timeout, Interval).Should(gomega.Succeed())
}

// ExpectWorkloadClusterQueueName waits until workload has specific cluster queue assignment
func ExpectWorkloadClusterQueueName(
	ctx context.Context,
	k8sClient client.Client,
	wlKey client.ObjectKey,
	expectedCQ string,
) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		wl := &kueue.Workload{}
		g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
		g.Expect(wl.Status.Admission).ShouldNot(gomega.BeNil())
		g.Expect(string(wl.Status.Admission.ClusterQueue)).Should(gomega.Equal(expectedCQ))
	}, Timeout, Interval).Should(gomega.Succeed())
}

// ExpectMultiKueueWorkloadApplied waits until a workload is applied to remote cluster
func ExpectMultiKueueWorkloadApplied(
	ctx context.Context,
	k8sClient client.Client,
	wlKey client.ObjectKey,
) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		wl := &kueue.Workload{}
		g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
		cond := FindStatusCondition(wl.Status.Conditions, "WorkloadApplied")
		g.Expect(cond).ShouldNot(gomega.BeNil())
		g.Expect(cond.Status).Should(gomega.Equal(metav1.ConditionTrue))
	}, Timeout, Interval).Should(gomega.Succeed())
}

// ExpectWorkloadVisibility waits for workload visibility info
func ExpectWorkloadVisibility(
	ctx context.Context,
	k8sClient client.Client,
	visibilityKey client.ObjectKey,
) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		vis := &visibility.ClusterQueue{}
		g.Expect(k8sClient.Get(ctx, visibilityKey, vis)).To(gomega.Succeed())
		g.Expect(vis.Name).NotTo(gomega.BeEmpty())
	}, Timeout, Interval).Should(gomega.Succeed())
}
