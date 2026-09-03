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

package dra

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util"
)

// Probe for: handleDRA runs AdjustResources on the reconciler's own object
// (not a copy); the deactivation path then updates the full object, persisting
// the adjusted resource values into the user's Workload spec.
const probeExtResource = "probe.example.com/gpu"

var _ = ginkgo.Describe("Workload spec on the DRA deactivation path", func() {
	var (
		ns             *corev1.Namespace
		resourceFlavor *kueue.ResourceFlavor
		clusterQueue   *kueue.ClusterQueue
		localQueue     *kueue.LocalQueue
		deviceClass    *resourcev1.DeviceClass
	)

	ginkgo.BeforeEach(func() {
		fwk.StartManager(ctx, cfg, managerSetup(nil))

		ns = utiltesting.MakeNamespaceWithGenerateName("dra-writeback-")
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
		deviceClass = utiltesting.MakeDeviceClass("").GeneratedName("gpu-wb-").
			ExtendedResourceName(probeExtResource).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, deviceClass)).To(gomega.Succeed())
		resourceFlavor = utiltestingapi.MakeResourceFlavor("").GeneratedName("rf-").Obj()
		gomega.Expect(k8sClient.Create(ctx, resourceFlavor)).To(gomega.Succeed())
		clusterQueue = utiltestingapi.MakeClusterQueue("").GeneratedName("cq-writeback-").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(resourceFlavor.Name).
					Resource(corev1.ResourceName(probeExtResource), "4").
					Resource(corev1.ResourceCPU, "1").
					Obj(),
			).Obj()
		gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())
		util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)
		localQueue = utiltestingapi.MakeLocalQueue("test-lq", ns.Name).
			ClusterQueue(clusterQueue.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, resourceFlavor, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, deviceClass, true)
		fwk.StopManager(ctx)
	})

	ginkgo.It("keeps the user-written spec when deactivating a pending DRA workload", func() {
		// The container declares only a cpu limit; the request slot is the
		// canary — AdjustResources fills it in memory, and it must never be
		// persisted.
		wl := utiltestingapi.MakeWorkload("writeback-probe", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Limit(corev1.ResourceCPU, "2").
			Request(corev1.ResourceName(probeExtResource), "1").
			Obj()
		gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

		ginkgo.By("waiting for the workload to be processed as pending", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				read := kueue.Workload{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &read)).To(gomega.Succeed())
				g.Expect(read.Status.Conditions).NotTo(gomega.BeEmpty())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("marking the workload as a deactivation target", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				read := kueue.Workload{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &read)).To(gomega.Succeed())
				apimeta.SetStatusCondition(&read.Status.Conditions, metav1.Condition{
					Type:    kueue.WorkloadDeactivationTarget,
					Status:  metav1.ConditionTrue,
					Reason:  "ByTest",
					Message: "deactivated by the probe",
				})
				g.Expect(k8sClient.Status().Update(ctx, &read)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("waiting for the deactivation to be executed", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				read := kueue.Workload{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &read)).To(gomega.Succeed())
				g.Expect(read.Spec.Active).NotTo(gomega.BeNil())
				g.Expect(*read.Spec.Active).To(gomega.BeFalse())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verifying the persisted spec still has no cpu request", func() {
			read := kueue.Workload{}
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &read)).To(gomega.Succeed())
			requests := read.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Requests
			gomega.Expect(requests).NotTo(gomega.HaveKey(corev1.ResourceCPU),
				"the adjusted cpu request must not be persisted into the user's spec; got %v", requests)
			gomega.Expect(requests[corev1.ResourceName(probeExtResource)]).To(gomega.Equal(resource.MustParse("1")))
		})
	})
})
