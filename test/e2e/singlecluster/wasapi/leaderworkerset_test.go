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

package wasapi

import (
	"strconv"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	schedulingv1alpha3 "k8s.io/api/scheduling/v1alpha3"
	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	leaderworkersettesting "sigs.k8s.io/kueue/pkg/util/testingjobs/leaderworkerset"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/e2e"
)

// These tests run a LeaderWorkerSet with native gang scheduling
// (spec.scheduling, LWS v1.0) under Kueue. In replica mode LWS creates one
// PodGroup per group, which lines up with the one Workload per group that
// Kueue creates.
//
// On top of the Job suite requirements, LWS must run with the Kubernetes
// scheduler provider. See install_lws in hack/testing/e2e-common.sh.
var _ = ginkgo.Describe("WorkloadAwareScheduling LeaderWorkerSet", ginkgo.Label("area:was", "feature:was", "feature:was-leaderworkerset"), func() {
	var (
		ns           *corev1.Namespace
		rf           *kueue.ResourceFlavor
		clusterQueue *kueue.ClusterQueue
		localQueue   *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-was-lws-")
		rf = utiltestingapi.MakeResourceFlavor("on-demand-was-"+ns.Name).
			NodeLabel("instance-type", "on-demand").Obj()
		behavioral.MustCreate(ctx, k8sClient, rf)
		clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue-was-" + ns.Name).
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(rf.Name).
					Resource(corev1.ResourceCPU, "4").
					Resource(corev1.ResourceMemory, "4Gi").
					Obj(),
			).
			Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)
		localQueue = utiltestingapi.MakeLocalQueue("main", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteAllLeaderWorkerSetsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
		behavioral.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.It("Should admit, evict and readmit each group with its own gang PodGroup", func() {
		const (
			replicas int32 = 2
			size     int32 = 2
		)
		lws := leaderworkersettesting.MakeLeaderWorkerSet("lws", ns.Name).
			Image(e2e.GetAgnHostImage(), e2e.BehaviorWaitForDeletion).
			Replicas(replicas).
			Size(size).
			RequestAndLimit(corev1.ResourceCPU, "200m").
			RequestAndLimit(corev1.ResourceMemory, "20Mi").
			TerminationGracePeriod(1).
			Queue(localQueue.Name).
			Scheduling(&leaderworkersetv1.LeaderWorkerSetScheduling{
				Replica: &leaderworkersetv1.LeaderWorkerSetReplicaScheduling{
					SchedulingPolicy: &schedulingv1alpha3.WorkloadCompositePodGroupSchedulingPolicy{
						Gang: &schedulingv1alpha3.WorkloadCompositePodGroupGangSchedulingPolicy{},
					},
				},
			}).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, lws)

		wlKeys := make([]client.ObjectKey, 0, replicas)
		for i := range replicas {
			wlKeys = append(wlKeys, e2e.WorkloadKeyForLeaderWorkerSet(lws, strconv.Itoa(int(i))))
		}

		ginkgo.By("verifying a Workload per group is admitted", func() {
			behavioral.ExpectWorkloadsToBeAdmittedByKeysWithTimeout(ctx, k8sClient, behavioral.MediumTimeout, wlKeys...)
		})

		ginkgo.By("verifying each group runs in its own PodGroup with gang minCount matching its Workload", func() {
			expectGroupsRunningInGangPodGroups(lws, wlKeys, size)
		})

		createdWorkload := &kueue.Workload{}
		ginkgo.By("deactivating the Workload of group 0 to evict it", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKeys[0], createdWorkload)).To(gomega.Succeed())
				createdWorkload.Spec.Active = new(false)
				g.Expect(k8sClient.Update(ctx, createdWorkload)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verifying group 0 is evicted and its pods are gated again", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKeys[0], createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason(kueue.WorkloadEvicted, kueue.WorkloadDeactivated))
				g.Expect(createdWorkload.Status.Conditions).To(utiltesting.HaveConditionStatusFalse(kueue.WorkloadQuotaReserved))
			}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				pods := groupPods(g, lws, "0")
				g.Expect(pods).To(gomega.HaveLen(int(size)))
				gated := make([]corev1.Pod, 0, len(pods))
				for _, pod := range pods {
					if utilpod.HasGate(&pod, podconstants.SchedulingGateName) {
						gated = append(gated, pod)
					}
				}
				g.Expect(gated).To(gomega.Equal(pods), "all pods of group 0 should be gated")
			}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verifying group 1 stays admitted", func() {
			behavioral.ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sClient, wlKeys[1])
		})

		ginkgo.By("reactivating the Workload of group 0", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKeys[0], createdWorkload)).To(gomega.Succeed())
				createdWorkload.Spec.Active = new(true)
				g.Expect(k8sClient.Update(ctx, createdWorkload)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verifying group 0 is readmitted and runs in a gang PodGroup again", func() {
			behavioral.ExpectWorkloadsToBeAdmittedByKeysWithTimeout(ctx, k8sClient, behavioral.MediumTimeout, wlKeys[0])
			expectGroupsRunningInGangPodGroups(lws, wlKeys, size)
		})
	})
})

