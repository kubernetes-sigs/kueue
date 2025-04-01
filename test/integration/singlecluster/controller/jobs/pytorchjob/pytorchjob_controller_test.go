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

package pytorchjob

import (
	"github.com/google/go-cmp/cmp/cmpopts"
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	workloadpytorchjob "sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/jobs/pytorchjob"
	"sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/kubeflowjob"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpytorchjob "sigs.k8s.io/kueue/pkg/util/testingjobs/pytorchjob"
	"sigs.k8s.io/kueue/pkg/workload"
	kftesting "sigs.k8s.io/kueue/test/integration/singlecluster/controller/jobs/kubeflow"
	"sigs.k8s.io/kueue/test/util"
)

const (
	jobName      = "test-job"
	instanceKey  = "cloud.provider.com/instance"
	jobQueueName = "test-queue"
)

var _ = ginkgo.Describe("Job controller", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
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

	ginkgo.It("Should reconcile PyTorchJobs", func() {
		kfJob := kubeflowjob.KubeflowJob{KFJobControl: (*workloadpytorchjob.JobControl)(testingpytorchjob.MakePyTorchJob(jobName, ns.Name).PyTorchReplicaSpecsDefault().Obj())}
		createdJob := kubeflowjob.KubeflowJob{KFJobControl: (*workloadpytorchjob.JobControl)(&kftraining.PyTorchJob{})}
		kftesting.ShouldReconcileJob(ctx, k8sClient, kfJob, createdJob, []kftesting.PodSetsResource{
			{
				RoleName:    kftraining.PyTorchJobReplicaTypeMaster,
				ResourceCPU: "on-demand",
			},
			{
				RoleName:    kftraining.PyTorchJobReplicaTypeWorker,
				ResourceCPU: "spot",
			},
		})
	})

	ginkgo.It("Should not manage a job without a queue-name submitted to an unmanaged namespace", func() {
		ginkgo.By("Creating an unsuspended job without a queue-name in unmanaged-ns")
		kfJob := kubeflowjob.KubeflowJob{KFJobControl: (*workloadpytorchjob.JobControl)(testingpytorchjob.MakePyTorchJob(jobName, "unmanaged-ns").Suspend(false).Obj())}
		createdJob := kubeflowjob.KubeflowJob{KFJobControl: (*workloadpytorchjob.JobControl)(&kftraining.PyTorchJob{})}
		kftesting.ShouldNotReconcileUnmanagedJob(ctx, k8sClient, kfJob, createdJob)
	})
})

