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
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/component-base/featuregate"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	testingraycluster "sigs.k8s.io/kueue/pkg/util/testingjobs/raycluster"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

// KEP-975: Partial preemption.
var _ = ginkgo.Describe("RayCluster partial preemption", ginkgo.Label("job:ray", "area:jobs"), func() {
	const (
		headPodSet    kueue.PodSetReference = "head"
		workersPodSet kueue.PodSetReference = workersGroupName
	)
	var (
		ns             *corev1.Namespace
		resourceFlavor *kueue.ResourceFlavor
		clusterQueue   *kueue.ClusterQueue
		localQueue     *kueue.LocalQueue
	)

	// mintPod emulates KubeRay creating a Pod for the RayCluster. envtest runs no KubeRay operator.
	mintPod := func(name string, wlName string, podSet kueue.PodSetReference) {
		ginkgo.GinkgoHelper()
		pod := testingpod.MakePod(name, ns.Name).
			Annotation(kueue.WorkloadAnnotation, wlName).
			Annotation(kueue.WorkloadSliceNameAnnotation, wlName).
			Label(constants.PodSetLabel, string(podSet)).
			Gate(kueue.ElasticJobSchedulingGate).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, pod)
	}

	// expectPods waits until the PodSet has want Pods, all ungated.
	expectPods := func(podSet kueue.PodSetReference, want int) {
		ginkgo.GinkgoHelper()
		gomega.Eventually(func(g gomega.Gomega) {
			pods := &corev1.PodList{}
			g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name),
				client.MatchingLabels{constants.PodSetLabel: string(podSet)})).Should(gomega.Succeed())
			g.Expect(pods.Items).Should(gomega.HaveLen(want))
			for i := range pods.Items {
				g.Expect(utilpod.HasGate(&pods.Items[i], kueue.ElasticJobSchedulingGate)).Should(gomega.BeFalse())
			}
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	}

	ginkgo.BeforeEach(func() {
		features.SetFeatureGatesDuringTest(ginkgo.GinkgoTB(), map[featuregate.Feature]bool{
			features.ElasticJobsViaWorkloadSlices: true,
			features.PartialPreemption:            true,
		})
		ginkgo.DeferCleanup(fwk.StopManager, ctx)
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup())

		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "ray-partial-preemption-")

		resourceFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
		behavioral.MustCreate(ctx, k8sClient, resourceFlavor)

		// "pods" is the constrained resource: the head and 4 workers fill the ClusterQueue.
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

	ginkgo.It("shrinks the worker group to admit a preemptor, and keeps the head", framework.SlowSpec, func() {
		const (
			workers int32 = 4
			// The preemptor needs 2 pods. The shrink pass targets the floor (1), and fill-back
			// raises the target to the largest count that still fits: 4 - 2 = 2.
			target int32 = 2
		)
		victim := testingraycluster.MakeCluster("victim", ns.Name).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			SetAnnotation(constants.PartialPreemptionAnnotation, "true").
			NodeAnnotation(rayv1.WorkerNode, constants.PodSetPartialPreemptionMinRetainedCountAnnotation, "1").
			Queue(localQueue.Name).
			RequestWorkerGroup(corev1.ResourceCPU, "1").
			FirstWorkerGroupReplicas(workers, 1, workers).
			Obj()

		var wl *kueue.Workload
		ginkgo.By("admitting the victim and running its head and workers", func() {
			behavioral.MustCreate(ctx, k8sClient, victim)
			wl = &behavioral.ExpectWorkloadsInNamespace(ctx, k8sClient, ns.Name, 1)[0]
			behavioral.ExpectPodSetAdmittedCount(ctx, k8sClient, wl, workersPodSet, workers)
			mintPod("victim-head", wl.Name, headPodSet)
			for i := range workers {
				mintPod(fmt.Sprintf("victim-worker-%d", i), wl.Name, workersPodSet)
			}
			expectPods(headPodSet, 1)
			expectPods(workersPodSet, int(workers))
		})

		preemptor := utiltestingapi.MakeWorkload("preemptor", ns.Name).
			Priority(10).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			PodSets(*utiltestingapi.MakePodSet("main", 2).Request(corev1.ResourceCPU, "1").Obj()).
			Obj()

		ginkgo.By("evicting the workers above the target, keeping the head, and admitting the preemptor", func() {
			behavioral.MustCreate(ctx, k8sClient, preemptor)
			expectPods(workersPodSet, int(target))
			expectPods(headPodSet, 1)
			behavioral.ExpectPodSetAdmittedCount(ctx, k8sClient, wl, workersPodSet, target)
			behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, preemptor)
		})
	})
})
