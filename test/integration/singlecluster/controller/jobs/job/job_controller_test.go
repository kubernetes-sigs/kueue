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

package job

import (
	"fmt"
	"maps"
	"strconv"
	"time"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

const (
	parallelism       = 4
	jobName           = "test-job"
	instanceKey       = "cloud.provider.com/instance"
	priorityClassName = "test-priority-class"
	priorityValue     = 10
	highPriorityValue = 20
	parentJobName     = jobName + "-parent"
	childJobName      = jobName + "-child"
)

var _ = ginkgo.Describe("Job controller", ginkgo.Label("job:batch", "area:jobs"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup(
			jobframework.WithManageJobsWithoutQueueName(true),
			jobframework.WithManagedJobsNamespaceSelector(util.NewNamespaceSelectorExcluding("unmanaged-ns")),
			jobframework.WithLabelKeysToCopy([]string{"toCopyKey"}),
		))
		unmanagedNamespace := utiltesting.MakeNamespace("unmanaged-ns")
		util.MustCreate(ctx, k8sClient, unmanagedNamespace)
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	var (
		ns             *corev1.Namespace
		childLookupKey types.NamespacedName
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-")
		childLookupKey = types.NamespacedName{Name: childJobName, Namespace: ns.Name}
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.It("Should reconcile workload and job for all jobs", framework.SlowSpec, func() {
		ginkgo.By("checking the job gets suspended when created unsuspended")
		priorityClass := utiltesting.MakePriorityClass(priorityClassName).
			PriorityValue(int32(priorityValue)).Obj()
		util.MustCreate(ctx, k8sClient, priorityClass)
		ginkgo.DeferCleanup(func() {
			gomega.Expect(k8sClient.Delete(ctx, priorityClass)).To(gomega.Succeed())
		})
		job := testingjob.MakeJob(jobName, ns.Name).
			PriorityClass(priorityClassName).
			SetAnnotation("provreq.kueue.x-k8s.io/ValidUntilSeconds", "0").
			SetAnnotation("invalid-provreq-prefix/Foo", "Bar").
			Label("toCopyKey", "toCopyValue").
			Label("doNotCopyKey", "doNotCopyValue").
			Obj()
		util.MustCreate(ctx, k8sClient, job)
		lookupKey := types.NamespacedName{Name: jobName, Namespace: ns.Name}
		createdJob := &batchv1.Job{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
			g.Expect(createdJob.Spec.Suspend).Should(gomega.Equal(ptr.To(true)))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("checking the workload is created without queue assigned")
		createdWorkload := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: ns.Name}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		gomega.Expect(createdWorkload.Spec.QueueName).Should(gomega.Equal(kueue.LocalQueueName("")), "The Workload shouldn't have .spec.queueName set")
		gomega.Expect(metav1.IsControlledBy(createdWorkload, job)).To(gomega.BeTrue(), "The Workload should be owned by the Job")

		createdTime := createdWorkload.CreationTimestamp

		ginkgo.By("checking the workload is created with workload priority", func() {
			util.ExpectWorkloadsWithPodPriority(ctx, k8sClient, priorityClassName, priorityValue, wlLookupKey)
		})

		ginkgo.By("checking the workload is created with ProvisioningRequest annotations", func() {
			gomega.Expect(createdWorkload.Annotations).Should(gomega.Equal(map[string]string{"provreq.kueue.x-k8s.io/ValidUntilSeconds": "0"}))
		})

		gomega.Expect(createdWorkload.Labels["toCopyKey"]).Should(gomega.Equal("toCopyValue"))
		gomega.Expect(createdWorkload.Labels).ShouldNot(gomega.ContainElement("doNotCopyValue"))

		ginkgo.By("checking the workload is updated with queue name when the job does")
		var jobQueueName kueue.LocalQueueName = "test-queue"
		createdJob.Labels[constants.QueueLabel] = string(jobQueueName)
		gomega.Expect(k8sClient.Update(ctx, createdJob)).Should(gomega.Succeed())
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
			g.Expect(createdWorkload.Spec.QueueName).Should(gomega.Equal(jobQueueName))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("checking the workload label is not updated when the job label is")
		newJobLabelValue := "updatedValue"
		createdJob.Labels["toCopyKey"] = newJobLabelValue
		gomega.Expect(k8sClient.Update(ctx, createdJob)).Should(gomega.Succeed())
		gomega.Consistently(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
			g.Expect(createdWorkload.Labels).Should(gomega.HaveKeyWithValue("toCopyKey", "toCopyValue"))
		}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())

		ginkgo.By("updated workload should have the same created timestamp", func() {
			gomega.Expect(createdWorkload.CreationTimestamp).Should(gomega.Equal(createdTime))
		})

		ginkgo.By("checking a second non-matching workload is deleted")
		secondWl := &kueue.Workload{
			ObjectMeta: metav1.ObjectMeta{
				Name:      workloadjob.GetWorkloadNameForJob("second-workload", "test-uid"),
				Namespace: createdWorkload.Namespace,
			},
			Spec: *createdWorkload.Spec.DeepCopy(),
		}
		gomega.Expect(ctrl.SetControllerReference(createdJob, secondWl, k8sClient.Scheme())).Should(gomega.Succeed())
		secondWl.Spec.PodSets[0].Count++
		util.MustCreate(ctx, k8sClient, secondWl)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, secondWl, false)
		// check the original wl is still there
		gomega.Consistently(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
		}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
		gomega.Eventually(func(g gomega.Gomega) {
			ok, _ := utiltesting.CheckEventRecordedFor(ctx, k8sClient, "DeletedWorkload", corev1.EventTypeNormal, fmt.Sprintf("Deleted not matching Workload: %v", workload.Key(secondWl)), lookupKey)
			g.Expect(ok).Should(gomega.BeTrue())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("checking the job is unsuspended when workload is assigned")
		onDemandFlavor := utiltestingapi.MakeResourceFlavor("on-demand").NodeLabel(instanceKey, "on-demand").Obj()
		util.MustCreate(ctx, k8sClient, onDemandFlavor)
		ginkgo.DeferCleanup(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		})
		spotFlavor := utiltestingapi.MakeResourceFlavor("spot").NodeLabel(instanceKey, "spot").Obj()
		util.MustCreate(ctx, k8sClient, spotFlavor)
		clusterQueue := utiltestingapi.MakeClusterQueue("cluster-queue").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj(),
				*utiltestingapi.MakeFlavorQuotas("spot").Resource(corev1.ResourceCPU, "5").Obj(),
			).Obj()
		admission := utiltestingapi.MakeAdmission(clusterQueue.Name).
			PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
				Assignment(corev1.ResourceCPU, "on-demand", "1m").
				Count(createdWorkload.Spec.PodSets[0].Count).
				Obj()).
			Obj()
		util.SetQuotaReservation(ctx, k8sClient, wlLookupKey, admission)
		util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
			g.Expect(createdJob.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		gomega.Eventually(func(g gomega.Gomega) {
			ok, _ := utiltesting.CheckEventRecordedFor(ctx, k8sClient, "Started", corev1.EventTypeNormal, fmt.Sprintf("Admitted by clusterQueue %v", clusterQueue.Name), lookupKey)
			g.Expect(ok).Should(gomega.BeTrue())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		gomega.Expect(createdJob.Spec.Template.Spec.NodeSelector).Should(gomega.HaveLen(1))
		gomega.Expect(createdJob.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))
		gomega.Consistently(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
			g.Expect(createdWorkload.Status.Conditions).Should(gomega.HaveLen(2))
		}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())

		// We need to set startTime to the job since the kube-controller-manager doesn't exist in envtest.
		ginkgo.By("setting startTime to the job")
		now := metav1.Now()
		createdJob.Status.StartTime = &now
		gomega.Expect(k8sClient.Status().Update(ctx, createdJob)).Should(gomega.Succeed())

		ginkgo.By("checking the job gets suspended when parallelism changes and the added node selectors are removed")
		newParallelism := int32(parallelism + 1)
		createdJob.Spec.Parallelism = &newParallelism
		gomega.Expect(k8sClient.Update(ctx, createdJob)).Should(gomega.Succeed())
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
			g.Expect(createdJob.Spec.Suspend).Should(gomega.Equal(ptr.To(true)))
			g.Expect(createdJob.Status.StartTime).Should(gomega.BeNil())
			g.Expect(createdJob.Spec.Template.Spec.NodeSelector).Should(gomega.BeEmpty())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		gomega.Eventually(func(g gomega.Gomega) {
			ok, _ := utiltesting.CheckEventRecordedFor(ctx, k8sClient, "DeletedWorkload", corev1.EventTypeNormal, fmt.Sprintf("Deleted not matching Workload: %v", wlLookupKey.String()), lookupKey)
			g.Expect(ok).Should(gomega.BeTrue())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("checking the workload is updated with new count")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
			g.Expect(createdWorkload.Spec.PodSets[0].Count).Should(gomega.Equal(createdWorkload.Spec.PodSets[0].Count))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		gomega.Expect(createdWorkload.Status.Admission).Should(gomega.BeNil())

		ginkgo.By("checking the job is unsuspended and selectors added when workload is assigned again")
		admission = utiltestingapi.MakeAdmission(clusterQueue.Name).
			PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
				Assignment(corev1.ResourceCPU, "spot", "1m").
				Count(createdWorkload.Spec.PodSets[0].Count).
				Obj()).
			Obj()
		util.SetQuotaReservation(ctx, k8sClient, wlLookupKey, admission)
		util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
			g.Expect(createdJob.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		gomega.Expect(createdJob.Spec.Template.Spec.NodeSelector).Should(gomega.HaveLen(1))
		gomega.Expect(createdJob.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(spotFlavor.Name))
		gomega.Consistently(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
			g.Expect(createdWorkload.Status.Conditions).Should(gomega.HaveLen(2))
		}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())

		ginkgo.By("checking the workload is finished when job is completed")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
			now := metav1.Now()
			createdJob.Status.StartTime = ptr.To(now)
			createdJob.Status.CompletionTime = ptr.To(now)
			createdJob.Status.Conditions = append(createdJob.Status.Conditions,
				batchv1.JobCondition{
					Type:               batchv1.JobSuccessCriteriaMet,
					Status:             corev1.ConditionTrue,
					LastProbeTime:      now,
					LastTransitionTime: now,
				},
				batchv1.JobCondition{
					Type:               batchv1.JobComplete,
					Status:             corev1.ConditionTrue,
					LastProbeTime:      now,
					LastTransitionTime: now,
				},
			)
			g.Expect(k8sClient.Status().Update(ctx, createdJob)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
			g.Expect(createdWorkload.Status.Conditions).Should(utiltesting.HaveConditionStatusTrue(kueue.WorkloadFinished))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("Should reconcile job when queueName set by label", func() {
		ginkgo.By("checking the workload is created with correct queue name assigned")
		var jobQueueName kueue.LocalQueueName = "test-queue"
		job := testingjob.MakeJob(jobName, ns.Name).Label(constants.QueueLabel, "test-queue").Obj()
		util.MustCreate(ctx, k8sClient, job)
		createdWorkload := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: ns.Name}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		gomega.Expect(createdWorkload.Spec.QueueName).Should(gomega.Equal(jobQueueName))
	})

	ginkgo.It("Should not manage a job without a queue-name submitted to an unmanaged namespace", func() {
		ginkgo.By("Creating an unsuspended job without a queue-name in unmanaged-ns")
		job := testingjob.MakeJob(jobName, "unmanaged-ns").Suspend(false).Obj()
		util.MustCreate(ctx, k8sClient, job)

		ginkgo.By("The job is not suspended and a workload is not created")
		lookupKey := types.NamespacedName{Name: job.Name, Namespace: job.Namespace}
		childWorkload := &kueue.Workload{}
		childWlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: job.Namespace}
		gomega.Consistently(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, lookupKey, job)).Should(gomega.Succeed())
			g.Expect(job.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
			g.Expect(k8sClient.Get(ctx, childWlLookupKey, childWorkload)).Should(utiltesting.BeNotFoundError())
		}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
	})

	ginkgo.It("Should manage a job without a queue-name submitted to managed namespace", func() {
		ginkgo.By("Creating an unsuspended job without a queue-name in a")
		job := testingjob.MakeJob(jobName, ns.Name).Suspend(false).Obj()
		util.MustCreate(ctx, k8sClient, job)

		ginkgo.By("The job is suspended and a workload is created")
		lookupKey := types.NamespacedName{Name: job.Name, Namespace: ns.Name}
		childWorkload := &kueue.Workload{}
		childWlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: job.Namespace}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, lookupKey, job)).Should(gomega.Succeed())
			g.Expect(job.Spec.Suspend).Should(gomega.Equal(ptr.To(true)))
			g.Expect(k8sClient.Get(ctx, childWlLookupKey, childWorkload)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.When("The parent job is managed by kueue", func() {
		ginkgo.It("Should suspend a job if the parent workload does not exist", func() {
			ginkgo.By("creating the parent job")
			parentJob := testingjob.MakeJob(parentJobName, ns.Name).Label(constants.PrebuiltWorkloadLabel, "missing").Obj()
			util.MustCreate(ctx, k8sClient, parentJob)

			ginkgo.By("Creating the child job which uses the parent workload annotation")
			childJob := testingjob.MakeJob(childJobName, ns.Name).Suspend(false).Obj()
			gomega.Expect(ctrl.SetControllerReference(parentJob, childJob, k8sClient.Scheme())).To(gomega.Succeed())
			util.MustCreate(ctx, k8sClient, childJob)

			ginkgo.By("checking that the child job is suspended")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, childLookupKey, childJob)).Should(gomega.Succeed())
				g.Expect(childJob.Spec.Suspend).Should(gomega.Equal(ptr.To(true)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should not create child workload for a job with a kueue managed parent", func() {
			ginkgo.By("creating the parent job")
			parentJob := testingjob.MakeJob(parentJobName, ns.Name).Obj()
			util.MustCreate(ctx, k8sClient, parentJob)

			ginkgo.By("waiting for the parent workload to be created")
			parentWorkload := &kueue.Workload{}
			parentWlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(parentJob.Name, parentJob.UID), Namespace: ns.Name}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, parentWlLookupKey, parentWorkload)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Creating the child job which uses the parent workload annotation")
			childJob := testingjob.MakeJob(childJobName, ns.Name).Obj()
			gomega.Expect(ctrl.SetControllerReference(parentJob, childJob, k8sClient.Scheme())).To(gomega.Succeed())
			util.MustCreate(ctx, k8sClient, childJob)

			ginkgo.By("Checking that the child workload is not created")
			childWorkload := &kueue.Workload{}
			childWlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(childJob.Name, childJob.UID), Namespace: ns.Name}
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, childWlLookupKey, childWorkload)).Should(utiltesting.BeNotFoundError())
			}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
		})

		ginkgo.It("Should not update the queue name of the workload with an empty value that the child job has", func() {
			var jobQueueName kueue.LocalQueueName = "test-queue"

			ginkgo.By("creating the parent job with queue name")
			parentJob := testingjob.MakeJob(parentJobName, ns.Name).Queue(jobQueueName).Obj()
			util.MustCreate(ctx, k8sClient, parentJob)

			ginkgo.By("waiting for the parent workload to be created")
			parentWorkload := &kueue.Workload{}
			parentWlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(parentJob.Name, parentJob.UID), Namespace: ns.Name}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, parentWlLookupKey, parentWorkload)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Creating the child job which uses the parent workload annotation")
			childJob := testingjob.MakeJob(childJobName, ns.Name).Obj()
			gomega.Expect(ctrl.SetControllerReference(parentJob, childJob, k8sClient.Scheme())).To(gomega.Succeed())
			util.MustCreate(ctx, k8sClient, childJob)

			ginkgo.By("Checking that the queue name of the parent workload isn't updated with an empty value")
			parentWorkload = &kueue.Workload{}
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, parentWlLookupKey, parentWorkload)).Should(gomega.Succeed())
				g.Expect(parentWorkload.Spec.QueueName).Should(gomega.Equal(jobQueueName))
			}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
		})

		ginkgo.It("Should change the suspension status of the child job when the parent's workload is not admitted", func() {
			ginkgo.By("Create a resource flavor")
			defaultFlavor := utiltestingapi.MakeResourceFlavor("default").NodeLabel(instanceKey, "default").Obj()
			util.MustCreate(ctx, k8sClient, defaultFlavor)
			ginkgo.DeferCleanup(func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, defaultFlavor, true)
			})

			ginkgo.By("creating the parent job")
			parentJob := testingjob.MakeJob(parentJobName, ns.Name).Obj()
			util.MustCreate(ctx, k8sClient, parentJob)

			ginkgo.By("waiting for the parent workload to be created")
			parentWorkload := &kueue.Workload{}
			parentWlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(parentJob.Name, parentJob.UID), Namespace: ns.Name}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, parentWlLookupKey, parentWorkload)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Creating the child job with the parent-workload annotation")
			childJob := testingjob.MakeJob(childJobName, ns.Name).Suspend(false).Obj()
			gomega.Expect(ctrl.SetControllerReference(parentJob, childJob, k8sClient.Scheme())).To(gomega.Succeed())
			util.MustCreate(ctx, k8sClient, childJob)

			ginkgo.By("checking that the child job is suspended")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, childLookupKey, childJob)).Should(gomega.Succeed())
				g.Expect(childJob.Spec.Suspend).Should(gomega.Equal(ptr.To(true)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.When("A prebuilt workload is used", func() {
		ginkgo.It("Should get suspended if the workload is not found", func() {
			job := testingjob.MakeJob("job", ns.Name).
				Queue("main").
				Label(constants.PrebuiltWorkloadLabel, "missing-workload").
				Obj()
			util.MustCreate(ctx, k8sClient, job)
			gomega.Eventually(func(g gomega.Gomega) {
				createdJob := batchv1.Job{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), &createdJob)).To(gomega.Succeed())
				g.Expect(ptr.Deref(createdJob.Spec.Suspend, false)).To(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should reconcile job when the workload is created later", func() {
			container := corev1.Container{
				Name:  "c",
				Image: "pause",
			}
			testingjob.SetContainerDefaults(&container)

			job := testingjob.MakeJob("job", ns.Name).
				Queue("main").
				Label(constants.PrebuiltWorkloadLabel, "wl").
				Containers(*container.DeepCopy()).
				Obj()

			util.MustCreate(ctx, k8sClient, job)
			ginkgo.By("Checking the job gets suspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdJob := batchv1.Job{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), &createdJob)).To(gomega.Succeed())
					g.Expect(ptr.Deref(createdJob.Spec.Suspend, false)).To(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			wl := utiltestingapi.MakeWorkload("wl", ns.Name).
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Containers(*container.DeepCopy()).
					Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, wl)

			ginkgo.By("Check the job gets the ownership of the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdWl := kueue.Workload{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &createdWl)).To(gomega.Succeed())
					util.MustHaveOwnerReference(g, createdWl.OwnerReferences, job, k8sClient.Scheme())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should take the ownership of the workload and continue the usual execution", func() {
			container := corev1.Container{
				Name:  "c",
				Image: "pause",
			}
			testingjob.SetContainerDefaults(&container)
			wl := utiltestingapi.MakeWorkload("wl", ns.Name).
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Containers(*container.DeepCopy()).
					Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, wl)
			gomega.Eventually(func(g gomega.Gomega) {
				createdWl := kueue.Workload{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &createdWl)).To(gomega.Succeed())
				g.Expect(createdWl.OwnerReferences).To(gomega.BeEmpty())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			job := testingjob.MakeJob("job", ns.Name).
				Queue("main").
				Label(constants.PrebuiltWorkloadLabel, "wl").
				Containers(*container.DeepCopy()).
				Obj()
			util.MustCreate(ctx, k8sClient, job)
			ginkgo.By("Checking the job gets suspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdJob := batchv1.Job{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), &createdJob)).To(gomega.Succeed())
					g.Expect(ptr.Deref(createdJob.Spec.Suspend, false)).To(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Check the job gets the ownership of the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdWl := kueue.Workload{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &createdWl)).To(gomega.Succeed())
					util.MustHaveOwnerReference(g, createdWl.OwnerReferences, job, k8sClient.Scheme())
					// The workload is not marked as finished.
					g.Expect(createdWl.Status.Conditions).ShouldNot(utiltesting.HaveConditionStatusTrue(kueue.WorkloadFinished))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Admitting the workload, the job should unsuspend", func() {
				admission := utiltestingapi.MakeAdmission("cq", kueue.NewPodSetReference(container.Name)).Obj()
				util.SetQuotaReservation(ctx, k8sClient, client.ObjectKeyFromObject(wl), admission)
				util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, wl)

				ginkgo.By("Checking the job gets suspended", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						createdJob := batchv1.Job{}
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), &createdJob)).To(gomega.Succeed())
						g.Expect(ptr.Deref(createdJob.Spec.Suspend, true)).To(gomega.BeFalse())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})
			})

			ginkgo.By("Finishing the job, the workload should be finish", func() {
				createdJob := batchv1.Job{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), &createdJob)).To(gomega.Succeed())
					now := metav1.Now()
					createdJob.Status.Succeeded = 1
					createdJob.Status.StartTime = ptr.To(now)
					createdJob.Status.CompletionTime = ptr.To(now)
					createdJob.Status.Conditions = []batchv1.JobCondition{
						{
							Type:               batchv1.JobComplete,
							Status:             corev1.ConditionTrue,
							LastProbeTime:      now,
							LastTransitionTime: now,
							Reason:             "ByTest",
							Message:            "Job finished successfully",
						},
						{
							Type:               batchv1.JobSuccessCriteriaMet,
							Status:             corev1.ConditionTrue,
							LastProbeTime:      now,
							LastTransitionTime: now,
							Reason:             "Reached expected number of succeeded pods",
						},
					}
					g.Expect(k8sClient.Status().Update(ctx, &createdJob)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				ginkgo.By("Checking the workload is finished", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						createdWl := kueue.Workload{}
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &createdWl)).To(gomega.Succeed())

						g.Expect(createdWl.Status.Conditions).To(gomega.ContainElement(
							gomega.BeComparableTo(metav1.Condition{
								Type:    kueue.WorkloadFinished,
								Status:  metav1.ConditionTrue,
								Reason:  kueue.WorkloadFinishedReasonSucceeded,
								Message: "Job finished successfully",
							}, util.IgnoreConditionTimestampsAndObservedGeneration)))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})
			})
		})
	})

	ginkgo.It("Should finish the preemption when the job becomes inactive", framework.SlowSpec, func() {
		job := testingjob.MakeJob(jobName, ns.Name).Queue("q").Obj()
		wl := &kueue.Workload{}
		var wlLookupKey types.NamespacedName
		ginkgo.By("create the job and admit the workload", func() {
			util.MustCreate(ctx, k8sClient, job)
			wlLookupKey = types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: ns.Name}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, wl)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			admission := utiltestingapi.MakeAdmission("q", kueue.NewPodSetReference(job.Spec.Template.Spec.Containers[0].Name)).Obj()
			util.SetQuotaReservation(ctx, k8sClient, wlLookupKey, admission)
			util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, wl)
		})

		ginkgo.By("wait for the job to be unsuspended", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), job)).To(gomega.Succeed())
				g.Expect(job.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("mark the job as active", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), job)).To(gomega.Succeed())
				job.Status.Active = 1
				g.Expect(k8sClient.Status().Update(ctx, job)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("preempt the workload", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, wl)).To(gomega.Succeed())
				g.Expect(workload.SetConditionAndUpdate(
					ctx,
					k8sClient,
					wl,
					kueue.WorkloadEvicted,
					metav1.ConditionTrue,
					kueue.WorkloadEvictedByPreemption, "By test", "evict",
					util.RealClock,
				)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("wait for the job to be suspended", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), job)).To(gomega.Succeed())
				g.Expect(job.Spec.Suspend).Should(gomega.Equal(ptr.To(true)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("the workload should stay admitted", func() {
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, wl)).To(gomega.Succeed())
				g.Expect(wl.Status.Conditions).Should(utiltesting.HaveConditionStatusTrue(kueue.WorkloadQuotaReserved))
			}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
		})

		ginkgo.By("mark the job as inactive", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), job)).To(gomega.Succeed())
				job.Status.Active = 0
				g.Expect(k8sClient.Status().Update(ctx, job)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("the workload should get unadmitted", func() {
			util.ExpectWorkloadsToBePending(ctx, k8sClient, wl)
		})
	})

	ginkgo.When("the queue has admission checks", func() {
		var (
			clusterQueueAc *kueue.ClusterQueue
			localQueue     *kueue.LocalQueue
			testFlavor     *kueue.ResourceFlavor
			jobLookupKey   *types.NamespacedName
			admissionCheck *kueue.AdmissionCheck
		)

		ginkgo.BeforeEach(func() {
			admissionCheck = utiltestingapi.MakeAdmissionCheck("check").ControllerName("ac-controller").Obj()
			util.MustCreate(ctx, k8sClient, admissionCheck)
			util.SetAdmissionCheckActive(ctx, k8sClient, admissionCheck, metav1.ConditionTrue)
			clusterQueueAc = utiltestingapi.MakeClusterQueue("prod-cq-with-checks").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("test-flavor").Resource(corev1.ResourceCPU, "5").Obj(),
				).AdmissionChecks("check").Obj()
			util.MustCreate(ctx, k8sClient, clusterQueueAc)
			localQueue = utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueueAc.Name).Obj()
			util.MustCreate(ctx, k8sClient, localQueue)
			testFlavor = utiltestingapi.MakeResourceFlavor("test-flavor").NodeLabel(instanceKey, "test-flavor").Obj()
			util.MustCreate(ctx, k8sClient, testFlavor)

			jobLookupKey = &types.NamespacedName{Name: jobName, Namespace: ns.Name}
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteObject(ctx, k8sClient, admissionCheck)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, testFlavor, true)
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueueAc, true)
		})

		ginkgo.It("labels and annotations should be propagated from admission check to job", func() {
			createdJob := &batchv1.Job{}
			createdWorkload := &kueue.Workload{}
			job := testingjob.MakeJob(jobName, ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				Request(corev1.ResourceCPU, "5").
				PodAnnotation("old-ann-key", "old-ann-value").
				Toleration(corev1.Toleration{
					Key:      "selector0",
					Value:    "selector-value1",
					Operator: corev1.TolerationOpEqual,
					Effect:   corev1.TaintEffectNoSchedule,
				}).
				PodLabel("old-label-key", "old-label-value").
				Obj()

			ginkgo.By("creating the job with pod labels & annotations", func() {
				util.MustCreate(ctx, k8sClient, job)
			})

			ginkgo.By("fetch the job and verify it is suspended as the checks are not ready", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, *jobLookupKey, createdJob)).Should(gomega.Succeed())
					g.Expect(createdJob.Spec.Suspend).Should(gomega.Equal(ptr.To(true)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			wlLookupKey := &types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: ns.Name}
			ginkgo.By("fetch the created workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, *wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("add labels & annotations to the workload admission check in PodSetUpdates", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					var newWL kueue.Workload
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(createdWorkload), &newWL)).To(gomega.Succeed())
					workload.SetAdmissionCheckState(&newWL.Status.AdmissionChecks, kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: kueue.DefaultPodSetName,
								Labels: map[string]string{
									"label1": "label-value1",
								},
								Annotations: map[string]string{
									"ann1": "ann-value1",
								},
								NodeSelector: map[string]string{
									"selector1": "selector-value1",
								},
								Tolerations: []corev1.Toleration{
									{
										Key:      "selector0",
										Value:    "selector-value1",
										Operator: corev1.TolerationOpEqual,
										Effect:   corev1.TaintEffectNoSchedule,
									},
									{
										Key:      "selector1",
										Value:    "selector-value1",
										Operator: corev1.TolerationOpEqual,
										Effect:   corev1.TaintEffectNoSchedule,
									},
								},
							},
						},
					}, util.RealClock)
					g.Expect(k8sClient.Status().Update(ctx, &newWL)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("admit the workload", func() {
				admission := utiltestingapi.MakeAdmission(clusterQueueAc.Name).
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "test-flavor", "1").
						Count(createdWorkload.Spec.PodSets[0].Count).
						Obj()).
					Obj()
				util.SetQuotaReservation(ctx, k8sClient, *wlLookupKey, admission)
				util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
			})

			ginkgo.By("await for the job to be admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, *jobLookupKey, createdJob)).Should(gomega.Succeed())
					g.Expect(createdJob.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verify the PodSetUpdates are propagated to the running job", func() {
				gomega.Expect(createdJob.Spec.Template.Annotations).Should(gomega.HaveKeyWithValue("ann1", "ann-value1"))
				gomega.Expect(createdJob.Spec.Template.Annotations).Should(gomega.HaveKeyWithValue("old-ann-key", "old-ann-value"))
				gomega.Expect(createdJob.Spec.Template.Labels).Should(gomega.HaveKeyWithValue("label1", "label-value1"))
				gomega.Expect(createdJob.Spec.Template.Labels).Should(gomega.HaveKeyWithValue("old-label-key", "old-label-value"))
				gomega.Expect(createdJob.Spec.Template.Spec.NodeSelector).Should(gomega.HaveKeyWithValue(instanceKey, "test-flavor"))
				gomega.Expect(createdJob.Spec.Template.Spec.NodeSelector).Should(gomega.HaveKeyWithValue("selector1", "selector-value1"))
				gomega.Expect(createdJob.Spec.Template.Spec.Tolerations).Should(gomega.BeComparableTo(
					[]corev1.Toleration{
						{
							Key:      "selector0",
							Value:    "selector-value1",
							Operator: corev1.TolerationOpEqual,
							Effect:   corev1.TaintEffectNoSchedule,
						},
						{
							Key:      "selector1",
							Value:    "selector-value1",
							Operator: corev1.TolerationOpEqual,
							Effect:   corev1.TaintEffectNoSchedule,
						},
					},
				))
			})

			ginkgo.By("delete the localQueue to prevent readmission", func() {
				gomega.Expect(util.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
			})

			ginkgo.By("clear the workload's admission to stop the job", func() {
				util.SetQuotaReservation(ctx, k8sClient, *wlLookupKey, nil)
				util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
			})

			ginkgo.By("await for the job to be suspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, *jobLookupKey, createdJob)).Should(gomega.Succeed())
					g.Expect(createdJob.Spec.Suspend).Should(gomega.Equal(ptr.To(true)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verify the PodSetUpdates are restored", func() {
				// In case of batch/Job the stop is done with multiple API calls, suspended=true being
				// done before the info restoration. We should retry the read if the Info is not restored.
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, *jobLookupKey, createdJob)).Should(gomega.Succeed())
					g.Expect(createdJob.Spec.Template.Annotations).ShouldNot(gomega.HaveKey("ann1"))
					g.Expect(createdJob.Spec.Template.Annotations).Should(gomega.HaveKeyWithValue("old-ann-key", "old-ann-value"))
					g.Expect(createdJob.Spec.Template.Labels).ShouldNot(gomega.HaveKey("label1"))
					g.Expect(createdJob.Spec.Template.Labels).Should(gomega.HaveKeyWithValue("old-label-key", "old-label-value"))
					g.Expect(createdJob.Spec.Template.Spec.NodeSelector).ShouldNot(gomega.HaveKey(instanceKey))
					g.Expect(createdJob.Spec.Template.Spec.NodeSelector).ShouldNot(gomega.HaveKey("selector1"))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should not admit workload if there is a conflict in labels", framework.SlowSpec, func() {
			createdJob := &batchv1.Job{}
			createdWorkload := &kueue.Workload{}
			job := testingjob.MakeJob(jobName, ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				Request(corev1.ResourceCPU, "5").
				PodLabel("label-key", "old-label-value").
				Obj()

			ginkgo.By("creating the job with default priority", func() {
				util.MustCreate(ctx, k8sClient, job)
			})

			wlLookupKey := &types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: ns.Name}
			ginkgo.By("fetch the created job & workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, *jobLookupKey, createdJob)).Should(gomega.Succeed())
					g.Expect(createdJob.Spec.Suspend).Should(gomega.Equal(ptr.To(true)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, *wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("add a conflicting label to the admission check in PodSetUpdates", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					var newWL kueue.Workload
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(createdWorkload), &newWL)).To(gomega.Succeed())
					workload.SetAdmissionCheckState(&newWL.Status.AdmissionChecks, kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: kueue.DefaultPodSetName,
								Labels: map[string]string{
									"label-key": "new-label-value",
								},
							},
						},
					}, util.RealClock)
					g.Expect(k8sClient.Status().Update(ctx, &newWL)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("attempt to admit the workload", func() {
				admission := utiltestingapi.MakeAdmission(clusterQueueAc.Name).
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "test-flavor", "1").
						Count(createdWorkload.Spec.PodSets[0].Count).
						Obj()).
					Obj()
				util.SetQuotaReservation(ctx, k8sClient, *wlLookupKey, admission)
				util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
			})

			ginkgo.By("verify the job is not started", func() {
				gomega.Consistently(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, *jobLookupKey, createdJob)).Should(gomega.Succeed())
					g.Expect(createdJob.Spec.Suspend).Should(gomega.Equal(ptr.To(true)))
				}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
			})

			ginkgo.By("verify the job has the old label value", func() {
				gomega.Expect(createdJob.Spec.Template.Labels).Should(gomega.HaveKeyWithValue("label-key", "old-label-value"))
			})
		})
	})
})