var _ = ginkgo.Describe("Job controller for workloads when only jobs with queue are managed", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var realClock = clock.RealClock{}

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

	ginkgo.It("Should reconcile jobs only when queue is set", func() {
		ginkgo.By("checking the workload is not created when queue name is not set")
		job := testingpytorchjob.MakePyTorchJob(jobName, ns.Name).
			PyTorchReplicaSpecsDefault().
			Obj()
		util.MustCreate(ctx, k8sClient, job)
		lookupKey := types.NamespacedName{Name: jobName, Namespace: ns.Name}
		createdJob := &kftraining.PyTorchJob{}
		gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())

		createdWorkload := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadpytorchjob.GetWorkloadNameForPyTorchJob(job.Name, job.UID), Namespace: ns.Name}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(testing.BeNotFoundError())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("checking the workload is created when queue name is set")
		createdJob.Annotations = map[string]string{constants.QueueAnnotation: jobQueueName}
		gomega.Expect(k8sClient.Update(ctx, createdJob)).Should(gomega.Succeed())
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
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
			util.MustCreate(ctx, k8sClient, admissionCheck)
			util.SetAdmissionCheckActive(ctx, k8sClient, admissionCheck, metav1.ConditionTrue)
			clusterQueueAc = testing.MakeClusterQueue("prod-cq-with-checks").
				ResourceGroup(
					*testing.MakeFlavorQuotas("test-flavor").Resource(corev1.ResourceCPU, "5").Obj(),
				).AdmissionChecks("check").Obj()
			util.MustCreate(ctx, k8sClient, clusterQueueAc)
			localQueue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueueAc.Name).Obj()
			util.MustCreate(ctx, k8sClient, localQueue)
			testFlavor = testing.MakeResourceFlavor("test-flavor").NodeLabel(instanceKey, "test-flavor").Obj()
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
			createdJob := &kftraining.PyTorchJob{}
			createdWorkload := &kueue.Workload{}
			job := testingpytorchjob.MakePyTorchJob(jobName, ns.Name).
				PyTorchReplicaSpecsDefault().
				PodAnnotation(kftraining.PyTorchJobReplicaTypeWorker, "old-ann-key", "old-ann-value").
				PodLabel(kftraining.PyTorchJobReplicaTypeWorker, "old-label-key", "old-label-value").
				Queue(localQueue.Name).
				Obj()

			ginkgo.By("creating the job with pod labels & annotations", func() {
				util.MustCreate(ctx, k8sClient, job)
			})

			ginkgo.By("fetch the job and verify it is suspended as the checks are not ready", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, *jobLookupKey, createdJob)).Should(gomega.Succeed())
					g.Expect(createdJob.Spec.RunPolicy.Suspend).Should(gomega.Equal(ptr.To(true)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			wlLookupKey := &types.NamespacedName{Name: workloadpytorchjob.GetWorkloadNameForPyTorchJob(job.Name, job.UID), Namespace: ns.Name}
			ginkgo.By("fetch the created workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, *wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("add labels & annotations to the admission check", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					var newWL kueue.Workload
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(createdWorkload), &newWL)).To(gomega.Succeed())
					workload.SetAdmissionCheckState(&newWL.Status.AdmissionChecks, kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: "master",
							},
							{
								Name: "worker",
								Annotations: map[string]string{
									"ann1": "ann-value1",
								},
								Labels: map[string]string{
									"label1": "label-value1",
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
					}, realClock)
					g.Expect(k8sClient.Status().Update(ctx, &newWL)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("admit the workload", func() {
				admission := testing.MakeAdmission(clusterQueueAc.Name).
					PodSets(
						kueue.PodSetAssignment{
							Name: "master",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "test-flavor",
							},
							Count: ptr.To(createdWorkload.Spec.PodSets[0].Count),
						},
						kueue.PodSetAssignment{
							Name: "worker",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "test-flavor",
							},
							Count: ptr.To(createdWorkload.Spec.PodSets[1].Count),
						},
					).
					Obj()
				gomega.Expect(k8sClient.Get(ctx, *wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, admission)).Should(gomega.Succeed())
				util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
			})

			ginkgo.By("await for the job to start", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, *jobLookupKey, createdJob)).Should(gomega.Succeed())
					g.Expect(createdJob.Spec.RunPolicy.Suspend).Should(gomega.Equal(ptr.To(false)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verify the PodSetUpdates are propagated to the running job", func() {
				worker := createdJob.Spec.PyTorchReplicaSpecs[kftraining.PyTorchJobReplicaTypeWorker].Template
				gomega.Expect(worker.Annotations).Should(gomega.HaveKeyWithValue("ann1", "ann-value1"))
				gomega.Expect(worker.Annotations).Should(gomega.HaveKeyWithValue("old-ann-key", "old-ann-value"))
				gomega.Expect(worker.Labels).Should(gomega.HaveKeyWithValue("label1", "label-value1"))
				gomega.Expect(worker.Labels).Should(gomega.HaveKeyWithValue("old-label-key", "old-label-value"))
				gomega.Expect(worker.Spec.NodeSelector).Should(gomega.HaveKeyWithValue(instanceKey, "test-flavor"))
				gomega.Expect(worker.Spec.NodeSelector).Should(gomega.HaveKeyWithValue("selector1", "selector-value1"))
				gomega.Expect(worker.Spec.Tolerations).Should(gomega.BeComparableTo(
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
				gomega.Expect(util.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
			})

			ginkgo.By("clear the workload's admission to stop the job", func() {
				gomega.Expect(k8sClient.Get(ctx, *wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, nil)).Should(gomega.Succeed())
				util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
			})

			ginkgo.By("await for the job to be suspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, *jobLookupKey, createdJob)).Should(gomega.Succeed())
					g.Expect(createdJob.Spec.RunPolicy.Suspend).Should(gomega.Equal(ptr.To(true)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verify the PodSetUpdates are restored", func() {
				worker := createdJob.Spec.PyTorchReplicaSpecs[kftraining.PyTorchJobReplicaTypeWorker].Template
				gomega.Expect(worker.Annotations).ShouldNot(gomega.HaveKey("ann1"))
				gomega.Expect(worker.Annotations).Should(gomega.HaveKeyWithValue("old-ann-key", "old-ann-value"))
				gomega.Expect(worker.Labels).ShouldNot(gomega.HaveKey("label1"))
				gomega.Expect(worker.Labels).Should(gomega.HaveKeyWithValue("old-label-key", "old-label-value"))
				gomega.Expect(worker.Spec.NodeSelector).ShouldNot(gomega.HaveKey(instanceKey))
				gomega.Expect(worker.Spec.NodeSelector).ShouldNot(gomega.HaveKey("selector1"))
			})
		})
	})
})

