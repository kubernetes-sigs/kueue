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

package sparkapplication

import (
	"github.com/google/go-cmp/cmp/cmpopts"
	sparkv1beta2 "github.com/kubeflow/spark-operator/v2/api/v1beta2"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	workloadsparkapplication "sigs.k8s.io/kueue/pkg/controller/jobs/sparkapplication"
	"sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingsparkapplication "sigs.k8s.io/kueue/pkg/util/testingjobs/sparkapplication"
	"sigs.k8s.io/kueue/test/util"
)

const (
	jobName = "test-job"
)

var _ = ginkgo.Describe("SparkApplication controller", ginkgo.Label("job:sparkapplication", "area:jobs"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
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

	ginkgo.It("Should reconcile SparkApplications", func() {
		sparkApplication := testingsparkapplication.MakeSparkApplication(jobName, ns.Name).
			Suspend(false).
			Obj()
		shouldReconcileSparkApplication(ctx, k8sClient, sparkApplication)
	})

	ginkgo.It("Should not manage a job without a queue-name submitted to an unmanaged namespace", func() {
		ginkgo.By("Creating an unsuspended job without a queue-name in unmanaged-ns")
		sparkApplication := testingsparkapplication.MakeSparkApplication(jobName, "unmanaged-ns").
			Suspend(false).
			Obj()
		shouldNotReconcileUnmanagedSparkApplication(ctx, k8sClient, sparkApplication)
	})
})

var _ = ginkgo.Describe("SparkApplication controller when waitForPodsReady enabled", ginkgo.Label("job:sparkapplication", "area:jobs"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns            *corev1.Namespace
		defaultFlavor = utiltestingapi.MakeResourceFlavor("default").NodeLabel(instanceKey, "default").Obj()
	)

	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup(jobframework.WithWaitForPodsReady(&configapi.WaitForPodsReady{}), jobframework.WithCache(schdcache.New(k8sClient))))

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

	ginkgo.DescribeTable("Single SparkApplication at different stages of progress towards completion",
		func(podsReadyTestSpec PodsReadyTestSpec) {
			sparkApplication := testingsparkapplication.MakeSparkApplication(jobName, ns.Name).Obj()
			createdSparkApplication := &sparkv1beta2.SparkApplication{}

			waitForPodsReadyEnabledForSparkApplication(ctx, k8sClient, sparkApplication, createdSparkApplication, podsReadyTestSpec)
		},
		ginkgo.Entry("No progress", PodsReadyTestSpec{
			AppState: sparkv1beta2.ApplicationStateSubmitted,
			WantCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionFalse,
				Reason:  kueue.WorkloadWaitForStart,
				Message: "Not all pods are ready or succeeded",
			},
		}),
		ginkgo.Entry("Running SparkApplication", PodsReadyTestSpec{
			AppState: sparkv1beta2.ApplicationStateRunning,
			WantCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionTrue,
				Reason:  kueue.WorkloadStarted,
				Message: "All pods reached readiness and the workload is running",
			},
		}),
		ginkgo.Entry("Running SparkApplication; PodsReady=False before", PodsReadyTestSpec{
			BeforeCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionFalse,
				Reason:  kueue.WorkloadWaitForStart,
				Message: "Not all pods are ready or succeeded",
			},
			AppState: sparkv1beta2.ApplicationStateRunning,
			WantCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionTrue,
				Reason:  kueue.WorkloadStarted,
				Message: "All pods reached readiness and the workload is running",
			},
		}),
		ginkgo.Entry("SparkApplication suspended; PodsReady=True before", PodsReadyTestSpec{
			BeforeAppState: ptr.To(sparkv1beta2.ApplicationStateRunning),
			BeforeCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionTrue,
				Reason:  kueue.WorkloadStarted,
				Message: "All pods reached readiness and the workload is running",
			},
			AppState:  sparkv1beta2.ApplicationStateSubmitted,
			Suspended: true,
			WantCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionFalse,
				Reason:  kueue.WorkloadWaitForStart,
				Message: "Not all pods are ready or succeeded",
			},
		}),
	)
})

