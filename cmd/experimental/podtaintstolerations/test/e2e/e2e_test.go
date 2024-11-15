/*
Copyright 2022 The Kubernetes Authors.

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
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"

	"podtaintstolerations/controller"
	testingpod "podtaintstolerations/test/util"
)

var _ = ginkgo.Describe("Kueue Pod-Taints-Tolerations Controller", func() {
	var ns *corev1.Namespace
	var samplePod *corev1.Pod
	var podKey types.NamespacedName

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "e2e-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
		samplePod = testingpod.MakePod("test-pod", ns.Name).
			Queue("main").
			Request("cpu", "1").
			Request("memory", "20Mi").
			Obj()
		podKey = client.ObjectKeyFromObject(samplePod)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.When("Creating a Pod without a matching LocalQueue", func() {
		ginkgo.It("Should stay in suspended", func() {
			gomega.Expect(k8sClient.Create(ctx, samplePod)).Should(gomega.Succeed())

			createdPod := &corev1.Pod{}
			gomega.Eventually(func() bool {
				if err := k8sClient.Get(ctx, podKey, createdPod); err != nil {
					return false
				}
				return isSuspended(createdPod)
			}, util.Timeout, util.Interval).Should(gomega.BeTrue())

			wlLookupKey := types.NamespacedName{Name: jobframework.GetWorkloadNameForOwnerWithGVK(podKey.Name, controller.GVK), Namespace: ns.Name}
			createdWorkload := &kueue.Workload{}
			gomega.Eventually(func() bool {
				if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
					return false
				}
				return workload.IsAdmitted(createdWorkload)
			}, util.Timeout, util.Interval).Should(gomega.BeFalse())

			gomega.Expect(k8sClient.Delete(ctx, samplePod)).Should(gomega.Succeed())
		})
	})

	ginkgo.When("Creating a Pod With Queueing", func() {
		var (
			onDemandRF   *kueue.ResourceFlavor
			spotRF       *kueue.ResourceFlavor
			localQueue   *kueue.LocalQueue
			clusterQueue *kueue.ClusterQueue
		)

		ginkgo.BeforeEach(func() {
			onDemandRF = testing.MakeResourceFlavor("on-demand").
				Label("instance-type", "on-demand").Obj()
			gomega.Expect(k8sClient.Create(ctx, onDemandRF)).Should(gomega.Succeed())

			spotRF = testing.MakeResourceFlavor("spot").
				Label("instance-type", "spot").Obj()
			gomega.Expect(k8sClient.Create(ctx, spotRF)).Should(gomega.Succeed())

			clusterQueue = testing.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*testing.MakeFlavorQuotas("on-demand").
						Resource(corev1.ResourceCPU, "1").
						Resource(corev1.ResourceMemory, "1Gi").
						Obj(),
					*testing.MakeFlavorQuotas("spot").
						Resource(corev1.ResourceCPU, "1").
						Resource(corev1.ResourceMemory, "1Gi").
						Obj(),
				).
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
				}).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).Should(gomega.Succeed())

			localQueue = testing.MakeLocalQueue("main", ns.Name).ClusterQueue("cluster-queue").Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).Should(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteLocalQueue(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
			gomega.Expect(deleteAllPodsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, onDemandRF, true)
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, spotRF, true)
		})

		ginkgo.It("Should unsuspend a Pod and set tolerations", func() {
			// Use a binary that ends.
			samplePod = (&testingpod.PodWrapper{Pod: *samplePod}).Image("gcr.io/k8s-staging-perf-tests/sleep:v0.0.3", []string{"5s"}).Obj()
			gomega.Expect(k8sClient.Create(ctx, samplePod)).Should(gomega.Succeed())

			expectPodUnsuspendedWithTolerations(podKey, map[string]string{
				"instance-type": "on-demand",
			})

			createdWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: jobframework.GetWorkloadNameForOwnerWithGVK(podKey.Name, controller.GVK), Namespace: ns.Name}
			gomega.Eventually(func() bool {
				if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
					return false
				}
				return workload.IsAdmitted(createdWorkload) &&
					apimeta.IsStatusConditionTrue(createdWorkload.Status.Conditions, kueue.WorkloadFinished)
			}, util.Timeout, util.Interval).Should(gomega.BeTrue())
		})

		ginkgo.It("Should preempted Pods via deletion", func() {
			gomega.Expect(k8sClient.Create(ctx, samplePod)).Should(gomega.Succeed())

			highPriorityClass := testing.MakePriorityClass("high").PriorityValue(100).Obj()
			gomega.Expect(k8sClient.Create(ctx, highPriorityClass))
			ginkgo.DeferCleanup(func() {
				gomega.Expect(k8sClient.Delete(ctx, highPriorityClass)).To(gomega.Succeed())
			})

			ginkgo.By("Pod is admitted using the first flavor", func() {
				expectPodUnsuspendedWithTolerations(podKey, map[string]string{
					"instance-type": "on-demand",
				})
			})

			ginkgo.By("Pod is preempted (deleted) by higher priority Pod", func() {
				pod := testingpod.MakePod("high", ns.Name).
					Queue("main").
					PriorityClass("high").
					Request(corev1.ResourceCPU, "1").
					NodeSelector("instance-type", "on-demand"). // target the same flavor to cause preemption
					Obj()
				gomega.Expect(k8sClient.Create(ctx, pod)).Should(gomega.Succeed())

				gomega.EventuallyWithOffset(1, func() bool {
					return apierrors.IsNotFound(k8sClient.Get(ctx, podKey, &corev1.Pod{}))
				}, util.Timeout, util.Interval).Should(gomega.BeTrue())
			})
		})
	})
})

func expectPodUnsuspendedWithTolerations(key types.NamespacedName, tolerations map[string]string) {
	pod := &corev1.Pod{}
	gomega.EventuallyWithOffset(1, func() bool {
		gomega.Expect(k8sClient.Get(ctx, key, pod)).To(gomega.Succeed())
		return isSuspended(pod)
	}, util.Timeout, util.Interval).Should(gomega.BeFalse())

	gomega.EventuallyWithOffset(1, func() map[string]string {
		gomega.Expect(k8sClient.Get(ctx, key, pod)).To(gomega.Succeed())

		ts := map[string]string{}
		for _, t := range pod.Spec.Tolerations {
			if _, ok := tolerations[t.Key]; ok {
				ts[t.Key] = t.Value
			}
		}

		return ts
	}, util.Timeout, util.Interval).Should(gomega.Equal(tolerations))
}

func isSuspended(p *corev1.Pod) bool {
	for _, t := range p.Spec.Tolerations {
		if t.Key == controller.AdmissionTaintKey && t.Operator == corev1.TolerationOpExists {
			return false
		}
	}
	return true
}

func deleteAllPodsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	err := c.DeleteAllOf(ctx, &corev1.Pod{}, client.InNamespace(ns.Name), client.PropagationPolicy(metav1.DeletePropagationBackground))
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}
