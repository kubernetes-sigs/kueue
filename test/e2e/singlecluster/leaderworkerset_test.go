/*
Copyright 2024 The Kubernetes Authors.

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

package e2e

import (
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobs/leaderworkerset"
	"sigs.k8s.io/kueue/pkg/util/testing"
	leaderworkersettesting "sigs.k8s.io/kueue/pkg/util/testingjobs/leaderworkerset"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Leader worker set integration", func() {
	var (
		ns               *corev1.Namespace
		onDemandRF       *kueue.ResourceFlavor
		RFName           = "lws-resource-flavour"
		clusterQueueName = "lws-cluster-queue"
		localQueueName   = "lws-local-queue"
		cq               *kueue.ClusterQueue
		lq               *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "lws-e2e-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
		onDemandRF = testing.MakeResourceFlavor(RFName).
			NodeLabel("instance-type", "on-demand").
			Obj()
		gomega.Expect(k8sClient.Create(ctx, onDemandRF)).To(gomega.Succeed())
	})
	//ginkgo.AfterEach(func() {
	//	gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	//	util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandRF, true)
	//})

	ginkgo.When("Single CQ", func() {
		ginkgo.BeforeEach(func() {
			cq = testing.MakeClusterQueue(clusterQueueName).
				ResourceGroup(
					*testing.MakeFlavorQuotas(RFName).
						Resource(corev1.ResourceCPU, "5").
						Obj(),
				).
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
				}).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())
			lq = testing.MakeLocalQueue(localQueueName, ns.Name).ClusterQueue(cq.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, lq)).To(gomega.Succeed())
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteAllPodsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		})

		ginkgo.It("should admit group with leader only that fits", func() {
			lws := leaderworkersettesting.MakeLeaderWorkerSet("lws", ns.Name).
				Image(util.E2eTestSleepImage, []string{"10m"}).
				Size(1).
				Replicas(1).
				Request(corev1.ResourceCPU, "1").
				Queue(lq.Name).
				Obj()
			wlLookupKey := types.NamespacedName{Name: leaderworkerset.GetWorkloadName(lws.Name), Namespace: ns.Name}
			gomega.Expect(k8sClient.Create(ctx, lws)).To(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				createdLeaderWorkerSet := &leaderworkersetv1.LeaderWorkerSet{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLeaderWorkerSet)).
					To(gomega.Succeed())
				g.Expect(createdLeaderWorkerSet.Status.ReadyReplicas).To(gomega.Equal(int32(1)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			createdWorkload := &kueue.Workload{}
			gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())

			// Expect to have statefulset with one leader replica
			gomega.Eventually(func(g gomega.Gomega) {
				leaderStatefulSet := &appsv1.StatefulSet{}
				g.Expect(k8sClient.Get(
					ctx,
					types.NamespacedName{Name: lws.Name, Namespace: ns.Name},
					leaderStatefulSet,
				)).To(gomega.Succeed())
				g.Expect(*leaderStatefulSet.Spec.Replicas).To(gomega.Equal(int32(1)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

			util.ExpectObjectToBeDeleted(ctx, k8sClient, lws, true)
			util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, createdWorkload, false, util.LongTimeout)
		})

		ginkgo.It("should admit group with leader and workers that fits", func() {
			lws := leaderworkersettesting.MakeLeaderWorkerSet("lws", ns.Name).
				Image(util.E2eTestSleepImage, []string{"10m"}).
				Size(3).
				Replicas(1).
				Request(corev1.ResourceCPU, "100m").
				Queue(lq.Name).
				Obj()
			wlLookupKey := types.NamespacedName{Name: leaderworkerset.GetWorkloadName(lws.Name), Namespace: ns.Name}
			gomega.Expect(k8sClient.Create(ctx, lws)).To(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				createdLeaderWorkerSet := &leaderworkersetv1.LeaderWorkerSet{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLeaderWorkerSet)).
					To(gomega.Succeed())
				g.Expect(createdLeaderWorkerSet.Status.ReadyReplicas).To(gomega.Equal(int32(1)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

			// Expect to have statefulset with one leader replica and another statefulset with 2 worker replicas
			gomega.Eventually(func(g gomega.Gomega) {
				leaderStatefulSet := &appsv1.StatefulSet{}
				g.Expect(k8sClient.Get(
					ctx,
					types.NamespacedName{Name: lws.Name, Namespace: ns.Name},
					leaderStatefulSet,
				)).To(gomega.Succeed())
				g.Expect(*leaderStatefulSet.Spec.Replicas).To(gomega.Equal(int32(1)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				workerStatefulSet := &appsv1.StatefulSet{}
				g.Expect(k8sClient.Get(
					ctx,
					// Worker statefulset name is formed by appending group index to the leaderworkerset name
					types.NamespacedName{Name: fmt.Sprintf("%s-0", lws.Name), Namespace: ns.Name},
					workerStatefulSet,
				)).To(gomega.Succeed())
				g.Expect(*workerStatefulSet.Spec.Replicas).To(gomega.Equal(int32(2)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

			createdWorkload := &kueue.Workload{}
			gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())

			util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, createdWorkload, false, util.LongTimeout)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, lws, true)
		})

		ginkgo.It("should admit group with multiple leaders and workers that fits", func() {
			lws := leaderworkersettesting.MakeLeaderWorkerSet("lws", ns.Name).
				Image(util.E2eTestSleepImage, []string{"10m"}).
				Size(2).
				Replicas(2).
				Request(corev1.ResourceCPU, "1").
				Queue(lq.Name).
				Obj()
			wlLookupKey := types.NamespacedName{Name: leaderworkerset.GetWorkloadName(lws.Name), Namespace: ns.Name}
			gomega.Expect(k8sClient.Create(ctx, lws)).To(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				createdLeaderWorkerSet := &leaderworkersetv1.LeaderWorkerSet{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLeaderWorkerSet)).
					To(gomega.Succeed())
				g.Expect(createdLeaderWorkerSet.Status.ReadyReplicas).To(gomega.Equal(int32(4)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

			// Expect to have statefulset with two leader replicas and statefulset with 2 worker replicas
			gomega.Eventually(func(g gomega.Gomega) {
				leaderStatefulSet := &appsv1.StatefulSet{}
				g.Expect(k8sClient.Get(
					ctx,
					types.NamespacedName{Name: lws.Name, Namespace: ns.Name},
					leaderStatefulSet,
				)).To(gomega.Succeed())
				g.Expect(*leaderStatefulSet.Spec.Replicas).To(gomega.Equal(int32(2)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				workerStatefulSet := &appsv1.StatefulSet{}
				g.Expect(k8sClient.Get(
					ctx,
					// Worker statefulset name is formed by appending group index to the leaderworkerset name
					types.NamespacedName{Name: fmt.Sprintf("%s-0", lws.Name), Namespace: ns.Name},
					workerStatefulSet,
				)).To(gomega.Succeed())
				g.Expect(*workerStatefulSet.Spec.Replicas).To(gomega.Equal(int32(2)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

			createdWorkload := &kueue.Workload{}
			gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())

			util.ExpectObjectToBeDeleted(ctx, k8sClient, lws, true)
			util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, createdWorkload, false, util.LongTimeout)
		})

		ginkgo.FIt("should admit group with multiple leaders and workers that fits and have different resource needs", func() {
			lws := leaderworkersettesting.MakeLeaderWorkerSet("lws", ns.Name).
				Image(util.E2eTestSleepImage, []string{"10m"}).
				Size(2).
				Replicas(2).
				Request(corev1.ResourceCPU, "1").
				Queue(lq.Name).
				LeaderTemplate(corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "c",
								Image: "pause",
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU: resource.MustParse("100m"),
									},
								},
							},
						},
						NodeSelector: map[string]string{},
					},
				}).
				Obj()
			wlLookupKey := types.NamespacedName{Name: leaderworkerset.GetWorkloadName(lws.Name), Namespace: ns.Name}
			gomega.Expect(k8sClient.Create(ctx, lws)).To(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				createdLeaderWorkerSet := &leaderworkersetv1.LeaderWorkerSet{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLeaderWorkerSet)).
					To(gomega.Succeed())
				g.Expect(createdLeaderWorkerSet.Status.ReadyReplicas).To(gomega.Equal(int32(4)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

			// Expect to have statefulset with two leader replicas and statefulset with 2 worker replicas
			gomega.Eventually(func(g gomega.Gomega) {
				leaderStatefulSet := &appsv1.StatefulSet{}
				g.Expect(k8sClient.Get(
					ctx,
					types.NamespacedName{Name: lws.Name, Namespace: ns.Name},
					leaderStatefulSet,
				)).To(gomega.Succeed())
				g.Expect(*leaderStatefulSet.Spec.Replicas).To(gomega.Equal(int32(2)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				workerStatefulSet := &appsv1.StatefulSet{}
				g.Expect(k8sClient.Get(
					ctx,
					// Worker statefulset name is formed by appending group index to the leaderworkerset name
					types.NamespacedName{Name: fmt.Sprintf("%s-0", lws.Name), Namespace: ns.Name},
					workerStatefulSet,
				)).To(gomega.Succeed())
				g.Expect(*workerStatefulSet.Spec.Replicas).To(gomega.Equal(int32(2)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

			createdWorkload := &kueue.Workload{}
			gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())

			util.ExpectObjectToBeDeleted(ctx, k8sClient, lws, true)
			util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, createdWorkload, false, util.LongTimeout)
		})
	})
})
