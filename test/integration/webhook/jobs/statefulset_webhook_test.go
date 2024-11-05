/*
Copyright 2024 The Kubernetes Authors.

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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	"sigs.k8s.io/kueue/pkg/controller/jobs/statefulset"
	"sigs.k8s.io/kueue/pkg/util/kubeversion"
	testingstatefulset "sigs.k8s.io/kueue/pkg/util/testingjobs/statefulset"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("StatefulSet Webhook", func() {
	var ns *corev1.Namespace

	ginkgo.When("with pod integration enabled", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
		ginkgo.BeforeAll(func() {
			discoveryClient, err := discovery.NewDiscoveryClientForConfig(cfg)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			serverVersionFetcher = kubeversion.NewServerVersionFetcher(discoveryClient)
			err = serverVersionFetcher.FetchServerVersion()
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			fwk.StartManager(ctx, cfg, managerSetup(
				statefulset.SetupWebhook,
				jobframework.WithManageJobsWithoutQueueName(false),
				jobframework.WithKubeServerVersion(serverVersionFetcher),
				jobframework.WithIntegrationOptions(
					corev1.SchemeGroupVersion.WithKind("Pod").String(),
					&configapi.PodIntegrationOptions{},
				),
			))
		})
		ginkgo.AfterAll(func() {
			fwk.StopManager(ctx)
		})
		ginkgo.BeforeEach(func() {
			ns = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "statefulset-",
				},
			}
			gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		})

		ginkgo.When("The queue-name label is set", func() {
			var (
				statefulset *appsv1.StatefulSet
				lookupKey   types.NamespacedName
			)

			ginkgo.BeforeEach(func() {
				statefulset = testingstatefulset.MakeStatefulSet("statefulset-with-queue-name", ns.Name).
					Queue("user-queue").
					Obj()
				lookupKey = client.ObjectKeyFromObject(statefulset)
			})

			ginkgo.It("Should inject queue name, pod group name to pod template labels, and pod group total count to pod template annotations", func() {
				gomega.Expect(k8sClient.Create(ctx, statefulset)).Should(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					createdStatefulSet := &appsv1.StatefulSet{}
					g.Expect(k8sClient.Get(ctx, lookupKey, createdStatefulSet)).Should(gomega.Succeed())
					g.Expect(createdStatefulSet.Spec.Template.Labels[constants.QueueLabel]).
						To(
							gomega.Equal("user-queue"),
							"Queue name should be injected to pod template labels",
						)
					g.Expect(createdStatefulSet.Spec.Template.Labels[pod.GroupNameLabel]).
						To(
							gomega.Equal(jobframework.GetWorkloadNameForOwnerWithGVK(createdStatefulSet.Name, "", appsv1.SchemeGroupVersion.WithKind("StatefulSet"))),
							"Pod group name should be injected to pod template labels",
						)
					g.Expect(createdStatefulSet.Spec.Template.Annotations[pod.GroupTotalCountAnnotation]).
						To(
							gomega.Equal("1"),
							"Pod group total count should be injected to pod template annotations",
						)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				ginkgo.By("Updating the statefulset should not allow to change the queue name", func() {
					statefulsetToUpdate := &appsv1.StatefulSet{}
					gomega.Expect(k8sClient.Get(ctx, lookupKey, statefulsetToUpdate)).Should(gomega.Succeed())
					statefulsetWrapper := &testingstatefulset.StatefulSetWrapper{
						StatefulSet: *statefulsetToUpdate,
					}
					updatedStatefulSet := statefulsetWrapper.
						Queue("another-queue").
						PodTemplateSpecPodGroupNameLabel("another", "another", appsv1.SchemeGroupVersion.WithKind("StatefulSet")).
						Obj()
					gomega.Expect(k8sClient.Update(ctx, updatedStatefulSet)).To(gomega.HaveOccurred())
				})
				ginkgo.By("Updating the statefulset should not allow to change replicas", func() {
					statefulsetToUpdate := &appsv1.StatefulSet{}
					gomega.Expect(k8sClient.Get(ctx, lookupKey, statefulsetToUpdate)).Should(gomega.Succeed())
					statefulsetWrapper := &testingstatefulset.StatefulSetWrapper{
						StatefulSet: *statefulsetToUpdate,
					}
					updatedStatefulSet := statefulsetWrapper.
						Replicas(5).
						PodTemplateSpecPodGroupNameLabel("another", "another", appsv1.SchemeGroupVersion.WithKind("StatefulSet")).
						Obj()
					gomega.Expect(k8sClient.Update(ctx, updatedStatefulSet)).To(gomega.HaveOccurred())
				})
			})
		})

		ginkgo.When("The queue-name label is not set", func() {
			var (
				statefulset *appsv1.StatefulSet
				lookupKey   types.NamespacedName
			)

			ginkgo.BeforeEach(func() {
				statefulset = testingstatefulset.MakeStatefulSet("statefulset-without-queue-name", ns.Name).
					Obj()
				lookupKey = client.ObjectKeyFromObject(statefulset)
			})

			ginkgo.It("Should not inject queue name to pod template labels", func() {
				gomega.Expect(k8sClient.Create(ctx, statefulset)).Should(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					createdStatefulSet := &appsv1.StatefulSet{}
					g.Expect(k8sClient.Get(ctx, lookupKey, createdStatefulSet)).Should(gomega.Succeed())
					g.Expect(createdStatefulSet.Spec.Template.Labels[constants.QueueLabel]).
						To(
							gomega.BeEmpty(),
							"Queue name should not be injected to pod template labels",
						)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})