var _ = ginkgo.Describe("SparkApplication controller interacting with scheduler", ginkgo.Label("job:sparkapplication", "area:jobs"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns                  *corev1.Namespace
		onDemandFlavor      *kueue.ResourceFlavor
		spotUntaintedFlavor *kueue.ResourceFlavor
		clusterQueue        *kueue.ClusterQueue
		localQueue          *kueue.LocalQueue
	)

	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup(false))
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-")

		onDemandFlavor = utiltestingapi.MakeResourceFlavor("on-demand").NodeLabel(instanceKey, "on-demand").Obj()
		util.MustCreate(ctx, k8sClient, onDemandFlavor)

		spotUntaintedFlavor = utiltestingapi.MakeResourceFlavor("spot-untainted").NodeLabel(instanceKey, "spot-untainted").Obj()
		util.MustCreate(ctx, k8sClient, spotUntaintedFlavor)

		clusterQueue = utiltestingapi.MakeClusterQueue("clusterqueue-scheduler").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("spot-untainted").
					Resource(corev1.ResourceCPU, "5").
					Resource(corev1.ResourceMemory, "5Gi").
					Obj(),
				*utiltestingapi.MakeFlavorQuotas("on-demand").
					Resource(corev1.ResourceCPU, "5").
					Resource(corev1.ResourceMemory, "5Gi").
					Obj(),
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
		localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		util.MustCreate(ctx, k8sClient, localQueue)

		ginkgo.By("checking a SparkApplication is admitted and unsuspended")
		sparkApplication := testingsparkapplication.MakeSparkApplication(jobName, ns.Name).
			Queue(localQueue.Name).
			DriverCoreRequest("3").
			ExecutorCoreRequest("4").
			ExecutorInstances(1).
			Obj()
		util.MustCreate(ctx, k8sClient, sparkApplication)

		createdSparkApplication := &sparkv1beta2.SparkApplication{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: sparkApplication.Name, Namespace: sparkApplication.Namespace}, createdSparkApplication)).Should(gomega.Succeed())
			g.Expect(ptr.Deref(createdSparkApplication.Spec.Suspend, true)).Should(gomega.BeFalse())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		gomega.Expect(createdSparkApplication.Spec.Driver.NodeSelector[instanceKey]).Should(gomega.Equal(spotUntaintedFlavor.Name))
		gomega.Expect(createdSparkApplication.Spec.Executor.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))
		util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
		util.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
	})
})

var _ = ginkgo.Describe("SparkApplication controller with TopologyAwareScheduling", ginkgo.Label("job:sparkapplication", "area:jobs", "feature:tas"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	const (
		nodeGroupLabel = "node-group"
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
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup(true))
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "tas-sparkapplication-")

		nodes = []corev1.Node{
			*testingnode.MakeNode("b1r1").
				Label(nodeGroupLabel, "tas").
				Label(utiltesting.DefaultBlockTopologyLevel, "b1").
				Label(utiltesting.DefaultRackTopologyLevel, "r1").
				StatusAllocatable(corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1"),
					corev1.ResourceMemory: resource.MustParse("1Gi"),
					corev1.ResourcePods:   resource.MustParse("10"),
				}).
				Ready().
				Obj(),
		}
		util.CreateNodesWithStatus(ctx, k8sClient, nodes)

		topology = utiltestingapi.MakeDefaultTwoLevelTopology("default")
		util.MustCreate(ctx, k8sClient, topology)

		tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
			NodeLabel(nodeGroupLabel, "tas").
			TopologyName("default").Obj()
		util.MustCreate(ctx, k8sClient, tasFlavor)

		clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).
				Resource(corev1.ResourceCPU, "5").
				Resource(corev1.ResourceMemory, "5Gi").
				Obj()).
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
		sparkApplication := testingsparkapplication.MakeSparkApplication("sparkapplication", ns.Name).
			DriverAnnotation(kueue.PodSetRequiredTopologyAnnotation, utiltesting.DefaultRackTopologyLevel).
			ExecutorAnnotation(kueue.PodSetPreferredTopologyAnnotation, utiltesting.DefaultBlockTopologyLevel).
			DriverCoreRequest("100m").
			ExecutorCoreRequest("100m").
			ExecutorInstances(1).
			Queue(localQueue.Name).
			Obj()

		ginkgo.By("creating a SparkApplication", func() {
			util.MustCreate(ctx, k8sClient, sparkApplication)
		})

		wl := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadsparkapplication.GetWorkloadNameForSparkApplication(sparkApplication.Name, sparkApplication.UID), Namespace: ns.Name}

		ginkgo.By("verify the workload is created", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, wl)).Should(gomega.Succeed())
				g.Expect(wl.Spec.PodSets).Should(gomega.BeComparableTo([]kueue.PodSet{
					{
						Name:  kueue.NewPodSetReference("driver"),
						Count: 1,
						TopologyRequest: &kueue.PodSetTopologyRequest{
							Required: ptr.To(utiltesting.DefaultRackTopologyLevel),
						},
					},
					{
						Name:  kueue.NewPodSetReference("executor"),
						Count: 1,
						TopologyRequest: &kueue.PodSetTopologyRequest{
							Preferred: ptr.To(utiltesting.DefaultBlockTopologyLevel),
						},
					},
				}, cmpopts.IgnoreFields(kueue.PodSet{}, "Template")))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verify the workload is admitted", func() {
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
			util.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
		})

		ginkgo.By("verify admission for the workload", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, wl)).Should(gomega.Succeed())
				g.Expect(wl.Status.Admission).ShouldNot(gomega.BeNil())
				g.Expect(wl.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(2))
				g.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
					tas.V1Beta2From(&tas.TopologyAssignment{
						Levels:  []string{utiltesting.DefaultBlockTopologyLevel, utiltesting.DefaultRackTopologyLevel},
						Domains: []tas.TopologyDomainAssignment{{Count: 1, Values: []string{"b1", "r1"}}},
					}),
				))
				g.Expect(wl.Status.Admission.PodSetAssignments[1].TopologyAssignment).Should(gomega.BeComparableTo(
					tas.V1Beta2From(&tas.TopologyAssignment{
						Levels:  []string{utiltesting.DefaultBlockTopologyLevel, utiltesting.DefaultRackTopologyLevel},
						Domains: []tas.TopologyDomainAssignment{{Count: 1, Values: []string{"b1", "r1"}}},
					}),
				))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
