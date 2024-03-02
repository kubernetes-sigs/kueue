/*
Copyright 2024 The Kubernetes Authors.

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
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	visibility "sigs.k8s.io/kueue/apis/visibility/v1alpha1"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Kueue visibility server", func() {
	const defaultFlavor = "default-flavor"

	// We do not check workload's Name, CreationTimestamp, and its OwnerReference's UID as they are generated at the server-side.
	var pendingWorkloadsCmpOpts = []cmp.Option{
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
		nsA = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "e2e-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, nsA)).To(gomega.Succeed())
		nsB = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "e2e-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, nsB)).To(gomega.Succeed())
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, nsA)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, nsB)).To(gomega.Succeed())
	})

	ginkgo.When("There are pending workloads due to capacity maxed by the admitted job", func() {
		ginkgo.BeforeEach(func() {
			defaultRF = testing.MakeResourceFlavor(defaultFlavor).Obj()
			gomega.Expect(k8sClient.Create(ctx, defaultRF)).Should(gomega.Succeed())

			clusterQueue = testing.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*testing.MakeFlavorQuotas(defaultFlavor).
						Resource(corev1.ResourceCPU, "1").
						Obj(),
				).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).Should(gomega.Succeed())

			localQueueA = testing.MakeLocalQueue("a", nsA.Name).ClusterQueue(clusterQueue.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueueA)).Should(gomega.Succeed())

			localQueueB = testing.MakeLocalQueue("b", nsA.Name).ClusterQueue(clusterQueue.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueueB)).Should(gomega.Succeed())

			highPriorityClass = testing.MakePriorityClass("high").PriorityValue(100).Obj()
			gomega.Expect(k8sClient.Create(ctx, highPriorityClass)).Should(gomega.Succeed())

			midPriorityClass = testing.MakePriorityClass("mid").PriorityValue(75).Obj()
			gomega.Expect(k8sClient.Create(ctx, midPriorityClass)).Should(gomega.Succeed())

			lowPriorityClass = testing.MakePriorityClass("low").PriorityValue(50).Obj()
			gomega.Expect(k8sClient.Create(ctx, lowPriorityClass)).Should(gomega.Succeed())

			ginkgo.By("Schedule a job that when admitted workload blocks the queue", func() {
				blockingJob = testingjob.MakeJob("test-job-1", nsA.Name).
					Queue(localQueueA.Name).
					Image("gcr.io/k8s-staging-perf-tests/sleep:v0.1.0", []string{"1m"}).
					Request(corev1.ResourceCPU, "1").
					TerminationGracePeriod(1).
					BackoffLimit(0).
					PriorityClass(highPriorityClass.Name).
					Obj()
				gomega.Expect(k8sClient.Create(ctx, blockingJob)).Should(gomega.Succeed())
			})
			ginkgo.By("Ensure the workload is admitted, by awaiting until the job is unsuspended", func() {
				expectJobUnsuspended(client.ObjectKeyFromObject(blockingJob))
			})
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(k8sClient.Delete(ctx, lowPriorityClass)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Delete(ctx, midPriorityClass)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Delete(ctx, highPriorityClass)).To(gomega.Succeed())
			gomega.Expect(util.DeleteLocalQueue(ctx, k8sClient, localQueueB)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteLocalQueue(ctx, k8sClient, localQueueA)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteAllJobsInNamespace(ctx, k8sClient, nsA)).Should(gomega.Succeed())
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, defaultRF, true)
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
					Image("gcr.io/k8s-staging-perf-tests/sleep:v0.1.0", []string{"1ms"}).
					Request(corev1.ResourceCPU, "1").
					PriorityClass(lowPriorityClass.Name).
					Obj()
				gomega.Expect(k8sClient.Create(ctx, sampleJob2)).Should(gomega.Succeed())
			})

			ginkgo.By("Verify there is one pending workload", func() {
				gomega.Eventually(func() []visibility.PendingWorkload {
					info, err := visibilityClient.ClusterQueues().GetPendingWorkloadsSummary(ctx, clusterQueue.Name, metav1.GetOptions{})
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					return info.Items
				}, util.Timeout, util.Interval).Should(gomega.HaveLen(1))
			})

			ginkgo.By("Await for pods to be running", func() {
				gomega.Eventually(func() int {
					createdJob := &batchv1.Job{}
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(blockingJob), createdJob)).Should(gomega.Succeed())
					return int(*createdJob.Status.Ready)
				}, util.Timeout, util.Interval).Should(gomega.Equal(1))
			})

			ginkgo.By("Terminate execution of the first workload to release the quota", func() {
				gomega.Expect(util.DeleteAllPodsInNamespace(ctx, k8sClient, nsA)).Should(gomega.Succeed())
			})

			ginkgo.By("Verify there are zero pending workloads, after the second workload is admitted", func() {
				gomega.Eventually(func() []visibility.PendingWorkload {
					info, err := visibilityClient.ClusterQueues().GetPendingWorkloadsSummary(ctx, clusterQueue.Name, metav1.GetOptions{})
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					return info.Items
				}, util.Timeout, util.Interval).Should(gomega.BeEmpty())
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
						Image("gcr.io/k8s-staging-perf-tests/sleep:v0.1.0", []string{"1ms"}).
						Request(corev1.ResourceCPU, "1").
						PriorityClass(jobCase.JobPrioClassName).
						Obj()
					gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
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
				gomega.Eventually(func() []visibility.PendingWorkload {
					info, err := visibilityClient.ClusterQueues().GetPendingWorkloadsSummary(ctx, clusterQueue.Name, metav1.GetOptions{})
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					return info.Items
				}, util.Timeout, util.Interval).Should(gomega.BeComparableTo(wantPendingWorkloads, pendingWorkloadsCmpOpts...))
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
					Image("gcr.io/k8s-staging-perf-tests/sleep:v0.1.0", []string{"1ms"}).
					Request(corev1.ResourceCPU, "1").
					PriorityClass(lowPriorityClass.Name).
					Obj()
				gomega.Expect(k8sClient.Create(ctx, sampleJob2)).Should(gomega.Succeed())
			})

			ginkgo.By("Verify there is one pending workload", func() {
				gomega.Eventually(func() []visibility.PendingWorkload {
					info, err := visibilityClient.LocalQueues(nsA.Name).GetPendingWorkloadsSummary(ctx, localQueueA.Name, metav1.GetOptions{})
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					return info.Items
				}, util.Timeout, util.Interval).Should(gomega.HaveLen(1))
			})

			ginkgo.By("Await for pods to be running", func() {
				gomega.Eventually(func() int {
					createdJob := &batchv1.Job{}
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(blockingJob), createdJob)).Should(gomega.Succeed())
					return int(*createdJob.Status.Ready)
				}, util.Timeout, util.Interval).Should(gomega.Equal(1))
			})

			ginkgo.By("Terminate execution of the first workload to release the quota", func() {
				gomega.Expect(util.DeleteAllPodsInNamespace(ctx, k8sClient, nsA)).Should(gomega.Succeed())
			})

			ginkgo.By("Verify there are zero pending workloads, after the second workload is admitted", func() {
				gomega.Eventually(func() int {
					info, err := visibilityClient.LocalQueues(nsA.Name).GetPendingWorkloadsSummary(ctx, localQueueA.Name, metav1.GetOptions{})
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					return len(info.Items)
				}, util.Timeout, util.Interval).Should(gomega.Equal(0))
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
						Image("gcr.io/k8s-staging-perf-tests/sleep:v0.1.0", []string{"1ms"}).
						Request(corev1.ResourceCPU, "1").
						PriorityClass(jobCase.JobPrioClassName).
						Obj()
					gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
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
				gomega.Eventually(func() []visibility.PendingWorkload {
					info, err := visibilityClient.LocalQueues(nsA.Name).GetPendingWorkloadsSummary(ctx, localQueueA.Name, metav1.GetOptions{})
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					return info.Items
				}, util.Timeout, util.Interval).Should(gomega.BeComparableTo(wantPendingWorkloads, pendingWorkloadsCmpOpts...))
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
				gomega.Eventually(func() []visibility.PendingWorkload {
					info, err := visibilityClient.LocalQueues(nsA.Name).GetPendingWorkloadsSummary(ctx, localQueueB.Name, metav1.GetOptions{})
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					return info.Items
				}, util.Timeout, util.Interval).Should(gomega.BeComparableTo(wantPendingWorkloads, pendingWorkloadsCmpOpts...))
			})
		})
		ginkgo.It("Should allow fetching information about position of pending workloads from different LocalQueues from different Namespaces", func() {

			ginkgo.By("Create a LocalQueue in a different Namespace", func() {
				localQueueB = testing.MakeLocalQueue("b", nsB.Name).ClusterQueue(clusterQueue.Name).Obj()
				gomega.Expect(k8sClient.Create(ctx, localQueueB)).Should(gomega.Succeed())
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
						Image("gcr.io/k8s-staging-perf-tests/sleep:v0.1.0", []string{"1ms"}).
						Request(corev1.ResourceCPU, "1").
						PriorityClass(jobCase.JobPrioClassName).
						Obj()
					gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
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
				gomega.Eventually(func() []visibility.PendingWorkload {
					info, err := visibilityClient.LocalQueues(nsA.Name).GetPendingWorkloadsSummary(ctx, localQueueA.Name, metav1.GetOptions{})
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					return info.Items
				}, util.Timeout, util.Interval).Should(gomega.BeComparableTo(wantPendingWorkloads, pendingWorkloadsCmpOpts...))
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
				gomega.Eventually(func() []visibility.PendingWorkload {
					info, err := visibilityClient.LocalQueues(nsA.Name).GetPendingWorkloadsSummary(ctx, localQueueB.Name, metav1.GetOptions{})
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					return info.Items
				}, util.Timeout, util.Interval).Should(gomega.BeComparableTo(wantPendingWorkloads, pendingWorkloadsCmpOpts...))
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
			gomega.Expect(k8sClient.Create(ctx, clusterRoleBinding)).Should(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(k8sClient.Delete(ctx, clusterRoleBinding)).To(gomega.Succeed())
		})

		ginkgo.It("Should return an appropriate error", func() {
			ginkgo.By("Returning a ResourceNotFound error for a nonexistent ClusterQueue", func() {
				_, err := impersonatedVisibilityClient.ClusterQueues().GetPendingWorkloadsSummary(ctx, "non-existent", metav1.GetOptions{})
				statusErr, ok := err.(*errors.StatusError)
				gomega.Expect(ok).To(gomega.BeTrue())
				gomega.Expect(statusErr.ErrStatus.Reason).To(gomega.Equal(metav1.StatusReasonNotFound))
			})
			ginkgo.By("Returning a ResourceNotFound error for a nonexistent LocalQueue", func() {
				_, err := impersonatedVisibilityClient.LocalQueues(nsA.Name).GetPendingWorkloadsSummary(ctx, "non-existent", metav1.GetOptions{})
				statusErr, ok := err.(*errors.StatusError)
				gomega.Expect(ok).To(gomega.BeTrue())
				gomega.Expect(statusErr.ErrStatus.Reason).To(gomega.Equal(metav1.StatusReasonNotFound))
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
			gomega.Expect(k8sClient.Create(ctx, roleBinding)).Should(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(k8sClient.Delete(ctx, roleBinding)).To(gomega.Succeed())
		})

		ginkgo.It("Should return an appropriate error", func() {
			ginkgo.By("Returning a Forbidden error due to insufficient permissions for the ClusterQueue request", func() {
				_, err := impersonatedVisibilityClient.ClusterQueues().GetPendingWorkloadsSummary(ctx, "non-existent", metav1.GetOptions{})
				statusErr, ok := err.(*errors.StatusError)
				gomega.Expect(ok).To(gomega.BeTrue())
				gomega.Expect(statusErr.ErrStatus.Reason).To(gomega.Equal(metav1.StatusReasonForbidden))
			})
			ginkgo.By("Returning a ResourceNotFound error for a nonexistent LocalQueue", func() {
				_, err := impersonatedVisibilityClient.LocalQueues(nsA.Name).GetPendingWorkloadsSummary(ctx, "non-existent", metav1.GetOptions{})
				statusErr, ok := err.(*errors.StatusError)
				gomega.Expect(ok).To(gomega.BeTrue())
				gomega.Expect(statusErr.ErrStatus.Reason).To(gomega.Equal(metav1.StatusReasonNotFound))
			})
			ginkgo.By("Returning a Forbidden error due to insufficient permissions for the LocalQueue request in different namespace", func() {
				_, err := impersonatedVisibilityClient.LocalQueues("default").GetPendingWorkloadsSummary(ctx, "non-existent", metav1.GetOptions{})
				statusErr, ok := err.(*errors.StatusError)
				gomega.Expect(ok).To(gomega.BeTrue())
				gomega.Expect(statusErr.ErrStatus.Reason).To(gomega.Equal(metav1.StatusReasonForbidden))
			})
		})
	})

	ginkgo.When("A subject is not bound to kueue-batch-user-role, nor to kueue-batch-admin-role", func() {
		ginkgo.It("Should return an appropriate error", func() {
			ginkgo.By("Returning a Forbidden error due to insufficient permissions for the ClusterQueue request", func() {
				_, err := impersonatedVisibilityClient.ClusterQueues().GetPendingWorkloadsSummary(ctx, "non-existent", metav1.GetOptions{})
				statusErr, ok := err.(*errors.StatusError)
				gomega.Expect(ok).To(gomega.BeTrue())
				gomega.Expect(statusErr.ErrStatus.Reason).To(gomega.Equal(metav1.StatusReasonForbidden))
			})
			ginkgo.By("Returning a Forbidden error due to insufficient permissions for the LocalQueue request", func() {
				_, err := impersonatedVisibilityClient.LocalQueues(nsA.Name).GetPendingWorkloadsSummary(ctx, "non-existent", metav1.GetOptions{})
				statusErr, ok := err.(*errors.StatusError)
				gomega.Expect(ok).To(gomega.BeTrue())
				gomega.Expect(statusErr.ErrStatus.Reason).To(gomega.Equal(metav1.StatusReasonForbidden))
			})
		})
	})
})
