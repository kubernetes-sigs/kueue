/*
Copyright 2023 The Kubernetes Authors.

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

package rayjob

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
	workloadrayjob "sigs.k8s.io/kueue/pkg/controller/jobs/rayjob"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingrayjob "sigs.k8s.io/kueue/pkg/util/testingjobs/rayjob"
	"sigs.k8s.io/kueue/test/util"
)

const (
	jobName                 = "test-job"
	instanceKey             = "cloud.provider.com/instance"
	priorityClassName       = "test-priority-class"
	priorityValue     int32 = 10
)

func setInitStatus(name, namespace string) {
	createdJob := &rayv1.RayJob{}
	nsName := types.NamespacedName{Name: name, Namespace: namespace}
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, nsName, createdJob)).Should(gomega.Succeed())
		createdJob.Status.JobDeploymentStatus = rayv1.JobDeploymentStatusSuspended
		g.Expect(k8sClient.Status().Update(ctx, createdJob)).Should(gomega.Succeed())
	}, util.Timeout, util.Interval).Should(gomega.Succeed())
}

var _ = ginkgo.Describe("Job controller", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup(jobframework.WithManageJobsWithoutQueueName(true)))
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	var (
		ns *corev1.Namespace
	)
	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "core-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.It("Should reconcile RayJobs", func() {
		ginkgo.By("checking the job gets suspended when created unsuspended")
		priorityClass := testing.MakePriorityClass(priorityClassName).
			PriorityValue(priorityValue).Obj()
		gomega.Expect(k8sClient.Create(ctx, priorityClass)).Should(gomega.Succeed())
		defer func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, priorityClass, true)
		}()

		job := testingrayjob.MakeJob(jobName, ns.Name).
			Suspend(false).
			WithPriorityClassName(priorityClassName).
			Obj()
		err := k8sClient.Create(ctx, job)
		gomega.Expect(err).To(gomega.Succeed())
		createdJob := &rayv1.RayJob{}

		setInitStatus(jobName, ns.Name)
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: jobName, Namespace: ns.Name}, createdJob)).Should(gomega.Succeed())
			g.Expect(createdJob.Spec.Suspend).Should(gomega.BeTrue())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("checking the workload is created without queue assigned")
		createdWorkload := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadrayjob.GetWorkloadNameForRayJob(job.Name, job.UID), Namespace: ns.Name}
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
				Name:      workloadrayjob.GetWorkloadNameForRayJob("second-workload", "test-uid"),
				Namespace: createdWorkload.Namespace,
			},
			Spec: *createdWorkload.Spec.DeepCopy(),
		}
		gomega.Expect(ctrl.SetControllerReference(createdJob, secondWl, k8sClient.Scheme())).Should(gomega.Succeed())
		secondWl.Spec.PodSets[0].Count += 1

		gomega.Expect(k8sClient.Create(ctx, secondWl)).Should(gomega.Succeed())
		gomega.Eventually(func(g gomega.Gomega) {
			wl := &kueue.Workload{}
			key := types.NamespacedName{Name: secondWl.Name, Namespace: secondWl.Namespace}
			g.Expect(k8sClient.Get(ctx, key, wl)).Should(testing.BeNotFoundError())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		// check the original wl is still there
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("checking the job is unsuspended when workload is assigned")
		onDemandFlavor := testing.MakeResourceFlavor("on-demand").NodeLabel(instanceKey, "on-demand").Obj()
		gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).Should(gomega.Succeed())
		spotFlavor := testing.MakeResourceFlavor("spot").NodeLabel(instanceKey, "spot").Obj()
		gomega.Expect(k8sClient.Create(ctx, spotFlavor)).Should(gomega.Succeed())
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
			g.Expect(createdJob.Spec.Suspend).Should(gomega.BeFalse())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		gomega.Eventually(func(g gomega.Gomega) {
			ok, _ := testing.CheckEventRecordedFor(ctx, k8sClient, "Started", corev1.EventTypeNormal, fmt.Sprintf("Admitted by clusterQueue %v", clusterQueue.Name), lookupKey)
			g.Expect(ok).Should(gomega.BeTrue())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		gomega.Expect(createdJob.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.NodeSelector).Should(gomega.HaveLen(1))
		gomega.Expect(createdJob.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))
		gomega.Expect(createdJob.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.NodeSelector).Should(gomega.HaveLen(1))
		gomega.Expect(createdJob.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(spotFlavor.Name))
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
			g.Expect(createdWorkload.Status.Conditions).Should(testing.HaveConditionStatusTrue(kueue.WorkloadQuotaReserved))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("checking the job gets suspended when parallelism changes and the added node selectors are removed")
		parallelism := ptr.Deref(job.Spec.RayClusterSpec.WorkerGroupSpecs[0].Replicas, 1)
		newParallelism := parallelism + 1
		createdJob.Spec.RayClusterSpec.WorkerGroupSpecs[0].Replicas = &newParallelism
		gomega.Expect(k8sClient.Update(ctx, createdJob)).Should(gomega.Succeed())
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
			g.Expect(createdJob.Spec.Suspend).Should(gomega.BeTrue())
			g.Expect(createdJob.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.NodeSelector).Should(gomega.BeEmpty())
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
			g.Expect(createdJob.Spec.Suspend).Should(gomega.BeFalse())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		gomega.Expect(createdJob.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.NodeSelector).Should(gomega.HaveLen(1))
		gomega.Expect(createdJob.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))
		gomega.Expect(createdJob.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.NodeSelector).Should(gomega.HaveLen(1))
		gomega.Expect(createdJob.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(spotFlavor.Name))
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
			g.Expect(createdWorkload.Status.Conditions).Should(testing.HaveConditionStatusTrue(kueue.WorkloadQuotaReserved))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("checking the workload is finished when job is completed")
		createdJob.Status.JobDeploymentStatus = rayv1.JobDeploymentStatusComplete
		createdJob.Status.JobStatus = rayv1.JobStatusSucceeded
		createdJob.Status.Message = "Job finished by test"

		gomega.Expect(k8sClient.Status().Update(ctx, createdJob)).Should(gomega.Succeed())
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			g.Expect(createdWorkload.Status.Conditions).Should(testing.HaveConditionStatusTrue(kueue.WorkloadFinished))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})
})

var _ = ginkgo.Describe("Job controller for workloads when only jobs with queue are managed", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
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
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "core-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.It("Should reconcile jobs only when queue is set", func() {
		ginkgo.By("checking the workload is not created when queue name is not set")
		job := testingrayjob.MakeJob(jobName, ns.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
		lookupKey := types.NamespacedName{Name: jobName, Namespace: ns.Name}
		createdJob := &rayv1.RayJob{}
		setInitStatus(jobName, ns.Name)
		gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())

		createdWorkload := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadrayjob.GetWorkloadNameForRayJob(job.Name, job.UID), Namespace: ns.Name}
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
})

var _ = ginkgo.Describe("Job controller when waitForPodsReady enabled", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	type podsReadyTestSpec struct {
		beforeJobStatus *rayv1.RayJobStatus
		beforeCondition *metav1.Condition
		jobStatus       rayv1.RayJobStatus
		suspended       bool
		wantCondition   *metav1.Condition
	}

	var defaultFlavor = testing.MakeResourceFlavor("default").NodeLabel(instanceKey, "default").Obj()

	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup(jobframework.WithWaitForPodsReady(&configapi.WaitForPodsReady{Enable: true})))

		ginkgo.By("Create a resource flavor")
		gomega.Expect(k8sClient.Create(ctx, defaultFlavor)).Should(gomega.Succeed())
	})

	ginkgo.AfterAll(func() {
		util.ExpectObjectToBeDeleted(ctx, k8sClient, defaultFlavor, true)
		fwk.StopManager(ctx)
	})

	var (
		ns *corev1.Namespace
	)
	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "core-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.DescribeTable("Single job at different stages of progress towards completion",
		func(podsReadyTestSpec podsReadyTestSpec) {
			ginkgo.By("Create a job")
			job := testingrayjob.MakeJob(jobName, ns.Name).Obj()
			jobQueueName := "test-queue"
			job.Annotations = map[string]string{constants.QueueAnnotation: jobQueueName}
			gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
			lookupKey := types.NamespacedName{Name: jobName, Namespace: ns.Name}
			setInitStatus(jobName, ns.Name)
			createdJob := &rayv1.RayJob{}
			gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())

			ginkgo.By("Fetch the workload created for the job")
			createdWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: workloadrayjob.GetWorkloadNameForRayJob(job.Name, job.UID), Namespace: ns.Name}
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
				g.Expect(createdJob.Spec.Suspend).Should(gomega.BeFalse())
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
				Reason:  "PodsReady",
				Message: "Not all pods are ready or succeeded",
			},
		}),
		ginkgo.Entry("Running RayJob", podsReadyTestSpec{
			jobStatus: rayv1.RayJobStatus{
				JobDeploymentStatus: rayv1.JobDeploymentStatusRunning,
				RayClusterStatus: rayv1.RayClusterStatus{
					State: rayv1.Ready,
				},
			},
			wantCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionTrue,
				Reason:  "PodsReady",
				Message: "All pods were ready or succeeded since the workload admission",
			},
		}),
		ginkgo.Entry("Running RayJob; PodsReady=False before", podsReadyTestSpec{
			beforeCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionFalse,
				Reason:  "PodsReady",
				Message: "Not all pods are ready or succeeded",
			},
			jobStatus: rayv1.RayJobStatus{
				JobDeploymentStatus: rayv1.JobDeploymentStatusRunning,
				RayClusterStatus: rayv1.RayClusterStatus{
					State: rayv1.Ready,
				},
			},
			wantCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionTrue,
				Reason:  "PodsReady",
				Message: "All pods were ready or succeeded since the workload admission",
			},
		}),
		ginkgo.Entry("Job suspended; PodsReady=True before", podsReadyTestSpec{
			beforeJobStatus: &rayv1.RayJobStatus{
				JobDeploymentStatus: rayv1.JobDeploymentStatusRunning,
				RayClusterStatus: rayv1.RayClusterStatus{
					State: rayv1.Ready,
				},
			},
			beforeCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionTrue,
				Reason:  "PodsReady",
				Message: "All pods were ready or succeeded since the workload admission",
			},
			jobStatus: rayv1.RayJobStatus{
				JobDeploymentStatus: rayv1.JobDeploymentStatusSuspended,
			},
			suspended: true,
			wantCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionFalse,
				Reason:  "PodsReady",
				Message: "Not all pods are ready or succeeded",
			},
		}),
	)
})

var _ = ginkgo.Describe("Job controller interacting with scheduler", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
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
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "core-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

		onDemandFlavor = testing.MakeResourceFlavor("on-demand").NodeLabel(instanceKey, "on-demand").Obj()
		gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).Should(gomega.Succeed())

		spotUntaintedFlavor = testing.MakeResourceFlavor("spot-untainted").NodeLabel(instanceKey, "spot-untainted").Obj()
		gomega.Expect(k8sClient.Create(ctx, spotUntaintedFlavor)).Should(gomega.Succeed())

		clusterQueue = testing.MakeClusterQueue("dev-clusterqueue").
			ResourceGroup(
				*testing.MakeFlavorQuotas("spot-untainted").Resource(corev1.ResourceCPU, "5").Obj(),
				*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj(),
			).Obj()
		gomega.Expect(k8sClient.Create(ctx, clusterQueue)).Should(gomega.Succeed())
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
		gomega.Expect(k8sClient.Create(ctx, localQueue)).Should(gomega.Succeed())

		ginkgo.By("checking a dev job starts")
		job := testingrayjob.MakeJob("dev-job", ns.Name).Queue(localQueue.Name).
			RequestHead(corev1.ResourceCPU, "3").
			RequestWorkerGroup(corev1.ResourceCPU, "4").
			Obj()
		gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
		setInitStatus(job.Name, job.Namespace)
		createdJob := &rayv1.RayJob{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: job.Name, Namespace: job.Namespace}, createdJob)).
				Should(gomega.Succeed())
			g.Expect(createdJob.Spec.Suspend).Should(gomega.BeFalse())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		gomega.Expect(createdJob.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(spotUntaintedFlavor.Name))
		gomega.Expect(createdJob.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))
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
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "core-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

		onDemandFlavor = testing.MakeResourceFlavor("on-demand").NodeLabel(instanceKey, "on-demand").Obj()
		gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).Should(gomega.Succeed())

		clusterQueue = testing.MakeClusterQueue("clusterqueue").
			ResourceGroup(
				*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "4").Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, clusterQueue)).Should(gomega.Succeed())

		ginkgo.By("creating localQueue")
		localQueue = testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, localQueue)).Should(gomega.Succeed())

		ginkgo.By("creating priority")
		priorityClass = testing.MakePriorityClass(priorityClassName).
			PriorityValue(priorityValue).Obj()
		gomega.Expect(k8sClient.Create(ctx, priorityClass)).Should(gomega.Succeed())
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, priorityClass, true)
	})

	ginkgo.It("Should preempt lower priority rayJobs when resource insufficient", func() {
		ginkgo.By("Create a low priority rayJob")
		lowPriorityJob := testingrayjob.MakeJob("rayjob-with-low-priority", ns.Name).Queue(localQueue.Name).
			RequestHead(corev1.ResourceCPU, "1").
			RequestWorkerGroup(corev1.ResourceCPU, "2").
			Obj()
		gomega.Expect(k8sClient.Create(ctx, lowPriorityJob)).Should(gomega.Succeed())
		setInitStatus(lowPriorityJob.Name, lowPriorityJob.Namespace)

		ginkgo.By("Await for the low priority workload to be admitted")
		createdJob := &rayv1.RayJob{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lowPriorityJob), createdJob)).Should(gomega.Succeed())
			g.Expect(createdJob.Spec.Suspend).Should(gomega.BeFalse())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Create a high priority rayJob which will preempt the lower one")
		highPriorityJob := testingrayjob.MakeJob("rayjob-with-high-priority", ns.Name).Queue(localQueue.Name).
			RequestHead(corev1.ResourceCPU, "2").
			WithPriorityClassName(priorityClassName).
			RequestWorkerGroup(corev1.ResourceCPU, "2").
			Obj()
		gomega.Expect(k8sClient.Create(ctx, highPriorityJob)).Should(gomega.Succeed())
		setInitStatus(highPriorityJob.Name, highPriorityJob.Namespace)

		ginkgo.By("High priority workload should be admitted")
		highPriorityWL := &kueue.Workload{}
		highPriorityLookupKey := types.NamespacedName{Name: workloadrayjob.GetWorkloadNameForRayJob(highPriorityJob.Name, highPriorityJob.UID), Namespace: ns.Name}

		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, highPriorityLookupKey, highPriorityWL)).To(gomega.Succeed())
			g.Expect(highPriorityWL.Status.Conditions).To(testing.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Low priority workload should not be admitted")
		createdWorkload := &kueue.Workload{}
		lowPriorityLookupKey := types.NamespacedName{Name: workloadrayjob.GetWorkloadNameForRayJob(lowPriorityJob.Name, lowPriorityJob.UID), Namespace: ns.Name}

		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, lowPriorityLookupKey, createdWorkload)).To(gomega.Succeed())
			g.Expect(apimeta.IsStatusConditionFalse(createdWorkload.Status.Conditions, kueue.WorkloadAdmitted)).To(gomega.BeTrue())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Low priority rayJob should be suspended")
		createdJob = &rayv1.RayJob{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lowPriorityJob), createdJob)).Should(gomega.Succeed())
			g.Expect(createdJob.Spec.Suspend).Should(gomega.BeTrue())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Delete high priority rayjob")
		util.ExpectObjectToBeDeleted(ctx, k8sClient, highPriorityJob, true)
		// Manually delete workload because no garbage collection controller.
		util.ExpectObjectToBeDeleted(ctx, k8sClient, highPriorityWL, true)

		ginkgo.By("Low priority workload should be admitted again")
		createdWorkload = &kueue.Workload{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, lowPriorityLookupKey, createdWorkload)).To(gomega.Succeed())
			g.Expect(createdWorkload.Status.Conditions).To(testing.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Low priority rayJob should be unsuspended")
		createdJob = &rayv1.RayJob{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lowPriorityJob), createdJob)).Should(gomega.Succeed())
			g.Expect(createdJob.Spec.Suspend).Should(gomega.BeFalse())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})
})