var _ = ginkgo.Describe("When waitForPodsReady enabled", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	type podsReadyTestSpec struct {
		beforeJobStatus *batchv1.JobStatus
		beforeCondition *metav1.Condition
		jobStatus       batchv1.JobStatus
		suspended       bool
		wantCondition   *metav1.Condition
	}

	var (
		ns            *corev1.Namespace
		defaultFlavor = utiltestingapi.MakeResourceFlavor("default").NodeLabel(instanceKey, "default").Obj()
	)

	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup(jobframework.WithWaitForPodsReady(&configapi.WaitForPodsReady{})))
		ginkgo.By("Create a resource flavor")
		util.MustCreate(ctx, k8sClient, defaultFlavor)
	})
	ginkgo.AfterAll(func() {
		util.ExpectObjectToBeDeleted(ctx, k8sClient, defaultFlavor, true)
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-")
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.DescribeTable("Single job at different stages of progress towards completion",
		func(podsReadyTestSpec podsReadyTestSpec) {
			ginkgo.By("Create a job")
			job := testingjob.MakeJob(jobName, ns.Name).Parallelism(2).Obj()
			jobQueueName := "test-queue"
			job.Labels = map[string]string{constants.QueueLabel: jobQueueName}
			util.MustCreate(ctx, k8sClient, job)
			lookupKey := types.NamespacedName{Name: jobName, Namespace: ns.Name}
			createdJob := &batchv1.Job{}
			gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())

			ginkgo.By("Fetch the workload created for the job")
			createdWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: ns.Name}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Admit the workload created for the job")
			admission := utiltestingapi.MakeAdmission("foo").
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(corev1.ResourceCPU, "default", "1m").
					Count(createdWorkload.Spec.PodSets[0].Count).
					Obj()).
				Obj()
			util.SetQuotaReservation(ctx, k8sClient, wlLookupKey, admission)
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
					g.Expect(apimeta.FindStatusCondition(createdWorkload.Status.Conditions, kueue.WorkloadPodsReady)).
						Should(gomega.BeComparableTo(podsReadyTestSpec.beforeCondition, util.IgnoreConditionTimestampsAndObservedGeneration))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			}

			ginkgo.By("Update the job status to simulate its progress towards completion")
			createdJob.Status = podsReadyTestSpec.jobStatus
			gomega.Expect(k8sClient.Status().Update(ctx, createdJob)).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())

			if podsReadyTestSpec.suspended {
				ginkgo.By("Unset admission of the workload to suspend the job")
				util.SetQuotaReservation(ctx, k8sClient, wlLookupKey, nil)
				gomega.Eventually(func(g gomega.Gomega) {
					// the update may need to be retried due to a conflict as the workload gets
					// also updated due to setting of the job status.
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					g.Expect(createdWorkload.Status.Conditions).To(gomega.ContainElements(
						gomega.BeComparableTo(metav1.Condition{
							Type:    kueue.WorkloadAdmitted,
							Status:  metav1.ConditionFalse,
							Reason:  "NoReservation",
							Message: "The workload has no reservation",
						}, util.IgnoreConditionTimestampsAndObservedGeneration),
					))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			}

			ginkgo.By("Verify the PodsReady condition is added")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				cond := apimeta.FindStatusCondition(createdWorkload.Status.Conditions, kueue.WorkloadPodsReady)
				g.Expect(cond).Should(gomega.BeComparableTo(podsReadyTestSpec.wantCondition, util.IgnoreConditionTimestampsAndObservedGeneration))
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
		ginkgo.Entry("Single pod ready", podsReadyTestSpec{
			jobStatus: batchv1.JobStatus{
				Active: 1,
				Ready:  ptr.To[int32](1),
			},
			wantCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionFalse,
				Reason:  kueue.WorkloadWaitForStart,
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
				Reason:  kueue.WorkloadWaitForStart,
				Message: "Not all pods are ready or succeeded",
			},
		}),
		ginkgo.Entry("All pods are ready", podsReadyTestSpec{
			jobStatus: batchv1.JobStatus{
				Active: 2,
				Ready:  ptr.To[int32](2),
			},
			wantCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionTrue,
				Reason:  kueue.WorkloadStarted,
				Message: "All pods reached readiness and the workload is running",
			},
		}),
		ginkgo.Entry("One pod ready, one terminating succeeded", podsReadyTestSpec{
			jobStatus: batchv1.JobStatus{
				Active: 1,
				Ready:  ptr.To[int32](1),
				UncountedTerminatedPods: &batchv1.UncountedTerminatedPods{
					Succeeded: []types.UID{"foo"},
				},
			},
			wantCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionTrue,
				Reason:  kueue.WorkloadStarted,
				Message: "All pods reached readiness and the workload is running",
			},
		}),
		ginkgo.Entry("One pod ready, one succeeded", podsReadyTestSpec{
			jobStatus: batchv1.JobStatus{
				Active:    1,
				Ready:     ptr.To[int32](1),
				Succeeded: 1,
			},
			wantCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionTrue,
				Reason:  kueue.WorkloadStarted,
				Message: "All pods reached readiness and the workload is running",
			},
		}),
		ginkgo.Entry("One pod succeeded, one terminating succeeded", podsReadyTestSpec{
			jobStatus: batchv1.JobStatus{
				Succeeded: 1,
				UncountedTerminatedPods: &batchv1.UncountedTerminatedPods{
					Succeeded: []types.UID{"foo"},
				},
			},
			wantCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionTrue,
				Reason:  kueue.WorkloadStarted,
				Message: "All pods reached readiness and the workload is running",
			},
		}),
		ginkgo.Entry("All pods terminating succeeded", podsReadyTestSpec{
			jobStatus: batchv1.JobStatus{
				UncountedTerminatedPods: &batchv1.UncountedTerminatedPods{
					Succeeded: []types.UID{"foo", "bar"},
				},
			},
			wantCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionTrue,
				Reason:  kueue.WorkloadStarted,
				Message: "All pods reached readiness and the workload is running",
			},
		}),
		ginkgo.Entry("All pods are succeeded", podsReadyTestSpec{
			jobStatus: batchv1.JobStatus{
				Ready:     ptr.To[int32](0),
				Succeeded: 2,
			},
			wantCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionTrue,
				Reason:  kueue.WorkloadStarted,
				Message: "All pods reached readiness and the workload is running",
			},
		}),
		ginkgo.Entry("All pods are succeeded; PodsReady=False before", podsReadyTestSpec{
			beforeCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionFalse,
				Reason:  kueue.WorkloadWaitForStart,
				Message: "Not all pods are ready or succeeded",
			},
			jobStatus: batchv1.JobStatus{
				Ready:     ptr.To[int32](0),
				Succeeded: 2,
			},
			wantCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionTrue,
				Reason:  kueue.WorkloadStarted,
				Message: "All pods reached readiness and the workload is running",
			},
		}),
		ginkgo.Entry("One ready pod, one failed; PodsReady=True before", podsReadyTestSpec{
			beforeJobStatus: &batchv1.JobStatus{
				Active: 2,
				Ready:  ptr.To[int32](2),
			},
			beforeCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionTrue,
				Reason:  kueue.WorkloadStarted,
				Message: "All pods reached readiness and the workload is running",
			},
			jobStatus: batchv1.JobStatus{
				Active: 1,
				Ready:  ptr.To[int32](1),
				Failed: 1,
			},
			wantCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionFalse,
				Reason:  kueue.WorkloadWaitForRecovery,
				Message: "At least one pod has failed, waiting for recovery",
			},
		}),
		ginkgo.Entry("Job suspended without ready pods; but PodsReady=True before", podsReadyTestSpec{
			beforeJobStatus: &batchv1.JobStatus{
				Active: 2,
				Ready:  ptr.To[int32](2),
			},
			beforeCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionTrue,
				Reason:  kueue.WorkloadStarted,
				Message: "All pods reached readiness and the workload is running",
			},
			jobStatus: batchv1.JobStatus{
				Failed: 2,
			},
			suspended: true,
			wantCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionFalse,
				Reason:  kueue.WorkloadWaitForStart,
				Message: "Not all pods are ready or succeeded",
			},
		}),
		ginkgo.Entry("Job suspended with all pods ready; PodsReady=True before", podsReadyTestSpec{
			beforeJobStatus: &batchv1.JobStatus{
				Active: 2,
				Ready:  ptr.To[int32](2),
			},
			beforeCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionTrue,
				Reason:  kueue.WorkloadStarted,
				Message: "All pods reached readiness and the workload is running",
			},
			jobStatus: batchv1.JobStatus{
				Active: 2,
				Ready:  ptr.To[int32](2),
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

var _ = ginkgo.Describe("Interacting with scheduler", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns                  *corev1.Namespace
		onDemandFlavor      *kueue.ResourceFlavor
		spotTaintedFlavor   *kueue.ResourceFlavor
		spotUntaintedFlavor *kueue.ResourceFlavor
		prodClusterQ        *kueue.ClusterQueue
		devClusterQ         *kueue.ClusterQueue
		podsCountClusterQ   *kueue.ClusterQueue
		prodLocalQ          *kueue.LocalQueue
		devLocalQ           *kueue.LocalQueue
		podsLocalQ          *kueue.LocalQueue
	)

	startManager := func() {
		fwk.StartManager(ctx, cfg, managerAndControllersSetup(false, true, nil))
	}

	stopManager := func() {
		fwk.StopManager(ctx)
	}

	restartManager := func() {
		stopManager()
		startManager()
	}

	ginkgo.BeforeAll(func() {
		startManager()
	})
	ginkgo.AfterAll(func() {
		stopManager()
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-")

		onDemandFlavor = utiltestingapi.MakeResourceFlavor("on-demand").NodeLabel(instanceKey, "on-demand").Obj()
		util.MustCreate(ctx, k8sClient, onDemandFlavor)

		spotTaintedFlavor = utiltestingapi.MakeResourceFlavor("spot-tainted").
			NodeLabel(instanceKey, "spot-tainted").
			Taint(corev1.Taint{
				Key:    instanceKey,
				Value:  "spot-tainted",
				Effect: corev1.TaintEffectNoSchedule,
			}).Obj()
		util.MustCreate(ctx, k8sClient, spotTaintedFlavor)

		spotUntaintedFlavor = utiltestingapi.MakeResourceFlavor("spot-untainted").NodeLabel(instanceKey, "spot-untainted").Obj()
		util.MustCreate(ctx, k8sClient, spotUntaintedFlavor)

		prodClusterQ = utiltestingapi.MakeClusterQueue("prod-cq").
			Cohort("prod").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("spot-tainted").Resource(corev1.ResourceCPU, "5", "0").Obj(),
				*utiltestingapi.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj(),
			).Obj()
		util.MustCreate(ctx, k8sClient, prodClusterQ)

		devClusterQ = utiltestingapi.MakeClusterQueue("dev-clusterqueue").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("spot-untainted").Resource(corev1.ResourceCPU, "5").Obj(),
				*utiltestingapi.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj(),
			).
			FlavorFungibility(kueue.FlavorFungibility{
				WhenCanBorrow:  kueue.MayStopSearch,
				WhenCanPreempt: kueue.TryNextFlavor,
			}).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj()
		util.MustCreate(ctx, k8sClient, devClusterQ)

		podsCountClusterQ = utiltestingapi.MakeClusterQueue("pods-clusterqueue").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("on-demand").Resource(corev1.ResourcePods, "5").Obj(),
			).
			Obj()
		util.MustCreate(ctx, k8sClient, podsCountClusterQ)

		prodLocalQ = utiltestingapi.MakeLocalQueue("prod-queue", ns.Name).ClusterQueue(prodClusterQ.Name).Obj()
		util.MustCreate(ctx, k8sClient, prodLocalQ)

		devLocalQ = utiltestingapi.MakeLocalQueue("dev-queue", ns.Name).ClusterQueue(devClusterQ.Name).Obj()
		util.MustCreate(ctx, k8sClient, devLocalQ)

		podsLocalQ = utiltestingapi.MakeLocalQueue("pods-queue", ns.Name).ClusterQueue(podsCountClusterQ.Name).Obj()
		util.MustCreate(ctx, k8sClient, podsLocalQ)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, prodClusterQ, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, devClusterQ, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, podsCountClusterQ, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, spotTaintedFlavor, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, spotUntaintedFlavor, true)
	})

	ginkgo.When("Substitute WorkloadPriorityClass", func() {
		var (
			lowWorkloadPriorityClass  *kueue.WorkloadPriorityClass
			highWorkloadPriorityClass *kueue.WorkloadPriorityClass
		)

		ginkgo.BeforeEach(func() {
			lowWorkloadPriorityClass = utiltestingapi.MakeWorkloadPriorityClass("low").PriorityValue(100).Obj()
			util.MustCreate(ctx, k8sClient, lowWorkloadPriorityClass)

			highWorkloadPriorityClass = utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(1000).Obj()
			util.MustCreate(ctx, k8sClient, highWorkloadPriorityClass)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(k8sClient.Delete(ctx, lowWorkloadPriorityClass)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Delete(ctx, highWorkloadPriorityClass)).To(gomega.Succeed())
		})

		ginkgo.It("running workload priority is decreased causing a pending workload to be scheduled and preempt the running one", framework.SlowSpec, func() {
			job1 := testingjob.MakeJob(jobName+"1", ns.Name).
				WorkloadPriorityClass(highWorkloadPriorityClass.Name).
				Queue(kueue.LocalQueueName(devLocalQ.Name)).
				Request(corev1.ResourceCPU, "5").
				Obj()
			ginkgo.By("creating the first Job with high priority", func() {
				util.MustCreate(ctx, k8sClient, job1)
			})

			createdWorkload1 := &kueue.Workload{}
			wlLookupKey1 := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job1.Name, job1.UID), Namespace: ns.Name}

			ginkgo.By("verify the first workload is created with workload priority class", func() {
				util.ExpectWorkloadsWithWorkloadPriority(ctx, k8sClient, highWorkloadPriorityClass.Name, highWorkloadPriorityClass.Value, wlLookupKey1)
			})

			ginkgo.By("checking that the first workload is admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey1, createdWorkload1)).Should(gomega.Succeed())
					g.Expect(createdWorkload1.Status.Conditions).Should(utiltesting.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			job2 := testingjob.MakeJob(jobName+"2", ns.Name).
				WorkloadPriorityClass(highWorkloadPriorityClass.Name).
				Queue(kueue.LocalQueueName(devLocalQ.Name)).
				Request(corev1.ResourceCPU, "5").
				Obj()
			ginkgo.By("creating the second Job with high priority", func() {
				util.MustCreate(ctx, k8sClient, job2)
			})

			createdWorkload2 := &kueue.Workload{}
			wlLookupKey2 := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job2.Name, job2.UID), Namespace: ns.Name}

			ginkgo.By("verify the second workload is created with workload priority class", func() {
				util.ExpectWorkloadsWithWorkloadPriority(ctx, k8sClient, highWorkloadPriorityClass.Name, highWorkloadPriorityClass.Value, wlLookupKey2)
			})

			ginkgo.By("checking that the second workload is admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey2, createdWorkload2)).Should(gomega.Succeed())
					g.Expect(createdWorkload2.Status.Conditions).Should(utiltesting.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			job3 := testingjob.MakeJob(jobName+"3", ns.Name).
				WorkloadPriorityClass(highWorkloadPriorityClass.Name).
				Queue(kueue.LocalQueueName(devLocalQ.Name)).
				Request(corev1.ResourceCPU, "5").
				Obj()
			ginkgo.By("creating the third Job with high priority", func() {
				util.MustCreate(ctx, k8sClient, job3)
			})

			createdWorkload3 := &kueue.Workload{}
			wlLookupKey3 := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job3.Name, job3.UID), Namespace: ns.Name}

			ginkgo.By("verify the third workload is created with workload priority class", func() {
				util.ExpectWorkloadsWithWorkloadPriority(ctx, k8sClient, highWorkloadPriorityClass.Name, highWorkloadPriorityClass.Value, wlLookupKey3)
			})

			ginkgo.By("checking that the third workload is not admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey3, createdWorkload3)).Should(gomega.Succeed())
					g.Expect(createdWorkload3.Status.Conditions).Should(utiltesting.HaveConditionStatusFalseAndReason(kueue.WorkloadQuotaReserved, "Pending"))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("updating the second workload’s WorkloadPriorityClass to low", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdJob := &batchv1.Job{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job2), createdJob)).Should(gomega.Succeed())
					createdJob.Labels[constants.WorkloadPriorityClassLabel] = lowWorkloadPriorityClass.Name
					g.Expect(k8sClient.Update(ctx, createdJob)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("checking that the second workload’s WorkloadPriorityClass is updated when the job’s priority label changes", func() {
				util.ExpectWorkloadsWithWorkloadPriority(ctx, k8sClient, lowWorkloadPriorityClass.Name, lowWorkloadPriorityClass.Value, wlLookupKey2)
			})

			ginkgo.By("checking that the third workload is admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey3, createdWorkload3)).Should(gomega.Succeed())
					g.Expect(createdWorkload3.Status.Conditions).Should(utiltesting.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("checking that the second workload is evicted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey2, createdWorkload2)).Should(gomega.Succeed())
					g.Expect(createdWorkload2.Status.Conditions).Should(utiltesting.HaveConditionStatusTrue(kueue.WorkloadEvicted))
					g.Expect(createdWorkload2.Status.Conditions).Should(utiltesting.HaveConditionStatusTrue(kueue.WorkloadPreempted))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("running workload priority is decreased, but still above the pending workload, and so the workload continues running undisturbed", framework.SlowSpec, func() {
			job1 := testingjob.MakeJob(jobName+"1", ns.Name).
				WorkloadPriorityClass(highWorkloadPriorityClass.Name).
				Queue(kueue.LocalQueueName(devLocalQ.Name)).
				Request(corev1.ResourceCPU, "5").
				Obj()
			ginkgo.By("creating the first Job with high priority", func() {
				util.MustCreate(ctx, k8sClient, job1)
			})

			createdWorkload1 := &kueue.Workload{}
			wlLookupKey1 := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job1.Name, job1.UID), Namespace: ns.Name}

			ginkgo.By("verify the first workload is created with workload priority class", func() {
				util.ExpectWorkloadsWithWorkloadPriority(ctx, k8sClient, highWorkloadPriorityClass.Name, highWorkloadPriorityClass.Value, wlLookupKey1)
			})

			ginkgo.By("checking that the first workload is admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey1, createdWorkload1)).Should(gomega.Succeed())
					g.Expect(createdWorkload1.Status.Conditions).Should(utiltesting.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			job2 := testingjob.MakeJob(jobName+"2", ns.Name).
				WorkloadPriorityClass(highWorkloadPriorityClass.Name).
				Queue(kueue.LocalQueueName(devLocalQ.Name)).
				Request(corev1.ResourceCPU, "5").
				Obj()
			ginkgo.By("creating the second Job with high priority", func() {
				util.MustCreate(ctx, k8sClient, job2)
			})

			createdWorkload2 := &kueue.Workload{}
			wlLookupKey2 := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job2.Name, job2.UID), Namespace: ns.Name}

			ginkgo.By("verify the second workload is created with workload priority class", func() {
				util.ExpectWorkloadsWithWorkloadPriority(ctx, k8sClient, highWorkloadPriorityClass.Name, highWorkloadPriorityClass.Value, wlLookupKey2)
			})

			ginkgo.By("checking that the second workload is admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey2, createdWorkload2)).Should(gomega.Succeed())
					g.Expect(createdWorkload2.Status.Conditions).Should(utiltesting.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			job3 := testingjob.MakeJob(jobName+"3", ns.Name).
				WorkloadPriorityClass(lowWorkloadPriorityClass.Name).
				Queue(kueue.LocalQueueName(devLocalQ.Name)).
				Request(corev1.ResourceCPU, "5").
				Obj()
			ginkgo.By("creating the third Job with low priority", func() {
				util.MustCreate(ctx, k8sClient, job3)
			})

			createdWorkload3 := &kueue.Workload{}
			wlLookupKey3 := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job3.Name, job3.UID), Namespace: ns.Name}

			ginkgo.By("verify the third workload is created with workload priority class", func() {
				util.ExpectWorkloadsWithWorkloadPriority(ctx, k8sClient, lowWorkloadPriorityClass.Name, lowWorkloadPriorityClass.Value, wlLookupKey3)
			})

			ginkgo.By("checking that the third workload is not admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey3, createdWorkload3)).Should(gomega.Succeed())
					g.Expect(createdWorkload3.Status.Conditions).Should(utiltesting.HaveConditionStatusFalseAndReason(kueue.WorkloadQuotaReserved, "Pending"))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("updating the second workload’s WorkloadPriorityClass to low", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdJob := &batchv1.Job{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job2), createdJob)).Should(gomega.Succeed())
					createdJob.Labels[constants.WorkloadPriorityClassLabel] = lowWorkloadPriorityClass.Name
					g.Expect(k8sClient.Update(ctx, createdJob)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("checking that the second workload’s WorkloadPriorityClass is updated when the job’s priority label changes", func() {
				util.ExpectWorkloadsWithWorkloadPriority(ctx, k8sClient, lowWorkloadPriorityClass.Name, lowWorkloadPriorityClass.Value, wlLookupKey2)
			})

			ginkgo.By("checking that the third workload is not admitted and the second workload is not evicted", func() {
				gomega.Consistently(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey2, createdWorkload2)).Should(gomega.Succeed())
					g.Expect(createdWorkload2.Status.Conditions).Should(utiltesting.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
					g.Expect(createdWorkload2.Status.Conditions).ShouldNot(utiltesting.HaveConditionStatusTrue(kueue.WorkloadEvicted))
					g.Expect(createdWorkload2.Status.Conditions).ShouldNot(utiltesting.HaveConditionStatusTrue(kueue.WorkloadPreempted))
					g.Expect(k8sClient.Get(ctx, wlLookupKey3, createdWorkload3)).Should(gomega.Succeed())
					g.Expect(createdWorkload3.Status.Conditions).Should(utiltesting.HaveConditionStatusFalseAndReason(kueue.WorkloadQuotaReserved, "Pending"))
				}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should trigger preemption when WorkloadPriorityClass is added to a pending workload that had no priority class", framework.SlowSpec, func() {
			ginkgo.By("Creating a low-priority job that gets admitted on spot-untainted flavor")
			jobLow := testingjob.MakeJob(jobName+"-low", ns.Name).
				WorkloadPriorityClass(lowWorkloadPriorityClass.Name).
				Queue(kueue.LocalQueueName(devLocalQ.Name)).
				Request(corev1.ResourceCPU, "5").
				NodeSelector(instanceKey, "spot-untainted").
				Obj()
			util.MustCreate(ctx, k8sClient, jobLow)

			lowWlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(jobLow.Name, jobLow.UID), Namespace: ns.Name}

			ginkgo.By("Verifying the low-priority workload is admitted")
			util.ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sClient, lowWlKey)

			ginkgo.By("Creating a job WITHOUT any WorkloadPriorityClass on the SAME flavor (will be pending)")
			jobNoPriority := testingjob.MakeJob(jobName+"-no-priority", ns.Name).
				Queue(kueue.LocalQueueName(devLocalQ.Name)).
				Request(corev1.ResourceCPU, "5").
				NodeSelector(instanceKey, "spot-untainted").
				Obj()
			util.MustCreate(ctx, k8sClient, jobNoPriority)

			noPriorityWlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(jobNoPriority.Name, jobNoPriority.UID), Namespace: ns.Name}

			ginkgo.By("Verifying the no-priority workload is pending (not admitted)")
			util.ExpectWorkloadsToBePendingByKeys(ctx, k8sClient, noPriorityWlKey)

			ginkgo.By("Adding high WorkloadPriorityClass to the pending job via label update")
			gomega.Eventually(func(g gomega.Gomega) {
				createdJob := &batchv1.Job{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(jobNoPriority), createdJob)).To(gomega.Succeed())
				if createdJob.Labels == nil {
					createdJob.Labels = make(map[string]string, 1)
				}
				createdJob.Labels[constants.WorkloadPriorityClassLabel] = highWorkloadPriorityClass.Name
				g.Expect(k8sClient.Update(ctx, createdJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Verifying the workload's priority is updated")
			util.ExpectWorkloadsWithWorkloadPriority(ctx, k8sClient, highWorkloadPriorityClass.Name, highWorkloadPriorityClass.Value, noPriorityWlKey)

			ginkgo.By("Verifying the low-priority workload gets preempted")
			util.ExpectWorkloadsToBePreemptedByKeys(ctx, k8sClient, lowWlKey)

			ginkgo.By("Verifying the high-priority workload gets admitted")
			util.ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sClient, noPriorityWlKey)
		})
	})

	ginkgo.When("Patch a WorkloadPriorityClass", func() {
		var (
			dynamicWorkloadPriorityClass *kueue.WorkloadPriorityClass
			midWorkloadPriorityClass     *kueue.WorkloadPriorityClass
			highWorkloadPriorityClass    *kueue.WorkloadPriorityClass
			lowPrio                      = int32(10)
			midPrio                      = int32(100)
			highPrio                     = int32(1000)
		)

		ginkgo.BeforeEach(func() {
			midWorkloadPriorityClass = utiltestingapi.MakeWorkloadPriorityClass("mid").PriorityValue(midPrio).Obj()
			util.MustCreate(ctx, k8sClient, midWorkloadPriorityClass)

			highWorkloadPriorityClass = utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(highPrio).Obj()
			util.MustCreate(ctx, k8sClient, highWorkloadPriorityClass)

			dynamicWorkloadPriorityClass = utiltestingapi.MakeWorkloadPriorityClass("dynamic").PriorityValue(highPrio).Obj()
			util.MustCreate(ctx, k8sClient, dynamicWorkloadPriorityClass)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(k8sClient.Delete(ctx, dynamicWorkloadPriorityClass)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Delete(ctx, midWorkloadPriorityClass)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Delete(ctx, highWorkloadPriorityClass)).To(gomega.Succeed())
		})

		ginkgo.It("running workload priority is decreased causing a pending workload to be scheduled and preempt the running one", framework.SlowSpec, func() {
			job1 := testingjob.MakeJob(jobName+"1", ns.Name).
				WorkloadPriorityClass(highWorkloadPriorityClass.Name).
				Queue(kueue.LocalQueueName(devLocalQ.Name)).
				Request(corev1.ResourceCPU, "5").
				Obj()
			ginkgo.By("creating the first Job with high priority", func() {
				util.MustCreate(ctx, k8sClient, job1)
			})

			createdWorkload1 := &kueue.Workload{}
			wlLookupKey1 := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job1.Name, job1.UID), Namespace: ns.Name}

			ginkgo.By("verify the first workload is created with workload priority class", func() {
				util.ExpectWorkloadsWithWorkloadPriority(ctx, k8sClient, highWorkloadPriorityClass.Name, highPrio, wlLookupKey1)
			})

			ginkgo.By("checking that the first workload is admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey1, createdWorkload1)).Should(gomega.Succeed())
					g.Expect(createdWorkload1.Status.Conditions).Should(utiltesting.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			job2 := testingjob.MakeJob(jobName+"2", ns.Name).
				WorkloadPriorityClass(dynamicWorkloadPriorityClass.Name).
				Queue(kueue.LocalQueueName(devLocalQ.Name)).
				Request(corev1.ResourceCPU, "5").
				Obj()
			ginkgo.By("creating the second Job with high priority", func() {
				util.MustCreate(ctx, k8sClient, job2)
			})

			createdWorkload2 := &kueue.Workload{}
			wlLookupKey2 := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job2.Name, job2.UID), Namespace: ns.Name}

			ginkgo.By("verify the second workload is created with workload priority class", func() {
				util.ExpectWorkloadsWithWorkloadPriority(ctx, k8sClient, dynamicWorkloadPriorityClass.Name, highPrio, wlLookupKey2)
			})

			ginkgo.By("checking that the second workload is admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey2, createdWorkload2)).Should(gomega.Succeed())
					g.Expect(createdWorkload2.Status.Conditions).Should(utiltesting.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			job3 := testingjob.MakeJob(jobName+"3", ns.Name).
				WorkloadPriorityClass(midWorkloadPriorityClass.Name).
				Queue(kueue.LocalQueueName(devLocalQ.Name)).
				Request(corev1.ResourceCPU, "5").
				Obj()
			ginkgo.By("creating the third Job with mid priority", func() {
				util.MustCreate(ctx, k8sClient, job3)
			})

			createdWorkload3 := &kueue.Workload{}
			wlLookupKey3 := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job3.Name, job3.UID), Namespace: ns.Name}

			ginkgo.By("verify the third workload is created with workload priority class", func() {
				util.ExpectWorkloadsWithWorkloadPriority(ctx, k8sClient, midWorkloadPriorityClass.Name, midPrio, wlLookupKey3)
			})

			ginkgo.By("checking that the third workload is not admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey3, createdWorkload3)).Should(gomega.Succeed())
					g.Expect(createdWorkload3.Status.Conditions).Should(utiltesting.HaveConditionStatusFalseAndReason(kueue.WorkloadQuotaReserved, "Pending"))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("patching the dynamic WorkloadPriorityClass priority high => low", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					wpc := &kueue.WorkloadPriorityClass{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(dynamicWorkloadPriorityClass), wpc)).Should(gomega.Succeed())
					wpc.Value = lowPrio
					g.Expect(k8sClient.Update(ctx, wpc)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("checking that the third workload is admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey3, createdWorkload3)).Should(gomega.Succeed())
					g.Expect(createdWorkload3.Status.Conditions).Should(utiltesting.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("checking that the second workload is evicted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey2, createdWorkload2)).Should(gomega.Succeed())
					g.Expect(createdWorkload2.Status.Conditions).Should(utiltesting.HaveConditionStatusTrue(kueue.WorkloadEvicted))
					g.Expect(createdWorkload2.Status.Conditions).Should(utiltesting.HaveConditionStatusTrue(kueue.WorkloadPreempted))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("running workload priority is decreased, but still above the pending workload, and so the workload continues running undisturbed", framework.SlowSpec, func() {
			job1 := testingjob.MakeJob(jobName+"1", ns.Name).
				WorkloadPriorityClass(highWorkloadPriorityClass.Name).
				Queue(kueue.LocalQueueName(devLocalQ.Name)).
				Request(corev1.ResourceCPU, "5").
				Obj()
			ginkgo.By("creating the first Job with high priority", func() {
				util.MustCreate(ctx, k8sClient, job1)
			})

			createdWorkload1 := &kueue.Workload{}
			wlLookupKey1 := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job1.Name, job1.UID), Namespace: ns.Name}

			ginkgo.By("verify the first workload is created with workload priority class", func() {
				util.ExpectWorkloadsWithWorkloadPriority(ctx, k8sClient, highWorkloadPriorityClass.Name, highPrio, wlLookupKey1)
			})

			ginkgo.By("checking that the first workload is admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey1, createdWorkload1)).Should(gomega.Succeed())
					g.Expect(createdWorkload1.Status.Conditions).Should(utiltesting.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			job2 := testingjob.MakeJob(jobName+"2", ns.Name).
				WorkloadPriorityClass(dynamicWorkloadPriorityClass.Name).
				Queue(kueue.LocalQueueName(devLocalQ.Name)).
				Request(corev1.ResourceCPU, "5").
				Obj()
			ginkgo.By("creating the second Job with high priority", func() {
				util.MustCreate(ctx, k8sClient, job2)
			})

			createdWorkload2 := &kueue.Workload{}
			wlLookupKey2 := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job2.Name, job2.UID), Namespace: ns.Name}

			ginkgo.By("verify the second workload is created with workload priority class", func() {
				util.ExpectWorkloadsWithWorkloadPriority(ctx, k8sClient, dynamicWorkloadPriorityClass.Name, highPrio, wlLookupKey2)
			})

			ginkgo.By("checking that the second workload is admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey2, createdWorkload2)).Should(gomega.Succeed())
					g.Expect(createdWorkload2.Status.Conditions).Should(utiltesting.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			job3 := testingjob.MakeJob(jobName+"3", ns.Name).
				WorkloadPriorityClass(midWorkloadPriorityClass.Name).
				Queue(kueue.LocalQueueName(devLocalQ.Name)).
				Request(corev1.ResourceCPU, "5").
				Obj()
			ginkgo.By("creating the third Job with low priority", func() {
				util.MustCreate(ctx, k8sClient, job3)
			})

			createdWorkload3 := &kueue.Workload{}
			wlLookupKey3 := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job3.Name, job3.UID), Namespace: ns.Name}

			ginkgo.By("verify the third workload is created with workload priority class", func() {
				util.ExpectWorkloadsWithWorkloadPriority(ctx, k8sClient, midWorkloadPriorityClass.Name, midPrio, wlLookupKey3)
			})

			ginkgo.By("checking that the third workload is not admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey3, createdWorkload3)).Should(gomega.Succeed())
					g.Expect(createdWorkload3.Status.Conditions).Should(utiltesting.HaveConditionStatusFalseAndReason(kueue.WorkloadQuotaReserved, "Pending"))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("updating the second workload’s WorkloadPriorityClass to mid", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					wpc := &kueue.WorkloadPriorityClass{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(dynamicWorkloadPriorityClass), wpc)).Should(gomega.Succeed())
					wpc.Value = midPrio
					g.Expect(k8sClient.Update(ctx, wpc)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("checking that the second workload’s WorkloadPriorityClass is updated when the job’s priority label changes", func() {
				util.ExpectWorkloadsWithWorkloadPriority(ctx, k8sClient, dynamicWorkloadPriorityClass.Name, midPrio, wlLookupKey2)
			})

			ginkgo.By("checking that the third workload is not admitted and the second workload is not evicted", func() {
				gomega.Consistently(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey2, createdWorkload2)).Should(gomega.Succeed())
					g.Expect(createdWorkload2.Status.Conditions).Should(utiltesting.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
					g.Expect(createdWorkload2.Status.Conditions).ShouldNot(utiltesting.HaveConditionStatusTrue(kueue.WorkloadEvicted))
					g.Expect(createdWorkload2.Status.Conditions).ShouldNot(utiltesting.HaveConditionStatusTrue(kueue.WorkloadPreempted))
					g.Expect(k8sClient.Get(ctx, wlLookupKey3, createdWorkload3)).Should(gomega.Succeed())
					g.Expect(createdWorkload3.Status.Conditions).Should(utiltesting.HaveConditionStatusFalseAndReason(kueue.WorkloadQuotaReserved, "Pending"))
				}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("Re-create a WorkloadPriorityClass", func() {
		var (
			recreatedWorkloadPriorityClass *kueue.WorkloadPriorityClass
			midWorkloadPriorityClass       *kueue.WorkloadPriorityClass
			highWorkloadPriorityClass      *kueue.WorkloadPriorityClass
			lowPrio                        = int32(10)
			midPrio                        = int32(100)
			highPrio                       = int32(1000)
		)

		ginkgo.BeforeEach(func() {
			midWorkloadPriorityClass = utiltestingapi.MakeWorkloadPriorityClass("mid").PriorityValue(midPrio).Obj()
			util.MustCreate(ctx, k8sClient, midWorkloadPriorityClass)

			highWorkloadPriorityClass = utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(highPrio).Obj()
			util.MustCreate(ctx, k8sClient, highWorkloadPriorityClass)

			recreatedWorkloadPriorityClass = utiltestingapi.MakeWorkloadPriorityClass("recreated").PriorityValue(highPrio).Obj()
			util.MustCreate(ctx, k8sClient, recreatedWorkloadPriorityClass)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(k8sClient.Delete(ctx, recreatedWorkloadPriorityClass)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Delete(ctx, midWorkloadPriorityClass)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Delete(ctx, highWorkloadPriorityClass)).To(gomega.Succeed())
		})

		ginkgo.It("WorkloadPriorityClass re-created with decreased priority causing a pending workload to be scheduled and preempt the running one", framework.SlowSpec, func() {
			job1 := testingjob.MakeJob(jobName+"1", ns.Name).
				WorkloadPriorityClass(highWorkloadPriorityClass.Name).
				Queue(kueue.LocalQueueName(devLocalQ.Name)).
				Request(corev1.ResourceCPU, "5").
				Obj()
			ginkgo.By("creating the first Job with high priority", func() {
				util.MustCreate(ctx, k8sClient, job1)
			})

			createdWorkload1 := &kueue.Workload{}
			wlLookupKey1 := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job1.Name, job1.UID), Namespace: ns.Name}

			ginkgo.By("verify the first workload is created with workload priority class", func() {
				util.ExpectWorkloadsWithWorkloadPriority(ctx, k8sClient, highWorkloadPriorityClass.Name, highPrio, wlLookupKey1)
			})

			ginkgo.By("checking that the first workload is admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey1, createdWorkload1)).Should(gomega.Succeed())
					g.Expect(createdWorkload1.Status.Conditions).Should(utiltesting.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			job2 := testingjob.MakeJob(jobName+"2", ns.Name).
				WorkloadPriorityClass(recreatedWorkloadPriorityClass.Name).
				Queue(kueue.LocalQueueName(devLocalQ.Name)).
				Request(corev1.ResourceCPU, "5").
				Obj()
			ginkgo.By("creating the second Job with high priority", func() {
				util.MustCreate(ctx, k8sClient, job2)
			})

			createdWorkload2 := &kueue.Workload{}
			wlLookupKey2 := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job2.Name, job2.UID), Namespace: ns.Name}

			ginkgo.By("verify the second workload is created with workload priority class", func() {
				util.ExpectWorkloadsWithWorkloadPriority(ctx, k8sClient, recreatedWorkloadPriorityClass.Name, highPrio, wlLookupKey2)
			})

			ginkgo.By("checking that the second workload is admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey2, createdWorkload2)).Should(gomega.Succeed())
					g.Expect(createdWorkload2.Status.Conditions).Should(utiltesting.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			job3 := testingjob.MakeJob(jobName+"3", ns.Name).
				WorkloadPriorityClass(midWorkloadPriorityClass.Name).
				Queue(kueue.LocalQueueName(devLocalQ.Name)).
				Request(corev1.ResourceCPU, "5").
				Obj()
			ginkgo.By("creating the third Job with mid priority", func() {
				util.MustCreate(ctx, k8sClient, job3)
			})

			createdWorkload3 := &kueue.Workload{}
			wlLookupKey3 := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job3.Name, job3.UID), Namespace: ns.Name}

			ginkgo.By("verify the third workload is created with workload priority class", func() {
				util.ExpectWorkloadsWithWorkloadPriority(ctx, k8sClient, midWorkloadPriorityClass.Name, midPrio, wlLookupKey3)
			})

			ginkgo.By("checking that the third workload is not admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey3, createdWorkload3)).Should(gomega.Succeed())
					g.Expect(createdWorkload3.Status.Conditions).Should(utiltesting.HaveConditionStatusFalseAndReason(kueue.WorkloadQuotaReserved, "Pending"))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("re-creating a WorkloadPriorityClass priority high => low", func() {
				gomega.Expect(k8sClient.Delete(ctx, recreatedWorkloadPriorityClass)).Should(gomega.Succeed())
				recreatedWorkloadPriorityClass = utiltestingapi.MakeWorkloadPriorityClass("recreated").PriorityValue(lowPrio).Obj()
				util.MustCreate(ctx, k8sClient, recreatedWorkloadPriorityClass)
			})

			ginkgo.By("checking that the third workload is admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey3, createdWorkload3)).Should(gomega.Succeed())
					g.Expect(createdWorkload3.Status.Conditions).Should(utiltesting.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("checking that the second workload is evicted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey2, createdWorkload2)).Should(gomega.Succeed())
					g.Expect(createdWorkload2.Status.Conditions).Should(utiltesting.HaveConditionStatusTrue(kueue.WorkloadEvicted))
					g.Expect(createdWorkload2.Status.Conditions).Should(utiltesting.HaveConditionStatusTrue(kueue.WorkloadPreempted))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.It("Should schedule jobs as they fit in their ClusterQueue", framework.SlowSpec, func() {
		ginkgo.By("checking the first prod job starts")
		prodJob1 := testingjob.MakeJob("prod-job1", ns.Name).Queue(kueue.LocalQueueName(prodLocalQ.Name)).Request(corev1.ResourceCPU, "2").Obj()
		util.MustCreate(ctx, k8sClient, prodJob1)
		lookupKey1 := types.NamespacedName{Name: prodJob1.Name, Namespace: prodJob1.Namespace}
		createdProdJob1 := &batchv1.Job{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, lookupKey1, createdProdJob1)).Should(gomega.Succeed())
			g.Expect(createdProdJob1.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		gomega.Expect(createdProdJob1.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))
		util.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 0)
		util.ExpectAdmittedWorkloadsTotalMetric(prodClusterQ, "", 1)

		ginkgo.By("checking a second no-fit prod job does not start")
		prodJob2 := testingjob.MakeJob("prod-job2", ns.Name).Queue(kueue.LocalQueueName(prodLocalQ.Name)).Request(corev1.ResourceCPU, "5").Obj()
		util.MustCreate(ctx, k8sClient, prodJob2)
		lookupKey2 := types.NamespacedName{Name: prodJob2.Name, Namespace: prodJob2.Namespace}
		createdProdJob2 := &batchv1.Job{}
		gomega.Consistently(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, lookupKey2, createdProdJob2)).Should(gomega.Succeed())
			g.Expect(createdProdJob2.Spec.Suspend).Should(gomega.Equal(ptr.To(true)))
		}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
		util.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 1)
		util.ExpectAdmittedWorkloadsTotalMetric(prodClusterQ, "", 1)

		ginkgo.By("checking a dev job starts")
		devJob := testingjob.MakeJob("dev-job", ns.Name).Queue(kueue.LocalQueueName(devLocalQ.Name)).Request(corev1.ResourceCPU, "5").Obj()
		util.MustCreate(ctx, k8sClient, devJob)
		createdDevJob := &batchv1.Job{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(devJob), createdDevJob)).Should(gomega.Succeed())
			g.Expect(createdDevJob.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		gomega.Expect(createdDevJob.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(spotUntaintedFlavor.Name))
		util.ExpectPendingWorkloadsMetric(devClusterQ, 0, 0)
		util.ExpectAdmittedWorkloadsTotalMetric(devClusterQ, "", 1)

		ginkgo.By("checking the second prod job starts when the first finishes")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, lookupKey1, createdProdJob1)).Should(gomega.Succeed())
			now := metav1.Now()
			createdProdJob1.Status.StartTime = ptr.To(now)
			createdProdJob1.Status.CompletionTime = ptr.To(now)
			createdProdJob1.Status.Conditions = append(createdProdJob1.Status.Conditions,
				batchv1.JobCondition{
					Type:               batchv1.JobSuccessCriteriaMet,
					Status:             corev1.ConditionTrue,
					LastProbeTime:      now,
					LastTransitionTime: now,
				},
				batchv1.JobCondition{
					Type:               batchv1.JobComplete,
					Status:             corev1.ConditionTrue,
					LastProbeTime:      now,
					LastTransitionTime: now,
				},
			)
			g.Expect(k8sClient.Status().Update(ctx, createdProdJob1)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, lookupKey2, createdProdJob2)).Should(gomega.Succeed())
			g.Expect(createdProdJob2.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		gomega.Expect(createdProdJob2.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))
		util.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 0)
		util.ExpectReservingActiveWorkloadsMetric(prodClusterQ, 1)
	})

	ginkgo.It("Should unsuspend job iff localQueue is in the same namespace", func() {
		ginkgo.By("create another namespace")
		ns2 := util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-")
		defer func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns2)).To(gomega.Succeed())
		}()

		ginkgo.By("create a localQueue located in a different namespace as the job")
		ns2LocalQ := utiltestingapi.MakeLocalQueue("local-queue", ns2.Name).Obj()
		ns2LocalQ.Spec.ClusterQueue = kueue.ClusterQueueReference(prodClusterQ.Name)

		ginkgo.By("create a job")
		prodJob := testingjob.MakeJob("prod-job", ns.Name).Queue(kueue.LocalQueueName(ns2LocalQ.Name)).Request(corev1.ResourceCPU, "2").Obj()
		util.MustCreate(ctx, k8sClient, prodJob)

		ginkgo.By("job should be suspend")
		lookupKey := types.NamespacedName{Name: prodJob.Name, Namespace: prodJob.Namespace}
		createdProdJob := &batchv1.Job{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, lookupKey, createdProdJob)).Should(gomega.Succeed())
			g.Expect(createdProdJob.Spec.Suspend).Should(gomega.Equal(ptr.To(true)))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("creating another localQueue of the same name and in the same namespace as the job")
		localQ := utiltestingapi.MakeLocalQueue(ns2LocalQ.Name, ns.Name).ClusterQueue(prodClusterQ.Name).Obj()
		util.MustCreate(ctx, k8sClient, localQ)

		ginkgo.By("job should be unsuspended and NodeSelector properly set")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, lookupKey, createdProdJob)).Should(gomega.Succeed())
			g.Expect(createdProdJob.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		runningSelector := maps.Clone(createdProdJob.Spec.Template.Spec.NodeSelector)

		gomega.Expect(runningSelector).To(gomega.Equal(map[string]string{instanceKey: "on-demand"}))
	})

	ginkgo.When("The workload's admission is removed", func() {
		ginkgo.It("Should restore the original node selectors", func() {
			localQueue := utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(prodClusterQ.Name).Obj()
			job := testingjob.MakeJob(jobName, ns.Name).Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "2").Obj()
			lookupKey := types.NamespacedName{Name: job.Name, Namespace: job.Namespace}
			createdJob := &batchv1.Job{}

			ginkgo.By("create a job", func() {
				util.MustCreate(ctx, k8sClient, job)
			})

			ginkgo.By("job should be suspend", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
					g.Expect(createdJob.Spec.Suspend).Should(gomega.Equal(ptr.To(true)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			// backup the podSet's node selector
			originalNodeSelector := createdJob.Spec.Template.Spec.NodeSelector

			ginkgo.By("create a localQueue", func() {
				util.MustCreate(ctx, k8sClient, localQueue)
			})

			ginkgo.By("job should be unsuspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
					g.Expect(createdJob.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("the node selector should be updated", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
					g.Expect(createdJob.Spec.Template.Spec.NodeSelector).ShouldNot(gomega.Equal(originalNodeSelector))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("delete the localQueue to prevent readmission", func() {
				gomega.Expect(util.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
			})

			ginkgo.By("clear the workload's admission to stop the job", func() {
				wlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: job.Namespace}
				wl := utiltestingapi.MakeWorkload(wlKey.Name, wlKey.Namespace).Obj()
				util.SetQuotaReservation(ctx, k8sClient, wlKey, nil)
				util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, wl)
			})

			ginkgo.By("the node selector should be restored", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
					g.Expect(createdJob.Spec.Template.Spec.NodeSelector).Should(gomega.Equal(originalNodeSelector))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("The workload is deleted while admitted", func() {
		ginkgo.It("Should restore the original node selectors", func() {
			localQueue := utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(prodClusterQ.Name).Obj()
			job := testingjob.MakeJob(jobName, ns.Name).Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "2").Suspend(false).Obj()
			lookupKey := types.NamespacedName{Name: job.Name, Namespace: job.Namespace}
			createdJob := &batchv1.Job{}

			ginkgo.By("create a job", func() {
				util.MustCreate(ctx, k8sClient, job)
			})

			ginkgo.By("job should be suspend", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
					g.Expect(createdJob.Spec.Suspend).Should(gomega.Equal(ptr.To(true)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			// backup the podSet's node selector
			originalNodeSelector := createdJob.Spec.Template.Spec.NodeSelector

			ginkgo.By("create a localQueue", func() {
				util.MustCreate(ctx, k8sClient, localQueue)
			})

			ginkgo.By("job should be unsuspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
					g.Expect(createdJob.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("the node selector should be updated", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
					g.Expect(createdJob.Spec.Template.Spec.NodeSelector).ShouldNot(gomega.Equal(originalNodeSelector))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("delete the localQueue to prevent readmission", func() {
				gomega.Expect(util.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
			})

			ginkgo.By("deleting the workload", func() {
				wl := &kueue.Workload{}
				wlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: job.Namespace}
				gomega.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
				gomega.Expect(k8sClient.Delete(ctx, wl)).Should(gomega.Succeed())
			})

			ginkgo.By("the node selector should be restored", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
					g.Expect(createdJob.Spec.Template.Spec.NodeSelector).Should(gomega.Equal(originalNodeSelector))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("The job is deleted while admitted", func() {
		ginkgo.It("Its workload finalizer should be removed", func() {
			localQueue := utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(prodClusterQ.Name).Obj()
			job := testingjob.MakeJob(jobName, ns.Name).Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "2").Suspend(false).Obj()
			lookupKey := types.NamespacedName{Name: job.Name, Namespace: job.Namespace}
			createdJob := &batchv1.Job{}

			ginkgo.By("create a job", func() {
				util.MustCreate(ctx, k8sClient, job)
			})

			ginkgo.By("job should be suspend", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
					g.Expect(createdJob.Spec.Suspend).Should(gomega.Equal(ptr.To(true)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("create a localQueue", func() {
				util.MustCreate(ctx, k8sClient, localQueue)
			})

			ginkgo.By("job should be unsuspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
					g.Expect(createdJob.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			wlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: job.Namespace}
			ginkgo.By("checking the finalizer is set", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					wl := &kueue.Workload{}
					g.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
					g.Expect(wl.Finalizers).Should(gomega.ContainElement(kueue.ResourceInUseFinalizerName))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("deleting the job", func() {
				gomega.Expect(k8sClient.Delete(ctx, job)).Should(gomega.Succeed())
			})

			ginkgo.By("checking that its workloads finalizer is removed", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					wl := &kueue.Workload{}
					g.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
					g.Expect(wl.Finalizers).ShouldNot(gomega.ContainElement(kueue.ResourceInUseFinalizerName))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.It("Should allow reclaim of resources that are no longer needed", framework.SlowSpec, func() {
		job1 := testingjob.MakeJob("job1", ns.Name).Queue(kueue.LocalQueueName(prodLocalQ.Name)).
			Request(corev1.ResourceCPU, "2").
			Completions(5).
			Parallelism(2).
			Obj()
		lookupKey1 := types.NamespacedName{Name: job1.Name, Namespace: job1.Namespace}

		ginkgo.By("checking the first job starts", func() {
			util.MustCreate(ctx, k8sClient, job1)
			createdJob1 := &batchv1.Job{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, lookupKey1, createdJob1)).Should(gomega.Succeed())
				g.Expect(createdJob1.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			gomega.Expect(createdJob1.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))
			util.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 0)
			util.ExpectAdmittedWorkloadsTotalMetric(prodClusterQ, "", 1)
		})

		job2 := testingjob.MakeJob("job2", ns.Name).Queue(kueue.LocalQueueName(prodLocalQ.Name)).Request(corev1.ResourceCPU, "3").Obj()
		lookupKey2 := types.NamespacedName{Name: job2.Name, Namespace: job2.Namespace}

		ginkgo.By("checking a second no-fit job does not start", func() {
			util.MustCreate(ctx, k8sClient, job2)
			createdJob2 := &batchv1.Job{}
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, lookupKey2, createdJob2)).Should(gomega.Succeed())
				g.Expect(createdJob2.Spec.Suspend).Should(gomega.Equal(ptr.To(true)))
			}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
			util.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 1)
			util.ExpectReservingActiveWorkloadsMetric(prodClusterQ, 1)
		})

		ginkgo.By("checking the second job starts when the first has less then to completions to go", func() {
			createdJob1 := &batchv1.Job{}
			gomega.Expect(k8sClient.Get(ctx, lookupKey1, createdJob1)).Should(gomega.Succeed())
			createdJob1.Status.Succeeded = 4
			gomega.Expect(k8sClient.Status().Update(ctx, createdJob1)).Should(gomega.Succeed())

			wl := &kueue.Workload{}
			wlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job1.Name, job1.UID), Namespace: job1.Namespace}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
				g.Expect(wl.Status.ReclaimablePods).Should(gomega.BeComparableTo([]kueue.ReclaimablePod{{
					Name:  kueue.DefaultPodSetName,
					Count: 1,
				}}))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			createdJob2 := &batchv1.Job{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, lookupKey2, createdJob2)).Should(gomega.Succeed())
				g.Expect(createdJob2.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			gomega.Expect(createdJob2.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))

			util.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 0)
			util.ExpectReservingActiveWorkloadsMetric(prodClusterQ, 2)
		})
	})

	ginkgo.It("Should readmit preempted Job with priorityClass in alternative flavor", func() {
		highPriorityClass := utiltesting.MakePriorityClass("high").PriorityValue(100).Obj()
		util.MustCreate(ctx, k8sClient, highPriorityClass)
		ginkgo.DeferCleanup(func() {
			gomega.Expect(k8sClient.Delete(ctx, highPriorityClass)).To(gomega.Succeed())
		})

		lowJobKey := types.NamespacedName{Name: "low", Namespace: ns.Name}
		ginkgo.By("Low priority job is unsuspended and has nodeSelector", func() {
			job := testingjob.MakeJob("low", ns.Name).
				Queue(kueue.LocalQueueName(devLocalQ.Name)).
				Parallelism(5).
				Request(corev1.ResourceCPU, "1").
				Obj()
			util.MustCreate(ctx, k8sClient, job)

			util.ExpectJobUnsuspendedWithNodeSelectors(ctx, k8sClient, lowJobKey, map[string]string{
				instanceKey: "spot-untainted",
			})
		})

		ginkgo.By("High priority job preemtps low priority job", func() {
			job := testingjob.MakeJob("high", ns.Name).
				Queue(kueue.LocalQueueName(devLocalQ.Name)).
				PriorityClass("high").
				Parallelism(5).
				Request(corev1.ResourceCPU, "1").
				NodeSelector(instanceKey, "spot-untainted"). // target the same flavor to cause preemption
				Obj()
			util.MustCreate(ctx, k8sClient, job)

			highJobKey := types.NamespacedName{Name: "high", Namespace: ns.Name}
			util.ExpectJobUnsuspendedWithNodeSelectors(ctx, k8sClient, highJobKey, map[string]string{
				instanceKey: "spot-untainted",
			})
		})

		ginkgo.By("Preempted job should be admitted on second flavor", func() {
			util.ExpectJobUnsuspendedWithNodeSelectors(ctx, k8sClient, lowJobKey, map[string]string{
				instanceKey: "on-demand",
			})
		})
	})

	ginkgo.It("Should readmit preempted Job with workloadPriorityClass in alternative flavor", func() {
		highWorkloadPriorityClass := utiltestingapi.MakeWorkloadPriorityClass("high-workload").PriorityValue(100).Obj()
		util.MustCreate(ctx, k8sClient, highWorkloadPriorityClass)
		ginkgo.DeferCleanup(func() {
			gomega.Expect(k8sClient.Delete(ctx, highWorkloadPriorityClass)).To(gomega.Succeed())
		})

		lowJobKey := types.NamespacedName{Name: "low", Namespace: ns.Name}
		ginkgo.By("Low priority job is unsuspended and has nodeSelector", func() {
			job := testingjob.MakeJob("low", ns.Name).
				Queue(kueue.LocalQueueName(devLocalQ.Name)).
				Parallelism(5).
				Request(corev1.ResourceCPU, "1").
				Obj()
			util.MustCreate(ctx, k8sClient, job)

			util.ExpectJobUnsuspendedWithNodeSelectors(ctx, k8sClient, lowJobKey, map[string]string{
				instanceKey: "spot-untainted",
			})
		})

		ginkgo.By("High priority job preemtps low priority job", func() {
			job := testingjob.MakeJob("high", ns.Name).
				Queue(kueue.LocalQueueName(devLocalQ.Name)).
				WorkloadPriorityClass("high-workload").
				Parallelism(5).
				Request(corev1.ResourceCPU, "1").
				NodeSelector(instanceKey, "spot-untainted"). // target the same flavor to cause preemption
				Obj()
			util.MustCreate(ctx, k8sClient, job)

			highJobKey := types.NamespacedName{Name: "high", Namespace: ns.Name}
			util.ExpectJobUnsuspendedWithNodeSelectors(ctx, k8sClient, highJobKey, map[string]string{
				instanceKey: "spot-untainted",
			})
		})

		ginkgo.By("Preempted job should be admitted on second flavor", func() {
			util.ExpectJobUnsuspendedWithNodeSelectors(ctx, k8sClient, lowJobKey, map[string]string{
				instanceKey: "on-demand",
			})
		})
	})

	ginkgo.It("Should schedule jobs with partial admission", framework.SlowSpec, func() {
		job1 := testingjob.MakeJob("job1", ns.Name).
			Queue(kueue.LocalQueueName(prodLocalQ.Name)).
			Parallelism(5).
			Completions(6).
			Request(corev1.ResourceCPU, "2").
			Obj()
		jobKey := types.NamespacedName{Name: job1.Name, Namespace: job1.Namespace}

		ginkgo.By("creating the job")
		util.MustCreate(ctx, k8sClient, job1)
		wlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job1.Name, job1.UID), Namespace: job1.Namespace}

		createdJob := &batchv1.Job{}
		ginkgo.By("the job should stay suspended", func() {
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, jobKey, createdJob)).Should(gomega.Succeed())
				g.Expect(createdJob.Spec.Suspend).Should(gomega.Equal(ptr.To(true)))
			}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
		})

		ginkgo.By("enable partial admission", func() {
			gomega.Expect(k8sClient.Get(ctx, jobKey, createdJob)).Should(gomega.Succeed())
			if createdJob.Annotations == nil {
				createdJob.Annotations = map[string]string{
					workloadjob.JobMinParallelismAnnotation: "1",
				}
			} else {
				createdJob.Annotations[workloadjob.JobMinParallelismAnnotation] = "1"
			}

			gomega.Expect(k8sClient.Update(ctx, createdJob)).Should(gomega.Succeed())
		})

		wl := &kueue.Workload{}
		ginkgo.By("the job should be unsuspended with a lower parallelism", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, jobKey, createdJob)).Should(gomega.Succeed())
				g.Expect(createdJob.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			gomega.Expect(*createdJob.Spec.Parallelism).To(gomega.BeEquivalentTo(2))

			gomega.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
			gomega.Expect(wl.Spec.PodSets[0].MinCount).ToNot(gomega.BeNil())
			gomega.Expect(*wl.Spec.PodSets[0].MinCount).To(gomega.BeEquivalentTo(1))
		})

		ginkgo.By("checking the clusterqueue usage", func() {
			updateCq := &kueue.ClusterQueue{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(prodClusterQ), updateCq)).Should(gomega.Succeed())
				g.Expect(updateCq.Status.FlavorsUsage).To(gomega.ContainElement(kueue.FlavorUsage{
					Name: "on-demand",
					Resources: []kueue.ResourceUsage{
						{
							Name:     corev1.ResourceCPU,
							Total:    resource.MustParse("4"),
							Borrowed: resource.MustParse("0"),
						},
					},
				}))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("delete the localQueue to prevent readmission", func() {
			gomega.Expect(util.DeleteObject(ctx, k8sClient, prodLocalQ)).Should(gomega.Succeed())
		})

		ginkgo.By("clear the workloads admission to stop the job", func() {
			util.SetQuotaReservation(ctx, k8sClient, wlKey, nil)
			util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, wl)
		})

		ginkgo.By("job should be suspended and its parallelism restored", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, jobKey, createdJob)).Should(gomega.Succeed())
				g.Expect(createdJob.Annotations[workloadjob.StoppingAnnotation]).ToNot(gomega.Equal("true"))
				g.Expect(ptr.Deref(createdJob.Spec.Suspend, false)).To(gomega.BeTrue(), "the job should be suspended")
				g.Expect(ptr.Deref(createdJob.Spec.Parallelism, 0)).To(gomega.BeEquivalentTo(5))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should set the flavor's node selectors if the job is admitted by pods count only", func() {
		ginkgo.By("Creating a job with no requests, will set the resource flavors selectors when admitted ", func() {
			job := testingjob.MakeJob("job", ns.Name).
				Queue(kueue.LocalQueueName(podsLocalQ.Name)).
				Parallelism(2).
				Obj()
			util.MustCreate(ctx, k8sClient, job)
			util.ExpectJobUnsuspendedWithNodeSelectors(ctx, k8sClient, client.ObjectKeyFromObject(job), map[string]string{
				instanceKey: "on-demand",
			})
		})
	})

	ginkgo.It("Should schedule updated job and update the workload", func() {
		job := testingjob.MakeJob(jobName, ns.Name).Queue(kueue.LocalQueueName(prodLocalQ.Name)).Request(corev1.ResourceCPU, "3").Parallelism(2).Suspend(false).Obj()
		lookupKey := types.NamespacedName{Name: job.Name, Namespace: job.Namespace}
		createdJob := &batchv1.Job{}

		ginkgo.By("creating the job that doesn't fit", func() {
			util.MustCreate(ctx, k8sClient, job)
		})

		ginkgo.By("job should be suspend", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
				g.Expect(createdJob.Spec.Suspend).Should(gomega.Equal(ptr.To(true)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: ns.Name}
		createdWorkload := util.AwaitAndVerifyCreatedWorkload(ctx, k8sClient, wlLookupKey, createdJob)
		createdTime := createdWorkload.CreationTimestamp

		ginkgo.By("updating the job", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
				createdJob.Spec.Parallelism = ptr.To[int32](1)
				g.Expect(k8sClient.Update(ctx, createdJob)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		createdWorkload = util.AwaitAndVerifyCreatedWorkload(ctx, k8sClient, wlLookupKey, createdJob)

		ginkgo.By("updated job should be unsuspended", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
				g.Expect(createdJob.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("updated workload should have the same created timestamp", func() {
			gomega.Expect(createdWorkload.CreationTimestamp).Should(gomega.Equal(createdTime))
		})
	})

	ginkgo.When("Suspend a running Job without requeuing through Workload's spec.active field", func() {
		ginkgo.It("Should not readmit a job to the queue after Active is changed to false", func() {
			sampleJob := testingjob.MakeJob("job1", ns.Name).Queue(kueue.LocalQueueName(prodLocalQ.Name)).Request(corev1.ResourceCPU, "2").Obj()
			lookupKey1 := types.NamespacedName{Name: sampleJob.Name, Namespace: sampleJob.Namespace}
			wll := &kueue.Workload{}

			ginkgo.By("checking the job starts")
			util.MustCreate(ctx, k8sClient, sampleJob)

			createdJob := &batchv1.Job{}
			wlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(sampleJob.Name, sampleJob.UID), Namespace: sampleJob.Namespace}

			ginkgo.By("checking the job's suspend field is false and the workload is admitted")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, lookupKey1, sampleJob)).Should(gomega.Succeed())
				g.Expect(sampleJob.Spec.Suspend).To(gomega.Equal(ptr.To(false)))
				g.Expect(k8sClient.Get(ctx, wlKey, wll)).Should(gomega.Succeed())
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wll)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Change the Active field to suspend the job and check the job remains suspended and the workload unadmitted")
			// Changing Active to false
			wll.Spec.Active = ptr.To(false)
			gomega.Expect(k8sClient.Update(ctx, wll)).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wll)).Should(gomega.Succeed())
				g.Expect(wll.Spec.Active).ShouldNot(gomega.BeNil())
				g.Expect(*wll.Spec.Active).Should(gomega.BeFalse())
				g.Expect(wll.Status.Conditions).To(gomega.ContainElements(
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadQuotaReserved,
						Status:  metav1.ConditionFalse,
						Reason:  "Pending",
						Message: "The workload is deactivated",
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadAdmitted,
						Status:  metav1.ConditionFalse,
						Reason:  "NoReservation",
						Message: "The workload has no reservation",
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadDeactivated,
						Message: "The workload is deactivated",
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadRequeued,
						Status:  metav1.ConditionFalse,
						Reason:  kueue.WorkloadDeactivated,
						Message: "The workload is deactivated",
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
				))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("checking a second job starts after first job is suspended")
			sampleJob2 := testingjob.MakeJob("job2", ns.Name).Queue(kueue.LocalQueueName(prodLocalQ.Name)).Request(corev1.ResourceCPU, "2").Obj()

			lookupKey2 := types.NamespacedName{Name: sampleJob2.Name, Namespace: sampleJob2.Namespace}
			wll2 := &kueue.Workload{}

			util.MustCreate(ctx, k8sClient, sampleJob2)
			wlKey2 := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(sampleJob2.Name, sampleJob2.UID), Namespace: sampleJob2.Namespace}

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, lookupKey2, sampleJob2)).Should(gomega.Succeed())
				g.Expect(sampleJob2.Spec.Suspend).To(gomega.Equal(ptr.To(false)))
				g.Expect(k8sClient.Get(ctx, wlKey2, wll2)).Should(gomega.Succeed())
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wll2)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			// Checking job stays suspended
			ginkgo.By("checking job is suspended")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, lookupKey1, createdJob)).Should(gomega.Succeed())
				g.Expect(createdJob.Spec.Suspend).Should(gomega.Equal(ptr.To(true)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("checking the first job and workload stay suspended and unadmitted")
			gomega.Consistently(func(g gomega.Gomega) {
				// Job should stay pending
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: sampleJob.Name, Namespace: sampleJob.Namespace}, createdJob)).
					Should(gomega.Succeed())
				g.Expect(createdJob.Spec.Suspend).To(gomega.Equal(ptr.To(true)))
				// Workload should get unadmitted
				g.Expect(k8sClient.Get(ctx, wlKey, wll)).Should(gomega.Succeed())
				util.ExpectWorkloadsToBePending(ctx, k8sClient, wll)
				// Workload should stay pending
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wll), wll)).Should(gomega.Succeed())
				// Should have Evicted condition
				g.Expect(wll.Status.Conditions).Should(utiltesting.HaveConditionStatusTrue(kueue.WorkloadEvicted))
			}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())

			ginkgo.By("checking the first job becomes unsuspended after we update the Active field back to true")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wll)).Should(gomega.Succeed())
				wll.Spec.Active = ptr.To(true)
				g.Expect(k8sClient.Update(ctx, wll)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wll)).Should(gomega.Succeed())
				g.Expect(wll.Spec.Active).ShouldNot(gomega.BeNil())
				g.Expect(*wll.Spec.Active).Should(gomega.BeTrue())
				g.Expect(wll.Status.Conditions).To(gomega.ContainElements(
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadQuotaReserved,
						Status:  metav1.ConditionTrue,
						Reason:  "QuotaReserved",
						Message: "Quota reserved in ClusterQueue prod-cq",
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadAdmitted,
						Status:  metav1.ConditionTrue,
						Reason:  "Admitted",
						Message: "The workload is admitted",
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionFalse,
						Reason:  "QuotaReserved",
						Message: "Previously: The workload is deactivated",
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadRequeued,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadReactivated,
						Message: "The workload was reactivated",
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
				))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: sampleJob.Name, Namespace: sampleJob.Namespace}, createdJob)).
					Should(gomega.Succeed())
				g.Expect(sampleJob.Spec.Suspend).To(gomega.Equal(ptr.To(false)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Shouldn't admit deactivated Workload after manager restart", framework.SlowSpec, func() {
		job := testingjob.MakeJob("job", ns.Name).
			Queue(kueue.LocalQueueName(prodLocalQ.Name)).
			Request(corev1.ResourceCPU, "2").
			Obj()

		ginkgo.By("Creating a Job", func() {
			util.MustCreate(ctx, k8sClient, job)
		})

		wl := &kueue.Workload{}
		wlKey := types.NamespacedName{
			Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
			Namespace: job.Namespace,
		}

		ginkgo.By("Checking that the Workload is admitted", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(wl)).To(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			util.ExpectAdmittedWorkloadsTotalMetric(prodClusterQ, "", 1)
		})

		ginkgo.By("Deactivate the Workload", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
				wl.Spec.Active = ptr.To(false)
				g.Expect(k8sClient.Update(ctx, wl)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking that the Workload is deactivated and evicted", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
				g.Expect(workload.IsActive(wl)).To(gomega.BeFalse())
				g.Expect(workload.IsEvicted(wl)).To(gomega.BeTrue())
				g.Expect(workload.IsAdmitted(wl)).To(gomega.BeFalse())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Restarting the manager", func() {
			restartManager()
		})

		ginkgo.By("Checking that the Workload is not admitted after restart the manager", func() {
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(wl)).To(gomega.BeFalse())
				g.Expect(workload.IsEvicted(wl)).To(gomega.BeTrue())
				// Using short intervals to make it likely to fail if the conditions flip
			}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
			// NOTE: controller restart in integration tests does not reset the metrics
			util.ExpectAdmittedWorkloadsTotalMetric(prodClusterQ, "", 1)
		})
	})

	ginkgo.When("the cluster queue has admission check and workload priority class", func() {
		var (
			highPriorityClass *kueue.WorkloadPriorityClass
			lowPriorityClass  *kueue.WorkloadPriorityClass
			admissionCheck    *kueue.AdmissionCheck
			resourceFlavor    *kueue.ResourceFlavor
			clusterQueue      *kueue.ClusterQueue
			localQueue        *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {
			highPriorityClass = utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(highPriorityValue).Obj()
			util.MustCreate(ctx, k8sClient, highPriorityClass)

			lowPriorityClass = utiltestingapi.MakeWorkloadPriorityClass("low").PriorityValue(priorityValue).Obj()
			util.MustCreate(ctx, k8sClient, lowPriorityClass)

			admissionCheck = utiltestingapi.MakeAdmissionCheck("ac").ControllerName("ac-controller").Obj()
			util.MustCreate(ctx, k8sClient, admissionCheck)

			util.SetAdmissionCheckActive(ctx, k8sClient, admissionCheck, metav1.ConditionTrue)

			resourceFlavor = utiltestingapi.MakeResourceFlavor("rf").NodeLabel(instanceKey, "on-demand").Obj()
			util.MustCreate(ctx, k8sClient, resourceFlavor)

			clusterQueue = utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(resourceFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj(),
				).
				AdmissionChecks("ac").
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
				}).
				Obj()
			util.MustCreate(ctx, k8sClient, clusterQueue)

			localQueue = utiltestingapi.MakeLocalQueue("lq", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			util.MustCreate(ctx, k8sClient, localQueue)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, admissionCheck, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, lowPriorityClass, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, highPriorityClass, true)
		})

		ginkgo.It("Should readmit preempted Job with priorityClass in alternative flavor with admission check", func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.LocalQueueMetrics, true)

			lowJob := testingjob.MakeJob("low", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				WorkloadPriorityClass(lowPriorityClass.Name).
				Parallelism(5).
				Request(corev1.ResourceCPU, "1").
				Suspend(false).
				Obj()
			ginkgo.By("Creating a low priority job", func() {
				util.MustCreate(ctx, k8sClient, lowJob)
			})

			ginkgo.By("Verifying the low priority job is suspended", func() {
				createdJob := &batchv1.Job{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lowJob), createdJob)).Should(gomega.Succeed())
					g.Expect(createdJob.Spec.Suspend).Should(gomega.Equal(ptr.To(true)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			createdLowWorkload := &kueue.Workload{}
			lowWlLookupKey := types.NamespacedName{
				Name:      workloadjob.GetWorkloadNameForJob(lowJob.Name, lowJob.UID),
				Namespace: ns.Name,
			}
			ginkgo.By("Verifying the created low priority workload is quota reserved", func() {
				util.ExpectWorkloadsToHaveQuotaReservationByKey(ctx, k8sClient, clusterQueue.Name, lowWlLookupKey)
			})

			ginkgo.By("Setting ready for admission checks in low priority workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, lowWlLookupKey, createdLowWorkload)).To(gomega.Succeed())
					workload.SetAdmissionCheckState(&createdLowWorkload.Status.AdmissionChecks, kueue.AdmissionCheckState{
						Name:  kueue.AdmissionCheckReference(admissionCheck.Name),
						State: kueue.CheckStateReady,
					}, util.RealClock)
					g.Expect(k8sClient.Status().Update(ctx, createdLowWorkload)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			util.ExpectAdmissionChecksWaitTimeMetric(clusterQueue, lowPriorityClass.Name, 1)
			util.ExpectAdmissionChecksWaitTimeMetric(clusterQueue, highPriorityClass.Name, 0)
			util.ExpectLQAdmissionChecksWaitTimeMetric(localQueue, lowPriorityClass.Name, 1)
			util.ExpectLQAdmissionChecksWaitTimeMetric(localQueue, highPriorityClass.Name, 0)

			highJob := testingjob.MakeJob("high", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				WorkloadPriorityClass(highPriorityClass.Name).
				Parallelism(5).
				Request(corev1.ResourceCPU, "1").
				Suspend(false).
				Obj()
			ginkgo.By("Creating a high priority job", func() {
				util.MustCreate(ctx, k8sClient, highJob)
			})

			ginkgo.By("Verifying the high priority job is suspended", func() {
				createdJob := &batchv1.Job{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(highJob), createdJob)).Should(gomega.Succeed())
					g.Expect(createdJob.Spec.Suspend).Should(gomega.Equal(ptr.To(true)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			createdHighWorkload := &kueue.Workload{}
			highWlLookupKey := types.NamespacedName{
				Name:      workloadjob.GetWorkloadNameForJob(highJob.Name, highJob.UID),
				Namespace: ns.Name,
			}
			ginkgo.By("Verifying the created high priority workload is quota reserved", func() {
				util.ExpectWorkloadsToHaveQuotaReservationByKey(ctx, k8sClient, clusterQueue.Name, highWlLookupKey)
			})

			ginkgo.By("Setting ready for admission checks in high priority workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, highWlLookupKey, createdHighWorkload)).To(gomega.Succeed())
					workload.SetAdmissionCheckState(&createdHighWorkload.Status.AdmissionChecks, kueue.AdmissionCheckState{
						Name:  kueue.AdmissionCheckReference(admissionCheck.Name),
						State: kueue.CheckStateReady,
					}, util.RealClock)
					g.Expect(k8sClient.Status().Update(ctx, createdHighWorkload)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			util.ExpectAdmissionChecksWaitTimeMetric(clusterQueue, lowPriorityClass.Name, 1)
			util.ExpectAdmissionChecksWaitTimeMetric(clusterQueue, highPriorityClass.Name, 1)
			util.ExpectLQAdmissionChecksWaitTimeMetric(localQueue, lowPriorityClass.Name, 1)
			util.ExpectLQAdmissionChecksWaitTimeMetric(localQueue, highPriorityClass.Name, 1)
		})
	})
})

var _ = ginkgo.Describe("Job controller interacting with Workload controller when waitForPodsReady is enabled", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		backoffBaseSeconds              int32
		backoffLimitCount               *int32
		waitForPodsReadyTimeout         metav1.Duration
		waitForPodsReadyRecoveryTimeout *metav1.Duration
		ns                              *corev1.Namespace
		fl                              *kueue.ResourceFlavor
		cq                              *kueue.ClusterQueue
		lq                              *kueue.LocalQueue
	)

	ginkgo.JustBeforeEach(func() {
		waitForPodsReady := &configapi.WaitForPodsReady{
			BlockAdmission: ptr.To(true),
			Timeout:        waitForPodsReadyTimeout,
			RequeuingStrategy: &configapi.RequeuingStrategy{
				Timestamp:          ptr.To(configapi.EvictionTimestamp),
				BackoffBaseSeconds: ptr.To(backoffBaseSeconds),
				BackoffLimitCount:  backoffLimitCount,
			},
			RecoveryTimeout: waitForPodsReadyRecoveryTimeout,
		}
		fwk.StartManager(ctx, cfg, managerAndControllersSetup(
			false,
			false,
			&configapi.Configuration{WaitForPodsReady: waitForPodsReady},
			jobframework.WithWaitForPodsReady(waitForPodsReady),
		))

		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-")

		fl = utiltestingapi.MakeResourceFlavor("fl").Obj()
		util.MustCreate(ctx, k8sClient, fl)

		cq = utiltestingapi.MakeClusterQueue("cq").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("fl").Resource(corev1.ResourceCPU, "10").Obj()).Obj()
		util.MustCreate(ctx, k8sClient, cq)

		lq = utiltestingapi.MakeLocalQueue("lq", ns.Name).ClusterQueue(cq.Name).Obj()
		util.MustCreate(ctx, k8sClient, lq)
	})

	ginkgo.JustAfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, fl, true)
		fwk.StopManager(ctx)
	})

	ginkgo.When("long backoffBaseSeconds and tiny waitForPodsReady.timeout", func() {
		ginkgo.BeforeEach(func() {
			backoffBaseSeconds = 10
			waitForPodsReadyTimeout = metav1.Duration{Duration: util.TinyTimeout}
		})

		ginkgo.It("should evict workload due waitForPodsReady.timeout", func() {
			ginkgo.By("creating job")
			job := testingjob.MakeJob("job", ns.Name).Queue(kueue.LocalQueueName(lq.Name)).Request(corev1.ResourceCPU, "2").Obj()
			util.MustCreate(ctx, k8sClient, job)

			wl := &kueue.Workload{}
			wlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: job.Namespace}

			ginkgo.By("setting quota reservation")
			util.SetQuotaReservation(ctx, k8sClient, wlKey, utiltestingapi.MakeAdmission(cq.Name).Obj())

			ginkgo.By("checking the workload is evicted")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
				g.Expect(wl.Status.Conditions).To(gomega.ContainElements(
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadPodsReady,
						Status:  metav1.ConditionFalse,
						Reason:  kueue.WorkloadWaitForStart,
						Message: "Not all pods are ready or succeeded",
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadQuotaReserved,
						Status:  metav1.ConditionFalse,
						Reason:  "Pending",
						Message: fmt.Sprintf("Exceeded the PodsReady timeout %s", wlKey.String()),
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadEvictedByPodsReadyTimeout,
						Message: fmt.Sprintf("Exceeded the PodsReady timeout %s", wlKey.String()),
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadAdmitted,
						Status:  metav1.ConditionFalse,
						Reason:  "NoReservation",
						Message: "The workload has no reservation",
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadRequeued,
						Status:  metav1.ConditionFalse,
						Reason:  kueue.WorkloadEvictedByPodsReadyTimeout,
						Message: fmt.Sprintf("Exceeded the PodsReady timeout %s", wlKey.String()),
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
				))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.When("Job is running and a pod fails without .recoveryTimeout configured", func() {
		ginkgo.BeforeEach(func() {
			waitForPodsReadyTimeout = metav1.Duration{Duration: 5 * time.Minute}
			waitForPodsReadyRecoveryTimeout = nil
		})

		ginkgo.It("shouldn't evict workload due waitForPodsReady.recoveryTimeout", func() {
			ginkgo.By("creating job")
			job := testingjob.MakeJob("job", ns.Name).Queue(kueue.LocalQueueName(lq.Name)).Request(corev1.ResourceCPU, "2").Obj()
			util.MustCreate(ctx, k8sClient, job)
			jobKey := client.ObjectKeyFromObject(job)

			wl := &kueue.Workload{}
			wlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: job.Namespace}

			ginkgo.By("setting quota reservation")
			util.SetQuotaReservation(ctx, k8sClient, wlKey, utiltestingapi.MakeAdmission(cq.Name).Obj())

			ginkgo.By("setting all job's pods to be ready")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, jobKey, job)).Should(gomega.Succeed())
				job.Status.Active = 1
				job.Status.Ready = ptr.To[int32](1)
				g.Expect(k8sClient.Status().Update(ctx, job)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("ensuring workload has PodsReady=True condition")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
				g.Expect(wl.Status.Conditions).Should(gomega.ContainElements(
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadPodsReady,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadStarted,
						Message: "All pods reached readiness and the workload is running",
					}, util.IgnoreConditionTimestampsAndObservedGeneration)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("failing one pod")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, jobKey, job)).Should(gomega.Succeed())
				job.Status.Active = 0
				job.Status.Ready = ptr.To[int32](0)
				job.Status.Failed = 1
				g.Expect(k8sClient.Status().Update(ctx, job)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("checking the workload is still admitted")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
				util.ExpectAdmittedWorkloadsTotalMetric(cq, "", 1)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.When("A tiny recoveryTimeout is configured and a pod fails", func() {
		ginkgo.BeforeEach(func() {
			waitForPodsReadyTimeout = metav1.Duration{Duration: 5 * time.Minute}
			waitForPodsReadyRecoveryTimeout = &metav1.Duration{Duration: util.TinyTimeout}
		})

		ginkgo.It("should evict workload due waitForPodsReady.recoveryTimeout", func() {
			ginkgo.By("creating job")
			job := testingjob.MakeJob("job", ns.Name).Queue(kueue.LocalQueueName(lq.Name)).Request(corev1.ResourceCPU, "2").Obj()
			util.MustCreate(ctx, k8sClient, job)
			jobKey := client.ObjectKeyFromObject(job)

			wl := &kueue.Workload{}
			wlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: job.Namespace}

			ginkgo.By("setting quota reservation")
			util.SetQuotaReservation(ctx, k8sClient, wlKey, utiltestingapi.MakeAdmission(cq.Name).Obj())

			ginkgo.By("setting all job's pods to be ready")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, jobKey, job)).Should(gomega.Succeed())
				job.Status.Active = 1
				job.Status.Ready = ptr.To[int32](1)
				g.Expect(k8sClient.Status().Update(ctx, job)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("ensuring workload has PodsReady=True condition")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
				g.Expect(wl.Status.Conditions).Should(gomega.ContainElements(
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadPodsReady,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadStarted,
						Message: "All pods reached readiness and the workload is running",
					}, util.IgnoreConditionTimestampsAndObservedGeneration)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("failing one pod")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, jobKey, job)).Should(gomega.Succeed())
				job.Status.Active = 0
				job.Status.Ready = ptr.To[int32](0)
				job.Status.Failed = 1
				g.Expect(k8sClient.Status().Update(ctx, job)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("checking the workload is evicted")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
				g.Expect(wl.Status.Conditions).To(gomega.ContainElements(
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadPodsReady,
						Status:  metav1.ConditionFalse,
						Reason:  kueue.WorkloadWaitForStart,
						Message: "Not all pods are ready or succeeded",
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadEvictedByPodsReadyTimeout,
						Message: fmt.Sprintf("Exceeded the PodsReady timeout %s", wlKey.String()),
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadAdmitted,
						Status:  metav1.ConditionFalse,
						Reason:  "NoReservation",
						Message: "The workload has no reservation",
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
				))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.When("A long recoveryTimeout is configured and a pod fails", func() {
		ginkgo.BeforeEach(func() {
			waitForPodsReadyTimeout = metav1.Duration{Duration: 5 * time.Minute}
			waitForPodsReadyRecoveryTimeout = &metav1.Duration{Duration: util.LongTimeout}
		})

		ginkgo.It("should wait for .recoveryTimeout for a workload to recover", func() {
			ginkgo.By("creating job")
			job := testingjob.MakeJob("job", ns.Name).Queue(kueue.LocalQueueName(lq.Name)).Request(corev1.ResourceCPU, "2").Obj()
			util.MustCreate(ctx, k8sClient, job)
			jobKey := client.ObjectKeyFromObject(job)

			wl := &kueue.Workload{}
			wlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: job.Namespace}

			ginkgo.By("setting quota reservation")
			util.SetQuotaReservation(ctx, k8sClient, wlKey, utiltestingapi.MakeAdmission(cq.Name).Obj())

			ginkgo.By("setting all job's pods to be ready")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, jobKey, job)).Should(gomega.Succeed())
				job.Status.Active = 1
				job.Status.Ready = ptr.To[int32](1)
				g.Expect(k8sClient.Status().Update(ctx, job)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("ensuring workload has PodsReady=True condition")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
				g.Expect(wl.Status.Conditions).Should(gomega.ContainElements(
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadPodsReady,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadStarted,
						Message: "All pods reached readiness and the workload is running",
					}, util.IgnoreConditionTimestampsAndObservedGeneration)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("failing one pod")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, jobKey, job)).Should(gomega.Succeed())
				job.Status.Active = 0
				job.Status.Ready = ptr.To[int32](0)
				job.Status.Failed = 1
				g.Expect(k8sClient.Status().Update(ctx, job)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("ensuring workload has PodsReady=False condition")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
				g.Expect(wl.Status.Conditions).Should(gomega.ContainElements(
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadPodsReady,
						Status:  metav1.ConditionFalse,
						Reason:  kueue.WorkloadWaitForRecovery,
						Message: "At least one pod has failed, waiting for recovery",
					}, util.IgnoreConditionTimestampsAndObservedGeneration)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("recovering the pod")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, jobKey, job)).Should(gomega.Succeed())
				job.Status.Active = 1
				job.Status.Ready = ptr.To[int32](1)
				job.Status.Failed = 1
				g.Expect(k8sClient.Status().Update(ctx, job)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("ensuring workload has PodsReady=True condition")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
				g.Expect(wl.Status.Conditions).Should(gomega.ContainElements(
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadPodsReady,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadRecovered,
						Message: "All pods reached readiness and the workload is running",
					}, util.IgnoreConditionTimestampsAndObservedGeneration)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("checking the workload is still admitted")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
				util.ExpectAdmittedWorkloadsTotalMetric(cq, "", 1)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.When("short backoffBaseSeconds and tiny .timeout", func() {
		ginkgo.BeforeEach(func() {
			backoffBaseSeconds = 1
			backoffLimitCount = ptr.To[int32](1)
			waitForPodsReadyTimeout = metav1.Duration{Duration: util.TinyTimeout}
		})

		ginkgo.It("should re-queue a workload evicted due to PodsReady timeout after the backoff elapses", func() {
			ginkgo.By("creating job")
			job := testingjob.MakeJob("job", ns.Name).Queue(kueue.LocalQueueName(lq.Name)).Request(corev1.ResourceCPU, "2").Obj()
			util.MustCreate(ctx, k8sClient, job)

			wl := &kueue.Workload{}
			wlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: job.Namespace}

			ginkgo.By("admit the workload, it gets evicted due to PodsReadyTimeout and re-queued")
			var admission *kueue.Admission
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
				admission = utiltestingapi.MakeAdmission(cq.Name).
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "on-demand", "1m").
						Count(wl.Spec.PodSets[0].Count).
						Obj()).
					Obj()
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			util.SetQuotaReservation(ctx, k8sClient, wlKey, admission)
			util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, wl)

			ginkgo.By("checking the workload is requeued")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
				g.Expect(wl.Status.RequeueState).ShouldNot(gomega.BeNil())
				g.Expect(wl.Status.RequeueState.Count).Should(gomega.Equal(ptr.To[int32](1)))
				g.Expect(wl.Status.RequeueState.RequeueAt).Should(gomega.BeNil())
				g.Expect(wl.Status.Conditions).To(gomega.ContainElements(
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadPodsReady,
						Status:  metav1.ConditionFalse,
						Reason:  kueue.WorkloadWaitForStart,
						Message: "Not all pods are ready or succeeded",
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadQuotaReserved,
						Status:  metav1.ConditionFalse,
						Reason:  "Pending",
						Message: fmt.Sprintf("Exceeded the PodsReady timeout %s", wlKey.String()),
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadEvictedByPodsReadyTimeout,
						Message: fmt.Sprintf("Exceeded the PodsReady timeout %s", wlKey.String()),
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadAdmitted,
						Status:  metav1.ConditionFalse,
						Reason:  "NoReservation",
						Message: "The workload has no reservation",
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadRequeued,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadBackoffFinished,
						Message: "The workload backoff was finished",
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
				))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("re-admit the workload to exceed the backoffLimitCount")
			util.SetQuotaReservation(ctx, k8sClient, wlKey, admission)
			util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, wl)

			ginkgo.By("checking the workload is evicted by deactivated due to PodsReadyTimeout")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
				g.Expect(wl.Spec.Active).ShouldNot(gomega.BeNil())
				g.Expect(*wl.Spec.Active).Should(gomega.BeFalse())
				g.Expect(wl.Status.RequeueState).Should(gomega.BeNil())
				g.Expect(wl.Status.Conditions).To(gomega.ContainElements(
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadPodsReady,
						Status:  metav1.ConditionFalse,
						Reason:  kueue.WorkloadWaitForStart,
						Message: "Not all pods are ready or succeeded",
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadQuotaReserved,
						Status:  metav1.ConditionFalse,
						Reason:  "Pending",
						Message: "The workload is deactivated due to exceeding the maximum number of re-queuing retries",
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  "DeactivatedDueToRequeuingLimitExceeded",
						Message: "The workload is deactivated due to exceeding the maximum number of re-queuing retries",
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadAdmitted,
						Status:  metav1.ConditionFalse,
						Reason:  "NoReservation",
						Message: "The workload has no reservation",
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadRequeued,
						Status:  metav1.ConditionFalse,
						Reason:  "DeactivatedDueToRequeuingLimitExceeded",
						Message: "The workload is deactivated due to exceeding the maximum number of re-queuing retries",
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
				))
				g.Expect(apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadDeactivationTarget)).Should(gomega.BeNil())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.When("testing ready wait time metrics with priority classes", func() {
		ginkgo.BeforeEach(func() {
			waitForPodsReadyTimeout = metav1.Duration{Duration: 5 * time.Minute}
			waitForPodsReadyRecoveryTimeout = nil
		})

		ginkgo.It("should record ready wait time metrics with workload priority class label", func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.LocalQueueMetrics, true)

			ginkgo.By("creating workload priority classes")
			lowPriorityClass := utiltestingapi.MakeWorkloadPriorityClass("low-priority").PriorityValue(priorityValue).Obj()
			util.MustCreate(ctx, k8sClient, lowPriorityClass)
			ginkgo.DeferCleanup(func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, lowPriorityClass, true)
			})

			highPriorityClass := utiltestingapi.MakeWorkloadPriorityClass("high-priority").PriorityValue(highPriorityValue).Obj()
			util.MustCreate(ctx, k8sClient, highPriorityClass)
			ginkgo.DeferCleanup(func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, highPriorityClass, true)
			})

			ginkgo.By("creating low priority job")
			lowJob := testingjob.MakeJob("low-job", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "1").
				WorkloadPriorityClass(lowPriorityClass.Name).
				Obj()
			util.MustCreate(ctx, k8sClient, lowJob)
			lowJobKey := client.ObjectKeyFromObject(lowJob)
			lowWl := &kueue.Workload{}
			lowWlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(lowJob.Name, lowJob.UID), Namespace: lowJob.Namespace}

			ginkgo.By("creating high priority job")
			highJob := testingjob.MakeJob("high-job", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "1").
				WorkloadPriorityClass(highPriorityClass.Name).
				Obj()
			util.MustCreate(ctx, k8sClient, highJob)
			highJobKey := client.ObjectKeyFromObject(highJob)
			highWl := &kueue.Workload{}
			highWlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(highJob.Name, highJob.UID), Namespace: highJob.Namespace}

			ginkgo.By("setting quota reservation for both jobs")
			util.SetQuotaReservation(ctx, k8sClient, lowWlKey, utiltestingapi.MakeAdmission(cq.Name).Obj())
			util.SetQuotaReservation(ctx, k8sClient, highWlKey, utiltestingapi.MakeAdmission(cq.Name).Obj())

			ginkgo.By("setting low priority job's pods to be ready")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, lowJobKey, lowJob)).Should(gomega.Succeed())
				lowJob.Status.Active = 1
				lowJob.Status.Ready = ptr.To[int32](1)
				g.Expect(k8sClient.Status().Update(ctx, lowJob)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("ensuring low priority workload has PodsReady=True condition")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, lowWlKey, lowWl)).Should(gomega.Succeed())
				g.Expect(lowWl.Status.Conditions).Should(gomega.ContainElements(
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadPodsReady,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadStarted,
						Message: "All pods reached readiness and the workload is running",
					}, util.IgnoreConditionTimestampsAndObservedGeneration)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("setting high priority job's pods to be ready")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, highJobKey, highJob)).Should(gomega.Succeed())
				highJob.Status.Active = 1
				highJob.Status.Ready = ptr.To[int32](1)
				g.Expect(k8sClient.Status().Update(ctx, highJob)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("ensuring high priority workload has PodsReady=True condition")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, highWlKey, highWl)).Should(gomega.Succeed())
				g.Expect(highWl.Status.Conditions).Should(gomega.ContainElements(
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadPodsReady,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadStarted,
						Message: "All pods reached readiness and the workload is running",
					}, util.IgnoreConditionTimestampsAndObservedGeneration)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("verifying ready wait time metrics are recorded with workload priority class labels")
			util.ExpectReadyWaitTimeMetricAtLeast(cq, lowPriorityClass.Name, 1)
			util.ExpectReadyWaitTimeMetricAtLeast(cq, highPriorityClass.Name, 1)
			util.ExpectAdmittedUntilReadyWaitTimeMetricAtLeast(cq, lowPriorityClass.Name, 1)
			util.ExpectAdmittedUntilReadyWaitTimeMetricAtLeast(cq, highPriorityClass.Name, 1)

			ginkgo.By("verifying local queue ready wait time metrics are recorded with workload priority class labels")
			util.ExpectLocalQueueReadyWaitTimeMetricAtLeast(lq, lowPriorityClass.Name, 1)
			util.ExpectLocalQueueReadyWaitTimeMetricAtLeast(lq, highPriorityClass.Name, 1)
			util.ExpectLocalQueueAdmittedUntilReadyWaitTimeMetricAtLeast(lq, lowPriorityClass.Name, 1)
			util.ExpectLocalQueueAdmittedUntilReadyWaitTimeMetricAtLeast(lq, highPriorityClass.Name, 1)

			util.ExpectLQAdmittedWorkloadsTotalMetric(lq, lowPriorityClass.Name, 1)
			util.ExpectLQAdmittedWorkloadsTotalMetric(lq, highPriorityClass.Name, 1)
		})
	})
})

