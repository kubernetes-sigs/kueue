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
	"k8s.io/component-base/featuregate"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadraycluster "sigs.k8s.io/kueue/pkg/controller/jobs/raycluster"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingraycluster "sigs.k8s.io/kueue/pkg/util/testingjobs/raycluster"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("RayCluster idle suspend", ginkgo.Label("job:ray", "area:jobs"), func() {
	var (
		ns             *corev1.Namespace
		spotFlavor     *kueue.ResourceFlavor
		onDemandFlavor *kueue.ResourceFlavor
		clusterQueue   *kueue.ClusterQueue
		localQueue     *kueue.LocalQueue
	)

	workloadKey := func(rayCluster *rayv1.RayCluster) client.ObjectKey {
		return client.ObjectKey{
			Name:      workloadraycluster.GetWorkloadNameForRayCluster(rayCluster.Name, rayCluster.UID),
			Namespace: rayCluster.Namespace,
		}
	}
	// setIdleSuspend emulates the Ray autoscaler, which sets spec.idleSuspend in
	// production, and the user who clears the field to resume the RayCluster.
	// envtest runs neither the KubeRay operator nor the autoscaler.
	setIdleSuspend := func(rayCluster *rayv1.RayCluster, idleSuspend bool) {
		ginkgo.GinkgoHelper()
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rayCluster), rayCluster)).Should(gomega.Succeed())
			rayCluster.Spec.IdleSuspend = new(idleSuspend)
			g.Expect(k8sClient.Update(ctx, rayCluster)).Should(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	}
	// expectStarted asserts that Kueue unsuspended the RayCluster with both
	// groups placed on the given flavor.
	expectStarted := func(rayCluster *rayv1.RayCluster, flavor *kueue.ResourceFlavor) {
		ginkgo.GinkgoHelper()
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rayCluster), rayCluster)).Should(gomega.Succeed())
			g.Expect(rayCluster.Spec.Suspend).Should(gomega.Equal(new(false)))
			g.Expect(rayCluster.Spec.HeadGroupSpec.Template.Spec.NodeSelector).Should(gomega.HaveKeyWithValue(instanceKey, flavor.Name))
			g.Expect(rayCluster.Spec.WorkerGroupSpecs[0].Template.Spec.NodeSelector).Should(gomega.HaveKeyWithValue(instanceKey, flavor.Name))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	}
	// expectStopped asserts that Kueue suspended the RayCluster and removed the
	// flavor node selectors from its Pod templates.
	expectStopped := func(rayCluster *rayv1.RayCluster) {
		ginkgo.GinkgoHelper()
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rayCluster), rayCluster)).Should(gomega.Succeed())
			g.Expect(rayCluster.Spec.Suspend).Should(gomega.Equal(new(true)))
			g.Expect(rayCluster.Spec.HeadGroupSpec.Template.Spec.NodeSelector).ShouldNot(gomega.HaveKey(instanceKey))
			g.Expect(rayCluster.Spec.WorkerGroupSpecs[0].Template.Spec.NodeSelector).ShouldNot(gomega.HaveKey(instanceKey))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	}
	// deleteRayCluster also deletes the Workload of the RayCluster, because there
	// is no garbage collection in integration tests.
	deleteRayCluster := func(rayCluster *rayv1.RayCluster) {
		ginkgo.GinkgoHelper()
		wl := &kueue.Workload{}
		gomega.Expect(k8sClient.Get(ctx, workloadKey(rayCluster), wl)).Should(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, rayCluster, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, wl, true)
	}
	expectOnHold := func(wlKey client.ObjectKey) {
		ginkgo.GinkgoHelper()
		gomega.Eventually(func(g gomega.Gomega) {
			wl := &kueue.Workload{}
			g.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
			g.Expect(wl.Status.Conditions).Should(utiltesting.HaveConditionStatusFalseAndReason(kueue.WorkloadQuotaReserved, kueue.WorkloadOnHold))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	}

	ginkgo.BeforeEach(func() {
		features.SetFeatureGatesDuringTest(ginkgo.GinkgoTB(), map[featuregate.Feature]bool{
			features.RayClusterIdleSuspend:        true,
			features.ElasticJobsViaWorkloadSlices: true,
		})
		fwk.StartManagerForSpec(ctx, cfg, managerAndSchedulerSetup())

		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "idle-suspend-")

		spotFlavor = utiltestingapi.MakeResourceFlavor("spot").NodeLabel(instanceKey, "spot").Obj()
		behavioral.MustCreate(ctx, k8sClient, spotFlavor)

		onDemandFlavor = utiltestingapi.MakeResourceFlavor("on-demand").NodeLabel(instanceKey, "on-demand").Obj()
		behavioral.MustCreate(ctx, k8sClient, onDemandFlavor)

		// CPU is the constrained resource. Memory is declared because the autoscaler
		// container of an elastic RayCluster requests it.
		clusterQueue = utiltestingapi.MakeClusterQueue("idle-suspend").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(spotFlavor.Name).
					Resource(corev1.ResourceCPU, "4").
					Resource(corev1.ResourceMemory, "5Gi").
					Obj(),
				*utiltestingapi.MakeFlavorQuotas(onDemandFlavor.Name).
					Resource(corev1.ResourceCPU, "4").
					Resource(corev1.ResourceMemory, "5Gi").
					Obj(),
			).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("idle-suspend", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, localQueue)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, spotFlavor, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
	})

	ginkgo.It("Should release the quota of an idle-suspended RayCluster and admit it again after it is resumed", func() {
		idleCluster := testingraycluster.MakeCluster("idle", ns.Name).
			Queue(localQueue.Name).
			RequestHead(corev1.ResourceCPU, "4").
			RequestWorkerGroup(corev1.ResourceCPU, "4").
			Obj()
		waitingCluster := testingraycluster.MakeCluster("waiting", ns.Name).
			Queue(localQueue.Name).
			RequestHead(corev1.ResourceCPU, "2").
			RequestWorkerGroup(corev1.ResourceCPU, "2").
			Obj()

		ginkgo.By("admitting a RayCluster that uses the quota of both flavors", func() {
			behavioral.MustCreate(ctx, k8sClient, idleCluster)
			behavioral.ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sClient, workloadKey(idleCluster))
		})

		ginkgo.By("creating a second RayCluster that has to wait for quota", func() {
			behavioral.MustCreate(ctx, k8sClient, waitingCluster)
			behavioral.ExpectWorkloadsToBePendingByKeys(ctx, k8sClient, workloadKey(waitingCluster))
		})

		ginkgo.By("idle-suspending the first RayCluster", func() {
			setIdleSuspend(idleCluster, true)
			expectStopped(idleCluster)
			expectOnHold(workloadKey(idleCluster))
		})

		ginkgo.By("checking the second RayCluster is admitted with the released quota", func() {
			behavioral.ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sClient, workloadKey(waitingCluster))
			expectStarted(waitingCluster, spotFlavor)
		})

		ginkgo.By("resuming the first RayCluster, which has to wait for quota", func() {
			setIdleSuspend(idleCluster, false)
			behavioral.ExpectWorkloadsToBePendingByKeys(ctx, k8sClient, workloadKey(idleCluster))
		})

		ginkgo.By("deleting the second RayCluster", func() {
			deleteRayCluster(waitingCluster)
		})

		ginkgo.By("checking the first RayCluster is admitted and started again", func() {
			behavioral.ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sClient, workloadKey(idleCluster))
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(idleCluster), idleCluster)).Should(gomega.Succeed())
				g.Expect(idleCluster.Spec.Suspend).Should(gomega.Equal(new(false)))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should start a resumed RayCluster on the flavor assigned at its new admission", func() {
		idleCluster := testingraycluster.MakeCluster("idle", ns.Name).
			Queue(localQueue.Name).
			RequestHead(corev1.ResourceCPU, "1").
			RequestWorkerGroup(corev1.ResourceCPU, "1").
			Obj()
		otherCluster := testingraycluster.MakeCluster("other", ns.Name).
			Queue(localQueue.Name).
			RequestHead(corev1.ResourceCPU, "2").
			RequestWorkerGroup(corev1.ResourceCPU, "2").
			Obj()

		ginkgo.By("admitting a RayCluster on the first flavor", func() {
			behavioral.MustCreate(ctx, k8sClient, idleCluster)
			expectStarted(idleCluster, spotFlavor)
		})

		ginkgo.By("idle-suspending the RayCluster", func() {
			setIdleSuspend(idleCluster, true)
			expectStopped(idleCluster)
			expectOnHold(workloadKey(idleCluster))
		})

		ginkgo.By("filling the first flavor with another RayCluster", func() {
			behavioral.MustCreate(ctx, k8sClient, otherCluster)
			expectStarted(otherCluster, spotFlavor)
		})

		ginkgo.By("resuming the RayCluster, which starts on the second flavor", func() {
			setIdleSuspend(idleCluster, false)
			expectStarted(idleCluster, onDemandFlavor)
		})
	})

	ginkgo.It("Should not admit a pending RayCluster while it is idle-suspended", func() {
		runningCluster := testingraycluster.MakeCluster("running", ns.Name).
			Queue(localQueue.Name).
			RequestHead(corev1.ResourceCPU, "4").
			RequestWorkerGroup(corev1.ResourceCPU, "4").
			Obj()
		pendingCluster := testingraycluster.MakeCluster("pending", ns.Name).
			Queue(localQueue.Name).
			RequestHead(corev1.ResourceCPU, "2").
			RequestWorkerGroup(corev1.ResourceCPU, "2").
			Obj()

		ginkgo.By("admitting a RayCluster that uses the quota of both flavors", func() {
			behavioral.MustCreate(ctx, k8sClient, runningCluster)
			behavioral.ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sClient, workloadKey(runningCluster))
		})

		ginkgo.By("creating a second RayCluster that has to wait for quota", func() {
			behavioral.MustCreate(ctx, k8sClient, pendingCluster)
			behavioral.ExpectWorkloadsToBePendingByKeys(ctx, k8sClient, workloadKey(pendingCluster))
		})

		ginkgo.By("idle-suspending the second RayCluster", func() {
			setIdleSuspend(pendingCluster, true)
			expectOnHold(workloadKey(pendingCluster))
		})

		ginkgo.By("deleting the first RayCluster", func() {
			deleteRayCluster(runningCluster)
		})

		ginkgo.By("checking the second RayCluster is not admitted with the released quota", func() {
			gomega.Consistently(func(g gomega.Gomega) {
				wl := &kueue.Workload{}
				g.Expect(k8sClient.Get(ctx, workloadKey(pendingCluster), wl)).Should(gomega.Succeed())
				g.Expect(wl.Status.Conditions).Should(utiltesting.HaveConditionStatusFalseAndReason(kueue.WorkloadQuotaReserved, kueue.WorkloadOnHold))
			}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
		})

		ginkgo.By("resuming the second RayCluster, which is admitted and started", func() {
			setIdleSuspend(pendingCluster, false)
			expectStarted(pendingCluster, spotFlavor)
		})
	})

	ginkgo.It("Should keep an idle-suspended RayCluster admitted when RayClusterIdleSuspend is disabled", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.RayClusterIdleSuspend, false)

		rayCluster := testingraycluster.MakeCluster("idle", ns.Name).
			Queue(localQueue.Name).
			RequestHead(corev1.ResourceCPU, "1").
			RequestWorkerGroup(corev1.ResourceCPU, "1").
			Obj()

		ginkgo.By("admitting a RayCluster", func() {
			behavioral.MustCreate(ctx, k8sClient, rayCluster)
			expectStarted(rayCluster, spotFlavor)
		})

		ginkgo.By("idle-suspending the RayCluster", func() {
			setIdleSuspend(rayCluster, true)
		})

		ginkgo.By("checking the RayCluster stays admitted", func() {
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rayCluster), rayCluster)).Should(gomega.Succeed())
				g.Expect(rayCluster.Spec.Suspend).Should(gomega.Equal(new(false)))
				wl := &kueue.Workload{}
				g.Expect(k8sClient.Get(ctx, workloadKey(rayCluster), wl)).Should(gomega.Succeed())
				g.Expect(wl.Status.Conditions).Should(utiltesting.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
			}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should release the quota of an idle-suspended elastic RayCluster and admit it again after it is resumed", func() {
		rayCluster := testingraycluster.MakeCluster("elastic", ns.Name).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			Queue(localQueue.Name).
			RequestHead(corev1.ResourceCPU, "1").
			RequestWorkerGroup(corev1.ResourceCPU, "1").
			WithEnableAutoscaling(new(true)).
			Obj()
		// The name of an elastic workload slice is not derived from the job alone.
		var wlKey client.ObjectKey

		ginkgo.By("admitting an elastic RayCluster", func() {
			behavioral.MustCreate(ctx, k8sClient, rayCluster)
			gomega.Eventually(func(g gomega.Gomega) {
				workloads := &kueue.WorkloadList{}
				g.Expect(k8sClient.List(ctx, workloads, client.InNamespace(ns.Name))).Should(gomega.Succeed())
				g.Expect(workloads.Items).Should(gomega.HaveLen(1))
				wlKey = client.ObjectKeyFromObject(&workloads.Items[0])
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			expectStarted(rayCluster, spotFlavor)
		})

		ginkgo.By("idle-suspending the RayCluster", func() {
			setIdleSuspend(rayCluster, true)
			expectStopped(rayCluster)
			expectOnHold(wlKey)
		})

		ginkgo.By("resuming the RayCluster, which is admitted and started again", func() {
			setIdleSuspend(rayCluster, false)
			behavioral.ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sClient, wlKey)
			expectStarted(rayCluster, spotFlavor)
		})
	})

	ginkgo.It("Should release the quota of an idle-suspended elastic RayCluster that has a pending scale-up", func() {
		rayCluster := testingraycluster.MakeCluster("elastic", ns.Name).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			Queue(localQueue.Name).
			RequestHead(corev1.ResourceCPU, "1").
			RequestWorkerGroup(corev1.ResourceCPU, "1").
			WithEnableAutoscaling(new(true)).
			Obj()
		// Uses the quota that the elastic RayCluster leaves on both flavors.
		otherCluster := testingraycluster.MakeCluster("other", ns.Name).
			Queue(localQueue.Name).
			RequestHead(corev1.ResourceCPU, "1500m").
			RequestWorkerGroup(corev1.ResourceCPU, "4").
			Obj()
		// The name of an elastic workload slice is not derived from the job alone.
		var admittedSliceKey, pendingSliceKey client.ObjectKey

		ginkgo.By("admitting an elastic RayCluster", func() {
			behavioral.MustCreate(ctx, k8sClient, rayCluster)
			gomega.Eventually(func(g gomega.Gomega) {
				workloads := &kueue.WorkloadList{}
				g.Expect(k8sClient.List(ctx, workloads, client.InNamespace(ns.Name))).Should(gomega.Succeed())
				g.Expect(workloads.Items).Should(gomega.HaveLen(1))
				admittedSliceKey = client.ObjectKeyFromObject(&workloads.Items[0])
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			expectStarted(rayCluster, spotFlavor)
		})

		ginkgo.By("filling the rest of the quota with another RayCluster", func() {
			behavioral.MustCreate(ctx, k8sClient, otherCluster)
			behavioral.ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sClient, workloadKey(otherCluster))
		})

		ginkgo.By("scaling up the elastic RayCluster, whose new workload slice has to wait for quota", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rayCluster), rayCluster)).Should(gomega.Succeed())
				rayCluster.Spec.WorkerGroupSpecs[0].Replicas = new(int32(2))
				g.Expect(k8sClient.Update(ctx, rayCluster)).Should(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				workloads := &kueue.WorkloadList{}
				g.Expect(k8sClient.List(ctx, workloads, client.InNamespace(ns.Name))).Should(gomega.Succeed())
				for i := range workloads.Items {
					if workloadslicing.ScaledUp(&workloads.Items[i]) {
						pendingSliceKey = client.ObjectKeyFromObject(&workloads.Items[i])
					}
				}
				g.Expect(pendingSliceKey.Name).ShouldNot(gomega.BeEmpty())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			behavioral.ExpectWorkloadsToBePendingByKeys(ctx, k8sClient, pendingSliceKey)
		})

		ginkgo.By("idle-suspending the elastic RayCluster", func() {
			setIdleSuspend(rayCluster, true)
			expectStopped(rayCluster)
			expectOnHold(pendingSliceKey)
		})

		ginkgo.By("checking the quota of the admitted workload slice is released", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				wl := &kueue.Workload{}
				g.Expect(k8sClient.Get(ctx, admittedSliceKey, wl)).Should(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(wl)).Should(gomega.BeFalse())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("deleting the other RayCluster", func() {
			deleteRayCluster(otherCluster)
		})

		ginkgo.By("resuming the elastic RayCluster, which is admitted with two workers and started", func() {
			setIdleSuspend(rayCluster, false)
			// Either workload slice can be the one that is kept for the RayCluster.
			gomega.Eventually(func(g gomega.Gomega) {
				workloads := &kueue.WorkloadList{}
				g.Expect(k8sClient.List(ctx, workloads, client.InNamespace(ns.Name))).Should(gomega.Succeed())
				g.Expect(workloads.Items).Should(gomega.ContainElement(gomega.Satisfy(func(wl kueue.Workload) bool {
					return workload.IsAdmitted(&wl) && wl.Spec.PodSets[1].Count == 2
				})))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			expectStarted(rayCluster, spotFlavor)
		})
	})
})
