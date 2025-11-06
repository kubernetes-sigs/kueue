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

package trainjob

import (
	"fmt"

	"github.com/google/go-cmp/cmp/cmpopts"
	kftrainerapi "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	jobsetapi "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	workloadtrainjob "sigs.k8s.io/kueue/pkg/controller/jobs/trainjob"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjobset "sigs.k8s.io/kueue/pkg/util/testingjobs/jobset"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingtrainjob "sigs.k8s.io/kueue/pkg/util/testingjobs/trainjob"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

const (
	instanceKey = "cloud.provider.com/instance"
)

var _ = ginkgo.Describe("Trainjob controller", ginkgo.Ordered, ginkgo.ContinueOnFailure, ginkgo.ContinueOnFailure, func() {
	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup(jobframework.WithManageJobsWithoutQueueName(true),
			jobframework.WithManagedJobsNamespaceSelector(util.NewNamespaceSelectorExcluding("unmanaged-ns"))))
		unmanagedNamespace := utiltesting.MakeNamespace("unmanaged-ns")
		util.MustCreate(ctx, k8sClient, unmanagedNamespace)
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	var (
		ns *corev1.Namespace
	)
	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "trainjob-")
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.When("basic setup", func() {
		var (
			clusterQueue   *kueue.ClusterQueue
			localQueue     *kueue.LocalQueue
			onDemandFlavor *kueue.ResourceFlavor
			spotFlavor     *kueue.ResourceFlavor
			testCtr        *kftrainerapi.ClusterTrainingRuntime
		)

		ginkgo.BeforeEach(func() {
			clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj(),
					*utiltestingapi.MakeFlavorQuotas("spot").Resource(corev1.ResourceCPU, "5").Obj(),
				).Obj()

			testJobSet := testingjobset.MakeJobSet("", "").ReplicatedJobs(
				testingjobset.ReplicatedJobRequirements{
					Name:     "node",
					Replicas: 1,
				}).
				Obj()
			testCtr = testingtrainjob.MakeClusterTrainingRuntime("test", testJobSet.Spec)

			util.MustCreate(ctx, k8sClient, testCtr)
			util.MustCreate(ctx, k8sClient, clusterQueue)
			localQueue = utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			util.MustCreate(ctx, k8sClient, localQueue)
			onDemandFlavor = utiltestingapi.MakeResourceFlavor("on-demand").NodeLabel(instanceKey, "on-demand").Obj()
			util.MustCreate(ctx, k8sClient, onDemandFlavor)
			spotFlavor = utiltestingapi.MakeResourceFlavor("spot").NodeLabel(instanceKey, "spot").Obj()
			util.MustCreate(ctx, k8sClient, spotFlavor)
		})

		ginkgo.AfterEach(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, testCtr, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, spotFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		})

		ginkgo.It("Should reconcile Trainjobs", func() {
			var (
				createdTrainJob kftrainerapi.TrainJob
				trainJob        *kftrainerapi.TrainJob
				wlLookupKey     types.NamespacedName
			)
			createdWorkload := &kueue.Workload{}
			ginkgo.By("creating a non suspended trainjob and its corresponding child jobset", func() {
				trainJob = testingtrainjob.MakeTrainJob("trainjob-test", ns.Name).RuntimeRef(kftrainerapi.RuntimeRef{
					APIGroup: ptr.To("trainer.kubeflow.org"),
					Name:     "test",
					Kind:     ptr.To("ClusterTrainingRuntime"),
				}).
					Suspend(false).
					Queue("local-queue").
					Obj()

				util.MustCreate(ctx, k8sClient, trainJob)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: trainJob.Name, Namespace: ns.Name}, &createdTrainJob)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("checking the workload is created", func() {
				wlLookupKey = types.NamespacedName{Name: workloadtrainjob.GetWorkloadNameForTrainJob(createdTrainJob.Name, createdTrainJob.UID), Namespace: ns.Name}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					g.Expect(createdWorkload.Spec.QueueName).Should(gomega.Equal(kueue.LocalQueueName("local-queue")))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("and that the trainjob is suspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: trainJob.Name, Namespace: ns.Name}, &createdTrainJob)).Should(gomega.Succeed())
					g.Expect(ptr.Deref(createdTrainJob.Spec.Suspend, false)).Should(gomega.BeTrue())
					g.Expect(createdWorkload.Spec.QueueName).Should(gomega.Equal(kueue.LocalQueueName("local-queue")))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("checking the Trainjob is unsuspended when workload is assigned", func() {
				admission := utiltestingapi.MakeAdmission(clusterQueue.Name).PodSets(
					kueue.PodSetAssignment{
						Name: createdWorkload.Spec.PodSets[0].Name,
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: kueue.ResourceFlavorReference(onDemandFlavor.Name),
						},
					},
				).Obj()
				util.SetQuotaReservation(ctx, k8sClient, wlLookupKey, admission)
				util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)

				lookupKey := types.NamespacedName{Name: trainJob.Name, Namespace: ns.Name}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, lookupKey, &createdTrainJob)).Should(gomega.Succeed())
					g.Expect(ptr.Deref(createdTrainJob.Spec.Suspend, false)).Should(gomega.BeFalse())
					ok, _ := utiltesting.CheckEventRecordedFor(ctx, k8sClient, "Started", corev1.EventTypeNormal, fmt.Sprintf("Admitted by clusterQueue %v", clusterQueue.Name), lookupKey)
					g.Expect(ok).Should(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
				util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, clusterQueue.Name, createdWorkload)
			})

			ginkgo.By("checking the workload is finished when TrainJob is completed", func() {
				apimeta.SetStatusCondition(&createdTrainJob.Status.Conditions, metav1.Condition{
					Type:   kftrainerapi.TrainJobComplete,
					Status: metav1.ConditionTrue,
					Reason: "ByTest",
				})
				gomega.Expect(k8sClient.Status().Update(ctx, &createdTrainJob)).Should(gomega.Succeed())
				util.ExpectWorkloadToFinish(ctx, k8sClient, wlLookupKey)
			})
		})

		ginkgo.It("A trainjob created in an unmanaged namespace is not suspended and a workload is not created", func() {
			ginkgo.By("Creating an unsuspended trainjob without a queue-name in unmanaged-ns", func() {
				trainJob := testingtrainjob.MakeTrainJob("trainjob-test", "unmanaged-ns").RuntimeRef(kftrainerapi.RuntimeRef{
					APIGroup: ptr.To("trainer.kubeflow.org"),
					Name:     "test",
					Kind:     ptr.To("ClusterTrainingRuntime"),
				}).
					Suspend(false).
					Obj()

				util.MustCreate(ctx, k8sClient, trainJob)
				createdTrainJob := &kftrainerapi.TrainJob{}
				wlLookupKey := types.NamespacedName{Name: workloadtrainjob.GetWorkloadNameForTrainJob(trainJob.Name, trainJob.UID), Namespace: ns.Name}
				createdWorkload := &kueue.Workload{}

				gomega.Consistently(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: trainJob.Name, Namespace: trainJob.Namespace}, createdTrainJob)).Should(gomega.Succeed())
					g.Expect(ptr.Deref(createdTrainJob.Spec.Suspend, false)).Should(gomega.BeFalse())
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(utiltesting.BeNotFoundError())
				}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should finish the preemption when the trainjob becomes inactive", func() {
			trainJob := testingtrainjob.MakeTrainJob("trainjob-test", ns.Name).RuntimeRef(kftrainerapi.RuntimeRef{
				APIGroup: ptr.To("trainer.kubeflow.org"),
				Name:     "test",
				Kind:     ptr.To("ClusterTrainingRuntime"),
			}).
				Suspend(false).
				Queue("local-queue").
				Obj()

			createdWorkload := &kueue.Workload{}
			var wlLookupKey types.NamespacedName

			ginkgo.By("admit the workload", func() {
				util.MustCreate(ctx, k8sClient, trainJob)
				wlLookupKey = types.NamespacedName{Name: workloadtrainjob.GetWorkloadNameForTrainJob(trainJob.Name, trainJob.UID), Namespace: ns.Name}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
				admission := utiltestingapi.MakeAdmission(localQueue.Name).PodSets(
					kueue.PodSetAssignment{
						Name: createdWorkload.Spec.PodSets[0].Name,
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: kueue.ResourceFlavorReference(onDemandFlavor.Name),
						},
					},
				).Obj()
				util.SetQuotaReservation(ctx, k8sClient, wlLookupKey, admission)
				util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
			})

			ginkgo.By("wait for the trainjob to be unsuspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: trainJob.Name, Namespace: ns.Name}, trainJob)).Should(gomega.Succeed())
					g.Expect(ptr.Deref(trainJob.Spec.Suspend, false)).Should(gomega.BeFalse())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("mark the trainjob as active", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(trainJob), trainJob)).To(gomega.Succeed())
					trainJob.Status.JobsStatus = []kftrainerapi.JobStatus{
						testingtrainjob.MakeJobStatusWrapper("node").Active(1).Obj(),
					}
					g.Expect(k8sClient.Status().Update(ctx, trainJob)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("preempt the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					g.Expect(workload.UpdateStatus(ctx, k8sClient, createdWorkload, kueue.WorkloadEvicted, metav1.ConditionTrue, kueue.WorkloadEvictedByPreemption, "By test", "evict", clock.RealClock{})).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("wait for the trainjob to be suspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(trainJob), trainJob)).To(gomega.Succeed())
					g.Expect(*trainJob.Spec.Suspend).To(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("the workload should stay admitted", func() {
				gomega.Consistently(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					g.Expect(createdWorkload.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadQuotaReserved))
				}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
			})

			ginkgo.By("mark the trainjob as inactive", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(trainJob), trainJob)).To(gomega.Succeed())
					trainJob.Status.JobsStatus[0].Active = ptr.To(int32(0))
					g.Expect(k8sClient.Status().Update(ctx, trainJob)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("the workload should get unadmitted", func() {
				util.ExpectWorkloadsToBePending(ctx, k8sClient, createdWorkload)
			})
		})
	})
})

