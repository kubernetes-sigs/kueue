/*
Copyright 2025 The Kubernetes Authors.

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

package tase2e

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/testing"
	awtesting "sigs.k8s.io/kueue/pkg/util/testingjobs/appwrapper"
	utiltestingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("TopologyAwareScheduling for AppWrapper", func() {
	var (
		ns           *corev1.Namespace
		topology     *kueuealpha.Topology
		tasFlavor    *kueue.ResourceFlavor
		clusterQueue *kueue.ClusterQueue
		localQueue   *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "e2e-tas-aw-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

		topology = testing.MakeDefaultThreeLevelTopology("datacenter")
		gomega.Expect(k8sClient.Create(ctx, topology)).Should(gomega.Succeed())

		tasFlavor = testing.MakeResourceFlavor("tas-flavor").
			NodeLabel(tasNodeGroupLabel, instanceType).
			TopologyName(topology.Name).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, tasFlavor)).Should(gomega.Succeed())

		clusterQueue = testing.MakeClusterQueue("cluster-queue").
			ResourceGroup(
				*testing.MakeFlavorQuotas(tasFlavor.Name).
					Resource(corev1.ResourceCPU, "1").
					Resource(extraResource, "8").
					Obj(),
			).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, clusterQueue)).Should(gomega.Succeed())
		util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

		localQueue = testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, localQueue)).Should(gomega.Succeed())
		util.ExpectLocalQueuesToBeActive(ctx, k8sClient, localQueue)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteAllAppWrappersInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.When("Creating an AppWrapper", func() {
		ginkgo.It("Should place pods", func() {
			numPods := 4

			aw := awtesting.MakeAppWrapper("appwrapper", ns.Name).
				Component(utiltestingjob.MakeJob("job-0", ns.Name).
					Parallelism(int32(numPods)).
					Completions(int32(numPods)).
					Request(corev1.ResourceCPU, "100m").
					Limit(corev1.ResourceCPU, "100m").
					Request(extraResource, "1").
					Limit(extraResource, "1").
					Suspend(false).
					Image(util.E2eTestSleepImage, []string{"60s"}).
					PodAnnotation(kueuealpha.PodSetPreferredTopologyAnnotation, testing.DefaultRackTopologyLevel).
					SetTypeMeta().Obj()).
				Queue(localQueue.Name).
				Obj()

			gomega.Expect(k8sClient.Create(ctx, aw)).Should(gomega.Succeed())

			ginkgo.By("AppWrapper is unsuspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(aw), aw)).To(gomega.Succeed())
					g.Expect(aw.Spec.Suspend).Should(gomega.BeFalse())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			pods := &corev1.PodList{}
			ginkgo.By("ensure all pods are created", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name))).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.HaveLen(numPods))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("ensure all pods are scheduled", func() {
				listOpts := &client.ListOptions{
					FieldSelector: fields.OneTermNotEqualSelector("spec.nodeName", ""),
				}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name), listOpts)).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.HaveLen(numPods))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})
