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

package baseline

import (
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	visibility "sigs.k8s.io/kueue/apis/visibility/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("Kueue visibility server", ginkgo.Label("area:singlecluster", "feature:visibility"), ginkgo.Serial, func() {
	// We do not check workload's Name, CreationTimestamp, and its OwnerReference's UID as they are generated at the server-side.
	var pendingWorkloadsCmpOpts = cmp.Options{
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "Name"),
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "CreationTimestamp"),
		cmpopts.IgnoreFields(metav1.OwnerReference{}, "UID"),
	}

	var (
		defaultRF         *kueue.ResourceFlavor
		localQueueA       *kueue.LocalQueue
		localQueueB       *kueue.LocalQueue
		clusterQueue      *kueue.ClusterQueue
		nsA               *corev1.Namespace
		nsB               *corev1.Namespace
		blockingJob       *batchv1.Job
		sampleJob2        *batchv1.Job
		highPriorityClass *kueue.WorkloadPriorityClass
		midPriorityClass  *kueue.WorkloadPriorityClass
		lowPriorityClass  *kueue.WorkloadPriorityClass
		defaultFlavor     string
	)

	ginkgo.BeforeEach(func() {
		nsA = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-")
		nsB = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-")
		defaultFlavor = "default-flavor-" + nsA.Name
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, nsA)).To(gomega.Succeed())
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, nsB)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, defaultRF, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		behavioral.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, nsA)
		behavioral.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, nsB)
	})

	ginkgo.When("There are pending workloads due to capacity maxed by the admitted job", func() {
		ginkgo.BeforeEach(func() {
			defaultRF = utiltestingapi.MakeResourceFlavor(defaultFlavor).Obj()
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Create(ctx, defaultRF)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue-" + nsA.Name).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(defaultFlavor).
						Resource(corev1.ResourceCPU, "1").
						Obj(),
				).
				Obj()
			behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

			localQueueA = utiltestingapi.MakeLocalQueue("a", nsA.Name).ClusterQueue(clusterQueue.Name).Obj()
			localQueueB = utiltestingapi.MakeLocalQueue("b", nsA.Name).ClusterQueue(clusterQueue.Name).Obj()
			behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueueA, localQueueB)

			highPriorityClass = utiltestingapi.MakeWorkloadPriorityClass("high-" + nsA.Name).PriorityValue(100).Obj()
			behavioral.MustCreate(ctx, k8sClient, highPriorityClass)

			midPriorityClass = utiltestingapi.MakeWorkloadPriorityClass("mid-" + nsA.Name).PriorityValue(75).Obj()
			behavioral.MustCreate(ctx, k8sClient, midPriorityClass)

			lowPriorityClass = utiltestingapi.MakeWorkloadPriorityClass("low-" + nsA.Name).PriorityValue(50).Obj()
			behavioral.MustCreate(ctx, k8sClient, lowPriorityClass)

			ginkgo.By("Schedule a job that when admitted workload blocks the queue", func() {
				blockingJob = testingjob.MakeJob("test-job-1", nsA.Name).
					Queue(kueue.LocalQueueName(localQueueA.Name)).
					Image(behavioral.GetAgnHostImage(), behavioral.BehaviorWaitForDeletion).
					RequestAndLimit(corev1.ResourceCPU, "1").
					TerminationGracePeriod(1).
					BackoffLimit(0).
					WorkloadPriorityClass(highPriorityClass.Name).
					Obj()
				behavioral.MustCreate(ctx, k8sClient, blockingJob)
			})
			ginkgo.By("Ensure the workload is admitted, by awaiting until the job is unsuspended", func() {
				expectJobUnsuspended(client.ObjectKeyFromObject(blockingJob))
			})
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(behavioral.DeleteAllJobsInNamespace(ctx, k8sClient, nsA)).Should(gomega.Succeed())
			gomega.Expect(behavioral.DeleteAllJobsInNamespace(ctx, k8sClient, nsB)).Should(gomega.Succeed())

			// Force remove workloads to be sure that cluster queue can be removed.
			gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, nsA)).Should(gomega.Succeed())
			gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, nsB)).Should(gomega.Succeed())

			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, localQueueA, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, localQueueB, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, defaultRF, true)

			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, lowPriorityClass, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, midPriorityClass, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, highPriorityClass, true)
		})

		ginkgo.It("Should allow fetching information about pending workloads in ClusterQueue", func() {
			ginkgo.By("Verify there are zero pending workloads", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					info, err := kueueClientset.VisibilityV1beta2().ClusterQueues().GetPendingWorkloadsSummary(ctx, clusterQueue.Name, metav1.GetOptions{})
					g.Expect(err).NotTo(gomega.HaveOccurred())
					g.Expect(info.Items).Should(gomega.BeEmpty())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Schedule a job which is pending due to lower priority", func() {
				sampleJob2 = testingjob.MakeJob("test-job-2", nsA.Name).
					Queue(kueue.LocalQueueName(localQueueA.Name)).
					Image(behavioral.GetAgnHostImage(), behavioral.BehaviorExitFast).
					RequestAndLimit(corev1.ResourceCPU, "1").
					WorkloadPriorityClass(lowPriorityClass.Name).
					Obj()
				behavioral.MustCreate(ctx, k8sClient, sampleJob2)
			})

			ginkgo.By("Verify there is one pending workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					info, err := kueueClientset.VisibilityV1beta2().ClusterQueues().GetPendingWorkloadsSummary(ctx, clusterQueue.Name, metav1.GetOptions{})
					g.Expect(err).NotTo(gomega.HaveOccurred())
					g.Expect(info.Items).Should(gomega.HaveLen(1))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Await for pods to be running", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdJob := &batchv1.Job{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(blockingJob), createdJob)).Should(gomega.Succeed())
					g.Expect(createdJob.Status.Ready).Should(gomega.Equal(new(int32(1))))
				}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Terminate execution of the first workload to release the quota", func() {
				gomega.Expect(behavioral.DeleteAllPodsInNamespace(ctx, k8sClient, nsA)).Should(gomega.Succeed())
			})

			ginkgo.By("Verify there are zero pending workloads, after the second workload is admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					info, err := kueueClientset.VisibilityV1beta2().ClusterQueues().GetPendingWorkloadsSummary(ctx, clusterQueue.Name, metav1.GetOptions{})
					g.Expect(err).NotTo(gomega.HaveOccurred())
					g.Expect(info.Items).Should(gomega.BeEmpty())
				}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should allow fetching information about position of pending workloads in ClusterQueue", func() {
			ginkgo.By("Schedule three different jobs with different priorities and two different LocalQueues", func() {
				createPendingJobs([]pendingJobCase{
					{JobName: "lq-a-high-prio", JobPrioClassName: highPriorityClass.Name, LocalQueueName: localQueueA.Name, nsName: nsA.Name},
					{JobName: "lq-b-mid-prio", JobPrioClassName: midPriorityClass.Name, LocalQueueName: localQueueB.Name, nsName: nsA.Name},
					{JobName: "lq-b-low-prio", JobPrioClassName: lowPriorityClass.Name, LocalQueueName: localQueueB.Name, nsName: nsA.Name},
				})
			})

			ginkgo.By("Verify their positions and priorities", func() {
				wantPendingWorkloads := []visibility.PendingWorkload{
					{
						Namespace:              nsA.Name,
						OwnerReferences:        defaultOwnerReferenceForJob("lq-a-high-prio"),
						Priority:               highPriorityClass.Value,
						PositionInLocalQueue:   0,
						PositionInClusterQueue: 0,
						LocalQueueName:         kueue.LocalQueueName(localQueueA.Name),
					},
					{
						Namespace:              nsA.Name,
						OwnerReferences:        defaultOwnerReferenceForJob("lq-b-mid-prio"),
						Priority:               midPriorityClass.Value,
						PositionInLocalQueue:   0,
						PositionInClusterQueue: 1,
						LocalQueueName:         kueue.LocalQueueName(localQueueB.Name),
					},
					{
						Namespace:              nsA.Name,
						OwnerReferences:        defaultOwnerReferenceForJob("lq-b-low-prio"),
						Priority:               lowPriorityClass.Value,
						PositionInLocalQueue:   1,
						PositionInClusterQueue: 2,
						LocalQueueName:         kueue.LocalQueueName(localQueueB.Name),
					},
				}
				gomega.Eventually(func(g gomega.Gomega) {
					info, err := kueueClientset.VisibilityV1beta2().ClusterQueues().GetPendingWorkloadsSummary(ctx, clusterQueue.Name, metav1.GetOptions{})
					g.Expect(err).NotTo(gomega.HaveOccurred())
					g.Expect(info.Items).Should(gomega.BeComparableTo(wantPendingWorkloads, pendingWorkloadsCmpOpts...))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should allow fetching information about pending workloads in LocalQueue", func() {
			ginkgo.By("Verify there are zero pending workloads", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					info, err := kueueClientset.VisibilityV1beta2().LocalQueues(nsA.Name).GetPendingWorkloadsSummary(ctx, localQueueA.Name, metav1.GetOptions{})
					g.Expect(err).NotTo(gomega.HaveOccurred())
					g.Expect(info.Items).Should(gomega.BeEmpty())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Schedule a job which is pending due to lower priority", func() {
				sampleJob2 = testingjob.MakeJob("test-job-2", nsA.Name).
					Queue(kueue.LocalQueueName(localQueueA.Name)).
					Image(behavioral.GetAgnHostImage(), behavioral.BehaviorExitFast).
					RequestAndLimit(corev1.ResourceCPU, "1").
					WorkloadPriorityClass(lowPriorityClass.Name).
					Obj()
				behavioral.MustCreate(ctx, k8sClient, sampleJob2)
			})

			ginkgo.By("Verify there is one pending workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					info, err := kueueClientset.VisibilityV1beta2().LocalQueues(nsA.Name).GetPendingWorkloadsSummary(ctx, localQueueA.Name, metav1.GetOptions{})
					g.Expect(err).NotTo(gomega.HaveOccurred())
					g.Expect(info.Items).Should(gomega.HaveLen(1))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Await for pods to be running", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdJob := &batchv1.Job{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(blockingJob), createdJob)).Should(gomega.Succeed())
					g.Expect(createdJob.Status.Ready).Should(gomega.Equal(new(int32(1))))
				}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Terminate execution of the first workload to release the quota", func() {
				gomega.Expect(behavioral.DeleteAllPodsInNamespace(ctx, k8sClient, nsA)).Should(gomega.Succeed())
			})

			ginkgo.By("Verify there are zero pending workloads, after the second workload is admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					info, err := kueueClientset.VisibilityV1beta2().LocalQueues(nsA.Name).GetPendingWorkloadsSummary(ctx, localQueueA.Name, metav1.GetOptions{})
					g.Expect(err).NotTo(gomega.HaveOccurred())
					g.Expect(info.Items).Should(gomega.BeEmpty())
				}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should allow fetching information about position of pending workloads from different LocalQueues", func() {
			ginkgo.By("Schedule three different jobs with different priorities and two different LocalQueues", func() {
				createPendingJobs([]pendingJobCase{
					{JobName: "lq-a-high-prio", JobPrioClassName: highPriorityClass.Name, LocalQueueName: localQueueA.Name, nsName: nsA.Name},
					{JobName: "lq-b-mid-prio", JobPrioClassName: midPriorityClass.Name, LocalQueueName: localQueueB.Name, nsName: nsA.Name},
					{JobName: "lq-b-low-prio", JobPrioClassName: lowPriorityClass.Name, LocalQueueName: localQueueB.Name, nsName: nsA.Name},
				})
			})

			ginkgo.By("Verify their positions and priorities in LocalQueueA", func() {
				wantPendingWorkloads := []visibility.PendingWorkload{
					{
						Namespace:              nsA.Name,
						OwnerReferences:        defaultOwnerReferenceForJob("lq-a-high-prio"),
						Priority:               highPriorityClass.Value,
						PositionInLocalQueue:   0,
						PositionInClusterQueue: 0,
						LocalQueueName:         kueue.LocalQueueName(localQueueA.Name),
					},
				}
				gomega.Eventually(func(g gomega.Gomega) {
					info, err := kueueClientset.VisibilityV1beta2().LocalQueues(nsA.Name).GetPendingWorkloadsSummary(ctx, localQueueA.Name, metav1.GetOptions{})
					g.Expect(err).NotTo(gomega.HaveOccurred())
					g.Expect(info.Items).Should(gomega.BeComparableTo(wantPendingWorkloads, pendingWorkloadsCmpOpts...))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Verify their positions and priorities in LocalQueueB", func() {
				wantPendingWorkloads := []visibility.PendingWorkload{
					{
						Namespace:              nsA.Name,
						OwnerReferences:        defaultOwnerReferenceForJob("lq-b-mid-prio"),
						Priority:               midPriorityClass.Value,
						PositionInLocalQueue:   0,
						PositionInClusterQueue: 1,
						LocalQueueName:         kueue.LocalQueueName(localQueueB.Name),
					},
					{
						Namespace:              nsA.Name,
						OwnerReferences:        defaultOwnerReferenceForJob("lq-b-low-prio"),
						Priority:               lowPriorityClass.Value,
						PositionInLocalQueue:   1,
						PositionInClusterQueue: 2,
						LocalQueueName:         kueue.LocalQueueName(localQueueB.Name),
					},
				}
				gomega.Eventually(func(g gomega.Gomega) {
					info, err := kueueClientset.VisibilityV1beta2().LocalQueues(nsA.Name).GetPendingWorkloadsSummary(ctx, localQueueB.Name, metav1.GetOptions{})
					g.Expect(err).NotTo(gomega.HaveOccurred())
					g.Expect(info.Items).Should(gomega.BeComparableTo(wantPendingWorkloads, pendingWorkloadsCmpOpts...))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should fetch information about the position of pending workloads with the same localQueue name in different namespaces", func() {
			const localQueueName = "local-queue"

			ginkgo.By("Create a LocalQueue", func() {
				lqA := utiltestingapi.MakeLocalQueue(localQueueName, nsA.Name).ClusterQueue(clusterQueue.Name).Obj()
				behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lqA)
			})

			ginkgo.By("Create a LocalQueue with the same name in a different Namespace", func() {
				lqB := utiltestingapi.MakeLocalQueue(localQueueName, nsB.Name).ClusterQueue(clusterQueue.Name).Obj()
				behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lqB)
			})

			ginkgo.By("Schedule different jobs in different Namespaces", func() {
				jobCases := []struct {
					name     string
					ns       string
					priority string
				}{
					{name: "job-a", ns: nsA.Name, priority: midPriorityClass.Name},
					{name: "job-b", ns: nsB.Name, priority: lowPriorityClass.Name},
				}
				for _, jobCase := range jobCases {
					job := testingjob.MakeJob(jobCase.name, jobCase.ns).
						Queue(localQueueName).
						Image(behavioral.GetAgnHostImage(), behavioral.BehaviorWaitForDeletion).
						RequestAndLimit(corev1.ResourceCPU, "2").
						WorkloadPriorityClass(jobCase.priority).
						TerminationGracePeriod(1).
						Obj()
					behavioral.MustCreate(ctx, k8sClient, job)
				}
			})

			ginkgo.By("Verify their positions and priorities in Namespace 'a'", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					info, err := kueueClientset.VisibilityV1beta2().LocalQueues(nsA.Name).GetPendingWorkloadsSummary(ctx, localQueueName, metav1.GetOptions{})
					g.Expect(err).NotTo(gomega.HaveOccurred())
					g.Expect(info.Items).Should(gomega.BeComparableTo([]visibility.PendingWorkload{{
						Namespace:              nsA.Name,
						OwnerReferences:        defaultOwnerReferenceForJob("job-a"),
						Priority:               midPriorityClass.Value,
						PositionInLocalQueue:   0,
						PositionInClusterQueue: 0,
						LocalQueueName:         localQueueName,
					}}, pendingWorkloadsCmpOpts...))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Verify their positions and priorities in Namespace 'b'", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					info, err := kueueClientset.VisibilityV1beta2().LocalQueues(nsB.Name).GetPendingWorkloadsSummary(ctx, localQueueName, metav1.GetOptions{})
					g.Expect(err).NotTo(gomega.HaveOccurred())
					g.Expect(info.Items).Should(gomega.BeComparableTo([]visibility.PendingWorkload{{
						Namespace:              nsB.Name,
						OwnerReferences:        defaultOwnerReferenceForJob("job-b"),
						Priority:               lowPriorityClass.Value,
						PositionInLocalQueue:   0,
						PositionInClusterQueue: 1,
						LocalQueueName:         localQueueName,
					}}, pendingWorkloadsCmpOpts...))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should allow fetching information about position of pending workloads from different LocalQueues from different Namespaces", func() {
			ginkgo.By("Create a LocalQueue in a different Namespace", func() {
				localQueueB = utiltestingapi.MakeLocalQueue("b", nsB.Name).ClusterQueue(clusterQueue.Name).Obj()
				behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueueB)
			})

			ginkgo.By("Schedule three different jobs with different priorities and different LocalQueues in different Namespaces", func() {
				createPendingJobs([]pendingJobCase{
					{JobName: "lq-a-high-prio", JobPrioClassName: highPriorityClass.Name, LocalQueueName: localQueueA.Name, nsName: nsA.Name},
					{JobName: "lq-b-mid-prio", JobPrioClassName: midPriorityClass.Name, LocalQueueName: localQueueB.Name, nsName: nsB.Name},
					{JobName: "lq-b-low-prio", JobPrioClassName: lowPriorityClass.Name, LocalQueueName: localQueueB.Name, nsName: nsB.Name},
				})
			})

			ginkgo.By("Verify their positions and priorities in LocalQueueA", func() {
				wantPendingWorkloads := []visibility.PendingWorkload{
					{
						Namespace:              nsA.Name,
						OwnerReferences:        defaultOwnerReferenceForJob("lq-a-high-prio"),
						Priority:               highPriorityClass.Value,
						PositionInLocalQueue:   0,
						PositionInClusterQueue: 0,
						LocalQueueName:         kueue.LocalQueueName(localQueueA.Name),
					},
				}
				gomega.Eventually(func(g gomega.Gomega) {
					info, err := kueueClientset.VisibilityV1beta2().LocalQueues(nsA.Name).GetPendingWorkloadsSummary(ctx, localQueueA.Name, metav1.GetOptions{})
					g.Expect(err).NotTo(gomega.HaveOccurred())
					g.Expect(info.Items).Should(gomega.BeComparableTo(wantPendingWorkloads, pendingWorkloadsCmpOpts...))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Verify their positions and priorities in LocalQueueB", func() {
				wantPendingWorkloads := []visibility.PendingWorkload{
					{
						Namespace:              nsB.Name,
						OwnerReferences:        defaultOwnerReferenceForJob("lq-b-mid-prio"),
						Priority:               midPriorityClass.Value,
						PositionInLocalQueue:   0,
						PositionInClusterQueue: 1,
						LocalQueueName:         kueue.LocalQueueName(localQueueB.Name),
					},
					{
						Namespace:              nsB.Name,
						OwnerReferences:        defaultOwnerReferenceForJob("lq-b-low-prio"),
						Priority:               lowPriorityClass.Value,
						PositionInLocalQueue:   1,
						PositionInClusterQueue: 2,
						LocalQueueName:         kueue.LocalQueueName(localQueueB.Name),
					},
				}
				gomega.Eventually(func(g gomega.Gomega) {
					info, err := kueueClientset.VisibilityV1beta2().LocalQueues(nsB.Name).GetPendingWorkloadsSummary(ctx, localQueueB.Name, metav1.GetOptions{})
					g.Expect(err).NotTo(gomega.HaveOccurred())
					g.Expect(info.Items).Should(gomega.BeComparableTo(wantPendingWorkloads, pendingWorkloadsCmpOpts...))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.When("A subject is bound to kueue-batch-user-role, but not to kueue-batch-admin-role, and reads a LocalQueue sharing a ClusterQueue across Namespaces", func() {
			var roleBinding *rbacv1.RoleBinding

			ginkgo.BeforeEach(func() {
				ginkgo.By("Create a LocalQueue in a different Namespace sharing the same ClusterQueue", func() {
					localQueueB = utiltestingapi.MakeLocalQueue("b", nsB.Name).ClusterQueue(clusterQueue.Name).Obj()
					behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueueB)
				})

				ginkgo.By("Schedule a pending job in each Namespace against the shared ClusterQueue", func() {
					createPendingJobs([]pendingJobCase{
						{JobName: "lq-a-low-prio", JobPrioClassName: lowPriorityClass.Name, LocalQueueName: localQueueA.Name, nsName: nsA.Name},
						{JobName: "lq-b-mid-prio", JobPrioClassName: midPriorityClass.Name, LocalQueueName: localQueueB.Name, nsName: nsB.Name},
					})
				})

				roleBinding = mustCreateBatchUserRoleBinding(nsA.Name)
				ginkgo.By("Wait for ResourceNotFound error instead of Forbidden to make sure the role binding works", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						_, err := impersonatedVisibilityClient.LocalQueues(nsA.Name).GetPendingWorkloadsSummary(ctx, "non-existent", metav1.GetOptions{})
						g.Expect(err).Should(utiltesting.BeNotFoundError())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				})
			})

			ginkgo.AfterEach(func() {
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, roleBinding, true)
			})

			ginkgo.It("Should only expose pending workloads from the caller's own Namespace", func() {
				ginkgo.By("Verifying the user only sees their own Namespace's pending workload in LocalQueueA", func() {
					wantPendingWorkloads := []visibility.PendingWorkload{
						{
							Namespace:              nsA.Name,
							OwnerReferences:        defaultOwnerReferenceForJob("lq-a-low-prio"),
							Priority:               lowPriorityClass.Value,
							PositionInLocalQueue:   0,
							PositionInClusterQueue: 1,
							LocalQueueName:         kueue.LocalQueueName(localQueueA.Name),
						},
					}
					gomega.Eventually(func(g gomega.Gomega) {
						info, err := impersonatedVisibilityClient.LocalQueues(nsA.Name).GetPendingWorkloadsSummary(ctx, localQueueA.Name, metav1.GetOptions{})
						g.Expect(err).NotTo(gomega.HaveOccurred())
						g.Expect(info.Items).Should(gomega.BeComparableTo(wantPendingWorkloads, pendingWorkloadsCmpOpts...))
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("Verifying the user is Forbidden from a LocalQueue in a different Namespace", func() {
					_, err := impersonatedVisibilityClient.LocalQueues(nsB.Name).GetPendingWorkloadsSummary(ctx, localQueueB.Name, metav1.GetOptions{})
					gomega.Expect(err).Should(utiltesting.BeForbiddenError())
				})

				ginkgo.By("Verifying the user is Forbidden from the cluster-scoped ClusterQueue endpoint", func() {
					_, err := impersonatedVisibilityClient.ClusterQueues().GetPendingWorkloadsSummary(ctx, clusterQueue.Name, metav1.GetOptions{})
					gomega.Expect(err).Should(utiltesting.BeForbiddenError())
				})
			})
		})
	})

	ginkgo.When("A subject is bound to kueue-batch-admin-role", func() {
		var clusterRoleBinding *rbacv1.ClusterRoleBinding

		ginkgo.BeforeEach(func() {
			clusterRoleBinding = &rbacv1.ClusterRoleBinding{
				Name:    "read-pending-workloads-" + nsA.Name,
				RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "kueue-batch-admin-role"},
				Subjects: []rbacv1.Subject{
					{Name: "default", APIGroup: "", Namespace: kueueNS, Kind: rbacv1.ServiceAccountKind},
				},
			}
			behavioral.MustCreate(ctx, k8sClient, clusterRoleBinding)
			ginkgo.By("Wait for ResourceNotFound error instead of Forbidden to make sure the role bindings work", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					_, err := impersonatedVisibilityClient.ClusterQueues().GetPendingWorkloadsSummary(ctx, "non-existent", metav1.GetOptions{})
					g.Expect(err).Should(utiltesting.BeNotFoundError())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.AfterEach(func() {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterRoleBinding, true)
		})

		ginkgo.It("Should return an appropriate error", func() {
			ginkgo.By("Returning a ResourceNotFound error for a nonexistent ClusterQueue", func() {
				_, err := impersonatedVisibilityClient.ClusterQueues().GetPendingWorkloadsSummary(ctx, "non-existent", metav1.GetOptions{})
				gomega.Expect(err).Should(utiltesting.BeNotFoundError())
			})
			ginkgo.By("Returning a ResourceNotFound error for a nonexistent LocalQueue", func() {
				_, err := impersonatedVisibilityClient.LocalQueues(nsA.Name).GetPendingWorkloadsSummary(ctx, "non-existent", metav1.GetOptions{})
				gomega.Expect(err).Should(utiltesting.BeNotFoundError())
			})
		})
	})

	ginkgo.When("A subject is bound to kueue-batch-user-role, but not to kueue-batch-admin-role", func() {
		var roleBinding *rbacv1.RoleBinding

		ginkgo.BeforeEach(func() {
			roleBinding = mustCreateBatchUserRoleBinding(nsA.Name)
			ginkgo.By("Wait for ResourceNotFound error instead of Forbidden to make sure the role bindings work", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					_, err := impersonatedVisibilityClient.LocalQueues(nsA.Name).GetPendingWorkloadsSummary(ctx, "non-existent", metav1.GetOptions{})
					g.Expect(err).Should(utiltesting.BeNotFoundError())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.AfterEach(func() {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, roleBinding, true)
		})

		ginkgo.It("Should return an appropriate error", func() {
			ginkgo.By("Returning a Forbidden error due to insufficient permissions for the ClusterQueue request", func() {
				_, err := impersonatedVisibilityClient.ClusterQueues().GetPendingWorkloadsSummary(ctx, "non-existent", metav1.GetOptions{})
				gomega.Expect(err).Should(utiltesting.BeForbiddenError())
			})
			ginkgo.By("Returning a ResourceNotFound error for a nonexistent LocalQueue", func() {
				_, err := impersonatedVisibilityClient.LocalQueues(nsA.Name).GetPendingWorkloadsSummary(ctx, "non-existent", metav1.GetOptions{})
				gomega.Expect(err).Should(utiltesting.BeNotFoundError())
			})
			ginkgo.By("Returning a Forbidden error due to insufficient permissions for the LocalQueue request in different namespace", func() {
				_, err := impersonatedVisibilityClient.LocalQueues("default").GetPendingWorkloadsSummary(ctx, "non-existent", metav1.GetOptions{})
				gomega.Expect(err).Should(utiltesting.BeForbiddenError())
			})
		})
	})

	ginkgo.When("A subject is not bound to kueue-batch-user-role, nor to kueue-batch-admin-role", func() {
		ginkgo.It("Should return an appropriate error", func() {
			ginkgo.By("Returning a Forbidden error due to insufficient permissions for the ClusterQueue request", func() {
				_, err := impersonatedVisibilityClient.ClusterQueues().GetPendingWorkloadsSummary(ctx, "non-existent", metav1.GetOptions{})
				gomega.Expect(err).Should(utiltesting.BeForbiddenError())
			})
			ginkgo.By("Returning a Forbidden error due to insufficient permissions for the LocalQueue request", func() {
				_, err := impersonatedVisibilityClient.LocalQueues(nsA.Name).GetPendingWorkloadsSummary(ctx, "non-existent", metav1.GetOptions{})
				gomega.Expect(err).Should(utiltesting.BeForbiddenError())
			})
		})
	})
})

type pendingJobCase struct {
	JobName          string
	JobPrioClassName string
	LocalQueueName   string
	nsName           string
}

func createPendingJobs(jobCases []pendingJobCase) {
	for _, jobCase := range jobCases {
		job := testingjob.MakeJob(jobCase.JobName, jobCase.nsName).
			Queue(kueue.LocalQueueName(jobCase.LocalQueueName)).
			Image(behavioral.GetAgnHostImage(), behavioral.BehaviorExitFast).
			RequestAndLimit(corev1.ResourceCPU, "1").
			WorkloadPriorityClass(jobCase.JobPrioClassName).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, job)
	}
}

func mustCreateBatchUserRoleBinding(ns string) *rbacv1.RoleBinding {
	roleBinding := utiltesting.MakeRoleBinding("read-pending-workloads", ns).
		RoleRef(rbacv1.GroupName, "ClusterRole", "kueue-batch-user-role").
		Subject(rbacv1.ServiceAccountKind, "default", kueueNS).
		Obj()
	behavioral.MustCreate(ctx, k8sClient, roleBinding)
	return roleBinding
}
