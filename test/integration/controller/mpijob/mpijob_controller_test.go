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

package mpijob

import (
	"fmt"

	"github.com/google/go-cmp/cmp/cmpopts"
	kubeflow "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/pointer"
	ctrl "sigs.k8s.io/controller-runtime"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	workloadmpijob "sigs.k8s.io/kueue/pkg/controller/jobs/mpijob"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingmpijob "sigs.k8s.io/kueue/pkg/util/testingjobs/mpijob"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

const (
	jobName           = "test-job"
	labelKey          = "cloud.provider.com/instance"
	priorityClassName = "test-priority-class"
	priorityValue     = 10
)

var (
	ignoreConditionTimestamps = cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime")
)

// +kubebuilder:docs-gen:collapse=Imports

var _ = ginkgo.Describe("Job controller", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {

	ginkgo.BeforeAll(func() {
		fwk = &framework.Framework{
			ManagerSetup: managerSetup(jobframework.WithManageJobsWithoutQueueName(true)),
			CRDPath:      crdPath,
			DepCRDPaths:  []string{mpiCrdPath},
		}

		ctx, cfg, k8sClient = fwk.Setup()
	})
	ginkgo.AfterAll(func() {
		fwk.Teardown()
	})

	var (
		ns          *corev1.Namespace
		wlLookupKey types.NamespacedName
	)
	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "core-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
		wlLookupKey = types.NamespacedName{Name: workloadmpijob.GetWorkloadNameForMPIJob(jobName), Namespace: ns.Name}
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.It("Should reconcile MPIJobs", func() {
		ginkgo.By("checking the job gets suspended when created unsuspended")
		priorityClass := testing.MakePriorityClass(priorityClassName).
			PriorityValue(int32(priorityValue)).Obj()
		gomega.Expect(k8sClient.Create(ctx, priorityClass)).Should(gomega.Succeed())

		job := testingmpijob.MakeMPIJob(jobName, ns.Name).PriorityClass(priorityClassName).Obj()
		err := k8sClient.Create(ctx, job)
		gomega.Expect(err).To(gomega.Succeed())
		createdJob := &kubeflow.MPIJob{}

		gomega.Eventually(func() bool {
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: jobName, Namespace: ns.Name}, createdJob); err != nil {
				return false
			}
			return createdJob.Spec.RunPolicy.Suspend != nil && *createdJob.Spec.RunPolicy.Suspend
		}, util.Timeout, util.Interval).Should(gomega.BeTrue())

		ginkgo.By("checking the workload is created without queue assigned")
		createdWorkload := &kueue.Workload{}
		gomega.Eventually(func() error {
			return k8sClient.Get(ctx, wlLookupKey, createdWorkload)
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		gomega.Expect(createdWorkload.Spec.QueueName).Should(gomega.Equal(""), "The Workload shouldn't have .spec.queueName set")
		gomega.Expect(metav1.IsControlledBy(createdWorkload, createdJob)).To(gomega.BeTrue(), "The Workload should be owned by the Job")

		ginkgo.By("checking the workload is created with priority and priorityName")
		gomega.Expect(createdWorkload.Spec.PriorityClassName).Should(gomega.Equal(priorityClassName))
		gomega.Expect(*createdWorkload.Spec.Priority).Should(gomega.Equal(int32(priorityValue)))

		ginkgo.By("checking the workload is updated with queue name when the job does")
		jobQueueName := "test-queue"
		createdJob.Annotations = map[string]string{jobframework.QueueAnnotation: jobQueueName}
		gomega.Expect(k8sClient.Update(ctx, createdJob)).Should(gomega.Succeed())
		gomega.Eventually(func() bool {
			if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
				return false
			}
			return createdWorkload.Spec.QueueName == jobQueueName
		}, util.Timeout, util.Interval).Should(gomega.BeTrue())

		ginkgo.By("checking a second non-matching workload is deleted")
		secondWl := &kueue.Workload{
			ObjectMeta: metav1.ObjectMeta{
				Name:      workloadmpijob.GetWorkloadNameForMPIJob("second-workload"),
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
		gomega.Consistently(func() bool {
			err := k8sClient.Get(ctx, wlLookupKey, createdWorkload)
			return err == nil
		}, util.ConsistentDuration, util.Interval).Should(gomega.BeTrue())

		ginkgo.By("checking the job is unsuspended when workload is assigned")
		onDemandFlavor := testing.MakeResourceFlavor("on-demand").Label(labelKey, "on-demand").Obj()
		gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).Should(gomega.Succeed())
		spotFlavor := testing.MakeResourceFlavor("spot").Label(labelKey, "spot").Obj()
		gomega.Expect(k8sClient.Create(ctx, spotFlavor)).Should(gomega.Succeed())
		clusterQueue := testing.MakeClusterQueue("cluster-queue").
			ResourceGroup(
				*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj(),
				*testing.MakeFlavorQuotas("spot").Resource(corev1.ResourceCPU, "5").Obj(),
			).Obj()
		admission := testing.MakeAdmission(clusterQueue.Name).
			PodSets(
				kueue.PodSetAssignment{
					Name: "Launcher",
					Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
						corev1.ResourceCPU: "on-demand",
					},
					Count: pointer.Int32(createdWorkload.Spec.PodSets[0].Count),
				},
				kueue.PodSetAssignment{
					Name: "Worker",
					Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
						corev1.ResourceCPU: "spot",
					},
					Count: pointer.Int32(createdWorkload.Spec.PodSets[1].Count),
				},
			).
			Obj()
		gomega.Expect(util.SetAdmission(ctx, k8sClient, createdWorkload, admission)).Should(gomega.Succeed())
		lookupKey := types.NamespacedName{Name: jobName, Namespace: ns.Name}
		gomega.Eventually(func() bool {
			if err := k8sClient.Get(ctx, lookupKey, createdJob); err != nil {
				return false
			}
			return !*createdJob.Spec.RunPolicy.Suspend
		}, util.Timeout, util.Interval).Should(gomega.BeTrue())
		gomega.Eventually(func() bool {
			ok, _ := testing.CheckLatestEvent(ctx, k8sClient, "Started", corev1.EventTypeNormal, fmt.Sprintf("Admitted by clusterQueue %v", clusterQueue.Name))
			return ok
		}, util.Timeout, util.Interval).Should(gomega.BeTrue())
		gomega.Expect(len(createdJob.Spec.MPIReplicaSpecs[kubeflow.MPIReplicaTypeLauncher].Template.Spec.NodeSelector)).Should(gomega.Equal(1))
		gomega.Expect(createdJob.Spec.MPIReplicaSpecs[kubeflow.MPIReplicaTypeLauncher].Template.Spec.NodeSelector[labelKey]).Should(gomega.Equal(onDemandFlavor.Name))
		gomega.Expect(len(createdJob.Spec.MPIReplicaSpecs[kubeflow.MPIReplicaTypeWorker].Template.Spec.NodeSelector)).Should(gomega.Equal(1))
		gomega.Expect(createdJob.Spec.MPIReplicaSpecs[kubeflow.MPIReplicaTypeWorker].Template.Spec.NodeSelector[labelKey]).Should(gomega.Equal(spotFlavor.Name))
		gomega.Consistently(func() bool {
			if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
				return false
			}
			return len(createdWorkload.Status.Conditions) == 1
		}, util.ConsistentDuration, util.Interval).Should(gomega.BeTrue())

		ginkgo.By("checking the job gets suspended when parallelism changes and the added node selectors are removed")
		parallelism := pointer.Int32Deref(job.Spec.MPIReplicaSpecs[kubeflow.MPIReplicaTypeWorker].Replicas, 1)
		newParallelism := int32(parallelism + 1)
		createdJob.Spec.MPIReplicaSpecs[kubeflow.MPIReplicaTypeWorker].Replicas = &newParallelism
		gomega.Expect(k8sClient.Update(ctx, createdJob)).Should(gomega.Succeed())
		gomega.Eventually(func() bool {
			if err := k8sClient.Get(ctx, lookupKey, createdJob); err != nil {
				return false
			}
			return createdJob.Spec.RunPolicy.Suspend != nil && *createdJob.Spec.RunPolicy.Suspend &&
				len(createdJob.Spec.MPIReplicaSpecs[kubeflow.MPIReplicaTypeWorker].Template.Spec.NodeSelector) == 0
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
			return createdWorkload.Spec.PodSets[1].Count == newParallelism
		}, util.Timeout, util.Interval).Should(gomega.BeTrue())
		gomega.Expect(createdWorkload.Status.Admission).Should(gomega.BeNil())

		ginkgo.By("checking the job is unsuspended and selectors added when workload is assigned again")
		admission = testing.MakeAdmission(clusterQueue.Name).
			PodSets(
				kueue.PodSetAssignment{
					Name: "Launcher",
					Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
						corev1.ResourceCPU: "on-demand",
					},
					Count: pointer.Int32(createdWorkload.Spec.PodSets[0].Count),
				},
				kueue.PodSetAssignment{
					Name: "Worker",
					Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
						corev1.ResourceCPU: "spot",
					},
					Count: pointer.Int32(createdWorkload.Spec.PodSets[1].Count),
				},
			).
			Obj()
		gomega.Expect(util.SetAdmission(ctx, k8sClient, createdWorkload, admission)).Should(gomega.Succeed())
		gomega.Eventually(func() bool {
			if err := k8sClient.Get(ctx, lookupKey, createdJob); err != nil {
				return false
			}
			return !*createdJob.Spec.RunPolicy.Suspend
		}, util.Timeout, util.Interval).Should(gomega.BeTrue())
		gomega.Expect(len(createdJob.Spec.MPIReplicaSpecs[kubeflow.MPIReplicaTypeLauncher].Template.Spec.NodeSelector)).Should(gomega.Equal(1))
		gomega.Expect(createdJob.Spec.MPIReplicaSpecs[kubeflow.MPIReplicaTypeLauncher].Template.Spec.NodeSelector[labelKey]).Should(gomega.Equal(onDemandFlavor.Name))
		gomega.Expect(len(createdJob.Spec.MPIReplicaSpecs[kubeflow.MPIReplicaTypeWorker].Template.Spec.NodeSelector)).Should(gomega.Equal(1))
		gomega.Expect(createdJob.Spec.MPIReplicaSpecs[kubeflow.MPIReplicaTypeWorker].Template.Spec.NodeSelector[labelKey]).Should(gomega.Equal(spotFlavor.Name))
		gomega.Consistently(func() bool {
			if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
				return false
			}
			return len(createdWorkload.Status.Conditions) == 1
		}, util.ConsistentDuration, util.Interval).Should(gomega.BeTrue())

		ginkgo.By("checking the workload is finished when job is completed")
		createdJob.Status.Conditions = append(createdJob.Status.Conditions,
			kubeflow.JobCondition{
				Type:               kubeflow.JobSucceeded,
				Status:             corev1.ConditionTrue,
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
})

