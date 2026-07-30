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

package centralizedtas

import (
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Centralized TAS spike", ginkgo.Label("area:multikueue", "feature:centralizedtas", "feature:tas"), func() {
	var (
		fixture *centralizedTASFixture
		managerLq *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		fixture = setupCentralizedTASFixture(managerCQSpec{generated: true, cpu: "8", memory: "8Gi"})
		managerLq = createManagerLQ(fixture, fixture.managerCQs[0].Name, "user-queue")
	})

	ginkgo.AfterEach(func() {
		cleanupCentralizedTASFixture(fixture)
	})

	ginkgo.It("should compute manager topology with cluster and hostname levels before dispatch", func() {
		job := createTASJob("topology-levels", fixture.managerNs.Name, managerLq.Name, 1, "500m")
		util.MustCreate(ctx, k8sManagerClient, job)
		waitForJobManagedByMultiKueue(job)

		wlKey := workloadKeyForJob(job)
		clusterName, levels := waitForManagerCentralizedAdmission(wlKey)
		gomega.Expect(levels).To(gomega.Equal([]string{"kueue.x-k8s.io/multikueue-cluster", corev1.LabelHostname}))
		gomega.Expect(clusterName).To(gomega.Or(gomega.Equal(fixture.workerCluster1.Name), gomega.Equal(fixture.workerCluster2.Name)))

		tasNode := getTASWorkerNode(fixture.workers[clusterName].client)
		expectWorkerPodsHostPinned(fixture.workers[clusterName].client, fixture.managerNs.Name, job.Name, tasNode.Name, 1)
	})

	ginkgo.It("should route around a worker whose TAS node is occupied by a non-TAS pod", func() {
		worker1Node := getTASWorkerNode(k8sWorker1Client)
		hogCPU := cpuRequestToSaturateNode(worker1Node, resource.MustParse("500m"))

		ginkgo.By("occupying worker1 TAS node with a non-TAS pod", func() {
			_ = createNonTASPodOnNode(k8sWorker1Client, fixture.worker1Ns.Name, "hog", worker1Node.Name, hogCPU)
		})

		ginkgo.By("waiting for the manager to ingest remote pod usage", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				pods := &corev1.PodList{}
				g.Expect(k8sWorker1Client.List(ctx, pods, client.InNamespace(fixture.worker1Ns.Name))).To(gomega.Succeed())
				g.Expect(pods.Items).NotTo(gomega.BeEmpty())
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		job := createTASJob("avoid-worker1", fixture.managerNs.Name, managerLq.Name, 1, "1")
		util.MustCreate(ctx, k8sManagerClient, job)
		waitForJobManagedByMultiKueue(job)

		wlKey := workloadKeyForJob(job)
		waitForWorkloadAdmittedOnCluster(wlKey, fixture.workerCluster2.Name)

		worker2Node := getTASWorkerNode(k8sWorker2Client)
		expectWorkerPodsHostPinned(k8sWorker2Client, fixture.managerNs.Name, job.Name, worker2Node.Name, 1)
	})

	ginkgo.It("should not admit workloads when central quota exceeds physical fleet capacity", func() {
		ginkgo.By("recreating fixture with inflated manager quota", func() {
			cleanupCentralizedTASFixture(fixture)
			fixture = setupCentralizedTASFixture(managerCQSpec{generated: true, cpu: "1000", memory: "1000Gi"})
			managerLq = createManagerLQ(fixture, fixture.managerCQs[0].Name, "user-queue")
		})

		job := createTASJob("oversubscribed", fixture.managerNs.Name, managerLq.Name, 12, "1")
		util.MustCreate(ctx, k8sManagerClient, job)
		waitForJobManagedByMultiKueue(job)

		wlKey := workloadKeyForJob(job)
		expectWorkloadNotAdmitted(wlKey)
	})
})

