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

package rayjob

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	workloadrayjob "sigs.k8s.io/kueue/pkg/controller/jobs/rayjob"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
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

var _ = ginkgo.Describe("Job controller", ginkgo.Label("job:ray", "area:jobs"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
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
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-")
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.It("Should not reconcile RayJobs since Kueue will manage RayCluster under RayJob instead of managing RayJob directly", func() {
		ginkgo.By("checking the job gets unsuspended")
		priorityClass := utiltesting.MakePriorityClass(priorityClassName).
			PriorityValue(priorityValue).Obj()
		util.MustCreate(ctx, k8sClient, priorityClass)
		defer func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, priorityClass, true)
		}()

		job := testingrayjob.MakeJob(jobName, ns.Name).
			Suspend(false).
			WithPriorityClassName(priorityClassName).
			Obj()
		util.MustCreate(ctx, k8sClient, job)
		createdJob := &rayv1.RayJob{}

		setInitStatus(jobName, ns.Name)
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: jobName, Namespace: ns.Name}, createdJob)).Should(gomega.Succeed())
			g.Expect(createdJob.Spec.Suspend).Should(gomega.BeFalse())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("checking no workload created for the job")
		wlLookupKey := types.NamespacedName{Name: workloadrayjob.GetWorkloadNameForRayJob(job.Name, job.UID), Namespace: ns.Name}
		createdWorkload := &kueue.Workload{}
		gomega.Consistently(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: job.Name, Namespace: job.Namespace}, createdJob)).Should(gomega.Succeed())
			g.Expect(createdJob.Spec.Suspend).Should(gomega.BeFalse())
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(utiltesting.BeNotFoundError())
		}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
	})

	ginkgo.It("A RayJob created in an unmanaged namespace is not suspended and a workload is not created", func() {
		ginkgo.By("Creating an unsuspended job without a queue-name in unmanaged-ns")
		job := testingrayjob.MakeJob(jobName, "unmanaged-ns").
			Suspend(false).
			Obj()
		util.MustCreate(ctx, k8sClient, job)
		createdJob := &rayv1.RayJob{}
		wlLookupKey := types.NamespacedName{Name: workloadrayjob.GetWorkloadNameForRayJob(job.Name, job.UID), Namespace: ns.Name}
		createdWorkload := &kueue.Workload{}

		gomega.Consistently(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: job.Name, Namespace: job.Namespace}, createdJob)).Should(gomega.Succeed())
			g.Expect(createdJob.Spec.Suspend).Should(gomega.BeFalse())
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(utiltesting.BeNotFoundError())
		}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
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
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-")
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.It("Should not reconcile jobs even when queue is set since Kueue will manage RayCluster under RayJob instead of managing RayJob directly", func() {
		ginkgo.By("checking the workload is not created when queue name is not set")
		job := testingrayjob.MakeJob(jobName, ns.Name).Obj()
		util.MustCreate(ctx, k8sClient, job)
		lookupKey := types.NamespacedName{Name: jobName, Namespace: ns.Name}
		createdJob := &rayv1.RayJob{}
		setInitStatus(jobName, ns.Name)
		gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())

		createdWorkload := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadrayjob.GetWorkloadNameForRayJob(job.Name, job.UID), Namespace: ns.Name}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(utiltesting.BeNotFoundError())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("checking no workload created even when queue name is set")
		jobQueueName := "test-queue"
		if createdJob.Labels == nil {
			createdJob.Labels = map[string]string{constants.QueueLabel: jobQueueName}
		} else {
			createdJob.Labels[constants.QueueLabel] = jobQueueName
		}
		gomega.Expect(k8sClient.Update(ctx, createdJob)).Should(gomega.Succeed())
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(utiltesting.BeNotFoundError())
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

	var defaultFlavor = utiltestingapi.MakeResourceFlavor("default").NodeLabel(instanceKey, "default").Obj()

	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup(jobframework.WithWaitForPodsReady(&configapi.WaitForPodsReady{})))

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
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-")

		onDemandFlavor = utiltestingapi.MakeResourceFlavor("on-demand").NodeLabel(instanceKey, "on-demand").Obj()
		util.MustCreate(ctx, k8sClient, onDemandFlavor)

		spotUntaintedFlavor = utiltestingapi.MakeResourceFlavor("spot-untainted").NodeLabel(instanceKey, "spot-untainted").Obj()
		util.MustCreate(ctx, k8sClient, spotUntaintedFlavor)

		clusterQueue = utiltestingapi.MakeClusterQueue("dev-clusterqueue").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("spot-untainted").Resource(corev1.ResourceCPU, "5").Resource(corev1.ResourceMemory, "1Gi").Obj(),
				*utiltestingapi.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Resource(corev1.ResourceMemory, "1Gi").Obj(),
			).Obj()
		util.MustCreate(ctx, k8sClient, clusterQueue)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, spotUntaintedFlavor, true)
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

		onDemandFlavor = utiltestingapi.MakeResourceFlavor("on-demand").NodeLabel(instanceKey, "on-demand").Obj()
		util.MustCreate(ctx, k8sClient, onDemandFlavor)

		clusterQueue = utiltestingapi.MakeClusterQueue("clusterqueue").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Resource(corev1.ResourceMemory, "4Gi").Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj()
		util.MustCreate(ctx, k8sClient, clusterQueue)

		ginkgo.By("creating localQueue")
		localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		util.MustCreate(ctx, k8sClient, localQueue)

		ginkgo.By("creating priority")
		priorityClass = utiltesting.MakePriorityClass(priorityClassName).
			PriorityValue(priorityValue).Obj()
		util.MustCreate(ctx, k8sClient, priorityClass)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, priorityClass, true)
	})

	ginkgo.It("Should skip reconciliation for RayJobs with clusterSelector", func() {
		ginkgo.By("Creating a RayJob with clusterSelector and queue label")
		job := testingrayjob.MakeJob("rayjob-with-selector", ns.Name).
			Queue("test-queue").
			ClusterSelector(map[string]string{"ray.io/cluster": "existing-cluster"}).
			Suspend(false).
			Obj()
		util.MustCreate(ctx, k8sClient, job)

		ginkgo.By("Checking that no workload is created for this job and the job remains unchanged")
		lookupKey := types.NamespacedName{
			Name:      workloadrayjob.GetWorkloadNameForRayJob(job.Name, job.UID),
			Namespace: ns.Name,
		}
		createdWorkload := &kueue.Workload{}
		createdJob := &rayv1.RayJob{}

		gomega.Consistently(func(g gomega.Gomega) {
			err := k8sClient.Get(ctx, lookupKey, createdWorkload)
			g.Expect(err).Should(gomega.HaveOccurred())
			g.Expect(client.IgnoreNotFound(err)).Should(gomega.Succeed())
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), createdJob)).Should(gomega.Succeed())
			g.Expect(createdJob.Spec.Suspend).Should(gomega.BeFalse())
		}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
	})
})
