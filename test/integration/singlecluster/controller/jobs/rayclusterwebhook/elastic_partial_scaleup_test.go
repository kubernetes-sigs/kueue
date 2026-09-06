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

package rayclusterwebhook

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingraycluster "sigs.k8s.io/kueue/pkg/util/testingjobs/raycluster"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadfinish "sigs.k8s.io/kueue/pkg/workload/finish"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/util"
)

// Regression coverage for KEP-12100 partial replica scale-up with production wiring: the
// RayCluster reconciler, the Workload webhooks, the scheduler and the API server all run together
// in the same process (exactly how a deployed kueue-manager is wired, modulo container packaging).
// This is the scenario a production user hits when they enable the feature for an elastic
// RayCluster. See https://github.com/kubernetes-sigs/kueue/issues/15249.

// podSetForWorkerGroup returns the Workload podSet that corresponds to a RayCluster worker
// group, resolving the name the same way the RayCluster integration does.
func podSetForWorkerGroup(wl *kueue.Workload, groupName string) *kueue.PodSet {
	podSetName := kueue.NewPodSetReference(groupName)
	for i := range wl.Spec.PodSets {
		if wl.Spec.PodSets[i].Name == podSetName {
			return &wl.Spec.PodSets[i]
		}
	}
	return nil
}

// expectWorkloadAdmitted waits until the given Workload holds a quota reservation and is admitted.
func expectWorkloadAdmitted(obj client.Object) {
	gomega.Eventually(func(g gomega.Gomega) {
		var wl kueue.Workload
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(obj), &wl)).Should(gomega.Succeed())
		g.Expect(workload.IsAdmitted(&wl)).Should(gomega.BeTrue())
	}, util.Timeout, util.Interval).Should(gomega.Succeed())
}