var _ = ginkgo.Describe("Job controller with TopologyAwareScheduling", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	const (
		nodeGroupLabel = "node-group"
		tasBlockLabel  = "cloud.com/topology-block"
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
		fwk.StartManager(ctx, cfg, managerAndControllersSetup(true, true, nil))
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "tas-job-")

		nodes = []corev1.Node{
			*testingnode.MakeNode("b1").
				Label("node-group", "tas").
				Label(tasBlockLabel, "b1").
				StatusAllocatable(corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1"),
					corev1.ResourceMemory: resource.MustParse("1Gi"),
					corev1.ResourcePods:   resource.MustParse("10"),
				}).
				Ready().
				Obj(),
		}
		util.CreateNodesWithStatus(ctx, k8sClient, nodes)

		topology = utiltestingapi.MakeTopology("default").Levels(tasBlockLabel).Obj()
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
		job := testingjob.MakeJob("job", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			PodAnnotation(kueue.PodSetRequiredTopologyAnnotation, tasBlockLabel).
			Request(corev1.ResourceCPU, "1").
			Obj()
		ginkgo.By("creating a job which requires block", func() {
			util.MustCreate(ctx, k8sClient, job)
		})

		wl := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: ns.Name}

		ginkgo.By("verify the workload is created", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, wl)).Should(gomega.Succeed())
				g.Expect(wl.Spec.PodSets).Should(gomega.BeComparableTo([]kueue.PodSet{{
					Name:  kueue.DefaultPodSetName,
					Count: 1,
					TopologyRequest: &kueue.PodSetTopologyRequest{
						Required:      ptr.To(tasBlockLabel),
						PodIndexLabel: ptr.To(batchv1.JobCompletionIndexAnnotation),
					},
				}}, cmpopts.IgnoreFields(kueue.PodSet{}, "Template")))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verify the workload is admitted", func() {
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
			util.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
		})

		ginkgo.By("verify admission for the workload", func() {
			wl := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: ns.Name}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, wl)).Should(gomega.Succeed())
				g.Expect(wl.Status.Admission).ShouldNot(gomega.BeNil())
				g.Expect(wl.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
				g.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
					tas.V1Beta2From(&tas.TopologyAssignment{
						Levels:  []string{tasBlockLabel},
						Domains: []tas.TopologyDomainAssignment{{Count: 1, Values: []string{"b1"}}},
					}),
				))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("should admit workload with topology unconstrained annotation which fits", func() {
		job := testingjob.MakeJob("job", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			PodAnnotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
			Request(corev1.ResourceCPU, "1").
			Obj()
		ginkgo.By("creating a job", func() {
			util.MustCreate(ctx, k8sClient, job)
		})

		wl := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: ns.Name}

		ginkgo.By("verify the workload is created", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, wl)).Should(gomega.Succeed())
				g.Expect(wl.Spec.PodSets).Should(gomega.BeComparableTo([]kueue.PodSet{{
					Name:  kueue.DefaultPodSetName,
					Count: 1,
					TopologyRequest: &kueue.PodSetTopologyRequest{
						Unconstrained: ptr.To(true),
						PodIndexLabel: ptr.To(batchv1.JobCompletionIndexAnnotation),
					},
				}}, cmpopts.IgnoreFields(kueue.PodSet{}, "Template")))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verify the workload is admitted", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, wl)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
			util.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
		})

		ginkgo.By("verify admission for the workload", func() {
			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: ns.Name}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, wl)).Should(gomega.Succeed())
				g.Expect(wl.Status.Admission).ShouldNot(gomega.BeNil())
				g.Expect(wl.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
				g.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
					tas.V1Beta2From(&tas.TopologyAssignment{
						Levels:  []string{tasBlockLabel},
						Domains: []tas.TopologyDomainAssignment{{Count: 1, Values: []string{"b1"}}},
					}),
				))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})

