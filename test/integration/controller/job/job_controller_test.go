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

package job

import (
	"fmt"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/util/pointer"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

const (
	parallelism       = 4
	jobName           = "test-job"
	jobNamespace      = "default"
	labelKey          = "cloud.provider.com/instance"
	priorityClassName = "test-priority-class"
	priorityValue     = 10
)

var (
	wlLookupKey               = types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(jobName), Namespace: jobNamespace}
	ignoreConditionTimestamps = cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime")
)

// +kubebuilder:docs-gen:collapse=Imports

var _ = ginkgo.Describe("Job controller", func() {

	ginkgo.BeforeEach(func() {
		fwk = &framework.Framework{
			ManagerSetup: managerSetup(jobframework.WithManageJobsWithoutQueueName(true)),
			CRDPath:      crdPath,
		}
		ctx, cfg, k8sClient = fwk.Setup()
	})
	ginkgo.AfterEach(func() {
		fwk.Teardown()
	})
	ginkgo.It("Should reconcile workload and job for all jobs", func() {
		ginkgo.By("checking the job gets suspended when created unsuspended")
		priorityClass := testing.MakePriorityClass(priorityClassName).
			PriorityValue(int32(priorityValue)).Obj()
		gomega.Expect(k8sClient.Create(ctx, priorityClass)).Should(gomega.Succeed())
		job := testingjob.MakeJob(jobName, jobNamespace).PriorityClass(priorityClassName).Obj()
		gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
		lookupKey := types.NamespacedName{Name: jobName, Namespace: jobNamespace}
		createdJob := &batchv1.Job{}
		gomega.Eventually(func() bool {
			if err := k8sClient.Get(ctx, lookupKey, createdJob); err != nil {
				return false
			}
			return createdJob.Spec.Suspend != nil && *createdJob.Spec.Suspend
		}, util.Timeout, util.Interval).Should(gomega.BeTrue())

		ginkgo.By("checking the workload is created without queue assigned")
		createdWorkload := &kueue.Workload{}
		gomega.Eventually(func() bool {
			err := k8sClient.Get(ctx, wlLookupKey, createdWorkload)
			return err == nil
		}, util.Timeout, util.Interval).Should(gomega.BeTrue())
		gomega.Expect(createdWorkload.Spec.QueueName).Should(gomega.Equal(""), "The Workload shouldn't have .spec.queueName set")
		gomega.Expect(metav1.IsControlledBy(createdWorkload, job)).To(gomega.BeTrue(), "The Workload should be owned by the Job")

		ginkgo.By("checking the workload is created with priority and priorityName")
		gomega.Expect(createdWorkload.Spec.PriorityClassName).Should(gomega.Equal(priorityClassName))
		gomega.Expect(*createdWorkload.Spec.Priority).Should(gomega.Equal(int32(priorityValue)))

		ginkgo.By("checking the workload is updated with queue name when the job does")
		jobQueueName := "test-queue"
		createdJob.Annotations = map[string]string{jobframework.QueueAnnotation: jobQueueName}
		gomega.Expect(k8sClient.Update(ctx, createdJob)).Should(gomega.Succeed())
		gomega.Eventually(func() bool {
			if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
				return false
			}
			return createdWorkload.Spec.QueueName == jobQueueName
		}, util.Timeout, util.Interval).Should(gomega.BeTrue())

		ginkgo.By("checking a second non-matching workload is deleted")
		secondWl := &kueue.Workload{
			ObjectMeta: metav1.ObjectMeta{
				Name:      workloadjob.GetWorkloadNameForJob("second-workload"),
				Namespace: createdWorkload.Namespace,
			},
			Spec: *createdWorkload.Spec.DeepCopy(),
		}
		gomega.Expect(ctrl.SetControllerReference(createdJob, secondWl, scheme.Scheme)).Should(gomega.Succeed())
		secondWl.Spec.PodSets[0].Count += 1
		gomega.Expect(k8sClient.Create(ctx, secondWl)).Should(gomega.Succeed())
		gomega.Eventually(func() error {
			wl := &kueue.Workload{}
			key := types.NamespacedName{Name: secondWl.Name, Namespace: secondWl.Namespace}
			return k8sClient.Get(ctx, key, wl)
		}, util.Timeout, util.Interval).Should(testing.BeNotFoundError())
		// check the original wl is still there
		gomega.Consistently(func() bool {
			err := k8sClient.Get(ctx, wlLookupKey, createdWorkload)
			return err == nil
		}, util.ConsistentDuration, util.Interval).Should(gomega.BeTrue())
		gomega.Eventually(func() bool {
			ok, _ := testing.CheckLatestEvent(ctx, k8sClient, "DeletedWorkload", corev1.EventTypeNormal, fmt.Sprintf("Deleted not matching Workload: %v", workload.Key(secondWl)))
			return ok
		}, util.Timeout, util.Interval).Should(gomega.BeTrue())

		ginkgo.By("checking the job is unsuspended when workload is assigned")
		onDemandFlavor := testing.MakeResourceFlavor("on-demand").Label(labelKey, "on-demand").Obj()
		gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).Should(gomega.Succeed())
		spotFlavor := testing.MakeResourceFlavor("spot").Label(labelKey, "spot").Obj()
		gomega.Expect(k8sClient.Create(ctx, spotFlavor)).Should(gomega.Succeed())
		clusterQueue := testing.MakeClusterQueue("cluster-queue").
			ResourceGroup(
				*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj(),
				*testing.MakeFlavorQuotas("spot").Resource(corev1.ResourceCPU, "5").Obj(),
			).Obj()
		createdWorkload.Status.Admission = &kueue.Admission{
			ClusterQueue: kueue.ClusterQueueReference(clusterQueue.Name),
			PodSetFlavors: []kueue.PodSetFlavors{{
				Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
					corev1.ResourceCPU: "on-demand",
				},
			}},
		}
		gomega.Expect(k8sClient.Status().Update(ctx, createdWorkload)).Should(gomega.Succeed())
		gomega.Eventually(func() bool {
			if err := k8sClient.Get(ctx, lookupKey, createdJob); err != nil {
				return false
			}
			return !*createdJob.Spec.Suspend
		}, util.Timeout, util.Interval).Should(gomega.BeTrue())
		gomega.Eventually(func() bool {
			ok, _ := testing.CheckLatestEvent(ctx, k8sClient, "Started", corev1.EventTypeNormal, fmt.Sprintf("Admitted by clusterQueue %v", clusterQueue.Name))
			return ok
		}, util.Timeout, util.Interval).Should(gomega.BeTrue())
		gomega.Expect(len(createdJob.Spec.Template.Spec.NodeSelector)).Should(gomega.Equal(1))
		gomega.Expect(createdJob.Spec.Template.Spec.NodeSelector[labelKey]).Should(gomega.Equal(onDemandFlavor.Name))
		gomega.Consistently(func() bool {
			if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
				return false
			}
			return len(createdWorkload.Status.Conditions) == 0
		}, util.ConsistentDuration, util.Interval).Should(gomega.BeTrue())

		ginkgo.By("checking the job gets suspended when parallelism changes and the added node selectors are removed")
		newParallelism := int32(parallelism + 1)
		createdJob.Spec.Parallelism = &newParallelism
		gomega.Expect(k8sClient.Update(ctx, createdJob)).Should(gomega.Succeed())
		gomega.Eventually(func() bool {
			if err := k8sClient.Get(ctx, lookupKey, createdJob); err != nil {
				return false
			}
			return createdJob.Spec.Suspend != nil && *createdJob.Spec.Suspend &&
				len(createdJob.Spec.Template.Spec.NodeSelector) == 0
		}, util.Timeout, util.Interval).Should(gomega.BeTrue())
		gomega.Eventually(func() bool {
			ok, _ := testing.CheckLatestEvent(ctx, k8sClient, "DeletedWorkload", corev1.EventTypeNormal, fmt.Sprintf("Deleted not matching Workload: %v", wlLookupKey.String()))
			return ok
		}, util.Timeout, util.Interval).Should(gomega.BeTrue())

		ginkgo.By("checking the workload is updated with new count")
		gomega.Eventually(func() bool {
			if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
				return false
			}
			return createdWorkload.Spec.PodSets[0].Count == newParallelism
		}, util.Timeout, util.Interval).Should(gomega.BeTrue())
		gomega.Expect(createdWorkload.Status.Admission).Should(gomega.BeNil())

		ginkgo.By("checking the job is unsuspended and selectors added when workload is assigned again")
		createdWorkload.Status.Admission = &kueue.Admission{
			ClusterQueue: kueue.ClusterQueueReference(clusterQueue.Name),
			PodSetFlavors: []kueue.PodSetFlavors{{
				Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
					corev1.ResourceCPU: "spot",
				},
			}},
		}
		gomega.Expect(k8sClient.Status().Update(ctx, createdWorkload)).Should(gomega.Succeed())
		gomega.Eventually(func() bool {
			if err := k8sClient.Get(ctx, lookupKey, createdJob); err != nil {
				return false
			}
			return !*createdJob.Spec.Suspend
		}, util.Timeout, util.Interval).Should(gomega.BeTrue())
		gomega.Expect(len(createdJob.Spec.Template.Spec.NodeSelector)).Should(gomega.Equal(1))
		gomega.Expect(createdJob.Spec.Template.Spec.NodeSelector[labelKey]).Should(gomega.Equal(spotFlavor.Name))
		gomega.Consistently(func() bool {
			if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
				return false
			}
			return len(createdWorkload.Status.Conditions) == 0
		}, util.ConsistentDuration, util.Interval).Should(gomega.BeTrue())

		ginkgo.By("checking the workload is finished when job is completed")
		createdJob.Status.Conditions = append(createdJob.Status.Conditions,
			batchv1.JobCondition{
				Type:               batchv1.JobComplete,
				Status:             corev1.ConditionTrue,
				LastProbeTime:      metav1.Now(),
				LastTransitionTime: metav1.Now(),
			})
		gomega.Expect(k8sClient.Status().Update(ctx, createdJob)).Should(gomega.Succeed())
		gomega.Eventually(func() bool {
			err := k8sClient.Get(ctx, wlLookupKey, createdWorkload)
			if err != nil || len(createdWorkload.Status.Conditions) == 0 {
				return false
			}

			return createdWorkload.Status.Conditions[0].Type == kueue.WorkloadFinished &&
				createdWorkload.Status.Conditions[0].Status == metav1.ConditionTrue
		}, util.Timeout, util.Interval).Should(gomega.BeTrue())
	})

	ginkgo.It("Should reconcile job when queueName set by annotation (deprecated)", func() {
		ginkgo.By("checking the workload is created with correct queue name assigned")
		jobQueueName := "test-queue"
		job := testingjob.MakeJob(jobName, jobNamespace).QueueNameAnnotation("test-queue").Obj()
		gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
		createdWorkload := &kueue.Workload{}
		gomega.Eventually(func() error {
			return k8sClient.Get(ctx, wlLookupKey, createdWorkload)
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		gomega.Expect(createdWorkload.Spec.QueueName).Should(gomega.Equal(jobQueueName))
	})

	ginkgo.When("The parent-workload annotation is used", func() {

		var (
			parentJobName     = jobName + "-parent"
			parentWlLookupKey = types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(parentJobName), Namespace: jobNamespace}
			childJobName      = jobName + "-child"
			childLookupKey    = types.NamespacedName{Name: childJobName, Namespace: jobNamespace}
			childWlLookupKey  = types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(childJobName), Namespace: jobNamespace}
		)

		ginkgo.It("Should suspend a job if the parent workload does not exist", func() {
			ginkgo.By("Creating the child job which uses the parent workload annotation")
			childJob := testingjob.MakeJob(childJobName, jobNamespace).Suspend(false).ParentWorkload("non-existing-parent-workload").Obj()
			gomega.Expect(k8sClient.Create(ctx, childJob)).Should(gomega.Succeed())

			ginkgo.By("checking that the child job is suspended")
			gomega.Eventually(func() *bool {
				gomega.Expect(k8sClient.Get(ctx, childLookupKey, childJob)).Should(gomega.Succeed())
				return childJob.Spec.Suspend
			}, util.Timeout, util.Interval).Should(gomega.Equal(pointer.Bool(true)))
		})

		ginkgo.It("Should not create child workload for a job with parent-workload annotation", func() {
			ginkgo.By("creating the parent job")
			parentJob := testingjob.MakeJob(parentJobName, jobNamespace).Obj()
			gomega.Expect(k8sClient.Create(ctx, parentJob)).Should(gomega.Succeed())

			ginkgo.By("waiting for the parent workload to be created")
			parentWorkload := &kueue.Workload{}
			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, parentWlLookupKey, parentWorkload)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Creating the child job which uses the parent workload annotation")
			childJob := testingjob.MakeJob(childJobName, jobNamespace).ParentWorkload(parentWorkload.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, childJob)).Should(gomega.Succeed())

			ginkgo.By("Checking that the child workload is not created")
			childWorkload := &kueue.Workload{}
			gomega.Consistently(func() bool {
				return apierrors.IsNotFound(k8sClient.Get(ctx, childWlLookupKey, childWorkload))
			}, util.ConsistentDuration, util.Interval).Should(gomega.BeTrue())
		})

		ginkgo.It("Should change the suspension status of the child job when the parent workload is admitted or unadmitted", func() {
			ginkgo.By("Create a resource flavor")
			defaultFlavor := testing.MakeResourceFlavor("default").Label(labelKey, "default").Obj()
			gomega.Expect(k8sClient.Create(ctx, defaultFlavor)).Should(gomega.Succeed())

			ginkgo.By("creating the parent job")
			parentJob := testingjob.MakeJob(parentJobName, jobNamespace).Obj()
			gomega.Expect(k8sClient.Create(ctx, parentJob)).Should(gomega.Succeed())

			ginkgo.By("Creating the child job with the parent-workload annotation")
			childJob := testingjob.MakeJob(childJobName, jobNamespace).ParentWorkload(parentWlLookupKey.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, childJob)).Should(gomega.Succeed())

			ginkgo.By("waiting for the parent workload to be created")
			parentWorkload := &kueue.Workload{}
			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, parentWlLookupKey, parentWorkload)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("admit the parent workload")
			parentWorkload.Status.Admission = &kueue.Admission{
				ClusterQueue: kueue.ClusterQueueReference("foo"),
				PodSetFlavors: []kueue.PodSetFlavors{{
					Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
						corev1.ResourceCPU: "default",
					},
				}},
			}
			gomega.Eventually(func() error {
				return k8sClient.Status().Update(ctx, parentWorkload)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("checking that the child job is unsuspended")
			gomega.Eventually(func() bool {
				gomega.Expect(k8sClient.Get(ctx, childLookupKey, childJob)).Should(gomega.Succeed())
				return childJob.Spec.Suspend != nil && *childJob.Spec.Suspend
			}, util.Timeout, util.Interval).Should(gomega.BeFalse())

			ginkgo.By("Unset admission of the workload to suspend the job")
			gomega.Eventually(func() error {
				if err := k8sClient.Get(ctx, parentWlLookupKey, parentWorkload); err != nil {
					return err
				}
				parentWorkload.Status.Admission = nil
				return k8sClient.Status().Update(ctx, parentWorkload)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("checking that the child job is suspended")
			gomega.Eventually(func() *bool {
				gomega.Expect(k8sClient.Get(ctx, childLookupKey, childJob)).Should(gomega.Succeed())
				return childJob.Spec.Suspend
			}, util.Timeout, util.Interval).Should(gomega.Equal(pointer.Bool(true)))
		})
	})
})

