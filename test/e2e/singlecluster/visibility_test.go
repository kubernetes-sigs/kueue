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
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	visibility "sigs.k8s.io/kueue/apis/visibility/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Kueue visibility server", func() {
	const defaultFlavor = "default-flavor"

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
		highPriorityClass *schedulingv1.PriorityClass
		midPriorityClass  *schedulingv1.PriorityClass
		lowPriorityClass  *schedulingv1.PriorityClass
	)

	ginkgo.BeforeEach(func() {
		nsA = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-")
		nsB = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-")
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, nsA)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, nsB)).To(gomega.Succeed())
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, nsA)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, nsB)
	})

	ginkgo.When("There are pending workloads due to capacity maxed by the admitted job", func() {
		ginkgo.BeforeEach(func() {
			defaultRF = testing.MakeResourceFlavor(defaultFlavor).Obj()
			util.MustCreate(ctx, k8sClient, defaultRF)

			clusterQueue = testing.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*testing.MakeFlavorQuotas(defaultFlavor).
						Resource(corev1.ResourceCPU, "1").
						Obj(),
				).
				Obj()
			util.MustCreate(ctx, k8sClient, clusterQueue)

			localQueueA = testing.MakeLocalQueue("a", nsA.Name).ClusterQueue(clusterQueue.Name).Obj()
			util.MustCreate(ctx, k8sClient, localQueueA)

			localQueueB = testing.MakeLocalQueue("b", nsA.Name).ClusterQueue(clusterQueue.Name).Obj()
			util.MustCreate(ctx, k8sClient, localQueueB)

			highPriorityClass = testing.MakePriorityClass("high").PriorityValue(100).Obj()
			util.MustCreate(ctx, k8sClient, highPriorityClass)

			midPriorityClass = testing.MakePriorityClass("mid").PriorityValue(75).Obj()
			util.MustCreate(ctx, k8sClient, midPriorityClass)

			lowPriorityClass = testing.MakePriorityClass("low").PriorityValue(50).Obj()
			util.MustCreate(ctx, k8sClient, lowPriorityClass)

			ginkgo.By("Schedule a job that when admitted workload blocks the queue", func() {
				blockingJob = testingjob.MakeJob("test-job-1", nsA.Name).
					Queue(localQueueA.Name).
					Image(util.E2eTestAgnHostImage, util.BehaviorWaitForDeletion).
					RequestAndLimit(corev1.ResourceCPU, "1").
					TerminationGracePeriod(1).
					BackoffLimit(0).
					PriorityClass(highPriorityClass.Name).
					Obj()
				util.MustCreate(ctx, k8sClient, blockingJob)
			})
			ginkgo.By("Ensure the workload is admitted, by awaiting until the job is unsuspended", func() {
				expectJobUnsuspended(client.ObjectKeyFromObject(blockingJob))
			})
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteAllJobsInNamespace(ctx, k8sClient, nsA)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteAllJobsInNamespace(ctx, k8sClient, nsB)).Should(gomega.Succeed())

			// Force remove workloads to be sure that cluster queue can be removed.
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, nsA)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, nsB)).Should(gomega.Succeed())

			util.ExpectObjectToBeDeleted(ctx, k8sClient, lowPriorityClass, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, midPriorityClass, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, highPriorityClass, true)

			util.ExpectObjectToBeDeleted(ctx, k8sClient, localQueueA, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, localQueueB, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, defaultRF, true)
		})

		ginkgo.It("Should allow fetching information about pending workloads in ClusterQueue", func() {
			ginkgo.By("Verify there are zero pending workloads", func() {
				info, err := visibilityClient.ClusterQueues().GetPendingWorkloadsSummary(ctx, clusterQueue.Name, metav1.GetOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(info.Items).Should(gomega.BeEmpty())
			})

			ginkgo.By("Schedule a job which is pending due to lower priority", func() {
				sampleJob2 = testingjob.MakeJob("test-job-2", nsA.Name).
					Queue(localQueueA.Name).
					Image(util.E2eTestAgnHostImage, util.BehaviorExitFast).
					RequestAndLimit(corev1.ResourceCPU, "1").
					PriorityClass(lowPriorityClass.Name).
					Obj()
				util.MustCreate(ctx, k8sClient, sampleJob2)
			})

			ginkgo.By("Verify there is one pending workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					info, err := visibilityClient.ClusterQueues().GetPendingWorkloadsSummary(ctx, clusterQueue.Name, metav1.GetOptions{})
					g.Expect(err).NotTo(gomega.HaveOccurred())
					g.Expect(info.Items).Should(gomega.HaveLen(1))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Await for pods to be running", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdJob := &batchv1.Job{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(blockingJob), createdJob)).Should(gomega.Succeed())
					g.Expect(createdJob.Status.Ready).Should(gomega.Equal(ptr.To[int32](1)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Terminate execution of the first workload to release the quota", func() {
				gomega.Expect(util.DeleteAllPodsInNamespace(ctx, k8sClient, nsA)).Should(gomega.Succeed())
			})

			ginkgo.By("Verify there are zero pending workloads, after the second workload is admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					info, err := visibilityClient.ClusterQueues().GetPendingWorkloadsSummary(ctx, clusterQueue.Name, metav1.GetOptions{})
					g.Expect(err).NotTo(gomega.HaveOccurred())
					g.Expect(info.Items).Should(gomega.BeEmpty())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should allow fetching information about position of pending workloads in ClusterQueue", func() {
			ginkgo.By("Schedule three different jobs with different priorities and two different LocalQueues", func() {
				jobCases := []struct {
					JobName          string
					JobPrioClassName string
					LocalQueueName   string
				}{
					{
						JobName:          "lq-a-high-prio",
						JobPrioClassName: highPriorityClass.Name,
						LocalQueueName:   localQueueA.Name,
					},
					{
						JobName:          "lq-b-mid-prio",
						JobPrioClassName: midPriorityClass.Name,
						LocalQueueName:   localQueueB.Name,
					},
					{
						JobName:          "lq-b-low-prio",
						JobPrioClassName: lowPriorityClass.Name,
						LocalQueueName:   localQueueB.Name,
					},
				}
				for _, jobCase := range jobCases {
					job := testingjob.MakeJob(jobCase.JobName, nsA.Name).
						Queue(jobCase.LocalQueueName).
						Image(util.E2eTestAgnHostImage, util.BehaviorExitFast).
						RequestAndLimit(corev1.ResourceCPU, "1").
						PriorityClass(jobCase.JobPrioClassName).
						Obj()
					util.MustCreate(ctx, k8sClient, job)
				}
			})

			ginkgo.By("Verify their positions and priorities", func() {
				wantPendingWorkloads := []visibility.PendingWorkload{
					{
						ObjectMeta: metav1.ObjectMeta{
							Namespace:       nsA.Name,
							OwnerReferences: defaultOwnerReferenceForJob("lq-a-high-prio"),
						},
						Priority:               highPriorityClass.Value,
						PositionInLocalQueue:   0,
						PositionInClusterQueue: 0,
						LocalQueueName:         localQueueA.Name,
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Namespace:       nsA.Name,
							OwnerReferences: defaultOwnerReferenceForJob("lq-b-mid-prio"),
						},
						Priority:               midPriorityClass.Value,
						PositionInLocalQueue:   0,
						PositionInClusterQueue: 1,
						LocalQueueName:         localQueueB.Name,
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Namespace:       nsA.Name,
							OwnerReferences: defaultOwnerReferenceForJob("lq-b-low-prio"),
						},
						Priority:               lowPriorityClass.Value,
						PositionInLocalQueue:   1,
						PositionInClusterQueue: 2,
						LocalQueueName:         localQueueB.Name,
					},
				}
				gomega.Eventually(func(g gomega.Gomega) {
					info, err := visibilityClient.ClusterQueues().GetPendingWorkloadsSummary(ctx, clusterQueue.Name, metav1.GetOptions{})
					g.Expect(err).NotTo(gomega.HaveOccurred())
					g.Expect(info.Items).Should(gomega.BeComparableTo(wantPendingWorkloads, pendingWorkloadsCmpOpts...))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should allow fetching information about pending workloads in LocalQueue", func() {
			ginkgo.By("Verify there are zero pending workloads", func() {
				info, err := visibilityClient.LocalQueues(nsA.Name).GetPendingWorkloadsSummary(ctx, localQueueA.Name, metav1.GetOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(info.Items).Should(gomega.BeEmpty())
			})

			ginkgo.By("Schedule a job which is pending due to lower priority", func() {
				sampleJob2 = testingjob.MakeJob("test-job-2", nsA.Name).
					Queue(localQueueA.Name).
					Image(util.E2eTestAgnHostImage, util.BehaviorExitFast).
					RequestAndLimit(corev1.ResourceCPU, "1").
					PriorityClass(lowPriorityClass.Name).
					Obj()
				util.MustCreate(ctx, k8sClient, sampleJob2)
			})

			ginkgo.By("Verify there is one pending workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					info, err := visibilityClient.LocalQueues(nsA.Name).GetPendingWorkloadsSummary(ctx, localQueueA.Name, metav1.GetOptions{})
					g.Expect(err).NotTo(gomega.HaveOccurred())
					g.Expect(info.Items).Should(gomega.HaveLen(1))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Await for pods to be running", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdJob := &batchv1.Job{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(blockingJob), createdJob)).Should(gomega.Succeed())
					g.Expect(createdJob.Status.Ready).Should(gomega.Equal(ptr.To[int32](1)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Terminate execution of the first workload to release the quota", func() {
				gomega.Expect(util.DeleteAllPodsInNamespace(ctx, k8sClient, nsA)).Should(gomega.Succeed())
			})

			ginkgo.By("Verify there are zero pending workloads, after the second workload is admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					info, err := visibilityClient.LocalQueues(nsA.Name).GetPendingWorkloadsSummary(ctx, localQueueA.Name, metav1.GetOptions{})
					g.Expect(err).NotTo(gomega.HaveOccurred())
					g.Expect(info.Items).Should(gomega.BeEmpty())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should allow fetching information about position of pending workloads from different LocalQueues", func() {
			ginkgo.By("Schedule three different jobs with different priorities and two different LocalQueues", func() {
				jobCases := []struct {
					JobName          string
					JobPrioClassName string
					LocalQueueName   string
				}{
					{
						JobName:          "lq-a-high-prio",
						JobPrioClassName: highPriorityClass.Name,
						LocalQueueName:   localQueueA.Name,
					},
					{
						JobName:          "lq-b-mid-prio",
						JobPrioClassName: midPriorityClass.Name,
						LocalQueueName:   localQueueB.Name,
					},
					{
						JobName:          "lq-b-low-prio",
						JobPrioClassName: lowPriorityClass.Name,
						LocalQueueName:   localQueueB.Name,
					},
				}
				for _, jobCase := range jobCases {
					job := testingjob.MakeJob(jobCase.JobName, nsA.Name).
						Queue(jobCase.LocalQueueName).
						Image(util.E2eTestAgnHostImage, util.BehaviorExitFast).
						RequestAndLimit(corev1.ResourceCPU, "1").
						PriorityClass(jobCase.JobPrioClassName).
						Obj()
					util.MustCreate(ctx, k8sClient, job)
				}
			})

			ginkgo.By("Verify their positions and priorities in LocalQueueA", func() {
				wantPendingWorkloads := []visibility.PendingWorkload{
					{
						ObjectMeta: metav1.ObjectMeta{
							Namespace:       nsA.Name,
							OwnerReferences: defaultOwnerReferenceForJob("lq-a-high-prio"),
						},
						Priority:               highPriorityClass.Value,
						PositionInLocalQueue:   0,
						PositionInClusterQueue: 0,
						LocalQueueName:         localQueueA.Name,
					},
				}
				gomega.Eventually(func(g gomega.Gomega) {
					info, err := visibilityClient.LocalQueues(nsA.Name).GetPendingWorkloadsSummary(ctx, localQueueA.Name, metav1.GetOptions{})
					g.Expect(err).NotTo(gomega.HaveOccurred())
					g.Expect(info.Items).Should(gomega.BeComparableTo(wantPendingWorkloads, pendingWorkloadsCmpOpts...))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Verify their positions and priorities in LocalQueueB", func() {
				wantPendingWorkloads := []visibility.PendingWorkload{
					{
						ObjectMeta: metav1.ObjectMeta{
							Namespace:       nsA.Name,
							OwnerReferences: defaultOwnerReferenceForJob("lq-b-mid-prio"),
						},
						Priority:               midPriorityClass.Value,
						PositionInLocalQueue:   0,
						PositionInClusterQueue: 1,
						LocalQueueName:         localQueueB.Name,
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Namespace:       nsA.Name,
							OwnerReferences: defaultOwnerReferenceForJob("lq-b-low-prio"),
						},
						Priority:               lowPriorityClass.Value,
						PositionInLocalQueue:   1,
						PositionInClusterQueue: 2,
						LocalQueueName:         localQueueB.Name,
					},
				}
				gomega.Eventually(func(g gomega.Gomega) {
					info, err := visibilityClient.LocalQueues(nsA.Name).GetPendingWorkloadsSummary(ctx, localQueueB.Name, metav1.GetOptions{})
					g.Expect(err).NotTo(gomega.HaveOccurred())
					g.Expect(info.Items).Should(gomega.BeComparableTo(wantPendingWorkloads, pendingWorkloadsCmpOpts...))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
		ginkgo.It("Should allow fetching information about position of pending workloads from different LocalQueues from different Namespaces", func() {
			ginkgo.By("Create a LocalQueue in a different Namespace", func() {
				localQueueB = testing.MakeLocalQueue("b", nsB.Name).ClusterQueue(clusterQueue.Name).Obj()
				util.MustCreate(ctx, k8sClient, localQueueB)
			})

			ginkgo.By("Schedule three different jobs with different priorities and different LocalQueues in different Namespaces", func() {
				jobCases := []struct {
					JobName          string
					JobPrioClassName string
					LocalQueueName   string
					nsName           string
				}{
					{
						JobName:          "lq-a-high-prio",
						JobPrioClassName: highPriorityClass.Name,
						LocalQueueName:   localQueueA.Name,
						nsName:           nsA.Name,
					},
					{
						JobName:          "lq-b-mid-prio",
						JobPrioClassName: midPriorityClass.Name,
						LocalQueueName:   localQueueB.Name,
						nsName:           nsB.Name,
					},
					{
						JobName:          "lq-b-low-prio",
						JobPrioClassName: lowPriorityClass.Name,
						LocalQueueName:   localQueueB.Name,
						nsName:           nsB.Name,
					},
				}
				for _, jobCase := range jobCases {
					job := testingjob.MakeJob(jobCase.JobName, jobCase.nsName).
						Queue(jobCase.LocalQueueName).
						Image(util.E2eTestAgnHostImage, util.BehaviorExitFast).
						RequestAndLimit(corev1.ResourceCPU, "1").
						PriorityClass(jobCase.JobPrioClassName).
						Obj()
					util.MustCreate(ctx, k8sClient, job)
				}
			})

			ginkgo.By("Verify their positions and priorities in LocalQueueA", func() {
				wantPendingWorkloads := []visibility.PendingWorkload{
					{
						ObjectMeta: metav1.ObjectMeta{
							Namespace:       nsA.Name,
							OwnerReferences: defaultOwnerReferenceForJob("lq-a-high-prio"),
						},
						Priority:               highPriorityClass.Value,
						PositionInLocalQueue:   0,
						PositionInClusterQueue: 0,
						LocalQueueName:         localQueueA.Name,
					},
				}
				gomega.Eventually(func(g gomega.Gomega) {
					info, err := visibilityClient.LocalQueues(nsA.Name).GetPendingWorkloadsSummary(ctx, localQueueA.Name, metav1.GetOptions{})
					g.Expect(err).NotTo(gomega.HaveOccurred())
					g.Expect(info.Items).Should(gomega.BeComparableTo(wantPendingWorkloads, pendingWorkloadsCmpOpts...))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Verify their positions and priorities in LocalQueueB", func() {
				wantPendingWorkloads := []visibility.PendingWorkload{
					{
						ObjectMeta: metav1.ObjectMeta{
							Namespace:       nsB.Name,
							OwnerReferences: defaultOwnerReferenceForJob("lq-b-mid-prio"),
						},
						Priority:               midPriorityClass.Value,
						PositionInLocalQueue:   0,
						PositionInClusterQueue: 1,
						LocalQueueName:         localQueueB.Name,
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Namespace:       nsB.Name,
							OwnerReferences: defaultOwnerReferenceForJob("lq-b-low-prio"),
						},
						Priority:               lowPriorityClass.Value,
						PositionInLocalQueue:   1,
						PositionInClusterQueue: 2,
						LocalQueueName:         localQueueB.Name,
					},
				}
				gomega.Eventually(func(g gomega.Gomega) {
					info, err := visibilityClient.LocalQueues(nsA.Name).GetPendingWorkloadsSummary(ctx, localQueueB.Name, metav1.GetOptions{})
					g.Expect(err).NotTo(gomega.HaveOccurred())
					g.Expect(info.Items).Should(gomega.BeComparableTo(wantPendingWorkloads, pendingWorkloadsCmpOpts...))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("A subject is bound to kueue-batch-admin-role", func() {
		var clusterRoleBinding *rbacv1.ClusterRoleBinding

		ginkgo.BeforeEach(func() {
			clusterRoleBinding = &rbacv1.ClusterRoleBinding{
				ObjectMeta: metav1.ObjectMeta{Name: "read-pending-workloads"},
				RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "kueue-batch-admin-role"},
				Subjects: []rbacv1.Subject{
					{Name: "default", APIGroup: "", Namespace: "kueue-system", Kind: rbacv1.ServiceAccountKind},
				},
			}
			util.MustCreate(ctx, k8sClient, clusterRoleBinding)
			ginkgo.By("Wait for ResourceNotFound error instead of Forbidden to make sure the role bindings work", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					_, err := impersonatedVisibilityClient.ClusterQueues().GetPendingWorkloadsSummary(ctx, "non-existent", metav1.GetOptions{})
					g.Expect(err).Should(testing.BeNotFoundError())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(k8sClient.Delete(ctx, clusterRoleBinding)).To(gomega.Succeed())
		})

		ginkgo.It("Should return an appropriate error", func() {
			ginkgo.By("Returning a ResourceNotFound error for a nonexistent ClusterQueue", func() {
				_, err := impersonatedVisibilityClient.ClusterQueues().GetPendingWorkloadsSummary(ctx, "non-existent", metav1.GetOptions{})
				gomega.Expect(err).Should(testing.BeNotFoundError())
			})
			ginkgo.By("Returning a ResourceNotFound error for a nonexistent LocalQueue", func() {
				_, err := impersonatedVisibilityClient.LocalQueues(nsA.Name).GetPendingWorkloadsSummary(ctx, "non-existent", metav1.GetOptions{})
				gomega.Expect(err).Should(testing.BeNotFoundError())
			})
		})
	})

	ginkgo.When("A subject is bound to kueue-batch-user-role, but not to kueue-batch-admin-role", func() {
		var roleBinding *rbacv1.RoleBinding

		ginkgo.BeforeEach(func() {
			roleBinding = &rbacv1.RoleBinding{
				ObjectMeta: metav1.ObjectMeta{Name: "read-pending-workloads", Namespace: nsA.Name},
				RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "kueue-batch-user-role"},
				Subjects: []rbacv1.Subject{
					{Name: "default", APIGroup: "", Namespace: "kueue-system", Kind: rbacv1.ServiceAccountKind},
				},
			}
			util.MustCreate(ctx, k8sClient, roleBinding)
			ginkgo.By("Wait for ResourceNotFound error instead of Forbidden to make sure the role bindings work", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					_, err := impersonatedVisibilityClient.LocalQueues(nsA.Name).GetPendingWorkloadsSummary(ctx, "non-existent", metav1.GetOptions{})
					g.Expect(err).Should(testing.BeNotFoundError())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(k8sClient.Delete(ctx, roleBinding)).To(gomega.Succeed())
		})

		ginkgo.It("Should return an appropriate error", func() {
			ginkgo.By("Returning a Forbidden error due to insufficient permissions for the ClusterQueue request", func() {
				_, err := impersonatedVisibilityClient.ClusterQueues().GetPendingWorkloadsSummary(ctx, "non-existent", metav1.GetOptions{})
				gomega.Expect(err).Should(testing.BeForbiddenError())
			})
			ginkgo.By("Returning a ResourceNotFound error for a nonexistent LocalQueue", func() {
				_, err := impersonatedVisibilityClient.LocalQueues(nsA.Name).GetPendingWorkloadsSummary(ctx, "non-existent", metav1.GetOptions{})
				gomega.Expect(err).Should(testing.BeNotFoundError())
			})
			ginkgo.By("Returning a Forbidden error due to insufficient permissions for the LocalQueue request in different namespace", func() {
				_, err := impersonatedVisibilityClient.LocalQueues("default").GetPendingWorkloadsSummary(ctx, "non-existent", metav1.GetOptions{})
				gomega.Expect(err).Should(testing.BeForbiddenError())
			})
		})
	})

	ginkgo.When("A subject is not bound to kueue-batch-user-role, nor to kueue-batch-admin-role", func() {
		ginkgo.It("Should return an appropriate error", func() {
			ginkgo.By("Returning a Forbidden error due to insufficient permissions for the ClusterQueue request", func() {
				_, err := impersonatedVisibilityClient.ClusterQueues().GetPendingWorkloadsSummary(ctx, "non-existent", metav1.GetOptions{})
				gomega.Expect(err).Should(testing.BeForbiddenError())
			})
			ginkgo.By("Returning a Forbidden error due to insufficient permissions for the LocalQueue request", func() {
				_, err := impersonatedVisibilityClient.LocalQueues(nsA.Name).GetPendingWorkloadsSummary(ctx, "non-existent", metav1.GetOptions{})
				gomega.Expect(err).Should(testing.BeForbiddenError())
			})
		})
	})
})