var _ = ginkgo.Describe("TrainJob controller for workloads when only jobs with queue are managed", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup())
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	var (
		ns *corev1.Namespace
	)
	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "trainjob-")
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.It("Should reconcile jobs only when queue is set", func() {
		ginkgo.By("checking the workload is not created when queue name is not set")
		testJobSet := testingjobset.MakeJobSet("", "").ReplicatedJobs(
			testingjobset.ReplicatedJobRequirements{
				Name:     "node",
				Replicas: 1,
			}).
			Obj()
		testTr := testingtrainjob.MakeTrainingRuntime("test", ns.Name, testJobSet.Spec)
		trainJob := testingtrainjob.MakeTrainJob("trainjob-test", ns.Name).RuntimeRef(kftrainerapi.RuntimeRef{
			APIGroup: ptr.To("trainer.kubeflow.org"),
			Name:     "test",
			Kind:     ptr.To(kftrainerapi.TrainingRuntimeKind),
		}).
			Suspend(false).
			Obj()

		util.MustCreate(ctx, k8sClient, testTr)
		util.MustCreate(ctx, k8sClient, trainJob)
		createdTrainJob := &kftrainerapi.TrainJob{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: trainJob.Name, Namespace: ns.Name}, createdTrainJob)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		createdWorkload := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadtrainjob.GetWorkloadNameForTrainJob(trainJob.Name, trainJob.UID), Namespace: ns.Name}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(utiltesting.BeNotFoundError())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("checking the workload is created when queue name is set")
		jobQueueName := "test-queue"
		if createdTrainJob.Labels == nil {
			createdTrainJob.Labels = map[string]string{constants.QueueLabel: jobQueueName}
		} else {
			createdTrainJob.Labels[constants.QueueLabel] = jobQueueName
		}
		gomega.Expect(k8sClient.Update(ctx, createdTrainJob)).Should(gomega.Succeed())
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})
})