// expectGroupsRunningInGangPodGroups waits until the LeaderWorkerSet is
// available and every group's pods are running in one PodGroup of their own,
// whose gang minCount equals the pod count of the group's Kueue Workload.
func expectGroupsRunningInGangPodGroups(lws *leaderworkersetv1.LeaderWorkerSet, wlKeys []client.ObjectKey, size int32) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		createdLWS := &leaderworkersetv1.LeaderWorkerSet{}
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLWS)).To(gomega.Succeed())
		g.Expect(createdLWS.Status.ReadyReplicas).To(gomega.Equal(int32(len(wlKeys))))
		g.Expect(createdLWS.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason("Available", "AllGroupsReady"))
	}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())

	podGroupNames := sets.New[string]()
	for i, wlKey := range wlKeys {
		wlPodCount := workloadPodCount(wlKey)
		gomega.Expect(wlPodCount).To(gomega.Equal(size))
		podGroupName := groupPodGroupName(lws, strconv.Itoa(i), size)
		expectGangMinCount(lws.Namespace, podGroupName, wlPodCount)
		podGroupNames.Insert(podGroupName)
	}
	gomega.Expect(podGroupNames.UnsortedList()).To(gomega.HaveLen(len(wlKeys)), "each group should have its own PodGroup")
}

// workloadPodCount returns the total pod count of a Workload's PodSets.
func workloadPodCount(wlKey client.ObjectKey) int32 {
	ginkgo.GinkgoHelper()
	var count int32
	gomega.Eventually(func(g gomega.Gomega) {
		wl := &kueue.Workload{}
		g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
		count = 0
		for _, ps := range wl.Spec.PodSets {
			count += ps.Count
		}
	}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	return count
}

// groupPodGroupName waits until all pods of a group are running in one
// PodGroup and returns its name.
func groupPodGroupName(lws *leaderworkersetv1.LeaderWorkerSet, groupIndex string, size int32) string {
	ginkgo.GinkgoHelper()
	var name string
	gomega.Eventually(func(g gomega.Gomega) {
		pods := groupPods(g, lws, groupIndex)
		g.Expect(pods).To(gomega.HaveLen(int(size)))
		names := sets.New[string]()
		for _, pod := range pods {
			g.Expect(pod.Status.Phase).To(gomega.Equal(corev1.PodRunning), "pod %s", pod.Name)
			g.Expect(pod.Spec.SchedulingGroup).NotTo(gomega.BeNil(), "pod %s has no PodGroup", pod.Name)
			g.Expect(pod.Spec.SchedulingGroup.PodGroupName).NotTo(gomega.BeNil(), "pod %s has no PodGroup", pod.Name)
			names.Insert(*pod.Spec.SchedulingGroup.PodGroupName)
		}
		g.Expect(names.UnsortedList()).To(gomega.HaveLen(1), "pods of group %s should share one PodGroup", groupIndex)
		name = names.UnsortedList()[0]
	}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())
	return name
}

// expectGangMinCount waits until the PodGroup has a gang policy with the given minCount.
func expectGangMinCount(namespace, podGroupName string, minCount int32) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		pg := &schedulingv1beta1.PodGroup{}
		g.Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: podGroupName}, pg)).To(gomega.Succeed())
		g.Expect(pg.Spec.SchedulingPolicy.Gang).NotTo(gomega.BeNil())
		g.Expect(pg.Spec.SchedulingPolicy.Gang.MinCount).To(gomega.Equal(minCount),
			"PodGroup gang minCount should match the Kueue Workload pod count")
	}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
}

// groupPods returns the pods of one LeaderWorkerSet group that are not being deleted.
func groupPods(g gomega.Gomega, lws *leaderworkersetv1.LeaderWorkerSet, groupIndex string) []corev1.Pod {
	ginkgo.GinkgoHelper()
	pods := &corev1.PodList{}
	g.Expect(k8sClient.List(ctx, pods, client.InNamespace(lws.Namespace), client.MatchingLabels{
		leaderworkersetv1.SetNameLabelKey:    lws.Name,
		leaderworkersetv1.GroupIndexLabelKey: groupIndex,
	})).To(gomega.Succeed())
	active := make([]corev1.Pod, 0, len(pods.Items))
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp.IsZero() {
			active = append(active, pod)
		}
	}
	return active
}