var _ = ginkgo.Describe("Job controller with TAS and ElasticJobsViaWorkloadSlices", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	const (
		nodeGroupLabel = "node-group"
		tasBlockLabel  = "cloud.com/topology-block"
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
		gomega.Expect(utilfeature.DefaultMutableFeatureGate.SetFromMap(map[string]bool{
			string(features.ElasticJobsViaWorkloadSlices): true,
		})).Should(gomega.Succeed())
		fwk.StartManager(ctx, cfg, managerAndControllersSetup(true, true, nil))
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "tas-elastic-")

		nodes = []corev1.Node{
			*testingnode.MakeNode("b1").
				Label(nodeGroupLabel, "tas").
				Label(tasBlockLabel, "b1").
				StatusAllocatable(corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1"),
					corev1.ResourceMemory: resource.MustParse("1Gi"),
					corev1.ResourcePods:   resource.MustParse("10"),
				}).
				Ready().
				Obj(),
			*testingnode.MakeNode("b2").
				Label(nodeGroupLabel, "tas").
				Label(tasBlockLabel, "b2").
				StatusAllocatable(corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1"),
					corev1.ResourceMemory: resource.MustParse("1Gi"),
					corev1.ResourcePods:   resource.MustParse("10"),
				}).
				Ready().
				Obj(),
		}
		for i := range nodes {
			util.MustCreate(ctx, k8sClient, &nodes[i])
		}

		topology = utiltestingapi.MakeTopology("default").Levels(tasBlockLabel).Obj()
		util.MustCreate(ctx, k8sClient, topology)

		tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
			NodeLabel(nodeGroupLabel, "tas").
			TopologyName(topology.Name).Obj()
		util.MustCreate(ctx, k8sClient, tasFlavor)

		clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).
					Resource(corev1.ResourceCPU, "5").
					Resource(corev1.ResourceMemory, "5Gi").
					Obj(),
			).Obj()
		util.MustCreate(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		util.MustCreate(ctx, k8sClient, localQueue)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		for i := range nodes {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, &nodes[i], true)
		}
	})

	ginkgo.It("should scale up an elastic job with TAS unconstrained topology", func() {
		job := testingjob.MakeJob("elastic-job", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			PodAnnotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			Parallelism(1).
			Completions(10).
			Request(corev1.ResourceCPU, "100m").
			Obj()

		ginkgo.By("creating an elastic job with unconstrained topology", func() {
			util.MustCreate(ctx, k8sClient, job)
		})

		var originalWorkload *kueue.Workload
		ginkgo.By("verify the workload is created and admitted with topology assignment", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				workloads := &kueue.WorkloadList{}
				g.Expect(k8sClient.List(ctx, workloads, client.InNamespace(ns.Name))).Should(gomega.Succeed())
				g.Expect(workloads.Items).Should(gomega.HaveLen(1))
				originalWorkload = &workloads.Items[0]
				g.Expect(originalWorkload.Spec.PodSets).Should(gomega.HaveLen(1))
				g.Expect(originalWorkload.Spec.PodSets[0].TopologyRequest).ShouldNot(gomega.BeNil())
				g.Expect(originalWorkload.Spec.PodSets[0].TopologyRequest.Unconstrained).Should(gomega.Equal(ptr.To(true)))
				g.Expect(originalWorkload.Status.Admission).ShouldNot(gomega.BeNil())
				g.Expect(originalWorkload.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
				g.Expect(originalWorkload.Status.Admission.PodSetAssignments[0].TopologyAssignment).ShouldNot(gomega.BeNil())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("scale up the job by increasing parallelism", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), job)).Should(gomega.Succeed())
				job.Spec.Parallelism = ptr.To(int32(2))
				g.Expect(k8sClient.Update(ctx, job)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verify a new workload slice is created and admitted with topology assignment", func() {
			newWorkload := util.ExpectNewWorkloadSlice(ctx, k8sClient, originalWorkload)
			gomega.Expect(newWorkload).ShouldNot(gomega.BeNil(), "Expected a new workload slice to be created")

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(newWorkload), newWorkload)).To(gomega.Succeed())
				g.Expect(newWorkload.Status.Admission).ShouldNot(gomega.BeNil(), "New workload should be admitted")
				g.Expect(newWorkload.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
				g.Expect(newWorkload.Status.Admission.PodSetAssignments[0].TopologyAssignment).ShouldNot(gomega.BeNil(),
					"New workload slice should have topology assignment")
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})

var _ = ginkgo.Describe("Job controller with ObjectRetentionPolicies", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		enableWaitForPodsReady  bool
		afterDeactivatedByKueue *metav1.Duration

		ns *corev1.Namespace
		fl *kueue.ResourceFlavor
		cq *kueue.ClusterQueue
		lq *kueue.LocalQueue
	)

	ginkgo.JustBeforeEach(func() {
		var waitForPodsReady *configapi.WaitForPodsReady
		if enableWaitForPodsReady {
			waitForPodsReady = &configapi.WaitForPodsReady{
				BlockAdmission:  ptr.To(true),
				Timeout:         metav1.Duration{Duration: util.TinyTimeout},
				RecoveryTimeout: nil,
				RequeuingStrategy: &configapi.RequeuingStrategy{
					Timestamp:          ptr.To(configapi.EvictionTimestamp),
					BackoffBaseSeconds: ptr.To(int32(1)),
					BackoffLimitCount:  ptr.To(int32(1)),
				},
			}
		}

		configuration := &configapi.Configuration{
			ObjectRetentionPolicies: &configapi.ObjectRetentionPolicies{
				Workloads: &configapi.WorkloadRetentionPolicy{
					AfterDeactivatedByKueue: afterDeactivatedByKueue,
				},
			},
			WaitForPodsReady: waitForPodsReady,
		}

		fwk.StartManager(ctx, cfg, managerAndControllersSetup(false, false, configuration,
			jobframework.WithObjectRetentionPolicies(configuration.ObjectRetentionPolicies),
			jobframework.WithWaitForPodsReady(waitForPodsReady),
		))

		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-")

		fl = utiltestingapi.MakeResourceFlavor("fl").Obj()
		util.MustCreate(ctx, k8sClient, fl)

		cq = utiltestingapi.MakeClusterQueue("cq").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(fl.Name).Resource(corev1.ResourceCPU, "5").Obj()).
			Obj()
		util.MustCreate(ctx, k8sClient, cq)

		lq = utiltestingapi.MakeLocalQueue("lq", ns.Name).ClusterQueue(cq.Name).Obj()
		util.MustCreate(ctx, k8sClient, lq)
	})

	ginkgo.JustAfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, fl, true)

		fwk.StopManager(ctx)
	})

	ginkgo.When("WaitForPodsReady disabled", func() {
		ginkgo.When("tiny deactivation retention period", func() {
			ginkgo.BeforeEach(func() {
				enableWaitForPodsReady = true
				afterDeactivatedByKueue = &metav1.Duration{Duration: util.TinyTimeout}
			})

			ginkgo.It("shouldn't delete job when it is deactivated manually", framework.SlowSpec, func() {
				job := testingjob.MakeJob("job", ns.Name).
					Queue(kueue.LocalQueueName(lq.Name)).
					Request(corev1.ResourceCPU, "2").
					Obj()
				ginkgo.By("Creating a Job", func() {
					util.MustCreate(ctx, k8sClient, job)
				})

				wlKey := types.NamespacedName{
					Namespace: job.Namespace,
					Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
				}
				wl := &kueue.Workload{}

				ginkgo.By("Waiting for the Workload to be created", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("Admitting the Workload", func() {
					admission := utiltestingapi.MakeAdmission(cq.Name).
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, kueue.ResourceFlavorReference(fl.Name), "1m").
							Count(wl.Spec.PodSets[0].Count).
							Obj()).
						Obj()
					util.SetQuotaReservation(ctx, k8sClient, wlKey, admission)
					util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, wl)
				})

				ginkgo.By("Deactivating the Workload", func() {
					util.DeactivateWorkload(ctx, k8sClient, wlKey)
				})

				ginkgo.By("Checking that the Job is deleted", func() {
					createdJob := &batchv1.Job{}
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), createdJob)).Should(gomega.Succeed())
						g.Expect(createdJob.GetDeletionTimestamp()).To(gomega.BeNil())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})
			})
		})
	})

	ginkgo.When("WaitForPodsReady enabled", func() {
		ginkgo.BeforeEach(func() {
			enableWaitForPodsReady = true
		})

		ginkgo.When("tiny deactivation retention period", func() {
			ginkgo.BeforeEach(func() {
				afterDeactivatedByKueue = &metav1.Duration{Duration: util.TinyTimeout}
			})

			ginkgo.It("should delete job", framework.SlowSpec, func() {
				job := testingjob.MakeJob("job", ns.Name).
					Queue(kueue.LocalQueueName(lq.Name)).
					Request(corev1.ResourceCPU, "2").
					Obj()
				ginkgo.By("Creating a Job", func() {
					util.MustCreate(ctx, k8sClient, job)
				})

				wlKey := types.NamespacedName{
					Namespace: job.Namespace,
					Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
				}
				wl := &kueue.Workload{}

				ginkgo.By("Waiting for the Workload to be created", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				admission := utiltestingapi.MakeAdmission(cq.Name).
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, kueue.ResourceFlavorReference(fl.Name), "1m").
						Count(wl.Spec.PodSets[0].Count).
						Obj()).
					Obj()

				ginkgo.By("Admitting the Workload", func() {
					util.SetQuotaReservation(ctx, k8sClient, wlKey, admission)
					util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, wl)
				})

				ginkgo.By("checking the workload is requeued", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
						g.Expect(wl.Status.RequeueState).ShouldNot(gomega.BeNil())
						g.Expect(wl.Status.RequeueState.Count).Should(gomega.Equal(ptr.To[int32](1)))
						g.Expect(wl.Status.RequeueState.RequeueAt).Should(gomega.BeNil())
						g.Expect(wl.Status.Conditions).To(gomega.ContainElements(
							gomega.BeComparableTo(metav1.Condition{
								Type:    kueue.WorkloadPodsReady,
								Status:  metav1.ConditionFalse,
								Reason:  kueue.WorkloadWaitForStart,
								Message: "Not all pods are ready or succeeded",
							}, util.IgnoreConditionTimestampsAndObservedGeneration),
							gomega.BeComparableTo(metav1.Condition{
								Type:    kueue.WorkloadQuotaReserved,
								Status:  metav1.ConditionFalse,
								Reason:  "Pending",
								Message: fmt.Sprintf("Exceeded the PodsReady timeout %s", wlKey.String()),
							}, util.IgnoreConditionTimestampsAndObservedGeneration),
							gomega.BeComparableTo(metav1.Condition{
								Type:    kueue.WorkloadEvicted,
								Status:  metav1.ConditionTrue,
								Reason:  kueue.WorkloadEvictedByPodsReadyTimeout,
								Message: fmt.Sprintf("Exceeded the PodsReady timeout %s", wlKey.String()),
							}, util.IgnoreConditionTimestampsAndObservedGeneration),
							gomega.BeComparableTo(metav1.Condition{
								Type:    kueue.WorkloadAdmitted,
								Status:  metav1.ConditionFalse,
								Reason:  "NoReservation",
								Message: "The workload has no reservation",
							}, util.IgnoreConditionTimestampsAndObservedGeneration),
							gomega.BeComparableTo(metav1.Condition{
								Type:    kueue.WorkloadRequeued,
								Status:  metav1.ConditionTrue,
								Reason:  kueue.WorkloadBackoffFinished,
								Message: "The workload backoff was finished",
							}, util.IgnoreConditionTimestampsAndObservedGeneration),
						))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("re-admit the workload to exceed the backoffLimitCount", func() {
					util.SetQuotaReservation(ctx, k8sClient, wlKey, admission)
					util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, wl)
				})

				ginkgo.By("Checking that the Job is deleted", func() {
					createdJob := &batchv1.Job{}
					gomega.Eventually(func(g gomega.Gomega) {
						err := k8sClient.Get(ctx, client.ObjectKeyFromObject(job), createdJob)
						if apierrors.IsNotFound(err) {
							return
						}
						g.Expect(err).NotTo(gomega.HaveOccurred())
						g.Expect(createdJob.GetDeletionTimestamp()).ToNot(gomega.BeNil())
					}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
				})
			})
		})

		ginkgo.When("long deactivation retention period", func() {
			ginkgo.BeforeEach(func() {
				afterDeactivatedByKueue = &metav1.Duration{Duration: util.LongTimeout}
			})

			ginkgo.It("shouldn't delete job", framework.SlowSpec, func() {
				job := testingjob.MakeJob("job", ns.Name).
					Queue(kueue.LocalQueueName(lq.Name)).
					Request(corev1.ResourceCPU, "2").
					Obj()
				ginkgo.By("Create a Job", func() {
					util.MustCreate(ctx, k8sClient, job)
				})

				wlKey := types.NamespacedName{
					Namespace: job.Namespace,
					Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
				}
				wl := &kueue.Workload{}

				ginkgo.By("Waiting for the Workload to be created", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				admission := utiltestingapi.MakeAdmission(cq.Name).
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, kueue.ResourceFlavorReference(fl.Name), "1m").
						Count(wl.Spec.PodSets[0].Count).
						Obj()).
					Obj()

				ginkgo.By("Admitting the Workload", func() {
					util.SetQuotaReservation(ctx, k8sClient, wlKey, admission)
					util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, wl)
				})

				ginkgo.By("checking the workload is requeued", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
						g.Expect(wl.Status.RequeueState).ShouldNot(gomega.BeNil())
						g.Expect(wl.Status.RequeueState.Count).Should(gomega.Equal(ptr.To[int32](1)))
						g.Expect(wl.Status.RequeueState.RequeueAt).Should(gomega.BeNil())
						g.Expect(wl.Status.Conditions).To(gomega.ContainElements(
							gomega.BeComparableTo(metav1.Condition{
								Type:    kueue.WorkloadPodsReady,
								Status:  metav1.ConditionFalse,
								Reason:  kueue.WorkloadWaitForStart,
								Message: "Not all pods are ready or succeeded",
							}, util.IgnoreConditionTimestampsAndObservedGeneration),
							gomega.BeComparableTo(metav1.Condition{
								Type:    kueue.WorkloadQuotaReserved,
								Status:  metav1.ConditionFalse,
								Reason:  "Pending",
								Message: fmt.Sprintf("Exceeded the PodsReady timeout %s", wlKey.String()),
							}, util.IgnoreConditionTimestampsAndObservedGeneration),
							gomega.BeComparableTo(metav1.Condition{
								Type:    kueue.WorkloadEvicted,
								Status:  metav1.ConditionTrue,
								Reason:  kueue.WorkloadEvictedByPodsReadyTimeout,
								Message: fmt.Sprintf("Exceeded the PodsReady timeout %s", wlKey.String()),
							}, util.IgnoreConditionTimestampsAndObservedGeneration),
							gomega.BeComparableTo(metav1.Condition{
								Type:    kueue.WorkloadAdmitted,
								Status:  metav1.ConditionFalse,
								Reason:  "NoReservation",
								Message: "The workload has no reservation",
							}, util.IgnoreConditionTimestampsAndObservedGeneration),
							gomega.BeComparableTo(metav1.Condition{
								Type:    kueue.WorkloadRequeued,
								Status:  metav1.ConditionTrue,
								Reason:  kueue.WorkloadBackoffFinished,
								Message: "The workload backoff was finished",
							}, util.IgnoreConditionTimestampsAndObservedGeneration),
						))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("re-admit the workload to exceed the backoffLimitCount", func() {
					util.SetQuotaReservation(ctx, k8sClient, wlKey, admission)
					util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, wl)
				})

				ginkgo.By("Checking that the Job is not deleted", func() {
					createdJob := &batchv1.Job{}
					gomega.Consistently(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), createdJob)).Should(gomega.Succeed())
						g.Expect(createdJob.GetDeletionTimestamp()).Should(gomega.BeNil())
					}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
				})
			})
		})
	})
})

