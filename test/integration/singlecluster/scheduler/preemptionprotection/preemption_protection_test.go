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

package preemptionprotection

import (
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
	"sigs.k8s.io/kueue/test/util"
)

const (
	lowPriority int32 = iota - 1
	midPriority
	highPriority
	veryHighPriority
)

const (
	// protectionDuration is the minAdmitDuration used by scenarios that wait
	// out the protection window. The envtest suite runs on a real clock, so
	// it is kept short.
	protectionDuration = 4 * time.Second
	// protectionAssertionWindow is how long tests assert that the protected
	// state holds (victim admitted, preemptor pending). It ends safely inside
	// protectionDuration to tolerate the delay between the victim's admission
	// and the start of the assertion.
	protectionAssertionWindow = 2 * time.Second
	// independenceProtectionDuration is used by the rule-independence
	// scenarios: it is far longer than util.Timeout, so observing a
	// preemption within util.Timeout proves the configured rule did not
	// apply to that preemption type.
	independenceProtectionDuration = 30 * time.Second
)

// Note: a dedicated capacity-reservation scenario (CanAlwaysReclaim treating a
// ClusterQueue with reclaimWithinCohort=Any as unable to always reclaim while
// a reclaim protection window is configured, so the scheduler reserves the
// contested capacity for the waiting reclaimer) is covered by unit tests in
// pkg/scheduler/preemption/policy_test.go and is not duplicated here.
var _ = ginkgo.Describe("Preemption Protection", ginkgo.Label("feature:preemptionprotection"), func() {
	var (
		defaultFlavor *kueue.ResourceFlavor
		ns            *corev1.Namespace

		cohorts []*kueue.Cohort
		cqs     []*kueue.ClusterQueue
		lqs     []*kueue.LocalQueue
		wls     []*kueue.Workload
		checks  []*kueue.AdmissionCheck
	)

	ginkgo.BeforeEach(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.PreemptionProtection, true)
		cohorts, cqs, lqs, wls, checks = nil, nil, nil, nil, nil
	})

	ginkgo.AfterEach(func() {
		for _, wl := range wls {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, wl, true)
		}
		for _, lq := range lqs {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, lq, true)
		}
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		for _, cq := range cqs {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		}
		for _, cohort := range cohorts {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cohort, true)
		}
		for _, check := range checks {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, check, true)
		}
		util.ExpectObjectToBeDeleted(ctx, k8sClient, defaultFlavor, true)
		fwk.StopManager(ctx)
	})

	// startManagerAndCreateBase starts a fresh manager and scheduler with the
	// given configuration and creates the shared flavor and test namespace.
	startManagerAndCreateBase := func(configuration *config.Configuration) {
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup(configuration))

		defaultFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
		util.MustCreate(ctx, k8sClient, defaultFlavor)

		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "preemption-protection-")
	}

	createCohort := func(cohort *kueue.Cohort) *kueue.Cohort {
		util.MustCreate(ctx, k8sClient, cohort)
		cohorts = append(cohorts, cohort)
		return cohort
	}

	// createQueue creates the ClusterQueue and a LocalQueue with the same
	// name, and waits for the ClusterQueue to become active.
	createQueue := func(cq *kueue.ClusterQueue) *kueue.ClusterQueue {
		util.MustCreate(ctx, k8sClient, cq)
		util.ExpectClusterQueuesToBeActive(ctx, k8sClient, cq)
		cqs = append(cqs, cq)

		lq := utiltestingapi.MakeLocalQueue(cq.Name, ns.Name).ClusterQueue(cq.Name).Obj()
		util.MustCreate(ctx, k8sClient, lq)
		util.ExpectLocalQueuesToBeActive(ctx, k8sClient, lq)
		lqs = append(lqs, lq)
		return cq
	}

	// createClusterQueueOnly creates the ClusterQueue and a LocalQueue with
	// the same name without waiting for the ClusterQueue to become active
	// (e.g. when it references an AdmissionCheck that is not active yet).
	createClusterQueueOnly := func(cq *kueue.ClusterQueue) *kueue.ClusterQueue {
		util.MustCreate(ctx, k8sClient, cq)
		cqs = append(cqs, cq)

		lq := utiltestingapi.MakeLocalQueue(cq.Name, ns.Name).ClusterQueue(cq.Name).Obj()
		util.MustCreate(ctx, k8sClient, lq)
		lqs = append(lqs, lq)
		return cq
	}

	createWorkload := func(name string, queue string, priority int32, cpuRequest string) *kueue.Workload {
		wl := utiltestingapi.MakeWorkload(name, ns.Name).
			Queue(kueue.LocalQueueName(queue)).
			Priority(priority).
			Request(corev1.ResourceCPU, cpuRequest).
			Obj()
		util.MustCreate(ctx, k8sClient, wl)
		wls = append(wls, wl)
		return wl
	}

	// expectProtectedFromPreemption asserts, for protectionAssertionWindow,
	// that the victim remains admitted (and not evicted) while the preemptor
	// does not even get a quota reservation.
	expectProtectedFromPreemption := func(victim, preemptor *kueue.Workload) {
		ginkgo.GinkgoHelper()
		gomega.Consistently(func(g gomega.Gomega) {
			updatedVictim := &kueue.Workload{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(victim), updatedVictim)).To(gomega.Succeed())
			g.Expect(workload.IsAdmitted(updatedVictim)).To(gomega.BeTrue(), "the victim should remain admitted within the protection window")
			g.Expect(workloadevict.IsEvicted(updatedVictim)).To(gomega.BeFalse(), "the victim should not be evicted within the protection window")

			updatedPreemptor := &kueue.Workload{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(preemptor), updatedPreemptor)).To(gomega.Succeed())
			g.Expect(workload.HasQuotaReservation(updatedPreemptor)).To(gomega.BeFalse(), "the preemptor should stay pending within the protection window")
		}, protectionAssertionWindow, util.Interval).Should(gomega.Succeed())
	}

	cqPath := func(cq *kueue.ClusterQueue) string {
		if cq.Spec.CohortName != "" {
			return "/" + string(cq.Spec.CohortName) + "/" + cq.Name
		}
		return "/" + cq.Name
	}

	ginkgo.Context("with fair sharing and fairSharing.minAdmitDuration configured", func() {
		var (
			cqBorrower  *kueue.ClusterQueue
			cqPreemptor *kueue.ClusterQueue
		)

		ginkgo.BeforeEach(func() {
			startManagerAndCreateBase(&config.Configuration{
				FairSharing: &config.FairSharing{},
				PreemptionProtection: &config.PreemptionProtection{
					FairSharing: &config.PreemptionProtectionPolicy{
						MinAdmitDuration: &metav1.Duration{Duration: protectionDuration},
					},
				},
			})

			createCohort(utiltestingapi.MakeCohort("fs-cohort").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "8").Obj()).
				Obj())
			cqBorrower = createQueue(utiltestingapi.MakeClusterQueue("fs-borrower").
				Cohort("fs-cohort").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "0").Obj()).
				Obj())
			cqPreemptor = createQueue(utiltestingapi.MakeClusterQueue("fs-preemptor").
				Cohort("fs-cohort").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "0").Obj()).
				Preemption(kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				}).
				Obj())
		})

		ginkgo.It("should protect the borrower during the window and preempt it after expiry without external events", func() {
			ginkgo.By("Admitting a borrowing workload")
			victim := createWorkload("victim", cqBorrower.Name, midPriority, "8")
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, victim)

			ginkgo.By("Creating a competing workload that requires InCohortFairSharing preemption")
			preemptor := createWorkload("preemptor", cqPreemptor.Name, midPriority, "4")

			ginkgo.By("Checking that the victim stays admitted and the preemptor stays pending within the protection window")
			expectProtectedFromPreemption(victim, preemptor)

			// No cluster event is generated between here and the protection
			// expiry: observing the preemption below also validates the
			// protection-expiry retry (queue Manager RebroadcastAtTime).
			ginkgo.By("Checking that the victim is preempted after the protection window expires")
			util.ExpectPreemptedCondition(ctx, k8sClient, kueue.InCohortFairSharingReason, metav1.ConditionTrue,
				victim, preemptor, string(preemptor.UID), "UNKNOWN", cqPath(cqPreemptor), cqPath(cqBorrower))
			util.FinishEvictionForWorkloads(ctx, k8sClient, victim)

			ginkgo.By("Checking that the preemptor is admitted")
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, preemptor)
		})
	})

	ginkgo.Context("with fair sharing and reclaimWithinCohort.minAdmitDuration configured", func() {
		var (
			cqBorrower *kueue.ClusterQueue
			cqOwner    *kueue.ClusterQueue
		)

		ginkgo.BeforeEach(func() {
			startManagerAndCreateBase(&config.Configuration{
				FairSharing: &config.FairSharing{},
				PreemptionProtection: &config.PreemptionProtection{
					ReclaimWithinCohort: &config.PreemptionProtectionPolicy{
						MinAdmitDuration: &metav1.Duration{Duration: protectionDuration},
					},
				},
			})

			cqBorrower = createQueue(utiltestingapi.MakeClusterQueue("fs-reclaim-borrower").
				Cohort("fs-reclaim-cohort").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "4").Obj()).
				Obj())
			cqOwner = createQueue(utiltestingapi.MakeClusterQueue("fs-reclaim-owner").
				Cohort("fs-reclaim-cohort").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "4").Obj()).
				Preemption(kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				}).
				Obj())
		})

		// FairSharingPreemptWithinNominal is enabled by default, so a
		// preemptor within its nominal quota reclaims via the fair sharing
		// path with the InCohortReclamation reason.
		ginkgo.It("should protect the borrower from reclaim during the window and reclaim after expiry", func() {
			ginkgo.By("Admitting a borrowing workload")
			victim := createWorkload("victim", cqBorrower.Name, midPriority, "8")
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, victim)

			ginkgo.By("Creating a workload that reclaims the owner's nominal quota")
			preemptor := createWorkload("preemptor", cqOwner.Name, midPriority, "4")

			ginkgo.By("Checking that the victim stays admitted and the reclaimer stays pending within the protection window")
			expectProtectedFromPreemption(victim, preemptor)

			ginkgo.By("Checking that the victim is reclaimed after the protection window expires")
			util.ExpectPreemptedCondition(ctx, k8sClient, kueue.InCohortReclamationReason, metav1.ConditionTrue,
				victim, preemptor, string(preemptor.UID), "UNKNOWN", cqPath(cqOwner), cqPath(cqBorrower))
			util.FinishEvictionForWorkloads(ctx, k8sClient, victim)

			ginkgo.By("Checking that the reclaimer is admitted")
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, preemptor)
		})
	})

	ginkgo.Context("with classical preemption and reclaimWithinCohort.minAdmitDuration configured", func() {
		var (
			cqBorrower *kueue.ClusterQueue
			cqOwner    *kueue.ClusterQueue
		)

		ginkgo.BeforeEach(func() {
			startManagerAndCreateBase(&config.Configuration{
				PreemptionProtection: &config.PreemptionProtection{
					ReclaimWithinCohort: &config.PreemptionProtectionPolicy{
						MinAdmitDuration: &metav1.Duration{Duration: protectionDuration},
					},
				},
			})

			cqBorrower = createQueue(utiltestingapi.MakeClusterQueue("reclaim-borrower").
				Cohort("reclaim-cohort").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "4").Obj()).
				Obj())
			cqOwner = createQueue(utiltestingapi.MakeClusterQueue("reclaim-owner").
				Cohort("reclaim-cohort").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "4").Obj()).
				Preemption(kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				}).
				Obj())
		})

		ginkgo.It("should protect the borrower from reclaim during the window and reclaim after expiry", func() {
			ginkgo.By("Admitting a borrowing workload")
			victim := createWorkload("victim", cqBorrower.Name, midPriority, "8")
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, victim)

			ginkgo.By("Creating a workload that reclaims the owner's nominal quota")
			preemptor := createWorkload("preemptor", cqOwner.Name, midPriority, "4")

			ginkgo.By("Checking that the victim stays admitted and the reclaimer stays pending within the protection window")
			expectProtectedFromPreemption(victim, preemptor)

			ginkgo.By("Checking that the victim is reclaimed after the protection window expires")
			util.ExpectPreemptedCondition(ctx, k8sClient, kueue.InCohortReclamationReason, metav1.ConditionTrue,
				victim, preemptor, string(preemptor.UID), "UNKNOWN", cqPath(cqOwner), cqPath(cqBorrower))
			util.FinishEvictionForWorkloads(ctx, k8sClient, victim)

			ginkgo.By("Checking that the reclaimer is admitted")
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, preemptor)
		})
	})

	ginkgo.Context("with classical preemption, borrowWithinCohort and reclaimWithinCohort.minAdmitDuration configured", func() {
		var (
			cqStandard   *kueue.ClusterQueue
			cqBestEffort *kueue.ClusterQueue
		)

		ginkgo.BeforeEach(func() {
			startManagerAndCreateBase(&config.Configuration{
				PreemptionProtection: &config.PreemptionProtection{
					ReclaimWithinCohort: &config.PreemptionProtectionPolicy{
						MinAdmitDuration: &metav1.Duration{Duration: protectionDuration},
					},
				},
			})

			// Most quota is in a shared ClusterQueue without a LocalQueue;
			// both the victim and the preemptor need to borrow from it.
			cqStandard = createQueue(utiltestingapi.MakeClusterQueue("rwb-standard").
				Cohort("rwb-cohort").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "1").Obj()).
				Preemption(kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
					BorrowWithinCohort: &kueue.BorrowWithinCohort{
						Policy:               kueue.BorrowWithinCohortPolicyLowerPriority,
						MaxPriorityThreshold: ptr.To(midPriority),
					},
				}).
				Obj())
			cqBestEffort = createQueue(utiltestingapi.MakeClusterQueue("rwb-best-effort").
				Cohort("rwb-cohort").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "1").Obj()).
				Obj())
			sharedCQ := utiltestingapi.MakeClusterQueue("rwb-shared").
				Cohort("rwb-cohort").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "6").Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, sharedCQ)
			util.ExpectClusterQueuesToBeActive(ctx, k8sClient, sharedCQ)
			cqs = append(cqs, sharedCQ)
		})

		ginkgo.It("should protect the borrowing victim from a borrowing preemptor during the window and preempt it after expiry", func() {
			ginkgo.By("Admitting a low priority borrowing workload")
			victim := createWorkload("victim", cqBestEffort.Name, lowPriority, "5")
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, victim)

			ginkgo.By("Creating a high priority workload that preempts while borrowing")
			preemptor := createWorkload("preemptor", cqStandard.Name, veryHighPriority, "7")

			ginkgo.By("Checking that the victim stays admitted and the preemptor stays pending within the protection window")
			expectProtectedFromPreemption(victim, preemptor)

			ginkgo.By("Checking that the victim is preempted after the protection window expires")
			util.ExpectPreemptedCondition(ctx, k8sClient, kueue.InCohortReclaimWhileBorrowingReason, metav1.ConditionTrue,
				victim, preemptor, string(preemptor.UID), "UNKNOWN", cqPath(cqStandard), cqPath(cqBestEffort))
			util.FinishEvictionForWorkloads(ctx, k8sClient, victim)

			ginkgo.By("Checking that the preemptor is admitted")
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, preemptor)
		})
	})

	ginkgo.Context("with fair sharing and only fairSharing.minAdmitDuration configured", func() {
		var (
			cqBorrower *kueue.ClusterQueue
			cqOwner    *kueue.ClusterQueue
		)

		ginkgo.BeforeEach(func() {
			startManagerAndCreateBase(&config.Configuration{
				FairSharing: &config.FairSharing{},
				PreemptionProtection: &config.PreemptionProtection{
					FairSharing: &config.PreemptionProtectionPolicy{
						MinAdmitDuration: &metav1.Duration{Duration: independenceProtectionDuration},
					},
				},
			})

			cqBorrower = createQueue(utiltestingapi.MakeClusterQueue("fs-only-borrower").
				Cohort("fs-only-cohort").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "4").Obj()).
				Obj())
			cqOwner = createQueue(utiltestingapi.MakeClusterQueue("fs-only-owner").
				Cohort("fs-only-cohort").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "4").Obj()).
				Preemption(kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				}).
				Obj())
		})

		ginkgo.It("should not delay InCohortReclamation preemption when only the fairSharing rule is set", func() {
			ginkgo.By("Admitting a borrowing workload")
			victim := createWorkload("victim", cqBorrower.Name, midPriority, "8")
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, victim)

			ginkgo.By("Creating a workload that reclaims the owner's nominal quota")
			preemptor := createWorkload("preemptor", cqOwner.Name, midPriority, "4")

			// The fairSharing protection window (30s) is far longer than the
			// util.Timeout used below, so observing the reclaim proves the
			// fairSharing rule does not apply to InCohortReclamation.
			ginkgo.By("Checking that the victim is reclaimed without waiting for the fairSharing protection window")
			util.ExpectPreemptedCondition(ctx, k8sClient, kueue.InCohortReclamationReason, metav1.ConditionTrue,
				victim, preemptor, string(preemptor.UID), "UNKNOWN", cqPath(cqOwner), cqPath(cqBorrower))
			util.FinishEvictionForWorkloads(ctx, k8sClient, victim)

			ginkgo.By("Checking that the reclaimer is admitted")
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, preemptor)
		})
	})

	ginkgo.Context("with classical preemption and only reclaimWithinCohort.minAdmitDuration configured", func() {
		var cq *kueue.ClusterQueue

		ginkgo.BeforeEach(func() {
			startManagerAndCreateBase(&config.Configuration{
				PreemptionProtection: &config.PreemptionProtection{
					ReclaimWithinCohort: &config.PreemptionProtectionPolicy{
						MinAdmitDuration: &metav1.Duration{Duration: independenceProtectionDuration},
					},
				},
			})

			cq = createQueue(utiltestingapi.MakeClusterQueue("within-cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "4").Obj()).
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
				}).
				Obj())
		})

		ginkgo.It("should not delay within-ClusterQueue preemption when only the reclaimWithinCohort rule is set", func() {
			ginkgo.By("Admitting a low priority workload")
			victim := createWorkload("victim", cq.Name, lowPriority, "4")
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, victim)

			ginkgo.By("Creating a high priority workload in the same ClusterQueue")
			preemptor := createWorkload("preemptor", cq.Name, highPriority, "4")

			// The reclaimWithinCohort protection window (30s) is far longer
			// than the util.Timeout used below, so observing the preemption
			// proves the rule does not apply to within-CQ preemption.
			ginkgo.By("Checking that the victim is preempted without waiting for the reclaim protection window")
			util.ExpectPreemptedCondition(ctx, k8sClient, kueue.InClusterQueueReason, metav1.ConditionTrue,
				victim, preemptor, string(preemptor.UID), "UNKNOWN", cqPath(cq), cqPath(cq))
			util.FinishEvictionForWorkloads(ctx, k8sClient, victim)

			ginkgo.By("Checking that the preemptor is admitted")
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, preemptor)
		})
	})

	ginkgo.Context("with two-phase admission and reclaimWithinCohort.minAdmitDuration configured", func() {
		var (
			cqBorrower *kueue.ClusterQueue
			cqOwner    *kueue.ClusterQueue
			check      *kueue.AdmissionCheck
		)

		ginkgo.BeforeEach(func() {
			startManagerAndCreateBase(&config.Configuration{
				PreemptionProtection: &config.PreemptionProtection{
					ReclaimWithinCohort: &config.PreemptionProtectionPolicy{
						MinAdmitDuration: &metav1.Duration{Duration: protectionDuration},
					},
				},
			})

			check = utiltestingapi.MakeAdmissionCheck("two-phase-check").ControllerName("test-controller").Obj()
			util.MustCreate(ctx, k8sClient, check)
			checks = append(checks, check)
			util.SetAdmissionCheckActive(ctx, k8sClient, check, metav1.ConditionTrue)

			cqBorrower = createClusterQueueOnly(utiltestingapi.MakeClusterQueue("two-phase-borrower").
				Cohort("two-phase-cohort").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "2").Obj()).
				AdmissionChecks(kueue.AdmissionCheckReference(check.Name)).
				Obj())
			cqOwner = createQueue(utiltestingapi.MakeClusterQueue("two-phase-owner").
				Cohort("two-phase-cohort").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "2").Obj()).
				Preemption(kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				}).
				Obj())
			util.ExpectClusterQueuesToBeActive(ctx, k8sClient, cqBorrower)
		})

		ginkgo.It("should measure the protection window from the Admitted condition, not from QuotaReserved", func() {
			ginkgo.By("Creating a borrowing workload that gets QuotaReserved but is not Admitted yet")
			victim := createWorkload("victim", cqBorrower.Name, midPriority, "4")
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cqBorrower.Name, victim)

			ginkgo.By("Waiting out more than the protection duration measured from QuotaReserved while the admission check is pending")
			gomega.Consistently(func(g gomega.Gomega) {
				updatedVictim := &kueue.Workload{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(victim), updatedVictim)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(updatedVictim)).To(gomega.BeTrue())
				g.Expect(workload.IsAdmitted(updatedVictim)).To(gomega.BeFalse())
			}, protectionDuration+time.Second, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Flipping the admission check to Ready so the workload becomes Admitted late")
			util.SetWorkloadsAdmissionCheck(ctx, k8sClient, victim, kueue.AdmissionCheckReference(check.Name), kueue.CheckStateReady, false)
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, victim)

			ginkgo.By("Creating a reclaimer right after the victim is Admitted")
			preemptor := createWorkload("preemptor", cqOwner.Name, midPriority, "2")

			// At this point the protection duration measured from
			// QuotaReserved has long expired, so surviving the assertion
			// window proves the protection is measured from Admitted.
			ginkgo.By("Checking that the victim stays admitted for the full protection window measured from Admitted")
			expectProtectedFromPreemption(victim, preemptor)

			ginkgo.By("Checking that the victim is reclaimed after the protection window expires")
			util.ExpectPreemptedCondition(ctx, k8sClient, kueue.InCohortReclamationReason, metav1.ConditionTrue,
				victim, preemptor, string(preemptor.UID), "UNKNOWN", cqPath(cqOwner), cqPath(cqBorrower))
			util.FinishEvictionForWorkloads(ctx, k8sClient, victim)

			ginkgo.By("Checking that the reclaimer is admitted")
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, preemptor)
		})
	})
})