var _ = ginkgo.Describe("KEP-12100 partial scale-up RayCluster end to end (PartialAdmission on)", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns             *corev1.Namespace
		resourceFlavor *kueue.ResourceFlavor
		clusterQueue   *kueue.ClusterQueue
		localQueue     *kueue.LocalQueue
	)

	ginkgo.BeforeAll(func() {
		gomega.Expect(utilfeature.DefaultMutableFeatureGate.SetFromMap(map[string]bool{
			string(features.ElasticJobsViaWorkloadSlices):                          true,
			string(features.ElasticJobsViaWorkloadSlicesWithPartialReplicaScaleUp): true,
			string(features.PartialAdmission):                                      true,
		})).Should(gomega.Succeed())
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup())
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-")

		resourceFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
		util.MustCreate(ctx, k8sClient, resourceFlavor)

		clusterQueue = utiltestingapi.MakeClusterQueue("default").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(resourceFlavor.Name).Resource(corev1.ResourceCPU, "6").Obj()).
			Obj()
		util.MustCreate(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("default", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		util.MustCreate(ctx, k8sClient, localQueue)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, resourceFlavor, true)
	})

	ginkgo.It("creates a Workload for the elastic partial scale-up RayCluster and keeps the partial minCount", func() {
		testRayCluster := testingraycluster.MakeCluster("partial-scale-up", ns.Name).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			SetAnnotation(constants.ElasticJobScaleUpStrategyAnnotationKey, constants.ElasticJobScaleUpStrategyPartial).
			Queue(localQueue.Name).
			Request(rayv1.HeadNode, corev1.ResourceCPU, "1").
			RequestWorkerGroup(corev1.ResourceCPU, "1").
			Obj()

		ginkgo.By("creating the elastic RayCluster with the partial scale-up strategy")
		util.MustCreate(ctx, k8sClient, testRayCluster)

		ginkgo.By("a Workload is created for the RayCluster")
		var wl kueue.Workload
		gomega.Eventually(func(g gomega.Gomega) {
			workloads := &kueue.WorkloadList{}
			g.Expect(k8sClient.List(ctx, workloads, client.InNamespace(ns.Name))).Should(gomega.Succeed())
			g.Expect(workloads.Items).Should(gomega.HaveLen(1))
			wl = workloads.Items[0]
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("the Workload is elastic and the worker podSet keeps the partial minCount")
		gomega.Expect(wl.Annotations[workloadslicing.EnabledAnnotationKey]).Should(gomega.Equal(workloadslicing.EnabledAnnotationValue))
		workerPS := podSetForWorkerGroup(&wl, "workers-group-0")
		gomega.Expect(workerPS).ShouldNot(gomega.BeNil())
		gomega.Expect(workerPS.MinCount).ShouldNot(gomega.BeNil())
		gomega.Expect(*workerPS.MinCount).Should(gomega.Equal(workerPS.Count))
		ginkgo.By("the Workload is admitted by the scheduler")
		expectWorkloadAdmitted(&wl)
	})

	ginkgo.It("keeps minCount on every worker podSet when the RayCluster has multiple worker groups", func() {
		testRayCluster := testingraycluster.MakeCluster("partial-scale-up-multi", ns.Name).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			SetAnnotation(constants.ElasticJobScaleUpStrategyAnnotationKey, constants.ElasticJobScaleUpStrategyPartial).
			Queue(localQueue.Name).
			Request(rayv1.HeadNode, corev1.ResourceCPU, "1").
			RequestWorkerGroup(corev1.ResourceCPU, "1").
			Obj()

		ginkgo.By("adding a second worker group")
		secondWorker := testRayCluster.Spec.WorkerGroupSpecs[0].DeepCopy()
		secondWorker.GroupName = "workers-group-1"
		secondWorkerReplicas := int32(2)
		secondWorker.Replicas = &secondWorkerReplicas
		testRayCluster.Spec.WorkerGroupSpecs = append(testRayCluster.Spec.WorkerGroupSpecs, *secondWorker)

		ginkgo.By("creating the elastic RayCluster with the partial scale-up strategy")
		util.MustCreate(ctx, k8sClient, testRayCluster)

		ginkgo.By("a Workload with one podSet per group is created")
		var wl kueue.Workload
		gomega.Eventually(func(g gomega.Gomega) {
			workloads := &kueue.WorkloadList{}
			g.Expect(k8sClient.List(ctx, workloads, client.InNamespace(ns.Name))).Should(gomega.Succeed())
			g.Expect(workloads.Items).Should(gomega.HaveLen(1))
			wl = workloads.Items[0]
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("every worker podSet keeps its partial minCount")
		for _, groupName := range []string{"workers-group-0", "workers-group-1"} {
			workerPS := podSetForWorkerGroup(&wl, groupName)
			gomega.Expect(workerPS).ShouldNot(gomega.BeNil())
			gomega.Expect(workerPS.MinCount).ShouldNot(gomega.BeNil())
			gomega.Expect(*workerPS.MinCount).Should(gomega.Equal(workerPS.Count))
		}
		ginkgo.By("the Workload is admitted by the scheduler")
		expectWorkloadAdmitted(&wl)
	})

	ginkgo.It("scales an admitted elastic RayCluster up through a partial scale-up probe slice", func() {
		testRayCluster := testingraycluster.MakeCluster("partial-scale-up-probe", ns.Name).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			SetAnnotation(constants.ElasticJobScaleUpStrategyAnnotationKey, constants.ElasticJobScaleUpStrategyPartial).
			Queue(localQueue.Name).
			Request(rayv1.HeadNode, corev1.ResourceCPU, "1").
			RequestWorkerGroup(corev1.ResourceCPU, "1").
			ScaleFirstWorkerGroup(1).
			Obj()

		ginkgo.By("creating the elastic RayCluster with one worker replica")
		util.MustCreate(ctx, k8sClient, testRayCluster)

		ginkgo.By("the initial Workload slice is admitted")
		var firstWl kueue.Workload
		gomega.Eventually(func(g gomega.Gomega) {
			workloads := &kueue.WorkloadList{}
			g.Expect(k8sClient.List(ctx, workloads, client.InNamespace(ns.Name))).Should(gomega.Succeed())
			g.Expect(workloads.Items).Should(gomega.HaveLen(1))
			firstWl = workloads.Items[0]
			g.Expect(workload.IsAdmitted(&firstWl)).Should(gomega.BeTrue())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("scaling the worker replicas from 1 to 4")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(testRayCluster), testRayCluster)).Should(gomega.Succeed())
			testRayCluster.Spec.WorkerGroupSpecs[0].Replicas = ptr.To[int32](4)
			g.Expect(k8sClient.Update(ctx, testRayCluster)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("the probe slice replaces the initial slice and is admitted")
		var newWl kueue.Workload
		gomega.Eventually(func(g gomega.Gomega) {
			workloads := &kueue.WorkloadList{}
			g.Expect(k8sClient.List(ctx, workloads, client.InNamespace(ns.Name))).Should(gomega.Succeed())
			g.Expect(workloads.Items).Should(gomega.HaveLen(2))

			finishedOld := false
			admittedNew := false
			for i := range workloads.Items {
				slice := &workloads.Items[i]
				if workloadfinish.IsFinished(slice) {
					finishedOld = true
					g.Expect(slice.Name).Should(gomega.Equal(firstWl.Name))
				} else {
					admittedNew = true
					g.Expect(workload.IsAdmitted(slice)).Should(gomega.BeTrue())
					newWl = *slice
				}
			}
			g.Expect(finishedOld && admittedNew).Should(gomega.BeTrue())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("the probe slice is linked to the initial slice and carries the partial minCount")
		gomega.Expect(newWl.Annotations[workloadslicing.WorkloadSliceReplacementFor]).Should(gomega.Equal(string(workload.Key(&firstWl))))
		gomega.Expect(newWl.Annotations[kueue.WorkloadSliceNameAnnotation]).ShouldNot(gomega.BeEmpty())
		workerPS := podSetForWorkerGroup(&newWl, "workers-group-0")
		gomega.Expect(workerPS).ShouldNot(gomega.BeNil())
		gomega.Expect(workerPS.Count).Should(gomega.Equal(int32(4)))
		gomega.Expect(workerPS.MinCount).ShouldNot(gomega.BeNil())
		gomega.Expect(*workerPS.MinCount).Should(gomega.Equal(int32(2)))
	})

	ginkgo.It("control: a plain elastic RayCluster (no partial strategy) still gets its Workload created", func() {
		testRayCluster := testingraycluster.MakeCluster("plain-elastic", ns.Name).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			Queue(localQueue.Name).
			Request(rayv1.HeadNode, corev1.ResourceCPU, "1").
			RequestWorkerGroup(corev1.ResourceCPU, "1").
			Obj()

		ginkgo.By("creating the elastic RayCluster without the partial scale-up strategy")
		util.MustCreate(ctx, k8sClient, testRayCluster)

		ginkgo.By("the Workload is created and carries no minCount")
		var wl kueue.Workload
		gomega.Eventually(func(g gomega.Gomega) {
			workloads := &kueue.WorkloadList{}
			g.Expect(k8sClient.List(ctx, workloads, client.InNamespace(ns.Name))).Should(gomega.Succeed())
			g.Expect(workloads.Items).Should(gomega.HaveLen(1))
			wl = workloads.Items[0]
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		gomega.Expect(wl.Annotations[workloadslicing.EnabledAnnotationKey]).Should(gomega.Equal(workloadslicing.EnabledAnnotationValue))
		for i := range wl.Spec.PodSets {
			gomega.Expect(wl.Spec.PodSets[i].MinCount).Should(gomega.BeNil())
		}
		ginkgo.By("the Workload is admitted by the scheduler")
		expectWorkloadAdmitted(&wl)
	})
})

// With PartialAdmission off, minCounts are only honored for elastic partial scale-up workloads.
// The reconciler must attach the elastic annotation before it decides whether minCounts are usable,
// and the mutating webhook must not wipe them; otherwise the partial scale-up feature silently
// degrades to an atomic workload even though its feature gate is on.
var _ = ginkgo.Describe("KEP-12100 partial scale-up RayCluster end to end (PartialAdmission off)", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns             *corev1.Namespace
		resourceFlavor *kueue.ResourceFlavor
		clusterQueue   *kueue.ClusterQueue
		localQueue     *kueue.LocalQueue
	)

	ginkgo.BeforeAll(func() {
		gomega.Expect(utilfeature.DefaultMutableFeatureGate.SetFromMap(map[string]bool{
			string(features.ElasticJobsViaWorkloadSlices):                          true,
			string(features.ElasticJobsViaWorkloadSlicesWithPartialReplicaScaleUp): true,
			string(features.PartialAdmission):                                      false,
		})).Should(gomega.Succeed())
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup())
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-")

		resourceFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
		util.MustCreate(ctx, k8sClient, resourceFlavor)

		clusterQueue = utiltestingapi.MakeClusterQueue("default").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(resourceFlavor.Name).Resource(corev1.ResourceCPU, "6").Obj()).
			Obj()
		util.MustCreate(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("default", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		util.MustCreate(ctx, k8sClient, localQueue)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, resourceFlavor, true)
	})

	ginkgo.It("keeps the partial minCount on the created Workload", func() {
		testRayCluster := testingraycluster.MakeCluster("partial-scale-up", ns.Name).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			SetAnnotation(constants.ElasticJobScaleUpStrategyAnnotationKey, constants.ElasticJobScaleUpStrategyPartial).
			Queue(localQueue.Name).
			Request(rayv1.HeadNode, corev1.ResourceCPU, "1").
			RequestWorkerGroup(corev1.ResourceCPU, "1").
			Obj()

		ginkgo.By("creating the elastic RayCluster with the partial scale-up strategy")
		util.MustCreate(ctx, k8sClient, testRayCluster)

		ginkgo.By("a Workload is created and the worker podSet still carries the partial minCount")
		var wl kueue.Workload
		gomega.Eventually(func(g gomega.Gomega) {
			workloads := &kueue.WorkloadList{}
			g.Expect(k8sClient.List(ctx, workloads, client.InNamespace(ns.Name))).Should(gomega.Succeed())
			g.Expect(workloads.Items).Should(gomega.HaveLen(1))
			wl = workloads.Items[0]
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		workerPS := podSetForWorkerGroup(&wl, "workers-group-0")
		gomega.Expect(workerPS).ShouldNot(gomega.BeNil())
		gomega.Expect(workerPS.MinCount).ShouldNot(gomega.BeNil())
		gomega.Expect(*workerPS.MinCount).Should(gomega.Equal(workerPS.Count))
		ginkgo.By("the Workload is admitted by the scheduler")
		expectWorkloadAdmitted(&wl)
	})
})
