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
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingraycluster "sigs.k8s.io/kueue/pkg/util/testingjobs/raycluster"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/util"
)

const rayActorNamespace = "kueue-e2e"

func createDetachedActorScript(actorName, resourceName string) string {
	return fmt.Sprintf(`import ray

ray.init(namespace=%q)

@ray.remote(num_cpus=0, resources={%q: 1})
class Actor:
    pass

Actor.options(name=%q, lifetime="detached").remote()
`, rayActorNamespace, resourceName, actorName)
}

func terminateDetachedActorScript(actorName string) string {
	return fmt.Sprintf(`import ray

ray.init(namespace=%q)
ray.kill(ray.get_actor(%q))
`, rayActorNamespace, actorName)
}

var _ = ginkgo.Describe("KubeRay multi-PodSet autoscaling", ginkgo.Label("area:singlecluster", "feature:kuberay"), func() {
	var (
		ns *corev1.Namespace
		rf *kueue.ResourceFlavor
		cq *kueue.ClusterQueue
		lq *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "kuberay-autoscaling-e2e-")
		rf = utiltestingapi.MakeResourceFlavor("kuberay-autoscaling-rf-"+ns.Name).
			NodeLabel("instance-type", "on-demand").
			Obj()
		gomega.Expect(k8sClient.Create(ctx, rf)).To(gomega.Succeed())

		cq = utiltestingapi.MakeClusterQueue("kuberay-autoscaling-cq-" + ns.Name).
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(rf.Name).
					Resource(corev1.ResourceCPU, "3").
					Resource(corev1.ResourceMemory, "2Gi").
					Obj(),
			).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

		lq = utiltestingapi.MakeLocalQueue("kuberay-autoscaling-lq-"+ns.Name, ns.Name).
			ClusterQueue(cq.Name).
			Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.It("Should scale RayCluster worker groups independently", ginkgo.Label("shard:kuberay-b"), func() {
		const (
			workerGroupA = "workers-a"
			workerGroupB = "workers-b"
			rayResourceA = "worker_a"
			rayResourceB = "worker_b"
			actorA       = "actor-a"
			actorB       = "actor-b"
		)

		kuberayTestImage := util.GetKuberayTestImage()
		rayCluster := testingraycluster.MakeCluster("raycluster-multi-podset", ns.Name).
			Queue(lq.Name).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			WithEnableAutoscaling(new(true)).
			RequestAndLimit(rayv1.HeadNode, corev1.ResourceCPU, "1").
			RayStartParam(rayv1.HeadNode, "num-cpus", "0").
			RayStartParam(rayv1.HeadNode, "object-store-memory", objectStoreMemory).
			RequestAndLimit(rayv1.WorkerNode, corev1.ResourceCPU, "400m").
			Image(rayv1.HeadNode, kuberayTestImage, []string{}).
			Image(rayv1.WorkerNode, kuberayTestImage, []string{}).
			Obj()
		rayCluster.Spec.AutoscalerOptions = &rayv1.AutoscalerOptions{IdleTimeoutSeconds: ptr.To[int32](10)}

		workerA := rayCluster.Spec.WorkerGroupSpecs[0].DeepCopy()
		workerA.GroupName = workerGroupA
		workerA.Replicas = ptr.To[int32](0)
		workerA.MinReplicas = ptr.To[int32](0)
		workerA.MaxReplicas = ptr.To[int32](1)
		workerA.RayStartParams = map[string]string{
			"num-cpus":            "0",
			"object-store-memory": objectStoreMemory,
			"resources":           fmt.Sprintf(`'{%q: 1}'`, rayResourceA),
		}
		workerB := workerA.DeepCopy()
		workerB.GroupName = workerGroupB
		workerB.RayStartParams["resources"] = fmt.Sprintf(`'{%q: 1}'`, rayResourceB)
		rayCluster.Spec.WorkerGroupSpecs = []rayv1.WorkerGroupSpec{*workerA, *workerB}

		ginkgo.By("Creating an elastic RayCluster with two independently scalable worker groups", func() {
			gomega.Expect(k8sClient.Create(ctx, rayCluster)).To(gomega.Succeed())
		})

		var headPod corev1.Pod
		ginkgo.By("Waiting for the zero-worker RayCluster to become ready", func() {
			createdRayCluster := &rayv1.RayCluster{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rayCluster), createdRayCluster)).To(gomega.Succeed())
				g.Expect(ptr.Deref(createdRayCluster.Spec.Suspend, true)).To(gomega.BeFalse())
				g.Expect(meta.IsStatusConditionTrue(createdRayCluster.Status.Conditions, string(rayv1.HeadPodReady))).To(gomega.BeTrue())
				g.Expect(createdRayCluster.Status.DesiredWorkerReplicas).To(gomega.Equal(int32(0)))

				headPods := &corev1.PodList{}
				g.Expect(k8sClient.List(ctx, headPods,
					client.InNamespace(ns.Name),
					client.MatchingLabels{"ray.io/node-type": "head"},
				)).To(gomega.Succeed())
				g.Expect(headPods.Items).To(gomega.HaveLen(1))
				g.Expect(headPods.Items[0].Status.Phase).To(gomega.Equal(corev1.PodRunning))
				headPod = headPods.Items[0]
			}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed(), util.AssertMsg("RayCluster did not become ready", createdRayCluster))
		})

		runOnHead := func(script string) {
			gomega.Expect(headPod.Spec.Containers).NotTo(gomega.BeEmpty())
			_, _, err := util.KExecute(ctx, cfg, restClient, ns.Name, headPod.Name, headPod.Spec.Containers[0].Name, []string{"python", "-c", script})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}

		expectWorkerGroups := func(expected map[string]int32) {
			createdRayCluster := &rayv1.RayCluster{}
			workerPods := &corev1.PodList{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rayCluster), createdRayCluster)).To(gomega.Succeed())
				g.Expect(k8sClient.List(ctx, workerPods,
					client.InNamespace(ns.Name),
					client.MatchingLabels{"ray.io/node-type": "worker"},
				)).To(gomega.Succeed())

				actualReplicas := make(map[string]int32, len(createdRayCluster.Spec.WorkerGroupSpecs))
				for i := range createdRayCluster.Spec.WorkerGroupSpecs {
					group := &createdRayCluster.Spec.WorkerGroupSpecs[i]
					actualReplicas[group.GroupName] = ptr.Deref(group.Replicas, 0)
				}
				g.Expect(actualReplicas).To(gomega.Equal(expected))

				runningPods := make(map[string]int32, len(expected))
				for i := range workerPods.Items {
					pod := &workerPods.Items[i]
					if pod.Status.Phase == corev1.PodRunning {
						runningPods[pod.Labels["ray.io/group"]]++
					}
				}
				for groupName, count := range expected {
					g.Expect(runningPods[groupName]).To(gomega.Equal(count))
				}
			}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed(), util.AssertMsg("RayCluster worker groups did not reach the expected sizes", createdRayCluster))
		}

		expectKueueAccounting := func(expected map[string]int32) {
			workloadList := &kueue.WorkloadList{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.List(ctx, workloadList, client.InNamespace(ns.Name))).To(gomega.Succeed())
				activeWorkloads := util.FindNonFinishedWorkloads(workloadList.Items)
				g.Expect(activeWorkloads).To(gomega.HaveLen(1))
				g.Expect(workload.IsAdmitted(&activeWorkloads[0])).To(gomega.BeTrue())
				podSetCounts := make(map[string]int32, len(activeWorkloads[0].Spec.PodSets))
				for i := range activeWorkloads[0].Spec.PodSets {
					podSet := &activeWorkloads[0].Spec.PodSets[i]
					podSetCounts[string(podSet.Name)] = podSet.Count
				}
				for groupName, count := range expected {
					g.Expect(podSetCounts[groupName]).To(gomega.Equal(count))
				}
			}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed(), util.AssertMsgObjList("Kueue did not account for the worker groups independently", workloadList))
		}

		ginkgo.By("Requesting only the first worker group's custom resource", func() {
			runOnHead(createDetachedActorScript(actorA, rayResourceA))
			expected := map[string]int32{workerGroupA: 1, workerGroupB: 0}
			expectWorkerGroups(expected)
			expectKueueAccounting(expected)
		})

		ginkgo.By("Requesting the second worker group's custom resource without changing the first group", func() {
			runOnHead(createDetachedActorScript(actorB, rayResourceB))
			expected := map[string]int32{workerGroupA: 1, workerGroupB: 1}
			expectWorkerGroups(expected)
			expectKueueAccounting(expected)
		})

		ginkgo.By("Terminating the first actor without scaling down the second worker group", func() {
			runOnHead(terminateDetachedActorScript(actorA))
			expected := map[string]int32{workerGroupA: 0, workerGroupB: 1}
			expectWorkerGroups(expected)
			expectKueueAccounting(expected)
		})

		ginkgo.By("Terminating the second actor and returning both worker groups to zero", func() {
			runOnHead(terminateDetachedActorScript(actorB))
			expected := map[string]int32{workerGroupA: 0, workerGroupB: 0}
			expectWorkerGroups(expected)
			expectKueueAccounting(expected)
		})
	})
})
