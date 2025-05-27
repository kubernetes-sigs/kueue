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

package tase2e

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	awv1beta2 "github.com/project-codeflare/appwrapper/api/v1beta2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
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
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-tas-aw-")

		topology = testing.MakeDefaultThreeLevelTopology("datacenter")
		util.MustCreate(ctx, k8sClient, topology)

		tasFlavor = testing.MakeResourceFlavor("tas-flavor").
			NodeLabel(tasNodeGroupLabel, instanceType).
			TopologyName(topology.Name).
			Obj()
		util.MustCreate(ctx, k8sClient, tasFlavor)

		clusterQueue = testing.MakeClusterQueue("cluster-queue").
			ResourceGroup(
				*testing.MakeFlavorQuotas(tasFlavor.Name).
					Resource(corev1.ResourceCPU, "1").
					Resource(extraResource, "8").
					Obj(),
			).
			Obj()
		util.MustCreate(ctx, k8sClient, clusterQueue)
		util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

		localQueue = testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		util.MustCreate(ctx, k8sClient, localQueue)
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
				Component(awtesting.Component{
					Template: utiltestingjob.MakeJob("job-0", ns.Name).
						Parallelism(int32(numPods)).
						Completions(int32(numPods)).
						RequestAndLimit(corev1.ResourceCPU, "200m").
						RequestAndLimit(extraResource, "1").
						Suspend(false).
						Image(util.E2eTestAgnHostImage, util.BehaviorExitFast).
						PodAnnotation(kueuealpha.PodSetPreferredTopologyAnnotation, testing.DefaultRackTopologyLevel).
						SetTypeMeta().Obj(),
				}).
				Queue(localQueue.Name).
				Obj()

			util.MustCreate(ctx, k8sClient, aw)

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

		ginkgo.It("Should place pods based on the ranks-ordering", func() {
			numPods := 4
			wrappedJob := utiltestingjob.MakeJob("ranks-job", ns.Name).
				Parallelism(int32(numPods)).
				Completions(int32(numPods)).
				Indexed(true).
				Suspend(false).
				RequestAndLimit(extraResource, "1").
				PodAnnotation(kueuealpha.PodSetRequiredTopologyAnnotation, testing.DefaultBlockTopologyLevel).
				Image(util.E2eTestAgnHostImage, util.BehaviorExitFast).
				SetTypeMeta().
				Obj()
			aw := awtesting.MakeAppWrapper("aw-ranks-job", ns.Name).
				Component(awtesting.Component{Template: wrappedJob}).
				Queue(localQueue.Name).
				Obj()

			util.MustCreate(ctx, k8sClient, aw)

			ginkgo.By("AppWrapper is unsuspended, and has all Pods active and ready", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(aw), aw)).To(gomega.Succeed())
					g.Expect(aw.Spec.Suspend).Should(gomega.BeFalse())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(aw), aw)).To(gomega.Succeed())
					g.Expect(aw.Status.Phase).Should(gomega.Equal(awv1beta2.AppWrapperRunning))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			pods := &corev1.PodList{}
			ginkgo.By("ensure all pods are created and scheduled", func() {
				listOpts := &client.ListOptions{
					FieldSelector: fields.OneTermNotEqualSelector("spec.nodeName", ""),
				}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name), listOpts)).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.HaveLen(numPods))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verify the assignment of pods are as expected with rank-based ordering", func() {
				gomega.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name))).To(gomega.Succeed())
				gotAssignment := make(map[string]string, numPods)
				for _, pod := range pods.Items {
					index := pod.Labels[batchv1.JobCompletionIndexAnnotation]
					gotAssignment[index] = pod.Spec.NodeName
				}
				wantAssignment := map[string]string{
					"0": "kind-worker",
					"1": "kind-worker2",
					"2": "kind-worker3",
					"3": "kind-worker4",
				}
				gomega.Expect(wantAssignment).Should(gomega.BeComparableTo(gotAssignment))
			})
		})
	})
})