var _ = ginkgo.Describe("Job with elastic jobs via workload-slices support", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns             *corev1.Namespace
		resourceFlavor *kueue.ResourceFlavor
		clusterQueue   *kueue.ClusterQueue
		localQueue     *kueue.LocalQueue
	)
	cpuNominalQuota := 5

	ginkgo.BeforeAll(func() {
		gomega.Expect(utilfeature.DefaultMutableFeatureGate.SetFromMap(map[string]bool{string(features.ElasticJobsViaWorkloadSlices): true})).Should(gomega.Succeed())
		fwk.StartManager(ctx, cfg, managerAndControllersSetup(false, true, nil))
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-")

		resourceFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
		util.MustCreate(ctx, k8sClient, resourceFlavor)

		clusterQueue = utiltestingapi.MakeClusterQueue("default").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(resourceFlavor.Name).Resource(corev1.ResourceCPU, strconv.Itoa(cpuNominalQuota)).Obj()).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj()
		util.MustCreate(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("default", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		util.MustCreate(ctx, k8sClient, localQueue)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, resourceFlavor, true)
	})

	ginkgo.It("Should support job scale-down and scale-up", framework.SlowSpec, func() {
		testJob := testingjob.MakeJob("job1", ns.Name).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Request(corev1.ResourceCPU, "100m").
			Parallelism(3).
			Completions(3).
			Obj()

		var testJobWorkload *kueue.Workload

		ginkgo.By("creating a job")
		util.MustCreate(ctx, k8sClient, testJob)

		ginkgo.By("admitting the job's workload")
		gomega.Eventually(func(g gomega.Gomega) {
			workloads := &kueue.WorkloadList{}
			g.Expect(k8sClient.List(ctx, workloads, client.InNamespace(testJob.Namespace))).Should(gomega.Succeed())
			g.Expect(workloads.Items).Should(gomega.HaveLen(1))
			testJobWorkload = &workloads.Items[0]
			g.Expect(workload.IsAdmitted(testJobWorkload)).Should(gomega.BeTrue())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("the job is unsuspended")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(testJob), testJob)).Should(gomega.Succeed())
			g.Expect(ptr.Deref(testJob.Spec.Suspend, false)).Should(gomega.BeFalse())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("resource flavor utilization is correctly recorded")
		gomega.Eventually(func(g gomega.Gomega) {
			cq := &kueue.ClusterQueue{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), cq)).Should(gomega.Succeed())
			g.Expect(len(cq.Status.FlavorsUsage)).Should(gomega.BeEquivalentTo(1))
			g.Expect(len(cq.Status.FlavorsUsage[0].Resources)).Should(gomega.BeEquivalentTo(1))
			g.Expect(cq.Status.FlavorsUsage[0].Resources[0].Total).Should(gomega.BeEquivalentTo(resource.MustParse("300m")))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("reducing the job's parallelism to emulate scale-down operation")
		gomega.Eventually(func(g gomega.Gomega) {
			testJob.Spec.Parallelism = ptr.To(int32(1))
			g.Expect(k8sClient.Update(ctx, testJob)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("resource flavor utilization is correctly updated")
		gomega.Eventually(func(g gomega.Gomega) {
			cq := &kueue.ClusterQueue{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), cq)).Should(gomega.Succeed())
			g.Expect(len(cq.Status.FlavorsUsage)).Should(gomega.BeEquivalentTo(1))
			g.Expect(len(cq.Status.FlavorsUsage[0].Resources)).Should(gomega.BeEquivalentTo(1))
			g.Expect(cq.Status.FlavorsUsage[0].Resources[0].Total).Should(gomega.BeEquivalentTo(resource.MustParse("100m")))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("assert the job's workload is updated and still admitted")
		gomega.Eventually(func(g gomega.Gomega) {
			workloads := &kueue.WorkloadList{}
			g.Expect(k8sClient.List(ctx, workloads, client.InNamespace(testJob.Namespace))).Should(gomega.Succeed())
			g.Expect(workloads.Items).Should(gomega.HaveLen(1))
			g.Expect(workloads.Items[0].Spec.PodSets[0].Count).Should(gomega.BeEquivalentTo(int32(1)))
			g.Expect(workloads.Items[0].UID).Should(gomega.BeEquivalentTo(testJobWorkload.UID))
			testJobWorkload = &workloads.Items[0]
			g.Expect(workload.IsAdmitted(testJobWorkload)).Should(gomega.BeTrue())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("increasing the job's parallelism to emulate scale-up operation")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(testJob), testJob)).Should(gomega.Succeed())
			testJob.Spec.Parallelism = ptr.To(int32(2))
			g.Expect(k8sClient.Update(ctx, testJob)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("resource flavor utilization is correctly updated")
		gomega.Eventually(func(g gomega.Gomega) {
			cq := &kueue.ClusterQueue{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), cq)).Should(gomega.Succeed())
			g.Expect(len(cq.Status.FlavorsUsage)).Should(gomega.BeEquivalentTo(1))
			g.Expect(len(cq.Status.FlavorsUsage[0].Resources)).Should(gomega.BeEquivalentTo(1))
			g.Expect(cq.Status.FlavorsUsage[0].Resources[0].Total).Should(gomega.BeEquivalentTo(resource.MustParse("200m")))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("old workload is finished and new workload is admitted")
		gomega.Eventually(func(g gomega.Gomega) {
			workloads := &kueue.WorkloadList{}
			g.Expect(k8sClient.List(ctx, workloads, client.InNamespace(testJob.Namespace))).Should(gomega.Succeed())
			g.Expect(workloads.Items).Should(gomega.HaveLen(2))
			for i := range workloads.Items {
				if workloads.Items[i].Name == testJobWorkload.Name {
					g.Expect(workload.IsFinished(&workloads.Items[i])).Should(gomega.BeTrue())
					continue
				}
				g.Expect(workload.IsAdmitted(&workloads.Items[i])).Should(gomega.BeTrue())
			}
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("Should support scheduling pending workload after freeing capacity on scale-down", func() {
		var (
			testJobAWorkload *kueue.Workload
			testJobBWorkload *kueue.Workload
		)

		testJobA := testingjob.MakeJob("job-a", ns.Name).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Request(corev1.ResourceCPU, "1").
			Parallelism(3).
			Completions(3).
			Obj()

		ginkgo.By("creating a job-a")
		util.MustCreate(ctx, k8sClient, testJobA)

		ginkgo.By("admitting the job-a's workload")
		gomega.Eventually(func(g gomega.Gomega) {
			workloads := &kueue.WorkloadList{}
			g.Expect(k8sClient.List(ctx, workloads, client.InNamespace(testJobA.Namespace))).Should(gomega.Succeed())
			g.Expect(workloads.Items).Should(gomega.HaveLen(1))
			testJobAWorkload = &workloads.Items[0]
			g.Expect(workload.IsAdmitted(testJobAWorkload)).Should(gomega.BeTrue())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("the job-a is unsuspended")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(testJobA), testJobA)).Should(gomega.Succeed())
			g.Expect(ptr.Deref(testJobA.Spec.Suspend, false)).Should(gomega.BeFalse())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		testJobB := testingjob.MakeJob("job-b", ns.Name).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Request(corev1.ResourceCPU, "1").
			Parallelism(3).
			Completions(3).
			Obj()

		ginkgo.By("creating a job-b")
		util.MustCreate(ctx, k8sClient, testJobB)

		ginkgo.By("the job-b remains suspended")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(testJobB), testJobB)).Should(gomega.Succeed())
			g.Expect(ptr.Deref(testJobB.Spec.Suspend, false)).Should(gomega.BeTrue())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("job-b's workload remains pending with unreserved quota")
		gomega.Eventually(func(g gomega.Gomega) {
			workloads := &kueue.WorkloadList{}
			g.Expect(k8sClient.List(ctx, workloads, client.InNamespace(testJobA.Namespace), client.MatchingLabels{constants.JobUIDLabel: string(testJobB.UID)})).Should(gomega.Succeed())
			g.Expect(workloads.Items).Should(gomega.HaveLen(1))
			testJobBWorkload = &workloads.Items[0]
			g.Expect(testJobBWorkload.Status.Conditions).Should(utiltesting.HaveConditionStatusFalseAndReason(kueue.WorkloadQuotaReserved, "Pending"))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("scale-down job-a to make room for job-b")
		gomega.Eventually(func(g gomega.Gomega) {
			testJobA.Spec.Parallelism = ptr.To(int32(1))
			g.Expect(k8sClient.Update(ctx, testJobA)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("admitting the job-b workload")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(testJobBWorkload), testJobBWorkload)).Should(gomega.Succeed())
			g.Expect(workload.IsAdmitted(testJobBWorkload)).Should(gomega.BeTrue())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("the job-b is unsuspended")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(testJobB), testJobB)).Should(gomega.Succeed())
			g.Expect(ptr.Deref(testJobB.Spec.Suspend, false)).Should(gomega.BeFalse())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("Should mark old pending workload-slice evicted by scheduler as finished", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlices, true)
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.LocalQueueMetrics, true)

		ginkgo.By("create low priority class")
		lowPriorityClass := utiltestingapi.MakeWorkloadPriorityClass("low").PriorityValue(int32(priorityValue)).Obj()
		util.MustCreate(ctx, k8sClient, lowPriorityClass)
		ginkgo.DeferCleanup(func() {
			gomega.Expect(k8sClient.Delete(ctx, lowPriorityClass)).To(gomega.Succeed())
		})

		ginkgo.By("create high priority class")
		highPriorityClass := utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(int32(highPriorityValue)).Obj()
		util.MustCreate(ctx, k8sClient, highPriorityClass)
		ginkgo.DeferCleanup(func() {
			gomega.Expect(k8sClient.Delete(ctx, highPriorityClass)).To(gomega.Succeed())
		})

		lowPriorityJob := testingjob.MakeJob("job-low", ns.Name).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Request(corev1.ResourceCPU, "1000m").
			Parallelism(3).
			Completions(int32(cpuNominalQuota + 1)).
			WorkloadPriorityClass(lowPriorityClass.Name).
			Obj()

		ginkgo.By("creating a low-priority job")
		util.MustCreate(ctx, k8sClient, lowPriorityJob)

		ginkgo.By("the low-priority job is unsuspended")
		util.ExpectJobUnsuspendedWithNodeSelectors(ctx, k8sClient, client.ObjectKeyFromObject(lowPriorityJob), nil)
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lowPriorityJob), lowPriorityJob)).Should(gomega.Succeed())
			g.Expect(ptr.Deref(lowPriorityJob.Spec.Suspend, false)).Should(gomega.BeFalse())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		var lowPriorityWorkloadSlice *kueue.Workload
		ginkgo.By("the low-priority workload is admitted")
		workloads := util.ExpectWorkloadsInNamespace(ctx, k8sClient, lowPriorityJob.Namespace, 1)
		lowPriorityWorkloadSlice = &workloads[0]
		util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, lowPriorityWorkloadSlice)
		util.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, lowPriorityClass.Name, 1)
		util.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, highPriorityClass.Name, 0)
		util.ExpectAdmissionWaitTimeMetric(clusterQueue, lowPriorityClass.Name, 1)
		util.ExpectAdmissionWaitTimeMetric(clusterQueue, highPriorityClass.Name, 0)
		util.ExpectQuotaReservedWorkloadsTotalMetric(clusterQueue, lowPriorityClass.Name, 1)
		util.ExpectQuotaReservedWorkloadsTotalMetric(clusterQueue, highPriorityClass.Name, 0)
		util.ExpectQuotaReservedWaitTimeMetric(clusterQueue, lowPriorityClass.Name, 1)
		util.ExpectQuotaReservedWaitTimeMetric(clusterQueue, highPriorityClass.Name, 0)

		ginkgo.By("scale-up low-priority job beyond the queue's nominal capacity")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lowPriorityJob), lowPriorityJob)).Should(gomega.Succeed())
			lowPriorityJob.Spec.Parallelism = ptr.To(int32(cpuNominalQuota + 1))
			g.Expect(k8sClient.Update(ctx, lowPriorityJob)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("low priority new workload slice is pending")
		lowPriorityNewWorkloadSlice := util.ExpectNewWorkloadSlice(ctx, k8sClient, lowPriorityWorkloadSlice)
		gomega.Expect(lowPriorityNewWorkloadSlice).Should(gomega.Not(gomega.BeNil()))
		util.ExpectWorkloadsToBePending(ctx, k8sClient, lowPriorityNewWorkloadSlice)

		highPriorityJob := testingjob.MakeJob("high", ns.Name).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Request(corev1.ResourceCPU, "1000m").
			Parallelism(3).
			Completions(3).
			WorkloadPriorityClass(highPriorityClass.Name).
			Obj()

		ginkgo.By("creating a high priority job")
		util.MustCreate(ctx, k8sClient, highPriorityJob)

		ginkgo.By("the high priority job is unsuspended")
		util.ExpectJobUnsuspendedWithNodeSelectors(ctx, k8sClient, client.ObjectKeyFromObject(highPriorityJob), nil)

		util.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, lowPriorityClass.Name, 1)
		util.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, highPriorityClass.Name, 1)
		util.ExpectAdmissionWaitTimeMetric(clusterQueue, lowPriorityClass.Name, 1)
		util.ExpectAdmissionWaitTimeMetric(clusterQueue, highPriorityClass.Name, 1)
		util.ExpectQuotaReservedWorkloadsTotalMetric(clusterQueue, lowPriorityClass.Name, 1)
		util.ExpectQuotaReservedWorkloadsTotalMetric(clusterQueue, highPriorityClass.Name, 1)
		util.ExpectQuotaReservedWaitTimeMetric(clusterQueue, lowPriorityClass.Name, 1)
		util.ExpectQuotaReservedWaitTimeMetric(clusterQueue, highPriorityClass.Name, 1)

		ginkgo.By("the low priority old workload slice is finished")
		util.ExpectWorkloadToFinish(ctx, k8sClient, client.ObjectKeyFromObject(lowPriorityWorkloadSlice))

		util.ExpectEvictedWorkloadsTotalMetric(clusterQueue.Name, kueue.WorkloadEvictedByPreemption, "", lowPriorityClass.Name, 1)
		util.ExpectEvictedWorkloadsTotalMetric(clusterQueue.Name, kueue.WorkloadEvictedByPreemption, "", highPriorityClass.Name, 0)

		util.ExpectEvictedWorkloadsOnceTotalMetric(clusterQueue.Name, kueue.WorkloadEvictedByPreemption, "", lowPriorityClass.Name, 1)
		util.ExpectEvictedWorkloadsOnceTotalMetric(clusterQueue.Name, kueue.WorkloadEvictedByPreemption, "", highPriorityClass.Name, 0)

		util.ExpectFinishedWorkloadsTotalMetric(clusterQueue, lowPriorityClass.Name, 1)
		util.ExpectLQFinishedWorkloadsTotalMetric(localQueue, lowPriorityClass.Name, 1)
		util.ExpectFinishedWorkloadsTotalMetric(clusterQueue, highPriorityClass.Name, 0)
		util.ExpectLQFinishedWorkloadsTotalMetric(localQueue, highPriorityClass.Name, 0)
	})
})

