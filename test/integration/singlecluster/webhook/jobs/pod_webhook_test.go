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

package jobs

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"

	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podcontroller "sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/util/kubeversion"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Pod Webhook", func() {
	var ns *corev1.Namespace

	ginkgo.When("with manageJobsWithoutQueueName disabled", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
		ginkgo.BeforeAll(func() {
			discoveryClient, err := discovery.NewDiscoveryClientForConfig(cfg)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			serverVersionFetcher = kubeversion.NewServerVersionFetcher(discoveryClient)
			err = serverVersionFetcher.FetchServerVersion()
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			nsSelector := &metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{
						Key:      corev1.LabelMetadataName,
						Operator: metav1.LabelSelectorOpNotIn,
						Values:   []string{"kube-system", "kueue-system"},
					},
				},
			}
			mjnsSelector, err := metav1.LabelSelectorAsSelector(nsSelector)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			fwk.StartManager(ctx, cfg, managerSetup(
				podcontroller.SetupWebhook,
				jobframework.WithManageJobsWithoutQueueName(false),
				jobframework.WithManagedJobsNamespaceSelector(mjnsSelector),
				jobframework.WithKubeServerVersion(serverVersionFetcher),
			))
		})
		ginkgo.BeforeEach(func() {
			ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "pod-")
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		})
		ginkgo.AfterAll(func() {
			fwk.StopManager(ctx)
		})

		ginkgo.When("The queue-name label is set", func() {
			var (
				pod       *corev1.Pod
				lookupKey types.NamespacedName
			)

			ginkgo.BeforeEach(func() {
				pod = testingpod.MakePod("pod-with-queue-name", ns.Name).Queue("user-queue").Obj()
				lookupKey = types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}
			})

			ginkgo.It("Should inject scheduling gate, 'managed' label and finalizer into created pod", func() {
				util.MustCreate(ctx, k8sClient, pod)

				createdPod := &corev1.Pod{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, lookupKey, createdPod)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Expect(createdPod.Spec.SchedulingGates).To(
					gomega.ContainElement(corev1.PodSchedulingGate{Name: podconstants.SchedulingGateName}),
					"Pod should have scheduling gate",
				)

				gomega.Expect(createdPod.Labels).To(
					gomega.HaveKeyWithValue(constants.ManagedByKueueLabelKey, constants.ManagedByKueueLabelValue),
					"Pod should have the label",
				)

				gomega.Expect(createdPod.Finalizers).To(gomega.ContainElement(constants.ManagedByKueueLabelKey),
					"Pod should have finalizer set")
			})

			ginkgo.It("Should skip a Pod created in the forbidden 'kube-system' namespace", func() {
				pod.Namespace = "kube-system"
				util.MustCreate(ctx, k8sClient, pod)

				lookupKey := types.NamespacedName{Name: pod.Name, Namespace: "kube-system"}
				createdPod := &corev1.Pod{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, lookupKey, createdPod)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Expect(createdPod.Spec.SchedulingGates).NotTo(
					gomega.ContainElement(corev1.PodSchedulingGate{Name: podconstants.SchedulingGateName}),
					"Pod shouldn't have scheduling gate",
				)

				gomega.Expect(createdPod.Labels).NotTo(
					gomega.HaveKeyWithValue(constants.ManagedByKueueLabelKey, constants.ManagedByKueueLabelValue),
					"Pod shouldn't have the label",
				)

				gomega.Expect(createdPod.Finalizers).NotTo(gomega.ContainElement(constants.ManagedByKueueLabelKey),
					"Pod shouldn't have finalizer set")
			})
		})

		ginkgo.When("The queue-name label is not set", func() {
			var (
				pod       *corev1.Pod
				lookupKey types.NamespacedName
			)

			ginkgo.BeforeEach(func() {
				pod = testingpod.MakePod("pod-with-queue-name", ns.Name).Obj()
				lookupKey = types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}
			})

			ginkgo.It("Should not inject scheduling gate, 'managed' label and finalizer into created pod", func() {
				util.MustCreate(ctx, k8sClient, pod)

				createdPod := &corev1.Pod{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, lookupKey, createdPod)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Expect(createdPod.Spec.SchedulingGates).NotTo(
					gomega.ContainElement(corev1.PodSchedulingGate{Name: podconstants.SchedulingGateName}),
					"Pod shouldn't have scheduling gate",
				)

				gomega.Expect(createdPod.Labels).NotTo(
					gomega.HaveKeyWithValue(constants.ManagedByKueueLabelKey, constants.ManagedByKueueLabelValue),
					"Pod shouldn't have the label",
				)

				gomega.Expect(createdPod.Finalizers).NotTo(gomega.ContainElement(constants.ManagedByKueueLabelKey),
					"Pod shouldn't have finalizer set")
			})
		})
	})

	ginkgo.When("with manageJobsWithoutQueueName enabled", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
		ginkgo.BeforeAll(func() {
			discoveryClient, err := discovery.NewDiscoveryClientForConfig(cfg)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			serverVersionFetcher = kubeversion.NewServerVersionFetcher(discoveryClient)
			err = serverVersionFetcher.FetchServerVersion()
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			nsSelector := &metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{
						Key:      corev1.LabelMetadataName,
						Operator: metav1.LabelSelectorOpNotIn,
						Values:   []string{"kube-system", "kueue-system"},
					},
				},
			}
			mjnsSelector, err := metav1.LabelSelectorAsSelector(nsSelector)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			fwk.StartManager(ctx, cfg, managerSetup(
				podcontroller.SetupWebhook,
				jobframework.WithManageJobsWithoutQueueName(true),
				jobframework.WithManagedJobsNamespaceSelector(mjnsSelector),
				jobframework.WithKubeServerVersion(serverVersionFetcher),
			))
		})
		ginkgo.BeforeEach(func() {
			ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "pod-")
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		})
		ginkgo.AfterAll(func() {
			fwk.StopManager(ctx)
		})

		ginkgo.When("The queue-name label is not set", func() {
			var pod *corev1.Pod

			ginkgo.BeforeEach(func() {
				pod = testingpod.MakePod("pod-integration", ns.Name).Obj()
			})

			ginkgo.It("Should inject scheduling gate, 'managed' label and finalizer into created pod", func() {
				util.MustCreate(ctx, k8sClient, pod)

				lookupKey := types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}
				createdPod := &corev1.Pod{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, lookupKey, createdPod)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Expect(createdPod.Spec.SchedulingGates).To(
					gomega.ContainElement(corev1.PodSchedulingGate{Name: podconstants.SchedulingGateName}),
					"Pod should have scheduling gate",
				)

				gomega.Expect(createdPod.Labels).To(
					gomega.HaveKeyWithValue(constants.ManagedByKueueLabelKey, constants.ManagedByKueueLabelValue),
					"Pod should have the label",
				)

				gomega.Expect(createdPod.Finalizers).To(gomega.ContainElement(constants.ManagedByKueueLabelKey),
					"Pod should have finalizer set")
			})

			ginkgo.It("Should skip a Pod created in the forbidden 'kube-system' namespace", func() {
				pod.Namespace = "kube-system"
				util.MustCreate(ctx, k8sClient, pod)

				lookupKey := types.NamespacedName{Name: pod.Name, Namespace: "kube-system"}
				createdPod := &corev1.Pod{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, lookupKey, createdPod)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Expect(createdPod.Spec.SchedulingGates).NotTo(
					gomega.ContainElement(corev1.PodSchedulingGate{Name: podconstants.SchedulingGateName}),
					"Pod shouldn't have scheduling gate",
				)

				gomega.Expect(createdPod.Labels).NotTo(
					gomega.HaveKeyWithValue(constants.ManagedByKueueLabelKey, constants.ManagedByKueueLabelValue),
					"Pod shouldn't have the label",
				)

				gomega.Expect(createdPod.Finalizers).NotTo(gomega.ContainElement(constants.ManagedByKueueLabelKey),
					"Pod shouldn't have finalizer set")
			})
		})
	})
})
