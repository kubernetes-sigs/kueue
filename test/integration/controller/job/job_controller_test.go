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

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"

	kueue "sigs.k8s.io/kueue/api/v1alpha1"
	"sigs.k8s.io/kueue/pkg/constants"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/workload/job"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/integration/framework"
)

const (
	parallelism       = 4
	jobName           = "test-job"
	jobNamespace      = "default"
	jobKey            = jobNamespace + "/" + jobName
	labelKey          = "cloud.provider.com/instance"
	priorityClassName = "test-priority-class"
	priorityValue     = 10
)

// +kubebuilder:docs-gen:collapse=Imports

var _ = ginkgo.Describe("Job controller", func() {
	ginkgo.It("Should reconcile workload and job", func() {
		ginkgo.By("checking the job gets suspended when created unsuspended")
		priorityClass := testing.MakePriorityClass(priorityClassName).
			PriorityValue(int32(priorityValue)).Obj()
		gomega.Expect(k8sClient.Create(ctx, priorityClass)).Should(gomega.Succeed())
		job := testing.MakeJob(jobName, jobNamespace).PriorityClass(priorityClassName).Obj()
		gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
		lookupKey := types.NamespacedName{Name: jobName, Namespace: jobNamespace}
		createdJob := &batchv1.Job{}
		gomega.Eventually(func() bool {
			if err := k8sClient.Get(ctx, lookupKey, createdJob); err != nil {
				return false
			}
			return createdJob.Spec.Suspend != nil && *createdJob.Spec.Suspend
		}, framework.Timeout, framework.Interval).Should(gomega.BeTrue())

		ginkgo.By("checking the workload is created without queue assigned")
		createdWorkload := &kueue.QueuedWorkload{}
		gomega.Eventually(func() bool {
			err := k8sClient.Get(ctx, lookupKey, createdWorkload)
			return err == nil
		}, framework.Timeout, framework.Interval).Should(gomega.BeTrue())
		gomega.Expect(createdWorkload.Spec.QueueName).Should(gomega.Equal(""), "The QueuedWorkload shouldn't have .spec.queueName set")
		gomega.Expect(metav1.IsControlledBy(createdWorkload, job)).To(gomega.BeTrue(), "The QueuedWorkload should be owned by the Job")

		ginkgo.By("checking the workload is created with priority and priorityName")
		gomega.Expect(createdWorkload.Spec.PriorityClassName).Should(gomega.Equal(priorityClassName))
		gomega.Expect(*createdWorkload.Spec.Priority).Should(gomega.Equal(int32(priorityValue)))

		ginkgo.By("checking the workload is updated with queue name when the job does")
		jobQueueName := "test-queue"
		createdJob.Annotations = map[string]string{constants.QueueAnnotation: jobQueueName}
		gomega.Expect(k8sClient.Update(ctx, createdJob)).Should(gomega.Succeed())
		gomega.Eventually(func() bool {
			if err := k8sClient.Get(ctx, lookupKey, createdWorkload); err != nil {
				return false
			}
			return createdWorkload.Spec.QueueName == jobQueueName
		}, framework.Timeout, framework.Interval).Should(gomega.BeTrue())

		ginkgo.By("checking a second non-matching workload is deleted")
		secondWl, _ := workloadjob.ConstructWorkloadFor(ctx, k8sClient, createdJob, scheme.Scheme)
		secondWl.Name = "second-workload"
		secondWl.Spec.PodSets[0].Count = parallelism + 1
		gomega.Expect(k8sClient.Create(ctx, secondWl)).Should(gomega.Succeed())
		gomega.Eventually(func() bool {
			wl := &kueue.QueuedWorkload{}
			key := types.NamespacedName{Name: secondWl.Name, Namespace: secondWl.Namespace}
			if err := k8sClient.Get(ctx, key, wl); err != nil && apierrors.IsNotFound(err) {
				return true
			}
			return false
		}, framework.Timeout, framework.Interval).Should(gomega.BeTrue())
		// check the original wl is still there
		gomega.Consistently(func() bool {
			err := k8sClient.Get(ctx, lookupKey, createdWorkload)
			return err == nil
		}, framework.ConsistentDuration, framework.Interval).Should(gomega.BeTrue())
		gomega.Eventually(func() bool {
			ok, _ := testing.CheckLatestEvent(ctx, k8sClient, "DeletedQueuedWorkload", corev1.EventTypeNormal, fmt.Sprintf("Deleted not matching QueuedWorkload: %v", workload.Key(secondWl)))
			return ok
		}, framework.Timeout, framework.Interval).Should(gomega.BeTrue())

		ginkgo.By("checking the job is unsuspended when workload is assigned")
		onDemandFlavor := testing.MakeResourceFlavor("on-demand").Label(labelKey, "on-demand").Obj()
		gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).Should(gomega.Succeed())
		spotFlavor := testing.MakeResourceFlavor("spot").Label(labelKey, "spot").Obj()
		gomega.Expect(k8sClient.Create(ctx, spotFlavor)).Should(gomega.Succeed())
		clusterQueue := testing.MakeClusterQueue("cluster-queue").
			Resource(testing.MakeResource(corev1.ResourceCPU).
				Flavor(testing.MakeFlavor(onDemandFlavor.Name, "5").Obj()).
				Flavor(testing.MakeFlavor(spotFlavor.Name, "5").Obj()).
				Obj()).Obj()
		gomega.Expect(k8sClient.Create(ctx, clusterQueue)).Should(gomega.Succeed())
		createdWorkload.Spec.Admission = &kueue.Admission{
			ClusterQueue: kueue.ClusterQueueReference(clusterQueue.Name),
			PodSetFlavors: []kueue.PodSetFlavors{{
				Flavors: map[corev1.ResourceName]string{
					corev1.ResourceCPU: onDemandFlavor.Name,
				},
			}},
		}
		gomega.Expect(k8sClient.Update(ctx, createdWorkload)).Should(gomega.Succeed())
		gomega.Eventually(func() bool {
			if err := k8sClient.Get(ctx, lookupKey, createdJob); err != nil {
				return false
			}
			return !*createdJob.Spec.Suspend
		}, framework.Timeout, framework.Interval).Should(gomega.BeTrue())
		gomega.Eventually(func() bool {
			ok, _ := testing.CheckLatestEvent(ctx, k8sClient, "Started", corev1.EventTypeNormal, fmt.Sprintf("Admitted by clusterQueue %v", clusterQueue.Name))
			return ok
		}, framework.Timeout, framework.Interval).Should(gomega.BeTrue())
		gomega.Expect(len(createdJob.Spec.Template.Spec.NodeSelector)).Should(gomega.Equal(1))
		gomega.Expect(createdJob.Spec.Template.Spec.NodeSelector[labelKey]).Should(gomega.Equal(onDemandFlavor.Name))
		gomega.Consistently(func() bool {
			if err := k8sClient.Get(ctx, lookupKey, createdWorkload); err != nil {
				return false
			}
			return len(createdWorkload.Status.Conditions) == 0
		}, framework.ConsistentDuration, framework.Interval).Should(gomega.BeTrue())

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
		}, framework.Timeout, framework.Interval).Should(gomega.BeTrue())
		gomega.Eventually(func() bool {
			ok, _ := testing.CheckLatestEvent(ctx, k8sClient, "DeletedQueuedWorkload", corev1.EventTypeNormal, fmt.Sprintf("Deleted not matching QueuedWorkload: %v", jobKey))
			return ok
		}, framework.Timeout, framework.Interval).Should(gomega.BeTrue())

		ginkgo.By("checking the workload is updated with new count")
		gomega.Eventually(func() bool {
			if err := k8sClient.Get(ctx, lookupKey, createdWorkload); err != nil {
				return false
			}
			return createdWorkload.Spec.PodSets[0].Count == newParallelism
		}, framework.Timeout, framework.Interval).Should(gomega.BeTrue())
		gomega.Expect(createdWorkload.Spec.Admission).Should(gomega.BeNil())

		ginkgo.By("checking the job is unsuspended and selectors added when workload is assigned again")
		createdWorkload.Spec.Admission = &kueue.Admission{
			ClusterQueue: kueue.ClusterQueueReference(clusterQueue.Name),
			PodSetFlavors: []kueue.PodSetFlavors{{
				Flavors: map[corev1.ResourceName]string{
					corev1.ResourceCPU: spotFlavor.Name,
				},
			}},
		}
		gomega.Expect(k8sClient.Update(ctx, createdWorkload)).Should(gomega.Succeed())
		gomega.Eventually(func() bool {
			if err := k8sClient.Get(ctx, lookupKey, createdJob); err != nil {
				return false
			}
			return !*createdJob.Spec.Suspend
		}, framework.Timeout, framework.Interval).Should(gomega.BeTrue())
		gomega.Expect(len(createdJob.Spec.Template.Spec.NodeSelector)).Should(gomega.Equal(1))
		gomega.Expect(createdJob.Spec.Template.Spec.NodeSelector[labelKey]).Should(gomega.Equal(spotFlavor.Name))
		gomega.Consistently(func() bool {
			if err := k8sClient.Get(ctx, lookupKey, createdWorkload); err != nil {
				return false
			}
			return len(createdWorkload.Status.Conditions) == 0
		}, framework.ConsistentDuration, framework.Interval).Should(gomega.BeTrue())

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
			err := k8sClient.Get(ctx, lookupKey, createdWorkload)
			if err != nil || len(createdWorkload.Status.Conditions) == 0 {
				return false
			}

			return createdWorkload.Status.Conditions[0].Type == kueue.QueuedWorkloadFinished &&
				createdWorkload.Status.Conditions[0].Status == corev1.ConditionTrue
		}, framework.Timeout, framework.Interval).Should(gomega.BeTrue())
	})
})
