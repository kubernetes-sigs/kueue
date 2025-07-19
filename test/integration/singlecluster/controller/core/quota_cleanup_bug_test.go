package core

import (
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Workload quota cleanup bug reproduction", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns           *corev1.Namespace
		localQueue   *kueue.LocalQueue
		clusterQueue *kueue.ClusterQueue
		flavor       *kueue.ResourceFlavor
		check1       *kueue.AdmissionCheck
		realClock    = clock.RealClock{}
	)

	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup)
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "quota-cleanup-bug-")

		// Create ResourceFlavor
		flavor = testing.MakeResourceFlavor(flavorOnDemand).Obj()
		util.MustCreate(ctx, k8sClient, flavor)

		// Create AdmissionCheck
		check1 = testing.MakeAdmissionCheck("check1").ControllerName("ctrl").Obj()
		util.MustCreate(ctx, k8sClient, check1)
		util.SetAdmissionCheckActive(ctx, k8sClient, check1, metav1.ConditionTrue)

		// Create ClusterQueue with AdmissionCheck
		clusterQueue = testing.MakeClusterQueue("cluster-queue").
			ResourceGroup(*testing.MakeFlavorQuotas(flavorOnDemand).
				Resource(resourceGPU, "5", "5").Obj()).
			Cohort("cohort").
			AdmissionChecks("check1").
			Obj()
		util.MustCreate(ctx, k8sClient, clusterQueue)

		// Create LocalQueue
		localQueue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		util.MustCreate(ctx, k8sClient, localQueue)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, check1, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
	})

	ginkgo.It("should clean up quota reservation when AdmissionCheck is rejected", func() {
		wl := testing.MakeWorkload("wl", ns.Name).Queue("queue").Request(resourceGPU, "1").Obj()
		wlKey := client.ObjectKeyFromObject(wl)
		createdWl := kueue.Workload{}

		ginkgo.By("creating the workload, the check conditions should be added", func() {
			util.MustCreate(ctx, k8sClient, wl)

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, &createdWl)).To(gomega.Succeed())
				g.Expect(len(createdWl.Status.AdmissionChecks)).To(gomega.Equal(1))
				g.Expect(createdWl.Status.AdmissionChecks[0].Name).To(gomega.Equal(kueue.AdmissionCheckReference("check1")))
				g.Expect(createdWl.Status.AdmissionChecks[0].State).To(gomega.Equal(kueue.CheckStatePending))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("simulating workload admission for quota reservation only", func() {
			admission := testing.MakeAdmission("cq").Obj()
			gomega.Expect(k8sClient.Get(ctx, wlKey, &createdWl)).To(gomega.Succeed())
			gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, &createdWl, admission)).Should(gomega.Succeed())
			util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, &createdWl)
		})

		ginkgo.By("setting admission check to Ready", func() {
			gomega.Expect(k8sClient.Get(ctx, wlKey, &createdWl)).To(gomega.Succeed())
			workload.SetAdmissionCheckState(&createdWl.Status.AdmissionChecks, kueue.AdmissionCheckState{
				Name:    "check1",
				State:   kueue.CheckStateReady,
				Message: "check ready for testing",
			}, realClock)
			gomega.Expect(k8sClient.Status().Update(ctx, &createdWl)).Should(gomega.Succeed())
		})

		ginkgo.By("rejecting the admission check", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, &createdWl)).To(gomega.Succeed())
				workload.SetAdmissionCheckState(&createdWl.Status.AdmissionChecks, kueue.AdmissionCheckState{
					Name:    "check1",
					State:   kueue.CheckStateRejected,
					Message: "check rejected for testing",
				}, realClock)
				g.Expect(k8sClient.Status().Update(ctx, &createdWl)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verifying the workload is deactivated and evicted", func() {
			updatedWl := &kueue.Workload{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, updatedWl)).To(gomega.Succeed())
				g.Expect(workload.IsActive(updatedWl)).To(gomega.BeFalse())
				g.Expect(workload.IsEvictedByDeactivation(updatedWl)).To(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			// Verify event is emitted
			gomega.Eventually(func(g gomega.Gomega) {
				ok, err := testing.HasEventAppeared(ctx, k8sClient, corev1.Event{
					Reason:  "AdmissionCheckRejected",
					Type:    corev1.EventTypeWarning,
					Message: "Deactivating workload because AdmissionCheck for check1 was Rejected: check rejected for testing",
				})
				g.Expect(err).NotTo(gomega.HaveOccurred())
				g.Expect(ok).To(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("BUG REPRODUCTION: verifying quota reservation is NOT properly cleaned up", func() {
			updatedWl := &kueue.Workload{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, updatedWl)).To(gomega.Succeed())

				// This is the BUG: workload should NOT have both QuotaReserved=True AND Evicted=True
				// When AdmissionCheck is rejected, quota should be cleaned up
				hasQuotaReserved := workload.HasQuotaReservation(updatedWl)
				isEvicted := workload.IsEvicted(updatedWl)

				/*
					"level"=0 "msg"="DEBUG: Workload conditions after rejection:\n"
					"level"=0 "msg"="  - Type: QuotaReserved, Status: True, Reason: QuotaReserved\n"
					"level"=0 "msg"="  - Type: Evicted, Status: True, Reason: DeactivatedDueToAdmissionCheck\n"
					"level"=0 "msg"="  - Type: Admitted, Status: True, Reason: Admitted\n"
					"level"=0 "msg"="DEBUG: HasQuotaReservation: true, IsEvicted: true\n"
					"level"=0 "msg"="DEBUG: Admission status: true\n"
					"level"=0 "msg"="BUG REPRODUCED: Workload has both QuotaReserved=True and Evicted=True\n"
					"level"=0 "msg"="Expected: QuotaReserved should be False when AdmissionCheck is rejected\n"
				*/

				ginkgo.GinkgoLogr.Info("DEBUG: Workload conditions after rejection:\n")
				for _, cond := range updatedWl.Status.Conditions {
					ginkgo.GinkgoLogr.Info(fmt.Sprintf("  - Type: %s, Status: %s, Reason: %s\n", cond.Type, cond.Status, cond.Reason))
				}
				ginkgo.GinkgoLogr.Info(fmt.Sprintf("DEBUG: HasQuotaReservation: %v, IsEvicted: %v\n", hasQuotaReserved, isEvicted))
				ginkgo.GinkgoLogr.Info(fmt.Sprintf("DEBUG: Admission status: %v\n", updatedWl.Status.Admission != nil))

				// The bug manifests as both conditions being true simultaneously
				if hasQuotaReserved && isEvicted {
					ginkgo.GinkgoLogr.Info("BUG REPRODUCED: Workload has both QuotaReserved=True and Evicted=True\n")
					ginkgo.GinkgoLogr.Info("Expected: QuotaReserved should be False when AdmissionCheck is rejected\n")
				}

				/*
					[FAILED] Timed out after 10.002s.
					  The function passed to Eventually failed at /Users/amychen/Gopath/src/github.com/kubernetes-sigs/kueue/test/integration/singlecluster/controller/core/quota_cleanup_bug_test.go:160 with:
					  BUG: Quota reservation should be cleaned up when AdmissionCheck is rejected, but workload still has QuotaReserved=True
					  Expected
					      <bool>: true
					  to be false
					  In [It] at: /Users/amychen/Gopath/src/github.com/kubernetes-sigs/kueue/test/integration/singlecluster/controller/core/quota_cleanup_bug_test.go:163 @ 07/18/25 17:55:41.177

				*/

				// This assertion will FAIL, demonstrating the bug
				// When the bug is fixed, this should pass
				g.Expect(hasQuotaReserved).To(gomega.BeFalse(),
					"BUG: Quota reservation should be cleaned up when AdmissionCheck is rejected, but workload still has QuotaReserved=True")

			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("should clean up quota reservation when AdmissionCheck transitions from Ready to Rejected", func() {
		wl := testing.MakeWorkload("wl-ready-to-rejected", ns.Name).Queue("queue").Request(resourceGPU, "1").Obj()
		wlKey := client.ObjectKeyFromObject(wl)
		createdWl := kueue.Workload{}

		ginkgo.By("creating the workload", func() {
			util.MustCreate(ctx, k8sClient, wl)

			// Wait for admission checks to be added
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, &createdWl)).To(gomega.Succeed())
				g.Expect(len(createdWl.Status.AdmissionChecks)).To(gomega.Equal(1))
				g.Expect(createdWl.Status.AdmissionChecks[0].Name).To(gomega.Equal(kueue.AdmissionCheckReference("check1")))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("manually setting quota reservation", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, &createdWl)).To(gomega.Succeed())
				g.Expect(util.SetQuotaReservation(ctx, k8sClient, &createdWl, testing.MakeAdmission(clusterQueue.Name).Obj())).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			// Sync the admitted condition - this is crucial for integration tests
			util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, &createdWl)
		})

		ginkgo.By("setting admission check to Ready (workload gets admitted)", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, &createdWl)).To(gomega.Succeed())
				workload.SetAdmissionCheckState(&createdWl.Status.AdmissionChecks, kueue.AdmissionCheckState{
					Name:    "check1",
					State:   kueue.CheckStateReady,
					Message: "check ready",
				}, realClock)
				g.Expect(k8sClient.Status().Update(ctx, &createdWl)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			// Verify workload is fully admitted (quota reservation AND admission checks)
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, &createdWl)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(&createdWl)).To(gomega.BeTrue())
				g.Expect(workload.HasQuotaReservation(&createdWl)).To(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("changing admission check from Ready to Rejected", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, &createdWl)).To(gomega.Succeed())
				workload.SetAdmissionCheckState(&createdWl.Status.AdmissionChecks, kueue.AdmissionCheckState{
					Name:    "check1",
					State:   kueue.CheckStateRejected,
					Message: "check failed after being ready",
				}, realClock)
				g.Expect(k8sClient.Status().Update(ctx, &createdWl)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("BUG REPRODUCTION: verifying quota is NOT cleaned up after rejection", func() {
			updatedWl := &kueue.Workload{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, updatedWl)).To(gomega.Succeed())

				hasQuotaReserved := workload.HasQuotaReservation(updatedWl)
				isEvicted := workload.IsEvicted(updatedWl)
				isActive := workload.IsActive(updatedWl)

				/*
					"level"=0 "msg"="DEBUG: After Ready->Rejected transition:\n"
					"level"=0 "msg"="  HasQuotaReservation: true\n"
					"level"=0 "msg"="  IsEvicted: false\n"
					"level"=0 "msg"="  IsActive: true\n"
					"level"=0 "msg"="  AdmissionCheck State: Rejected\n"
				*/

				ginkgo.GinkgoLogr.Info("DEBUG: After Ready->Rejected transition:\n")
				ginkgo.GinkgoLogr.Info(fmt.Sprintf("  HasQuotaReservation: %v\n", hasQuotaReserved))
				ginkgo.GinkgoLogr.Info(fmt.Sprintf("  IsEvicted: %v\n", isEvicted))
				ginkgo.GinkgoLogr.Info(fmt.Sprintf("  IsActive: %v\n", isActive))
				if len(updatedWl.Status.AdmissionChecks) > 0 {
					ginkgo.GinkgoLogr.Info(fmt.Sprintf("  AdmissionCheck State: %v\n", updatedWl.Status.AdmissionChecks[0].State))
				}

				/*
					[FAILED] Timed out after 10.001s.
					  The function passed to Eventually failed at /Users/amychen/Gopath/src/github.com/kubernetes-sigs/kueue/test/integration/singlecluster/controller/core/quota_cleanup_bug_test.go:239 with:
					  BUG: Quota should be cleaned up when AdmissionCheck transitions from Ready to Rejected
					  Expected
					      <bool>: true
					  to be false
					  In [It] at: /Users/amychen/Gopath/src/github.com/kubernetes-sigs/kueue/test/integration/singlecluster/controller/core/quota_cleanup_bug_test.go:244 @ 07/18/25 17:55:52.802

				*/

				// The bug: workload should not retain quota after being rejected
				g.Expect(hasQuotaReserved).To(gomega.BeFalse(),
					"BUG: Quota should be cleaned up when AdmissionCheck transitions from Ready to Rejected")

			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
