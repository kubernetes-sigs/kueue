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

package rayservice

import (
	"fmt"
	"slices"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	testingrayservice "sigs.k8s.io/kueue/pkg/util/testingjobs/rayservice"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("RayService with elastic jobs via workload-slices support", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns             *corev1.Namespace
		clusterQueue   *kueue.ClusterQueue
		localQueue     *kueue.LocalQueue
		resourceFlavor *kueue.ResourceFlavor
	)

	ginkgo.BeforeAll(func() {
		gomega.Expect(utilfeature.DefaultMutableFeatureGate.SetFromMap(map[string]bool{string(features.ElasticJobsViaWorkloadSlices): true})).Should(gomega.Succeed())
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup())
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "rayservice-elastic-")

		resourceFlavor = utiltestingapi.MakeResourceFlavor("flavor").Obj()
		util.MustCreate(ctx, k8sClient, resourceFlavor)

		clusterQueue = utiltestingapi.MakeClusterQueue("cq").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("flavor").
				Resource(corev1.ResourceCPU, "10").
				Resource(corev1.ResourceMemory, "5Gi").Obj()).
			Obj()
		util.MustCreate(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("lq", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		util.MustCreate(ctx, k8sClient, localQueue)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, resourceFlavor, true)
	})

	ginkgo.It("Should ungate chain pods after the origin workload slice is deleted", framework.SlowSpec, func() {
		service := testingrayservice.MakeService("foo", ns.Name).
			Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			Queue(localQueue.Name).
			Request(rayv1.HeadNode, corev1.ResourceCPU, "1").
			Request(rayv1.WorkerNode, corev1.ResourceCPU, "1").
			EnableInTreeAutoscaling().
			Obj()

		ginkgo.By("creating and admitting the rayservice's origin workload slice")
		util.MustCreate(ctx, k8sClient, service)
		var originSlice *kueue.Workload
		gomega.Eventually(func(g gomega.Gomega) {
			workloads := &kueue.WorkloadList{}
			g.Expect(k8sClient.List(ctx, workloads, client.InNamespace(ns.Name))).Should(gomega.Succeed())
			g.Expect(workloads.Items).Should(gomega.HaveLen(1))
			originSlice = &workloads.Items[0]
			g.Expect(workload.IsAdmitted(originSlice)).Should(gomega.BeTrue())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		originSliceName := originSlice.Name

		ginkgo.By("creating the child RayCluster owned by the RayService, as KubeRay would")
		// The child RayCluster is owned by the RayService, mirroring the real
		// topology: the pods' controller owner (RayCluster) differs from the
		// slices' owner (RayService).
		childCluster := &rayv1.RayCluster{
			ObjectMeta: metav1.ObjectMeta{Name: service.Name + "-raycluster", Namespace: ns.Name},
			Spec:       *service.Spec.RayClusterSpec.DeepCopy(),
		}
		childCluster.OwnerReferences = []metav1.OwnerReference{{
			APIVersion:         rayv1.SchemeGroupVersion.String(),
			Kind:               "RayService",
			Name:               service.Name,
			UID:                service.UID,
			Controller:         new(true),
			BlockOwnerDeletion: new(true),
		}}
		util.MustCreate(ctx, k8sClient, childCluster)

		ginkgo.By("promoting the child cluster to active in the RayService status, as KubeRay would")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(service), service)).Should(gomega.Succeed())
			service.Status.ActiveServiceStatus.RayClusterName = childCluster.Name
			g.Expect(k8sClient.Status().Update(ctx, service)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("scaling up the child RayCluster's worker replicas, as the Ray autoscaler would")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(childCluster), childCluster)).Should(gomega.Succeed())
			childCluster.Spec.WorkerGroupSpecs[0].Replicas = new(int32(2))
			g.Expect(k8sClient.Update(ctx, childCluster)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		var activeSlice *kueue.Workload
		gomega.Eventually(func(g gomega.Gomega) {
			workloads := &kueue.WorkloadList{}
			g.Expect(k8sClient.List(ctx, workloads, client.InNamespace(ns.Name))).Should(gomega.Succeed())
			g.Expect(workloads.Items).Should(gomega.HaveLen(2))
			activeWorkloads := util.FindNonFinishedWorkloads(workloads.Items)
			g.Expect(activeWorkloads).Should(gomega.HaveLen(1))
			activeSlice = &activeWorkloads[0]
			g.Expect(activeSlice.Name).ShouldNot(gomega.Equal(originSliceName))
			g.Expect(workload.IsAdmitted(activeSlice)).Should(gomega.BeTrue())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("deleting the origin slice to emulate a rollout GC of the old cluster's workloads")
		util.DeleteWorkloadSliceAndAwaitDeletion(ctx, k8sClient, types.NamespacedName{Namespace: ns.Name, Name: originSliceName})

		ginkgo.By("creating still-gated pods that point at the now-deleted origin slice, owned by the child RayCluster")
		var workerPodSet kueue.PodSetReference
		for _, ps := range activeSlice.Spec.PodSets {
			if ps.Name != "head" {
				workerPodSet = ps.Name
			}
		}
		gomega.Expect(workerPodSet).ShouldNot(gomega.BeEmpty())

		// One more gated pod than the active slice's granted worker count (2), so
		// the test also pins that ungating stays capped by the admitted quota.
		gatedPods := make([]*corev1.Pod, 3)
		for i := range gatedPods {
			gatedPods[i] = testingpod.MakePod(fmt.Sprintf("worker-%d", i), ns.Name).
				Annotation(kueue.WorkloadAnnotation, originSliceName).
				Annotation(kueue.WorkloadSliceNameAnnotation, originSliceName).
				Label(constants.PodSetLabel, string(workerPodSet)).
				Gate(kueue.ElasticJobSchedulingGate).
				Obj()
			gatedPods[i].OwnerReferences = []metav1.OwnerReference{{
				APIVersion:         rayv1.SchemeGroupVersion.String(),
				Kind:               "RayCluster",
				Name:               childCluster.Name,
				UID:                childCluster.UID,
				Controller:         new(true),
				BlockOwnerDeletion: new(true),
			}}
			util.MustCreate(ctx, k8sClient, gatedPods[i])
		}

		hasElasticGate := func(g gomega.Gomega, pod *corev1.Pod) bool {
			var got corev1.Pod
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), &got)).To(gomega.Succeed())
			return slices.Contains(got.Spec.SchedulingGates, corev1.PodSchedulingGate{Name: kueue.ElasticJobSchedulingGate})
		}

		ginkgo.By("the ungater removes the elastic scheduling gate from the two lowest-named pods despite the origin slice being gone")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(hasElasticGate(g, gatedPods[0])).Should(gomega.BeFalse())
			g.Expect(hasElasticGate(g, gatedPods[1])).Should(gomega.BeFalse())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("the pod beyond the active slice's granted worker count stays gated")
		gomega.Consistently(func(g gomega.Gomega) {
			g.Expect(hasElasticGate(g, gatedPods[2])).Should(gomega.BeTrue())
		}, util.LongConsistentDuration, util.Interval).Should(gomega.Succeed())
	})
})
