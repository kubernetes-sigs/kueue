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

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
)

// ExpectCapacityProviderReady waits until CapacityProvider is ready
func ExpectCapacityProviderReady(ctx context.Context, k8sClient client.Client, cpKey client.ObjectKey) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		cp := &kueuealpha.CapacityProvider{}
		g.Expect(k8sClient.Get(ctx, cpKey, cp)).To(gomega.Succeed())
		cond := FindStatusCondition(cp.Status.Conditions, kueuealpha.CapacityProviderCapacitySynchronized)
		g.Expect(cond).ShouldNot(gomega.BeNil())
		g.Expect(cond.Status).Should(gomega.Equal("True"))
	}, Timeout, Interval).Should(gomega.Succeed())
}

// ExpectCapacityProviderNotReady waits until CapacityProvider is not ready
func ExpectCapacityProviderNotReady(ctx context.Context, k8sClient client.Client, cpKey client.ObjectKey) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		cp := &kueuealpha.CapacityProvider{}
		g.Expect(k8sClient.Get(ctx, cpKey, cp)).To(gomega.Succeed())
		cond := FindStatusCondition(cp.Status.Conditions, kueuealpha.CapacityProviderCapacitySynchronized)
		g.Expect(cond).ShouldNot(gomega.BeNil())
		g.Expect(cond.Status).Should(gomega.Equal("False"))
	}, Timeout, Interval).Should(gomega.Succeed())
}
