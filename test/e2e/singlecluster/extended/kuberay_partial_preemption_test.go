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

package extended

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingraycluster "sigs.k8s.io/kueue/pkg/util/testingjobs/raycluster"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/e2e"
)

// KEP-975: Partial preemption.
var _ = ginkgo.Describe("KubeRay partial preemption", ginkgo.Label("area:singlecluster", "feature:kuberay"), func() {
	const workers kueue.PodSetReference = "workers-group-0"
	var (
		ns      *corev1.Namespace
		rf      *kueue.ResourceFlavor
		cq      *kueue.ClusterQueue
		lq      *kueue.LocalQueue
		highWPC *kueue.WorkloadPriorityClass
	)

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "kuberay-partial-preemption-e2e-")
		rf = utiltestingapi.MakeResourceFlavor("kuberay-pp-rf-" + ns.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, rf)
		// The head and 4 workers fill the 5-pod quota.
		cq = utiltestingapi.MakeClusterQueue("kuberay-pp-cq-" + ns.Name).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).
				Resource(corev1.ResourcePods, "5").
				Resource(corev1.ResourceCPU, "10").
				Resource(corev1.ResourceMemory, "8Gi").
				Obj()).
			Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
			Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)
		lq = utiltestingapi.MakeLocalQueue("kuberay-pp-lq-"+ns.Name, ns.Name).ClusterQueue(cq.Name).Obj()
		behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
		highWPC = utiltestingapi.MakeWorkloadPriorityClass("kuberay-pp-high-" + ns.Name).PriorityValue(1000).Obj()
		behavioral.MustCreate(ctx, k8sClient, highWPC)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, highWPC, true)
		behavioral.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.It("Should shrink a RayCluster's workers to admit a preemptor and grow back", ginkgo.Label("shard:kuberay-a"), func() {
		kuberayTestImage := e2e.GetKuberayTestImage()
		rayCluster := testingraycluster.MakeCluster("raycluster-partial-preemption", ns.Name).
			Suspend(true).
			Queue(lq.Name).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			SetAnnotation(constants.PartialPreemptionAnnotation, "true").
			NodeAnnotation(rayv1.WorkerNode, constants.PodSetPartialPreemptionMinRetainedCountAnnotation, "1").
			FirstWorkerGroupReplicas(4, 1, 4).
			Request(rayv1.HeadNode, corev1.ResourceCPU, "500m").
			RayStartParam(rayv1.HeadNode, "object-store-memory", objectStoreMemory).
			Request(rayv1.WorkerNode, corev1.ResourceCPU, "100m").
			RayStartParam(rayv1.WorkerNode, "object-store-memory", objectStoreMemory).
			Image(rayv1.HeadNode, kuberayTestImage, []string{}).
			Image(rayv1.WorkerNode, kuberayTestImage, []string{}).
			TerminationGracePeriod(1).
			Obj()
		rayClusterKey := client.ObjectKeyFromObject(rayCluster)

		var victim *kueue.Workload
		var head *corev1.Pod
		ginkgo.By("Running the RayCluster with its head and 4 workers", func() {
			behavioral.MustCreate(ctx, k8sClient, rayCluster)
			victim = behavioral.ExpectSingleActiveWorkload(ctx, k8sClient, ns.Name)
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, rayClusterKey, rayCluster)).To(gomega.Succeed())
				g.Expect(apimeta.IsStatusConditionTrue(rayCluster.Status.Conditions, string(rayv1.HeadPodReady))).To(gomega.BeTrue())
			}, behavioral.VeryLongTimeout, behavioral.Interval).Should(gomega.Succeed())
			e2e.ExpectRayClusterWorkerPods(ctx, k8sClient, rayClusterKey, 4, 0)
			var err error
			head, err = e2e.GetRayClusterHeadPod(ctx, k8sClient, rayClusterKey)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		preemptor := testingjob.MakeJob("preemptor", ns.Name).
			Queue(kueue.LocalQueueName(lq.Name)).
			WorkloadPriorityClass(highWPC.Name).
			Parallelism(2).
			Completions(2).
			RequestAndLimit(corev1.ResourceCPU, "100m").
			Image(e2e.GetAgnHostImage(), e2e.BehaviorWaitForDeletion).
			Obj()

		ginkgo.By("Shrinking the workers to 2 to admit the preemptor", func() {
			behavioral.MustCreate(ctx, k8sClient, preemptor)
			behavioral.ExpectPodSetAdmittedCountWithTimeout(ctx, k8sClient, victim, workers, 2, behavioral.LongTimeout)
			// KubeRay recreates the evicted workers; Kueue keeps them gated.
			e2e.ExpectRayClusterWorkerPods(ctx, k8sClient, rayClusterKey, 2, 2)
			behavioral.ExpectWorkloadsToBeAdmittedByKeysWithTimeout(ctx, k8sClient, behavioral.LongTimeout,
				client.ObjectKey{Namespace: ns.Name, Name: workloadjob.GetWorkloadNameForJob(preemptor.Name, preemptor.UID)})
		})

		ginkgo.By("Growing the workers back, with the same head, once the preemptor is deleted", func() {
			gomega.Expect(k8sClient.Delete(ctx, preemptor, client.PropagationPolicy("Background"))).To(gomega.Succeed())
			e2e.ExpectRayClusterWorkerPods(ctx, k8sClient, rayClusterKey, 4, 0)
			gotHead, err := e2e.GetRayClusterHeadPod(ctx, k8sClient, rayClusterKey)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(gotHead.UID).To(gomega.Equal(head.UID))
		})
	})
})
