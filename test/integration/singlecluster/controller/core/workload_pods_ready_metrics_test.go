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

package core

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
	workloadpatching "sigs.k8s.io/kueue/pkg/workload/patching"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/integration"
)

var _ = ginkgo.Describe("Workload PodsReadyActiveWorkloads metrics", ginkgo.Label("controller:workload", "area:core"), func() {
	var (
		ns           *corev1.Namespace
		clusterQueue *kueue.ClusterQueue
		localQueue   *kueue.LocalQueue
		flavor       *kueue.ResourceFlavor
		check        *kueue.AdmissionCheck
	)

	ginkgo.BeforeEach(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.LocalQueueMetrics, true)
		fwk.StartManager(ctx, cfg, managerSetup)
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "pods-ready-metrics-")
		flavor = utiltestingapi.MakeResourceFlavor(flavorOnDemand).Obj()
		behavioral.MustCreate(ctx, k8sClient, flavor)

		check = utiltestingapi.MakeAdmissionCheck("check-pods-ready").ControllerName("ctrl").Obj()
		behavioral.MustCreate(ctx, k8sClient, check)
		behavioral.SetAdmissionCheckActive(ctx, k8sClient, check, metav1.ConditionTrue)

		clusterQueue = utiltestingapi.MakeClusterQueue("cq-pods-ready").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavorOnDemand).Resource(corev1.ResourceCPU, "2").Obj()).
			AdmissionCheckStrategy(kueue.AdmissionCheckStrategyRule{Name: "check-pods-ready"}).
			Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("lq-pods-ready", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, check, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
		fwk.StopManager(ctx)
	})

	ginkgo.It("should report CQ and LQ pods_ready_active_workloads across reservation, admission, PodsReady flapping, eviction, readmission, and finish", func() {
		cqRef := kueue.ClusterQueueReference(clusterQueue.Name)
		wl := utiltestingapi.MakeWorkload("wl-pods-ready", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Request(corev1.ResourceCPU, "1").
			Obj()
		wlKey := client.ObjectKeyFromObject(wl)
		createdWl := kueue.Workload{}

		setPodsReady := func(status metav1.ConditionStatus) {
			ginkgo.GinkgoHelper()
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, &createdWl)).To(gomega.Succeed())
				apimeta.SetStatusCondition(&createdWl.Status.Conditions, metav1.Condition{
					Type:               kueue.WorkloadPodsReady,
					Status:             status,
					LastTransitionTime: metav1.Now(),
					Reason:             "PodsReady",
					Message:            "Updated by test",
				})
				g.Expect(k8sClient.Status().Update(ctx, &createdWl)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		}

		setCheckReady := func() {
			ginkgo.GinkgoHelper()
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, &createdWl)).To(gomega.Succeed())
				workloadpatching.SetAdmissionCheckState(&createdWl.Status.AdmissionChecks, kueue.AdmissionCheckState{
					Name:    "check-pods-ready",
					State:   kueue.CheckStateReady,
					Message: "check ready",
				}, behavioral.RealClock)
				g.Expect(k8sClient.Status().Update(ctx, &createdWl)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		}

		ginkgo.By("creating the workload and waiting for its admission check to be initialized", func() {
			behavioral.MustCreate(ctx, k8sClient, wl)
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, &createdWl)).To(gomega.Succeed())
				g.Expect(createdWl.Status.AdmissionChecks).To(gomega.HaveLen(1))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		podSet := kueue.PodSetAssignment{
			Name: kueue.DefaultPodSetName,
			Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
				corev1.ResourceCPU: kueue.ResourceFlavorReference(flavor.Name),
			},
			ResourceUsage: corev1.ResourceList{
				corev1.ResourceCPU: wl.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU],
			},
		}
		admission := utiltestingapi.MakeAdmission(cqRef).PodSets(podSet).Obj()

		ginkgo.By("reserving quota while admission check is still Pending even with stale PodsReady=True", func() {
			integration.SetQuotaReservation(ctx, k8sClient, wlKey, admission)
			setPodsReady(metav1.ConditionTrue)
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, &createdWl)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(&createdWl)).To(gomega.BeFalse())
			}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
			behavioral.ExpectPodsReadyActiveWorkloadsGaugeMetric(cqRef, 0)
			behavioral.ExpectLQPodsReadyActiveWorkloadsGaugeMetric(localQueue, 0)
		})

		ginkgo.By("clearing PodsReady to False and marking admission check Ready to admit the workload", func() {
			setPodsReady(metav1.ConditionFalse)
			setCheckReady()
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, &createdWl)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(&createdWl)).To(gomega.BeTrue())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			behavioral.ExpectAdmittedActiveWorkloadsGaugeMetric(cqRef, 1)
			behavioral.ExpectLQAdmittedActiveWorkloadsGaugeMetric(localQueue, 1)
			behavioral.ExpectPodsReadyActiveWorkloadsGaugeMetric(cqRef, 0)
			behavioral.ExpectLQPodsReadyActiveWorkloadsGaugeMetric(localQueue, 0)
		})

		ginkgo.By("transitioning PodsReady False -> True -> False -> True", func() {
			setPodsReady(metav1.ConditionTrue)
			behavioral.ExpectPodsReadyActiveWorkloadsGaugeMetric(cqRef, 1)
			behavioral.ExpectLQPodsReadyActiveWorkloadsGaugeMetric(localQueue, 1)

			setPodsReady(metav1.ConditionFalse)
			behavioral.ExpectAdmittedActiveWorkloadsGaugeMetric(cqRef, 1)
			behavioral.ExpectLQAdmittedActiveWorkloadsGaugeMetric(localQueue, 1)
			behavioral.ExpectPodsReadyActiveWorkloadsGaugeMetric(cqRef, 0)
			behavioral.ExpectLQPodsReadyActiveWorkloadsGaugeMetric(localQueue, 0)

			setPodsReady(metav1.ConditionTrue)
			behavioral.ExpectPodsReadyActiveWorkloadsGaugeMetric(cqRef, 1)
			behavioral.ExpectLQPodsReadyActiveWorkloadsGaugeMetric(localQueue, 1)
		})

		ginkgo.By("evicting the running workload and verifying gauges drop to 0", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, &createdWl)).To(gomega.Succeed())
				g.Expect(workloadpatching.PatchAdmissionStatus(ctx, k8sClient, &createdWl, behavioral.RealClock, func(wl *kueue.Workload) (bool, error) {
					return workloadevict.SetEvictedCondition(wl, behavioral.RealClock.Now(), "ByTest", "by test"), nil
				})).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			behavioral.FinishEvictionForWorkloads(ctx, k8sClient, wl)
			behavioral.ExpectPodsReadyActiveWorkloadsGaugeMetric(cqRef, 0)
			behavioral.ExpectLQPodsReadyActiveWorkloadsGaugeMetric(localQueue, 0)
		})

		ginkgo.By("readmitting the workload and marking PodsReady=True again", func() {
			integration.SetQuotaReservation(ctx, k8sClient, wlKey, admission)
			setCheckReady()
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, &createdWl)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(&createdWl)).To(gomega.BeTrue())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			setPodsReady(metav1.ConditionTrue)
			behavioral.ExpectPodsReadyActiveWorkloadsGaugeMetric(cqRef, 1)
			behavioral.ExpectLQPodsReadyActiveWorkloadsGaugeMetric(localQueue, 1)
		})

		ginkgo.By("finishing the workload and verifying gauges drop to 0", func() {
			integration.FinishWorkloads(ctx, k8sClient, wl)
			behavioral.ExpectPodsReadyActiveWorkloadsGaugeMetric(cqRef, 0)
			behavioral.ExpectLQPodsReadyActiveWorkloadsGaugeMetric(localQueue, 0)
		})
	})
})
