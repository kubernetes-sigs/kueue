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
	"context"
	"fmt"
	"path/filepath"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/workload/job"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/workload/job"
	"sigs.k8s.io/kueue/pkg/util/pointer"
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

var (
	cfg       *rest.Config
	k8sClient client.Client
	ctx       context.Context
	fwk       *framework.Framework
	crdPath   = filepath.Join("..", "..", "..", "..", "config", "crd", "bases")
)

// +kubebuilder:docs-gen:collapse=Imports

var _ = ginkgo.Describe("Job controller", func() {
	ginkgo.BeforeEach(func() {
		fwk = &framework.Framework{
			ManagerSetup: managerSetup(job.WithManageJobsWithoutQueueName(true)),
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
		createdWorkload := &kueue.Workload{}
		gomega.Eventually(func() bool {
			err := k8sClient.Get(ctx, lookupKey, createdWorkload)
			return err == nil
		}, framework.Timeout, framework.Interval).Should(gomega.BeTrue())
		gomega.Expect(createdWorkload.Spec.QueueName).Should(gomega.Equal(""), "The Workload shouldn't have .spec.queueName set")
		gomega.Expect(metav1.IsControlledBy(createdWorkload, job)).To(gomega.BeTrue(), "The Workload should be owned by the Job")

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
		gomega.Eventually(func() error {
			wl := &kueue.Workload{}
			key := types.NamespacedName{Name: secondWl.Name, Namespace: secondWl.Namespace}
			return k8sClient.Get(ctx, key, wl)
		}, framework.Timeout, framework.Interval).Should(testing.BeNotFoundError())
		// check the original wl is still there
		gomega.Consistently(func() bool {
			err := k8sClient.Get(ctx, lookupKey, createdWorkload)
			return err == nil
		}, framework.ConsistentDuration, framework.Interval).Should(gomega.BeTrue())
		gomega.Eventually(func() bool {
			ok, _ := testing.CheckLatestEvent(ctx, k8sClient, "DeletedWorkload", corev1.EventTypeNormal, fmt.Sprintf("Deleted not matching Workload: %v", workload.Key(secondWl)))
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
			ok, _ := testing.CheckLatestEvent(ctx, k8sClient, "DeletedWorkload", corev1.EventTypeNormal, fmt.Sprintf("Deleted not matching Workload: %v", jobKey))
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

			return createdWorkload.Status.Conditions[0].Type == kueue.WorkloadFinished &&
				createdWorkload.Status.Conditions[0].Status == metav1.ConditionTrue
		}, framework.Timeout, framework.Interval).Should(gomega.BeTrue())
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
		job := testing.MakeJob(jobName, jobNamespace).Obj()
		gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
		lookupKey := types.NamespacedName{Name: jobName, Namespace: jobNamespace}
		createdJob := &batchv1.Job{}
		gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())

		createdWorkload := &kueue.Workload{}
		gomega.Consistently(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, lookupKey, createdWorkload))
		}, framework.ConsistentDuration, framework.Interval).Should(gomega.BeTrue())

		ginkgo.By("checking the workload is created when queue name is set")
		jobQueueName := "test-queue"
		createdJob.Annotations = map[string]string{constants.QueueAnnotation: jobQueueName}
		gomega.Expect(k8sClient.Update(ctx, createdJob)).Should(gomega.Succeed())
		gomega.Eventually(func() error {
			return k8sClient.Get(ctx, lookupKey, createdWorkload)
		}, framework.Timeout, framework.Interval).Should(gomega.Succeed())
	})
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
		prodQueue           *kueue.Queue
		devQueue            *kueue.Queue
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
			Resource(testing.MakeResource(corev1.ResourceCPU).
				Flavor(testing.MakeFlavor(spotTaintedFlavor.Name, "5").Max("5").Obj()).
				Flavor(testing.MakeFlavor(onDemandFlavor.Name, "5").Obj()).
				Obj()).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, prodClusterQ)).Should(gomega.Succeed())

		devClusterQ = testing.MakeClusterQueue("dev-clusterqueue").
			Resource(testing.MakeResource(corev1.ResourceCPU).
				Flavor(testing.MakeFlavor(spotUntaintedFlavor.Name, "5").Obj()).
				Flavor(testing.MakeFlavor(onDemandFlavor.Name, "5").Obj()).
				Obj()).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, devClusterQ)).Should(gomega.Succeed())

		prodQueue = testing.MakeQueue("prod-queue", ns.Name).ClusterQueue(prodClusterQ.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, prodQueue)).Should(gomega.Succeed())

		devQueue = testing.MakeQueue("dev-queue", ns.Name).ClusterQueue(devClusterQ.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, devQueue)).Should(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(framework.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		framework.ExpectClusterQueueToBeDeleted(ctx, k8sClient, prodClusterQ, true)
		framework.ExpectClusterQueueToBeDeleted(ctx, k8sClient, devClusterQ, true)
		framework.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		gomega.Expect(framework.DeleteResourceFlavor(ctx, k8sClient, spotTaintedFlavor)).To(gomega.Succeed())
		gomega.Expect(framework.DeleteResourceFlavor(ctx, k8sClient, spotUntaintedFlavor)).To(gomega.Succeed())

		fwk.Teardown()
	})

	ginkgo.It("Should schedule jobs as they fit in their ClusterQueue", func() {
		ginkgo.By("checking the first prod job starts")
		prodJob1 := testing.MakeJob("prod-job1", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "2").Obj()
		gomega.Expect(k8sClient.Create(ctx, prodJob1)).Should(gomega.Succeed())
		lookupKey1 := types.NamespacedName{Name: prodJob1.Name, Namespace: prodJob1.Namespace}
		createdProdJob1 := &batchv1.Job{}
		gomega.Eventually(func() *bool {
			gomega.Expect(k8sClient.Get(ctx, lookupKey1, createdProdJob1)).Should(gomega.Succeed())
			return createdProdJob1.Spec.Suspend
		}, framework.Timeout, framework.Interval).Should(gomega.Equal(pointer.Bool(false)))
		gomega.Expect(createdProdJob1.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))
		framework.ExpectPendingWorkloadsMetric(prodClusterQ, 0)
		framework.ExpectAdmittedActiveWorkloadsMetric(prodClusterQ, 1)

		ginkgo.By("checking a second no-fit prod job does not start")
		prodJob2 := testing.MakeJob("prod-job2", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "5").Obj()
		gomega.Expect(k8sClient.Create(ctx, prodJob2)).Should(gomega.Succeed())
		lookupKey2 := types.NamespacedName{Name: prodJob2.Name, Namespace: prodJob2.Namespace}
		createdProdJob2 := &batchv1.Job{}
		gomega.Consistently(func() *bool {
			gomega.Expect(k8sClient.Get(ctx, lookupKey2, createdProdJob2)).Should(gomega.Succeed())
			return createdProdJob2.Spec.Suspend
		}, framework.ConsistentDuration, framework.Interval).Should(gomega.Equal(pointer.Bool(true)))
		framework.ExpectPendingWorkloadsMetric(prodClusterQ, 1)
		framework.ExpectAdmittedActiveWorkloadsMetric(prodClusterQ, 1)

		ginkgo.By("checking a dev job starts")
		devJob := testing.MakeJob("dev-job", ns.Name).Queue(devQueue.Name).Request(corev1.ResourceCPU, "5").Obj()
		gomega.Expect(k8sClient.Create(ctx, devJob)).Should(gomega.Succeed())
		createdDevJob := &batchv1.Job{}
		gomega.Eventually(func() *bool {
			gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: devJob.Name, Namespace: devJob.Namespace}, createdDevJob)).
				Should(gomega.Succeed())
			return createdDevJob.Spec.Suspend
		}, framework.Timeout, framework.Interval).Should(gomega.Equal(pointer.Bool(false)))
		gomega.Expect(createdDevJob.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(spotUntaintedFlavor.Name))
		framework.ExpectPendingWorkloadsMetric(devClusterQ, 0)
		framework.ExpectAdmittedActiveWorkloadsMetric(devClusterQ, 1)

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
		}, framework.Timeout, framework.Interval).Should(gomega.Equal(pointer.Bool(false)))
		gomega.Expect(createdProdJob2.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))
		framework.ExpectPendingWorkloadsMetric(prodClusterQ, 0)
		framework.ExpectAdmittedActiveWorkloadsMetric(prodClusterQ, 1)
	})
})
