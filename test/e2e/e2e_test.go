/*
Copyright 2022 The Kubernetes Authors.

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
	schedulingv1 "k8s.io/api/scheduling/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	visibility "sigs.k8s.io/kueue/apis/visibility/v1alpha1"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/util/slices"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

// +kubebuilder:docs-gen:collapse=Imports

var _ = ginkgo.Describe("Kueue visibility server", func() {
	const defaultFlavor = "default-flavor"

	// We do not check workload's Name and its OwnerReference's UID as they are generated at the server-side.
	var pendingWorkloadsCmpOpts = []cmp.Option{
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "Name"),
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
			gomega.Expect(k8sClient.Create(ctx, highPriorityClass))

			midPriorityClass = testing.MakePriorityClass("mid").PriorityValue(75).Obj()
			gomega.Expect(k8sClient.Create(ctx, midPriorityClass))

			lowPriorityClass = testing.MakePriorityClass("low").PriorityValue(50).Obj()
			gomega.Expect(k8sClient.Create(ctx, lowPriorityClass))

			ginkgo.By("Schedule a job that when admitted workload blocks the queue", func() {
				blockingJob = testingjob.MakeJob("test-job-1", nsA.Name).
					Queue(localQueueA.Name).
					Image("gcr.io/k8s-staging-perf-tests/sleep:v0.0.3", []string{"60s", "-termination-grace-period", "0s"}).
					Request(corev1.ResourceCPU, "1").
					TerimnationGracePeriod(1).
					BackoffLimit(0).
					PriorityClass(highPriorityClass.Name).
					Obj()
				gomega.Expect(k8sClient.Create(ctx, blockingJob)).Should(gomega.Succeed())
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
				info, err := visibilityClient.LocalQueues(nsA.Name).GetPendingWorkloadsSummary(ctx, localQueueA.Name, metav1.GetOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(len(info.Items)).Should(gomega.Equal(0))
			})

			ginkgo.By("Schedule a job which is pending due to lower priority", func() {
				sampleJob2 = testingjob.MakeJob("test-job-2", nsA.Name).
					Queue(localQueueA.Name).
					Image("gcr.io/k8s-staging-perf-tests/sleep:v0.0.3", []string{"1ms"}).
					Request(corev1.ResourceCPU, "1").
					PriorityClass(lowPriorityClass.Name).
					Obj()
				gomega.Expect(k8sClient.Create(ctx, sampleJob2)).Should(gomega.Succeed())
			})

			ginkgo.By("Verify there is one pending workload", func() {
				gomega.Eventually(func() int {
					info, err := visibilityClient.ClusterQueues().GetPendingWorkloadsSummary(ctx, clusterQueue.Name, metav1.GetOptions{})
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					return len(info.Items)
				}, util.Timeout, util.Interval).Should(gomega.Equal(1))
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
					info, err := visibilityClient.ClusterQueues().GetPendingWorkloadsSummary(ctx, clusterQueue.Name, metav1.GetOptions{})
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					return len(info.Items)
				}, util.Timeout, util.Interval).Should(gomega.Equal(0))
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
						Image("gcr.io/k8s-staging-perf-tests/sleep:v0.0.3", []string{"1ms"}).
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
				gomega.Expect(len(info.Items)).Should(gomega.Equal(0))
			})

			ginkgo.By("Schedule a job which is pending due to lower priority", func() {
				sampleJob2 = testingjob.MakeJob("test-job-2", nsA.Name).
					Queue(localQueueA.Name).
					Image("gcr.io/k8s-staging-perf-tests/sleep:v0.0.3", []string{"1ms"}).
					Request(corev1.ResourceCPU, "1").
					PriorityClass(lowPriorityClass.Name).
					Obj()
				gomega.Expect(k8sClient.Create(ctx, sampleJob2)).Should(gomega.Succeed())
			})

			ginkgo.By("Verify there is one pending workload", func() {
				gomega.Eventually(func() int {
					info, err := visibilityClient.LocalQueues(nsA.Name).GetPendingWorkloadsSummary(ctx, localQueueA.Name, metav1.GetOptions{})
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					return len(info.Items)
				}, util.Timeout, util.Interval).Should(gomega.Equal(1))
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
						Image("gcr.io/k8s-staging-perf-tests/sleep:v0.0.3", []string{"1ms"}).
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
						Image("gcr.io/k8s-staging-perf-tests/sleep:v0.0.3", []string{"1ms"}).
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
})

var _ = ginkgo.Describe("Kueue", func() {
	var ns *corev1.Namespace
	var sampleJob *batchv1.Job
	var jobKey types.NamespacedName

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "e2e-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
		sampleJob = testingjob.MakeJob("test-job", ns.Name).
			Queue("main").
			Request("cpu", "1").
			Request("memory", "20Mi").
			Obj()
		jobKey = client.ObjectKeyFromObject(sampleJob)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.When("Creating a Job without a matching LocalQueue", func() {
		ginkgo.It("Should stay in suspended", func() {
			gomega.Expect(k8sClient.Create(ctx, sampleJob)).Should(gomega.Succeed())

			createdJob := &batchv1.Job{}
			gomega.Eventually(func() bool {
				if err := k8sClient.Get(ctx, jobKey, createdJob); err != nil {
					return false
				}
				return *createdJob.Spec.Suspend
			}, util.Timeout, util.Interval).Should(gomega.BeTrue())
			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(jobKey.Name), Namespace: ns.Name}
			createdWorkload := &kueue.Workload{}
			gomega.Eventually(func() bool {
				if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
					return false
				}
				return workload.HasQuotaReservation(createdWorkload)

			}, util.Timeout, util.Interval).Should(gomega.BeFalse())
			gomega.Expect(k8sClient.Delete(ctx, sampleJob)).Should(gomega.Succeed())
		})
	})

	ginkgo.When("Creating a Job With Queueing", func() {
		var (
			onDemandRF   *kueue.ResourceFlavor
			spotRF       *kueue.ResourceFlavor
			localQueue   *kueue.LocalQueue
			clusterQueue *kueue.ClusterQueue
		)
		ginkgo.BeforeEach(func() {
			onDemandRF = testing.MakeResourceFlavor("on-demand").
				Label("instance-type", "on-demand").Obj()
			gomega.Expect(k8sClient.Create(ctx, onDemandRF)).Should(gomega.Succeed())
			spotRF = testing.MakeResourceFlavor("spot").
				Label("instance-type", "spot").Obj()
			gomega.Expect(k8sClient.Create(ctx, spotRF)).Should(gomega.Succeed())
			clusterQueue = testing.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*testing.MakeFlavorQuotas("on-demand").
						Resource(corev1.ResourceCPU, "1").
						Resource(corev1.ResourceMemory, "1Gi").
						Obj(),
					*testing.MakeFlavorQuotas("spot").
						Resource(corev1.ResourceCPU, "1").
						Resource(corev1.ResourceMemory, "1Gi").
						Obj(),
				).
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
				}).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).Should(gomega.Succeed())
			localQueue = testing.MakeLocalQueue("main", ns.Name).ClusterQueue("cluster-queue").Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).Should(gomega.Succeed())
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteLocalQueue(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, onDemandRF, true)
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, spotRF, true)
		})

		ginkgo.It("Should unsuspend a job and set nodeSelectors", func() {
			// Use a binary that ends.
			sampleJob = (&testingjob.JobWrapper{Job: *sampleJob}).Image("gcr.io/k8s-staging-perf-tests/sleep:v0.0.3", []string{"5s"}).Obj()
			gomega.Expect(k8sClient.Create(ctx, sampleJob)).Should(gomega.Succeed())

			createdWorkload := &kueue.Workload{}
			expectJobUnsuspendedWithNodeSelectors(jobKey, map[string]string{
				"instance-type": "on-demand",
			})
			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(jobKey.Name), Namespace: ns.Name}
			gomega.Eventually(func() bool {
				if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
					return false
				}
				return workload.HasQuotaReservation(createdWorkload) &&
					apimeta.IsStatusConditionTrue(createdWorkload.Status.Conditions, kueue.WorkloadFinished)

			}, util.LongTimeout, util.Interval).Should(gomega.BeTrue())
		})

		ginkgo.It("Should readmit preempted job with priorityClass into a separate flavor", func() {
			gomega.Expect(k8sClient.Create(ctx, sampleJob)).Should(gomega.Succeed())

			highPriorityClass := testing.MakePriorityClass("high").PriorityValue(100).Obj()
			gomega.Expect(k8sClient.Create(ctx, highPriorityClass))
			ginkgo.DeferCleanup(func() {
				gomega.Expect(k8sClient.Delete(ctx, highPriorityClass)).To(gomega.Succeed())
			})

			ginkgo.By("Job is admitted using the first flavor", func() {
				expectJobUnsuspendedWithNodeSelectors(jobKey, map[string]string{
					"instance-type": "on-demand",
				})
			})

			ginkgo.By("Job is preempted by higher priority job", func() {
				job := testingjob.MakeJob("high", ns.Name).
					Queue("main").
					PriorityClass("high").
					Request(corev1.ResourceCPU, "1").
					NodeSelector("instance-type", "on-demand"). // target the same flavor to cause preemption
					Obj()
				gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())

				expectJobUnsuspendedWithNodeSelectors(client.ObjectKeyFromObject(job), map[string]string{
					"instance-type": "on-demand",
				})
			})

			ginkgo.By("Job is re-admitted using the second flavor", func() {
				expectJobUnsuspendedWithNodeSelectors(jobKey, map[string]string{
					"instance-type": "spot",
				})
			})
		})

		ginkgo.It("Should readmit preempted job with workloadPriorityClass into a separate flavor", func() {
			gomega.Expect(k8sClient.Create(ctx, sampleJob)).Should(gomega.Succeed())

			highWorkloadPriorityClass := testing.MakeWorkloadPriorityClass("high-workload").PriorityValue(300).Obj()
			gomega.Expect(k8sClient.Create(ctx, highWorkloadPriorityClass)).Should(gomega.Succeed())
			ginkgo.DeferCleanup(func() {
				gomega.Expect(k8sClient.Delete(ctx, highWorkloadPriorityClass)).To(gomega.Succeed())
			})

			ginkgo.By("Job is admitted using the first flavor", func() {
				expectJobUnsuspendedWithNodeSelectors(jobKey, map[string]string{
					"instance-type": "on-demand",
				})
			})

			ginkgo.By("Job is preempted by higher priority job", func() {
				job := testingjob.MakeJob("high-with-wpc", ns.Name).
					Queue("main").
					WorkloadPriorityClass("high-workload").
					Request(corev1.ResourceCPU, "1").
					NodeSelector("instance-type", "on-demand"). // target the same flavor to cause preemption
					Obj()
				gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())

				expectJobUnsuspendedWithNodeSelectors(client.ObjectKeyFromObject(job), map[string]string{
					"instance-type": "on-demand",
				})
			})

			ginkgo.By("Job is re-admitted using the second flavor", func() {
				expectJobUnsuspendedWithNodeSelectors(jobKey, map[string]string{
					"instance-type": "spot",
				})
			})
		})
		ginkgo.It("Should partially admit the Job if configured and not fully fits", func() {
			// Use a binary that ends.
			job := testingjob.MakeJob("job", ns.Name).
				Queue("main").
				Image("gcr.io/k8s-staging-perf-tests/sleep:v0.0.3", []string{"1s"}).
				Request("cpu", "500m").
				Parallelism(3).
				Completions(4).
				SetAnnotation(workloadjob.JobMinParallelismAnnotation, "1").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())

			ginkgo.By("Wait for the job to start and check the updated Parallelism and Completions", func() {
				jobKey := client.ObjectKeyFromObject(job)
				expectJobUnsuspendedWithNodeSelectors(jobKey, map[string]string{
					"instance-type": "on-demand",
				})

				updatedJob := &batchv1.Job{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, jobKey, updatedJob)).To(gomega.Succeed())
					g.Expect(ptr.Deref(updatedJob.Spec.Parallelism, 0)).To(gomega.Equal(int32(2)))
					g.Expect(ptr.Deref(updatedJob.Spec.Completions, 0)).To(gomega.Equal(int32(4)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

			})

			ginkgo.By("Wait for the job to finish", func() {
				createdWorkload := &kueue.Workload{}
				wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name), Namespace: ns.Name}
				gomega.Eventually(func() bool {
					if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
						return false
					}
					return workload.HasQuotaReservation(createdWorkload) &&
						apimeta.IsStatusConditionTrue(createdWorkload.Status.Conditions, kueue.WorkloadFinished)

				}, util.LongTimeout, util.Interval).Should(gomega.BeTrue())
			})
		})
	})

	ginkgo.When("Creating a Job In a Twostepadmission Queue", func() {
		var (
			onDemandRF   *kueue.ResourceFlavor
			localQueue   *kueue.LocalQueue
			clusterQueue *kueue.ClusterQueue
			check        *kueue.AdmissionCheck
		)
		ginkgo.BeforeEach(func() {
			check = testing.MakeAdmissionCheck("check1").ControllerName("ac-controller").Obj()
			gomega.Expect(k8sClient.Create(ctx, check)).Should(gomega.Succeed())
			util.SetAdmissionCheckActive(ctx, k8sClient, check, metav1.ConditionTrue)
			onDemandRF = testing.MakeResourceFlavor("on-demand").
				Label("instance-type", "on-demand").Obj()
			gomega.Expect(k8sClient.Create(ctx, onDemandRF)).Should(gomega.Succeed())
			clusterQueue = testing.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*testing.MakeFlavorQuotas("on-demand").
						Resource(corev1.ResourceCPU, "1").
						Resource(corev1.ResourceMemory, "1Gi").
						Obj(),
				).
				AdmissionChecks("check1").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).Should(gomega.Succeed())
			localQueue = testing.MakeLocalQueue("main", ns.Name).ClusterQueue("cluster-queue").Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).Should(gomega.Succeed())
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteLocalQueue(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, onDemandRF, true)
			gomega.Expect(k8sClient.Delete(ctx, check)).Should(gomega.Succeed())
		})

		ginkgo.It("Should unsuspend a job only after all checks are cleared", func() {
			// Use a binary that ends.
			sampleJob = (&testingjob.JobWrapper{Job: *sampleJob}).Image("gcr.io/k8s-staging-perf-tests/sleep:v0.0.3", []string{"5s"}).Obj()
			gomega.Expect(k8sClient.Create(ctx, sampleJob)).Should(gomega.Succeed())

			createdWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(jobKey.Name), Namespace: ns.Name}

			ginkgo.By("verify the check is added to the workload", func() {
				gomega.Eventually(func() map[string]string {
					if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
						return nil
					}
					return slices.ToMap(createdWorkload.Status.AdmissionChecks, func(i int) (string, string) { return createdWorkload.Status.AdmissionChecks[i].Name, "" })

				}, util.LongTimeout, util.Interval).Should(gomega.BeComparableTo(map[string]string{"check1": ""}))
			})

			ginkgo.By("waiting for the workload to be assigned", func() {
				gomega.Eventually(func() bool {
					if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
						return false
					}
					return apimeta.IsStatusConditionTrue(createdWorkload.Status.Conditions, kueue.WorkloadQuotaReserved)

				}, util.Timeout, util.Interval).Should(gomega.BeTrue())
			})

			ginkgo.By("checking the job remains suspended", func() {
				createdJob := &batchv1.Job{}
				jobKey := client.ObjectKeyFromObject(sampleJob)
				gomega.Consistently(func() bool {
					if err := k8sClient.Get(ctx, jobKey, createdJob); err != nil {
						return false
					}
					return ptr.Deref(createdJob.Spec.Suspend, false)

				}, util.ConsistentDuration, util.Interval).Should(gomega.BeTrue())
			})

			ginkgo.By("setting the check as successful", func() {
				gomega.Eventually(func() error {
					if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
						return err
					}
					patch := workload.BaseSSAWorkload(createdWorkload)
					workload.SetAdmissionCheckState(&patch.Status.AdmissionChecks, kueue.AdmissionCheckState{
						Name:  "check1",
						State: kueue.CheckStateReady,
					})
					return k8sClient.Status().Patch(ctx, patch, client.Apply, client.FieldOwner("test-admission-check-controller"), client.ForceOwnership)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			expectJobUnsuspendedWithNodeSelectors(jobKey, map[string]string{
				"instance-type": "on-demand",
			})
			gomega.Eventually(func() bool {
				if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
					return false
				}
				return workload.HasQuotaReservation(createdWorkload) &&
					apimeta.IsStatusConditionTrue(createdWorkload.Status.Conditions, kueue.WorkloadFinished)

			}, util.LongTimeout, util.Interval).Should(gomega.BeTrue())
		})

		ginkgo.It("Should suspend a job when its checks become invalid", func() {
			// Use a binary that ends.
			sampleJob = (&testingjob.JobWrapper{Job: *sampleJob}).Image("gcr.io/k8s-staging-perf-tests/sleep:v0.0.3", []string{"5s"}).Obj()
			gomega.Expect(k8sClient.Create(ctx, sampleJob)).Should(gomega.Succeed())

			createdWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(jobKey.Name), Namespace: ns.Name}

			ginkgo.By("verify the check is added to the workload", func() {
				gomega.Eventually(func() map[string]string {
					if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
						return nil
					}
					return slices.ToMap(createdWorkload.Status.AdmissionChecks, func(i int) (string, string) { return createdWorkload.Status.AdmissionChecks[i].Name, "" })

				}, util.LongTimeout, util.Interval).Should(gomega.BeComparableTo(map[string]string{"check1": ""}))
			})

			ginkgo.By("setting the check as successful", func() {
				gomega.Eventually(func() error {
					if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
						return err
					}
					patch := workload.BaseSSAWorkload(createdWorkload)
					workload.SetAdmissionCheckState(&patch.Status.AdmissionChecks, kueue.AdmissionCheckState{
						Name:  "check1",
						State: kueue.CheckStateReady,
					})
					return k8sClient.Status().Patch(ctx, patch, client.Apply, client.FieldOwner("test-admission-check-controller"), client.ForceOwnership)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			expectJobUnsuspendedWithNodeSelectors(jobKey, map[string]string{
				"instance-type": "on-demand",
			})

			ginkgo.By("setting the check as failed (Retry)", func() {
				gomega.Eventually(func() error {
					if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
						return err
					}
					patch := workload.BaseSSAWorkload(createdWorkload)
					workload.SetAdmissionCheckState(&patch.Status.AdmissionChecks, kueue.AdmissionCheckState{
						Name:  "check1",
						State: kueue.CheckStateRetry,
					})
					return k8sClient.Status().Patch(ctx, patch, client.Apply, client.FieldOwner("test-admission-check-controller"), client.ForceOwnership)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("checking the job gets suspended", func() {
				createdJob := &batchv1.Job{}
				jobKey := client.ObjectKeyFromObject(sampleJob)
				gomega.Eventually(func() bool {
					if err := k8sClient.Get(ctx, jobKey, createdJob); err != nil {
						return false
					}
					return ptr.Deref(createdJob.Spec.Suspend, false)

				}, util.Timeout, util.Interval).Should(gomega.BeTrue())
			})
		})
	})
})

func expectJobUnsuspendedWithNodeSelectors(key types.NamespacedName, ns map[string]string) {
	job := &batchv1.Job{}
	gomega.EventuallyWithOffset(1, func() []any {
		gomega.Expect(k8sClient.Get(ctx, key, job)).To(gomega.Succeed())
		return []any{*job.Spec.Suspend, job.Spec.Template.Spec.NodeSelector}
	}, util.Timeout, util.Interval).Should(gomega.Equal([]any{false, ns}))
}

func defaultOwnerReferenceForJob(name string) []metav1.OwnerReference {
	return []metav1.OwnerReference{
		{
			APIVersion: "batch/v1",
			Kind:       "Job",
			Name:       name,
		},
	}
}