var _ = ginkgo.Describe("TrainJob controller interacting with scheduler", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup(false))
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	var (
		ns                  *corev1.Namespace
		onDemandFlavor      *kueue.ResourceFlavor
		spotUntaintedFlavor *kueue.ResourceFlavor
		clusterQueue        *kueue.ClusterQueue
		localQueue          *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "trainjob-")

		onDemandFlavor = utiltestingapi.MakeResourceFlavor("on-demand").NodeLabel(instanceKey, "on-demand").Obj()
		util.MustCreate(ctx, k8sClient, onDemandFlavor)

		spotUntaintedFlavor = utiltestingapi.MakeResourceFlavor("spot-untainted").NodeLabel(instanceKey, "spot-untainted").Obj()
		util.MustCreate(ctx, k8sClient, spotUntaintedFlavor)

		clusterQueue = utiltestingapi.MakeClusterQueue("dev-clusterqueue").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("spot-untainted").Resource(corev1.ResourceCPU, "1").Obj(),
				*utiltestingapi.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj(),
			).Obj()
		util.MustCreate(ctx, k8sClient, clusterQueue)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, spotUntaintedFlavor, true)
	})

	ginkgo.It("Should schedule TrainJobs as they fit in their ClusterQueue", func() {
		ginkgo.By("creating localQueue")
		localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		util.MustCreate(ctx, k8sClient, localQueue)

		ginkgo.By("checking a dev job starts")

		testJobSet := testingjobset.MakeJobSet("", "").ReplicatedJobs(
			testingjobset.ReplicatedJobRequirements{
				Name:        "node-1",
				Replicas:    1,
				Parallelism: 1,
				Completions: 1,
			}, testingjobset.ReplicatedJobRequirements{
				Name:        "node-2",
				Replicas:    3,
				Parallelism: 1,
				Completions: 1,
			},
		).
			Request("node-1", corev1.ResourceCPU, "1").
			Request("node-2", corev1.ResourceCPU, "1").
			Obj()
		testTr := testingtrainjob.MakeTrainingRuntime("test", ns.Name, testJobSet.Spec)
		trainJob := testingtrainjob.MakeTrainJob("trainjob-test", ns.Name).RuntimeRef(kftrainerapi.RuntimeRef{
			APIGroup: ptr.To("trainer.kubeflow.org"),
			Name:     "test",
			Kind:     ptr.To(kftrainerapi.TrainingRuntimeKind),
		}).
			Queue("local-queue").
			Obj()

		util.MustCreate(ctx, k8sClient, testTr)
		util.MustCreate(ctx, k8sClient, trainJob)
		createdTrainJob := &kftrainerapi.TrainJob{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: trainJob.Name, Namespace: ns.Name}, createdTrainJob)).Should(gomega.Succeed())
			g.Expect(*createdTrainJob.Spec.Suspend).Should(gomega.BeFalse())
			g.Expect(createdTrainJob.Spec.PodTemplateOverrides).To(gomega.HaveLen(2))
			g.Expect(createdTrainJob.Spec.PodTemplateOverrides[0].TargetJobs[0]).Should(gomega.Equal(kftrainerapi.PodTemplateOverrideTargetJob{Name: "node-1"}))
			g.Expect(createdTrainJob.Spec.PodTemplateOverrides[1].TargetJobs[0]).Should(gomega.Equal(kftrainerapi.PodTemplateOverrideTargetJob{Name: "node-2"}))
			g.Expect(createdTrainJob.Spec.PodTemplateOverrides[0].Spec.NodeSelector[instanceKey]).Should(gomega.Equal(spotUntaintedFlavor.Name))
			g.Expect(createdTrainJob.Spec.PodTemplateOverrides[1].Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
		util.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
	})

	ginkgo.It("Should allow reclaim of resources that are no longer needed", func() {
		ginkgo.By("creating localQueue", func() {
			localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			util.MustCreate(ctx, k8sClient, localQueue)
		})

		testJobset1 := testingjobset.MakeJobSet("dev-jobset1", ns.Name).ReplicatedJobs(
			testingjobset.ReplicatedJobRequirements{
				Name:        "node-1",
				Replicas:    2,
				Parallelism: 4,
				Completions: 8,
			}, testingjobset.ReplicatedJobRequirements{
				Name:        "node-2",
				Replicas:    3,
				Parallelism: 4,
				Completions: 4,
			},
		).
			Request("node-1", corev1.ResourceCPU, "250m").
			Request("node-2", corev1.ResourceCPU, "250m").
			Obj()
		testTr1 := testingtrainjob.MakeTrainingRuntime("tr-1", ns.Name, testJobset1.Spec)
		trainJob1 := testingtrainjob.MakeTrainJob("trainjob-test", ns.Name).RuntimeRef(kftrainerapi.RuntimeRef{
			APIGroup: ptr.To("trainer.kubeflow.org"),
			Name:     "tr-1",
			Kind:     ptr.To(kftrainerapi.TrainingRuntimeKind),
		}).
			Queue("local-queue").
			Suspend(true).
			Obj()

		util.MustCreate(ctx, k8sClient, testTr1)
		util.MustCreate(ctx, k8sClient, trainJob1)
		ginkgo.By("checking the first trainjob starts", func() {
			createdTrainJob1 := &kftrainerapi.TrainJob{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: trainJob1.Name, Namespace: ns.Name}, createdTrainJob1)).Should(gomega.Succeed())
				g.Expect(*createdTrainJob1.Spec.Suspend).Should(gomega.BeFalse())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
			util.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
		})

		testJobset2 := testingjobset.MakeJobSet("", "").ReplicatedJobs(
			testingjobset.ReplicatedJobRequirements{
				Name:        "node-1",
				Replicas:    2,
				Parallelism: 1,
				Completions: 1,
			}, testingjobset.ReplicatedJobRequirements{
				Name:        "node-2",
				Replicas:    1,
				Parallelism: 1,
				Completions: 1,
			},
		).Queue(localQueue.Name).
			Request("node-1", corev1.ResourceCPU, "1").
			Request("node-2", corev1.ResourceCPU, "1").
			Obj()

		testTr2 := testingtrainjob.MakeTrainingRuntime("tr-2", ns.Name, testJobset2.Spec)
		trainJob2 := testingtrainjob.MakeTrainJob("trainjob-test-2", ns.Name).RuntimeRef(kftrainerapi.RuntimeRef{
			APIGroup: ptr.To("trainer.kubeflow.org"),
			Name:     "tr-2",
			Kind:     ptr.To(kftrainerapi.TrainingRuntimeKind),
		}).
			Queue("local-queue").
			Suspend(true).
			Obj()

		util.MustCreate(ctx, k8sClient, testTr2)
		util.MustCreate(ctx, k8sClient, trainJob2)
		ginkgo.By("checking a second no-fit trainjob does not start", func() {
			createdTrainJob2 := &kftrainerapi.TrainJob{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: trainJob2.Name, Namespace: ns.Name}, createdTrainJob2)).Should(gomega.Succeed())
				g.Expect(*createdTrainJob2.Spec.Suspend).Should(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
			util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
		})

		ginkgo.By("checking the second job starts when the first one needs less then two cpus", func() {
			createdTrainJob1 := &kftrainerapi.TrainJob{}
			gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: trainJob1.Name, Namespace: ns.Name}, createdTrainJob1)).Should(gomega.Succeed())
			createdTrainJob1.Status.JobsStatus = []kftrainerapi.JobStatus{
				testingtrainjob.MakeJobStatusWrapper("node-1").Succeeded(2).Obj(),
				testingtrainjob.MakeJobStatusWrapper("node-2").Succeeded(1).Obj(),
			}
			gomega.Expect(k8sClient.Status().Update(ctx, createdTrainJob1)).Should(gomega.Succeed())

			wl := &kueue.Workload{}
			wlKey := types.NamespacedName{Name: workloadtrainjob.GetWorkloadNameForTrainJob(createdTrainJob1.Name, createdTrainJob1.UID), Namespace: createdTrainJob1.Namespace}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
				g.Expect(wl.Status.ReclaimablePods).Should(gomega.BeComparableTo([]kueue.ReclaimablePod{
					{
						Name:  "node-1",
						Count: 8,
					},
					{
						Name:  "node-2",
						Count: 4,
					},
				}))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			createdTrainJob2 := &kftrainerapi.TrainJob{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: trainJob2.Name, Namespace: ns.Name}, createdTrainJob2)).Should(gomega.Succeed())
				g.Expect(*createdTrainJob2.Spec.Suspend).Should(gomega.BeFalse())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
			util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 2)
		})
	})
})

