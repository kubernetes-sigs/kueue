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

package paddlejob

import (
	"github.com/google/go-cmp/cmp/cmpopts"
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	workloadpaddlejob "sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/jobs/paddlejob"
	"sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/kubeflowjob"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpaddlejob "sigs.k8s.io/kueue/pkg/util/testingjobs/paddlejob"
	"sigs.k8s.io/kueue/test/integration/framework"
	kftesting "sigs.k8s.io/kueue/test/integration/singlecluster/controller/jobs/kubeflow"
	"sigs.k8s.io/kueue/test/util"
)

const (
	jobName     = "test-job"
	instanceKey = "cloud.provider.com/instance"
)

var _ = ginkgo.Describe("Job controller", framework.RedundantSpec, ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup(jobframework.WithManageJobsWithoutQueueName(true),
			jobframework.WithManagedJobsNamespaceSelector(util.NewNamespaceSelectorExcluding("unmanaged-ns"))))
		unmanagedNamespace := testing.MakeNamespace("unmanaged-ns")
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

	ginkgo.It("Should reconcile PaddleJobs", func() {
		kfJob := kubeflowjob.KubeflowJob{KFJobControl: (*workloadpaddlejob.JobControl)(testingpaddlejob.MakePaddleJob(jobName, ns.Name).PaddleReplicaSpecsDefault().Obj())}
		createdJob := kubeflowjob.KubeflowJob{KFJobControl: (*workloadpaddlejob.JobControl)(&kftraining.PaddleJob{})}

		kftesting.ShouldReconcileJob(ctx, k8sClient, kfJob, createdJob, []kftesting.PodSetsResource{
			{
				RoleName:    kftraining.PaddleJobReplicaTypeMaster,
				ResourceCPU: "on-demand",
			},
			{
				RoleName:    kftraining.PaddleJobReplicaTypeWorker,
				ResourceCPU: "spot",
			},
		})
	})

	ginkgo.It("Should not manage a job without a queue-name submitted to an unmanaged namespace", func() {
		ginkgo.By("Creating an unsuspended job without a queue-name in unmanaged-ns")
		kfJob := kubeflowjob.KubeflowJob{KFJobControl: (*workloadpaddlejob.JobControl)(testingpaddlejob.MakePaddleJob(jobName, "unmanaged-ns").Suspend(false).Obj())}
		createdJob := kubeflowjob.KubeflowJob{KFJobControl: (*workloadpaddlejob.JobControl)(&kftraining.PaddleJob{})}
		kftesting.ShouldNotReconcileUnmanagedJob(ctx, k8sClient, kfJob, createdJob)
	})
})

