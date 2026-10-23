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

package raycluster

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/component-base/featuregate"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobs/raycluster"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingraycluster "sigs.k8s.io/kueue/pkg/util/testingjobs/raycluster"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("RayCluster with partial preemption", ginkgo.Label("job:ray", "area:jobs"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns             *corev1.Namespace
		resourceFlavor *kueue.ResourceFlavor
		clusterQueue   *kueue.ClusterQueue
		localQueue     *kueue.LocalQueue
	)

	ginkgo.BeforeAll(func() {
		features.SetFeatureGatesDuringTest(ginkgo.GinkgoTB(), map[featuregate.Feature]bool{
			features.ElasticJobsViaWorkloadSlices: true,
			features.PartialPreemption:            true,
		})
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup())
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "partial-preemption-")

		resourceFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
		behavioral.MustCreate(ctx, k8sClient, resourceFlavor)

		// "pods" is the binding resource; cpu is generous because every requested resource must be covered.
		clusterQueue = utiltestingapi.MakeClusterQueue("default").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(resourceFlavor.Name).
				Resource(corev1.ResourcePods, "5").
				Resource(corev1.ResourceCPU, "100").
				Obj()).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("default", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, localQueue)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, resourceFlavor, true)
	})

	ginkgo.It("Should shrink the worker group to the reclaim target instead of evicting the RayCluster", func() {
		rayCluster := testingraycluster.MakeCluster("victim", ns.Name).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			SetAnnotation(constants.PartialPreemptionAnnotation, "true").
			Queue(localQueue.Name).
			RequestWorkerGroup(corev1.ResourceCPU, "1").
			FirstWorkerGroupReplicas(4, 1, 4).
			Obj()

		ginkgo.By("admitting the raycluster with 1 head + 4 workers, filling the 5-pod quota")
		behavioral.MustCreate(ctx, k8sClient, rayCluster)
		wl := &behavioral.ExpectWorkloadsInNamespace(ctx, k8sClient, ns.Name, 1)[0]
		gomega.Expect(wl.Spec.PodSets[workersPodSetIdx].MinCount).Should(gomega.Equal(new(int32(1))))
		behavioral.ExpectPodSetAdmittedCount(ctx, k8sClient, wl, workersGroupName, 4)

		ginkgo.By("creating a higher-priority workload that needs 2 pods")
		preemptor := utiltestingapi.MakeWorkload("preemptor", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Priority(1).
			PodSets(*utiltestingapi.MakePodSet("main", 2).Request(corev1.ResourceCPU, "1").Obj()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, preemptor)

		ginkgo.By("the worker group is scaled down to the reclaim target, and the raycluster keeps running")
		gomega.Eventually(func(g gomega.Gomega) {
			got := &rayv1.RayCluster{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rayCluster), got)).Should(gomega.Succeed())
			g.Expect(ptr.Deref(got.Spec.Suspend, false)).Should(gomega.BeFalse())
			g.Expect(ptr.Deref(got.Spec.WorkerGroupSpecs[0].Replicas, 0)).Should(gomega.Equal(int32(2)))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		ginkgo.By("the same workload shrinks in place and the reclaim target is cleared")
		gomega.Eventually(func(g gomega.Gomega) {
			got := &kueue.Workload{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), got)).Should(gomega.Succeed())
			g.Expect(got.Spec.PodSets[workersPodSetIdx].Count).Should(gomega.Equal(int32(2)))
			g.Expect(got.Status.Admission).ShouldNot(gomega.BeNil())
			psa := got.Status.Admission.PodSetAssignments[workersPodSetIdx]
			g.Expect(ptr.Deref(psa.Count, -1)).Should(gomega.Equal(int32(2)))
			g.Expect(psa.ReclaimTargetCount).Should(gomega.BeNil())
			// the workload controller scales the admitted usage proportionally (4 -> 2 workers).
			g.Expect(psa.ResourceUsage).Should(gomega.HaveKeyWithValue(corev1.ResourceCPU, gomega.BeComparableTo(resource.MustParse("2"))))
			g.Expect(psa.ResourceUsage).Should(gomega.HaveKeyWithValue(corev1.ResourcePods, gomega.BeComparableTo(resource.MustParse("2"))))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		ginkgo.By("the preemptor is admitted with the reclaimed quota")
		behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, preemptor)
		behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
	})

	ginkgo.It("Should restore the original worker replicas when the shrunk RayCluster is evicted, and requeue it at the original size", func() {
		rayCluster := testingraycluster.MakeCluster("victim", ns.Name).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			SetAnnotation(constants.PartialPreemptionAnnotation, "true").
			Queue(localQueue.Name).
			RequestWorkerGroup(corev1.ResourceCPU, "1").
			FirstWorkerGroupReplicas(4, 1, 4).
			Obj()

		ginkgo.By("admitting the raycluster with 1 head + 4 workers, filling the 5-pod quota")
		behavioral.MustCreate(ctx, k8sClient, rayCluster)
		wl := &behavioral.ExpectWorkloadsInNamespace(ctx, k8sClient, ns.Name, 1)[0]
		behavioral.ExpectPodSetAdmittedCount(ctx, k8sClient, wl, workersGroupName, 4)

		ginkgo.By("creating a higher-priority workload that needs 2 pods")
		preemptor := utiltestingapi.MakeWorkload("preemptor", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Priority(1).
			PodSets(*utiltestingapi.MakePodSet("main", 2).Request(corev1.ResourceCPU, "1").Obj()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, preemptor)

		ginkgo.By("the worker group is scaled down to 2 and the original and Kueue-set replicas are recorded")
		gomega.Eventually(func(g gomega.Gomega) {
			got := &rayv1.RayCluster{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rayCluster), got)).Should(gomega.Succeed())
			g.Expect(ptr.Deref(got.Spec.Suspend, false)).Should(gomega.BeFalse())
			g.Expect(ptr.Deref(got.Spec.WorkerGroupSpecs[0].Replicas, 0)).Should(gomega.Equal(int32(2)))
			g.Expect(got.Annotations).Should(gomega.HaveKeyWithValue(raycluster.RayClusterShrunkReplicasAnnotation, "4/2"))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, preemptor)

		ginkgo.By("deactivating the raycluster's workload")
		gomega.Eventually(func(g gomega.Gomega) {
			got := &kueue.Workload{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), got)).Should(gomega.Succeed())
			got.Spec.Active = new(false)
			g.Expect(k8sClient.Update(ctx, got)).Should(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		ginkgo.By("the raycluster is suspended, the original replicas are restored and the annotation is removed")
		gomega.Eventually(func(g gomega.Gomega) {
			got := &rayv1.RayCluster{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rayCluster), got)).Should(gomega.Succeed())
			g.Expect(ptr.Deref(got.Spec.Suspend, false)).Should(gomega.BeTrue())
			g.Expect(ptr.Deref(got.Spec.WorkerGroupSpecs[0].Replicas, 0)).Should(gomega.Equal(int32(4)))
			g.Expect(got.Annotations).ShouldNot(gomega.HaveKey(raycluster.RayClusterShrunkReplicasAnnotation))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		ginkgo.By("freeing the quota and reactivating the workload")
		gomega.Expect(k8sClient.Delete(ctx, preemptor)).Should(gomega.Succeed())
		gomega.Eventually(func(g gomega.Gomega) {
			got := &kueue.Workload{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), got)).Should(gomega.Succeed())
			got.Spec.Active = new(true)
			g.Expect(k8sClient.Update(ctx, got)).Should(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		ginkgo.By("the same workload is re-admitted with 4 workers and the raycluster is unsuspended")
		behavioral.ExpectWorkloadPodSet(ctx, k8sClient, client.ObjectKeyFromObject(wl), workersGroupName, 4, new(int32(1)))
		behavioral.ExpectPodSetAdmittedCount(ctx, k8sClient, wl, workersGroupName, 4)
		behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
		gomega.Eventually(func(g gomega.Gomega) {
			got := &rayv1.RayCluster{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rayCluster), got)).Should(gomega.Succeed())
			g.Expect(ptr.Deref(got.Spec.Suspend, true)).Should(gomega.BeFalse())
			g.Expect(ptr.Deref(got.Spec.WorkerGroupSpecs[0].Replicas, 0)).Should(gomega.Equal(int32(4)))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("Should partially admit the worker group down to minReplicas when the quota does not fit all workers", func() {
		rayCluster := testingraycluster.MakeCluster("partial", ns.Name).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			SetAnnotation(constants.PartialPreemptionAnnotation, "true").
			Queue(localQueue.Name).
			RequestWorkerGroup(corev1.ResourceCPU, "1").
			FirstWorkerGroupReplicas(6, 2, 6).
			Obj()

		ginkgo.By("creating a raycluster with 1 head + 6 workers (minReplicas=2) against the 5-pod quota")
		behavioral.MustCreate(ctx, k8sClient, rayCluster)
		// The spec count (6) is not asserted: the job reconciler lowers it to the admitted count
		// right after admission, possibly before this first read.
		wl := &behavioral.ExpectWorkloadsInNamespace(ctx, k8sClient, ns.Name, 1)[0]
		gomega.Expect(wl.Spec.PodSets[workersPodSetIdx].MinCount).Should(gomega.Equal(new(int32(2))))

		ginkgo.By("the workload is admitted with only the 4 workers that fit next to the head")
		behavioral.ExpectPodSetAdmittedCount(ctx, k8sClient, wl, workersGroupName, 4)
		psa := wl.Status.Admission.PodSetAssignments[workersPodSetIdx]
		gomega.Expect(psa.ResourceUsage).Should(gomega.HaveKeyWithValue(corev1.ResourceCPU, gomega.BeComparableTo(resource.MustParse("4"))))
		gomega.Expect(psa.ResourceUsage).Should(gomega.HaveKeyWithValue(corev1.ResourcePods, gomega.BeComparableTo(resource.MustParse("4"))))

		ginkgo.By("the raycluster is unsuspended")
		gomega.Eventually(func(g gomega.Gomega) {
			got := &rayv1.RayCluster{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rayCluster), got)).Should(gomega.Succeed())
			g.Expect(ptr.Deref(got.Spec.Suspend, true)).Should(gomega.BeFalse())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		ginkgo.By("the worker group is lowered to the admitted count")
		gomega.Eventually(func(g gomega.Gomega) {
			got := &rayv1.RayCluster{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rayCluster), got)).Should(gomega.Succeed())
			g.Expect(ptr.Deref(got.Spec.WorkerGroupSpecs[0].Replicas, -1)).Should(gomega.Equal(int32(4)))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		ginkgo.By("the same workload is lowered to the admitted count in place")
		behavioral.ExpectWorkloadPodSet(ctx, k8sClient, client.ObjectKeyFromObject(wl), workersGroupName, 4, new(int32(2)))
		behavioral.ExpectWorkloadsInNamespace(ctx, k8sClient, ns.Name, 1)
		behavioral.ExpectPodSetAdmittedCount(ctx, k8sClient, wl, workersGroupName, 4)
	})
})