var _ = ginkgo.Describe("Job controller for workloads with no queue set", func() {
	ginkgo.BeforeEach(func() {
		fwk = &framework.Framework{
			ManagerSetup: managerSetup(),
			CRDPath:      crdPath,
		}
		ctx, cfg, k8sClient = fwk.Setup()
	})
	ginkgo.AfterEach(func() {
		fwk.Teardown()
	})
	ginkgo.It("Should reconcile jobs only when queue is set", func() {
		ginkgo.By("checking the workload is not created when queue name is not set")
		job := testingjob.MakeJob(jobName, jobNamespace).Obj()
		gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
		lookupKey := types.NamespacedName{Name: jobName, Namespace: jobNamespace}
		createdJob := &batchv1.Job{}
		gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())

		createdWorkload := &kueue.Workload{}
		gomega.Consistently(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, wlLookupKey, createdWorkload))
		}, util.ConsistentDuration, util.Interval).Should(gomega.BeTrue())

		ginkgo.By("checking the workload is created when queue name is set")
		jobQueueName := "test-queue"
		createdJob.Annotations = map[string]string{jobframework.QueueAnnotation: jobQueueName}
		gomega.Expect(k8sClient.Update(ctx, createdJob)).Should(gomega.Succeed())
		gomega.Eventually(func() error {
			return k8sClient.Get(ctx, wlLookupKey, createdWorkload)
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})
	ginkgo.When("The parent-workload annotation is used", func() {

		var (
			childJobName   = jobName + "-child"
			childLookupKey = types.NamespacedName{Name: childJobName, Namespace: jobNamespace}
		)

		ginkgo.It("Should suspend a job if the parent workload does not exist", func() {
			ginkgo.By("Creating the child job which uses the parent workload annotation")
			childJob := testingjob.MakeJob(childJobName, jobNamespace).Suspend(false).ParentWorkload("non-existing-parent-workload").Obj()
			gomega.Expect(k8sClient.Create(ctx, childJob)).Should(gomega.Succeed())

			ginkgo.By("checking that the child job is suspended")
			gomega.Eventually(func() *bool {
				gomega.Expect(k8sClient.Get(ctx, childLookupKey, childJob)).Should(gomega.Succeed())
				return childJob.Spec.Suspend
			}, util.Timeout, util.Interval).Should(gomega.Equal(pointer.Bool(true)))
		})

	})
})

