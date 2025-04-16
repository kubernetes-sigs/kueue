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

package raycluster

import (
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	workloadraycluster "sigs.k8s.io/kueue/pkg/controller/jobs/raycluster"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingraycluster "sigs.k8s.io/kueue/pkg/util/testingjobs/raycluster"
	testingrayjob "sigs.k8s.io/kueue/pkg/util/testingjobs/rayjob"
	"sigs.k8s.io/kueue/test/util"

	_ "sigs.k8s.io/kueue/pkg/controller/jobs/rayjob" // to enable the framework
)

const (
	jobName                 = "test-job"
	instanceKey             = "cloud.provider.com/instance"
	priorityClassName       = "test-priority-class"
	priorityValue     int32 = 10
)

var _ = ginkgo.Describe("RayCluster controller", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup(jobframework.WithManageJobsWithoutQueueName(true),
			jobframework.WithManagedJobsNamespaceSelector(util.NewNamespaceSelectorExcluding("unmanaged-ns"))))
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	var (
		ns *corev1.Namespace
	)
	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-")
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.It("Should reconcile RayClusters", func() {
		ginkgo.By("checking the job gets suspended when created unsuspended")
		priorityClass := testing.MakePriorityClass(priorityClassName).
			PriorityValue(priorityValue).Obj()
		util.MustCreate(ctx, k8sClient, priorityClass)
		defer func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, priorityClass, true)
		}()

		job := testingraycluster.MakeCluster(jobName, ns.Name).
			Suspend(false).
			WithPriorityClassName(priorityClassName).
			Obj()
		util.MustCreate(ctx, k8sClient, job)
		createdJob := &rayv1.RayCluster{}

		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: jobName, Namespace: ns.Name}, createdJob)).Should(gomega.Succeed())
			g.Expect(createdJob.Spec.Suspend).Should(gomega.Equal(ptr.To(true)))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("checking the workload is created without queue assigned")
		createdWorkload := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadraycluster.GetWorkloadNameForRayCluster(job.Name, job.UID), Namespace: ns.Name}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		gomega.Expect(createdWorkload.Spec.QueueName).Should(gomega.Equal(""), "The Workload shouldn't have .spec.queueName set")
		gomega.Expect(metav1.IsControlledBy(createdWorkload, createdJob)).To(gomega.BeTrue(), "The Workload should be owned by the Job")

		ginkgo.By("checking the workload is created with priority and priorityName")
		gomega.Expect(createdWorkload.Spec.PriorityClassName).Should(gomega.Equal(priorityClassName))
		gomega.Expect(*createdWorkload.Spec.Priority).Should(gomega.Equal(priorityValue))

		ginkgo.By("checking the workload is updated with queue name when the job does")
		jobQueueName := "test-queue"
		createdJob.Annotations = map[string]string{constants.QueueAnnotation: jobQueueName}
		gomega.Expect(k8sClient.Update(ctx, createdJob)).Should(gomega.Succeed())
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
			g.Expect(createdWorkload.Spec.QueueName).Should(gomega.Equal(jobQueueName))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("checking a second non-matching workload is deleted")
		secondWl := &kueue.Workload{
			ObjectMeta: metav1.ObjectMeta{
				Name:      workloadraycluster.GetWorkloadNameForRayCluster("second-workload", "test-uid"),
				Namespace: createdWorkload.Namespace,
			},
			Spec: *createdWorkload.Spec.DeepCopy(),
		}

		gomega.Expect(ctrl.SetControllerReference(createdJob, secondWl, k8sClient.Scheme())).Should(gomega.Succeed())
		secondWl.Spec.PodSets[0].Count++

		util.MustCreate(ctx, k8sClient, secondWl)
		gomega.Eventually(func(g gomega.Gomega) {
			wl := &kueue.Workload{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(secondWl), wl)).Should(testing.BeNotFoundError())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		// check the original wl is still there
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("checking the job is unsuspended when workload is assigned")
		onDemandFlavor := testing.MakeResourceFlavor("on-demand").NodeLabel(instanceKey, "on-demand").Obj()
		util.MustCreate(ctx, k8sClient, onDemandFlavor)
		spotFlavor := testing.MakeResourceFlavor("spot").NodeLabel(instanceKey, "spot").Obj()
		util.MustCreate(ctx, k8sClient, spotFlavor)
		defer func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, spotFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		}()
		clusterQueue := testing.MakeClusterQueue("cluster-queue").
			ResourceGroup(
				*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj(),
				*testing.MakeFlavorQuotas("spot").Resource(corev1.ResourceCPU, "5").Obj(),
			).Obj()
		admission := testing.MakeAdmission(clusterQueue.Name).PodSets(
			kueue.PodSetAssignment{
				Name: createdWorkload.Spec.PodSets[0].Name,
				Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
					corev1.ResourceCPU: "on-demand",
				},
			}, kueue.PodSetAssignment{
				Name: createdWorkload.Spec.PodSets[1].Name,
				Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
					corev1.ResourceCPU: "spot",
				},
			},
		).Obj()
		gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, admission)).Should(gomega.Succeed())
		util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)

		lookupKey := types.NamespacedName{Name: jobName, Namespace: ns.Name}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
			g.Expect(createdJob.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		gomega.Eventually(func(g gomega.Gomega) {
			ok, _ := testing.CheckEventRecordedFor(ctx, k8sClient, "Started", corev1.EventTypeNormal, fmt.Sprintf("Admitted by clusterQueue %v", clusterQueue.Name), lookupKey)
			g.Expect(ok).Should(gomega.BeTrue())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		gomega.Expect(createdJob.Spec.HeadGroupSpec.Template.Spec.NodeSelector).Should(gomega.HaveLen(1))
		gomega.Expect(createdJob.Spec.HeadGroupSpec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))
		gomega.Expect(createdJob.Spec.WorkerGroupSpecs[0].Template.Spec.NodeSelector).Should(gomega.HaveLen(1))
		gomega.Expect(createdJob.Spec.WorkerGroupSpecs[0].Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(spotFlavor.Name))
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
			g.Expect(createdWorkload.Status.Conditions).Should(testing.HaveConditionStatusTrue(kueue.WorkloadQuotaReserved))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("checking the job gets suspended when parallelism changes and the added node selectors are removed")
		parallelism := ptr.Deref(job.Spec.WorkerGroupSpecs[0].Replicas, 1)
		newParallelism := parallelism + 1
		createdJob.Spec.WorkerGroupSpecs[0].Replicas = &newParallelism
		gomega.Expect(k8sClient.Update(ctx, createdJob)).Should(gomega.Succeed())
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
			g.Expect(createdJob.Spec.Suspend).Should(gomega.Equal(ptr.To(true)))
			g.Expect(createdJob.Spec.WorkerGroupSpecs[0].Template.Spec.NodeSelector).Should(gomega.BeEmpty())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		gomega.Eventually(func(g gomega.Gomega) {
			ok, _ := testing.CheckEventRecordedFor(ctx, k8sClient, "DeletedWorkload", corev1.EventTypeNormal, fmt.Sprintf("Deleted not matching Workload: %v", wlLookupKey.String()), lookupKey)
			g.Expect(ok).Should(gomega.BeTrue())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("checking the workload is updated with new count")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
			g.Expect(createdWorkload.Spec.PodSets[1].Count).Should(gomega.Equal(newParallelism))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		gomega.Expect(createdWorkload.Status.Admission).Should(gomega.BeNil())

		ginkgo.By("checking the job is unsuspended and selectors added when workload is assigned again")
		gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, admission)).Should(gomega.Succeed())
		util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
			g.Expect(createdJob.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		gomega.Expect(createdJob.Spec.HeadGroupSpec.Template.Spec.NodeSelector).Should(gomega.HaveLen(1))
		gomega.Expect(createdJob.Spec.HeadGroupSpec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))
		gomega.Expect(createdJob.Spec.WorkerGroupSpecs[0].Template.Spec.NodeSelector).Should(gomega.HaveLen(1))
		gomega.Expect(createdJob.Spec.WorkerGroupSpecs[0].Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(spotFlavor.Name))
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
			g.Expect(createdWorkload.Status.Conditions).Should(testing.HaveConditionStatusTrue(kueue.WorkloadQuotaReserved))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})
})

