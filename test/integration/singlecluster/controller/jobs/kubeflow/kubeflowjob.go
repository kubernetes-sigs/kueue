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

package kubeflow

import (
	"context"
	"fmt"

	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/kubeflowjob"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/util"
)

const (
	instanceKey       = "cloud.provider.com/instance"
	priorityClassName = "test-priority-class"
	priorityValue     = 10
	jobQueueName      = "test-queue"
)

type PodsReadyTestSpec struct {
	BeforeJobStatus *kftraining.JobStatus
	BeforeCondition *metav1.Condition
	JobStatus       kftraining.JobStatus
	Suspended       bool
	WantCondition   *metav1.Condition
}

var ReplicaTypeWorker = kftraining.ReplicaType("Worker")

func ShouldReconcileJob(ctx context.Context, k8sClient client.Client, job, createdJob kubeflowjob.KubeflowJob, podSetsResources []PodSetsResource) {
	ginkgo.By("checking the job gets suspended when created unsuspended")
	priorityClass := testing.MakePriorityClass(priorityClassName).
		PriorityValue(int32(priorityValue)).Obj()
	util.MustCreate(ctx, k8sClient, priorityClass)

	if job.KFJobControl.RunPolicy().SchedulingPolicy == nil {
		job.KFJobControl.RunPolicy().SchedulingPolicy = &kftraining.SchedulingPolicy{}
	}
	job.KFJobControl.RunPolicy().SchedulingPolicy.PriorityClass = priorityClassName
	util.MustCreate(ctx, k8sClient, job.Object())

	lookupKey := client.ObjectKeyFromObject(job.Object())

	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, lookupKey, createdJob.Object())).Should(gomega.Succeed())
		g.Expect(createdJob.IsSuspended()).Should(gomega.BeTrue())
	}, util.Timeout, util.Interval).Should(gomega.Succeed())

	wlLookupKey := types.NamespacedName{
		Name:      jobframework.GetWorkloadNameForOwnerWithGVK(job.Object().GetName(), job.Object().GetUID(), job.GVK()),
		Namespace: job.Object().GetNamespace(),
	}

	ginkgo.By("checking the workload is created without queue assigned")
	createdWorkload := util.AwaitAndVerifyCreatedWorkload(ctx, k8sClient, wlLookupKey, createdJob.Object())
	util.VerifyWorkloadPriority(createdWorkload, priorityClassName, priorityValue)
	gomega.Expect(createdWorkload.Spec.QueueName).Should(gomega.Equal(""), "The Workload shouldn't have .spec.queueName set")

	ginkgo.By("checking the workload is created with priority and priorityName")
	gomega.Expect(createdWorkload.Spec.PriorityClassName).Should(gomega.Equal(priorityClassName))
	gomega.Expect(*createdWorkload.Spec.Priority).Should(gomega.Equal(int32(priorityValue)))

	ginkgo.By("checking the workload is updated with queue name when the job does")
	createdJob.Object().SetAnnotations(map[string]string{constants.QueueAnnotation: jobQueueName})
	gomega.Expect(k8sClient.Update(ctx, createdJob.Object())).Should(gomega.Succeed())
	util.AwaitAndVerifyWorkloadQueueName(ctx, k8sClient, createdWorkload, wlLookupKey, jobQueueName)

	ginkgo.By("checking a second non-matching workload is deleted")
	secondWl := &kueue.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobframework.GetWorkloadNameForOwnerWithGVK("second-workload", "test-uid", job.GVK()),
			Namespace: createdWorkload.Namespace,
		},
		Spec: *createdWorkload.Spec.DeepCopy(),
	}
	gomega.Expect(ctrl.SetControllerReference(createdJob.Object(), secondWl, k8sClient.Scheme())).Should(gomega.Succeed())
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
		util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, spotFlavor, true)
	}()
	clusterQueue := testing.MakeClusterQueue("cluster-queue").
		ResourceGroup(
			*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj(),
			*testing.MakeFlavorQuotas("spot").Resource(corev1.ResourceCPU, "5").Obj(),
		).Obj()
	admission := testing.MakeAdmission(clusterQueue.Name).PodSets(CreatePodSetAssignment(createdWorkload, podSetsResources)...).Obj()
	gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, admission)).Should(gomega.Succeed())
	util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, lookupKey, createdJob.Object())).To(gomega.Succeed())
		g.Expect(createdJob.IsSuspended()).Should(gomega.BeFalse())
	}, util.Timeout, util.Interval).Should(gomega.Succeed())
	gomega.Eventually(func(g gomega.Gomega) {
		ok, _ := testing.CheckEventRecordedFor(ctx, k8sClient, "Started", corev1.EventTypeNormal, fmt.Sprintf("Admitted by clusterQueue %v", clusterQueue.Name), lookupKey)
		g.Expect(ok).Should(gomega.BeTrue())
	}, util.Timeout, util.Interval).Should(gomega.Succeed())
	for _, psr := range podSetsResources {
		gomega.Expect(createdJob.KFJobControl.ReplicaSpecs()[psr.RoleName].Template.Spec.NodeSelector).
			To(gomega.BeComparableTo(map[string]string{instanceKey: string(psr.ResourceCPU)}))
	}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
		g.Expect(createdWorkload.Status.Conditions).Should(gomega.HaveLen(2))
	}, util.Timeout, util.Interval).Should(gomega.Succeed())

	ginkgo.By("checking the job gets suspended when parallelism changes and the added node selectors are removed")
	parallelism := ptr.Deref(job.KFJobControl.ReplicaSpecs()[ReplicaTypeWorker].Replicas, 1)
	newParallelism := parallelism + 1
	createdJob.KFJobControl.ReplicaSpecs()[ReplicaTypeWorker].Replicas = &newParallelism
	gomega.Expect(k8sClient.Update(ctx, createdJob.Object())).Should(gomega.Succeed())
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, lookupKey, createdJob.Object())).Should(gomega.Succeed())
		g.Expect(createdJob.IsSuspended()).Should(gomega.BeTrue())
		g.Expect(createdJob.KFJobControl.ReplicaSpecs()[ReplicaTypeWorker].Template.Spec.NodeSelector).Should(gomega.BeEmpty())
	}, util.Timeout, util.Interval).Should(gomega.Succeed())
	gomega.Eventually(func(g gomega.Gomega) {
		ok, _ := testing.CheckEventRecordedFor(ctx, k8sClient, "DeletedWorkload", corev1.EventTypeNormal, fmt.Sprintf("Deleted not matching Workload: %v", wlLookupKey.String()), lookupKey)
		g.Expect(ok).Should(gomega.BeTrue())
	}, util.Timeout, util.Interval).Should(gomega.Succeed())

	ginkgo.By("checking the workload is updated with new count")
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
		g.Expect(workerPodSetsCount(createdWorkload, podSetsResources)).Should(gomega.Equal(newParallelism))
	}, util.Timeout, util.Interval).Should(gomega.Succeed())
	gomega.Expect(createdWorkload.Status.Admission).Should(gomega.BeNil())

	ginkgo.By("checking the job is unsuspended and selectors added when workload is assigned again")
	admission = testing.MakeAdmission(clusterQueue.Name).PodSets(CreatePodSetAssignment(createdWorkload, podSetsResources)...).Obj()
	gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, admission)).Should(gomega.Succeed())
	util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, lookupKey, createdJob.Object())).Should(gomega.Succeed())
		g.Expect(createdJob.IsSuspended()).Should(gomega.BeFalse())
	}, util.Timeout, util.Interval).Should(gomega.Succeed())
	for _, psr := range podSetsResources {
		gomega.Expect(createdJob.KFJobControl.ReplicaSpecs()[psr.RoleName].Template.Spec.NodeSelector).
			To(gomega.BeComparableTo(map[string]string{instanceKey: string(psr.ResourceCPU)}))
	}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
		g.Expect(createdWorkload.Status.Conditions).Should(gomega.HaveLen(2))
	}, util.Timeout, util.Interval).Should(gomega.Succeed())

	ginkgo.By("checking the workload is finished when job is completed")
	createdJob.KFJobControl.JobStatus().Conditions = append(createdJob.KFJobControl.JobStatus().Conditions,
		kftraining.JobCondition{
			Type:               kftraining.JobSucceeded,
			Status:             corev1.ConditionTrue,
			LastTransitionTime: metav1.Now(),
		})
	gomega.Expect(k8sClient.Status().Update(ctx, createdJob.Object())).Should(gomega.Succeed())
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
		g.Expect(createdWorkload.Status.Conditions).ShouldNot(gomega.HaveLen(2))
		g.Expect(createdWorkload.Status.Conditions).Should(testing.HaveConditionStatusTrue(kueue.WorkloadFinished))
	}, util.Timeout, util.Interval).Should(gomega.Succeed())
}

