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

package fairsharing

import (
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/metrics"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Cohorts", func() {
	var (
		defaultFlavor *kueue.ResourceFlavor
		flavor1       *kueue.ResourceFlavor
		flavor2       *kueue.ResourceFlavor
		ns            *corev1.Namespace

		cohorts map[string]*kueue.Cohort
		cqs     map[string]*kueue.ClusterQueue
		lqs     map[string]*kueue.LocalQueue
	)

	var createCohort = func(cohort *kueue.Cohort) *kueue.Cohort {
		util.MustCreate(ctx, k8sClient, cohort)
		cohorts[cohort.Name] = cohort
		return cohort
	}

	var createQueue = func(cq *kueue.ClusterQueue) *kueue.ClusterQueue {
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)
		cqs[cq.Name] = cq

		lq := utiltestingapi.MakeLocalQueue(cq.Name, ns.Name).ClusterQueue(cq.Name).Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
		lqs[lq.Name] = lq
		return cq
	}

	createWorkload := func(lqName string, cpu string) *kueue.Workload {
		wl := utiltestingapi.MakeWorkloadWithGeneratedName("usage-", ns.Name).
			Queue(kueue.LocalQueueName(lqName)).
			Request(corev1.ResourceCPU, cpu).
			Obj()
		util.MustCreate(ctx, k8sClient, wl)
		return wl
	}

	withLending := func(fName kueue.ResourceFlavorReference, nominal, lending string) kueue.FlavorQuotas {
		fq := *utiltestingapi.MakeFlavorQuotas(string(fName)).
			Resource(corev1.ResourceCPU, nominal).
			Obj()
		fq.Resources[0].LendingLimit = ptr.To(resource.MustParse(lending))
		return fq
	}

	ginkgo.When("creating, modifying and removing", func() {
		ginkgo.BeforeEach(func() {
			cohorts = make(map[string]*kueue.Cohort)
			cqs = make(map[string]*kueue.ClusterQueue)
			lqs = make(map[string]*kueue.LocalQueue)

			fwk.StartManager(ctx, cfg, managerAndSchedulerSetup(
				&config.AdmissionFairSharing{
					UsageHalfLifeTime: metav1.Duration{
						Duration: 1 * time.Second,
					},
					UsageSamplingInterval: metav1.Duration{
						Duration: 1 * time.Second,
					},
				},
			))
			defaultFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
			util.MustCreate(ctx, k8sClient, defaultFlavor)
			flavor1 = utiltestingapi.MakeResourceFlavor("flavor1").Obj()
			util.MustCreate(ctx, k8sClient, flavor1)
			flavor2 = utiltestingapi.MakeResourceFlavor("flavor2").Obj()
			util.MustCreate(ctx, k8sClient, flavor2)

			ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-")
		})

		ginkgo.AfterEach(func() {
			for _, lq := range lqs {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, lq, true)
			}
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			for _, cq := range cqs {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			}
			for _, cohort := range cohorts {
				metrics.ClearCohortMetrics(cohort.Name)
				util.ExpectObjectToBeDeleted(ctx, k8sClient, cohort, true)
			}
			util.ExpectObjectToBeDeleted(ctx, k8sClient, defaultFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, flavor1, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, flavor2, true)
			fwk.StopManager(ctx)
		})

		ginkgo.It("follows reporting correct metrics", func() {
			ginkgo.By("Creating initial ClusterQueue cq1, with no cohort", func() {
				createQueue(utiltestingapi.MakeClusterQueue("cq1").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas(defaultFlavor.Name).
							Resource(corev1.ResourceCPU, "10").
							Resource(corev1.ResourceMemory, "10Gi").
							Obj(),
					).Obj())

				// no metrics for the ch1 cohort
				util.ExpectCohortSubtreeQuotaGaugeMetricCleaned("ch1", defaultFlavor.Name, corev1.ResourceCPU.String())
				util.ExpectCohortSubtreeQuotaGaugeMetricCleaned("ch1", defaultFlavor.Name, corev1.ResourceMemory.String())
			})

			ginkgo.By("Setting cq1 cohort to ch1", func() {
				var cq kueue.ClusterQueue
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cqs["cq1"]), &cq)).To(gomega.Succeed())
					cq.Spec.CohortName = "ch1"
					g.Expect(k8sClient.Update(ctx, &cq)).To(gomega.Succeed())
				}, util.Timeout, util.ShortInterval).Should(gomega.Succeed())

				util.ExpectCohortSubtreeQuotaGaugeMetric("ch1", defaultFlavor.Name, corev1.ResourceCPU.String(), 10_000)
				util.ExpectCohortSubtreeQuotaGaugeMetric("ch1", defaultFlavor.Name, corev1.ResourceMemory.String(), util.ResourceQtyToFloat64("10Gi"))
			})

			ginkgo.By("Creating ClusterQueue cq2 and cohort ch1", func() {
				createCohort(utiltestingapi.MakeCohort("ch1").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas(defaultFlavor.Name).
							Resource(corev1.ResourceCPU, "15").
							Resource(corev1.ResourceMemory, "15Gi").
							Obj(),
					).Obj())

				createQueue(utiltestingapi.MakeClusterQueue("cq2").
					Cohort("ch1").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas(defaultFlavor.Name).
							Resource(corev1.ResourceCPU, "5").
							Resource(corev1.ResourceMemory, "5Gi").
							Obj(),
					).Obj())

				// combined values of resources of cq1(10 cpu, 10Gi) and cq2(5 cpu, 5Gi) and ch1 cohort(15 cpu, 15Gi)
				util.ExpectCohortSubtreeQuotaGaugeMetric("ch1", defaultFlavor.Name, corev1.ResourceCPU.String(), 30_000)
				util.ExpectCohortSubtreeQuotaGaugeMetric("ch1", defaultFlavor.Name, corev1.ResourceMemory.String(), util.ResourceQtyToFloat64("30Gi"))
			})

			ginkgo.By("Creating ClusterQueues cqd and cqe and its parent cohort ch2", func() {
				createCohort(utiltestingapi.MakeCohort("ch2").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas(defaultFlavor.Name).
							Resource(corev1.ResourceCPU, "10").
							Resource(corev1.ResourceMemory, "10Gi").
							Obj(),
					).Obj())

				createQueue(utiltestingapi.MakeClusterQueue("cqd").
					Cohort("ch2").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas(defaultFlavor.Name).
							Resource(corev1.ResourceCPU, "5").
							Resource(corev1.ResourceMemory, "5Gi").
							Obj(),
					).Obj())

				createQueue(utiltestingapi.MakeClusterQueue("cqe").
					Cohort("ch2").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas(defaultFlavor.Name).
							Resource(corev1.ResourceCPU, "5").
							Resource(corev1.ResourceMemory, "5Gi").
							Obj(),
					).Obj())

				// combined values of resources of cqd(5 cpu, 5Gi) and cqe(5 cpu, 5Gi) and ch2 cohort(10 cpu, 10Gi)
				util.ExpectCohortSubtreeQuotaGaugeMetric("ch2", defaultFlavor.Name, corev1.ResourceCPU.String(), 20_000)
				util.ExpectCohortSubtreeQuotaGaugeMetric("ch2", defaultFlavor.Name, corev1.ResourceMemory.String(), util.ResourceQtyToFloat64("20Gi"))
			})

			ginkgo.By("Creating root cohort with 20 CPUs and 5 GPU, and make ch1 and ch2 children of root", func() {
				createCohort(utiltestingapi.MakeCohort("root").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas(flavor1.Name).
							Resource(corev1.ResourceCPU, "20").
							Resource("nvidia.com/gpu", "5").
							Obj(),
					).Obj())

				var ch1, ch2 kueue.Cohort
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: "ch1"}, &ch1)).To(gomega.Succeed())
					ch1.Spec.ParentName = "root"
					g.Expect(k8sClient.Update(ctx, &ch1)).To(gomega.Succeed())

					g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: "ch2"}, &ch2)).To(gomega.Succeed())
					ch2.Spec.ParentName = "root"
					g.Expect(k8sClient.Update(ctx, &ch2)).To(gomega.Succeed())
				}, util.Timeout, util.ShortInterval).Should(gomega.Succeed())

				// combined values of resources of ch1(30 cpu, 30Gi) and ch2(20 cpu, 20Gi) and root cohort flavor1 (20 cpu, 5 gpu)
				util.ExpectCohortSubtreeQuotaGaugeMetric("root", defaultFlavor.Name, corev1.ResourceCPU.String(), 50_000)
				util.ExpectCohortSubtreeQuotaGaugeMetric("root", defaultFlavor.Name, corev1.ResourceMemory.String(), util.ResourceQtyToFloat64("50Gi"))
				util.ExpectCohortSubtreeQuotaGaugeMetric("root", flavor1.Name, corev1.ResourceCPU.String(), 20_000)
				util.ExpectCohortSubtreeQuotaGaugeMetric("root", flavor1.Name, "nvidia.com/gpu", 5)

				util.ExpectCohortSubtreeQuotaGaugeMetricCleaned("root", flavor2.Name, corev1.ResourceCPU.String())
				util.ExpectCohortSubtreeQuotaGaugeMetricCleaned("root", flavor2.Name, corev1.ResourceMemory.String())
			})

			ginkgo.By("Creating cohort ch3 with 5 CPUs and 1 GPU, and make it child of root, but without any ClusterQueue", func() {
				createCohort(utiltestingapi.MakeCohort("ch3").
					Parent("root").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas(flavor1.Name).
							Resource(corev1.ResourceCPU, "5").
							Resource("nvidia.com/gpu", "1").
							Obj(),
					).Obj())

				util.ExpectCohortSubtreeQuotaGaugeMetric("ch3", flavor1.Name, corev1.ResourceCPU.String(), 5_000)
				util.ExpectCohortSubtreeQuotaGaugeMetric("ch3", flavor1.Name, "nvidia.com/gpu", 1)

				util.ExpectCohortSubtreeQuotaGaugeMetric("root", defaultFlavor.Name, corev1.ResourceCPU.String(), 50_000)
				util.ExpectCohortSubtreeQuotaGaugeMetric("root", defaultFlavor.Name, corev1.ResourceMemory.String(), util.ResourceQtyToFloat64("50Gi"))
				util.ExpectCohortSubtreeQuotaGaugeMetric("root", flavor1.Name, corev1.ResourceCPU.String(), 25_000)
				util.ExpectCohortSubtreeQuotaGaugeMetric("root", flavor1.Name, "nvidia.com/gpu", 6)
			})

			ginkgo.By("Creating ClusterQueue cqg as child of cohort ch3, with 1 CPU and 1Gi", func() {
				createQueue(utiltestingapi.MakeClusterQueue("cqg").
					Cohort("ch3").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas(flavor2.Name).
							Resource(corev1.ResourceCPU, "1").
							Resource(corev1.ResourceMemory, "1Gi").
							Obj(),
					).Obj())

				// combined values of resources of cqg(1 cpu, 1Gi) and ch3 cohort(5 cpu, 1 gpu)
				util.ExpectCohortSubtreeQuotaGaugeMetric("ch3", flavor1.Name, corev1.ResourceCPU.String(), 5_000)
				util.ExpectCohortSubtreeQuotaGaugeMetric("ch3", flavor1.Name, "nvidia.com/gpu", 1)
				util.ExpectCohortSubtreeQuotaGaugeMetric("ch3", flavor2.Name, corev1.ResourceCPU.String(), 1_000)
				util.ExpectCohortSubtreeQuotaGaugeMetric("ch3", flavor2.Name, corev1.ResourceMemory.String(), 1.073741824e+09)

				// updated combined values of resources of ch1(30 cpu, 30Gi) and ch2(20 cpu, 20Gi) and ch3 flavor2 (6 cpu, 1 gpu) and root cohort flavor1 (20 cpu, 5 gpu)
				util.ExpectCohortSubtreeQuotaGaugeMetric("root", defaultFlavor.Name, corev1.ResourceCPU.String(), 50_000)
				util.ExpectCohortSubtreeQuotaGaugeMetric("root", defaultFlavor.Name, corev1.ResourceMemory.String(), util.ResourceQtyToFloat64("50Gi"))
				util.ExpectCohortSubtreeQuotaGaugeMetric("root", flavor1.Name, corev1.ResourceCPU.String(), 25_000)
				util.ExpectCohortSubtreeQuotaGaugeMetric("root", flavor1.Name, "nvidia.com/gpu", 6)
				util.ExpectCohortSubtreeQuotaGaugeMetric("root", flavor2.Name, corev1.ResourceCPU.String(), 1_000)
				util.ExpectCohortSubtreeQuotaGaugeMetric("root", flavor2.Name, corev1.ResourceMemory.String(), util.ResourceQtyToFloat64("1Gi"))
			})

			ginkgo.By("Deleting ClusterQueue cqg", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, cqs["cqg"], true)

				// updated values for ch3 with cqg removed, and root with ch3 updated
				util.ExpectCohortSubtreeQuotaGaugeMetric("ch3", flavor1.Name, corev1.ResourceCPU.String(), 5_000)
				util.ExpectCohortSubtreeQuotaGaugeMetric("ch3", flavor1.Name, "nvidia.com/gpu", 1)
				util.ExpectCohortSubtreeQuotaGaugeMetricCleaned("ch3", flavor2.Name, corev1.ResourceCPU.String())
				util.ExpectCohortSubtreeQuotaGaugeMetricCleaned("ch3", flavor2.Name, corev1.ResourceMemory.String())

				util.ExpectCohortSubtreeQuotaGaugeMetric("root", defaultFlavor.Name, corev1.ResourceCPU.String(), 50_000)
				util.ExpectCohortSubtreeQuotaGaugeMetric("root", defaultFlavor.Name, corev1.ResourceMemory.String(), util.ResourceQtyToFloat64("50Gi"))
				util.ExpectCohortSubtreeQuotaGaugeMetric("root", flavor1.Name, corev1.ResourceCPU.String(), 25_000)
				util.ExpectCohortSubtreeQuotaGaugeMetric("root", flavor1.Name, "nvidia.com/gpu", 6)

				// root metrics for flavor2 with cqg removed
				util.ExpectCohortSubtreeQuotaGaugeMetricCleaned("root", flavor2.Name, corev1.ResourceCPU.String())
				util.ExpectCohortSubtreeQuotaGaugeMetricCleaned("root", flavor2.Name, corev1.ResourceMemory.String())
			})

			ginkgo.By("Re-assign cohort ch2 cluster queues to ch3", func() {
				var cqD, cqE kueue.ClusterQueue
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cqs["cqd"]), &cqD)).To(gomega.Succeed())
					cqD.Spec.CohortName = "ch3"
					g.Expect(k8sClient.Update(ctx, &cqD)).To(gomega.Succeed())
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cqs["cqe"]), &cqE)).To(gomega.Succeed())
					cqE.Spec.CohortName = "ch3"
					g.Expect(k8sClient.Update(ctx, &cqE)).To(gomega.Succeed())
				}, util.Timeout, util.ShortInterval).Should(gomega.Succeed())

				// updated values for ch3 with cqd and cqe added, and root with ch3 updated
				util.ExpectCohortSubtreeQuotaGaugeMetric("ch3", defaultFlavor.Name, corev1.ResourceCPU.String(), 10_000)
				util.ExpectCohortSubtreeQuotaGaugeMetric("ch3", defaultFlavor.Name, corev1.ResourceMemory.String(), util.ResourceQtyToFloat64("10Gi"))

				util.ExpectCohortSubtreeQuotaGaugeMetric("root", defaultFlavor.Name, corev1.ResourceCPU.String(), 50_000)
				util.ExpectCohortSubtreeQuotaGaugeMetric("root", defaultFlavor.Name, corev1.ResourceMemory.String(), util.ResourceQtyToFloat64("50Gi"))
				util.ExpectCohortSubtreeQuotaGaugeMetric("root", flavor1.Name, corev1.ResourceCPU.String(), 25_000)
				util.ExpectCohortSubtreeQuotaGaugeMetric("root", flavor1.Name, "nvidia.com/gpu", 6)
			})

			ginkgo.By("Deleting cohort ch2", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, cohorts["ch2"], true)

				// updated values for ch3 with cqd and cqe added, and root with ch3 updated
				util.ExpectCohortSubtreeQuotaGaugeMetric("ch3", defaultFlavor.Name, corev1.ResourceCPU.String(), 10_000)
				util.ExpectCohortSubtreeQuotaGaugeMetric("ch3", defaultFlavor.Name, corev1.ResourceMemory.String(), util.ResourceQtyToFloat64("10Gi"))

				// root metrics with ch2 removed, so only ch1 and ch3 values
				util.ExpectCohortSubtreeQuotaGaugeMetric("root", defaultFlavor.Name, corev1.ResourceCPU.String(), 40_000)
				util.ExpectCohortSubtreeQuotaGaugeMetric("root", defaultFlavor.Name, corev1.ResourceMemory.String(), util.ResourceQtyToFloat64("40Gi"))
				util.ExpectCohortSubtreeQuotaGaugeMetric("root", flavor1.Name, corev1.ResourceCPU.String(), 25_000)
				util.ExpectCohortSubtreeQuotaGaugeMetric("root", flavor1.Name, "nvidia.com/gpu", 6)

				// ch2 metrics removed
				util.ExpectCohortSubtreeQuotaGaugeMetricCleaned("ch2", defaultFlavor.Name, corev1.ResourceCPU.String())
				util.ExpectCohortSubtreeQuotaGaugeMetricCleaned("ch2", defaultFlavor.Name, corev1.ResourceMemory.String())
			})

			ginkgo.By("Changing ch1 quota to 15 CPUs", func() {
				var ch1 kueue.Cohort

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: "ch1"}, &ch1)).To(gomega.Succeed())
					ch1Flavors := ch1.Spec.ResourceGroups[0].Flavors
					ch1Flavors[0] = kueue.FlavorQuotas{
						Name: kueue.ResourceFlavorReference(defaultFlavor.Name),
						Resources: []kueue.ResourceQuota{
							{
								Name:         corev1.ResourceCPU,
								NominalQuota: resource.MustParse("20"),
							},
							{
								Name:         corev1.ResourceMemory,
								NominalQuota: resource.MustParse("15Gi"),
							},
						},
					}
					ch1.Spec.ResourceGroups[0].Flavors = ch1Flavors
					g.Expect(k8sClient.Update(ctx, &ch1)).To(gomega.Succeed())
				}, util.Timeout, util.ShortInterval).Should(gomega.Succeed())

				util.ExpectCohortSubtreeQuotaGaugeMetric("ch1", defaultFlavor.Name, corev1.ResourceCPU.String(), 35_000)
				util.ExpectCohortSubtreeQuotaGaugeMetric("ch1", defaultFlavor.Name, corev1.ResourceMemory.String(), util.ResourceQtyToFloat64("30Gi"))

				// root metrics updated with new quotas of ch1
				util.ExpectCohortSubtreeQuotaGaugeMetric("root", defaultFlavor.Name, corev1.ResourceCPU.String(), 45_000)
				util.ExpectCohortSubtreeQuotaGaugeMetric("root", defaultFlavor.Name, corev1.ResourceMemory.String(), util.ResourceQtyToFloat64("40Gi"))
				util.ExpectCohortSubtreeQuotaGaugeMetric("root", flavor1.Name, corev1.ResourceCPU.String(), 25_000)
				util.ExpectCohortSubtreeQuotaGaugeMetric("root", flavor1.Name, "nvidia.com/gpu", 6)
			})
		})
		ginkgo.It("correctly handles cohort metrics when clusterQueue moves between cohorts", func() {
			ginkgo.By("Creating cohort ch1 with 5 CPUs and 1 GPU, and cohort ch2 with 4 CPUs and 1 GPU", func() {
				createCohort(utiltestingapi.MakeCohort("ch1").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas(flavor1.Name).
							Resource(corev1.ResourceCPU, "5").
							Resource("nvidia.com/gpu", "1").
							Obj(),
					).Obj())

				createCohort(utiltestingapi.MakeCohort("ch2").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas(flavor1.Name).
							Resource(corev1.ResourceCPU, "3").
							Resource("nvidia.com/gpu", "1").
							Obj(),
					).Obj())

				util.ExpectCohortSubtreeQuotaGaugeMetric("ch1", flavor1.Name, corev1.ResourceCPU.String(), 5_000)
				util.ExpectCohortSubtreeQuotaGaugeMetric("ch1", flavor1.Name, "nvidia.com/gpu", 1)

				util.ExpectCohortSubtreeQuotaGaugeMetric("ch2", flavor1.Name, corev1.ResourceCPU.String(), 3_000)
				util.ExpectCohortSubtreeQuotaGaugeMetric("ch2", flavor1.Name, "nvidia.com/gpu", 1)
			})

			ginkgo.By("Create cluster queue cq1 under implicit cohort ch0 with 1 CPU and 1 GPU", func() {
				createQueue(utiltestingapi.MakeClusterQueue("cq1").
					Cohort("ch0").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas(flavor1.Name).
							Resource(corev1.ResourceCPU, "1").
							Resource("nvidia.com/gpu", "1").
							Obj(),
					).Obj())

				util.ExpectCohortSubtreeQuotaGaugeMetric("ch0", flavor1.Name, corev1.ResourceCPU.String(), 1_000)
				util.ExpectCohortSubtreeQuotaGaugeMetric("ch0", flavor1.Name, "nvidia.com/gpu", 1)
			})

			ginkgo.By("Re-assign cq1 from ch0 to ch1", func() {
				var cq1 kueue.ClusterQueue
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cqs["cq1"]), &cq1)).To(gomega.Succeed())
					cq1.Spec.CohortName = "ch1"
					g.Expect(k8sClient.Update(ctx, &cq1)).To(gomega.Succeed())
				}, util.Timeout, util.ShortInterval).Should(gomega.Succeed())

				// updated values for ch1 with cq1 added
				util.ExpectCohortSubtreeQuotaGaugeMetric("ch1", flavor1.Name, corev1.ResourceCPU.String(), 6_000)
				util.ExpectCohortSubtreeQuotaGaugeMetric("ch1", flavor1.Name, "nvidia.com/gpu", 2)

				// cleared up values for ch0 with cq1 removed
				util.ExpectCohortSubtreeQuotaGaugeMetricCleaned("ch0", flavor1.Name, corev1.ResourceCPU.String())
				util.ExpectCohortSubtreeQuotaGaugeMetricCleaned("ch0", flavor1.Name, "nvidia.com/gpu")
			})

			ginkgo.By("Re-assign cq1 from ch1 to ch2", func() {
				var cq1 kueue.ClusterQueue
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cqs["cq1"]), &cq1)).To(gomega.Succeed())
					cq1.Spec.CohortName = "ch2"
					g.Expect(k8sClient.Update(ctx, &cq1)).To(gomega.Succeed())
				}, util.Timeout, util.ShortInterval).Should(gomega.Succeed())

				// updated values for ch2 with cq1 added
				util.ExpectCohortSubtreeQuotaGaugeMetric("ch2", flavor1.Name, corev1.ResourceCPU.String(), 4_000)
				util.ExpectCohortSubtreeQuotaGaugeMetric("ch2", flavor1.Name, "nvidia.com/gpu", 2)

				// updated values for ch1 with cq1 removed
				util.ExpectCohortSubtreeQuotaGaugeMetric("ch1", flavor1.Name, corev1.ResourceCPU.String(), 5_000)
				util.ExpectCohortSubtreeQuotaGaugeMetric("ch1", flavor1.Name, "nvidia.com/gpu", 1)
			})
		})

		ginkgo.It("reports CohortSubtreeResourceUsage across child-parent propagation, reduced spill, release, and topology recompute", func() {
			var (
				wlCh1a *kueue.Workload
				wlCh1b *kueue.Workload
				wlCh1c *kueue.Workload
				wlCh2  *kueue.Workload
			)

			ginkgo.By("Create root and children cohorts with explicit quotas/lending limits to reduce spillage", func() {
				createCohort(utiltestingapi.MakeCohort("root").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas(defaultFlavor.Name).
							Resource(corev1.ResourceCPU, "30").
							Obj(),
					).Obj())

				// ch1 local quota before spilling to parent is 8-3=5 CPU
				createCohort(utiltestingapi.MakeCohort("ch1").
					Parent("root").
					ResourceGroup(
						withLending(kueue.ResourceFlavorReference(defaultFlavor.Name), "8", "3"),
					).Obj())

				// ch2 local quota before spilling to parent is 6-2=4 CPU.
				createCohort(utiltestingapi.MakeCohort("ch2").
					Parent("root").
					ResourceGroup(
						withLending(kueue.ResourceFlavorReference(defaultFlavor.Name), "6", "2"),
					).Obj())

				util.ExpectCohortSubtreeResourceUsageGaugeMetricCleaned("ch1", defaultFlavor.Name, corev1.ResourceCPU.String())
				util.ExpectCohortSubtreeResourceUsageGaugeMetricCleaned("ch2", defaultFlavor.Name, corev1.ResourceCPU.String())
				util.ExpectCohortSubtreeResourceUsageGaugeMetricCleaned("root", defaultFlavor.Name, corev1.ResourceCPU.String())
			})

			ginkgo.By("Create one ClusterQueue under each child cohort (also with lending limits)", func() {
				createQueue(utiltestingapi.MakeClusterQueue("cq1").
					Cohort("ch1").
					ResourceGroup(
						withLending(kueue.ResourceFlavorReference(defaultFlavor.Name), "10", "8"),
					).Obj())

				createQueue(utiltestingapi.MakeClusterQueue("cq2").
					Cohort("ch2").
					ResourceGroup(
						withLending(kueue.ResourceFlavorReference(defaultFlavor.Name), "8", "6"),
					).Obj())

				util.ExpectCohortSubtreeQuotaGaugeMetric("ch1", defaultFlavor.Name, corev1.ResourceCPU.String(), 16_000)
				util.ExpectCohortSubtreeQuotaGaugeMetric("ch2", defaultFlavor.Name, corev1.ResourceCPU.String(), 12_000)
				util.ExpectCohortSubtreeQuotaGaugeMetric("root", defaultFlavor.Name, corev1.ResourceCPU.String(), 35_000)
			})

			ginkgo.By("Admit workload in ch1: usage rises in ch1 but root spill remains zero due to ch1 local quota", func() {
				wlCh1a = createWorkload("cq1", "6")
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlCh1a)

				util.ExpectCohortSubtreeResourceUsageGaugeMetric("ch1", defaultFlavor.Name, corev1.ResourceCPU.String(), 4_000)
				util.ExpectCohortSubtreeResourceUsageGaugeMetricCleaned("root", defaultFlavor.Name, corev1.ResourceCPU.String())
			})

			ginkgo.By("Add more load in ch1: child usage grows but root spill still stays at zero with current lending limits", func() {
				wlCh1b = createWorkload("cq1", "8")
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlCh1b)

				// total cq1 usage is now 14 CPU, extra 8 CPU spill to ch1 from cq1,
				// but ch1 can absorb it with its local quota and lending limit, so root spill stays at zero.
				util.ExpectCohortSubtreeResourceUsageGaugeMetric("ch1", defaultFlavor.Name, corev1.ResourceCPU.String(), 12_000)
				util.ExpectCohortSubtreeResourceUsageGaugeMetricCleaned("root", defaultFlavor.Name, corev1.ResourceCPU.String())
			})

			ginkgo.By("Force a small spill from ch1 to root with extra load", func() {
				wlCh1c = createWorkload("cq1", "2")
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlCh1c)

				// Total cq1 usage is 16 CPU (=wlCh1a 6 + wlCh1b 8 + wlCh1c 2), cq1 local part is 2 CPU so ch1 sees 14 CPU,
				// ch1 local capacity is 13 CPU (=16 subtree quota - ch1 lendingLimit 3), and root gets the excess 1 CPU.
				util.ExpectCohortSubtreeResourceUsageGaugeMetric("ch1", defaultFlavor.Name, corev1.ResourceCPU.String(), 14_000)
				util.ExpectCohortSubtreeResourceUsageGaugeMetric("root", defaultFlavor.Name, corev1.ResourceCPU.String(), 1_000)
			})

			ginkgo.By("Admit workload in ch2 and verify usage is spilled to ch2 and root", func() {
				wlCh2 = createWorkload("cq2", "7")
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlCh2)

				// ch2 usage gets cq2 overflow: 7-2=5 CPU.
				// ch2 local capacity is 4 CPU (=6 subtree quota - ch2 lendingLimit 2), so it absorbs 4 CPU and spills the excess 1 CPU to root.
				util.ExpectCohortSubtreeResourceUsageGaugeMetric("ch2", defaultFlavor.Name, corev1.ResourceCPU.String(), 5_000)
				util.ExpectCohortSubtreeResourceUsageGaugeMetric("root", defaultFlavor.Name, corev1.ResourceCPU.String(), 1_000)
			})

			ginkgo.By("Finish the extra spill workload and verify root goes back to zero", func() {
				util.FinishWorkloads(ctx, k8sClient, wlCh1c)

				util.ExpectCohortSubtreeResourceUsageGaugeMetric("ch1", defaultFlavor.Name, corev1.ResourceCPU.String(), 12_000)
				util.ExpectCohortSubtreeResourceUsageGaugeMetricCleaned("root", defaultFlavor.Name, corev1.ResourceCPU.String())
			})

			ginkgo.By("Release high-overflow workload in ch1 and verify root remains at zero spill", func() {
				util.FinishWorkloads(ctx, k8sClient, wlCh1b)

				util.ExpectCohortSubtreeResourceUsageGaugeMetric("ch1", defaultFlavor.Name, corev1.ResourceCPU.String(), 4_000)
				util.ExpectCohortSubtreeResourceUsageGaugeMetric("ch2", defaultFlavor.Name, corev1.ResourceCPU.String(), 5_000)
				util.ExpectCohortSubtreeResourceUsageGaugeMetricCleaned("root", defaultFlavor.Name, corev1.ResourceCPU.String())
			})

			ginkgo.By("Move cq2 from ch2 to ch1 and verify recomputed hierarchical attribution", func() {
				var cq kueue.ClusterQueue
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: "cq2"}, &cq)).To(gomega.Succeed())
					cq.Spec.CohortName = "ch1"
					g.Expect(k8sClient.Update(ctx, &cq)).To(gomega.Succeed())
				}, util.Timeout, util.ShortInterval).Should(gomega.Succeed())

				// ch1 gains recomputed usage after the move, ch2 goes to zero
				util.ExpectCohortSubtreeResourceUsageGaugeMetric("ch1", defaultFlavor.Name, corev1.ResourceCPU.String(), 9_000)
				util.ExpectCohortSubtreeResourceUsageGaugeMetricCleaned("root", defaultFlavor.Name, corev1.ResourceCPU.String())
				util.ExpectCohortSubtreeResourceUsageGaugeMetricCleaned("ch2", defaultFlavor.Name, corev1.ResourceCPU.String())
			})
		})

		ginkgo.It("reports CohortSubtreeResourceUsage overflow only above child local quota", func() {
			var (
				wlLocal    *kueue.Workload
				wlOverflow *kueue.Workload
			)

			createWorkload := func(lqName string, cpu string) *kueue.Workload {
				wl := utiltestingapi.MakeWorkloadWithGeneratedName("usage-overflow-", ns.Name).
					Queue(kueue.LocalQueueName(lqName)).
					Request(corev1.ResourceCPU, cpu).
					Obj()
				util.MustCreate(ctx, k8sClient, wl)
				return wl
			}

			ginkgo.By("Create root and child cohort ch1", func() {
				createCohort(utiltestingapi.MakeCohort("root").Obj())
				createCohort(utiltestingapi.MakeCohort("ch1").Parent("root").Obj())
			})

			ginkgo.By("Create cq1 with nominal=10 CPU and lendingLimit=4 CPU (local quota=6 CPU)", func() {
				createQueue(utiltestingapi.MakeClusterQueue("cq1-overflow").
					Cohort("ch1").
					ResourceGroup(
						withLending(kueue.ResourceFlavorReference(defaultFlavor.Name), "10", "4"),
					).Obj())
			})

			ginkgo.By("Admit workload using exactly local quota (6 CPU): no overflow should be recorded in cohort or root", func() {
				wlLocal = createWorkload("cq1-overflow", "6")
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlLocal)

				util.ExpectCohortSubtreeResourceUsageGaugeMetricCleaned("ch1", defaultFlavor.Name, corev1.ResourceCPU.String())
				util.ExpectCohortSubtreeResourceUsageGaugeMetricCleaned("root", defaultFlavor.Name, corev1.ResourceCPU.String())
			})

			ginkgo.By("Admit extra 2 CPU (total=8): only overflow 2 should appear in cohort and root", func() {
				wlOverflow = createWorkload("cq1-overflow", "2")
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlOverflow)

				util.ExpectCohortSubtreeResourceUsageGaugeMetric("ch1", defaultFlavor.Name, corev1.ResourceCPU.String(), 2_000)
				util.ExpectCohortSubtreeResourceUsageGaugeMetric("root", defaultFlavor.Name, corev1.ResourceCPU.String(), 2_000)
			})

			ginkgo.By("Finish overflow workload and verify spill is removed from cohort and root", func() {
				util.FinishWorkloads(ctx, k8sClient, wlOverflow)
				util.ExpectObjectToBeDeleted(ctx, k8sClient, wlOverflow, true)

				util.ExpectCohortSubtreeResourceUsageGaugeMetricCleaned("ch1", defaultFlavor.Name, corev1.ResourceCPU.String())
				util.ExpectCohortSubtreeResourceUsageGaugeMetricCleaned("root", defaultFlavor.Name, corev1.ResourceCPU.String())
			})

			ginkgo.By("Finish local workload and keep all usage at zero", func() {
				util.FinishWorkloads(ctx, k8sClient, wlLocal)
				util.ExpectObjectToBeDeleted(ctx, k8sClient, wlLocal, true)

				util.ExpectCohortSubtreeResourceUsageGaugeMetricCleaned("ch1", defaultFlavor.Name, corev1.ResourceCPU.String())
				util.ExpectCohortSubtreeResourceUsageGaugeMetricCleaned("root", defaultFlavor.Name, corev1.ResourceCPU.String())
			})
		})
	})
})
