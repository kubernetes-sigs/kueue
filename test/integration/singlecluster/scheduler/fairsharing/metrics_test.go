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
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	workloadpatching "sigs.k8s.io/kueue/pkg/workload/patching"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("Cohorts", func() {
	var (
		admissionChecks map[string]*kueue.AdmissionCheck

		defaultFlavor *kueue.ResourceFlavor
		flavor1       *kueue.ResourceFlavor
		flavor2       *kueue.ResourceFlavor
		ns            *corev1.Namespace

		cohorts map[string]*kueue.Cohort
		cqs     map[string]*kueue.ClusterQueue
		lqs     map[string]*kueue.LocalQueue

		wls map[string]*kueue.Workload
	)

	var createAdmissionCheck = func(ac *kueue.AdmissionCheck) {
		behavioral.MustCreate(ctx, k8sClient, ac)
		behavioral.SetAdmissionCheckActive(ctx, k8sClient, ac, metav1.ConditionTrue)
		admissionChecks[ac.Name] = ac
	}

	var createCohort = func(cohort *kueue.Cohort) *kueue.Cohort {
		behavioral.MustCreate(ctx, k8sClient, cohort)
		cohorts[cohort.Name] = cohort
		return cohort
	}

	var createQueue = func(cq *kueue.ClusterQueue) *kueue.ClusterQueue {
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)
		cqs[cq.Name] = cq

		lq := utiltestingapi.MakeLocalQueue(cq.Name, ns.Name).ClusterQueue(cq.Name).Obj()
		behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
		lqs[lq.Name] = lq
		return cq
	}

	var createWorkload = func(wl *kueue.Workload) *kueue.Workload {
		behavioral.MustCreate(ctx, k8sClient, wl)
		wls[wl.Name] = wl
		return wl
	}

	withLending := func(fName kueue.ResourceFlavorReference, nominal, lending string) kueue.FlavorQuotas {
		fq := *utiltestingapi.MakeFlavorQuotas(string(fName)).
			Resource(corev1.ResourceCPU, nominal, "", lending).
			Obj()
		return fq
	}

	ginkgo.When("creating, modifying and removing", func() {
		ginkgo.BeforeEach(func() {
			admissionChecks = make(map[string]*kueue.AdmissionCheck)
			cohorts = make(map[string]*kueue.Cohort)
			cqs = make(map[string]*kueue.ClusterQueue)
			lqs = make(map[string]*kueue.LocalQueue)
			wls = make(map[string]*kueue.Workload)

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
			behavioral.MustCreate(ctx, k8sClient, defaultFlavor)
			flavor1 = utiltestingapi.MakeResourceFlavor("flavor1").Obj()
			behavioral.MustCreate(ctx, k8sClient, flavor1)
			flavor2 = utiltestingapi.MakeResourceFlavor("flavor2").Obj()
			behavioral.MustCreate(ctx, k8sClient, flavor2)

			ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-")
		})

		ginkgo.AfterEach(func() {
			for _, wl := range wls {
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, wl, true)
			}
			for _, lq := range lqs {
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, lq, true)
			}
			gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			for _, cq := range cqs {
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			}
			for _, cohort := range cohorts {
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cohort, true)
			}
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, defaultFlavor, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, flavor1, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, flavor2, true)
			for _, ac := range admissionChecks {
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, ac, true)
			}
			fwk.StopManager(ctx)
		})

		ginkgo.It("follows reporting correct metrics", func() {
			ginkgo.By("Creating initial ClusterQueue cqa, with no cohort", func() {
				createQueue(utiltestingapi.MakeClusterQueue("cqa").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas(defaultFlavor.Name).
							Resource(corev1.ResourceCPU, "10").
							Resource(corev1.ResourceMemory, "10Gi").
							Obj(),
					).Obj())

				// no metrics for the ch1 cohort
				behavioral.ExpectCohortSubtreeQuotaGaugeMetricCleaned("ch1", defaultFlavor.Name, corev1.ResourceCPU.String())
				behavioral.ExpectCohortSubtreeQuotaGaugeMetricCleaned("ch1", defaultFlavor.Name, corev1.ResourceMemory.String())

				behavioral.ExpectClusterQueueInfoMetric("cqa", "", "", 1)
			})

			ginkgo.By("Setting cqa cohort to ch1", func() {
				var cq kueue.ClusterQueue
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cqs["cqa"]), &cq)).To(gomega.Succeed())
					cq.Spec.CohortName = "ch1"
					g.Expect(k8sClient.Update(ctx, &cq)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.ShortInterval).Should(gomega.Succeed())

				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("ch1", defaultFlavor.Name, corev1.ResourceCPU.String(), 10)
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("ch1", defaultFlavor.Name, corev1.ResourceMemory.String(), behavioral.ResourceQtyToFloat64("10Gi"))

				behavioral.ExpectClusterQueueInfoMetric("cqa", "ch1", "ch1", 1)
			})

			ginkgo.By("Creating ClusterQueue cqb and cohort ch1", func() {
				createCohort(utiltestingapi.MakeCohort("ch1").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas(defaultFlavor.Name).
							Resource(corev1.ResourceCPU, "15").
							Resource(corev1.ResourceMemory, "15Gi").
							Obj(),
					).Obj())

				createQueue(utiltestingapi.MakeClusterQueue("cqb").
					Cohort("ch1").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas(defaultFlavor.Name).
							Resource(corev1.ResourceCPU, "5").
							Resource(corev1.ResourceMemory, "5Gi").
							Obj(),
					).Obj())

				// combined values of resources of cqa(10 cpu, 10Gi) and cqb(5 cpu, 5Gi) and ch1 cohort(15 cpu, 15Gi)
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("ch1", defaultFlavor.Name, corev1.ResourceCPU.String(), 30)
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("ch1", defaultFlavor.Name, corev1.ResourceMemory.String(), behavioral.ResourceQtyToFloat64("30Gi"))

				behavioral.ExpectCohortInfoMetric("ch1", "", "ch1", 1)
				behavioral.ExpectClusterQueueInfoMetric("cqa", "ch1", "ch1", 1)
				behavioral.ExpectClusterQueueInfoMetric("cqb", "ch1", "ch1", 1)
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
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("ch2", defaultFlavor.Name, corev1.ResourceCPU.String(), 20)
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("ch2", defaultFlavor.Name, corev1.ResourceMemory.String(), behavioral.ResourceQtyToFloat64("20Gi"))
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
				}, behavioral.Timeout, behavioral.ShortInterval).Should(gomega.Succeed())

				// combined values of resources of ch1(30 cpu, 30Gi) and ch2(20 cpu, 20Gi) and root cohort flavor1 (20 cpu, 5 gpu)
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("root", defaultFlavor.Name, corev1.ResourceCPU.String(), 50)
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("root", defaultFlavor.Name, corev1.ResourceMemory.String(), behavioral.ResourceQtyToFloat64("50Gi"))
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("root", flavor1.Name, corev1.ResourceCPU.String(), 20)
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("root", flavor1.Name, "nvidia.com/gpu", 5)

				behavioral.ExpectCohortSubtreeQuotaGaugeMetricCleaned("root", flavor2.Name, corev1.ResourceCPU.String())
				behavioral.ExpectCohortSubtreeQuotaGaugeMetricCleaned("root", flavor2.Name, corev1.ResourceMemory.String())

				behavioral.ExpectCohortInfoMetric("root", "", "root", 1)
				behavioral.ExpectCohortInfoMetric("ch1", "root", "root", 1)
				behavioral.ExpectCohortInfoMetric("ch2", "root", "root", 1)
				behavioral.ExpectClusterQueueInfoMetric("cqa", "ch1", "root", 1)
				behavioral.ExpectClusterQueueInfoMetric("cqb", "ch1", "root", 1)
				behavioral.ExpectClusterQueueInfoMetric("cqd", "ch2", "root", 1)
				behavioral.ExpectClusterQueueInfoMetric("cqe", "ch2", "root", 1)
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

				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("ch3", flavor1.Name, corev1.ResourceCPU.String(), 5)
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("ch3", flavor1.Name, "nvidia.com/gpu", 1)

				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("root", defaultFlavor.Name, corev1.ResourceCPU.String(), 50)
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("root", defaultFlavor.Name, corev1.ResourceMemory.String(), behavioral.ResourceQtyToFloat64("50Gi"))
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("root", flavor1.Name, corev1.ResourceCPU.String(), 25)
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("root", flavor1.Name, "nvidia.com/gpu", 6)

				behavioral.ExpectCohortInfoMetric("ch3", "root", "root", 1)
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
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("ch3", flavor1.Name, corev1.ResourceCPU.String(), 5)
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("ch3", flavor1.Name, "nvidia.com/gpu", 1)
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("ch3", flavor2.Name, corev1.ResourceCPU.String(), 1)
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("ch3", flavor2.Name, corev1.ResourceMemory.String(), 1.073741824e+09)

				// updated combined values of resources of ch1(30 cpu, 30Gi) and ch2(20 cpu, 20Gi) and ch3 flavor2 (6 cpu, 1 gpu) and root cohort flavor1 (20 cpu, 5 gpu)
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("root", defaultFlavor.Name, corev1.ResourceCPU.String(), 50)
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("root", defaultFlavor.Name, corev1.ResourceMemory.String(), behavioral.ResourceQtyToFloat64("50Gi"))
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("root", flavor1.Name, corev1.ResourceCPU.String(), 25)
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("root", flavor1.Name, "nvidia.com/gpu", 6)
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("root", flavor2.Name, corev1.ResourceCPU.String(), 1)
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("root", flavor2.Name, corev1.ResourceMemory.String(), behavioral.ResourceQtyToFloat64("1Gi"))

				behavioral.ExpectClusterQueueInfoMetric("cqg", "ch3", "root", 1)
			})

			ginkgo.By("Deleting ClusterQueue cqg", func() {
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cqs["cqg"], true)

				// updated values for ch3 with cqg removed, and root with ch3 updated
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("ch3", flavor1.Name, corev1.ResourceCPU.String(), 5)
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("ch3", flavor1.Name, "nvidia.com/gpu", 1)
				behavioral.ExpectCohortSubtreeQuotaGaugeMetricCleaned("ch3", flavor2.Name, corev1.ResourceCPU.String())
				behavioral.ExpectCohortSubtreeQuotaGaugeMetricCleaned("ch3", flavor2.Name, corev1.ResourceMemory.String())

				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("root", defaultFlavor.Name, corev1.ResourceCPU.String(), 50)
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("root", defaultFlavor.Name, corev1.ResourceMemory.String(), behavioral.ResourceQtyToFloat64("50Gi"))
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("root", flavor1.Name, corev1.ResourceCPU.String(), 25)
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("root", flavor1.Name, "nvidia.com/gpu", 6)

				// root metrics for flavor2 with cqg removed
				behavioral.ExpectCohortSubtreeQuotaGaugeMetricCleaned("root", flavor2.Name, corev1.ResourceCPU.String())
				behavioral.ExpectCohortSubtreeQuotaGaugeMetricCleaned("root", flavor2.Name, corev1.ResourceMemory.String())
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
				}, behavioral.Timeout, behavioral.ShortInterval).Should(gomega.Succeed())

				// updated values for ch3 with cqd and cqe added, and root with ch3 updated
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("ch3", defaultFlavor.Name, corev1.ResourceCPU.String(), 10)
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("ch3", defaultFlavor.Name, corev1.ResourceMemory.String(), behavioral.ResourceQtyToFloat64("10Gi"))

				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("root", defaultFlavor.Name, corev1.ResourceCPU.String(), 50)
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("root", defaultFlavor.Name, corev1.ResourceMemory.String(), behavioral.ResourceQtyToFloat64("50Gi"))
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("root", flavor1.Name, corev1.ResourceCPU.String(), 25)
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("root", flavor1.Name, "nvidia.com/gpu", 6)

				behavioral.ExpectClusterQueueInfoMetric("cqd", "ch3", "root", 1)
				behavioral.ExpectClusterQueueInfoMetric("cqe", "ch3", "root", 1)
				behavioral.ExpectClusterQueueInfoMetric("cqd", "ch2", "root", 0)
				behavioral.ExpectClusterQueueInfoMetric("cqe", "ch2", "root", 0)
			})

			ginkgo.By("Deleting cohort ch2", func() {
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cohorts["ch2"], true)

				// updated values for ch3 with cqd and cqe added, and root with ch3 updated
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("ch3", defaultFlavor.Name, corev1.ResourceCPU.String(), 10)
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("ch3", defaultFlavor.Name, corev1.ResourceMemory.String(), behavioral.ResourceQtyToFloat64("10Gi"))

				// root metrics with ch2 removed, so only ch1 and ch3 values
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("root", defaultFlavor.Name, corev1.ResourceCPU.String(), 40)
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("root", defaultFlavor.Name, corev1.ResourceMemory.String(), behavioral.ResourceQtyToFloat64("40Gi"))
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("root", flavor1.Name, corev1.ResourceCPU.String(), 25)
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("root", flavor1.Name, "nvidia.com/gpu", 6)

				// ch2 metrics removed
				behavioral.ExpectCohortSubtreeQuotaGaugeMetricCleaned("ch2", defaultFlavor.Name, corev1.ResourceCPU.String())
				behavioral.ExpectCohortSubtreeQuotaGaugeMetricCleaned("ch2", defaultFlavor.Name, corev1.ResourceMemory.String())

				behavioral.ExpectCohortInfoMetric("ch2", "root", "root", 0)
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
				}, behavioral.Timeout, behavioral.ShortInterval).Should(gomega.Succeed())

				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("ch1", defaultFlavor.Name, corev1.ResourceCPU.String(), 35)
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("ch1", defaultFlavor.Name, corev1.ResourceMemory.String(), behavioral.ResourceQtyToFloat64("30Gi"))

				// root metrics updated with new quotas of ch1
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("root", defaultFlavor.Name, corev1.ResourceCPU.String(), 45)
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("root", defaultFlavor.Name, corev1.ResourceMemory.String(), behavioral.ResourceQtyToFloat64("40Gi"))
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("root", flavor1.Name, corev1.ResourceCPU.String(), 25)
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("root", flavor1.Name, "nvidia.com/gpu", 6)
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

				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("ch1", flavor1.Name, corev1.ResourceCPU.String(), 5)
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("ch1", flavor1.Name, "nvidia.com/gpu", 1)

				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("ch2", flavor1.Name, corev1.ResourceCPU.String(), 3)
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("ch2", flavor1.Name, "nvidia.com/gpu", 1)

				behavioral.ExpectCohortInfoMetric("ch1", "", "ch1", 1)
				behavioral.ExpectCohortInfoMetric("ch2", "", "ch2", 1)
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

				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("ch0", flavor1.Name, corev1.ResourceCPU.String(), 1)
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("ch0", flavor1.Name, "nvidia.com/gpu", 1)

				behavioral.ExpectClusterQueueInfoMetric("cq1", "ch0", "ch0", 1)
			})

			ginkgo.By("Re-assign cq1 from ch0 to ch1", func() {
				var cq1 kueue.ClusterQueue
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cqs["cq1"]), &cq1)).To(gomega.Succeed())
					cq1.Spec.CohortName = "ch1"
					g.Expect(k8sClient.Update(ctx, &cq1)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.ShortInterval).Should(gomega.Succeed())

				// updated values for ch1 with cq1 added
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("ch1", flavor1.Name, corev1.ResourceCPU.String(), 6)
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("ch1", flavor1.Name, "nvidia.com/gpu", 2)

				// cleared up values for ch0 with cq1 removed
				behavioral.ExpectCohortSubtreeQuotaGaugeMetricCleaned("ch0", flavor1.Name, corev1.ResourceCPU.String())
				behavioral.ExpectCohortSubtreeQuotaGaugeMetricCleaned("ch0", flavor1.Name, "nvidia.com/gpu")

				behavioral.ExpectCohortInfoMetric("ch0", "", "ch0", 0)
				behavioral.ExpectClusterQueueInfoMetric("cq1", "ch1", "ch1", 1)
				behavioral.ExpectClusterQueueInfoMetric("cq1", "ch0", "ch0", 0)
			})

			ginkgo.By("Re-assign cq1 from ch1 to ch2", func() {
				var cq1 kueue.ClusterQueue
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cqs["cq1"]), &cq1)).To(gomega.Succeed())
					cq1.Spec.CohortName = "ch2"
					g.Expect(k8sClient.Update(ctx, &cq1)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.ShortInterval).Should(gomega.Succeed())

				// updated values for ch2 with cq1 added
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("ch2", flavor1.Name, corev1.ResourceCPU.String(), 4)
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("ch2", flavor1.Name, "nvidia.com/gpu", 2)

				// updated values for ch1 with cq1 removed
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("ch1", flavor1.Name, corev1.ResourceCPU.String(), 5)
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("ch1", flavor1.Name, "nvidia.com/gpu", 1)

				behavioral.ExpectCohortInfoMetric("ch1", "", "ch1", 1)
				behavioral.ExpectCohortInfoMetric("ch2", "", "ch2", 1)
				behavioral.ExpectClusterQueueInfoMetric("cq1", "ch2", "ch2", 1)
				behavioral.ExpectClusterQueueInfoMetric("cq1", "ch1", "ch1", 0)
			})
		})

		ginkgo.It("updates deep descendants info metrics when reparenting an ancestor cohort", func() {
			ginkgo.By("Creating a -> b -> c cohort chain and queue qdeep under c", func() {
				createCohort(utiltestingapi.MakeCohort("a").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas(defaultFlavor.Name).
							Resource(corev1.ResourceCPU, "1").
							Obj(),
					).Obj())
				createCohort(utiltestingapi.MakeCohort("b").Parent("a").Obj())
				createCohort(utiltestingapi.MakeCohort("c").Parent("b").Obj())
				createQueue(utiltestingapi.MakeClusterQueue("qdeep").
					Cohort("c").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas(defaultFlavor.Name).
							Resource(corev1.ResourceCPU, "1").
							Obj(),
					).Obj())

				behavioral.ExpectCohortInfoMetric("b", "a", "a", 1)
				behavioral.ExpectCohortInfoMetric("c", "b", "a", 1)
				behavioral.ExpectClusterQueueInfoMetric("qdeep", "c", "a", 1)
			})

			ginkgo.By("Creating root2", func() {
				createCohort(utiltestingapi.MakeCohort("root2").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas(defaultFlavor.Name).
							Resource(corev1.ResourceCPU, "1").
							Obj(),
					).Obj())
				behavioral.ExpectCohortInfoMetric("root2", "", "root2", 1)
			})

			ginkgo.By("reparenting cohort a under root2", func() {
				var a kueue.Cohort
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: "a"}, &a)).To(gomega.Succeed())
					a.Spec.ParentName = "root2"
					g.Expect(k8sClient.Update(ctx, &a)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.ShortInterval).Should(gomega.Succeed())

				behavioral.ExpectCohortInfoMetric("a", "root2", "root2", 1)
				behavioral.ExpectCohortInfoMetric("b", "a", "root2", 1)
				behavioral.ExpectCohortInfoMetric("c", "b", "root2", 1)
				behavioral.ExpectClusterQueueInfoMetric("qdeep", "c", "root2", 1)
				behavioral.ExpectClusterQueueInfoMetric("qdeep", "c", "a", 0)
			})
		})

		ginkgo.It("reports metrics for newly created implicit parent, remove implicit parent metrics when it has been removed", func() {
			ginkgo.By("Creating a cohort with implicit parent", func() {
				createCohort(utiltestingapi.MakeCohort("child").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas(defaultFlavor.Name).
							Resource(corev1.ResourceCPU, "1").
							Obj(),
					).Parent("implicit-parent").Obj())
			})

			// Verify metrics were reported for child and implicit parent
			behavioral.ExpectCohortInfoMetric("implicit-parent", "", "implicit-parent", 1)
			behavioral.ExpectCohortInfoMetric("child", "implicit-parent", "implicit-parent", 1)

			ginkgo.By("Updating cohort to have a new implicit parent", func() {
				var ch kueue.Cohort
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: "child"}, &ch)).To(gomega.Succeed())
					ch.Spec.ParentName = "new-implicit-parent"
					g.Expect(k8sClient.Update(ctx, &ch)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.ShortInterval).Should(gomega.Succeed())
			})

			// Verify metrics were reported for child with new parent and new implicit parent, and metrics for old implicit parent were removed
			behavioral.ExpectCohortInfoMetric("implicit-parent", "", "implicit-parent", 0)
			behavioral.ExpectCohortInfoMetric("new-implicit-parent", "", "new-implicit-parent", 1)
			behavioral.ExpectCohortInfoMetric("child", "new-implicit-parent", "new-implicit-parent", 1)
		})

		ginkgo.It("correctly handles cohort metrics when workload admitted with admission check", func() {
			const (
				numWorkloadsForCQ1 = 5
				numWorkloadsForCQ2 = 2
			)

			ginkgo.By("Creating AdmissionCheck", func() {
				createAdmissionCheck(utiltestingapi.MakeAdmissionCheck("check1").ControllerName("ctrl1").Obj())
			})

			ginkgo.By("Creating Cohorts", func() {
				createCohort(utiltestingapi.MakeCohort("ch1").
					Parent("root").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas(flavor1.Name).
							Resource(corev1.ResourceCPU, "5").
							Obj(),
					).Obj())

				createCohort(utiltestingapi.MakeCohort("ch2").
					Parent("root").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas(flavor1.Name).
							Resource(corev1.ResourceCPU, "2").
							Obj(),
					).Obj())
			})

			ginkgo.By("Create ClusterQueues", func() {
				createQueue(utiltestingapi.MakeClusterQueue("cq1").
					Cohort("ch1").
					AdmissionChecks("check1").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas(flavor1.Name).
							Resource(corev1.ResourceCPU, "5").
							Obj(),
					).Obj())

				createQueue(utiltestingapi.MakeClusterQueue("cq2").
					Cohort("ch2").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas(flavor1.Name).
							Resource(corev1.ResourceCPU, "2").
							Obj(),
					).Obj())
			})

			shouldAdmit := make([]*kueue.Workload, 0, numWorkloadsForCQ1)

			ginkgo.By("Creating Workloads", func() {
				for range numWorkloadsForCQ1 {
					shouldAdmit = append(shouldAdmit,
						createWorkload(
							utiltestingapi.MakeWorkloadWithGeneratedName("workload-", ns.Name).
								Queue("cq1").
								Request(corev1.ResourceCPU, "1").Obj(),
						),
					)
				}

				for range numWorkloadsForCQ2 {
					createWorkload(
						utiltestingapi.MakeWorkloadWithGeneratedName("workload-", ns.Name).
							Queue("cq2").
							Request(corev1.ResourceCPU, "1").Obj(),
					)
				}
			})

			ginkgo.By("Checking that only cq2 Workloads admitted", func() {
				behavioral.ExpectAdmittedActiveWorkloadsGaugeMetric("cq1", 0)
				behavioral.ExpectAdmittedActiveWorkloadsGaugeMetric("cq2", numWorkloadsForCQ2)

				behavioral.ExpectCohortSubtreeAdmittedWorkloadsTotalMetric("root", "", numWorkloadsForCQ2)
				behavioral.ExpectCohortSubtreeAdmittedWorkloadsTotalMetric("ch1", "", 0)
				behavioral.ExpectCohortSubtreeAdmittedWorkloadsTotalMetric("ch2", "", numWorkloadsForCQ2)

				behavioral.ExpectCohortSubtreeAdmittedActiveWorkloadsGaugeMetric("root", numWorkloadsForCQ2)
				behavioral.ExpectCohortSubtreeAdmittedActiveWorkloadsGaugeMetric("ch1", 0)
				behavioral.ExpectCohortSubtreeAdmittedActiveWorkloadsGaugeMetric("ch2", numWorkloadsForCQ2)
			})

			ginkgo.By("Marking the checks as passed", func() {
				for _, wl := range shouldAdmit {
					createdWorkload := &kueue.Workload{}
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), createdWorkload)).To(gomega.Succeed())
						workloadpatching.SetAdmissionCheckState(&createdWorkload.Status.AdmissionChecks, kueue.AdmissionCheckState{
							Name:    "check1",
							State:   kueue.CheckStateReady,
							Message: "Check successfully passed",
						}, behavioral.RealClock)
						g.Expect(k8sClient.Status().Update(ctx, createdWorkload)).Should(gomega.Succeed())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				}
			})

			ginkgo.By("Checking that all Workloads admitted", func() {
				behavioral.ExpectAdmittedActiveWorkloadsGaugeMetric("cq1", numWorkloadsForCQ1)
				behavioral.ExpectAdmittedActiveWorkloadsGaugeMetric("cq2", numWorkloadsForCQ2)

				behavioral.ExpectCohortSubtreeAdmittedWorkloadsTotalMetric("root", "", numWorkloadsForCQ1+numWorkloadsForCQ2)
				behavioral.ExpectCohortSubtreeAdmittedWorkloadsTotalMetric("ch1", "", numWorkloadsForCQ1)
				behavioral.ExpectCohortSubtreeAdmittedWorkloadsTotalMetric("ch2", "", numWorkloadsForCQ2)

				behavioral.ExpectCohortSubtreeAdmittedActiveWorkloadsGaugeMetric("root", numWorkloadsForCQ1+numWorkloadsForCQ2)
				behavioral.ExpectCohortSubtreeAdmittedActiveWorkloadsGaugeMetric("ch1", numWorkloadsForCQ1)
				behavioral.ExpectCohortSubtreeAdmittedActiveWorkloadsGaugeMetric("ch2", numWorkloadsForCQ2)
			})
		})

		ginkgo.It("correctly handles cohort metrics when workload admitted with workload priority class", func() {
			const (
				numWorkloadsForCQ1 = 5
				numWorkloadsForCQ2 = 2
			)

			ginkgo.By("Creating Cohorts", func() {
				createCohort(utiltestingapi.MakeCohort("ch1").
					Parent("root").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas(flavor1.Name).
							Resource(corev1.ResourceCPU, "5").
							Obj(),
					).Obj())

				createCohort(utiltestingapi.MakeCohort("ch2").
					Parent("root").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas(flavor1.Name).
							Resource(corev1.ResourceCPU, "2").
							Obj(),
					).Obj())
			})

			ginkgo.By("Create ClusterQueues", func() {
				createQueue(utiltestingapi.MakeClusterQueue("cq1").
					Cohort("ch1").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas(flavor1.Name).
							Resource(corev1.ResourceCPU, "5").
							Obj(),
					).Obj())

				createQueue(utiltestingapi.MakeClusterQueue("cq2").
					Cohort("ch2").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas(flavor1.Name).
							Resource(corev1.ResourceCPU, "2").
							Obj(),
					).Obj())
			})

			ginkgo.By("Creating Workloads", func() {
				for range numWorkloadsForCQ1 {
					createWorkload(
						utiltestingapi.MakeWorkloadWithGeneratedName("workload-", ns.Name).
							Queue("cq1").
							WorkloadPriorityClassRef("high").
							Priority(100).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					)
				}

				for range numWorkloadsForCQ2 {
					createWorkload(utiltestingapi.MakeWorkloadWithGeneratedName("workload-", ns.Name).
						Queue("cq2").
						WorkloadPriorityClassRef("low").
						Priority(10).
						Request(corev1.ResourceCPU, "1").
						Obj(),
					)
				}
			})

			ginkgo.By("Checking that only cq2 Workloads admitted", func() {
				behavioral.ExpectAdmittedActiveWorkloadsGaugeMetric("cq1", numWorkloadsForCQ1)
				behavioral.ExpectAdmittedActiveWorkloadsGaugeMetric("cq2", numWorkloadsForCQ2)

				behavioral.ExpectCohortSubtreeAdmittedWorkloadsTotalMetric("root", "high", numWorkloadsForCQ1)
				behavioral.ExpectCohortSubtreeAdmittedWorkloadsTotalMetric("root", "low", numWorkloadsForCQ2)
				behavioral.ExpectCohortSubtreeAdmittedWorkloadsTotalMetric("ch1", "high", numWorkloadsForCQ1)
				behavioral.ExpectCohortSubtreeAdmittedWorkloadsTotalMetric("ch2", "low", numWorkloadsForCQ2)

				behavioral.ExpectCohortSubtreeAdmittedActiveWorkloadsGaugeMetric("root", numWorkloadsForCQ1+numWorkloadsForCQ2)
				behavioral.ExpectCohortSubtreeAdmittedActiveWorkloadsGaugeMetric("ch1", numWorkloadsForCQ1)
				behavioral.ExpectCohortSubtreeAdmittedActiveWorkloadsGaugeMetric("ch2", numWorkloadsForCQ2)
			})
		})

		ginkgo.It("should clear metrics for the implicit root Cohort after deleting Cohort then ClusterQueues", func() {
			ginkgo.By("Creating Cohorts", func() {
				createCohort(utiltestingapi.MakeCohort("ch1").
					Parent("root").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas(flavor1.Name).
							Resource(corev1.ResourceCPU, "5").
							Obj(),
					).Obj())
			})

			var cq1 *kueue.ClusterQueue

			ginkgo.By("Create ClusterQueues", func() {
				cq1 = createQueue(utiltestingapi.MakeClusterQueue("cq1").
					Cohort("ch1").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas(flavor1.Name).
							Resource(corev1.ResourceCPU, "5").
							Obj(),
					).Obj())
			})

			ginkgo.By("Creating Workloads", func() {
				for range 5 {
					createWorkload(
						utiltestingapi.MakeWorkloadWithGeneratedName("workload-", ns.Name).
							Queue("cq1").
							Request(corev1.ResourceCPU, "1").
							Obj(),
					)
				}
			})

			ginkgo.By("Checking that Workloads are admitted", func() {
				behavioral.ExpectAdmittedWorkloadsTotalMetric(cq1, "", 5)
				behavioral.ExpectAdmittedActiveWorkloadsGaugeMetric("cq1", 5)
				behavioral.ExpectCohortSubtreeAdmittedWorkloadsTotalMetric("root", "", 5)
				behavioral.ExpectCohortSubtreeAdmittedWorkloadsTotalMetric("ch1", "", 5)
				behavioral.ExpectCohortSubtreeAdmittedActiveWorkloadsGaugeMetric("root", 5)
				behavioral.ExpectCohortSubtreeAdmittedActiveWorkloadsGaugeMetric("ch1", 5)
			})

			ginkgo.By("Deleting Cohorts", func() {
				for _, cohort := range cohorts {
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cohort, true)
				}
			})

			ginkgo.By("Checking that metrics cleared", func() {
				behavioral.ExpectAdmittedWorkloadsTotalMetric(cq1, "", 5)
				behavioral.ExpectAdmittedActiveWorkloadsGaugeMetric("cq1", 5)
				behavioral.ExpectCohortSubtreeAdmittedWorkloadsTotalMetric("ch1", "", 0)
				behavioral.ExpectCohortSubtreeAdmittedWorkloadsTotalMetric("root", "", 0)
				behavioral.ExpectCohortSubtreeAdmittedActiveWorkloadsGaugeMetric("root", 0)
				behavioral.ExpectCohortSubtreeAdmittedActiveWorkloadsGaugeMetric("ch1", 0)
			})

			ginkgo.By("Deleting Workloads", func() {
				for _, wl := range wls {
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, wl, true)
				}
			})

			ginkgo.By("Deleting LocalQueues", func() {
				for _, lq := range lqs {
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, lq, true)
				}
			})

			ginkgo.By("Deleting ClusterQueues", func() {
				for _, cq := range cqs {
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
				}
			})

			ginkgo.By("Checking that metrics cleared", func() {
				behavioral.ExpectAdmittedWorkloadsTotalMetric(cq1, "", 0)
				behavioral.ExpectAdmittedActiveWorkloadsGaugeMetric("cq1", 0)
				behavioral.ExpectCohortSubtreeAdmittedWorkloadsTotalMetric("ch1", "", 0)
				behavioral.ExpectCohortSubtreeAdmittedWorkloadsTotalMetric("root", "", 0)
				behavioral.ExpectCohortSubtreeAdmittedActiveWorkloadsGaugeMetric("root", 0)
				behavioral.ExpectCohortSubtreeAdmittedActiveWorkloadsGaugeMetric("ch1", 0)
			})
		})

		ginkgo.It("should clear metrics for the implicit root Cohort after deleting ClusterQueues then Cohort", func() {
			ginkgo.By("Creating Cohorts", func() {
				createCohort(utiltestingapi.MakeCohort("ch1").
					Parent("root").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas(flavor1.Name).
							Resource(corev1.ResourceCPU, "5").
							Obj(),
					).Obj())
			})

			var cq1 *kueue.ClusterQueue

			ginkgo.By("Create ClusterQueues", func() {
				cq1 = createQueue(utiltestingapi.MakeClusterQueue("cq1").
					Cohort("ch1").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas(flavor1.Name).
							Resource(corev1.ResourceCPU, "5").
							Obj(),
					).Obj())
			})

			ginkgo.By("Creating Workloads", func() {
				for range 5 {
					createWorkload(
						utiltestingapi.MakeWorkloadWithGeneratedName("workload-", ns.Name).
							Queue("cq1").
							Request(corev1.ResourceCPU, "1").
							Obj(),
					)
				}
			})

			ginkgo.By("Checking that Workloads are admitted", func() {
				behavioral.ExpectAdmittedWorkloadsTotalMetric(cq1, "", 5)
				behavioral.ExpectAdmittedActiveWorkloadsGaugeMetric("cq1", 5)
				behavioral.ExpectCohortSubtreeAdmittedWorkloadsTotalMetric("root", "", 5)
				behavioral.ExpectCohortSubtreeAdmittedWorkloadsTotalMetric("ch1", "", 5)
				behavioral.ExpectCohortSubtreeAdmittedActiveWorkloadsGaugeMetric("root", 5)
				behavioral.ExpectCohortSubtreeAdmittedActiveWorkloadsGaugeMetric("ch1", 5)
			})

			ginkgo.By("Deleting Workloads", func() {
				for _, wl := range wls {
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, wl, true)
				}
			})

			ginkgo.By("Deleting LocalQueues", func() {
				for _, lq := range lqs {
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, lq, true)
				}
			})

			ginkgo.By("Checking that metrics cleared", func() {
				behavioral.ExpectAdmittedWorkloadsTotalMetric(cq1, "", 5)
				behavioral.ExpectAdmittedActiveWorkloadsGaugeMetric("cq1", 0)
				behavioral.ExpectCohortSubtreeAdmittedWorkloadsTotalMetric("ch1", "", 5)
				behavioral.ExpectCohortSubtreeAdmittedWorkloadsTotalMetric("root", "", 5)
				behavioral.ExpectCohortSubtreeAdmittedActiveWorkloadsGaugeMetric("root", 0)
				behavioral.ExpectCohortSubtreeAdmittedActiveWorkloadsGaugeMetric("ch1", 0)
			})

			ginkgo.By("Deleting ClusterQueues", func() {
				for _, cq := range cqs {
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
				}
			})

			ginkgo.By("Checking that metrics cleared", func() {
				behavioral.ExpectAdmittedWorkloadsTotalMetric(cq1, "", 0)
				behavioral.ExpectAdmittedActiveWorkloadsGaugeMetric("cq1", 0)
				behavioral.ExpectCohortSubtreeAdmittedWorkloadsTotalMetric("ch1", "", 5)
				behavioral.ExpectCohortSubtreeAdmittedWorkloadsTotalMetric("root", "", 5)
				behavioral.ExpectCohortSubtreeAdmittedActiveWorkloadsGaugeMetric("root", 0)
				behavioral.ExpectCohortSubtreeAdmittedActiveWorkloadsGaugeMetric("ch1", 0)
			})

			ginkgo.By("Deleting Cohorts", func() {
				for _, cohort := range cohorts {
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cohort, true)
				}
			})

			ginkgo.By("Checking that metrics cleared", func() {
				behavioral.ExpectAdmittedWorkloadsTotalMetric(cq1, "", 0)
				behavioral.ExpectAdmittedActiveWorkloadsGaugeMetric("cq1", 0)
				behavioral.ExpectCohortSubtreeAdmittedWorkloadsTotalMetric("ch1", "", 0)
				behavioral.ExpectCohortSubtreeAdmittedWorkloadsTotalMetric("root", "", 0)
				behavioral.ExpectCohortSubtreeAdmittedActiveWorkloadsGaugeMetric("root", 0)
				behavioral.ExpectCohortSubtreeAdmittedActiveWorkloadsGaugeMetric("ch1", 0)
			})
		})

		ginkgo.It("should clear metrics for the implicit root Cohort after deleting ClusterQueues", func() {
			var cq1 *kueue.ClusterQueue

			ginkgo.By("Create ClusterQueues", func() {
				cq1 = createQueue(utiltestingapi.MakeClusterQueue("cq1").
					Cohort("root").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas(flavor1.Name).
							Resource(corev1.ResourceCPU, "5").
							Obj(),
					).Obj())
			})

			ginkgo.By("Creating Workloads", func() {
				for range 5 {
					createWorkload(
						utiltestingapi.MakeWorkloadWithGeneratedName("workload-", ns.Name).
							Queue("cq1").
							Request(corev1.ResourceCPU, "1").
							Obj(),
					)
				}
			})

			ginkgo.By("Checking that Workloads are admitted", func() {
				behavioral.ExpectAdmittedWorkloadsTotalMetric(cq1, "", 5)
				behavioral.ExpectAdmittedActiveWorkloadsGaugeMetric("cq1", 5)
				behavioral.ExpectCohortSubtreeAdmittedWorkloadsTotalMetric("root", "", 5)
				behavioral.ExpectCohortSubtreeAdmittedActiveWorkloadsGaugeMetric("root", 5)
			})

			ginkgo.By("Deleting Workloads", func() {
				for _, wl := range wls {
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, wl, true)
				}
			})

			ginkgo.By("Deleting LocalQueues", func() {
				for _, lq := range lqs {
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, lq, true)
				}
			})

			ginkgo.By("Checking that metrics cleared", func() {
				behavioral.ExpectAdmittedWorkloadsTotalMetric(cq1, "", 5)
				behavioral.ExpectAdmittedActiveWorkloadsGaugeMetric("cq1", 0)
				behavioral.ExpectCohortSubtreeAdmittedWorkloadsTotalMetric("root", "", 5)
				behavioral.ExpectCohortSubtreeAdmittedActiveWorkloadsGaugeMetric("root", 0)
			})

			ginkgo.By("Deleting ClusterQueues", func() {
				for _, cq := range cqs {
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
				}
			})

			ginkgo.By("Checking that metrics cleared", func() {
				behavioral.ExpectAdmittedWorkloadsTotalMetric(cq1, "", 0)
				behavioral.ExpectAdmittedActiveWorkloadsGaugeMetric("cq1", 0)
				behavioral.ExpectCohortSubtreeAdmittedWorkloadsTotalMetric("root", "", 0)
				behavioral.ExpectCohortSubtreeAdmittedActiveWorkloadsGaugeMetric("root", 0)
			})
		})

		// cq1 -> ch1 -> root1 (implicit)	=> cq1 -> ch1 -> root2 (explicit)
		// cq2 -> root2 (explicit)			=> cq2 -> root2 (explicit)
		ginkgo.It("should clear metrics for the implicit root Cohort after switching to another root Cohort", func() {
			var (
				cq1 *kueue.ClusterQueue
				cq2 *kueue.ClusterQueue
				ch1 *kueue.Cohort
			)

			ginkgo.By("Creating Cohorts", func() {
				ch1 = createCohort(utiltestingapi.MakeCohort("ch1").
					Parent("root1").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas(flavor1.Name).
							Resource(corev1.ResourceCPU, "5").
							Obj(),
					).Obj())

				createCohort(utiltestingapi.MakeCohort("root2").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas(flavor1.Name).
							Resource(corev1.ResourceCPU, "5").
							Obj(),
					).Obj())
			})

			ginkgo.By("Create ClusterQueues", func() {
				cq1 = createQueue(utiltestingapi.MakeClusterQueue("cq1").
					Cohort("ch1").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas(flavor1.Name).
							Resource(corev1.ResourceCPU, "5").
							Obj(),
					).Obj())

				cq2 = createQueue(utiltestingapi.MakeClusterQueue("cq2").
					Cohort("root2").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas(flavor1.Name).
							Resource(corev1.ResourceCPU, "5").
							Obj(),
					).Obj())
			})

			ginkgo.By("Creating Workloads", func() {
				for range 3 {
					createWorkload(
						utiltestingapi.MakeWorkloadWithGeneratedName("workload-", ns.Name).
							Queue("cq1").
							Request(corev1.ResourceCPU, "1").
							Obj(),
					)
				}

				for range 2 {
					createWorkload(
						utiltestingapi.MakeWorkloadWithGeneratedName("workload-", ns.Name).
							Queue("cq2").
							Request(corev1.ResourceCPU, "1").
							Obj(),
					)
				}
			})

			ginkgo.By("Checking that Workloads are admitted", func() {
				behavioral.ExpectAdmittedWorkloadsTotalMetric(cq1, "", 3)
				behavioral.ExpectAdmittedWorkloadsTotalMetric(cq2, "", 2)
				behavioral.ExpectAdmittedActiveWorkloadsGaugeMetric("cq1", 3)
				behavioral.ExpectAdmittedActiveWorkloadsGaugeMetric("cq2", 2)
				behavioral.ExpectCohortSubtreeAdmittedWorkloadsTotalMetric("ch1", "", 3)
				behavioral.ExpectCohortSubtreeAdmittedActiveWorkloadsGaugeMetric("ch1", 3)
				behavioral.ExpectCohortSubtreeAdmittedWorkloadsTotalMetric("root1", "", 3)
				behavioral.ExpectCohortSubtreeAdmittedActiveWorkloadsGaugeMetric("root1", 3)
				behavioral.ExpectCohortSubtreeAdmittedWorkloadsTotalMetric("root2", "", 2)
				behavioral.ExpectCohortSubtreeAdmittedActiveWorkloadsGaugeMetric("root2", 2)
			})

			ginkgo.By("Switch ch1->root1 to ch1->root2", func() {
				createdCh1 := &kueue.Cohort{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ch1), createdCh1)).Should(gomega.Succeed())
					createdCh1.Spec.ParentName = "root2"
					g.Expect(k8sClient.Update(ctx, createdCh1)).Should(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that root1 metrics are cleaned cleared", func() {
				behavioral.ExpectAdmittedWorkloadsTotalMetric(cq1, "", 3)
				behavioral.ExpectAdmittedWorkloadsTotalMetric(cq2, "", 2)
				behavioral.ExpectAdmittedActiveWorkloadsGaugeMetric("cq1", 3)
				behavioral.ExpectAdmittedActiveWorkloadsGaugeMetric("cq2", 2)
				behavioral.ExpectCohortSubtreeAdmittedWorkloadsTotalMetric("ch1", "", 3)
				behavioral.ExpectCohortSubtreeAdmittedActiveWorkloadsGaugeMetric("ch1", 3)
				behavioral.ExpectCohortSubtreeAdmittedWorkloadsTotalMetric("root1", "", 0)
				behavioral.ExpectCohortSubtreeAdmittedActiveWorkloadsGaugeMetric("root1", 0)
				behavioral.ExpectCohortSubtreeAdmittedWorkloadsTotalMetric("root2", "", 2)
				behavioral.ExpectCohortSubtreeAdmittedActiveWorkloadsGaugeMetric("root2", 2)
			})
		})

		ginkgo.It("reports CohortSubtreeResourceReservations across child-parent topology", func() {
			var (
				wlCh1a *kueue.Workload
				wlCh1b *kueue.Workload
				wlCh1c *kueue.Workload
				wlCh2  *kueue.Workload
			)

			ginkgo.By("Create root and children cohorts with explicit quotas/lending limits", func() {
				createCohort(utiltestingapi.MakeCohort("root").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas(defaultFlavor.Name).
							Resource(corev1.ResourceCPU, "30").
							Obj(),
					).Obj())

				createCohort(utiltestingapi.MakeCohort("ch1").
					Parent("root").
					ResourceGroup(
						withLending(kueue.ResourceFlavorReference(defaultFlavor.Name), "8", "3"),
					).Obj())

				createCohort(utiltestingapi.MakeCohort("ch2").
					Parent("root").
					ResourceGroup(
						withLending(kueue.ResourceFlavorReference(defaultFlavor.Name), "6", "2"),
					).Obj())
			})

			ginkgo.By("Create one ClusterQueue under each child cohort (also with lending limits)", func() {
				createQueue(utiltestingapi.MakeClusterQueue("cqa").
					Cohort("ch1").
					ResourceGroup(
						withLending(kueue.ResourceFlavorReference(defaultFlavor.Name), "10", "8"),
					).Obj())

				createQueue(utiltestingapi.MakeClusterQueue("cqb").
					Cohort("ch2").
					ResourceGroup(
						withLending(kueue.ResourceFlavorReference(defaultFlavor.Name), "8", "6"),
					).Obj())

				behavioral.ExpectCohortSubtreeResourceReservationsGaugeMetricCleaned("ch1", defaultFlavor.Name, corev1.ResourceCPU.String())
				behavioral.ExpectCohortSubtreeResourceReservationsGaugeMetricCleaned("ch2", defaultFlavor.Name, corev1.ResourceCPU.String())
				behavioral.ExpectCohortSubtreeResourceReservationsGaugeMetricCleaned("root", defaultFlavor.Name, corev1.ResourceCPU.String())

				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("ch1", defaultFlavor.Name, corev1.ResourceCPU.String(), 16)
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("ch2", defaultFlavor.Name, corev1.ResourceCPU.String(), 12)
				behavioral.ExpectCohortSubtreeQuotaGaugeMetric("root", defaultFlavor.Name, corev1.ResourceCPU.String(), 35)
			})

			ginkgo.By("Admit workload in ch1: reservations rises in ch1 , which is reflected in root", func() {
				wlCh1a = createWorkload(utiltestingapi.MakeWorkloadWithGeneratedName("reservations-", ns.Name).
					Queue(kueue.LocalQueueName("cqa")).
					Request(corev1.ResourceCPU, "6").
					Obj())
				behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlCh1a)

				behavioral.ExpectCohortSubtreeResourceReservationsGaugeMetric("ch1", defaultFlavor.Name, corev1.ResourceCPU.String(), 6)
				behavioral.ExpectCohortSubtreeResourceReservationsGaugeMetric("root", defaultFlavor.Name, corev1.ResourceCPU.String(), 6)
			})

			ginkgo.By("Add more load in ch1", func() {
				wlCh1b = createWorkload(utiltestingapi.MakeWorkloadWithGeneratedName("reservations-", ns.Name).
					Queue(kueue.LocalQueueName("cqa")).
					Request(corev1.ResourceCPU, "8").
					Obj())
				behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlCh1b)

				wlCh1c = createWorkload(utiltestingapi.MakeWorkloadWithGeneratedName("reservations-", ns.Name).
					Queue(kueue.LocalQueueName("cqa")).
					Request(corev1.ResourceCPU, "2").
					Obj())
				behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlCh1c)

				behavioral.ExpectCohortSubtreeResourceReservationsGaugeMetric("ch1", defaultFlavor.Name, corev1.ResourceCPU.String(), 16)
				behavioral.ExpectCohortSubtreeResourceReservationsGaugeMetric("root", defaultFlavor.Name, corev1.ResourceCPU.String(), 16)
			})

			ginkgo.By("Admit workload in ch2 and verify reservations in root", func() {
				wlCh2 = createWorkload(utiltestingapi.MakeWorkloadWithGeneratedName("reservations-", ns.Name).
					Queue(kueue.LocalQueueName("cqb")).
					Request(corev1.ResourceCPU, "7").
					Obj())
				behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlCh2)

				behavioral.ExpectCohortSubtreeResourceReservationsGaugeMetric("ch2", defaultFlavor.Name, corev1.ResourceCPU.String(), 7)
				behavioral.ExpectCohortSubtreeResourceReservationsGaugeMetric("root", defaultFlavor.Name, corev1.ResourceCPU.String(), 23)
			})

			ginkgo.By("Finish one workload and verify reservations", func() {
				behavioral.FinishWorkloads(ctx, k8sClient, wlCh1c)

				behavioral.ExpectCohortSubtreeResourceReservationsGaugeMetric("ch1", defaultFlavor.Name, corev1.ResourceCPU.String(), 14)
				behavioral.ExpectCohortSubtreeResourceReservationsGaugeMetric("root", defaultFlavor.Name, corev1.ResourceCPU.String(), 21)
			})

			ginkgo.By("Release high-overflow workload in ch1 and verify reservations", func() {
				behavioral.FinishWorkloads(ctx, k8sClient, wlCh1b)

				behavioral.ExpectCohortSubtreeResourceReservationsGaugeMetric("ch1", defaultFlavor.Name, corev1.ResourceCPU.String(), 6)
				behavioral.ExpectCohortSubtreeResourceReservationsGaugeMetric("ch2", defaultFlavor.Name, corev1.ResourceCPU.String(), 7)
				behavioral.ExpectCohortSubtreeResourceReservationsGaugeMetric("root", defaultFlavor.Name, corev1.ResourceCPU.String(), 13)
			})

			ginkgo.By("Move cqb from ch2 to ch1 and verify recomputed hierarchical attribution", func() {
				var cq kueue.ClusterQueue
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: "cqb"}, &cq)).To(gomega.Succeed())
					cq.Spec.CohortName = "ch1"
					g.Expect(k8sClient.Update(ctx, &cq)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.ShortInterval).Should(gomega.Succeed())

				// ch1 gains recomputed reservations after the move, ch2 goes to zero
				behavioral.ExpectCohortSubtreeResourceReservationsGaugeMetric("ch1", defaultFlavor.Name, corev1.ResourceCPU.String(), 13)
				behavioral.ExpectCohortSubtreeResourceReservationsGaugeMetric("root", defaultFlavor.Name, corev1.ResourceCPU.String(), 13)
				behavioral.ExpectCohortSubtreeResourceReservationsGaugeMetricCleaned("ch2", defaultFlavor.Name, corev1.ResourceCPU.String())
			})
		})
	})
})