var _ = ginkgo.Describe("Job controller RayCluster for workloads when only jobs with queue are managed", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
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
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-")
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.It("Should reconcile jobs only when queue is set", func() {
		ginkgo.By("checking the workload is not created when queue name is not set")
		job := testingraycluster.MakeCluster(jobName, ns.Name).Obj()
		util.MustCreate(ctx, k8sClient, job)
		lookupKey := types.NamespacedName{Name: jobName, Namespace: ns.Name}
		createdJob := &rayv1.RayCluster{}
		gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())

		createdWorkload := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadraycluster.GetWorkloadNameForRayCluster(job.Name, job.UID), Namespace: ns.Name}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(testing.BeNotFoundError())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("checking the workload is created when queue name is set")
		jobQueueName := "test-queue"
		if createdJob.Labels == nil {
			createdJob.Labels = map[string]string{constants.QueueAnnotation: jobQueueName}
		} else {
			createdJob.Labels[constants.QueueLabel] = jobQueueName
		}
		gomega.Expect(k8sClient.Update(ctx, createdJob)).Should(gomega.Succeed())
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("Should suspend a cluster if the parent's workload does not exist or is not admitted", func() {
		ginkgo.By("Creating the parent job which has a queue name")
		parentJob := testingrayjob.MakeJob("parent-job", ns.Name).
			Queue("test").
			Suspend(false).
			Obj()
		util.MustCreate(ctx, k8sClient, parentJob)

		ginkgo.By("Creating the child cluster.")
		childCluster := testingraycluster.MakeCluster(jobName, ns.Name).
			Suspend(false).
			Obj()
		gomega.Expect(ctrl.SetControllerReference(parentJob, childCluster, k8sClient.Scheme())).To(gomega.Succeed())
		util.MustCreate(ctx, k8sClient, childCluster)

		childClusterKey := client.ObjectKeyFromObject(childCluster)
		ginkgo.By("checking that the child cluster is suspended")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, childClusterKey, childCluster)).Should(gomega.Succeed())
			g.Expect(childCluster.Spec.Suspend).Should(gomega.Equal(ptr.To(true)))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})
})

