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

	"github.com/onsi/gomega"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

// ExpectClusterQueueStatusReady waits until ClusterQueue status is ready
func ExpectClusterQueueStatusReady(ctx context.Context, k8sClient client.Client, cqKey client.ObjectKey) {
	cq := &kueue.ClusterQueue{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, cqKey, cq)).To(gomega.Succeed())
		g.Expect(cq.Status.Conditions).To(HaveConditionStatusTrue(kueue.ClusterQueueActive))
	}, Timeout, Interval).Should(gomega.Succeed())
}

// ExpectClusterQueueStatusNotReady waits until ClusterQueue status is not ready
func ExpectClusterQueueStatusNotReady(ctx context.Context, k8sClient client.Client, cqKey client.ObjectKey) {
	cq := &kueue.ClusterQueue{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, cqKey, cq)).To(gomega.Succeed())
		cond := FindStatusCondition(cq.Status.Conditions, kueue.ClusterQueueActive)
		g.Expect(cond).Should(gomega.BeNil())
	}, Timeout, Interval).Should(gomega.Succeed())
}

// ExpectLocalQueueStatusReady waits until LocalQueue status is ready
func ExpectLocalQueueStatusReady(ctx context.Context, k8sClient client.Client, lqKey client.ObjectKey) {
	lq := &kueue.LocalQueue{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, lqKey, lq)).To(gomega.Succeed())
		g.Expect(lq.Status.Conditions).To(HaveConditionStatusTrue(kueue.LocalQueueActive))
	}, Timeout, Interval).Should(gomega.Succeed())
}

// ExpectLocalQueueStatusNotReady waits until LocalQueue status is not ready
func ExpectLocalQueueStatusNotReady(ctx context.Context, k8sClient client.Client, lqKey client.ObjectKey) {
	lq := &kueue.LocalQueue{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, lqKey, lq)).To(gomega.Succeed())
		cond := FindStatusCondition(lq.Status.Conditions, kueue.LocalQueueActive)
		g.Expect(cond).Should(gomega.BeNil())
	}, Timeout, Interval).Should(gomega.Succeed())
}

// HoldClusterQueue sets the stop policy on a ClusterQueue
func HoldClusterQueue(ctx context.Context, k8sClient client.Client, cq *kueue.ClusterQueue) {
	gomega.Eventually(func(g gomega.Gomega) {
		var newCQ kueue.ClusterQueue
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &newCQ)).To(gomega.Succeed())
		newCQ.Spec.StopPolicy = ptr.To(kueue.HoldAndDrain)
		g.Expect(k8sClient.Update(ctx, &newCQ)).To(gomega.Succeed())
	}, Timeout, Interval).Should(gomega.Succeed())
}

// HoldLocalQueue sets the stop policy on a LocalQueue
func HoldLocalQueue(ctx context.Context, k8sClient client.Client, lq *kueue.LocalQueue) {
	gomega.Eventually(func(g gomega.Gomega) {
		var newLQ kueue.LocalQueue
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lq), &newLQ)).To(gomega.Succeed())
		newLQ.Spec.StopPolicy = ptr.To(kueue.HoldAndDrain)
		g.Expect(k8sClient.Update(ctx, &newLQ)).To(gomega.Succeed())
	}, Timeout, Interval).Should(gomega.Succeed())
}
