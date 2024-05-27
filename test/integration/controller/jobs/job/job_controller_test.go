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
	"maps"
	"time"

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
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

const (
	parallelism       = 4
	jobName           = "test-job"
	instanceKey       = "cloud.provider.com/instance"
	priorityClassName = "test-priority-class"
	priorityValue     = 10
	parentJobName     = jobName + "-parent"
	childJobName      = jobName + "-child"
)

// +kubebuilder:docs-gen:collapse=Imports

var _ = ginkgo.Describe("Job controller", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	ginkgo.BeforeAll(func() {
		fwk = &framework.Framework{
			CRDPath: crdPath,
		}
		cfg = fwk.Init()
		ctx, k8sClient = fwk.RunManager(cfg, managerSetup(
			jobframework.WithManageJobsWithoutQueueName(true),
			jobframework.WithLabelKeysToCopy([]string{"toCopyKey"}),
		))
	})
	ginkgo.AfterAll(func() {
		fwk.Teardown()
	})

	var (
		ns             *corev1.Namespace
		childLookupKey types.NamespacedName
	)

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "core-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
		childLookupKey = types.NamespacedName{Name: childJobName, Namespace: ns.Name}
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.It("Should reconcile workload and job for all jobs", func() {
		ginkgo.By("checking the job gets suspended when created unsuspended")
		priorityClass := testing.MakePriorityClass(priorityClassName).
			PriorityValue(int32(priorityValue)).Obj()
		gomega.Expect(k8sClient.Create(ctx, priorityClass)).Should(gomega.Succeed())
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
		gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
		lookupKey := types.NamespacedName{Name: jobName, Namespace: ns.Name}
		createdJob := &batchv1.Job{}
		gomega.Eventually(func() bool {
			if err := k8sClient.Get(ctx, lookupKey, createdJob); err != nil {
				return false
			}
			return createdJob.Spec.Suspend != nil && *createdJob.Spec.Suspend
		}, util.Timeout, util.Interval).Should(gomega.BeTrue())

		ginkgo.By("checking the workload is created without queue assigned")
		createdWorkload := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: ns.Name}
		gomega.Eventually(func() error {
			return k8sClient.Get(ctx, wlLookupKey, createdWorkload)
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		gomega.Expect(createdWorkload.Spec.QueueName).Should(gomega.Equal(""), "The Workload shouldn't have .spec.queueName set")
		gomega.Expect(metav1.IsControlledBy(createdWorkload, job)).To(gomega.BeTrue(), "The Workload should be owned by the Job")

		createdTime := createdWorkload.CreationTimestamp

		ginkgo.By("checking the workload is created with priority, priorityName, and ProvisioningRequest annotations")
		gomega.Expect(createdWorkload.Spec.PriorityClassName).Should(gomega.Equal(priorityClassName))
		gomega.Expect(*createdWorkload.Spec.Priority).Should(gomega.Equal(int32(priorityValue)))
		gomega.Expect(createdWorkload.Annotations).Should(gomega.Equal(map[string]string{"provreq.kueue.x-k8s.io/ValidUntilSeconds": "0"}))

		gomega.Expect(createdWorkload.Labels["toCopyKey"]).Should(gomega.Equal("toCopyValue"))
		gomega.Expect(createdWorkload.Labels).ShouldNot(gomega.ContainElement("doNotCopyValue"))

		ginkgo.By("checking the workload is updated with queue name when the job does")
		jobQueueName := "test-queue"
		createdJob.Annotations = map[string]string{constants.QueueAnnotation: jobQueueName}
		gomega.Expect(k8sClient.Update(ctx, createdJob)).Should(gomega.Succeed())
		gomega.Eventually(func() bool {
			if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
				return false
			}
			return createdWorkload.Spec.QueueName == jobQueueName
		}, util.Timeout, util.Interval).Should(gomega.BeTrue())

		ginkgo.By("checking the workload label is not updated when the job label is")
		newJobLabelValue := "updatedValue"
		createdJob.Labels["toCopyKey"] = newJobLabelValue
		gomega.Expect(k8sClient.Update(ctx, createdJob)).Should(gomega.Succeed())
		gomega.Consistently(func() string {
			if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
				return ""
			}
			return createdWorkload.Labels["toCopyKey"]
		}, util.ConsistentDuration, util.Interval).Should(gomega.Equal("toCopyValue"))

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
		gomega.Expect(ctrl.SetControllerReference(createdJob, secondWl, scheme.Scheme)).Should(gomega.Succeed())
		secondWl.Spec.PodSets[0].Count += 1
		gomega.Expect(k8sClient.Create(ctx, secondWl)).Should(gomega.Succeed())
		gomega.Eventually(func() error {
			wl := &kueue.Workload{}
			key := types.NamespacedName{Name: secondWl.Name, Namespace: secondWl.Namespace}
			return k8sClient.Get(ctx, key, wl)
		}, util.Timeout, util.Interval).Should(testing.BeNotFoundError())
		// check the original wl is still there
		gomega.Consistently(func() error {
			return k8sClient.Get(ctx, wlLookupKey, createdWorkload)
		}, util.ConsistentDuration, util.Interval).Should(gomega.Succeed())
		gomega.Eventually(func() bool {
			ok, _ := testing.CheckLatestEvent(ctx, k8sClient, "DeletedWorkload", corev1.EventTypeNormal, fmt.Sprintf("Deleted not matching Workload: %v", workload.Key(secondWl)))
			return ok
		}, util.Timeout, util.Interval).Should(gomega.BeTrue())

		ginkgo.By("checking the job is unsuspended when workload is assigned")
		onDemandFlavor := testing.MakeResourceFlavor("on-demand").Label(instanceKey, "on-demand").Obj()
		gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).Should(gomega.Succeed())
		spotFlavor := testing.MakeResourceFlavor("spot").Label(instanceKey, "spot").Obj()
		gomega.Expect(k8sClient.Create(ctx, spotFlavor)).Should(gomega.Succeed())
		clusterQueue := testing.MakeClusterQueue("cluster-queue").
			ResourceGroup(
				*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj(),
				*testing.MakeFlavorQuotas("spot").Resource(corev1.ResourceCPU, "5").Obj(),
			).Obj()
		admission := testing.MakeAdmission(clusterQueue.Name).
			Assignment(corev1.ResourceCPU, "on-demand", "1m").
			AssignmentPodCount(createdWorkload.Spec.PodSets[0].Count).
			Obj()
		gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, admission)).Should(gomega.Succeed())
		util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
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
		gomega.Expect(createdJob.Spec.Template.Spec.NodeSelector).Should(gomega.HaveLen(1))
		gomega.Expect(createdJob.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))
		gomega.Consistently(func() bool {
			if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
				return false
			}
			return len(createdWorkload.Status.Conditions) == 2
		}, util.ConsistentDuration, util.Interval).Should(gomega.BeTrue())

		// We need to set startTime to the job since the kube-controller-manager doesn't exist in envtest.
		ginkgo.By("setting startTime to the job")
		now := metav1.Now()
		createdJob.Status.StartTime = &now
		gomega.Expect(k8sClient.Status().Update(ctx, createdJob)).Should(gomega.Succeed())

		ginkgo.By("checking the job gets suspended when parallelism changes and the added node selectors are removed")
		newParallelism := int32(parallelism + 1)
		createdJob.Spec.Parallelism = &newParallelism
		gomega.Expect(k8sClient.Update(ctx, createdJob)).Should(gomega.Succeed())
		gomega.Eventually(func() bool {
			if err := k8sClient.Get(ctx, lookupKey, createdJob); err != nil {
				return false
			}
			return createdJob.Spec.Suspend != nil && *createdJob.Spec.Suspend && createdJob.Status.StartTime == nil &&
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
		admission = testing.MakeAdmission(clusterQueue.Name).
			Assignment(corev1.ResourceCPU, "spot", "1m").
			AssignmentPodCount(createdWorkload.Spec.PodSets[0].Count).
			Obj()
		gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, admission)).Should(gomega.Succeed())
		util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
		gomega.Eventually(func() bool {
			if err := k8sClient.Get(ctx, lookupKey, createdJob); err != nil {
				return false
			}
			return !*createdJob.Spec.Suspend
		}, util.Timeout, util.Interval).Should(gomega.BeTrue())
		gomega.Expect(createdJob.Spec.Template.Spec.NodeSelector).Should(gomega.HaveLen(1))
		gomega.Expect(createdJob.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(spotFlavor.Name))
		gomega.Consistently(func() bool {
			if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
				return false
			}
			return len(createdWorkload.Status.Conditions) == 2
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
			if err != nil || len(createdWorkload.Status.Conditions) == 1 {
				return false
			}

			return apimeta.IsStatusConditionTrue(createdWorkload.Status.Conditions, kueue.WorkloadFinished)
		}, util.Timeout, util.Interval).Should(gomega.BeTrue())
	})

	ginkgo.It("Should reconcile job when queueName set by annotation (deprecated)", func() {
		ginkgo.By("checking the workload is created with correct queue name assigned")
		jobQueueName := "test-queue"
		job := testingjob.MakeJob(jobName, ns.Name).QueueNameAnnotation("test-queue").Obj()
		gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
		createdWorkload := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: ns.Name}
		gomega.Eventually(func() error {
			return k8sClient.Get(ctx, wlLookupKey, createdWorkload)
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		gomega.Expect(createdWorkload.Spec.QueueName).Should(gomega.Equal(jobQueueName))
	})

	ginkgo.When("The parent job is managed by kueue", func() {
		ginkgo.It("Should suspend a job if the parent workload does not exist", func() {
			ginkgo.By("creating the parent job")
			parentJob := testingjob.MakeJob(parentJobName, ns.Name).Label(constants.PrebuiltWorkloadLabel, "missing").Obj()
			gomega.Expect(k8sClient.Create(ctx, parentJob)).Should(gomega.Succeed())

			ginkgo.By("Creating the child job which uses the parent workload annotation")
			childJob := testingjob.MakeJob(childJobName, ns.Name).Suspend(false).Obj()
			gomega.Expect(ctrl.SetControllerReference(parentJob, childJob, k8sClient.Scheme())).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, childJob)).Should(gomega.Succeed())

			ginkgo.By("checking that the child job is suspended")
			gomega.Eventually(func() *bool {
				gomega.Expect(k8sClient.Get(ctx, childLookupKey, childJob)).Should(gomega.Succeed())
				return childJob.Spec.Suspend
			}, util.Timeout, util.Interval).Should(gomega.Equal(ptr.To(true)))
		})

		ginkgo.It("Should not create child workload for a job with a kueue managed parent", func() {
			ginkgo.By("creating the parent job")
			parentJob := testingjob.MakeJob(parentJobName, ns.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, parentJob)).Should(gomega.Succeed())

			ginkgo.By("waiting for the parent workload to be created")
			parentWorkload := &kueue.Workload{}
			parentWlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(parentJob.Name, parentJob.UID), Namespace: ns.Name}
			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, parentWlLookupKey, parentWorkload)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Creating the child job which uses the parent workload annotation")
			childJob := testingjob.MakeJob(childJobName, ns.Name).Obj()
			gomega.Expect(ctrl.SetControllerReference(parentJob, childJob, k8sClient.Scheme())).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, childJob)).Should(gomega.Succeed())

			ginkgo.By("Checking that the child workload is not created")
			childWorkload := &kueue.Workload{}
			childWlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(childJob.Name, childJob.UID), Namespace: ns.Name}
			gomega.Consistently(func() bool {
				return apierrors.IsNotFound(k8sClient.Get(ctx, childWlLookupKey, childWorkload))
			}, util.ConsistentDuration, util.Interval).Should(gomega.BeTrue())
		})

		ginkgo.It("Should not update the queue name of the workload with an empty value that the child job has", func() {
			jobQueueName := "test-queue"

			ginkgo.By("creating the parent job with queue name")
			parentJob := testingjob.MakeJob(parentJobName, ns.Name).Queue(jobQueueName).Obj()
			gomega.Expect(k8sClient.Create(ctx, parentJob)).Should(gomega.Succeed())

			ginkgo.By("waiting for the parent workload to be created")
			parentWorkload := &kueue.Workload{}
			parentWlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(parentJob.Name, parentJob.UID), Namespace: ns.Name}
			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, parentWlLookupKey, parentWorkload)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Creating the child job which uses the parent workload annotation")
			childJob := testingjob.MakeJob(childJobName, ns.Name).Obj()
			gomega.Expect(ctrl.SetControllerReference(parentJob, childJob, k8sClient.Scheme())).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, childJob)).Should(gomega.Succeed())

			ginkgo.By("Checking that the queue name of the parent workload isn't updated with an empty value")
			parentWorkload = &kueue.Workload{}
			gomega.Consistently(func() bool {
				if err := k8sClient.Get(ctx, parentWlLookupKey, parentWorkload); err != nil {
					return true
				}
				return parentWorkload.Spec.QueueName == jobQueueName
			}, util.ConsistentDuration, util.Interval).Should(gomega.BeTrue())
		})

		ginkgo.It("Should change the suspension status of the child job when the parent's workload is not admitted", func() {
			ginkgo.By("Create a resource flavor")
			defaultFlavor := testing.MakeResourceFlavor("default").Label(instanceKey, "default").Obj()
			gomega.Expect(k8sClient.Create(ctx, defaultFlavor)).Should(gomega.Succeed())

			ginkgo.By("creating the parent job")
			parentJob := testingjob.MakeJob(parentJobName, ns.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, parentJob)).Should(gomega.Succeed())

			ginkgo.By("waiting for the parent workload to be created")
			parentWorkload := &kueue.Workload{}
			parentWlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(parentJob.Name, parentJob.UID), Namespace: ns.Name}
			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, parentWlLookupKey, parentWorkload)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Creating the child job with the parent-workload annotation")
			childJob := testingjob.MakeJob(childJobName, ns.Name).Suspend(false).Obj()
			gomega.Expect(ctrl.SetControllerReference(parentJob, childJob, k8sClient.Scheme())).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, childJob)).Should(gomega.Succeed())

			ginkgo.By("checking that the child job is suspended")
			gomega.Eventually(func() *bool {
				gomega.Expect(k8sClient.Get(ctx, childLookupKey, childJob)).Should(gomega.Succeed())
				return childJob.Spec.Suspend
			}, util.Timeout, util.Interval).Should(gomega.Equal(ptr.To(true)))
		})
	})

	ginkgo.When("A prebuilt workload is used", func() {
		ginkgo.It("Should get suspended if the workload is not found", func() {
			job := testingjob.MakeJob("job", ns.Name).
				Queue("main").
				Label(constants.PrebuiltWorkloadLabel, "missing-workload").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, job)).To(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				createdJob := batchv1.Job{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), &createdJob)).To(gomega.Succeed())
				g.Expect(ptr.Deref(createdJob.Spec.Suspend, false)).To(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should take the ownership of the workload and continue the usual execution", func() {
			container := corev1.Container{
				Name:  "c",
				Image: "pause",
			}
			testingjob.SetContainerDefaults(&container)
			wl := testing.MakeWorkload("wl", ns.Name).
				PodSets(*testing.MakePodSet("main", 1).
					Containers(*container.DeepCopy()).
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())
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
			gomega.Expect(k8sClient.Create(ctx, job)).To(gomega.Succeed())
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

					g.Expect(createdWl.OwnerReferences).To(gomega.ContainElement(
						gomega.BeComparableTo(metav1.OwnerReference{
							Name: job.Name,
							UID:  job.UID,
						}, cmpopts.IgnoreFields(metav1.OwnerReference{}, "APIVersion", "Kind", "Controller", "BlockOwnerDeletion"))))

					// The workload is not marked as finished.
					g.Expect(apimeta.IsStatusConditionTrue(createdWl.Status.Conditions, kueue.WorkloadFinished)).To(gomega.BeFalse())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Admitting the workload, the job should unsuspend", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdWl := kueue.Workload{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &createdWl)).To(gomega.Succeed())

					admission := testing.MakeAdmission("cq", container.Name).Obj()
					g.Expect(util.SetQuotaReservation(ctx, k8sClient, wl, admission)).To(gomega.Succeed())
					util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, wl)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

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
					createdJob.Status.Succeeded = 1
					createdJob.Status.Conditions = []batchv1.JobCondition{
						{
							Type:               batchv1.JobComplete,
							Status:             corev1.ConditionTrue,
							LastProbeTime:      metav1.Now(),
							LastTransitionTime: metav1.Now(),
							Reason:             "ByTest",
							Message:            "Job finished successfully",
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

	ginkgo.It("Should finish the preemption when the job becomes inactive", func() {
		job := testingjob.MakeJob(jobName, ns.Name).Queue("q").Obj()
		wl := &kueue.Workload{}
		var wlLookupKey types.NamespacedName
		ginkgo.By("create the job and admit the workload", func() {
			gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
			wlLookupKey = types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: ns.Name}
			gomega.Eventually(func() error { return k8sClient.Get(ctx, wlLookupKey, wl) }, util.Timeout, util.Interval).Should(gomega.Succeed())
			admission := testing.MakeAdmission("q", job.Spec.Template.Spec.Containers[0].Name).Obj()
			gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, wl, admission)).To(gomega.Succeed())
			util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, wl)
		})

		ginkgo.By("wait for the job to be unsuspended", func() {
			gomega.Eventually(func() bool {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), job)).To(gomega.Succeed())
				return *job.Spec.Suspend
			}, util.Timeout, util.Interval).Should(gomega.BeFalse())
		})

		ginkgo.By("mark the job as active", func() {
			gomega.Eventually(func() error {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), job)).To(gomega.Succeed())
				job.Status.Active = 1
				return k8sClient.Status().Update(ctx, job)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("preempt the workload", func() {
			gomega.Eventually(func() error {
				gomega.Expect(k8sClient.Get(ctx, wlLookupKey, wl)).To(gomega.Succeed())
				return workload.UpdateStatus(ctx, k8sClient, wl, kueue.WorkloadEvicted, metav1.ConditionTrue, kueue.WorkloadEvictedByPreemption, "By test", "evict")
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("wait for the job to be suspended", func() {
			gomega.Eventually(func() bool {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), job)).To(gomega.Succeed())
				return *job.Spec.Suspend
			}, util.Timeout, util.Interval).Should(gomega.BeTrue())
		})

		ginkgo.By("the workload should stay admitted", func() {
			gomega.Consistently(func() bool {
				gomega.Expect(k8sClient.Get(ctx, wlLookupKey, wl)).To(gomega.Succeed())
				return apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
			}, util.ConsistentDuration, util.Interval).Should(gomega.BeTrue())
		})

		ginkgo.By("mark the job as inactive", func() {
			gomega.Eventually(func() error {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), job)).To(gomega.Succeed())
				job.Status.Active = 0
				return k8sClient.Status().Update(ctx, job)
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
			admissionCheck = testing.MakeAdmissionCheck("check").ControllerName("ac-controller").Obj()
			gomega.Expect(k8sClient.Create(ctx, admissionCheck)).To(gomega.Succeed())
			util.SetAdmissionCheckActive(ctx, k8sClient, admissionCheck, metav1.ConditionTrue)
			clusterQueueAc = testing.MakeClusterQueue("prod-cq-with-checks").
				ResourceGroup(
					*testing.MakeFlavorQuotas("test-flavor").Resource(corev1.ResourceCPU, "5").Obj(),
				).AdmissionChecks("check").Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueueAc)).Should(gomega.Succeed())
			localQueue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueueAc.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())
			testFlavor = testing.MakeResourceFlavor("test-flavor").Label(instanceKey, "test-flavor").Obj()
			gomega.Expect(k8sClient.Create(ctx, testFlavor)).Should(gomega.Succeed())

			jobLookupKey = &types.NamespacedName{Name: jobName, Namespace: ns.Name}
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteAdmissionCheck(ctx, k8sClient, admissionCheck)).To(gomega.Succeed())
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, testFlavor, true)
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, clusterQueueAc, true)
		})

		ginkgo.It("labels and annotations should be propagated from admission check to job", func() {
			createdJob := &batchv1.Job{}
			createdWorkload := &kueue.Workload{}
			job := testingjob.MakeJob(jobName, ns.Name).
				Queue(localQueue.Name).
				Request(corev1.ResourceCPU, "5").
				PodAnnotation("old-ann-key", "old-ann-value").
				PodLabel("old-label-key", "old-label-value").
				Obj()

			ginkgo.By("creating the job with pod labels & annotations", func() {
				gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
			})

			ginkgo.By("fetch the job and verify it is suspended as the checks are not ready", func() {
				gomega.Eventually(func() *bool {
					gomega.Expect(k8sClient.Get(ctx, *jobLookupKey, createdJob)).Should(gomega.Succeed())
					return createdJob.Spec.Suspend
				}, util.Timeout, util.Interval).Should(gomega.Equal(ptr.To(true)))
			})

			wlLookupKey := &types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: ns.Name}
			ginkgo.By("fetch the created workload", func() {
				gomega.Eventually(func() error {
					return k8sClient.Get(ctx, *wlLookupKey, createdWorkload)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("add labels & annotations to the workload admission check in PodSetUpdates", func() {
				gomega.Eventually(func() error {
					var newWL kueue.Workload
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(createdWorkload), &newWL)).To(gomega.Succeed())
					workload.SetAdmissionCheckState(&newWL.Status.AdmissionChecks, kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: "main",
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
										Key:      "selector1",
										Value:    "selector-value1",
										Operator: corev1.TolerationOpEqual,
										Effect:   corev1.TaintEffectNoSchedule,
									},
								},
							},
						},
					})
					return k8sClient.Status().Update(ctx, &newWL)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("admit the workload", func() {
				admission := testing.MakeAdmission(clusterQueueAc.Name).
					Assignment(corev1.ResourceCPU, "test-flavor", "1").
					AssignmentPodCount(createdWorkload.Spec.PodSets[0].Count).
					Obj()
				gomega.Expect(k8sClient.Get(ctx, *wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, admission)).Should(gomega.Succeed())
				util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
			})

			ginkgo.By("await for the job to be admitted", func() {
				gomega.Eventually(func() *bool {
					gomega.Expect(k8sClient.Get(ctx, *jobLookupKey, createdJob)).Should(gomega.Succeed())
					return createdJob.Spec.Suspend
				}, util.Timeout, util.Interval).Should(gomega.Equal(ptr.To(false)))
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
							Key:      "selector1",
							Value:    "selector-value1",
							Operator: corev1.TolerationOpEqual,
							Effect:   corev1.TaintEffectNoSchedule,
						},
					},
				))
			})

			ginkgo.By("delete the localQueue to prevent readmission", func() {
				gomega.Expect(util.DeleteLocalQueue(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
			})

			ginkgo.By("clear the workload's admission to stop the job", func() {
				gomega.Expect(k8sClient.Get(ctx, *wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, nil)).Should(gomega.Succeed())
				util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
			})

			ginkgo.By("await for the job to be suspended", func() {
				gomega.Eventually(func() *bool {
					gomega.Expect(k8sClient.Get(ctx, *jobLookupKey, createdJob)).Should(gomega.Succeed())
					return createdJob.Spec.Suspend
				}, util.Timeout, util.Interval).Should(gomega.Equal(ptr.To(true)))
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

		ginkgo.It("should not admit workload if there is a conflict in labels", func() {
			createdJob := &batchv1.Job{}
			createdWorkload := &kueue.Workload{}
			job := testingjob.MakeJob(jobName, ns.Name).
				Queue(localQueue.Name).
				Request(corev1.ResourceCPU, "5").
				PodLabel("label-key", "old-label-value").
				Obj()

			ginkgo.By("creating the job with default priority", func() {
				gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
			})

			wlLookupKey := &types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: ns.Name}
			ginkgo.By("fetch the created job & workload", func() {
				gomega.Eventually(func() *bool {
					gomega.Expect(k8sClient.Get(ctx, *jobLookupKey, createdJob)).Should(gomega.Succeed())
					return createdJob.Spec.Suspend
				}, util.Timeout, util.Interval).Should(gomega.Equal(ptr.To(true)))
				gomega.Eventually(func() error {
					return k8sClient.Get(ctx, *wlLookupKey, createdWorkload)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("add a conflicting label to the admission check in PodSetUpdates", func() {
				gomega.Eventually(func() error {
					var newWL kueue.Workload
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(createdWorkload), &newWL)).To(gomega.Succeed())
					workload.SetAdmissionCheckState(&newWL.Status.AdmissionChecks, kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: "main",
								Labels: map[string]string{
									"label-key": "new-label-value",
								},
							},
						},
					})
					return k8sClient.Status().Update(ctx, &newWL)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("attempt to admit the workload", func() {
				admission := testing.MakeAdmission(clusterQueueAc.Name).
					Assignment(corev1.ResourceCPU, "test-flavor", "1").
					AssignmentPodCount(createdWorkload.Spec.PodSets[0].Count).
					Obj()
				gomega.Expect(k8sClient.Get(ctx, *wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, admission)).Should(gomega.Succeed())
				util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
			})

			ginkgo.By("verify the job is not started", func() {
				gomega.Consistently(func() *bool {
					gomega.Expect(k8sClient.Get(ctx, *jobLookupKey, createdJob)).Should(gomega.Succeed())
					return createdJob.Spec.Suspend
				}, util.ConsistentDuration, util.Interval).Should(gomega.Equal(ptr.To(true)))
			})

			ginkgo.By("verify the job has the old label value", func() {
				gomega.Expect(createdJob.Spec.Template.Labels).Should(gomega.HaveKeyWithValue("label-key", "old-label-value"))
			})
		})
	})
})

var _ = ginkgo.Describe("Job controller when waitForPodsReady enabled", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	type podsReadyTestSpec struct {
		beforeJobStatus *batchv1.JobStatus
		beforeCondition *metav1.Condition
		jobStatus       batchv1.JobStatus
		suspended       bool
		wantCondition   *metav1.Condition
	}

	var (
		ns            *corev1.Namespace
		defaultFlavor = testing.MakeResourceFlavor("default").Label(instanceKey, "default").Obj()
	)

	ginkgo.BeforeAll(func() {
		fwk = &framework.Framework{
			CRDPath: crdPath,
		}
		cfg = fwk.Init()
		ctx, k8sClient = fwk.RunManager(cfg, managerSetup(jobframework.WithWaitForPodsReady(&configapi.WaitForPodsReady{Enable: true})))
		ginkgo.By("Create a resource flavor")
		gomega.Expect(k8sClient.Create(ctx, defaultFlavor)).Should(gomega.Succeed())
	})
	ginkgo.AfterAll(func() {
		util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, defaultFlavor, true)
		fwk.Teardown()
	})

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
			job := testingjob.MakeJob(jobName, ns.Name).Parallelism(2).Obj()
			jobQueueName := "test-queue"
			job.Annotations = map[string]string{constants.QueueAnnotation: jobQueueName}
			gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
			lookupKey := types.NamespacedName{Name: jobName, Namespace: ns.Name}
			createdJob := &batchv1.Job{}
			gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())

			ginkgo.By("Fetch the workload created for the job")
			createdWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: ns.Name}
			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, wlLookupKey, createdWorkload)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Admit the workload created for the job")
			admission := testing.MakeAdmission("foo").
				Assignment(corev1.ResourceCPU, "default", "1m").
				AssignmentPodCount(createdWorkload.Spec.PodSets[0].Count).
				Obj()
			gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, admission)).Should(gomega.Succeed())
			util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
			gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())

			ginkgo.By("Await for the job to be unsuspended")
			gomega.Eventually(func() *bool {
				gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
				return createdJob.Spec.Suspend
			}, util.Timeout, util.Interval).Should(gomega.Equal(ptr.To(false)))

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
				}, util.Timeout, util.Interval).Should(gomega.BeComparableTo(podsReadyTestSpec.beforeCondition, util.IgnoreConditionTimestampsAndObservedGeneration))
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
					return util.SetQuotaReservation(ctx, k8sClient, createdWorkload, nil)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
				util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
			}

			ginkgo.By("Verify the PodsReady condition is added")
			gomega.Eventually(func() *metav1.Condition {
				gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				return apimeta.FindStatusCondition(createdWorkload.Status.Conditions, kueue.WorkloadPodsReady)
			}, util.Timeout, util.Interval).Should(gomega.BeComparableTo(podsReadyTestSpec.wantCondition, util.IgnoreConditionTimestampsAndObservedGeneration))
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
				Ready: ptr.To[int32](1),
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
				Ready: ptr.To[int32](2),
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
				Ready:     ptr.To[int32](1),
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
				Ready:     ptr.To[int32](0),
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
				Ready:     ptr.To[int32](0),
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
				Ready: ptr.To[int32](2),
			},
			beforeCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionTrue,
				Reason:  "PodsReady",
				Message: "All pods were ready or succeeded since the workload admission",
			},
			jobStatus: batchv1.JobStatus{
				Ready:  ptr.To[int32](1),
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
				Ready: ptr.To[int32](2),
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
				Ready: ptr.To[int32](2),
			},
			beforeCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionTrue,
				Reason:  "PodsReady",
				Message: "All pods were ready or succeeded since the workload admission",
			},
			jobStatus: batchv1.JobStatus{
				Ready: ptr.To[int32](2),
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
	)

	ginkgo.BeforeAll(func() {
		fwk = &framework.Framework{
			CRDPath: crdPath,
		}
		cfg = fwk.Init()
		ctx, k8sClient = fwk.RunManager(cfg, managerAndControllersSetup(true, nil))
	})
	ginkgo.AfterAll(func() {
		fwk.Teardown()
	})

	ginkgo.BeforeEach(func() {
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
			FlavorFungibility(kueue.FlavorFungibility{
				WhenCanBorrow:  kueue.Borrow,
				WhenCanPreempt: kueue.TryNextFlavor,
			}).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, devClusterQ)).Should(gomega.Succeed())
		podsCountClusterQ = testing.MakeClusterQueue("pods-clusterqueue").
			ResourceGroup(
				*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourcePods, "5").Obj(),
			).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, podsCountClusterQ)).Should(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, prodClusterQ, true)
		util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, devClusterQ, true)
		util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, podsCountClusterQ, true)
		util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, spotTaintedFlavor, true)
		util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, spotUntaintedFlavor, true)
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
		}, util.Timeout, util.Interval).Should(gomega.Equal(ptr.To(false)))
		gomega.Expect(createdProdJob1.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))
		util.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 0)
		util.ExpectReservingActiveWorkloadsMetric(prodClusterQ, 1)

		ginkgo.By("checking a second no-fit prod job does not start")
		prodJob2 := testingjob.MakeJob("prod-job2", ns.Name).Queue(prodLocalQ.Name).Request(corev1.ResourceCPU, "5").Obj()
		gomega.Expect(k8sClient.Create(ctx, prodJob2)).Should(gomega.Succeed())
		lookupKey2 := types.NamespacedName{Name: prodJob2.Name, Namespace: prodJob2.Namespace}
		createdProdJob2 := &batchv1.Job{}
		gomega.Consistently(func() *bool {
			gomega.Expect(k8sClient.Get(ctx, lookupKey2, createdProdJob2)).Should(gomega.Succeed())
			return createdProdJob2.Spec.Suspend
		}, util.ConsistentDuration, util.Interval).Should(gomega.Equal(ptr.To(true)))
		util.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 1)
		util.ExpectReservingActiveWorkloadsMetric(prodClusterQ, 1)

		ginkgo.By("checking a dev job starts")
		devJob := testingjob.MakeJob("dev-job", ns.Name).Queue(devLocalQ.Name).Request(corev1.ResourceCPU, "5").Obj()
		gomega.Expect(k8sClient.Create(ctx, devJob)).Should(gomega.Succeed())
		createdDevJob := &batchv1.Job{}
		gomega.Eventually(func() *bool {
			gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: devJob.Name, Namespace: devJob.Namespace}, createdDevJob)).
				Should(gomega.Succeed())
			return createdDevJob.Spec.Suspend
		}, util.Timeout, util.Interval).Should(gomega.Equal(ptr.To(false)))
		gomega.Expect(createdDevJob.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(spotUntaintedFlavor.Name))
		util.ExpectPendingWorkloadsMetric(devClusterQ, 0, 0)
		util.ExpectReservingActiveWorkloadsMetric(devClusterQ, 1)

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
		}, util.Timeout, util.Interval).Should(gomega.Equal(ptr.To(false)))
		gomega.Expect(createdProdJob2.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))
		util.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 0)
		util.ExpectReservingActiveWorkloadsMetric(prodClusterQ, 1)
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
		}, util.Timeout, util.Interval).Should(gomega.Equal(ptr.To(true)))

		ginkgo.By("creating another localQueue of the same name and in the same namespace as the job")
		prodLocalQ = testing.MakeLocalQueue(localQueue.Name, ns.Name).ClusterQueue(prodClusterQ.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, prodLocalQ)).Should(gomega.Succeed())

		ginkgo.By("job should be unsuspended and NodeSelector properly set")
		gomega.Eventually(func() *bool {
			gomega.Expect(k8sClient.Get(ctx, lookupKey, createdProdJob)).Should(gomega.Succeed())
			return createdProdJob.Spec.Suspend
		}, util.Timeout, util.Interval).Should(gomega.Equal(ptr.To(false)))

		runningSelector := maps.Clone(createdProdJob.Spec.Template.Spec.NodeSelector)

		gomega.Expect(runningSelector).To(gomega.Equal(map[string]string{instanceKey: "on-demand"}))
	})

	ginkgo.When("The workload's admission is removed", func() {
		ginkgo.It("Should restore the original node selectors", func() {
			localQueue := testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(prodClusterQ.Name).Obj()
			job := testingjob.MakeJob(jobName, ns.Name).Queue(localQueue.Name).Request(corev1.ResourceCPU, "2").Obj()
			lookupKey := types.NamespacedName{Name: job.Name, Namespace: job.Namespace}
			createdJob := &batchv1.Job{}

			ginkgo.By("create a job", func() {
				gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
			})

			ginkgo.By("job should be suspend", func() {
				gomega.Eventually(func() *bool {
					gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
					return createdJob.Spec.Suspend
				}, util.Timeout, util.Interval).Should(gomega.Equal(ptr.To(true)))
			})

			// backup the podSet's node selector
			originalNodeSelector := createdJob.Spec.Template.Spec.NodeSelector

			ginkgo.By("create a localQueue", func() {
				gomega.Expect(k8sClient.Create(ctx, localQueue)).Should(gomega.Succeed())
			})

			ginkgo.By("job should be unsuspended", func() {
				gomega.Eventually(func() *bool {
					gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
					return createdJob.Spec.Suspend
				}, util.Timeout, util.Interval).Should(gomega.Equal(ptr.To(false)))
			})

			ginkgo.By("the node selector should be updated", func() {
				gomega.Eventually(func() map[string]string {
					gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
					return createdJob.Spec.Template.Spec.NodeSelector
				}, util.Timeout, util.Interval).ShouldNot(gomega.Equal(originalNodeSelector))
			})

			ginkgo.By("delete the localQueue to prevent readmission", func() {
				gomega.Expect(util.DeleteLocalQueue(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
			})

			ginkgo.By("clear the workload's admission to stop the job", func() {
				wl := &kueue.Workload{}
				wlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: job.Namespace}
				gomega.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
				gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, wl, nil)).Should(gomega.Succeed())
				util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, wl)
			})

			ginkgo.By("the node selector should be restored", func() {
				gomega.Eventually(func() map[string]string {
					gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
					return createdJob.Spec.Template.Spec.NodeSelector
				}, util.Timeout, util.Interval).Should(gomega.Equal(originalNodeSelector))
			})
		})
	})

	ginkgo.When("The workload is deleted while admitted", func() {
		ginkgo.It("Should restore the original node selectors", func() {
			localQueue := testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(prodClusterQ.Name).Obj()
			job := testingjob.MakeJob(jobName, ns.Name).Queue(localQueue.Name).Request(corev1.ResourceCPU, "2").Suspend(false).Obj()
			lookupKey := types.NamespacedName{Name: job.Name, Namespace: job.Namespace}
			createdJob := &batchv1.Job{}

			ginkgo.By("create a job", func() {
				gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
			})

			ginkgo.By("job should be suspend", func() {
				gomega.Eventually(func() *bool {
					gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
					return createdJob.Spec.Suspend
				}, util.Timeout, util.Interval).Should(gomega.Equal(ptr.To[bool](true)))
			})

			// backup the podSet's node selector
			originalNodeSelector := createdJob.Spec.Template.Spec.NodeSelector

			ginkgo.By("create a localQueue", func() {
				gomega.Expect(k8sClient.Create(ctx, localQueue)).Should(gomega.Succeed())
			})

			ginkgo.By("job should be unsuspended", func() {
				gomega.Eventually(func() *bool {
					gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
					return createdJob.Spec.Suspend
				}, util.Timeout, util.Interval).Should(gomega.Equal(ptr.To[bool](false)))
			})

			ginkgo.By("the node selector should be updated", func() {
				gomega.Eventually(func() map[string]string {
					gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
					return createdJob.Spec.Template.Spec.NodeSelector
				}, util.Timeout, util.Interval).ShouldNot(gomega.Equal(originalNodeSelector))
			})

			ginkgo.By("delete the localQueue to prevent readmission", func() {
				gomega.Expect(util.DeleteLocalQueue(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
			})

			ginkgo.By("deleting the workload", func() {
				wl := &kueue.Workload{}
				wlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: job.Namespace}
				gomega.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
				gomega.Expect(k8sClient.Delete(ctx, wl)).Should(gomega.Succeed())
			})

			ginkgo.By("the node selector should be restored", func() {
				gomega.Eventually(func() map[string]string {
					gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
					return createdJob.Spec.Template.Spec.NodeSelector
				}, util.Timeout, util.Interval).Should(gomega.Equal(originalNodeSelector))
			})
		})
	})

	ginkgo.When("The job is deleted while admitted", func() {
		ginkgo.It("Its workload finalizer should be removed", func() {
			localQueue := testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(prodClusterQ.Name).Obj()
			job := testingjob.MakeJob(jobName, ns.Name).Queue(localQueue.Name).Request(corev1.ResourceCPU, "2").Suspend(false).Obj()
			lookupKey := types.NamespacedName{Name: job.Name, Namespace: job.Namespace}
			createdJob := &batchv1.Job{}

			ginkgo.By("create a job", func() {
				gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
			})

			ginkgo.By("job should be suspend", func() {
				gomega.Eventually(func() *bool {
					gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
					return createdJob.Spec.Suspend
				}, util.Timeout, util.Interval).Should(gomega.Equal(ptr.To[bool](true)))
			})

			ginkgo.By("create a localQueue", func() {
				gomega.Expect(k8sClient.Create(ctx, localQueue)).Should(gomega.Succeed())
			})

			ginkgo.By("job should be unsuspended", func() {
				gomega.Eventually(func() *bool {
					gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
					return createdJob.Spec.Suspend
				}, util.Timeout, util.Interval).Should(gomega.Equal(ptr.To[bool](false)))
			})

			wlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: job.Namespace}
			ginkgo.By("checking the finalizer is set", func() {
				gomega.Eventually(func() []string {
					wl := &kueue.Workload{}
					gomega.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
					return wl.Finalizers
				}, util.Timeout, util.Interval).Should(gomega.ContainElement(kueue.ResourceInUseFinalizerName))
			})

			ginkgo.By("deleting the job", func() {
				gomega.Expect(k8sClient.Delete(ctx, job)).Should(gomega.Succeed())
			})

			ginkgo.By("checking that its workloads finalizer is removed", func() {
				gomega.Eventually(func() []string {
					wl := &kueue.Workload{}
					err := k8sClient.Get(ctx, wlKey, wl)
					if err != nil {
						if apierrors.IsNotFound(err) {
							return []string{}
						}
						return []string{kueue.ResourceInUseFinalizerName}
					}
					return wl.Finalizers
				}, util.Timeout, util.Interval).ShouldNot(gomega.ContainElement(kueue.ResourceInUseFinalizerName))
			})
		})
	})

	ginkgo.It("Should allow reclaim of resources that are no longer needed", func() {
		ginkgo.By("creating localQueue", func() {
			prodLocalQ = testing.MakeLocalQueue("prod-queue", ns.Name).ClusterQueue(prodClusterQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, prodLocalQ)).Should(gomega.Succeed())
		})

		job1 := testingjob.MakeJob("job1", ns.Name).Queue(prodLocalQ.Name).
			Request(corev1.ResourceCPU, "2").
			Completions(5).
			Parallelism(2).
			Obj()
		lookupKey1 := types.NamespacedName{Name: job1.Name, Namespace: job1.Namespace}

		ginkgo.By("checking the first job starts", func() {
			gomega.Expect(k8sClient.Create(ctx, job1)).Should(gomega.Succeed())
			createdJob1 := &batchv1.Job{}
			gomega.Eventually(func() *bool {
				gomega.Expect(k8sClient.Get(ctx, lookupKey1, createdJob1)).Should(gomega.Succeed())
				return createdJob1.Spec.Suspend
			}, util.Timeout, util.Interval).Should(gomega.Equal(ptr.To(false)))
			gomega.Expect(createdJob1.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))
			util.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 0)
			util.ExpectReservingActiveWorkloadsMetric(prodClusterQ, 1)
		})

		job2 := testingjob.MakeJob("job2", ns.Name).Queue(prodLocalQ.Name).Request(corev1.ResourceCPU, "3").Obj()
		lookupKey2 := types.NamespacedName{Name: job2.Name, Namespace: job2.Namespace}

		ginkgo.By("checking a second no-fit job does not start", func() {
			gomega.Expect(k8sClient.Create(ctx, job2)).Should(gomega.Succeed())
			createdJob2 := &batchv1.Job{}
			gomega.Consistently(func() *bool {
				gomega.Expect(k8sClient.Get(ctx, lookupKey2, createdJob2)).Should(gomega.Succeed())
				return createdJob2.Spec.Suspend
			}, util.ConsistentDuration, util.Interval).Should(gomega.Equal(ptr.To(true)))
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
			gomega.Eventually(func() []kueue.ReclaimablePod {
				gomega.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
				return wl.Status.ReclaimablePods
			}, util.Timeout, util.Interval).Should(gomega.BeComparableTo([]kueue.ReclaimablePod{{
				Name:  "main",
				Count: 1,
			}}))

			createdJob2 := &batchv1.Job{}
			gomega.Eventually(func() *bool {
				gomega.Expect(k8sClient.Get(ctx, lookupKey2, createdJob2)).Should(gomega.Succeed())
				return createdJob2.Spec.Suspend
			}, util.Timeout, util.Interval).Should(gomega.Equal(ptr.To(false)))
			gomega.Expect(createdJob2.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))

			util.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 0)
			util.ExpectReservingActiveWorkloadsMetric(prodClusterQ, 2)
		})
	})

	ginkgo.It("Should readmit preempted Job with priorityClass in alternative flavor", func() {
		devLocalQ = testing.MakeLocalQueue("dev-queue", ns.Name).ClusterQueue(devClusterQ.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, devLocalQ)).Should(gomega.Succeed())

		highPriorityClass := testing.MakePriorityClass("high").PriorityValue(100).Obj()
		gomega.Expect(k8sClient.Create(ctx, highPriorityClass)).Should(gomega.Succeed())
		ginkgo.DeferCleanup(func() {
			gomega.Expect(k8sClient.Delete(ctx, highPriorityClass)).To(gomega.Succeed())
		})

		lowJobKey := types.NamespacedName{Name: "low", Namespace: ns.Name}
		ginkgo.By("Low priority job is unsuspended and has nodeSelector", func() {
			job := testingjob.MakeJob("low", ns.Name).
				Queue(devLocalQ.Name).
				Parallelism(5).
				Request(corev1.ResourceCPU, "1").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())

			expectJobUnsuspendedWithNodeSelectors(lowJobKey, map[string]string{
				instanceKey: "spot-untainted",
			})
		})

		ginkgo.By("High priority job preemtps low priority job", func() {
			job := testingjob.MakeJob("high", ns.Name).
				Queue(devLocalQ.Name).
				PriorityClass("high").
				Parallelism(5).
				Request(corev1.ResourceCPU, "1").
				NodeSelector(instanceKey, "spot-untainted"). // target the same flavor to cause preemption
				Obj()
			gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())

			highJobKey := types.NamespacedName{Name: "high", Namespace: ns.Name}
			expectJobUnsuspendedWithNodeSelectors(highJobKey, map[string]string{
				instanceKey: "spot-untainted",
			})
		})

		ginkgo.By("Preempted job should be admitted on second flavor", func() {
			expectJobUnsuspendedWithNodeSelectors(lowJobKey, map[string]string{
				instanceKey: "on-demand",
			})
		})
	})

	ginkgo.It("Should readmit preempted Job with workloadPriorityClass in alternative flavor", func() {
		devLocalQ = testing.MakeLocalQueue("dev-queue", ns.Name).ClusterQueue(devClusterQ.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, devLocalQ)).Should(gomega.Succeed())

		highWorkloadPriorityClass := testing.MakeWorkloadPriorityClass("high-workload").PriorityValue(100).Obj()
		gomega.Expect(k8sClient.Create(ctx, highWorkloadPriorityClass)).Should(gomega.Succeed())
		ginkgo.DeferCleanup(func() {
			gomega.Expect(k8sClient.Delete(ctx, highWorkloadPriorityClass)).To(gomega.Succeed())
		})

		lowJobKey := types.NamespacedName{Name: "low", Namespace: ns.Name}
		ginkgo.By("Low priority job is unsuspended and has nodeSelector", func() {
			job := testingjob.MakeJob("low", ns.Name).
				Queue(devLocalQ.Name).
				Parallelism(5).
				Request(corev1.ResourceCPU, "1").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())

			expectJobUnsuspendedWithNodeSelectors(lowJobKey, map[string]string{
				instanceKey: "spot-untainted",
			})
		})

		ginkgo.By("High priority job preemtps low priority job", func() {
			job := testingjob.MakeJob("high", ns.Name).
				Queue(devLocalQ.Name).
				WorkloadPriorityClass("high-workload").
				Parallelism(5).
				Request(corev1.ResourceCPU, "1").
				NodeSelector(instanceKey, "spot-untainted"). // target the same flavor to cause preemption
				Obj()
			gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())

			highJobKey := types.NamespacedName{Name: "high", Namespace: ns.Name}
			expectJobUnsuspendedWithNodeSelectors(highJobKey, map[string]string{
				instanceKey: "spot-untainted",
			})
		})

		ginkgo.By("Preempted job should be admitted on second flavor", func() {
			expectJobUnsuspendedWithNodeSelectors(lowJobKey, map[string]string{
				instanceKey: "on-demand",
			})
		})
	})

	ginkgo.It("Should schedule jobs with partial admission", func() {
		prodLocalQ = testing.MakeLocalQueue("prod-queue", ns.Name).ClusterQueue(prodClusterQ.Name).Obj()
		job1 := testingjob.MakeJob("job1", ns.Name).
			Queue(prodLocalQ.Name).
			Parallelism(5).
			Completions(6).
			Request(corev1.ResourceCPU, "2").
			Obj()
		jobKey := types.NamespacedName{Name: job1.Name, Namespace: job1.Namespace}

		ginkgo.By("creating localQueues")
		gomega.Expect(k8sClient.Create(ctx, prodLocalQ)).Should(gomega.Succeed())

		ginkgo.By("creating the job")
		gomega.Expect(k8sClient.Create(ctx, job1)).Should(gomega.Succeed())
		wlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job1.Name, job1.UID), Namespace: job1.Namespace}

		createdJob := &batchv1.Job{}
		ginkgo.By("the job should stay suspended", func() {
			gomega.Consistently(func() *bool {
				gomega.Expect(k8sClient.Get(ctx, jobKey, createdJob)).Should(gomega.Succeed())
				return createdJob.Spec.Suspend
			}, util.ConsistentDuration, util.Interval).Should(gomega.Equal(ptr.To(true)))
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
			gomega.Eventually(func() *bool {
				gomega.Expect(k8sClient.Get(ctx, jobKey, createdJob)).Should(gomega.Succeed())
				return createdJob.Spec.Suspend
			}, util.Timeout, util.Interval).Should(gomega.Equal(ptr.To(false)))
			gomega.Expect(*createdJob.Spec.Parallelism).To(gomega.BeEquivalentTo(2))

			gomega.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
			gomega.Expect(wl.Spec.PodSets[0].MinCount).ToNot(gomega.BeNil())
			gomega.Expect(*wl.Spec.PodSets[0].MinCount).To(gomega.BeEquivalentTo(1))
		})

		ginkgo.By("delete the localQueue to prevent readmission", func() {
			gomega.Expect(util.DeleteLocalQueue(ctx, k8sClient, prodLocalQ)).Should(gomega.Succeed())
		})

		ginkgo.By("clear the workloads admission to stop the job", func() {
			gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, wl, nil)).To(gomega.Succeed())
			util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, wl)
		})

		ginkgo.By("job should be suspended and its parallelism restored", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, jobKey, createdJob)).Should(gomega.Succeed())
				g.Expect(ptr.Deref(createdJob.Spec.Suspend, false)).To(gomega.BeTrue(), "the job should be suspended")
				g.Expect(ptr.Deref(createdJob.Spec.Parallelism, 0)).To(gomega.BeEquivalentTo(5))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should set the flavor's node selectors if the job is admitted by pods count only", func() {
		localQ := testing.MakeLocalQueue("dev-queue", ns.Name).ClusterQueue(podsCountClusterQ.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, localQ)).Should(gomega.Succeed())
		ginkgo.By("Creating a job with no requests, will set the resource flavors selectors when admitted ", func() {
			job := testingjob.MakeJob("job", ns.Name).
				Queue(localQ.Name).
				Parallelism(2).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
			expectJobUnsuspendedWithNodeSelectors(client.ObjectKeyFromObject(job), map[string]string{
				instanceKey: "on-demand",
			})
		})
	})
	ginkgo.It("Should schedule updated job and update the workload", func() {
		localQueue := testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(prodClusterQ.Name).Obj()
		ginkgo.By("create a localQueue", func() {
			gomega.Expect(k8sClient.Create(ctx, localQueue)).Should(gomega.Succeed())
		})

		job := testingjob.MakeJob(jobName, ns.Name).Queue(localQueue.Name).Request(corev1.ResourceCPU, "3").Parallelism(2).Suspend(false).Obj()
		lookupKey := types.NamespacedName{Name: job.Name, Namespace: job.Namespace}
		createdJob := &batchv1.Job{}

		ginkgo.By("creating the job that doesn't fit", func() {
			gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
		})

		ginkgo.By("job should be suspend", func() {
			gomega.Eventually(func() *bool {
				gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
				return createdJob.Spec.Suspend
			}, util.Timeout, util.Interval).Should(gomega.Equal(ptr.To(true)))
		})

		wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: ns.Name}
		createdWorkload := util.AwaitAndVerifyCreatedWorkload(ctx, k8sClient, wlLookupKey, createdJob)
		createdTime := createdWorkload.CreationTimestamp

		createdJob.Spec.Parallelism = ptr.To[int32](1)

		ginkgo.By("updating the job", func() {
			gomega.Expect(k8sClient.Update(ctx, createdJob)).Should(gomega.Succeed())
		})

		createdWorkload = util.AwaitAndVerifyCreatedWorkload(ctx, k8sClient, wlLookupKey, createdJob)

		ginkgo.By("updated job should be unsuspended", func() {
			gomega.Eventually(func() *bool {
				gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
				return createdJob.Spec.Suspend
			}, util.Timeout, util.Interval).Should(gomega.Equal(ptr.To(false)))
		})

		ginkgo.By("updated workload should have the same created timestamp", func() {
			gomega.Expect(createdWorkload.CreationTimestamp).Should(gomega.Equal(createdTime))
		})
	})

	ginkgo.When("Suspend a running Job without requeuing through Workload's spec.active field", func() {
		ginkgo.It("Should not readmit a job to the queue after Active is changed to false", func() {
			ginkgo.By("creating localQueue")
			localQueue := testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(prodClusterQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).Should(gomega.Succeed())

			sampleJob := testingjob.MakeJob("job1", ns.Name).Queue(localQueue.Name).Request(corev1.ResourceCPU, "2").Obj()
			lookupKey1 := types.NamespacedName{Name: sampleJob.Name, Namespace: sampleJob.Namespace}
			wll := &kueue.Workload{}

			ginkgo.By("checking the job starts")
			gomega.Expect(k8sClient.Create(ctx, sampleJob)).Should(gomega.Succeed())

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
						Reason:  kueue.WorkloadEvictedByDeactivation,
						Message: "The workload is deactivated",
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadRequeued,
						Status:  metav1.ConditionFalse,
						Reason:  kueue.WorkloadEvictedByDeactivation,
						Message: "The workload is deactivated",
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
				))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("checking a second job starts after first job is suspended")
			sampleJob2 := testingjob.MakeJob("job2", ns.Name).Queue(localQueue.Name).Request(corev1.ResourceCPU, "2").Obj()

			lookupKey2 := types.NamespacedName{Name: sampleJob2.Name, Namespace: sampleJob2.Namespace}
			wll2 := &kueue.Workload{}

			gomega.Expect(k8sClient.Create(ctx, sampleJob2)).Should(gomega.Succeed())
			wlKey2 := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(sampleJob2.Name, sampleJob2.UID), Namespace: sampleJob2.Namespace}

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, lookupKey2, sampleJob2)).Should(gomega.Succeed())
				g.Expect(sampleJob2.Spec.Suspend).To(gomega.Equal(ptr.To(false)))
				g.Expect(k8sClient.Get(ctx, wlKey2, wll2)).Should(gomega.Succeed())
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wll2)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			// Checking job stays suspended
			ginkgo.By("checking job is suspended")
			gomega.Eventually(func() *bool {
				gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: sampleJob.Name, Namespace: sampleJob.Namespace}, createdJob)).
					Should(gomega.Succeed())
				return createdJob.Spec.Suspend
			}, util.Timeout, util.Interval).Should(gomega.Equal(ptr.To(true)))

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
				isEvicting := apimeta.IsStatusConditionTrue(wll.Status.Conditions, kueue.WorkloadEvicted)
				gomega.Expect(isEvicting).Should(gomega.BeTrue())
			}, util.ConsistentDuration, util.Interval).Should(gomega.Succeed())

			ginkgo.By("checking the first job becomes unsuspended after we update the Active field back to true")
			gomega.Eventually(func() error {
				gomega.Expect(k8sClient.Get(ctx, wlKey, wll)).Should(gomega.Succeed())
				wll.Spec.Active = ptr.To(true)
				return k8sClient.Update(ctx, wll)
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
})