var _ = ginkgo.Describe("Job controller when waitForPodsReady enabled", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	type podsReadyTestSpec struct {
		beforeJobStatus *rayv1.RayClusterStatus
		beforeCondition *metav1.Condition
		jobStatus       rayv1.RayClusterStatus
		suspended       bool
		wantCondition   *metav1.Condition
	}

	var defaultFlavor = testing.MakeResourceFlavor("default").NodeLabel(instanceKey, "default").Obj()

	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup(jobframework.WithWaitForPodsReady(&configapi.WaitForPodsReady{Enable: true})))

		ginkgo.By("Create a resource flavor")
		util.MustCreate(ctx, k8sClient, defaultFlavor)
	})

	ginkgo.AfterAll(func() {
		util.ExpectObjectToBeDeleted(ctx, k8sClient, defaultFlavor, true)
		fwk.StopManager(ctx)
	})

	var (
		ns *corev1.Namespace
	)
	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-")
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.DescribeTable("Single job at different stages of progress towards completion",
		func(podsReadyTestSpec podsReadyTestSpec) {
			ginkgo.By("Create a job")
			job := testingraycluster.MakeCluster(jobName, ns.Name).Obj()
			jobQueueName := "test-queue"
			job.Annotations = map[string]string{constants.QueueAnnotation: jobQueueName}
			util.MustCreate(ctx, k8sClient, job)
			lookupKey := types.NamespacedName{Name: jobName, Namespace: ns.Name}
			createdJob := &rayv1.RayCluster{}
			gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())

			ginkgo.By("Fetch the workload created for the job")
			createdWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: workloadraycluster.GetWorkloadNameForRayCluster(job.Name, job.UID), Namespace: ns.Name}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Admit the workload created for the job")
			admission := testing.MakeAdmission("foo").PodSets(
				kueue.PodSetAssignment{
					Name: createdWorkload.Spec.PodSets[0].Name,
					Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
						corev1.ResourceCPU: "default",
					},
				}, kueue.PodSetAssignment{
					Name: createdWorkload.Spec.PodSets[1].Name,
					Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
						corev1.ResourceCPU: "default",
					},
				},
			).Obj()
			gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, admission)).Should(gomega.Succeed())
			util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
			gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())

			ginkgo.By("Await for the job to be unsuspended")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
				g.Expect(createdJob.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			if podsReadyTestSpec.beforeJobStatus != nil {
				ginkgo.By("Update the job status to simulate its initial progress towards completion")
				createdJob.Status = *podsReadyTestSpec.beforeJobStatus
				gomega.Expect(k8sClient.Status().Update(ctx, createdJob)).Should(gomega.Succeed())
				gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
			}

			if podsReadyTestSpec.beforeCondition != nil {
				ginkgo.By("Update the workload status")
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					g.Expect(apimeta.FindStatusCondition(createdWorkload.Status.Conditions, kueue.WorkloadPodsReady)).Should(
						gomega.BeComparableTo(podsReadyTestSpec.beforeCondition, util.IgnoreConditionTimestampsAndObservedGeneration),
					)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			}

			ginkgo.By("Update the job status to simulate its progress towards completion")
			createdJob.Status = podsReadyTestSpec.jobStatus
			gomega.Expect(k8sClient.Status().Update(ctx, createdJob)).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())

			if podsReadyTestSpec.suspended {
				ginkgo.By("Unset admission of the workload to suspend the job")
				gomega.Eventually(func(g gomega.Gomega) {
					// the update may need to be retried due to a conflict as the workload gets
					// also updated due to setting of the job status.
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					g.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, nil)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
				util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
			}

			ginkgo.By("Verify the PodsReady condition is added")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				g.Expect(apimeta.FindStatusCondition(createdWorkload.Status.Conditions, kueue.WorkloadPodsReady)).Should(
					gomega.BeComparableTo(podsReadyTestSpec.wantCondition, util.IgnoreConditionTimestampsAndObservedGeneration),
				)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		},

		ginkgo.Entry("No progress", podsReadyTestSpec{
			wantCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionFalse,
				Reason:  kueue.WorkloadWaitForStart,
				Message: "Not all pods are ready or succeeded",
			},
		}),
		ginkgo.Entry("Running RayCluster", podsReadyTestSpec{
			jobStatus: rayv1.RayClusterStatus{
				State: rayv1.Ready,
			},
			wantCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionTrue,
				Reason:  kueue.WorkloadStarted,
				Message: "All pods reached readiness and the workload is running",
			},
		}),

		ginkgo.Entry("Running RayCluster; PodsReady=False before", podsReadyTestSpec{
			beforeCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionFalse,
				Reason:  kueue.WorkloadWaitForStart,
				Message: "Not all pods are ready or succeeded",
			},
			jobStatus: rayv1.RayClusterStatus{

				State: rayv1.Ready,
			},
			wantCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionTrue,
				Reason:  kueue.WorkloadStarted,
				Message: "All pods reached readiness and the workload is running",
			},
		}),
		ginkgo.Entry("Job suspended; PodsReady=True before", podsReadyTestSpec{
			beforeJobStatus: &rayv1.RayClusterStatus{
				State: rayv1.Ready,
			},
			beforeCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionTrue,
				Reason:  kueue.WorkloadStarted,
				Message: "All pods reached readiness and the workload is running",
			},
			jobStatus: rayv1.RayClusterStatus{
				State: rayv1.Ready,
			},
			suspended: true,
			wantCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionFalse,
				Reason:  kueue.WorkloadWaitForStart,
				Message: "Not all pods are ready or succeeded",
			},
		}),
	)
})

