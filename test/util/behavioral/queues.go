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
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func SetResourceNominalQuota(cq *kueue.ClusterQueue, resourceName corev1.ResourceName, value string) *kueue.ClusterQueue {
	return SetFlavorResourceNominalQuota(cq, "", resourceName, value)
}

func SetFlavorResourceNominalQuota(cq *kueue.ClusterQueue, flavorName string, resourceName corev1.ResourceName, value string) *kueue.ClusterQueue {
	for rgi := range cq.Spec.ResourceGroups {
		for fi := range cq.Spec.ResourceGroups[rgi].Flavors {
			if flavorName == "" || string(cq.Spec.ResourceGroups[rgi].Flavors[fi].Name) == flavorName {
				for ri := range cq.Spec.ResourceGroups[rgi].Flavors[fi].Resources {
					if cq.Spec.ResourceGroups[rgi].Flavors[fi].Resources[ri].Name == resourceName {
						cq.Spec.ResourceGroups[rgi].Flavors[fi].Resources[ri].NominalQuota = resource.MustParse(value)
						return cq
					}
				}
			}
		}
	}
	return cq
}

func setClusterQueueStopPolicy(ctx context.Context, k8sClient client.Client, cq *kueue.ClusterQueue, policy *kueue.StopPolicy) {
	ginkgo.GinkgoHelper()
	var cqCopy kueue.ClusterQueue
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &cqCopy)).To(gomega.Succeed())
		if ptr.Equal(cqCopy.Spec.StopPolicy, policy) {
			return
		}
		cqCopy.Spec.StopPolicy = policy
		g.Expect(k8sClient.Update(ctx, &cqCopy)).To(gomega.Succeed())
	}, Timeout, ShortInterval).Should(gomega.Succeed(), AssertMsg("Failed to set cluster queue stop policy to "+string(ptr.Deref(policy, "<unset>")), &cqCopy))
}

func SetClusterQueueStopPolicy(ctx context.Context, k8sClient client.Client, cq *kueue.ClusterQueue, policy kueue.StopPolicy) {
	ginkgo.GinkgoHelper()
	setClusterQueueStopPolicy(ctx, k8sClient, cq, new(policy))
}

func UnsetClusterQueueStopPolicy(ctx context.Context, k8sClient client.Client, cq *kueue.ClusterQueue) {
	ginkgo.GinkgoHelper()
	setClusterQueueStopPolicy(ctx, k8sClient, cq, nil)
}

func SetLocalQueueStopPolicy(ctx context.Context, k8sClient client.Client, lq *kueue.LocalQueue, policy kueue.StopPolicy) {
	ginkgo.GinkgoHelper()
	setLocalQueueStopPolicy(ctx, k8sClient, lq, &policy, Timeout)
}

func SetLocalQueueStopPolicyWithTimeout(ctx context.Context, k8sClient client.Client, lq *kueue.LocalQueue, policy kueue.StopPolicy, timeout time.Duration) {
	ginkgo.GinkgoHelper()
	setLocalQueueStopPolicy(ctx, k8sClient, lq, &policy, timeout)
}

func UnsetLocalQueueStopPolicy(ctx context.Context, k8sClient client.Client, lq *kueue.LocalQueue) {
	ginkgo.GinkgoHelper()
	setLocalQueueStopPolicy(ctx, k8sClient, lq, nil, Timeout)
}

func setLocalQueueStopPolicy(ctx context.Context, k8sClient client.Client, lq *kueue.LocalQueue, policy *kueue.StopPolicy, timeout time.Duration) {
	ginkgo.GinkgoHelper()
	var lqCopy kueue.LocalQueue
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lq), &lqCopy)).To(gomega.Succeed())
		if ptr.Equal(lqCopy.Spec.StopPolicy, policy) {
			return
		}
		lqCopy.Spec.StopPolicy = policy
		g.Expect(k8sClient.Update(ctx, &lqCopy)).To(gomega.Succeed())
	}, timeout, Interval).Should(gomega.Succeed(), AssertMsg("Failed to set local queue stop policy to "+string(ptr.Deref(policy, "<unset>")), &lqCopy))
}

