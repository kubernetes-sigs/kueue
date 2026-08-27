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
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	disaggregatedsetv1 "sigs.k8s.io/lws/api/disaggregatedset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	ctrlconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	disaggregatedsettesting "sigs.k8s.io/kueue/pkg/util/testingjobs/disaggregatedset"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("DisaggregatedSet integration", ginkgo.Label("area:singlecluster", "feature:disaggregatedset"), func() {
	var (
		ns                 *corev1.Namespace
		rf                 *kueue.ResourceFlavor
		cq                 *kueue.ClusterQueue
		lq                 *kueue.LocalQueue
		resourceFlavorName string
		clusterQueueName   string
		localQueueName     string
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "ds-e2e-")
		resourceFlavorName = "ds-rf-" + ns.Name
		clusterQueueName = "ds-cq-" + ns.Name
		localQueueName = "ds-lq-" + ns.Name

		rf = utiltestingapi.MakeResourceFlavor(resourceFlavorName).NodeLabel("instance-type", "on-demand").Obj()
		util.MustCreate(ctx, k8sClient, rf)

		cq = utiltestingapi.MakeClusterQueue(clusterQueueName).
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(resourceFlavorName).
					Resource(corev1.ResourceCPU, "10").
					Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

		lq = utiltestingapi.MakeLocalQueue(localQueueName, ns.Name).ClusterQueue(cq.Name).Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteAllDisaggregatedSetsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.When("DisaggregatedSet created", func() {
		ginkgo.It("should create workload and admit a basic 2-role DS", func() {
			ds := disaggregatedsettesting.MakeDisaggregatedSet("ds-basic", ns.Name).
				Role("prefill", 1, 1).
				Role("decode", 1, 1).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				TerminationGracePeriod(1).
				Queue(lq.Name).
				Obj()

			ginkgo.By("Create a DisaggregatedSet", func() {
				util.MustCreate(ctx, k8sClient, ds)
			})

			wlLookupKey := util.WorkloadKeyForDisaggregatedSet(ds)

			ginkgo.By("Checking that the workload is created and admitted", func() {
				util.ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sClient, wlLookupKey)
			})

			createdWorkload := &kueue.Workload{}
			ginkgo.By("Check workload has JobUID label", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					g.Expect(createdWorkload.Labels[ctrlconstants.JobUIDLabel]).To(gomega.Equal(string(ds.UID)))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Waiting for pods to be running", func() {
				pods := &corev1.PodList{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.MatchingLabels{
						disaggregatedsetv1.SetNameLabelKey: ds.Name,
					}, client.InNamespace(ds.Namespace))).Should(gomega.Succeed())
					g.Expect(pods.Items).NotTo(gomega.BeEmpty())
					for _, pod := range pods.Items {
						g.Expect(pod.Status.Phase).To(gomega.Equal(corev1.PodRunning))
					}
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Delete the DisaggregatedSet", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, ds, true)
			})

			ginkgo.By("Check pods are deleted", func() {
				pods := &corev1.PodList{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.MatchingLabels{
						disaggregatedsetv1.SetNameLabelKey: ds.Name,
					}, client.InNamespace(ds.Namespace))).Should(gomega.Succeed())
					g.Expect(pods.Items).To(gomega.BeEmpty())
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Check workload is deleted", func() {
				util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, createdWorkload, false, util.MediumTimeout)
			})
		})

		ginkgo.It("should admit DS with different resource shapes per role", func() {
			ds := disaggregatedsettesting.MakeDisaggregatedSet("ds-shapes", ns.Name).
				Role("prefill", 2, 1).
				RoleRequest(corev1.ResourceCPU, "100m").
				Role("decode", 1, 2).
				RoleRequest(corev1.ResourceCPU, "200m").
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				TerminationGracePeriod(1).
				Queue(lq.Name).
				Obj()

			ginkgo.By("Create a DisaggregatedSet", func() {
				util.MustCreate(ctx, k8sClient, ds)
			})

			wlLookupKey := util.WorkloadKeyForDisaggregatedSet(ds)

			ginkgo.By("Checking that the workload is created and admitted", func() {
				util.ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sClient, wlLookupKey)
			})

			createdWorkload := &kueue.Workload{}
			ginkgo.By("Check workload has correct PodSets", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					g.Expect(createdWorkload.Spec.PodSets).To(gomega.HaveLen(2))
					podSetCounts := make(map[string]int32, len(createdWorkload.Spec.PodSets))
					for _, ps := range createdWorkload.Spec.PodSets {
						podSetCounts[string(ps.Name)] = ps.Count
					}
					g.Expect(podSetCounts).To(gomega.Equal(map[string]int32{
						"decode-main":  2,
						"prefill-main": 2,
					}))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Waiting for pods to be running", func() {
				pods := &corev1.PodList{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.MatchingLabels{
						disaggregatedsetv1.SetNameLabelKey: ds.Name,
					}, client.InNamespace(ds.Namespace))).Should(gomega.Succeed())
					g.Expect(pods.Items).NotTo(gomega.BeEmpty())
					for _, pod := range pods.Items {
						g.Expect(pod.Status.Phase).To(gomega.Equal(corev1.PodRunning))
					}
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Delete the DisaggregatedSet", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, ds, true)
			})

			ginkgo.By("Check pods are deleted", func() {
				pods := &corev1.PodList{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.MatchingLabels{
						disaggregatedsetv1.SetNameLabelKey: ds.Name,
					}, client.InNamespace(ds.Namespace))).Should(gomega.Succeed())
					g.Expect(pods.Items).To(gomega.BeEmpty())
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Check workload is deleted", func() {
				util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, createdWorkload, false, util.MediumTimeout)
			})
		})

		ginkgo.It("should scale up replicas in a role", func() {
			ds := disaggregatedsettesting.MakeDisaggregatedSet("ds-scaleup", ns.Name).
				Role("prefill", 1, 1).
				Role("decode", 1, 1).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				TerminationGracePeriod(1).
				Queue(lq.Name).
				Obj()

			ginkgo.By("Create a DisaggregatedSet", func() {
				util.MustCreate(ctx, k8sClient, ds)
			})

			wlLookupKey := util.WorkloadKeyForDisaggregatedSet(ds)

			ginkgo.By("Checking that the workload is created and admitted", func() {
				util.ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sClient, wlLookupKey)
			})

			ginkgo.By("Waiting for pods to be running", func() {
				pods := &corev1.PodList{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.MatchingLabels{
						disaggregatedsetv1.SetNameLabelKey: ds.Name,
					}, client.InNamespace(ds.Namespace))).Should(gomega.Succeed())
					g.Expect(pods.Items).NotTo(gomega.BeEmpty())
					for _, pod := range pods.Items {
						g.Expect(pod.Status.Phase).To(gomega.Equal(corev1.PodRunning))
					}
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Scale up the decode role to 2 replicas", func() {
				createdDS := &disaggregatedsetv1.DisaggregatedSet{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ds), createdDS)).To(gomega.Succeed())
					for i := range createdDS.Spec.Roles {
						if createdDS.Spec.Roles[i].Name == "decode" {
							*createdDS.Spec.Roles[i].Spec.Replicas = 2
						}
					}
					g.Expect(k8sClient.Update(ctx, createdDS)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Check that the workload has updated PodSet counts after scale-up", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					wl := &kueue.Workload{}
					g.Expect(k8sClient.Get(ctx, wlLookupKey, wl)).To(gomega.Succeed())
					podSetCounts := make(map[string]int32, len(wl.Spec.PodSets))
					for _, ps := range wl.Spec.PodSets {
						podSetCounts[string(ps.Name)] = ps.Count
					}
					g.Expect(podSetCounts).To(gomega.Equal(map[string]int32{
						"decode-main":  2,
						"prefill-main": 1,
					}))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Waiting for scaled-up pods to be running", func() {
				pods := &corev1.PodList{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.MatchingLabels{
						disaggregatedsetv1.SetNameLabelKey: ds.Name,
					}, client.InNamespace(ds.Namespace))).Should(gomega.Succeed())
					g.Expect(pods.Items).To(gomega.HaveLen(3))
					for _, pod := range pods.Items {
						g.Expect(pod.Status.Phase).To(gomega.Equal(corev1.PodRunning))
					}
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Delete the DisaggregatedSet", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, ds, true)
			})

			ginkgo.By("Check pods are deleted", func() {
				pods := &corev1.PodList{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.MatchingLabels{
						disaggregatedsetv1.SetNameLabelKey: ds.Name,
					}, client.InNamespace(ds.Namespace))).Should(gomega.Succeed())
					g.Expect(pods.Items).To(gomega.BeEmpty())
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should scale down replicas in a role", func() {
			ds := disaggregatedsettesting.MakeDisaggregatedSet("ds-scaledown", ns.Name).
				Role("prefill", 1, 1).
				Role("decode", 2, 1).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				TerminationGracePeriod(1).
				Queue(lq.Name).
				Obj()

			ginkgo.By("Create a DisaggregatedSet", func() {
				util.MustCreate(ctx, k8sClient, ds)
			})

			wlLookupKey := util.WorkloadKeyForDisaggregatedSet(ds)

			ginkgo.By("Checking that the workload is created and admitted", func() {
				util.ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sClient, wlLookupKey)
			})

			ginkgo.By("Waiting for pods to be running", func() {
				pods := &corev1.PodList{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.MatchingLabels{
						disaggregatedsetv1.SetNameLabelKey: ds.Name,
					}, client.InNamespace(ds.Namespace))).Should(gomega.Succeed())
					g.Expect(pods.Items).NotTo(gomega.BeEmpty())
					for _, pod := range pods.Items {
						g.Expect(pod.Status.Phase).To(gomega.Equal(corev1.PodRunning))
					}
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Scale down the decode role to 1 replica", func() {
				createdDS := &disaggregatedsetv1.DisaggregatedSet{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ds), createdDS)).To(gomega.Succeed())
					for i := range createdDS.Spec.Roles {
						if createdDS.Spec.Roles[i].Name == "decode" {
							*createdDS.Spec.Roles[i].Spec.Replicas = 1
						}
					}
					g.Expect(k8sClient.Update(ctx, createdDS)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Check that the workload has updated PodSet counts after scale-down", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					wl := &kueue.Workload{}
					g.Expect(k8sClient.Get(ctx, wlLookupKey, wl)).To(gomega.Succeed())
					podSetCounts := make(map[string]int32, len(wl.Spec.PodSets))
					for _, ps := range wl.Spec.PodSets {
						podSetCounts[string(ps.Name)] = ps.Count
					}
					g.Expect(podSetCounts).To(gomega.Equal(map[string]int32{
						"decode-main":  1,
						"prefill-main": 1,
					}))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Check pod count decreased after scale down", func() {
				pods := &corev1.PodList{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.MatchingLabels{
						disaggregatedsetv1.SetNameLabelKey: ds.Name,
					}, client.InNamespace(ds.Namespace))).Should(gomega.Succeed())
					g.Expect(pods.Items).To(gomega.HaveLen(2))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Delete the DisaggregatedSet", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, ds, true)
			})

			ginkgo.By("Check pods are deleted", func() {
				pods := &corev1.PodList{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.MatchingLabels{
						disaggregatedsetv1.SetNameLabelKey: ds.Name,
					}, client.InNamespace(ds.Namespace))).Should(gomega.Succeed())
					g.Expect(pods.Items).To(gomega.BeEmpty())
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should cleanup workload when DS is deleted", func() {
			ds := disaggregatedsettesting.MakeDisaggregatedSet("ds-cleanup", ns.Name).
				Role("prefill", 1, 1).
				Role("decode", 1, 1).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				TerminationGracePeriod(1).
				Queue(lq.Name).
				Obj()

			ginkgo.By("Create a DisaggregatedSet", func() {
				util.MustCreate(ctx, k8sClient, ds)
			})

			wlLookupKey := util.WorkloadKeyForDisaggregatedSet(ds)

			ginkgo.By("Checking that the workload is created and admitted", func() {
				util.ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sClient, wlLookupKey)
			})

			createdWorkload := &kueue.Workload{}
			ginkgo.By("Check workload exists", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Delete the DisaggregatedSet", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, ds, true)
			})

			ginkgo.By("Check pods are deleted", func() {
				pods := &corev1.PodList{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.MatchingLabels{
						disaggregatedsetv1.SetNameLabelKey: ds.Name,
					}, client.InNamespace(ds.Namespace))).Should(gomega.Succeed())
					g.Expect(pods.Items).To(gomega.BeEmpty())
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Check workload is deleted", func() {
				wl := &kueue.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Name:      createdWorkload.Name,
						Namespace: createdWorkload.Namespace,
					},
				}
				util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, wl, false, util.MediumTimeout)
			})
		})
	})
})
