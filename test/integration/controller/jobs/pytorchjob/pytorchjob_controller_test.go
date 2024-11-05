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

package pytorchjob

import (
	"strings"

	"github.com/google/go-cmp/cmp/cmpopts"
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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
	testingpytorchjob "sigs.k8s.io/kueue/pkg/util/testingjobs/pytorchjob"
	"sigs.k8s.io/kueue/pkg/workload"
	kftesting "sigs.k8s.io/kueue/test/integration/controller/jobs/kubeflow"
	"sigs.k8s.io/kueue/test/util"
)

const (
	jobName           = "test-job"
	instanceKey       = "cloud.provider.com/instance"
	priorityClassName = "test-priority-class"
	priorityValue     = 10
	jobQueueName      = "test-queue"
)

// +kubebuilder:docs-gen:collapse=Imports

var _ = ginkgo.Describe("Job controller", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup(jobframework.WithManageJobsWithoutQueueName(true)))
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	var (
		ns *corev1.Namespace
	)
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

	ginkgo.It("Should reconcile jobs only when queue is set", func() {
		ginkgo.By("checking the workload is not created when queue name is not set")
		job := testingpytorchjob.MakePyTorchJob(jobName, ns.Name).
			PyTorchReplicaSpecsDefault().
			Obj()
		gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
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
			gomega.Expect(k8sClient.Create(ctx, admissionCheck)).To(gomega.Succeed())
			util.SetAdmissionCheckActive(ctx, k8sClient, admissionCheck, metav1.ConditionTrue)
			clusterQueueAc = testing.MakeClusterQueue("prod-cq-with-checks").
				ResourceGroup(
					*testing.MakeFlavorQuotas("test-flavor").Resource(corev1.ResourceCPU, "5").Obj(),
				).AdmissionChecks("check").Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueueAc)).Should(gomega.Succeed())
			localQueue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueueAc.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())
			testFlavor = testing.MakeResourceFlavor("test-flavor").NodeLabel(instanceKey, "test-flavor").Obj()
			gomega.Expect(k8sClient.Create(ctx, testFlavor)).Should(gomega.Succeed())

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
				gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
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
					})
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
		gomega.Expect(k8sClient.Create(ctx, defaultFlavor)).Should(gomega.Succeed())
	})

	ginkgo.AfterAll(func() {
		util.ExpectObjectToBeDeleted(ctx, k8sClient, defaultFlavor, true)
		fwk.StopManager(ctx)
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
				Reason:  "PodsReady",
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
				Reason:  "PodsReady",
				Message: "All pods were ready or succeeded since the workload admission",
			},
		}),
		ginkgo.Entry("Running PyTorchJob; PodsReady=False before", kftesting.PodsReadyTestSpec{
			BeforeCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionFalse,
				Reason:  "PodsReady",
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
				Reason:  "PodsReady",
				Message: "All pods were ready or succeeded since the workload admission",
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
				Reason:  "PodsReady",
				Message: "All pods were ready or succeeded since the workload admission",
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
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "core-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

		onDemandFlavor = testing.MakeResourceFlavor("on-demand").NodeLabel(instanceKey, "on-demand").Obj()
		gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).Should(gomega.Succeed())

		spotUntaintedFlavor = testing.MakeResourceFlavor("spot-untainted").NodeLabel(instanceKey, "spot-untainted").Obj()
		gomega.Expect(k8sClient.Create(ctx, spotUntaintedFlavor)).Should(gomega.Succeed())

		clusterQueue = testing.MakeClusterQueue("dev-clusterqueue").
			ResourceGroup(
				*testing.MakeFlavorQuotas("spot-untainted").Resource(corev1.ResourceCPU, "5").Obj(),
				*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj(),
			).Obj()
		gomega.Expect(k8sClient.Create(ctx, clusterQueue)).Should(gomega.Succeed())
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
		gomega.Expect(k8sClient.Create(ctx, localQueue)).Should(gomega.Succeed())

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
				gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
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
				gomega.Expect(k8sClient.Create(ctx, localQueue)).Should(gomega.Succeed())
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
		tasBlockLabel  = "cloud.com/topology-block"
		tasRackLabel   = "cloud.com/topology-rack"
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

		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "tas-pytorchjob-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

		nodes = []corev1.Node{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "b1r1",
					Labels: map[string]string{
						nodeGroupLabel: "tas",
						tasBlockLabel:  "b1",
						tasRackLabel:   "r1",
					},
				},
				Status: corev1.NodeStatus{
					Allocatable: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
					},
					Conditions: []corev1.NodeCondition{
						{
							Type:   corev1.NodeReady,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
		}
		for _, node := range nodes {
			gomega.Expect(k8sClient.Create(ctx, &node)).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Status().Update(ctx, &node)).Should(gomega.Succeed())
		}

		topology = testing.MakeTopology("default").Levels([]string{
			tasBlockLabel, tasRackLabel,
		}).Obj()
		gomega.Expect(k8sClient.Create(ctx, topology)).Should(gomega.Succeed())

		tasFlavor = testing.MakeResourceFlavor("tas-flavor").
			NodeLabel(nodeGroupLabel, "tas").
			TopologyName("default").Obj()
		gomega.Expect(k8sClient.Create(ctx, tasFlavor)).Should(gomega.Succeed())

		clusterQueue = testing.MakeClusterQueue("cluster-queue").
			ResourceGroup(*testing.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj()).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, clusterQueue)).Should(gomega.Succeed())
		util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

		localQueue = testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, localQueue)).Should(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
		gomega.Expect(util.DeleteObject(ctx, k8sClient, topology)).Should(gomega.Succeed())
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
						kueuealpha.PodSetRequiredTopologyAnnotation: tasRackLabel,
					},
				},
				testingpytorchjob.PyTorchReplicaSpecRequirement{
					ReplicaType:  kftraining.PyTorchJobReplicaTypeWorker,
					ReplicaCount: 1,
					Annotations: map[string]string{
						kueuealpha.PodSetPreferredTopologyAnnotation: tasBlockLabel,
					},
				},
			).
			Queue(localQueue.Name).
			Request(kftraining.PyTorchJobReplicaTypeMaster, corev1.ResourceCPU, "100m").
			Request(kftraining.PyTorchJobReplicaTypeWorker, corev1.ResourceCPU, "100m").
			Obj()
		ginkgo.By("creating a PyTorchJob", func() {
			gomega.Expect(k8sClient.Create(ctx, pytorchJob)).Should(gomega.Succeed())
		})

		wl := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadpytorchjob.GetWorkloadNameForPyTorchJob(pytorchJob.Name, pytorchJob.UID), Namespace: ns.Name}

		ginkgo.By("verify the workload is created", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, wl)).Should(gomega.Succeed())
				g.Expect(wl.Spec.PodSets).Should(gomega.BeComparableTo([]kueue.PodSet{
					{
						Name:  strings.ToLower(string(kftraining.PyTorchJobReplicaTypeMaster)),
						Count: 1,
						TopologyRequest: &kueue.PodSetTopologyRequest{
							Required: ptr.To(tasRackLabel),
						},
					},
					{
						Name:  strings.ToLower(string(kftraining.PyTorchJobReplicaTypeWorker)),
						Count: 1,
						TopologyRequest: &kueue.PodSetTopologyRequest{
							Preferred: ptr.To(tasBlockLabel),
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
						Levels:  []string{tasBlockLabel, tasRackLabel},
						Domains: []kueue.TopologyDomainAssignment{{Count: 1, Values: []string{"b1", "r1"}}},
					},
				))
				g.Expect(wl.Status.Admission.PodSetAssignments[1].TopologyAssignment).Should(gomega.BeComparableTo(
					&kueue.TopologyAssignment{
						Levels:  []string{tasBlockLabel, tasRackLabel},
						Domains: []kueue.TopologyDomainAssignment{{Count: 1, Values: []string{"b1", "r1"}}},
					},
				))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