func ExpectClusterQueuesToBeActive(ctx context.Context, c client.Client, cqs ...*kueue.ClusterQueue) {
	ginkgo.GinkgoHelper()
	readCq := &kueue.ClusterQueue{}
	gomega.Eventually(func(g gomega.Gomega) {
		for _, cq := range cqs {
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(cq), readCq)).To(gomega.Succeed())
			g.Expect(readCq.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.ClusterQueueActive))
		}
	}, MediumTimeout, Interval).Should(gomega.Succeed(), AssertMsg("ClusterQueues did not become active", readCq))
}

func ExpectLocalQueuesToBeActive(ctx context.Context, c client.Client, lqs ...*kueue.LocalQueue) {
	ginkgo.GinkgoHelper()
	readLq := &kueue.LocalQueue{}
	gomega.Eventually(func(g gomega.Gomega) {
		for _, lq := range lqs {
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(lq), readLq)).To(gomega.Succeed())
			g.Expect(readLq.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.LocalQueueActive))
		}
	}, MediumTimeout, Interval).Should(gomega.Succeed(), AssertMsg("LocalQueues did not become active", readLq))
}

func CreateClusterQueuesAndWaitForActive(ctx context.Context, c client.Client, cqs ...*kueue.ClusterQueue) {
	ginkgo.GinkgoHelper()
	for _, cq := range cqs {
		MustCreate(ctx, c, cq)
	}
	ExpectClusterQueuesToBeActive(ctx, c, cqs...)
}

func CreateLocalQueuesAndWaitForActive(ctx context.Context, c client.Client, lqs ...*kueue.LocalQueue) {
	ginkgo.GinkgoHelper()
	for _, lq := range lqs {
		MustCreate(ctx, c, lq)
	}
	ExpectLocalQueuesToBeActive(ctx, c, lqs...)
}

func ExpectLocalQueueFairSharingUsageToBe(ctx context.Context, k8sClient client.Client, lqKey client.ObjectKey, comparator string, compareTo any) {
	ginkgo.GinkgoHelper()
	lq := &kueue.LocalQueue{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, lqKey, lq)).Should(gomega.Succeed())
		g.Expect(lq.Status.FairSharing).ShouldNot(gomega.BeNil())
		g.Expect(lq.Status.FairSharing.AdmissionFairSharingStatus).ShouldNot(gomega.BeNil())
		g.Expect(lq.Status.FairSharing.AdmissionFairSharingStatus.ConsumedResources).Should(gomega.HaveLen(1))
		usage := lq.Status.FairSharing.AdmissionFairSharingStatus.ConsumedResources[corev1.ResourceCPU]
		g.Expect(usage.MilliValue()).To(gomega.BeNumerically(comparator, compareTo))
	}, Timeout, Interval).Should(gomega.Succeed(), AssertMsg("LocalQueue fair sharing usage does not match expected", lq))
}

// ClusterQueueResourceUsage returns the total resource usage across all flavors for the given resource.
func ClusterQueueResourceUsage(cq *kueue.ClusterQueue, resourceName corev1.ResourceName) int64 {
	total := resource.Quantity{}
	for _, fu := range cq.Status.FlavorsUsage {
		for _, r := range fu.Resources {
			if r.Name == resourceName {
				total.Add(r.Total)
			}
		}
	}
	return total.Value()
}

// ExpectClusterQueueResourceUsage waits until the ClusterQueue reports the given resource usage.
func ExpectClusterQueueResourceUsage(ctx context.Context, k8sClient client.Client, cqKey client.ObjectKey, resourceName corev1.ResourceName, want int64) {
	ginkgo.GinkgoHelper()
	ExpectClusterQueueResourceUsageWithTimeout(ctx, k8sClient, cqKey, resourceName, want, MediumTimeout)
}

// ExpectClusterQueueResourceUsageWithTimeout waits until the ClusterQueue reports the given resource usage within the given timeout.
func ExpectClusterQueueResourceUsageWithTimeout(ctx context.Context, k8sClient client.Client, cqKey client.ObjectKey, resourceName corev1.ResourceName, want int64, timeout time.Duration) {
	ginkgo.GinkgoHelper()
	cq := &kueue.ClusterQueue{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, cqKey, cq)).To(gomega.Succeed())
		g.Expect(ClusterQueueResourceUsage(cq, resourceName)).To(gomega.Equal(want))
	}, timeout, Interval).Should(gomega.Succeed(), AssertMsg("ClusterQueue "+string(resourceName)+" usage did not settle", cq))
}