func ShouldNotReconcileUnmanagedJob(ctx context.Context, k8sClient client.Client, job, createdJob kubeflowjob.KubeflowJob) {
	ginkgo.By("checking the job gets suspended when created unsuspended")
	util.MustCreate(ctx, k8sClient, job.Object())

	lookupKey := client.ObjectKeyFromObject(job.Object())
	wlLookupKey := types.NamespacedName{
		Name:      jobframework.GetWorkloadNameForOwnerWithGVK(job.Object().GetName(), job.Object().GetUID(), job.GVK()),
		Namespace: job.Object().GetNamespace(),
	}
	workload := &kueue.Workload{}
	gomega.Consistently(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, lookupKey, createdJob.Object())).Should(gomega.Succeed())
		g.Expect(createdJob.IsSuspended()).Should(gomega.BeFalse())
		g.Expect(k8sClient.Get(ctx, wlLookupKey, workload)).Should(testing.BeNotFoundError())
	}, util.Timeout, util.Interval).Should(gomega.Succeed())
}

func JobControllerWhenWaitForPodsReadyEnabled(ctx context.Context, k8sClient client.Client, job, createdJob kubeflowjob.KubeflowJob, podsReadyTestSpec PodsReadyTestSpec, podSetsResources []PodSetsResource) {
	ginkgo.By("Create a job")
	job.Object().SetAnnotations(map[string]string{constants.QueueAnnotation: jobQueueName})
	util.MustCreate(ctx, k8sClient, job.Object())
	lookupKey := client.ObjectKeyFromObject(job.Object())
	gomega.ExpectWithOffset(1, k8sClient.Get(ctx, lookupKey, createdJob.Object())).Should(gomega.Succeed())

	wlLookupKey := types.NamespacedName{
		Name:      jobframework.GetWorkloadNameForOwnerWithGVK(job.Object().GetName(), job.Object().GetUID(), job.GVK()),
		Namespace: job.Object().GetNamespace(),
	}

	ginkgo.By("Fetch the workload created for the job")
	createdWorkload := &kueue.Workload{}
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
	}, util.Timeout, util.Interval).Should(gomega.Succeed())

	ginkgo.By("Admit the workload created for the job")
	admission := testing.MakeAdmission("foo").PodSets(CreatePodSetAssignment(createdWorkload, podSetsResources)...).Obj()
	gomega.ExpectWithOffset(1, util.SetQuotaReservation(ctx, k8sClient, createdWorkload, admission)).Should(gomega.Succeed())
	util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
	gomega.ExpectWithOffset(1, k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())

	ginkgo.By("Await for the job to be unsuspended")
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, lookupKey, createdJob.Object())).Should(gomega.Succeed())
		g.Expect(createdJob.IsSuspended()).Should(gomega.BeFalse())
	}, util.Timeout, util.Interval).Should(gomega.Succeed())

	if podsReadyTestSpec.BeforeJobStatus != nil {
		ginkgo.By("Update the job status to simulate its initial progress towards completion")
		createdJob.KFJobControl.JobStatus().Conditions = append(createdJob.KFJobControl.JobStatus().Conditions, podsReadyTestSpec.BeforeJobStatus.Conditions...)
		gomega.ExpectWithOffset(1, k8sClient.Status().Update(ctx, createdJob.Object())).Should(gomega.Succeed())
		gomega.ExpectWithOffset(1, k8sClient.Get(ctx, lookupKey, createdJob.Object())).Should(gomega.Succeed())
	}

	if podsReadyTestSpec.BeforeCondition != nil {
		ginkgo.By("Update the workload status")
		gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
			g.Expect(apimeta.FindStatusCondition(createdWorkload.Status.Conditions, kueue.WorkloadPodsReady)).Should(
				gomega.BeComparableTo(
					podsReadyTestSpec.BeforeCondition,
					util.IgnoreConditionTimestampsAndObservedGeneration,
				),
			)
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	}

	ginkgo.By("Update the job status to simulate its progress towards completion")
	createdJob.KFJobControl.JobStatus().Conditions = append(createdJob.KFJobControl.JobStatus().Conditions, podsReadyTestSpec.JobStatus.Conditions...)
	gomega.ExpectWithOffset(1, k8sClient.Status().Update(ctx, createdJob.Object())).Should(gomega.Succeed())
	gomega.ExpectWithOffset(1, k8sClient.Get(ctx, lookupKey, createdJob.Object())).Should(gomega.Succeed())

	if podsReadyTestSpec.Suspended {
		ginkgo.By("Unset admission of the workload to suspend the job")
		gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
			// the update may need to be retried due to a conflict as the workload gets
			// also updated due to setting of the job status.
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
			g.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, nil)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
	}

	ginkgo.By("Verify the PodsReady condition is added")
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
		g.Expect(apimeta.FindStatusCondition(createdWorkload.Status.Conditions, kueue.WorkloadPodsReady)).Should(
			gomega.BeComparableTo(
				podsReadyTestSpec.WantCondition,
				util.IgnoreConditionTimestampsAndObservedGeneration,
			),
		)
	}, util.Timeout, util.Interval).Should(gomega.Succeed())
}

