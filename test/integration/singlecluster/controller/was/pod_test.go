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

package was

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/util/wasapi"
	"sigs.k8s.io/kueue/test/util"
)

var podGroupGVK = schema.GroupVersionKind{Group: wasapi.GroupName, Version: "v1alpha2", Kind: wasapi.PodGroupKind}

func makePodGroup(namespace, name string, minCount int64) *unstructured.Unstructured {
	pg := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"namespace": namespace, "name": name},
		"spec":     map[string]any{},
	}}
	pg.SetGroupVersionKind(podGroupGVK)
	gomega.Expect(unstructured.SetNestedField(pg.Object, minCount, "spec", "schedulingPolicy", "gang", "minCount")).To(gomega.Succeed())
	return pg
}

var _ = ginkgo.Describe("Plain Pods with a standard PodGroup reference", ginkgo.Label("job:pod", "feature:was"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns           *corev1.Namespace
		flavor       *kueue.ResourceFlavor
		clusterQueue *kueue.ClusterQueue
		localQueue   *kueue.LocalQueue
	)

	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup())
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "was-pod-")
		flavor = utiltestingapi.MakeResourceFlavor("was-flavor-" + ns.Name).Obj()
		util.MustCreate(ctx, k8sClient, flavor)
		clusterQueue = utiltestingapi.MakeClusterQueue("was-cq-" + ns.Name).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).Resource(corev1.ResourceCPU, "10").Obj()).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)
		localQueue = utiltestingapi.MakeLocalQueue("main", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteAllPodsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
	})

	ginkgo.It("Should admit a group defined only via schedulingGroup.podGroupName once the PodGroup exists", func() {
		ginkgo.By("creating the standalone PodGroup", func() {
			util.MustCreate(ctx, k8sClient, makePodGroup(ns.Name, "was-group", 2))
		})

		basePod := testingpod.MakePod("member", ns.Name).
			Queue(localQueue.Name).
			SchedulingGroupPodGroupName("was-group").
			Request(corev1.ResourceCPU, "1")
		pod1 := basePod.Clone().Name("member-1").Obj()
		pod2 := basePod.Clone().Name("member-2").Obj()

		ginkgo.By("creating the member pods", func() {
			util.MustCreate(ctx, k8sClient, pod1)
			util.MustCreate(ctx, k8sClient, pod2)
		})

		ginkgo.By("checking both pods are ungated", func() {
			for _, p := range []*corev1.Pod{pod1, pod2} {
				gomega.Eventually(func(g gomega.Gomega) {
					got := &corev1.Pod{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(p), got)).To(gomega.Succeed())
					g.Expect(got.Spec.SchedulingGates).Should(gomega.BeEmpty())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			}
		})
	})

	ginkgo.It("Should requeue and admit pods once their referenced PodGroup appears", func() {
		basePod := testingpod.MakePod("member", ns.Name).
			Queue(localQueue.Name).
			SchedulingGroupPodGroupName("late-group").
			Request(corev1.ResourceCPU, "1")
		pod1 := basePod.Clone().Name("member-1").Obj()
		pod2 := basePod.Clone().Name("member-2").Obj()

		ginkgo.By("creating the member pods before their PodGroup exists", func() {
			util.MustCreate(ctx, k8sClient, pod1)
			util.MustCreate(ctx, k8sClient, pod2)
		})

		ginkgo.By("checking the pods remain gated", func() {
			gomega.Consistently(func(g gomega.Gomega) {
				got := &corev1.Pod{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod1), got)).To(gomega.Succeed())
				g.Expect(got.Spec.SchedulingGates).ShouldNot(gomega.BeEmpty())
			}, util.ConsistentDuration, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("creating the standalone PodGroup", func() {
			util.MustCreate(ctx, k8sClient, makePodGroup(ns.Name, "late-group", 2))
		})

		ginkgo.By("checking both pods are ungated", func() {
			for _, p := range []*corev1.Pod{pod1, pod2} {
				gomega.Eventually(func(g gomega.Gomega) {
					got := &corev1.Pod{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(p), got)).To(gomega.Succeed())
					g.Expect(got.Spec.SchedulingGates).Should(gomega.BeEmpty())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			}
		})
	})

	ginkgo.It("Should reject a pod with conflicting standard and legacy group identities", func() {
		p := testingpod.MakePod("conflicting", ns.Name).
			Queue(localQueue.Name).
			SchedulingGroupPodGroupName("standard-group").
			GroupNameLabel("legacy-group").
			GroupTotalCount("2").
			Obj()
		err := k8sClient.Create(ctx, p)
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("conflicts with"))
	})
})
