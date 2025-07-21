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

package e2e

import (
	"fmt"
	"sync"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Pod Preemption Serialization Issue", ginkgo.Ordered, func() {
	var (
		ns  *corev1.Namespace
		rf  *kueue.ResourceFlavor
		cqA *kueue.ClusterQueue // Cluster Queue A (will borrow)
		cqB *kueue.ClusterQueue // Cluster Queue B (will reclaim)
		lqA *kueue.LocalQueue   // Local Queue A
		lqB *kueue.LocalQueue   // Local Queue B

		// Test configuration with whole numbers for easier math
		nominalQuotaCPU    = "4"   // Each CQ gets 4 CPU nominal
		nominalQuotaMemory = "4Gi" // Each CQ gets 4Gi memory nominal
		workloadCPU        = "1"   // Each workload requests 1 CPU
		workloadMemory     = "1Gi" // Each workload requests 1Gi memory

		// Pod counts to demonstrate serialization
		podCountCqA = 8 // CQ-A gets 8 pods (borrows 4 CPU from CQ-B)
		podCountCqB = 4 // CQ-B gets 4 pods (needs to reclaim 4 CPU)
		podsPerPod  = 1 // Single pod per workload

		terminationGracePeriodSeconds = int64(30) // 30 second pod termination grace period
	)

	ginkgo.BeforeEach(func() {
		// Setup namespace and resource flavor
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test",
			},
		}
		util.MustCreate(ctx, k8sClient, ns)

		rf = testing.MakeResourceFlavor("default").Obj()
		util.MustCreate(ctx, k8sClient, rf)

		// Create Cluster Queue A (will borrow resources)
		cqA = testing.MakeClusterQueue("cq-a").
			Cohort("test-cohort").
			ResourceGroup(*testing.MakeFlavorQuotas(rf.Name).
				Resource(corev1.ResourceCPU, nominalQuotaCPU).
				Resource(corev1.ResourceMemory, nominalQuotaMemory).
				Obj()).
			Preemption(kueue.ClusterQueuePreemption{
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
			}).
			Obj()
		util.MustCreate(ctx, k8sClient, cqA)

		// Create Cluster Queue B (will reclaim resources)
		cqB = testing.MakeClusterQueue("cq-b").
			Cohort("test-cohort").
			ResourceGroup(*testing.MakeFlavorQuotas(rf.Name).
				Resource(corev1.ResourceCPU, nominalQuotaCPU).
				Resource(corev1.ResourceMemory, nominalQuotaMemory).
				Obj()).
			Preemption(kueue.ClusterQueuePreemption{
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
			}).
			Obj()
		util.MustCreate(ctx, k8sClient, cqB)

		// Create Local Queues
		lqA = testing.MakeLocalQueue("lq-a", ns.Name).ClusterQueue(cqA.Name).Obj()
		util.MustCreate(ctx, k8sClient, lqA)

		lqB = testing.MakeLocalQueue("lq-b", ns.Name).ClusterQueue(cqB.Name).Obj()
		util.MustCreate(ctx, k8sClient, lqB)

		// Wait for cluster queues to be active
		util.ExpectClusterQueuesToBeActive(ctx, k8sClient, cqA, cqB)
		util.ExpectLocalQueuesToBeActive(ctx, k8sClient, lqA, lqB)
	})

	ginkgo.AfterEach(func() {
		// Cleanup
		gomega.Expect(util.DeleteAllPodsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
		gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cqA, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cqB, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.FIt("Demonstrates serialized preemption due to scheduler only processing queue heads", func() {
		var borrowingPods [][]*corev1.Pod
		var reclaimingPods [][]*corev1.Pod
		var borrowingWorkloads []*kueue.Workload
		var reclaimingWorkloads []*kueue.Workload

		// 1: Submit pods to CQ-A that will borrow from CQ-B
		ginkgo.By("Creating borrowing pods for CQ-A", func() {
			borrowingPods = make([][]*corev1.Pod, podCountCqA)
			borrowingWorkloads = make([]*kueue.Workload, podCountCqA)

			// Create pods concurrently
			var wg sync.WaitGroup
			for i := 0; i < podCountCqA; i++ {
				wg.Add(1)
				go func(idx int) {
					defer wg.Done()
					podName := fmt.Sprintf("borrowing-%d", idx)
					pod := testingpod.MakePod(podName, ns.Name).
						Queue(lqA.Name).
						Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletionFailOnExit).
						RequestAndLimit(corev1.ResourceCPU, workloadCPU).
						RequestAndLimit(corev1.ResourceMemory, workloadMemory).
						TerminationGracePeriod(terminationGracePeriodSeconds).
						MakeGroup(podsPerPod)

					// Add preStop lifecycle hook to ensure pods take full grace period to shut down
					for _, podPtr := range pod {
						if podPtr.Spec.Containers[0].Lifecycle == nil {
							podPtr.Spec.Containers[0].Lifecycle = &corev1.Lifecycle{}
						}
						podPtr.Spec.Containers[0].Lifecycle.PreStop = &corev1.LifecycleHandler{
							Exec: &corev1.ExecAction{
								Command: []string{"/bin/sh", "-c", fmt.Sprintf("echo 'PreStop hook executed at $(date)' > /tmp/prestop-executed; sleep %d", terminationGracePeriodSeconds-5)},
							},
						}
						util.MustCreate(ctx, k8sClient, podPtr)
					}

					borrowingPods[idx] = pod
				}(i)
			}
			wg.Wait()

			// Get workload references
			for i := range borrowingPods {
				podName := fmt.Sprintf("borrowing-%d", i)
				wlKey := types.NamespacedName{
					Name:      podName,
					Namespace: ns.Name,
				}
				createdWorkload := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, createdWorkload)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
				borrowingWorkloads[i] = createdWorkload
			}
		})

		// 2: Verify all CQ-A workloads are admitted (borrowing from CQ-B)
		ginkgo.By("Verifying all CQ-A workloads are admitted with borrowing", func() {
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, borrowingWorkloads...)

			// Verify CQ-A is borrowing resources
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCQA kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cqA), &updatedCQA)).To(gomega.Succeed())

				// Should have admitted all workloads
				g.Expect(updatedCQA.Status.AdmittedWorkloads).Should(gomega.Equal(int32(podCountCqA)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			// Verify that all pods are running
			for i, pod := range borrowingPods {
				ginkgo.By(fmt.Sprintf("Verifying pod %d is running", i), func() {
					for _, podPtr := range pod {
						gomega.Eventually(func(g gomega.Gomega) {
							var updatedPod corev1.Pod
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(podPtr), &updatedPod)).To(gomega.Succeed())

							// Pod should be running
							g.Expect(updatedPod.Status.Phase).Should(gomega.Equal(corev1.PodRunning))
						}, util.Timeout, util.Interval).Should(gomega.Succeed())
					}
				})
			}
		})

		// 3: Submit pods to CQ-B that will require preemption from CQ-A
		ginkgo.By("Creating pods for CQ-B that require preemption", func() {
			reclaimingPods = make([][]*corev1.Pod, podCountCqB)
			reclaimingWorkloads = make([]*kueue.Workload, podCountCqB)

			// Create all pods that need preemption from CQ-A at once to demonstrate serialized preemption issue
			var wg sync.WaitGroup
			for i := 0; i < podCountCqB; i++ {
				wg.Add(1)
				go func(idx int) {
					defer wg.Done()
					podName := fmt.Sprintf("reclaiming-%d", idx)
					pod := testingpod.MakePod(podName, ns.Name).
						Queue(lqB.Name).
						Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletionFailOnExit).
						RequestAndLimit(corev1.ResourceCPU, workloadCPU).
						RequestAndLimit(corev1.ResourceMemory, workloadMemory).
						TerminationGracePeriod(terminationGracePeriodSeconds).
						MakeGroup(podsPerPod)

					// Add preStop lifecycle hook to ensure pods take full grace period to shut down
					for _, podPtr := range pod {
						if podPtr.Spec.Containers[0].Lifecycle == nil {
							podPtr.Spec.Containers[0].Lifecycle = &corev1.Lifecycle{}
						}
						podPtr.Spec.Containers[0].Lifecycle.PreStop = &corev1.LifecycleHandler{
							Exec: &corev1.ExecAction{
								Command: []string{"/bin/sh", "-c", fmt.Sprintf("echo 'PreStop hook executed at $(date)' > /tmp/prestop-executed; sleep %d", terminationGracePeriodSeconds-5)},
							},
						}
						util.MustCreate(ctx, k8sClient, podPtr)
					}

					reclaimingPods[idx] = pod
				}(i)
			}
			wg.Wait()

			// Get workload references
			for i := range reclaimingPods {
				podName := fmt.Sprintf("reclaiming-%d", i)
				wlKey := types.NamespacedName{
					Name:      podName,
					Namespace: ns.Name,
				}
				createdWorkload := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, createdWorkload)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
				reclaimingWorkloads[i] = createdWorkload
			}
		})

		// 4: Track when each workload gets admitted
		ginkgo.By("Demonstrating serialized admission due to scheduler processing only queue heads", func() {
			admissionTimes := make([]time.Time, podCountCqB)

			// Track when each workload gets admitted
			for i, wl := range reclaimingWorkloads {
				wlIndex := i // capture for closure

				gomega.Eventually(func(g gomega.Gomega) {
					var updatedWl kueue.Workload
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())

					// Check if workload is admitted
					admittedCondition := apimeta.FindStatusCondition(updatedWl.Status.Conditions, kueue.WorkloadAdmitted)
					if admittedCondition != nil && admittedCondition.Status == metav1.ConditionTrue {
						admissionTimes[wlIndex] = time.Now()
						return
					}

					g.Expect(false).To(gomega.BeTrue(), fmt.Sprintf("Workload %s not yet admitted", wl.Name))
				}, 10*time.Minute, util.Interval).Should(gomega.Succeed())
			}

			// Calculate timing analysis
			totalAdmissionTime := admissionTimes[len(admissionTimes)-1].Sub(admissionTimes[0])
			var maxAdmissionGap time.Duration

			for i := 1; i < len(admissionTimes); i++ {
				gap := admissionTimes[i].Sub(admissionTimes[i-1])
				if gap > maxAdmissionGap {
					maxAdmissionGap = gap
				}
			}

			// Validate serialization behavior
			gomega.Expect(maxAdmissionGap).Should(gomega.BeNumerically(">=", 20*time.Second),
				"At least one workload should experience significant preemption delay")

			// Final comprehensive log output
			var finalCQA, finalCQB kueue.ClusterQueue
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cqA), &finalCQA)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cqB), &finalCQB)).To(gomega.Succeed())

			ginkgo.GinkgoLogr.Info("=== KUEUE SERIALIZED PREEMPTION ISSUE TEST RESULTS ===")
			ginkgo.GinkgoLogr.Info("Total Workloads Causing Preemption", "count", podCountCqB)
			ginkgo.GinkgoLogr.Info("Total Admission Time", "duration", totalAdmissionTime)
			ginkgo.GinkgoLogr.Info("Max Admission Gap", "duration", maxAdmissionGap)
		})
	})
})

/*
The issue is more evident when you run "kubectl get clusterqueue -w" alongside this e2e:
Amys-MacBook-Pro:kueue amychen$ kubectl get clusterqueue -w
NAME   COHORT        PENDING WORKLOADS
cq-a   test-cohort   0
cq-b   test-cohort   4
--- 30 seconds ---
cq-a   test-cohort   1
cq-b   test-cohort   3
--- 30 seconds ---
cq-a   test-cohort   2
cq-b   test-cohort   2
--- 30 seconds ---
cq-a   test-cohort   3
cq-b   test-cohort   1
--- 30 seconds ---
cq-a   test-cohort   3
cq-a   test-cohort   3
--- 30 seconds ---
cq-b   test-cohort   1
cq-b   test-cohort   1
*/

/*
 STEP: Demonstrating serialized admission due to scheduler processing only queue heads @ 07/21/25 15:50:55.295
  "level"=0 "msg"="=== KUEUE SERIALIZED PREEMPTION ISSUE TEST RESULTS ==="
  "level"=0 "msg"="Total Workloads Causing Preemption" "count"=4
  "level"=0 "msg"="Total Admission Time" "duration"="1m16.346381875s"
  "level"=0 "msg"="Max Admission Gap" "duration"="51.089174917s"
*/