var _ = ginkgo.Describe("Centralized TAS fair sharing", ginkgo.Label("area:multikueue", "feature:centralizedtas", "feature:fairsharing", "feature:tas"), func() {
	var (
		fixture   *centralizedTASFixture
		cohort    kueue.CohortReference
		teamACQ   *kueue.ClusterQueue
		teamBCQ   *kueue.ClusterQueue
		teamALq   *kueue.LocalQueue
		teamBLq   *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		cohort = kueue.CohortReference("centralized-tas-cohort")
		fixture = setupCentralizedTASFixture(
			managerCQSpec{name: "team-a", cpu: "1", memory: "1Gi", cohort: cohort},
			managerCQSpec{name: "team-b", cpu: "1", memory: "1Gi", cohort: cohort},
		)
		for _, cq := range fixture.managerCQs {
			switch cq.Name {
			case "team-a":
				teamACQ = cq
			case "team-b":
				teamBCQ = cq
			}
		}
		teamALq = createManagerLQ(fixture, teamACQ.Name, "team-a")
		teamBLq = createManagerLQ(fixture, teamBCQ.Name, "team-b")
	})

	ginkgo.AfterEach(func() {
		cleanupCentralizedTASFixture(fixture)
	})

	ginkgo.It("should reduce high borrowing as workloads finish across worker clusters", func() {
		var teamAJobs []*batchv1.Job
		ginkgo.By("team-a submits workloads that borrow from the cohort", func() {
			for i := range 3 {
				job := createTASJob(fmt.Sprintf("team-a-%d", i), fixture.managerNs.Name, teamALq.Name, 2, "500m")
				util.MustCreate(ctx, k8sManagerClient, job)
				waitForJobManagedByMultiKueue(job)
				teamAJobs = append(teamAJobs, job)
			}
		})

		ginkgo.By("waiting for team-a to enter high borrowing", func() {
			waitForClusterQueueWeightedShare(teamACQ.Name, ">", 100)
		})
		peakBorrowing := waitForClusterQueueWeightedShare(teamACQ.Name, ">=", 100)

		ginkgo.By("team-b also gets admitted work on the global pool", func() {
			job := createTASJob("team-b-0", fixture.managerNs.Name, teamBLq.Name, 1, "500m")
			util.MustCreate(ctx, k8sManagerClient, job)
			waitForJobManagedByMultiKueue(job)
			util.ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sManagerClient, workloadKeyForJob(job))
		})

		ginkgo.By("finishing team-a workloads on both worker clusters", func() {
			for _, job := range teamAJobs {
				wlKey := workloadKeyForJob(job)
				clusterName, _ := waitForManagerCentralizedAdmission(wlKey)
				terminateJobPods(fixture, clusterName, fixture.managerNs.Name, job.Name, 2)
				gomega.Eventually(func(g gomega.Gomega) {
					wl := &kueue.Workload{}
					g.Expect(k8sManagerClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
					g.Expect(wl.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason(kueue.WorkloadFinished, kueue.WorkloadFinishedReasonSucceeded))
				}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
			}
		})

		ginkgo.By("expecting team-a borrowing to drop as capacity returns to the cohort", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				cq := &kueue.ClusterQueue{}
				g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(teamACQ), cq)).To(gomega.Succeed())
				g.Expect(cq.Status.FairSharing).NotTo(gomega.BeNil())
				g.Expect(cq.Status.FairSharing.WeightedShare).To(gomega.BeNumerically("<", peakBorrowing))
				g.Expect(cq.Status.AdmittedWorkloads).To(gomega.Equal(int32(0)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})
	})
})