var _ = ginkgo.Describe("Job reconciliation", ginkgo.Ordered, func() {
	const (
		cqName = "cluster-queue"
		rfName = "default"
		lqName = "user-queue"
	)

	var (
		rf          *kueue.ResourceFlavor
		lq          *kueue.LocalQueue
		cq          *kueue.ClusterQueue
		managedNs   *corev1.Namespace
		unmanagedNs *corev1.Namespace
	)

	ginkgo.BeforeAll(func() {
		var managedByQueueRequirement, err = labels.NewRequirement("managed-by-kueue", selection.In, []string{"true"})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		fwk.StartManager(ctx, cfg,
			managerSetup(
				jobframework.WithManagedJobsNamespaceSelector(labels.NewSelector().Add(*managedByQueueRequirement)),
			),
		)

		managedNs = utiltesting.MakeNamespaceWrapper("").
			GenerateName("managed-ns-").
			Label("managed-by-kueue", "true").
			Obj()
		util.MustCreate(ctx, k8sClient, managedNs)

		unmanagedNs = utiltesting.MakeNamespaceWrapper("").
			GenerateName("unmanaged-ns-").
			Obj()
		util.MustCreate(ctx, k8sClient, unmanagedNs)

		rf = utiltestingapi.MakeResourceFlavor(rfName).Obj()
		util.MustCreate(ctx, k8sClient, rf)

		cq = utiltestingapi.MakeClusterQueue(cqName).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rfName).Resource(corev1.ResourceCPU, "5").Obj()).
			Obj()
		util.MustCreate(ctx, k8sClient, cq)

		lq = utiltestingapi.MakeLocalQueue(lqName, managedNs.Name).ClusterQueue(cqName).Obj()
		util.MustCreate(ctx, k8sClient, lq)
	})

	ginkgo.AfterAll(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, managedNs)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, unmanagedNs)).To(gomega.Succeed())
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, rf, true, util.LongTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, lq, true, util.LongTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, cq, true, util.LongTimeout)
		fwk.StopManager(ctx)
	})

	ginkgo.When("ManagedJobsNamespaceSelectorAlwaysRespected enabled", func() {
		ginkgo.BeforeEach(func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ManagedJobsNamespaceSelectorAlwaysRespected, true)
		})

		ginkgo.It("should not reconcile a job in an unmanaged namespace", func() {
			job := testingjob.MakeJob("unmanaged-job", unmanagedNs.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Suspend(true).
				Image(util.GetAgnHostImage(), util.BehaviorExitFast).
				Obj()

			gomega.Expect(k8sClient.Create(ctx, job)).To(gomega.Succeed())

			wls := &kueue.WorkloadList{}
			gomega.Expect(k8sClient.List(ctx, wls, client.InNamespace(unmanagedNs.Name))).To(gomega.Succeed())
			gomega.Expect(wls.Items).To(gomega.BeEmpty(), "Expected no workload in unmanaged namespace")
		})

		ginkgo.It("should reconcile a job in managed namespace and create a workload", func() {
			job := testingjob.MakeJob("managed-job", managedNs.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Suspend(true).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Obj()

			gomega.Expect(k8sClient.Create(ctx, job)).To(gomega.Succeed())

			ginkgo.By("check that only one workload is created", func() {
				createdWorkloads := &kueue.WorkloadList{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, createdWorkloads, client.InNamespace(managedNs.Name))).To(gomega.Succeed())
					g.Expect(createdWorkloads.Items).To(gomega.HaveLen(1))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("ManagedJobsNamespaceSelectorAlwaysRespected disabled", func() {
		ginkgo.BeforeEach(func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ManagedJobsNamespaceSelectorAlwaysRespected, false)
		})

		ginkgo.It("should reconcile a job in an unmanaged namespace with queue name set and create a workload", func() {
			job := testingjob.MakeJob("unmanaged-job-with-queue-name", unmanagedNs.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Suspend(true).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Obj()

			gomega.Expect(k8sClient.Create(ctx, job)).To(gomega.Succeed())

			ginkgo.By("check that only one workload is created", func() {
				createdWorkloads := &kueue.WorkloadList{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, createdWorkloads, client.InNamespace(unmanagedNs.Name))).To(gomega.Succeed())
					g.Expect(createdWorkloads.Items).To(gomega.HaveLen(1))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})
