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
	"k8s.io/client-go/kubernetes/scheme"
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
	jobName           = "test-job"
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

func ShouldReconcileJob(ctx context.Context, k8sClient client.Client, job, createdJob kubeflowjob.KubeflowJob, ns *corev1.Namespace, wlLookupKey types.NamespacedName, podSetsResources []PodSetsResource) {
	ginkgo.By("checking the job gets suspended when created unsuspended")
	priorityClass := testing.MakePriorityClass(priorityClassName).
		PriorityValue(int32(priorityValue)).Obj()
	gomega.Expect(k8sClient.Create(ctx, priorityClass)).Should(gomega.Succeed())

	if job.KFJobControl.RunPolicy().SchedulingPolicy == nil {
		job.KFJobControl.RunPolicy().SchedulingPolicy = &kftraining.SchedulingPolicy{}
	}
	job.KFJobControl.RunPolicy().SchedulingPolicy.PriorityClass = priorityClassName
	err := k8sClient.Create(ctx, job.Object())
	gomega.Expect(err).To(gomega.Succeed())

	gomega.Eventually(func() bool {
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: jobName, Namespace: ns.Name}, createdJob.Object()); err != nil {
			return false
		}
		return createdJob.IsSuspended()
	}, util.Timeout, util.Interval).Should(gomega.BeTrue())

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
			Name:      jobframework.GetWorkloadNameForOwnerWithGVK("second-workload", job.GVK()),
			Namespace: createdWorkload.Namespace,
		},
		Spec: *createdWorkload.Spec.DeepCopy(),
	}
	gomega.Expect(ctrl.SetControllerReference(createdJob.Object(), secondWl, scheme.Scheme)).Should(gomega.Succeed())
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
	admission := testing.MakeAdmission(clusterQueue.Name).PodSets(CreatePodSetAssigment(createdWorkload, podSetsResources)...).Obj()
	gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, admission)).Should(gomega.Succeed())
	util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
	lookupKey := types.NamespacedName{Name: jobName, Namespace: ns.Name}
	gomega.Eventually(func() bool {
		if err := k8sClient.Get(ctx, lookupKey, createdJob.Object()); err != nil {
			return false
		}
		return !createdJob.IsSuspended()
	}, util.Timeout, util.Interval).Should(gomega.BeTrue())
	gomega.Eventually(func() bool {
		ok, _ := testing.CheckLatestEvent(ctx, k8sClient, "Started", corev1.EventTypeNormal, fmt.Sprintf("Admitted by clusterQueue %v", clusterQueue.Name))
		return ok
	}, util.Timeout, util.Interval).Should(gomega.BeTrue())
	gomega.Expect(len(createdJob.KFJobControl.ReplicaSpecs()[podSetsResources[0].NodeName].Template.Spec.NodeSelector)).Should(gomega.Equal(1))
	gomega.Expect(createdJob.KFJobControl.ReplicaSpecs()[podSetsResources[0].NodeName].Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))
	for _, psr := range podSetsResources[1:] {
		gomega.Expect(len(createdJob.KFJobControl.ReplicaSpecs()[psr.NodeName].Template.Spec.NodeSelector)).Should(gomega.Equal(1))
		gomega.Expect(createdJob.KFJobControl.ReplicaSpecs()[psr.NodeName].Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(spotFlavor.Name))
	}
	gomega.Consistently(func() bool {
		if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
			return false
		}
		return len(createdWorkload.Status.Conditions) == 2
	}, util.ConsistentDuration, util.Interval).Should(gomega.BeTrue())

	ginkgo.By("checking the job gets suspended when parallelism changes and the added node selectors are removed")
	parallelism := ptr.Deref(job.KFJobControl.ReplicaSpecs()[ReplicaTypeWorker].Replicas, 1)
	newParallelism := parallelism + 1
	createdJob.KFJobControl.ReplicaSpecs()[ReplicaTypeWorker].Replicas = &newParallelism
	gomega.Expect(k8sClient.Update(ctx, createdJob.Object())).Should(gomega.Succeed())
	gomega.Eventually(func() bool {
		if err := k8sClient.Get(ctx, lookupKey, createdJob.Object()); err != nil {
			return false
		}
		return createdJob.IsSuspended() &&
			len(createdJob.KFJobControl.ReplicaSpecs()[ReplicaTypeWorker].Template.Spec.NodeSelector) == 0
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
		return workerPodSetsCount(createdWorkload, podSetsResources) == newParallelism
	}, util.Timeout, util.Interval).Should(gomega.BeTrue())
	gomega.Expect(createdWorkload.Status.Admission).Should(gomega.BeNil())

	ginkgo.By("checking the job is unsuspended and selectors added when workload is assigned again")
	admission = testing.MakeAdmission(clusterQueue.Name).PodSets(CreatePodSetAssigment(createdWorkload, podSetsResources)...).Obj()
	gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, admission)).Should(gomega.Succeed())
	util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
	gomega.Eventually(func() bool {
		if err := k8sClient.Get(ctx, lookupKey, createdJob.Object()); err != nil {
			return false
		}
		return !createdJob.IsSuspended()
	}, util.Timeout, util.Interval).Should(gomega.BeTrue())
	gomega.Expect(len(createdJob.KFJobControl.ReplicaSpecs()[podSetsResources[0].NodeName].Template.Spec.NodeSelector)).Should(gomega.Equal(1))
	gomega.Expect(createdJob.KFJobControl.ReplicaSpecs()[podSetsResources[0].NodeName].Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))
	for _, psr := range podSetsResources[1:] {
		gomega.Expect(len(createdJob.KFJobControl.ReplicaSpecs()[psr.NodeName].Template.Spec.NodeSelector)).Should(gomega.Equal(1))
		gomega.Expect(createdJob.KFJobControl.ReplicaSpecs()[psr.NodeName].Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(spotFlavor.Name))
	}
	gomega.Consistently(func() bool {
		if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
			return false
		}
		return len(createdWorkload.Status.Conditions) == 2
	}, util.ConsistentDuration, util.Interval).Should(gomega.BeTrue())

	ginkgo.By("checking the workload is finished when job is completed")
	createdJob.KFJobControl.JobStatus().Conditions = append(createdJob.KFJobControl.JobStatus().Conditions,
		kftraining.JobCondition{
			Type:               kftraining.JobSucceeded,
			Status:             corev1.ConditionTrue,
			LastTransitionTime: metav1.Now(),
		})
	gomega.Expect(k8sClient.Status().Update(ctx, createdJob.Object())).Should(gomega.Succeed())
	gomega.Eventually(func() bool {
		err := k8sClient.Get(ctx, wlLookupKey, createdWorkload)
		if err != nil || len(createdWorkload.Status.Conditions) == 2 {
			return false
		}

		return apimeta.IsStatusConditionTrue(createdWorkload.Status.Conditions, kueue.WorkloadFinished)
	}, util.Timeout, util.Interval).Should(gomega.BeTrue())
}

type PodSetsResource struct {
	NodeName    kftraining.ReplicaType
	ResourceCPU kueue.ResourceFlavorReference
}

func CreatePodSetAssigment(createdWorkload *kueue.Workload, podSetsResource []PodSetsResource) []kueue.PodSetAssignment {
	pda := []kueue.PodSetAssignment{}
	for i, psr := range podSetsResource {
		pda = append(pda, kueue.PodSetAssignment{
			Name: string(psr.NodeName),
			Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
				corev1.ResourceCPU: psr.ResourceCPU,
			},
			Count: ptr.To(createdWorkload.Spec.PodSets[i].Count),
		})
	}
	return pda
}

func workerPodSetsCount(wl *kueue.Workload, podSetsResources []PodSetsResource) int32 {
	idx := -1
	for i, psr := range podSetsResources {
		if psr.NodeName == ReplicaTypeWorker {
			idx = i
		}
	}
	return wl.Spec.PodSets[idx].Count
}