var _ = ginkgo.Describe("Centralized TAS placement invariants", ginkgo.Label("area:multikueue", "feature:centralizedtas", "feature:tas"), func() {
	var (
		fixture   *centralizedTASFixture
		managerLq *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		fixture = setupCentralizedTASFixture(managerCQSpec{generated: true, cpu: "8", memory: "8Gi"})
		managerLq = createManagerLQ(fixture, fixture.managerCQs[0].Name, "user-queue")
	})

	ginkgo.AfterEach(func() {
		cleanupCentralizedTASFixture(fixture)
	})

	ginkgo.It("should keep an entire gang on a single worker cluster", func() {
		job := createTASJob("gang", fixture.managerNs.Name, managerLq.Name, 3, "500m")
		util.MustCreate(ctx, k8sManagerClient, job)
		waitForJobManagedByMultiKueue(job)

		wlKey := workloadKeyForJob(job)
		clusterName, _ := waitForManagerCentralizedAdmission(wlKey)

		wl := &kueue.Workload{}
		gomega.Expect(k8sManagerClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
		ta := wl.Status.Admission.PodSetAssignments[0].TopologyAssignment
		gomega.Expect(ta.Slices).NotTo(gomega.BeEmpty())
		for _, slice := range ta.Slices {
			gomega.Expect(slice.ValuesPerLevel).NotTo(gomega.BeEmpty())
			clusterVal := slice.ValuesPerLevel[0].Universal
			gomega.Expect(clusterVal).NotTo(gomega.BeNil())
			gomega.Expect(*clusterVal).To(gomega.Equal(clusterName))
		}

		otherCluster := fixture.workerCluster2.Name
		if clusterName == fixture.workerCluster2.Name {
			otherCluster = fixture.workerCluster1.Name
		}
		pods := &corev1.PodList{}
		gomega.Consistently(func(g gomega.Gomega) {
			g.Expect(fixture.workers[otherCluster].client.List(ctx, pods, client.InNamespace(fixture.managerNs.Name))).To(gomega.Succeed())
			g.Expect(pods.Items).To(gomega.BeEmpty())
		}, util.ShortConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
	})
})

var _ = ginkgo.Describe("Centralized TAS preemption", ginkgo.Label("area:multikueue", "feature:centralizedtas", "feature:tas"), func() {
	var (
		fixture   *centralizedTASFixture
		managerLq *kueue.LocalQueue
		highWPC   *kueue.WorkloadPriorityClass
		lowWPC    *kueue.WorkloadPriorityClass
	)

	ginkgo.BeforeEach(func() {
		fixture = setupCentralizedTASFixture(managerCQSpec{
			generated:        true,
			cpu:              "20",
			memory:           "20Gi",
			enablePreemption: true,
		})
		managerLq = createManagerLQ(fixture, fixture.managerCQs[0].Name, "user-queue")
		highWPC, lowWPC = createWorkloadPriorityClasses(fixture)
	})

	ginkgo.AfterEach(func() {
		if highWPC != nil && lowWPC != nil {
			for _, cl := range []client.Client{k8sManagerClient, k8sWorker1Client, k8sWorker2Client} {
				util.ExpectObjectToBeDeletedWithTimeout(ctx, cl, highWPC, true, util.MediumTimeout)
				util.ExpectObjectToBeDeletedWithTimeout(ctx, cl, lowWPC, true, util.MediumTimeout)
			}
		}
		if fixture != nil {
			cleanupCentralizedTASFixture(fixture)
		}
	})

	ginkgo.It("should preempt topology-correctly on only the worker cluster where the high job lands", func() {
		const (
			lowParallelism  int32 = 1
			highParallelism int32 = 2
			lowPodCPU             = "1"
			highPodCPU            = "1"
		)
		highGangCPU := resource.MustParse(highPodCPU)
		for i := int32(1); i < highParallelism; i++ {
			highGangCPU.Add(resource.MustParse(highPodCPU))
		}
		reserveForHog := highGangCPU

		lowJob1 := createTASJobWithWPC("low-a", fixture.managerNs.Name, managerLq.Name, lowWPC.Name, lowParallelism, lowPodCPU)
		util.MustCreate(ctx, k8sManagerClient, lowJob1)
		waitForJobManagedByMultiKueue(lowJob1)
		lowWlKey1 := workloadKeyForJob(lowJob1)

		var lowCluster1 string
		ginkgo.By("waiting for the first low gang to land on a worker", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				wl := &kueue.Workload{}
				g.Expect(k8sManagerClient.Get(ctx, lowWlKey1, wl)).To(gomega.Succeed())
				g.Expect(wl.Status.ClusterName).NotTo(gomega.BeNil())
				g.Expect(wl.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
				lowCluster1 = *wl.Status.ClusterName
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		lowJob2 := createTASJobWithWPC("low-b", fixture.managerNs.Name, managerLq.Name, lowWPC.Name, lowParallelism, lowPodCPU)
		util.MustCreate(ctx, k8sManagerClient, lowJob2)
		waitForJobManagedByMultiKueue(lowJob2)
		lowWlKey2 := workloadKeyForJob(lowJob2)

		var lowCluster2 string
		ginkgo.By("waiting for the second low gang to land on the other worker", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				wl := &kueue.Workload{}
				g.Expect(k8sManagerClient.Get(ctx, lowWlKey2, wl)).To(gomega.Succeed())
				g.Expect(wl.Status.ClusterName).NotTo(gomega.BeNil())
				g.Expect(wl.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
				lowCluster2 = *wl.Status.ClusterName
				g.Expect(lowCluster2).NotTo(gomega.Equal(lowCluster1))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("saturating both worker TAS nodes with non-TAS hogs so the high gang needs preemption", func() {
			hogTASNodeLeavingRoomFor(fixture, lowCluster1, reserveForHog, "hog-a")
			hogTASNodeLeavingRoomFor(fixture, lowCluster2, reserveForHog, "hog-b")
			waitForWorkerPodsInNamespace(fixture.workers[lowCluster1].client, workerNsName(fixture, lowCluster1))
			waitForWorkerPodsInNamespace(fixture.workers[lowCluster2].client, workerNsName(fixture, lowCluster2))
		})

		highJob := createTASJobWithWPC("high-gang", fixture.managerNs.Name, managerLq.Name, highWPC.Name, highParallelism, highPodCPU)
		util.MustCreate(ctx, k8sManagerClient, highJob)
		waitForJobManagedByMultiKueue(highJob)
		highWlKey := workloadKeyForJob(highJob)

		var (
			chosenCluster      string
			evictedWlKey       types.NamespacedName
			unaffectedWlKey    types.NamespacedName
			unaffectedWorker   client.Client
			chosenWorkerClient client.Client
		)

		lowByCluster := map[string]types.NamespacedName{
			lowCluster1: lowWlKey1,
			lowCluster2: lowWlKey2,
		}

		ginkgo.By("waiting for the high-priority gang to be admitted on exactly one worker", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				var admittedOn []string
				for clusterName, wc := range fixture.workers {
					if clusterName == "" {
						continue
					}
					wl := &kueue.Workload{}
					if wc.client.Get(ctx, highWlKey, wl) != nil {
						continue
					}
					g.Expect(wl.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
					admittedOn = append(admittedOn, clusterName)
				}
				g.Expect(admittedOn).To(gomega.HaveLen(1))
				chosenCluster = admittedOn[0]
				evictedWlKey = lowByCluster[chosenCluster]
				for cluster, wlKey := range lowByCluster {
					if cluster != chosenCluster {
						unaffectedWlKey = wlKey
						unaffectedWorker = fixture.workers[cluster].client
						break
					}
				}
				chosenWorkerClient = fixture.workers[chosenCluster].client
				g.Expect(evictedWlKey.Name).NotTo(gomega.BeEmpty())
				g.Expect(unaffectedWlKey.Name).NotTo(gomega.BeEmpty())
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("checking the manager pinned the high gang to the chosen worker cluster", func() {
			waitForWorkloadOnCluster(highWlKey, chosenCluster)
		})

		ginkgo.By("checking only the victim on the chosen worker was preempted", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				evictedWl := &kueue.Workload{}
				g.Expect(k8sManagerClient.Get(ctx, evictedWlKey, evictedWl)).To(gomega.Succeed())
				cond := apimeta.FindStatusCondition(evictedWl.Status.Conditions, kueue.WorkloadEvicted)
				g.Expect(cond).NotTo(gomega.BeNil())
				g.Expect(cond.Status).To(gomega.Equal(metav1.ConditionTrue))
				g.Expect(cond.Reason).To(gomega.Equal(kueue.WorkloadPreempted))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			gomega.Consistently(func(g gomega.Gomega) {
				unaffectedWl := &kueue.Workload{}
				g.Expect(k8sManagerClient.Get(ctx, unaffectedWlKey, unaffectedWl)).To(gomega.Succeed())
				g.Expect(workloadevict.IsEvicted(unaffectedWl)).To(gomega.BeFalse())
				g.Expect(unaffectedWorker.Get(ctx, unaffectedWlKey, unaffectedWl)).To(gomega.Succeed())
				g.Expect(workloadevict.IsEvicted(unaffectedWl)).To(gomega.BeFalse())
			}, util.ShortConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
		})

		ginkgo.By("checking the high gang is host-pinned on the chosen worker", func() {
			tasNode := getTASWorkerNode(chosenWorkerClient)
			expectWorkerPodsHostPinned(chosenWorkerClient, fixture.managerNs.Name, highJob.Name, tasNode.Name, int(highParallelism))
		})
	})
})
