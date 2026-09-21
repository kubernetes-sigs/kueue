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
	"strconv"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadleaderworkerset "sigs.k8s.io/kueue/pkg/controller/jobs/leaderworkerset"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingleaderworkerset "sigs.k8s.io/kueue/pkg/util/testingjobs/leaderworkerset"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

type leaderWorkerSetTestContext struct {
	managerNs         *corev1.Namespace
	managerLq         *kueue.LocalQueue
	multiKueueAc      *kueue.AdmissionCheck
	kubernetesClients kubernetesClientsMap
}

func registerLeaderWorkerSetTests(contextProvider func() leaderWorkerSetTestContext) {
	ginkgo.It("Should sync a LeaderWorkerSet and run replicas on worker cluster", ginkgo.Label("feature:leaderworkerset"), func() {
		tc := contextProvider()
		managerNs := tc.managerNs
		managerLq := tc.managerLq
		multiKueueAc := tc.multiKueueAc
		kubernetesClients := tc.kubernetesClients

		lws := testingleaderworkerset.MakeLeaderWorkerSet("leaderworkerset", managerNs.Name).
			Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
			Replicas(2).
			Size(2).
			RequestAndLimit(corev1.ResourceCPU, "100m").
			RequestAndLimit(corev1.ResourceMemory, "100M").
			Queue(managerLq.Name).
			TerminationGracePeriod(1).
			Obj()

		ginkgo.By("Creating the leaderworkerset", func() {
			util.MustCreate(ctx, k8sManagerClient, lws)
		})

		createdLWS := &leaderworkersetv1.LeaderWorkerSet{}
		gomega.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLWS)).To(gomega.Succeed())

		wlLookupKey0 := types.NamespacedName{
			Name:      workloadleaderworkerset.GetWorkloadName(createdLWS.UID, createdLWS.Name, "0"),
			Namespace: managerNs.Name,
		}
		wlLookupKey1 := types.NamespacedName{
			Name:      workloadleaderworkerset.GetWorkloadName(createdLWS.UID, createdLWS.Name, "1"),
			Namespace: managerNs.Name,
		}

		admittedWorkerName := util.ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlLookupKey0, multiKueueAc.Name)
		workerClient := kubernetesClients[admittedWorkerName].client

		ginkgo.By("Verifying both workloads are admitted on the same worker", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				wl0 := &kueue.Workload{}
				g.Expect(workerClient.Get(ctx, wlLookupKey0, wl0)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(wl0)).To(gomega.BeTrue())
				wl1 := &kueue.Workload{}
				g.Expect(workerClient.Get(ctx, wlLookupKey1, wl1)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(wl1)).To(gomega.BeTrue())
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Waiting for LWS to be synced to worker cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				workerLWS := &leaderworkersetv1.LeaderWorkerSet{}
				g.Expect(workerClient.Get(ctx, client.ObjectKeyFromObject(lws), workerLWS)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Waiting for all replicas to be ready on worker cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				workerLWS := &leaderworkersetv1.LeaderWorkerSet{}
				g.Expect(workerClient.Get(ctx, client.ObjectKeyFromObject(lws), workerLWS)).To(gomega.Succeed())
				g.Expect(workerLWS.Status.ReadyReplicas).To(gomega.Equal(int32(2)))
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Verifying pods on management cluster remain gated", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				pods := &corev1.PodList{}
				g.Expect(k8sManagerClient.List(ctx, pods, client.InNamespace(managerNs.Name), client.MatchingLabels{
					leaderworkersetv1.SetNameLabelKey: lws.Name,
				})).To(gomega.Succeed())
				g.Expect(pods.Items).ToNot(gomega.BeEmpty())
				for _, pod := range pods.Items {
					g.Expect(utilpod.HasGate(&pod, podconstants.SchedulingGateName)).To(gomega.BeTrue())
				}
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Deleting the leaderworkerset", func() {
			util.ExpectObjectToBeDeleted(ctx, k8sManagerClient, lws, true)
			util.ExpectObjectToBeDeletedWithTimeout(ctx, workerClient, lws, false, util.MediumTimeout)
		})

		ginkgo.By("Checking that all workloads are deleted from manager and worker clusters", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sManagerClient.Get(ctx, wlLookupKey0, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
				g.Expect(k8sManagerClient.Get(ctx, wlLookupKey1, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
				g.Expect(workerClient.Get(ctx, wlLookupKey0, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
				g.Expect(workerClient.Get(ctx, wlLookupKey1, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should dispatch all LeaderWorkerSet workloads to the same worker", ginkgo.Label("feature:leaderworkerset"), func() {
		tc := contextProvider()
		managerNs := tc.managerNs
		managerLq := tc.managerLq
		multiKueueAc := tc.multiKueueAc
		kubernetesClients := tc.kubernetesClients

		const lwsReplicas = 3
		lws := testingleaderworkerset.MakeLeaderWorkerSet("leaderworkerset", managerNs.Name).
			Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
			Replicas(lwsReplicas).
			Size(2).
			RequestAndLimit(corev1.ResourceCPU, "100m").
			RequestAndLimit(corev1.ResourceMemory, "100M").
			Queue(managerLq.Name).
			TerminationGracePeriod(1).
			Obj()

		ginkgo.By("Creating the leaderworkerset", func() {
			util.MustCreate(ctx, k8sManagerClient, lws)
		})

		createdLWS := &leaderworkersetv1.LeaderWorkerSet{}
		gomega.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLWS)).To(gomega.Succeed())

		wlKeys := make([]types.NamespacedName, lwsReplicas)
		for i := range lwsReplicas {
			wlKeys[i] = types.NamespacedName{
				Name:      workloadleaderworkerset.GetWorkloadName(createdLWS.UID, createdLWS.Name, strconv.Itoa(i)),
				Namespace: managerNs.Name,
			}
		}

		ginkgo.By("Waiting for workloads to be created on manager cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				for _, key := range wlKeys {
					wl := &kueue.Workload{}
					g.Expect(k8sManagerClient.Get(ctx, key, wl)).To(gomega.Succeed())
				}
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		admittedWorkerName := util.ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlKeys[0], multiKueueAc.Name)
		workerClient := kubernetesClients[admittedWorkerName].client

		ginkgo.By("Verifying primary workload is admitted on worker2", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				wl := &kueue.Workload{}
				g.Expect(workerClient.Get(ctx, wlKeys[0], wl)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(wl)).To(gomega.BeTrue())
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Verifying LWS is synced to worker cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				workerLWS := &leaderworkersetv1.LeaderWorkerSet{}
				g.Expect(workerClient.Get(ctx, client.ObjectKeyFromObject(lws), workerLWS)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Verifying follower workloads are dispatched to the same worker cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				primaryWl := &kueue.Workload{}
				g.Expect(k8sManagerClient.Get(ctx, wlKeys[0], primaryWl)).To(gomega.Succeed())
				g.Expect(primaryWl.Status.ClusterName).ToNot(gomega.BeNil())
				for _, key := range wlKeys[1:] {
					wl := &kueue.Workload{}
					g.Expect(k8sManagerClient.Get(ctx, key, wl)).To(gomega.Succeed())
					g.Expect(wl.Status.ClusterName).ToNot(gomega.BeNil())
					g.Expect(*wl.Status.ClusterName).To(gomega.Equal(*primaryWl.Status.ClusterName))
				}
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Deleting the leaderworkerset", func() {
			util.ExpectObjectToBeDeleted(ctx, k8sManagerClient, lws, true)
			util.ExpectObjectToBeDeletedWithTimeout(ctx, workerClient, lws, false, util.MediumTimeout)
		})

		ginkgo.By("Checking that all workloads are deleted from manager and worker clusters", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				for _, key := range wlKeys {
					g.Expect(k8sManagerClient.Get(ctx, key, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
					g.Expect(workerClient.Get(ctx, key, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
				}
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})
	})
}