func ShouldScheduleJobsAsTheyFitInTheirClusterQueue(ctx context.Context, k8sClient client.Client, job, createdJob kubeflowjob.KubeflowJob, clusterQueue *kueue.ClusterQueue, podSetsResources []PodSetsResource) {
	ginkgo.By("checking a job starts")
	util.MustCreate(ctx, k8sClient, job.Object())
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job.Object()), createdJob.Object())).Should(gomega.Succeed())
		g.Expect(createdJob.IsSuspended()).Should(gomega.BeFalse())
	}, util.Timeout, util.Interval).Should(gomega.Succeed())
	for _, psr := range podSetsResources {
		gomega.ExpectWithOffset(1, createdJob.KFJobControl.ReplicaSpecs()[psr.RoleName].Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(string(psr.ResourceCPU)))
	}
	util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
	util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
}

type PodSetsResource struct {
	RoleName    kftraining.ReplicaType
	ResourceCPU kueue.ResourceFlavorReference
}

func CreatePodSetAssignment(createdWorkload *kueue.Workload, podSetsResource []PodSetsResource) []kueue.PodSetAssignment {
	pda := make([]kueue.PodSetAssignment, len(podSetsResource))
	for i, psr := range podSetsResource {
		pda[i] = kueue.PodSetAssignment{
			Name: kueue.NewPodSetReference(string(psr.RoleName)),
			Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
				corev1.ResourceCPU: psr.ResourceCPU,
			},
			Count: ptr.To(createdWorkload.Spec.PodSets[i].Count),
		}
	}
	return pda
}

func workerPodSetsCount(wl *kueue.Workload, podSetsResources []PodSetsResource) int32 {
	idx := -1
	for i, psr := range podSetsResources {
		if psr.RoleName == ReplicaTypeWorker {
			idx = i
		}
	}
	return wl.Spec.PodSets[idx].Count
}