var _ = ginkgo.Describe("Job controller when waitForPodsReady enabled", framework.RedundantSpec, ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns            *corev1.Namespace
		defaultFlavor = testing.MakeResourceFlavor("default").NodeLabel(instanceKey, "default").Obj()
	)

	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup(jobframework.WithWaitForPodsReady(&configapi.WaitForPodsReady{Enable: true})))

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
		func(podsReadyTestSpec kftesting.PodsReadyTestSpec) {
			kfJob := kubeflowjob.KubeflowJob{KFJobControl: (*workloadpaddlejob.JobControl)(testingpaddlejob.MakePaddleJob(jobName, ns.Name).PaddleReplicaSpecsDefault().Parallelism(2).Obj())}
			createdJob := kubeflowjob.KubeflowJob{KFJobControl: (*workloadpaddlejob.JobControl)(&kftraining.PaddleJob{})}

			kftesting.JobControllerWhenWaitForPodsReadyEnabled(ctx, k8sClient, kfJob, createdJob, podsReadyTestSpec, []kftesting.PodSetsResource{
				{
					RoleName:    kftraining.PaddleJobReplicaTypeMaster,
					ResourceCPU: "default",
				},
				{
					RoleName:    kftraining.PaddleJobReplicaTypeWorker,
					ResourceCPU: "default",
				},
			})
		},
		ginkgo.Entry("No progress", kftesting.PodsReadyTestSpec{
			WantCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionFalse,
				Reason:  kueue.WorkloadWaitForStart,
				Message: "Not all pods are ready or succeeded",
			},
		}),
		ginkgo.Entry("Running PaddleJob", kftesting.PodsReadyTestSpec{
			JobStatus: kftraining.JobStatus{
				Conditions: []kftraining.JobCondition{
					{
						Type:   kftraining.JobRunning,
						Status: corev1.ConditionTrue,
						Reason: "Running",
					},
				},
			},
			WantCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionTrue,
				Reason:  kueue.WorkloadStarted,
				Message: "All pods reached readiness and the workload is running",
			},
		}),
		ginkgo.Entry("Running PaddleJob; PodsReady=False before", kftesting.PodsReadyTestSpec{
			BeforeCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionFalse,
				Reason:  kueue.WorkloadWaitForStart,
				Message: "Not all pods are ready or succeeded",
			},
			JobStatus: kftraining.JobStatus{
				Conditions: []kftraining.JobCondition{
					{
						Type:   kftraining.JobRunning,
						Status: corev1.ConditionTrue,
						Reason: "Running",
					},
				},
			},
			WantCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionTrue,
				Reason:  kueue.WorkloadStarted,
				Message: "All pods reached readiness and the workload is running",
			},
		}),
		ginkgo.Entry("Job suspended; PodsReady=True before", kftesting.PodsReadyTestSpec{
			BeforeJobStatus: &kftraining.JobStatus{
				Conditions: []kftraining.JobCondition{
					{
						Type:   kftraining.JobRunning,
						Status: corev1.ConditionTrue,
						Reason: "Running",
					},
				},
			},
			BeforeCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionTrue,
				Reason:  kueue.WorkloadStarted,
				Message: "All pods reached readiness and the workload is running",
			},
			JobStatus: kftraining.JobStatus{
				Conditions: []kftraining.JobCondition{
					{
						Type:   kftraining.JobRunning,
						Status: corev1.ConditionFalse,
						Reason: "Suspended",
					},
				},
			},
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

var _ = ginkgo.Describe("Job controller interacting with scheduler", framework.RedundantSpec, ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
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

		onDemandFlavor = testing.MakeResourceFlavor("on-demand").NodeLabel(instanceKey, "on-demand").Obj()
		util.MustCreate(ctx, k8sClient, onDemandFlavor)

		spotUntaintedFlavor = testing.MakeResourceFlavor("spot-untainted").NodeLabel(instanceKey, "spot-untainted").Obj()
		util.MustCreate(ctx, k8sClient, spotUntaintedFlavor)

		clusterQueue = testing.MakeClusterQueue("dev-clusterqueue").
			ResourceGroup(
				*testing.MakeFlavorQuotas("spot-untainted").Resource(corev1.ResourceCPU, "5").Obj(),
				*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj(),
			).Obj()
		util.MustCreate(ctx, k8sClient, clusterQueue)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		gomega.Expect(util.DeleteObject(ctx, k8sClient, spotUntaintedFlavor)).To(gomega.Succeed())
	})

	ginkgo.It("Should schedule jobs as they fit in their ClusterQueue", func() {
		ginkgo.By("creating localQueue")
		localQueue = testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		util.MustCreate(ctx, k8sClient, localQueue)

		kfJob := kubeflowjob.KubeflowJob{KFJobControl: (*workloadpaddlejob.JobControl)(
			testingpaddlejob.MakePaddleJob(jobName, ns.Name).Queue(localQueue.Name).
				PaddleReplicaSpecsDefault().
				Request(kftraining.PaddleJobReplicaTypeMaster, corev1.ResourceCPU, "3").
				Request(kftraining.PaddleJobReplicaTypeWorker, corev1.ResourceCPU, "4").
				Obj(),
		)}
		createdJob := kubeflowjob.KubeflowJob{KFJobControl: (*workloadpaddlejob.JobControl)(&kftraining.PaddleJob{})}

		kftesting.ShouldScheduleJobsAsTheyFitInTheirClusterQueue(ctx, k8sClient, kfJob, createdJob, clusterQueue, []kftesting.PodSetsResource{
			{
				RoleName:    kftraining.PaddleJobReplicaTypeMaster,
				ResourceCPU: kueue.ResourceFlavorReference(spotUntaintedFlavor.Name),
			},
			{
				RoleName:    kftraining.PaddleJobReplicaTypeWorker,
				ResourceCPU: kueue.ResourceFlavorReference(onDemandFlavor.Name),
			},
		})
	})
})