var _ = ginkgo.Describe("Job controller when waitForPodsReady enabled", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
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
			kfJob := kubeflowjob.KubeflowJob{KFJobControl: (*workloadpytorchjob.JobControl)(testingpytorchjob.MakePyTorchJob(jobName, ns.Name).PyTorchReplicaSpecsDefault().Parallelism(2).Obj())}
			createdJob := kubeflowjob.KubeflowJob{KFJobControl: (*workloadpytorchjob.JobControl)(&kftraining.PyTorchJob{})}

			kftesting.JobControllerWhenWaitForPodsReadyEnabled(ctx, k8sClient, kfJob, createdJob, podsReadyTestSpec, []kftesting.PodSetsResource{
				{
					RoleName:    kftraining.PyTorchJobReplicaTypeMaster,
					ResourceCPU: "default",
				},
				{
					RoleName:    kftraining.PyTorchJobReplicaTypeWorker,
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
		ginkgo.Entry("Running PyTorchJob", kftesting.PodsReadyTestSpec{
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
		ginkgo.Entry("Running PyTorchJob; PodsReady=False before", kftesting.PodsReadyTestSpec{
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

var _ = ginkgo.Describe("Job controller interacting with scheduler", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
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
		util.ExpectObjectToBeDeleted(ctx, k8sClient, spotUntaintedFlavor, true)
	})

	ginkgo.It("Should schedule jobs as they fit in their ClusterQueue", func() {
		ginkgo.By("creating localQueue")
		localQueue = testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		util.MustCreate(ctx, k8sClient, localQueue)

		kfJob := kubeflowjob.KubeflowJob{KFJobControl: (*workloadpytorchjob.JobControl)(
			testingpytorchjob.MakePyTorchJob(jobName, ns.Name).
				PyTorchReplicaSpecsDefault().
				Queue(localQueue.Name).
				Request(kftraining.PyTorchJobReplicaTypeMaster, corev1.ResourceCPU, "3").
				Request(kftraining.PyTorchJobReplicaTypeWorker, corev1.ResourceCPU, "4").
				Obj(),
		)}
		createdJob := kubeflowjob.KubeflowJob{KFJobControl: (*workloadpytorchjob.JobControl)(&kftraining.PyTorchJob{})}

		kftesting.ShouldScheduleJobsAsTheyFitInTheirClusterQueue(ctx, k8sClient, kfJob, createdJob, clusterQueue, []kftesting.PodSetsResource{
			{
				RoleName:    kftraining.PyTorchJobReplicaTypeMaster,
				ResourceCPU: kueue.ResourceFlavorReference(spotUntaintedFlavor.Name),
			},
			{
				RoleName:    kftraining.PyTorchJobReplicaTypeWorker,
				ResourceCPU: kueue.ResourceFlavorReference(onDemandFlavor.Name),
			},
		})
	})

	ginkgo.When("The workload's admission is removed", func() {
		ginkgo.It("Should restore the original node selectors", func() {
			localQueue := testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			job := testingpytorchjob.MakePyTorchJob(jobName, ns.Name).
				PyTorchReplicaSpecsDefault().
				Queue(localQueue.Name).
				Request(kftraining.PyTorchJobReplicaTypeMaster, corev1.ResourceCPU, "3").
				Request(kftraining.PyTorchJobReplicaTypeWorker, corev1.ResourceCPU, "4").
				Obj()
			lookupKey := types.NamespacedName{Name: job.Name, Namespace: job.Namespace}
			createdJob := &kftraining.PyTorchJob{}

			nodeSelectors := func(j *kftraining.PyTorchJob) map[kftraining.ReplicaType]map[string]string {
				ret := map[kftraining.ReplicaType]map[string]string{}
				for k := range j.Spec.PyTorchReplicaSpecs {
					ret[k] = j.Spec.PyTorchReplicaSpecs[k].Template.Spec.NodeSelector
				}
				return ret
			}

			ginkgo.By("create a job", func() {
				util.MustCreate(ctx, k8sClient, job)
			})

			ginkgo.By("job should be suspend", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
					g.Expect(createdJob.Spec.RunPolicy.Suspend).Should(gomega.Equal(ptr.To(true)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			// backup the node selectors
			originalNodeSelectors := nodeSelectors(createdJob)

			ginkgo.By("create a localQueue", func() {
				util.MustCreate(ctx, k8sClient, localQueue)
			})

			ginkgo.By("job should be unsuspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
					g.Expect(createdJob.Spec.RunPolicy.Suspend).Should(gomega.Equal(ptr.To(false)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("the node selectors should be updated", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
					g.Expect(nodeSelectors(createdJob)).ShouldNot(gomega.Equal(originalNodeSelectors))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("delete the localQueue to prevent readmission", func() {
				gomega.Expect(util.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
			})

			ginkgo.By("clear the workload's admission to stop the job", func() {
				wl := &kueue.Workload{}
				wlKey := types.NamespacedName{Name: workloadpytorchjob.GetWorkloadNameForPyTorchJob(job.Name, job.UID), Namespace: job.Namespace}
				gomega.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
				gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, wl, nil)).Should(gomega.Succeed())
				util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, wl)
			})

			ginkgo.By("the node selectors should be restored", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
					g.Expect(nodeSelectors(createdJob)).Should(gomega.Equal(originalNodeSelectors))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})

var _ = ginkgo.Describe("PyTorchJob controller when TopologyAwareScheduling enabled", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
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

		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "tas-pytorchjob-")

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
		pytorchJob := testingpytorchjob.MakePyTorchJob("pytorchjob", ns.Name).
			PyTorchReplicaSpecs(
				testingpytorchjob.PyTorchReplicaSpecRequirement{
					ReplicaType:  kftraining.PyTorchJobReplicaTypeMaster,
					ReplicaCount: 1,
					Annotations: map[string]string{
						kueuealpha.PodSetRequiredTopologyAnnotation: testing.DefaultRackTopologyLevel,
					},
				},
				testingpytorchjob.PyTorchReplicaSpecRequirement{
					ReplicaType:  kftraining.PyTorchJobReplicaTypeWorker,
					ReplicaCount: 1,
					Annotations: map[string]string{
						kueuealpha.PodSetPreferredTopologyAnnotation: testing.DefaultBlockTopologyLevel,
					},
				},
			).
			Queue(localQueue.Name).
			Request(kftraining.PyTorchJobReplicaTypeMaster, corev1.ResourceCPU, "100m").
			Request(kftraining.PyTorchJobReplicaTypeWorker, corev1.ResourceCPU, "100m").
			Obj()
		ginkgo.By("creating a PyTorchJob", func() {
			util.MustCreate(ctx, k8sClient, pytorchJob)
		})

		wl := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadpytorchjob.GetWorkloadNameForPyTorchJob(pytorchJob.Name, pytorchJob.UID), Namespace: ns.Name}

		ginkgo.By("verify the workload is created", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, wl)).Should(gomega.Succeed())
				g.Expect(wl.Spec.PodSets).Should(gomega.BeComparableTo([]kueue.PodSet{
					{
						Name:  kueue.NewPodSetReference(string(kftraining.PyTorchJobReplicaTypeMaster)),
						Count: 1,
						TopologyRequest: &kueue.PodSetTopologyRequest{
							Required:      ptr.To(testing.DefaultRackTopologyLevel),
							PodIndexLabel: ptr.To(kftraining.ReplicaIndexLabel),
						},
					},
					{
						Name:  kueue.NewPodSetReference(string(kftraining.PyTorchJobReplicaTypeWorker)),
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