var _ = ginkgo.Describe("Job controller when waitForPodsReady enabled", func() {
	type podsReadyTestSpec struct {
		beforeJobStatus *batchv1.JobStatus
		beforeCondition *metav1.Condition
		jobStatus       batchv1.JobStatus
		suspended       bool
		wantCondition   *metav1.Condition
	}

	ginkgo.BeforeEach(func() {
		fwk = &framework.Framework{
			ManagerSetup: managerSetup(jobframework.WithWaitForPodsReady(true)),
			CRDPath:      crdPath,
		}
		ctx, cfg, k8sClient = fwk.Setup()
	})
	ginkgo.AfterEach(func() {
		fwk.Teardown()
	})

	ginkgo.DescribeTable("Single job at different stages of progress towards completion",
		func(podsReadyTestSpec podsReadyTestSpec) {
			ginkgo.By("Create a resource flavor")
			defaultFlavor := testing.MakeResourceFlavor("default").Label(labelKey, "default").Obj()
			gomega.Expect(k8sClient.Create(ctx, defaultFlavor)).Should(gomega.Succeed())

			ginkgo.By("Create a job")
			job := testingjob.MakeJob(jobName, jobNamespace).Parallelism(2).Obj()
			jobQueueName := "test-queue"
			job.Annotations = map[string]string{jobframework.QueueAnnotation: jobQueueName}
			gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
			lookupKey := types.NamespacedName{Name: jobName, Namespace: jobNamespace}
			createdJob := &batchv1.Job{}
			gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())

			ginkgo.By("Fetch the workload created for the job")
			createdWorkload := &kueue.Workload{}
			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, wlLookupKey, createdWorkload)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Admit the workload created for the job")
			createdWorkload.Status.Admission = &kueue.Admission{
				ClusterQueue: kueue.ClusterQueueReference("foo"),
				PodSetFlavors: []kueue.PodSetFlavors{{
					Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
						corev1.ResourceCPU: "default",
					},
				}},
			}
			gomega.Expect(k8sClient.Status().Update(ctx, createdWorkload)).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())

			ginkgo.By("Await for the job to be unsuspended")
			gomega.Eventually(func() *bool {
				gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
				return createdJob.Spec.Suspend
			}, util.Timeout, util.Interval).Should(gomega.Equal(pointer.Bool(false)))

			if podsReadyTestSpec.beforeJobStatus != nil {
				ginkgo.By("Update the job status to simulate its initial progress towards completion")
				createdJob.Status = *podsReadyTestSpec.beforeJobStatus
				gomega.Expect(k8sClient.Status().Update(ctx, createdJob)).Should(gomega.Succeed())
				gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
			}

			if podsReadyTestSpec.beforeCondition != nil {
				ginkgo.By("Update the workload status")
				gomega.Eventually(func() *metav1.Condition {
					gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					return apimeta.FindStatusCondition(createdWorkload.Status.Conditions, kueue.WorkloadPodsReady)
				}, util.Timeout, util.Interval).Should(gomega.BeComparableTo(podsReadyTestSpec.beforeCondition, ignoreConditionTimestamps))
			}

			ginkgo.By("Update the job status to simulate its progress towards completion")
			createdJob.Status = podsReadyTestSpec.jobStatus
			gomega.Expect(k8sClient.Status().Update(ctx, createdJob)).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())

			if podsReadyTestSpec.suspended {
				ginkgo.By("Unset admission of the workload to suspend the job")
				gomega.Eventually(func() error {
					// the update may need to be retried due to a conflict as the workload gets
					// also updated due to setting of the job status.
					if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
						return err
					}
					createdWorkload.Status.Admission = nil
					return k8sClient.Status().Update(ctx, createdWorkload)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			}

			ginkgo.By("Verify the PodsReady condition is added")
			gomega.Eventually(func() *metav1.Condition {
				gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				return apimeta.FindStatusCondition(createdWorkload.Status.Conditions, kueue.WorkloadPodsReady)
			}, util.Timeout, util.Interval).Should(gomega.BeComparableTo(podsReadyTestSpec.wantCondition, ignoreConditionTimestamps))
		},
		ginkgo.Entry("No progress", podsReadyTestSpec{
			wantCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionFalse,
				Reason:  "PodsReady",
				Message: "Not all pods are ready or succeeded",
			},
		}),
		ginkgo.Entry("Single pod ready", podsReadyTestSpec{
			jobStatus: batchv1.JobStatus{
				Ready: pointer.Int32(1),
			},
			wantCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionFalse,
				Reason:  "PodsReady",
				Message: "Not all pods are ready or succeeded",
			},
		}),
		ginkgo.Entry("Single pod succeeded", podsReadyTestSpec{
			jobStatus: batchv1.JobStatus{
				Succeeded: 1,
			},
			wantCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionFalse,
				Reason:  "PodsReady",
				Message: "Not all pods are ready or succeeded",
			},
		}),
		ginkgo.Entry("All pods are ready", podsReadyTestSpec{
			jobStatus: batchv1.JobStatus{
				Ready: pointer.Int32(2),
			},
			wantCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionTrue,
				Reason:  "PodsReady",
				Message: "All pods were ready or succeeded since the workload admission",
			},
		}),
		ginkgo.Entry("One pod ready, one succeeded", podsReadyTestSpec{
			jobStatus: batchv1.JobStatus{
				Ready:     pointer.Int32(1),
				Succeeded: 1,
			},
			wantCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionTrue,
				Reason:  "PodsReady",
				Message: "All pods were ready or succeeded since the workload admission",
			},
		}),
		ginkgo.Entry("All pods are succeeded", podsReadyTestSpec{
			jobStatus: batchv1.JobStatus{
				Ready:     pointer.Int32(0),
				Succeeded: 2,
			},
			wantCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionTrue,
				Reason:  "PodsReady",
				Message: "All pods were ready or succeeded since the workload admission",
			},
		}),
		ginkgo.Entry("All pods are succeeded; PodsReady=False before", podsReadyTestSpec{
			beforeCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionFalse,
				Reason:  "PodsReady",
				Message: "Not all pods are ready or succeeded",
			},
			jobStatus: batchv1.JobStatus{
				Ready:     pointer.Int32(0),
				Succeeded: 2,
			},
			wantCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionTrue,
				Reason:  "PodsReady",
				Message: "All pods were ready or succeeded since the workload admission",
			},
		}),
		ginkgo.Entry("One ready pod, one failed; PodsReady=True before", podsReadyTestSpec{
			beforeJobStatus: &batchv1.JobStatus{
				Ready: pointer.Int32(2),
			},
			beforeCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionTrue,
				Reason:  "PodsReady",
				Message: "All pods were ready or succeeded since the workload admission",
			},
			jobStatus: batchv1.JobStatus{
				Ready:  pointer.Int32(1),
				Failed: 1,
			},
			wantCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionTrue,
				Reason:  "PodsReady",
				Message: "All pods were ready or succeeded since the workload admission",
			},
		}),
		ginkgo.Entry("Job suspended without ready pods; but PodsReady=True before", podsReadyTestSpec{
			beforeJobStatus: &batchv1.JobStatus{
				Ready: pointer.Int32(2),
			},
			beforeCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionTrue,
				Reason:  "PodsReady",
				Message: "All pods were ready or succeeded since the workload admission",
			},
			jobStatus: batchv1.JobStatus{
				Failed: 2,
			},
			suspended: true,
			wantCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionFalse,
				Reason:  "PodsReady",
				Message: "Not all pods are ready or succeeded",
			},
		}),
		ginkgo.Entry("Job suspended with all pods ready; PodsReady=True before", podsReadyTestSpec{
			beforeJobStatus: &batchv1.JobStatus{
				Ready: pointer.Int32(2),
			},
			beforeCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionTrue,
				Reason:  "PodsReady",
				Message: "All pods were ready or succeeded since the workload admission",
			},
			jobStatus: batchv1.JobStatus{
				Ready: pointer.Int32(2),
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

var _ = ginkgo.Describe("Job controller interacting with scheduler", func() {
	const (
		instanceKey = "cloud.provider.com/instance"
	)

	var (
		ns                  *corev1.Namespace
		onDemandFlavor      *kueue.ResourceFlavor
		spotTaintedFlavor   *kueue.ResourceFlavor
		spotUntaintedFlavor *kueue.ResourceFlavor
		prodClusterQ        *kueue.ClusterQueue
		devClusterQ         *kueue.ClusterQueue
		prodLocalQ          *kueue.LocalQueue
		devLocalQ           *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		fwk = &framework.Framework{
			ManagerSetup: managerAndSchedulerSetup(),
			CRDPath:      crdPath,
		}
		ctx, cfg, k8sClient = fwk.Setup()

		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "core-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

		onDemandFlavor = testing.MakeResourceFlavor("on-demand").Label(instanceKey, "on-demand").Obj()
		gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).Should(gomega.Succeed())

		spotTaintedFlavor = testing.MakeResourceFlavor("spot-tainted").
			Label(instanceKey, "spot-tainted").
			Taint(corev1.Taint{
				Key:    instanceKey,
				Value:  "spot-tainted",
				Effect: corev1.TaintEffectNoSchedule,
			}).Obj()
		gomega.Expect(k8sClient.Create(ctx, spotTaintedFlavor)).Should(gomega.Succeed())

		spotUntaintedFlavor = testing.MakeResourceFlavor("spot-untainted").Label(instanceKey, "spot-untainted").Obj()
		gomega.Expect(k8sClient.Create(ctx, spotUntaintedFlavor)).Should(gomega.Succeed())

		prodClusterQ = testing.MakeClusterQueue("prod-cq").
			Cohort("prod").
			ResourceGroup(
				*testing.MakeFlavorQuotas("spot-tainted").Resource(corev1.ResourceCPU, "5", "0").Obj(),
				*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj(),
			).Obj()
		gomega.Expect(k8sClient.Create(ctx, prodClusterQ)).Should(gomega.Succeed())

		devClusterQ = testing.MakeClusterQueue("dev-clusterqueue").
			ResourceGroup(
				*testing.MakeFlavorQuotas("spot-untainted").Resource(corev1.ResourceCPU, "5").Obj(),
				*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj(),
			).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, devClusterQ)).Should(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, prodClusterQ, true)
		util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, devClusterQ, true)
		util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		gomega.Expect(util.DeleteResourceFlavor(ctx, k8sClient, spotTaintedFlavor)).To(gomega.Succeed())
		gomega.Expect(util.DeleteResourceFlavor(ctx, k8sClient, spotUntaintedFlavor)).To(gomega.Succeed())

		fwk.Teardown()
	})

	ginkgo.It("Should schedule jobs as they fit in their ClusterQueue", func() {
		ginkgo.By("creating localQueues")
		prodLocalQ = testing.MakeLocalQueue("prod-queue", ns.Name).ClusterQueue(prodClusterQ.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, prodLocalQ)).Should(gomega.Succeed())
		devLocalQ = testing.MakeLocalQueue("dev-queue", ns.Name).ClusterQueue(devClusterQ.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, devLocalQ)).Should(gomega.Succeed())

		ginkgo.By("checking the first prod job starts")
		prodJob1 := testingjob.MakeJob("prod-job1", ns.Name).Queue(prodLocalQ.Name).Request(corev1.ResourceCPU, "2").Obj()
		gomega.Expect(k8sClient.Create(ctx, prodJob1)).Should(gomega.Succeed())
		lookupKey1 := types.NamespacedName{Name: prodJob1.Name, Namespace: prodJob1.Namespace}
		createdProdJob1 := &batchv1.Job{}
		gomega.Eventually(func() *bool {
			gomega.Expect(k8sClient.Get(ctx, lookupKey1, createdProdJob1)).Should(gomega.Succeed())
			return createdProdJob1.Spec.Suspend
		}, util.Timeout, util.Interval).Should(gomega.Equal(pointer.Bool(false)))
		gomega.Expect(createdProdJob1.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))
		util.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 0)
		util.ExpectAdmittedActiveWorkloadsMetric(prodClusterQ, 1)

		ginkgo.By("checking a second no-fit prod job does not start")
		prodJob2 := testingjob.MakeJob("prod-job2", ns.Name).Queue(prodLocalQ.Name).Request(corev1.ResourceCPU, "5").Obj()
		gomega.Expect(k8sClient.Create(ctx, prodJob2)).Should(gomega.Succeed())
		lookupKey2 := types.NamespacedName{Name: prodJob2.Name, Namespace: prodJob2.Namespace}
		createdProdJob2 := &batchv1.Job{}
		gomega.Consistently(func() *bool {
			gomega.Expect(k8sClient.Get(ctx, lookupKey2, createdProdJob2)).Should(gomega.Succeed())
			return createdProdJob2.Spec.Suspend
		}, util.ConsistentDuration, util.Interval).Should(gomega.Equal(pointer.Bool(true)))
		util.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 1)
		util.ExpectAdmittedActiveWorkloadsMetric(prodClusterQ, 1)

		ginkgo.By("checking a dev job starts")
		devJob := testingjob.MakeJob("dev-job", ns.Name).Queue(devLocalQ.Name).Request(corev1.ResourceCPU, "5").Obj()
		gomega.Expect(k8sClient.Create(ctx, devJob)).Should(gomega.Succeed())
		createdDevJob := &batchv1.Job{}
		gomega.Eventually(func() *bool {
			gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: devJob.Name, Namespace: devJob.Namespace}, createdDevJob)).
				Should(gomega.Succeed())
			return createdDevJob.Spec.Suspend
		}, util.Timeout, util.Interval).Should(gomega.Equal(pointer.Bool(false)))
		gomega.Expect(createdDevJob.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(spotUntaintedFlavor.Name))
		util.ExpectPendingWorkloadsMetric(devClusterQ, 0, 0)
		util.ExpectAdmittedActiveWorkloadsMetric(devClusterQ, 1)

		ginkgo.By("checking the second prod job starts when the first finishes")
		createdProdJob1.Status.Conditions = append(createdProdJob1.Status.Conditions,
			batchv1.JobCondition{
				Type:               batchv1.JobComplete,
				Status:             corev1.ConditionTrue,
				LastProbeTime:      metav1.Now(),
				LastTransitionTime: metav1.Now(),
			})
		gomega.Expect(k8sClient.Status().Update(ctx, createdProdJob1)).Should(gomega.Succeed())
		gomega.Eventually(func() *bool {
			gomega.Expect(k8sClient.Get(ctx, lookupKey2, createdProdJob2)).Should(gomega.Succeed())
			return createdProdJob2.Spec.Suspend
		}, util.Timeout, util.Interval).Should(gomega.Equal(pointer.Bool(false)))
		gomega.Expect(createdProdJob2.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))
		util.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 0)
		util.ExpectAdmittedActiveWorkloadsMetric(prodClusterQ, 1)
	})

	ginkgo.It("Should unsuspend job iff localQueue is in the same namespace", func() {
		ginkgo.By("create another namespace")
		ns2 := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "e2e-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns2)).To(gomega.Succeed())
		defer func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns2)).To(gomega.Succeed())
		}()

		ginkgo.By("create a localQueue located in a different namespace as the job")
		localQueue := testing.MakeLocalQueue("local-queue", ns2.Name).Obj()
		localQueue.Spec.ClusterQueue = kueue.ClusterQueueReference(prodClusterQ.Name)

		ginkgo.By("create a job")
		prodJob := testingjob.MakeJob("prod-job", ns.Name).Queue(localQueue.Name).Request(corev1.ResourceCPU, "2").Obj()
		gomega.Expect(k8sClient.Create(ctx, prodJob)).Should(gomega.Succeed())

		ginkgo.By("job should be suspend")
		lookupKey := types.NamespacedName{Name: prodJob.Name, Namespace: prodJob.Namespace}
		createdProdJob := &batchv1.Job{}
		gomega.Eventually(func() *bool {
			gomega.Expect(k8sClient.Get(ctx, lookupKey, createdProdJob)).Should(gomega.Succeed())
			return createdProdJob.Spec.Suspend
		}, util.Timeout, util.Interval).Should(gomega.Equal(pointer.Bool(true)))

		ginkgo.By("creating another localQueue of the same name and in the same namespace as the job")
		prodLocalQ = testing.MakeLocalQueue(localQueue.Name, ns.Name).ClusterQueue(prodClusterQ.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, prodLocalQ)).Should(gomega.Succeed())

		ginkgo.By("job should be unsuspended")
		gomega.Eventually(func() *bool {
			gomega.Expect(k8sClient.Get(ctx, lookupKey, createdProdJob)).Should(gomega.Succeed())
			return createdProdJob.Spec.Suspend
		}, util.Timeout, util.Interval).Should(gomega.Equal(pointer.Bool(false)))
	})
})