var _ = ginkgo.Describe("RayCluster Job controller interacting with scheduler", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup())
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
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-")

		onDemandFlavor = testing.MakeResourceFlavor("on-demand").NodeLabel(instanceKey, "on-demand").Obj()
		util.MustCreate(ctx, k8sClient, onDemandFlavor)

		spotUntaintedFlavor = testing.MakeResourceFlavor("spot-untainted").NodeLabel(instanceKey, "spot-untainted").Obj()
		util.MustCreate(ctx, k8sClient, spotUntaintedFlavor)

		clusterQueue = testing.MakeClusterQueue("dev-clusterqueue").
			ResourceGroup(
				*testing.MakeFlavorQuotas("spot-untainted").Resource(corev1.ResourceCPU, "4").Obj(),
				*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "4").Obj(),
			).Obj()
		util.MustCreate(ctx, k8sClient, clusterQueue)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, spotUntaintedFlavor, true)
	})

	ginkgo.It("Should schedule jobs as they fit in their ClusterQueue", func() {
		ginkgo.By("creating localQueue")
		localQueue = testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		util.MustCreate(ctx, k8sClient, localQueue)

		ginkgo.By("checking a dev job starts")
		job := testingraycluster.MakeCluster("dev-job", ns.Name).Queue(localQueue.Name).
			RequestHead(corev1.ResourceCPU, "3").
			RequestWorkerGroup(corev1.ResourceCPU, "4").
			Obj()
		util.MustCreate(ctx, k8sClient, job)
		createdJob := &rayv1.RayCluster{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), createdJob)).Should(gomega.Succeed())
			g.Expect(createdJob.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		gomega.Expect(createdJob.Spec.HeadGroupSpec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(spotUntaintedFlavor.Name))
		gomega.Expect(createdJob.Spec.WorkerGroupSpecs[0].Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))
		util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
		util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)

		ginkgo.By("checking a second no-fit RayCluster does not start")
		job2 := testingraycluster.MakeCluster("dev-job2", ns.Name).Queue(localQueue.Name).
			RequestHead(corev1.ResourceCPU, "2").
			RequestWorkerGroup(corev1.ResourceCPU, "2").
			Obj()
		util.MustCreate(ctx, k8sClient, job2)
		createdJob2 := &rayv1.RayCluster{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job2), createdJob2)).Should(gomega.Succeed())
			g.Expect(createdJob2.Spec.Suspend).Should(gomega.Equal(ptr.To(true)))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
		util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)

		ginkgo.By("deleting the job", func() {
			gomega.Expect(k8sClient.Delete(ctx, job)).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), job)).Should(testing.BeNotFoundError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		// Users should not have to delete the workload
		// This is usually done by the garbage collector, but there is no garbage collection in integration test
		ginkgo.By("deleting the workload", func() {
			wl := &kueue.Workload{}
			wlKey := types.NamespacedName{Name: workloadraycluster.GetWorkloadNameForRayCluster(job.Name, job.UID), Namespace: job.Namespace}
			gomega.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Delete(ctx, wl)).Should(gomega.Succeed())
		})

		ginkgo.By("checking the second RayCluster starts when the first one was deleted")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job2), createdJob2)).Should(gomega.Succeed())
			g.Expect(createdJob2.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		gomega.Expect(createdJob2.Spec.HeadGroupSpec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(spotUntaintedFlavor.Name))
		gomega.Expect(createdJob2.Spec.WorkerGroupSpecs[0].Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(spotUntaintedFlavor.Name))
		util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
		util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
	})
})