var _ = ginkgo.Describe("PaddleJob controller when TopologyAwareScheduling enabled", framework.RedundantSpec, ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	const (
		nodeGroupLabel = "node-group"
	)

	var (
		ns           *corev1.Namespace
		nodes        []corev1.Node
		topology     *kueuealpha.Topology
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
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TopologyAwareScheduling, true)

		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "tas-paddlejob-")

		nodes = []corev1.Node{
			*testingnode.MakeNode("b1r1").
				Label(nodeGroupLabel, "tas").
				Label(testing.DefaultBlockTopologyLevel, "b1").
				Label(testing.DefaultRackTopologyLevel, "r1").
				StatusAllocatable(corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1"),
					corev1.ResourceMemory: resource.MustParse("1Gi"),
					corev1.ResourcePods:   resource.MustParse("10"),
				}).
				Ready().
				Obj(),
		}
		util.CreateNodesWithStatus(ctx, k8sClient, nodes)

		topology = testing.MakeDefaultTwoLevelTopology("default")
		util.MustCreate(ctx, k8sClient, topology)

		tasFlavor = testing.MakeResourceFlavor("tas-flavor").
			NodeLabel(nodeGroupLabel, "tas").
			TopologyName("default").Obj()
		util.MustCreate(ctx, k8sClient, tasFlavor)

		clusterQueue = testing.MakeClusterQueue("cluster-queue").
			ResourceGroup(*testing.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj()).
			Obj()
		util.MustCreate(ctx, k8sClient, clusterQueue)
		util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

		localQueue = testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
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
		paddleJob := testingpaddlejob.MakePaddleJob("paddlejob", ns.Name).
			PaddleReplicaSpecs(
				testingpaddlejob.PaddleReplicaSpecRequirement{
					ReplicaType:  kftraining.PaddleJobReplicaTypeMaster,
					ReplicaCount: 1,
					Annotations: map[string]string{
						kueuealpha.PodSetRequiredTopologyAnnotation: testing.DefaultRackTopologyLevel,
					},
				},
				testingpaddlejob.PaddleReplicaSpecRequirement{
					ReplicaType:  kftraining.PaddleJobReplicaTypeWorker,
					ReplicaCount: 1,
					Annotations: map[string]string{
						kueuealpha.PodSetPreferredTopologyAnnotation: testing.DefaultBlockTopologyLevel,
					},
				},
			).
			Queue(localQueue.Name).
			Request(kftraining.PaddleJobReplicaTypeMaster, corev1.ResourceCPU, "100m").
			Request(kftraining.PaddleJobReplicaTypeWorker, corev1.ResourceCPU, "100m").
			Obj()
		ginkgo.By("creating a PaddleJob", func() {
			util.MustCreate(ctx, k8sClient, paddleJob)
		})

		wl := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadpaddlejob.GetWorkloadNameForPaddleJob(paddleJob.Name, paddleJob.UID), Namespace: ns.Name}

		ginkgo.By("verify the workload is created", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, wl)).Should(gomega.Succeed())
				g.Expect(wl.Spec.PodSets).Should(gomega.BeComparableTo([]kueue.PodSet{
					{
						Name:  kueue.NewPodSetReference(string(kftraining.PaddleJobReplicaTypeMaster)),
						Count: 1,
						TopologyRequest: &kueue.PodSetTopologyRequest{
							Required:      ptr.To(testing.DefaultRackTopologyLevel),
							PodIndexLabel: ptr.To(kftraining.ReplicaIndexLabel),
						},
					},
					{
						Name:  kueue.NewPodSetReference(string(kftraining.PaddleJobReplicaTypeWorker)),
						Count: 1,
						TopologyRequest: &kueue.PodSetTopologyRequest{
							Preferred:     ptr.To(testing.DefaultBlockTopologyLevel),
							PodIndexLabel: ptr.To(kftraining.ReplicaIndexLabel),
						},
					},
				}, cmpopts.IgnoreFields(kueue.PodSet{}, "Template")))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verify the workload is admitted", func() {
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
			util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
		})

		ginkgo.By("verify admission for the workload", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, wl)).Should(gomega.Succeed())
				g.Expect(wl.Status.Admission).ShouldNot(gomega.BeNil())
				g.Expect(wl.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(2))
				g.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
					&kueue.TopologyAssignment{
						Levels:  []string{testing.DefaultBlockTopologyLevel, testing.DefaultRackTopologyLevel},
						Domains: []kueue.TopologyDomainAssignment{{Count: 1, Values: []string{"b1", "r1"}}},
					},
				))
				g.Expect(wl.Status.Admission.PodSetAssignments[1].TopologyAssignment).Should(gomega.BeComparableTo(
					&kueue.TopologyAssignment{
						Levels:  []string{testing.DefaultBlockTopologyLevel, testing.DefaultRackTopologyLevel},
						Domains: []kueue.TopologyDomainAssignment{{Count: 1, Values: []string{"b1", "r1"}}},
					},
				))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