var _ = ginkgo.Describe("Job controller for workloads when only jobs with queue are managed", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	ginkgo.BeforeAll(func() {
		fwk = &framework.Framework{
			ManagerSetup: managerSetup(),
			CRDPath:      crdPath,
			DepCRDPaths:  []string{mpiCrdPath},
		}
		ctx, cfg, k8sClient = fwk.Setup()
	})
	ginkgo.AfterAll(func() {
		fwk.Teardown()
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
		job := testingmpijob.MakeMPIJob(jobName, ns.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
		lookupKey := types.NamespacedName{Name: jobName, Namespace: ns.Name}
		createdJob := &kubeflow.MPIJob{}
		gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())

		createdWorkload := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadmpijob.GetWorkloadNameForMPIJob(jobName), Namespace: ns.Name}
		gomega.Consistently(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, wlLookupKey, createdWorkload))
		}, util.ConsistentDuration, util.Interval).Should(gomega.BeTrue())

		ginkgo.By("checking the workload is created when queue name is set")
		jobQueueName := "test-queue"
		createdJob.Annotations = map[string]string{jobframework.QueueAnnotation: jobQueueName}
		gomega.Expect(k8sClient.Update(ctx, createdJob)).Should(gomega.Succeed())
		gomega.Eventually(func() error {
			return k8sClient.Get(ctx, wlLookupKey, createdWorkload)
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

})