var _ = ginkgo.Describe("Job controller interacting with Workload controller when waitForPodsReady with requeuing strategy is enabled", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		backoffBaseSeconds int32
		backoffLimitCount  *int32
		ns                 *corev1.Namespace
		fl                 *kueue.ResourceFlavor
		cq                 *kueue.ClusterQueue
		lq                 *kueue.LocalQueue
	)

	ginkgo.JustBeforeEach(func() {
		fwk = &framework.Framework{
			CRDPath: crdPath,
		}
		cfg = fwk.Init()
		waitForPodsReady := &configapi.WaitForPodsReady{
			Enable:         true,
			BlockAdmission: ptr.To(true),
			Timeout:        &metav1.Duration{Duration: 10 * time.Millisecond},
			RequeuingStrategy: &configapi.RequeuingStrategy{
				Timestamp:          ptr.To(configapi.EvictionTimestamp),
				BackoffBaseSeconds: ptr.To[int32](backoffBaseSeconds),
				BackoffLimitCount:  backoffLimitCount,
			},
		}
		ctx, k8sClient = fwk.RunManager(cfg, managerAndControllersSetup(
			false,
			&configapi.Configuration{WaitForPodsReady: waitForPodsReady},
			jobframework.WithWaitForPodsReady(waitForPodsReady),
		))

		ns = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "core-"}}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

		fl = testing.MakeResourceFlavor("fl").Obj()
		gomega.Expect(k8sClient.Create(ctx, fl)).Should(gomega.Succeed())

		cq = testing.MakeClusterQueue("cq").
			ResourceGroup(*testing.MakeFlavorQuotas("fl").Resource(corev1.ResourceCPU, "10").Obj()).Obj()
		gomega.Expect(k8sClient.Create(ctx, cq)).Should(gomega.Succeed())

		lq = testing.MakeLocalQueue("lq", ns.Name).ClusterQueue(cq.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, lq)).Should(gomega.Succeed())
	})

	ginkgo.JustAfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, cq, true)
		util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, fl, true)
		fwk.Teardown()
	})

	ginkgo.When("long backoffBaseSeconds", func() {
		ginkgo.BeforeEach(func() {
			backoffBaseSeconds = 10
		})

		ginkgo.It("should evict workload due pods ready timeout", func() {
			ginkgo.By("creating job")
			job := testingjob.MakeJob("job", ns.Name).Queue(lq.Name).Request(corev1.ResourceCPU, "2").Obj()
			gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())

			wl := &kueue.Workload{}
			wlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: job.Namespace}

			ginkgo.By("setting quota reservation")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
				g.Expect(util.SetQuotaReservation(ctx, k8sClient, wl, testing.MakeAdmission(cq.Name).Obj())).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("checking the workload is evicted")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
				g.Expect(wl.Status.Conditions).To(gomega.ContainElements(
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadPodsReady,
						Status:  metav1.ConditionFalse,
						Reason:  kueue.WorkloadPodsReady,
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

	ginkgo.When("short backoffBaseSeconds", func() {
		ginkgo.BeforeEach(func() {
			backoffBaseSeconds = 1
			backoffLimitCount = ptr.To[int32](1)
		})

		ginkgo.It("should re-queue a workload evicted due to PodsReady timeout after the backoff elapses", func() {
			ginkgo.By("creating job")
			job := testingjob.MakeJob("job", ns.Name).Queue(lq.Name).Request(corev1.ResourceCPU, "2").Obj()
			gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())

			wl := &kueue.Workload{}
			wlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: job.Namespace}

			ginkgo.By("admit the workload, it gets evicted due to PodsReadyTimeout and re-queued")
			var admission *kueue.Admission
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
				admission = testing.MakeAdmission(cq.Name).
					Assignment(corev1.ResourceCPU, "on-demand", "1m").
					AssignmentPodCount(wl.Spec.PodSets[0].Count).
					Obj()
				gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, wl, admission)).Should(gomega.Succeed())
				util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, wl)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

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
						Reason:  kueue.WorkloadPodsReady,
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
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
				g.Expect(util.SetQuotaReservation(ctx, k8sClient, wl, admission)).Should(gomega.Succeed())
				util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, wl)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

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
						Reason:  kueue.WorkloadPodsReady,
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
						Reason:  kueue.WorkloadEvictedByDeactivation,
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
						Reason:  kueue.WorkloadEvictedByDeactivation,
						Message: "The workload is deactivated due to exceeding the maximum number of re-queuing retries",
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
				))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
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
