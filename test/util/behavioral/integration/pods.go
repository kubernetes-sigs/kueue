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

package integration

import (
	"context"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/test/util/behavioral"
)

func SetPodsPhaseByKeys(ctx context.Context, k8sClient client.Client, phase corev1.PodPhase, keys ...client.ObjectKey) {
	ginkgo.GinkgoHelper()
	for _, key := range keys {
		updatedPod := corev1.Pod{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, key, &updatedPod)).To(gomega.Succeed())
			updatedPod.Status.Phase = phase
			g.Expect(k8sClient.Status().Update(ctx, &updatedPod)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed(), behavioral.AssertMsg("Failed to set pod phase", &updatedPod))
	}
}

func SetPodsPhase(ctx context.Context, k8sClient client.Client, phase corev1.PodPhase, pods ...*corev1.Pod) {
	ginkgo.GinkgoHelper()
	SetPodsPhaseByKeys(ctx, k8sClient, phase, podKeys(pods)...)
}

func podKeys(pods []*corev1.Pod) []client.ObjectKey {
	keys := make([]client.ObjectKey, 0, len(pods))
	for _, p := range pods {
		keys = append(keys, client.ObjectKeyFromObject(p))
	}
	return keys
}

func BindPodWithNode(ctx context.Context, k8sClient client.Client, nodeName string, pods ...*corev1.Pod) {
	for _, p := range pods {
		updatedPod := corev1.Pod{}
		gomega.ExpectWithOffset(1, k8sClient.Get(ctx, client.ObjectKeyFromObject(p), &updatedPod)).To(gomega.Succeed())
		binding := corev1.Binding{
			Target: corev1.ObjectReference{
				Kind: "Node",
				Name: nodeName,
			},
		}
		gomega.ExpectWithOffset(1, k8sClient.SubResource("binding").Create(ctx, &updatedPod, &binding)).To(gomega.Succeed())
	}
}