var _ = ginkgo.Describe("Job controller when waitForPodsReady enabled", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	type podsReadyTestSpec struct {
		beforeJobStatus *kubeflow.JobStatus
		beforeCondition *metav1.Condition
		jobStatus       kubeflow.JobStatus
		suspended       bool
		wantCondition   *metav1.Condition
	}

	var (
		ns            *corev1.Namespace
		wlLookupKey   types.NamespacedName
		defaultFlavor = testing.MakeResourceFlavor("default").Label(labelKey, "default").Obj()
	)

	ginkgo.BeforeAll(func() {
		fwk = &framework.Framework{
			ManagerSetup: managerSetup(jobframework.WithWaitForPodsReady(true)),
			CRDPath:      crdPath,
			DepCRDPaths:  []string{mpiCrdPath},
		}
		ctx, cfg, k8sClient = fwk.Setup()

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
		wlLookupKey = types.NamespacedName{Name: workloadmpijob.GetWorkloadNameForMPIJob(jobName), Namespace: ns.Name}
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.DescribeTable("Single job at different stages of progress towards completion",
		func(podsReadyTestSpec podsReadyTestSpec) {
			ginkgo.By("Create a job")
			job := testingmpijob.MakeMPIJob(jobName, ns.Name).Parallelism(2).Obj()
			jobQueueName := "test-queue"
			job.Annotations = map[string]string{jobframework.QueueAnnotation: jobQueueName}
			gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
			lookupKey := types.NamespacedName{Name: jobName, Namespace: ns.Name}
			createdJob := &kubeflow.MPIJob{}
			gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())

			ginkgo.By("Fetch the workload created for the job")
			createdWorkload := &kueue.Workload{}
			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, wlLookupKey, createdWorkload)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Admit the workload created for the job")
			admission := testing.MakeAdmission("foo").
				PodSets(
					kueue.PodSetAssignment{
						Name: "Launcher",
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: "default",
						},
						Count: pointer.Int32(createdWorkload.Spec.PodSets[0].Count),
					},
					kueue.PodSetAssignment{
						Name: "Worker",
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: "default",
						},
						Count: pointer.Int32(createdWorkload.Spec.PodSets[1].Count),
					},
				).
				Obj()
			gomega.Expect(util.SetAdmission(ctx, k8sClient, createdWorkload, admission)).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())

			ginkgo.By("Await for the job to be unsuspended")
			gomega.Eventually(func() *bool {
				gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
				return createdJob.Spec.RunPolicy.Suspend
			}, util.Timeout, util.Interval).Should(gomega.Equal(pointer.Bool(false)))

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
				}, util.Timeout, util.Interval).Should(gomega.BeComparableTo(podsReadyTestSpec.beforeCondition, ignoreConditionTimestamps))
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
					return util.SetAdmission(ctx, k8sClient, createdWorkload, nil)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			}

			ginkgo.By("Verify the PodsReady condition is added")
			gomega.Eventually(func() *metav1.Condition {
				gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				return apimeta.FindStatusCondition(createdWorkload.Status.Conditions, kueue.WorkloadPodsReady)
			}, util.Timeout, util.Interval).Should(gomega.BeComparableTo(podsReadyTestSpec.wantCondition, ignoreConditionTimestamps))
		},
		ginkgo.Entry("No progress", podsReadyTestSpec{
			wantCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionFalse,
				Reason:  "PodsReady",
				Message: "Not all pods are ready or succeeded",
			},
		}),
		ginkgo.Entry("Running MPIJob", podsReadyTestSpec{
			jobStatus: kubeflow.JobStatus{
				Conditions: []kubeflow.JobCondition{
					{
						Type:   kubeflow.JobRunning,
						Status: corev1.ConditionTrue,
						Reason: "Running",
					},
				},
			},
			wantCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionTrue,
				Reason:  "PodsReady",
				Message: "All pods were ready or succeeded since the workload admission",
			},
		}),
		ginkgo.Entry("Running MPIJob; PodsReady=False before", podsReadyTestSpec{
			beforeCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionFalse,
				Reason:  "PodsReady",
				Message: "Not all pods are ready or succeeded",
			},
			jobStatus: kubeflow.JobStatus{
				Conditions: []kubeflow.JobCondition{
					{
						Type:   kubeflow.JobRunning,
						Status: corev1.ConditionTrue,
						Reason: "Running",
					},
				},
			},
			wantCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionTrue,
				Reason:  "PodsReady",
				Message: "All pods were ready or succeeded since the workload admission",
			},
		}),
		ginkgo.Entry("Job suspended; PodsReady=True before", podsReadyTestSpec{
			beforeJobStatus: &kubeflow.JobStatus{
				Conditions: []kubeflow.JobCondition{
					{
						Type:   kubeflow.JobRunning,
						Status: corev1.ConditionTrue,
						Reason: "Running",
					},
				},
			},
			beforeCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionTrue,
				Reason:  "PodsReady",
				Message: "All pods were ready or succeeded since the workload admission",
			},
			jobStatus: kubeflow.JobStatus{
				Conditions: []kubeflow.JobCondition{
					{
						Type:   kubeflow.JobRunning,
						Status: corev1.ConditionFalse,
						Reason: "Suspended",
					},
				},
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
	const (
		instanceKey = "cloud.provider.com/instance"
	)

	var (
		ns                  *corev1.Namespace
		onDemandFlavor      *kueue.ResourceFlavor
		spotUntaintedFlavor *kueue.ResourceFlavor
		clusterQueue        *kueue.ClusterQueue
		localQueue          *kueue.LocalQueue
	)

	ginkgo.BeforeAll(func() {
		fwk = &framework.Framework{
			ManagerSetup: managerAndSchedulerSetup(),
			CRDPath:      crdPath,
			DepCRDPaths:  []string{mpiCrdPath},
		}
		ctx, cfg, k8sClient = fwk.Setup()
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

		spotUntaintedFlavor = testing.MakeResourceFlavor("spot-untainted").Label(instanceKey, "spot-untainted").Obj()
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
		util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		gomega.Expect(util.DeleteResourceFlavor(ctx, k8sClient, spotUntaintedFlavor)).To(gomega.Succeed())
	})

	ginkgo.It("Should schedule jobs as they fit in their ClusterQueue", func() {
		ginkgo.By("creating localQueue")
		localQueue = testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, localQueue)).Should(gomega.Succeed())

		ginkgo.By("checking a dev job starts")
		job := testingmpijob.MakeMPIJob("dev-job", ns.Name).Queue(localQueue.Name).
			Request(kubeflow.MPIReplicaTypeLauncher, corev1.ResourceCPU, "3").
			Request(kubeflow.MPIReplicaTypeWorker, corev1.ResourceCPU, "4").
			Obj()
		gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
		createdJob := &kubeflow.MPIJob{}
		gomega.Eventually(func() *bool {
			gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: job.Name, Namespace: job.Namespace}, createdJob)).
				Should(gomega.Succeed())
			return createdJob.Spec.RunPolicy.Suspend
		}, util.Timeout, util.Interval).Should(gomega.Equal(pointer.Bool(false)))
		gomega.Expect(createdJob.Spec.MPIReplicaSpecs[kubeflow.MPIReplicaTypeLauncher].Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(spotUntaintedFlavor.Name))
		gomega.Expect(createdJob.Spec.MPIReplicaSpecs[kubeflow.MPIReplicaTypeWorker].Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))
		util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
		util.ExpectAdmittedActiveWorkloadsMetric(clusterQueue, 1)

	})

	ginkgo.When("The workload is deleted while it's admitted", func() {
		ginkgo.It("Should restore the original node selectors", func() {

			localQueue := testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			job := testingmpijob.MakeMPIJob(jobName, ns.Name).Queue(localQueue.Name).
				Request(kubeflow.MPIReplicaTypeLauncher, corev1.ResourceCPU, "3").
				Request(kubeflow.MPIReplicaTypeWorker, corev1.ResourceCPU, "4").
				Obj()
			lookupKey := types.NamespacedName{Name: job.Name, Namespace: job.Namespace}
			createdJob := &kubeflow.MPIJob{}

			nodeSelectors := func(j *kubeflow.MPIJob) map[kubeflow.MPIReplicaType]map[string]string {
				ret := map[kubeflow.MPIReplicaType]map[string]string{}
				for k := range j.Spec.MPIReplicaSpecs {
					ret[k] = j.Spec.MPIReplicaSpecs[k].Template.Spec.NodeSelector
				}
				return ret
			}

			ginkgo.By("create a job", func() {
				gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
			})

			ginkgo.By("job should be suspend", func() {
				gomega.Eventually(func() *bool {
					gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
					return createdJob.Spec.RunPolicy.Suspend
				}, util.Timeout, util.Interval).Should(gomega.Equal(pointer.Bool(true)))
			})

			// backup the the node selectors
			originalNodeSelectors := nodeSelectors(createdJob)

			ginkgo.By("create a localQueue", func() {
				gomega.Expect(k8sClient.Create(ctx, localQueue)).Should(gomega.Succeed())
			})

			ginkgo.By("job should be unsuspended", func() {
				gomega.Eventually(func() *bool {
					gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
					return createdJob.Spec.RunPolicy.Suspend
				}, util.Timeout, util.Interval).Should(gomega.Equal(pointer.Bool(false)))
			})

			ginkgo.By("the node selectors should be updated", func() {
				gomega.Eventually(func() map[kubeflow.MPIReplicaType]map[string]string {
					gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
					return nodeSelectors(createdJob)
				}, util.Timeout, util.Interval).ShouldNot(gomega.Equal(originalNodeSelectors))
			})

			ginkgo.By("delete the localQueue to prevent readmission", func() {
				gomega.Expect(util.DeleteLocalQueue(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
			})

			ginkgo.By("delete the workload to stop the job", func() {
				wl := &kueue.Workload{}
				wlKey := types.NamespacedName{Name: workloadmpijob.GetWorkloadNameForMPIJob(job.Name), Namespace: job.Namespace}
				gomega.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
				gomega.Expect(util.DeleteWorkload(ctx, k8sClient, wl)).Should(gomega.Succeed())
			})

			ginkgo.By("the node selectors should be restored", func() {
				gomega.Eventually(func() map[kubeflow.MPIReplicaType]map[string]string {
					gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
					return nodeSelectors(createdJob)
				}, util.Timeout, util.Interval).Should(gomega.Equal(originalNodeSelectors))
			})
		})
	})
})