var _ = ginkgo.Describe("Job controller with preemption enabled", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup())
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	var (
		ns             *corev1.Namespace
		onDemandFlavor *kueue.ResourceFlavor
		clusterQueue   *kueue.ClusterQueue
		localQueue     *kueue.LocalQueue
		priorityClass  *schedulingv1.PriorityClass
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-")

		onDemandFlavor = testing.MakeResourceFlavor("on-demand").NodeLabel(instanceKey, "on-demand").Obj()
		util.MustCreate(ctx, k8sClient, onDemandFlavor)

		clusterQueue = testing.MakeClusterQueue("clusterqueue").
			ResourceGroup(
				*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "4").Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj()
		util.MustCreate(ctx, k8sClient, clusterQueue)

		ginkgo.By("creating localQueue")
		localQueue = testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		util.MustCreate(ctx, k8sClient, localQueue)

		ginkgo.By("creating priority")
		priorityClass = testing.MakePriorityClass(priorityClassName).
			PriorityValue(priorityValue).Obj()
		util.MustCreate(ctx, k8sClient, priorityClass)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, priorityClass, true)
	})

	ginkgo.It("Should preempt lower priority RayClusters when resource insufficient", func() {
		ginkgo.By("Create a low priority RayCluster")
		lowPriorityJob := testingraycluster.MakeCluster("raycluster-with-low-priority", ns.Name).Queue(localQueue.Name).
			RequestHead(corev1.ResourceCPU, "1").
			RequestWorkerGroup(corev1.ResourceCPU, "2").
			Obj()
		util.MustCreate(ctx, k8sClient, lowPriorityJob)

		ginkgo.By("Await for the low priority workload to be admitted")
		createdJob := &rayv1.RayCluster{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lowPriorityJob), createdJob)).Should(gomega.Succeed())
			g.Expect(createdJob.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Create a high priority RayCluster which will preempt the lower one")
		highPriorityJob := testingraycluster.MakeCluster("raycluster-with-high-priority", ns.Name).Queue(localQueue.Name).
			RequestHead(corev1.ResourceCPU, "2").
			WithPriorityClassName(priorityClassName).
			RequestWorkerGroup(corev1.ResourceCPU, "2").
			Obj()
		util.MustCreate(ctx, k8sClient, highPriorityJob)

		ginkgo.By("High priority workload should be admitted")
		highPriorityWL := &kueue.Workload{}
		highPriorityLookupKey := types.NamespacedName{Name: workloadraycluster.GetWorkloadNameForRayCluster(highPriorityJob.Name, highPriorityJob.UID), Namespace: ns.Name}

		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, highPriorityLookupKey, highPriorityWL)).Should(gomega.Succeed())
			g.Expect(highPriorityWL.Status.Conditions).Should(testing.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Low priority workload should not be admitted")
		createdWorkload := &kueue.Workload{}
		lowPriorityLookupKey := types.NamespacedName{Name: workloadraycluster.GetWorkloadNameForRayCluster(lowPriorityJob.Name, lowPriorityJob.UID), Namespace: ns.Name}

		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, lowPriorityLookupKey, createdWorkload)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		apimeta.IsStatusConditionFalse(createdWorkload.Status.Conditions, kueue.WorkloadAdmitted)

		ginkgo.By("Low priority RayCluster should be suspended")
		createdJob = &rayv1.RayCluster{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lowPriorityJob), createdJob)).Should(gomega.Succeed())
			g.Expect(createdJob.Spec.Suspend).Should(gomega.Equal(ptr.To(true)))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Delete high priority raycluster")
		util.ExpectObjectToBeDeleted(ctx, k8sClient, highPriorityJob, true)
		// Manually delete workload because no garbage collection controller.
		util.ExpectObjectToBeDeleted(ctx, k8sClient, highPriorityWL, true)

		ginkgo.By("Low priority workload should be admitted again")
		createdWorkload = &kueue.Workload{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, lowPriorityLookupKey, createdWorkload)).Should(gomega.Succeed())
			g.Expect(createdWorkload.Status.Conditions).Should(testing.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Low priority RayCluster should be unsuspended")
		createdJob = &rayv1.RayCluster{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lowPriorityJob), createdJob)).Should(gomega.Succeed())
			g.Expect(createdJob.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})
})