var _ = ginkgo.Describe("TrainJob controller with TopologyAwareScheduling", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	const (
		nodeGroupLabel = "node-group"
	)

	var (
		ns           *corev1.Namespace
		nodes        []corev1.Node
		topology     *kueue.Topology
		tasFlavor    *kueue.ResourceFlavor
		clusterQueue *kueue.ClusterQueue
		localQueue   *kueue.LocalQueue
	)

	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup(true))
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "tas-jobset-")

		nodes = []corev1.Node{
			*testingnode.MakeNode("b1r1").
				Label(nodeGroupLabel, "tas").
				Label(utiltesting.DefaultBlockTopologyLevel, "b1").
				Label(utiltesting.DefaultRackTopologyLevel, "r1").
				StatusAllocatable(corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1"),
					corev1.ResourceMemory: resource.MustParse("1Gi"),
					corev1.ResourcePods:   resource.MustParse("10"),
				}).
				Ready().
				Obj(),
		}
		util.CreateNodesWithStatus(ctx, k8sClient, nodes)

		topology = utiltestingapi.MakeDefaultTwoLevelTopology("default")
		util.MustCreate(ctx, k8sClient, topology)

		tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
			NodeLabel(nodeGroupLabel, "tas").
			TopologyName("default").Obj()
		util.MustCreate(ctx, k8sClient, tasFlavor)

		clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj()).
			Obj()
		util.MustCreate(ctx, k8sClient, clusterQueue)
		util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		util.MustCreate(ctx, k8sClient, localQueue)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		for _, node := range nodes {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, &node, true)
		}
	})

	ginkgo.It("should admit workload which fits in a required topology domain", func() {
		testJobSet := testingjobset.MakeJobSet("", "").ReplicatedJobs(
			testingjobset.ReplicatedJobRequirements{
				Name:        "node-1",
				Replicas:    1,
				Parallelism: 1,
				Completions: 1,
				PodAnnotations: map[string]string{
					kueue.PodSetRequiredTopologyAnnotation: utiltesting.DefaultBlockTopologyLevel,
				},
			}, testingjobset.ReplicatedJobRequirements{
				Name:        "node-2",
				Replicas:    1,
				Parallelism: 1,
				Completions: 1,
				PodAnnotations: map[string]string{
					kueue.PodSetPreferredTopologyAnnotation: utiltesting.DefaultRackTopologyLevel,
				},
			},
		).
			Request("node-1", corev1.ResourceCPU, "100m").
			Request("node-2", corev1.ResourceCPU, "100m").
			Obj()
		testTr := testingtrainjob.MakeTrainingRuntime("test", ns.Name, testJobSet.Spec)
		trainJob := testingtrainjob.MakeTrainJob("trainjob-test", ns.Name).RuntimeRef(kftrainerapi.RuntimeRef{
			APIGroup: ptr.To("trainer.kubeflow.org"),
			Name:     "test",
			Kind:     ptr.To(kftrainerapi.TrainingRuntimeKind),
		}).
			Queue(localQueue.Name).
			Suspend(false).
			Obj()

		ginkgo.By("creating a TrainJob", func() {
			util.MustCreate(ctx, k8sClient, testTr)
			util.MustCreate(ctx, k8sClient, trainJob)
		})

		wl := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{
			Name:      workloadtrainjob.GetWorkloadNameForTrainJob(trainJob.Name, trainJob.UID),
			Namespace: ns.Name,
		}

		ginkgo.By("verify the workload is created", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, wl)).Should(gomega.Succeed())
				g.Expect(wl.Spec.PodSets).Should(gomega.BeComparableTo([]kueue.PodSet{
					{
						Name:  "node-1",
						Count: 1,
						TopologyRequest: &kueue.PodSetTopologyRequest{
							Required:           ptr.To(utiltesting.DefaultBlockTopologyLevel),
							PodIndexLabel:      ptr.To(batchv1.JobCompletionIndexAnnotation),
							SubGroupIndexLabel: ptr.To(jobsetapi.JobIndexKey),
							SubGroupCount:      ptr.To[int32](1),
						},
					},
					{
						Name:  "node-2",
						Count: 1,
						TopologyRequest: &kueue.PodSetTopologyRequest{
							Preferred:          ptr.To(utiltesting.DefaultRackTopologyLevel),
							PodIndexLabel:      ptr.To(batchv1.JobCompletionIndexAnnotation),
							SubGroupIndexLabel: ptr.To(jobsetapi.JobIndexKey),
							SubGroupCount:      ptr.To[int32](1),
						},
					},
				}, cmpopts.IgnoreFields(kueue.PodSet{}, "Template")))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verify the workload is admitted", func() {
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
			util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
		})

		ginkgo.By("verify admission for the workload", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, wl)).Should(gomega.Succeed())
				g.Expect(wl.Status.Admission).ShouldNot(gomega.BeNil())
				g.Expect(wl.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(2))
				g.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
					&kueue.TopologyAssignment{
						Levels:  []string{utiltesting.DefaultBlockTopologyLevel, utiltesting.DefaultRackTopologyLevel},
						Domains: []kueue.TopologyDomainAssignment{{Count: 1, Values: []string{"b1", "r1"}}},
					},
				))
				g.Expect(wl.Status.Admission.PodSetAssignments[1].TopologyAssignment).Should(gomega.BeComparableTo(
					&kueue.TopologyAssignment{
						Levels:  []string{utiltesting.DefaultBlockTopologyLevel, utiltesting.DefaultRackTopologyLevel},
						Domains: []kueue.TopologyDomainAssignment{{Count: 1, Values: []string{"b1", "r1"}}},
					},
				))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
