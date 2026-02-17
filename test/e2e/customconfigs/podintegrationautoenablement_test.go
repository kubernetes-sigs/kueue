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

package customconfigse2e

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobs/statefulset"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	testingsts "sigs.k8s.io/kueue/pkg/util/testingjobs/statefulset"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Auto-Enablement of Pod Integration for Pod-Dependent Frameworks", ginkgo.Ordered, func() {
	var (
		ns           *corev1.Namespace
		defaultRf    *kueue.ResourceFlavor
		localQueue   *kueue.LocalQueue
		clusterQueue *kueue.ClusterQueue
	)

	ginkgo.BeforeAll(func() {
		util.UpdateKueueConfigurationAndRestart(ctx, k8sClient, defaultKueueCfg, kindClusterName, func(cfg *config.Configuration) {
			cfg.Integrations.Frameworks = []string{"statefulset"}
		})

		currentCfg := util.GetKueueConfiguration(ctx, k8sClient)
		frameworkSet := make(map[string]bool)
		for _, framework := range currentCfg.Integrations.Frameworks {
			frameworkSet[framework] = true
		}
		gomega.Expect(frameworkSet["statefulset"]).To(gomega.BeTrue())
		gomega.Expect(frameworkSet["pod"]).To(gomega.BeFalse())
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-disabled-pod-")
		defaultRf = utiltestingapi.MakeResourceFlavor("default").Obj()
		util.MustCreate(ctx, k8sClient, defaultRf)
		clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(defaultRf.Name).
					Resource(corev1.ResourceCPU, "2").
					Resource(corev1.ResourceMemory, "2G").Obj()).Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("main", ns.Name).ClusterQueue("cluster-queue").Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, clusterQueue, true, util.LongTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, defaultRf, true, util.LongTimeout)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.It("should successfully run StatefulSets when pod integration is auto-enabled", func() {
		testSts := testingsts.MakeStatefulSet("test-sts", ns.Name).
			Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
			Replicas(2).
			RequestAndLimit(corev1.ResourceCPU, "200m").
			TerminationGracePeriod(1).
			Queue(localQueue.Name).
			Obj()
		util.MustCreate(ctx, k8sClient, testSts)

		ginkgo.By("verifying StatefulSet workload is created and admitted", func() {
			wlLookupKey := types.NamespacedName{Name: statefulset.GetWorkloadName(testSts.Name), Namespace: ns.Name}
			createdWorkload := &kueue.Workload{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verifying StatefulSet pods are managed correctly", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				pods := &corev1.PodList{}
				g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name),
					client.MatchingLabels(testSts.Spec.Selector.MatchLabels))).To(gomega.Succeed())
				g.Expect(pods.Items).To(gomega.HaveLen(2))
				for _, pod := range pods.Items {
					g.Expect(pod.Annotations).To(gomega.HaveKey(podconstants.SuspendedByParentAnnotation))
				}
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verifying StatefulSet becomes ready", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdStatefulSet := &appsv1.StatefulSet{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(testSts), createdStatefulSet)).To(gomega.Succeed())
				g.Expect(createdStatefulSet.Status.ReadyReplicas).To(gomega.Equal(int32(2)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("should not manage pods without queue names regardless of auto-enablement", func() {
		testPod := testingpod.MakePod("plain-pod", ns.Name).
			Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
			RequestAndLimit(corev1.ResourceCPU, "200m").
			Obj()
		util.MustCreate(ctx, k8sClient, testPod)

		ginkgo.By("verifying pod is not managed by Kueue", func() {
			gomega.Consistently(func(g gomega.Gomega) {
				createdPod := &corev1.Pod{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(testPod), createdPod)).To(gomega.Succeed())
				g.Expect(createdPod.Spec.SchedulingGates).To(gomega.BeEmpty())
			}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
		})

		ginkgo.By("verifying no workload is created for the pod", func() {
			gomega.Consistently(func(g gomega.Gomega) {
				workloads := &kueue.WorkloadList{}
				g.Expect(k8sClient.List(ctx, workloads, client.InNamespace(ns.Name))).To(gomega.Succeed())
				for _, wl := range workloads.Items {
					g.Expect(wl.Name).NotTo(gomega.ContainSubstring("plain-pod"))
				}
			}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
		})
	})
})
