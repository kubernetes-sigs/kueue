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

package raycluster

import (
	"fmt"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	workloadraycluster "sigs.k8s.io/kueue/pkg/controller/jobs/raycluster"
	_ "sigs.k8s.io/kueue/pkg/controller/jobs/rayjob" // to enable the framework
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingraycluster "sigs.k8s.io/kueue/pkg/util/testingjobs/raycluster"
	testingrayjob "sigs.k8s.io/kueue/pkg/util/testingjobs/rayjob"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

const (
	jobName                 = "test-job"
	instanceKey             = "cloud.provider.com/instance"
	priorityClassName       = "test-priority-class"
	priorityValue     int32 = 10
)

var (
	ignoreConditionTimestamps = cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime")
)

// +kubebuilder:docs-gen:collapse=Imports

var _ = ginkgo.Describe("RayCluster controller", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	ginkgo.BeforeAll(func() {
		fwk = &framework.Framework{
			CRDPath:     crdPath,
			DepCRDPaths: []string{rayCrdPath},
		}

		cfg = fwk.Init()
		ctx, k8sClient = fwk.RunManager(cfg, managerSetup(jobframework.WithManageJobsWithoutQueueName(true)))
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

	ginkgo.It("Should reconcile RayClusters", func() {
		ginkgo.By("checking the job gets suspended when created unsuspended")
		priorityClass := testing.MakePriorityClass(priorityClassName).
			PriorityValue(priorityValue).Obj()
		gomega.Expect(k8sClient.Create(ctx, priorityClass)).Should(gomega.Succeed())

		job := testingraycluster.MakeCluster(jobName, ns.Name).
			Suspend(false).
			WithPriorityClassName(priorityClassName).
			Obj()
		err := k8sClient.Create(ctx, job)
		gomega.Expect(err).To(gomega.Succeed())
		createdJob := &rayv1.RayCluster{}

		gomega.Eventually(func() bool {
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: jobName, Namespace: ns.Name}, createdJob); err != nil {
				return false
			}
			return *createdJob.Spec.Suspend
		}, util.Timeout, util.Interval).Should(gomega.BeTrue())

		ginkgo.By("checking the workload is created without queue assigned")
		createdWorkload := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadraycluster.GetWorkloadNameForRayCluster(job.Name, job.UID), Namespace: ns.Name}
		gomega.Eventually(func() error {
			return k8sClient.Get(ctx, wlLookupKey, createdWorkload)
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		gomega.Expect(createdWorkload.Spec.QueueName).Should(gomega.Equal(""), "The Workload shouldn't have .spec.queueName set")
		gomega.Expect(metav1.IsControlledBy(createdWorkload, createdJob)).To(gomega.BeTrue(), "The Workload should be owned by the Job")

		ginkgo.By("checking the workload is created with priority and priorityName")
		gomega.Expect(createdWorkload.Spec.PriorityClassName).Should(gomega.Equal(priorityClassName))
		gomega.Expect(*createdWorkload.Spec.Priority).Should(gomega.Equal(priorityValue))

		ginkgo.By("checking the workload is updated with queue name when the job does")
		jobQueueName := "test-queue"
		createdJob.Annotations = map[string]string{constants.QueueAnnotation: jobQueueName}
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
				Name:      workloadraycluster.GetWorkloadNameForRayCluster("second-workload", "test-uid"),
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
		gomega.Eventually(func() error {
			return k8sClient.Get(ctx, wlLookupKey, createdWorkload)
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

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
		admission := testing.MakeAdmission(clusterQueue.Name).PodSets(
			kueue.PodSetAssignment{
				Name: createdWorkload.Spec.PodSets[0].Name,
				Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
					corev1.ResourceCPU: "on-demand",
				},
			}, kueue.PodSetAssignment{
				Name: createdWorkload.Spec.PodSets[1].Name,
				Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
					corev1.ResourceCPU: "spot",
				},
			},
		).Obj()
		gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, admission)).Should(gomega.Succeed())
		util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)

		lookupKey := types.NamespacedName{Name: jobName, Namespace: ns.Name}
		gomega.Eventually(func() bool {
			if err := k8sClient.Get(ctx, lookupKey, createdJob); err != nil {
				return false
			}
			return !*createdJob.Spec.Suspend
		}, util.Timeout, util.Interval).Should(gomega.BeTrue())

		gomega.Eventually(func() bool {
			ok, _ := testing.CheckLatestEvent(ctx, k8sClient, "Started", corev1.EventTypeNormal, fmt.Sprintf("Admitted by clusterQueue %v", clusterQueue.Name))
			return ok
		}, util.Timeout, util.Interval).Should(gomega.BeTrue())
		gomega.Expect(len(createdJob.Spec.HeadGroupSpec.Template.Spec.NodeSelector)).Should(gomega.Equal(1))
		gomega.Expect(createdJob.Spec.HeadGroupSpec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))
		gomega.Expect(len(createdJob.Spec.WorkerGroupSpecs[0].Template.Spec.NodeSelector)).Should(gomega.Equal(1))
		gomega.Expect(createdJob.Spec.WorkerGroupSpecs[0].Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(spotFlavor.Name))
		gomega.Eventually(func() bool {
			if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
				return false
			}
			return apimeta.IsStatusConditionTrue(createdWorkload.Status.Conditions, kueue.WorkloadQuotaReserved)
		}, util.Timeout, util.Interval).Should(gomega.BeTrue())

		ginkgo.By("checking the job gets suspended when parallelism changes and the added node selectors are removed")
		parallelism := ptr.Deref(job.Spec.WorkerGroupSpecs[0].Replicas, 1)
		newParallelism := int32(parallelism + 1)
		createdJob.Spec.WorkerGroupSpecs[0].Replicas = &newParallelism
		gomega.Expect(k8sClient.Update(ctx, createdJob)).Should(gomega.Succeed())
		gomega.Eventually(func() bool {
			if err := k8sClient.Get(ctx, lookupKey, createdJob); err != nil {
				return false
			}
			return *createdJob.Spec.Suspend && len(createdJob.Spec.WorkerGroupSpecs[0].Template.Spec.NodeSelector) == 0
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
		gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, admission)).Should(gomega.Succeed())
		util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
		gomega.Eventually(func() bool {
			if err := k8sClient.Get(ctx, lookupKey, createdJob); err != nil {
				return false
			}
			return !*createdJob.Spec.Suspend
		}, util.Timeout, util.Interval).Should(gomega.BeTrue())
		gomega.Expect(len(createdJob.Spec.HeadGroupSpec.Template.Spec.NodeSelector)).Should(gomega.Equal(1))
		gomega.Expect(createdJob.Spec.HeadGroupSpec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))
		gomega.Expect(len(createdJob.Spec.WorkerGroupSpecs[0].Template.Spec.NodeSelector)).Should(gomega.Equal(1))
		gomega.Expect(createdJob.Spec.WorkerGroupSpecs[0].Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(spotFlavor.Name))
		gomega.Eventually(func() bool {
			if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
				return false
			}
			return apimeta.IsStatusConditionTrue(createdWorkload.Status.Conditions, kueue.WorkloadQuotaReserved)
		}, util.Timeout, util.Interval).Should(gomega.BeTrue())
	})
})

var _ = ginkgo.Describe("Job controller RayCluster for workloads when only jobs with queue are managed", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	ginkgo.BeforeAll(func() {
		fwk = &framework.Framework{
			CRDPath:     crdPath,
			DepCRDPaths: []string{rayCrdPath},
		}
		cfg = fwk.Init()
		ctx, k8sClient = fwk.RunManager(cfg, managerSetup())
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
		job := testingraycluster.MakeCluster(jobName, ns.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
		lookupKey := types.NamespacedName{Name: jobName, Namespace: ns.Name}
		createdJob := &rayv1.RayCluster{}
		gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())

		createdWorkload := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadraycluster.GetWorkloadNameForRayCluster(job.Name, job.UID), Namespace: ns.Name}
		gomega.Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, wlLookupKey, createdWorkload))
		}, util.Timeout, util.Interval).Should(gomega.BeTrue())

		ginkgo.By("checking the workload is created when queue name is set")
		jobQueueName := "test-queue"
		if createdJob.Labels == nil {
			createdJob.Labels = map[string]string{constants.QueueAnnotation: jobQueueName}
		} else {
			createdJob.Labels[constants.QueueLabel] = jobQueueName
		}
		gomega.Expect(k8sClient.Update(ctx, createdJob)).Should(gomega.Succeed())
		gomega.Eventually(func() error {
			return k8sClient.Get(ctx, wlLookupKey, createdWorkload)
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("Should suspend a cluster if the parent's workload does not exist or is not admitted", func() {
		ginkgo.By("Creating the parent job which has a queue name")
		parentJob := testingrayjob.MakeJob("parent-job", ns.Name).
			Queue("test").
			Suspend(false).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, parentJob)).Should(gomega.Succeed())

		ginkgo.By("Creating the child cluster.")
		childCluster := testingraycluster.MakeCluster(jobName, ns.Name).
			Suspend(false).
			Obj()
		gomega.Expect(ctrl.SetControllerReference(parentJob, childCluster, k8sClient.Scheme())).To(gomega.Succeed())
		gomega.Expect(k8sClient.Create(ctx, childCluster)).Should(gomega.Succeed())

		childClusterKey := client.ObjectKeyFromObject(childCluster)
		ginkgo.By("checking that the child cluster is suspended")
		gomega.Eventually(func() *bool {
			gomega.Expect(k8sClient.Get(ctx, childClusterKey, childCluster)).Should(gomega.Succeed())
			return childCluster.Spec.Suspend
		}, util.Timeout, util.Interval).Should(gomega.Equal(ptr.To(true)))
	})

})

var _ = ginkgo.Describe("Job controller when waitForPodsReady enabled", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	type podsReadyTestSpec struct {
		beforeJobStatus *rayv1.RayClusterStatus
		beforeCondition *metav1.Condition
		jobStatus       rayv1.RayClusterStatus
		suspended       bool
		wantCondition   *metav1.Condition
	}

	var defaultFlavor = testing.MakeResourceFlavor("default").Label(instanceKey, "default").Obj()

	ginkgo.BeforeAll(func() {
		fwk = &framework.Framework{
			CRDPath:     crdPath,
			DepCRDPaths: []string{rayCrdPath},
		}
		cfg = fwk.Init()
		ctx, k8sClient = fwk.RunManager(cfg, managerSetup(jobframework.WithWaitForPodsReady(&configapi.WaitForPodsReady{Enable: true})))

		ginkgo.By("Create a resource flavor")
		gomega.Expect(k8sClient.Create(ctx, defaultFlavor)).Should(gomega.Succeed())
	})

	ginkgo.AfterAll(func() {
		util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, defaultFlavor, true)
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

	ginkgo.DescribeTable("Single job at different stages of progress towards completion",
		func(podsReadyTestSpec podsReadyTestSpec) {
			ginkgo.By("Create a job")
			job := testingraycluster.MakeCluster(jobName, ns.Name).Obj()
			jobQueueName := "test-queue"
			job.Annotations = map[string]string{constants.QueueAnnotation: jobQueueName}
			gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
			lookupKey := types.NamespacedName{Name: jobName, Namespace: ns.Name}
			createdJob := &rayv1.RayCluster{}
			gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())

			ginkgo.By("Fetch the workload created for the job")
			createdWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: workloadraycluster.GetWorkloadNameForRayCluster(job.Name, job.UID), Namespace: ns.Name}
			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, wlLookupKey, createdWorkload)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Admit the workload created for the job")
			admission := testing.MakeAdmission("foo").PodSets(
				kueue.PodSetAssignment{
					Name: createdWorkload.Spec.PodSets[0].Name,
					Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
						corev1.ResourceCPU: "default",
					},
				}, kueue.PodSetAssignment{
					Name: createdWorkload.Spec.PodSets[1].Name,
					Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
						corev1.ResourceCPU: "default",
					},
				},
			).Obj()
			gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, admission)).Should(gomega.Succeed())
			util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
			gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())

			ginkgo.By("Await for the job to be unsuspended")
			gomega.Eventually(func() bool {
				gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
				return *createdJob.Spec.Suspend
			}, util.Timeout, util.Interval).Should(gomega.BeFalse())

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
					return util.SetQuotaReservation(ctx, k8sClient, createdWorkload, nil)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
				util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
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
		ginkgo.Entry("Running RayCluster", podsReadyTestSpec{
			jobStatus: rayv1.RayClusterStatus{
				State: rayv1.Ready,
			},
			wantCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionTrue,
				Reason:  "PodsReady",
				Message: "All pods were ready or succeeded since the workload admission",
			},
		}),

		ginkgo.Entry("Running RayCluster; PodsReady=False before", podsReadyTestSpec{
			beforeCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionFalse,
				Reason:  "PodsReady",
				Message: "Not all pods are ready or succeeded",
			},
			jobStatus: rayv1.RayClusterStatus{

				State: rayv1.Ready,
			},
			wantCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionTrue,
				Reason:  "PodsReady",
				Message: "All pods were ready or succeeded since the workload admission",
			},
		}),
		ginkgo.Entry("Job suspended; PodsReady=True before", podsReadyTestSpec{
			beforeJobStatus: &rayv1.RayClusterStatus{
				State: rayv1.Ready,
			},
			beforeCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionTrue,
				Reason:  "PodsReady",
				Message: "All pods were ready or succeeded since the workload admission",
			},
			jobStatus: rayv1.RayClusterStatus{
				State: rayv1.Ready,
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

var _ = ginkgo.Describe("RayCluster Job controller interacting with scheduler", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	ginkgo.BeforeAll(func() {
		fwk = &framework.Framework{
			CRDPath:     crdPath,
			DepCRDPaths: []string{rayCrdPath},
		}
		cfg = fwk.Init()
		ctx, k8sClient = fwk.RunManager(cfg, managerAndSchedulerSetup())
	})
	ginkgo.AfterAll(func() {
		fwk.Teardown()
	})

	var (
		ns                  *corev1.Namespace
		onDemandFlavor      *kueue.ResourceFlavor
		spotUntaintedFlavor *kueue.ResourceFlavor
		clusterQueue        *kueue.ClusterQueue
		localQueue          *kueue.LocalQueue
	)

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
				*testing.MakeFlavorQuotas("spot-untainted").Resource(corev1.ResourceCPU, "4").Obj(),
				*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "4").Obj(),
			).Obj()
		gomega.Expect(k8sClient.Create(ctx, clusterQueue)).Should(gomega.Succeed())
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, spotUntaintedFlavor, true)
	})

	ginkgo.It("Should schedule jobs as they fit in their ClusterQueue", func() {
		ginkgo.By("creating localQueue")
		localQueue = testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, localQueue)).Should(gomega.Succeed())

		ginkgo.By("checking a dev job starts")
		job := testingraycluster.MakeCluster("dev-job", ns.Name).Queue(localQueue.Name).
			RequestHead(corev1.ResourceCPU, "3").
			RequestWorkerGroup(corev1.ResourceCPU, "4").
			Obj()
		gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
		createdJob := &rayv1.RayCluster{}
		gomega.Eventually(func() bool {
			gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: job.Name, Namespace: job.Namespace}, createdJob)).
				Should(gomega.Succeed())
			return *createdJob.Spec.Suspend
		}, util.Timeout, util.Interval).Should(gomega.BeFalse())
		gomega.Expect(createdJob.Spec.HeadGroupSpec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(spotUntaintedFlavor.Name))
		gomega.Expect(createdJob.Spec.WorkerGroupSpecs[0].Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))
		util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
		util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)

		ginkgo.By("checking a second no-fit RayCluster does not start")
		job2 := testingraycluster.MakeCluster("dev-job2", ns.Name).Queue(localQueue.Name).
			RequestHead(corev1.ResourceCPU, "2").
			RequestWorkerGroup(corev1.ResourceCPU, "2").
			Obj()
		gomega.Expect(k8sClient.Create(ctx, job2)).Should(gomega.Succeed())
		createdJob2 := &rayv1.RayCluster{}
		gomega.Eventually(func() bool {
			gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: job2.Name, Namespace: job2.Namespace}, createdJob2)).
				Should(gomega.Succeed())
			return *createdJob2.Spec.Suspend
		}, util.Timeout, util.Interval).Should(gomega.BeTrue())
		util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
		util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)

		ginkgo.By("deleting the job", func() {
			gomega.Expect(k8sClient.Delete(ctx, job)).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: job.Name, Namespace: job.Namespace}, job)
			}, util.Timeout, util.Interval).Should(testing.BeNotFoundError())
		})

		// Users should not have to delete the workload
		// This is usually done by the garbage collector, but there is no garbage collection in integration test
		ginkgo.By("deleting the workload", func() {
			wl := &kueue.Workload{}
			wlKey := types.NamespacedName{Name: workloadraycluster.GetWorkloadNameForRayCluster(job.Name, job.UID), Namespace: job.Namespace}
			gomega.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Delete(ctx, wl)).Should(gomega.Succeed())
		})

		ginkgo.By("checking the second RayCluster starts when the first one was deleted")
		gomega.Eventually(func() bool {
			gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: job2.Name, Namespace: job2.Namespace}, createdJob2)).
				Should(gomega.Succeed())
			return *createdJob2.Spec.Suspend
		}, util.Timeout, util.Interval).Should(gomega.BeFalse())
		gomega.Expect(createdJob2.Spec.HeadGroupSpec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(spotUntaintedFlavor.Name))
		gomega.Expect(createdJob2.Spec.WorkerGroupSpecs[0].Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(spotUntaintedFlavor.Name))
		util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
		util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
	})
})

var _ = ginkgo.Describe("Job controller with preemption enabled", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	ginkgo.BeforeAll(func() {
		fwk = &framework.Framework{
			CRDPath:     crdPath,
			DepCRDPaths: []string{rayCrdPath},
		}
		cfg = fwk.Init()
		ctx, k8sClient = fwk.RunManager(cfg, managerAndSchedulerSetup())
	})
	ginkgo.AfterAll(func() {
		fwk.Teardown()
	})

	var (
		ns             *corev1.Namespace
		onDemandFlavor *kueue.ResourceFlavor
		clusterQueue   *kueue.ClusterQueue
		localQueue     *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "core-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

		onDemandFlavor = testing.MakeResourceFlavor("on-demand").Label(instanceKey, "on-demand").Obj()
		gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).Should(gomega.Succeed())

		clusterQueue = testing.MakeClusterQueue("clusterqueue").
			ResourceGroup(
				*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "4").Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, clusterQueue)).Should(gomega.Succeed())

		ginkgo.By("creating localQueue")
		localQueue = testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, localQueue)).Should(gomega.Succeed())

		ginkgo.By("creating priority")
		priorityClass := testing.MakePriorityClass(priorityClassName).
			PriorityValue(priorityValue).Obj()
		gomega.Expect(k8sClient.Create(ctx, priorityClass)).Should(gomega.Succeed())
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
	})

	ginkgo.It("Should preempt lower priority RayClusters when resource insufficient", func() {
		ginkgo.By("Create a low priority RayCluster")
		lowPriorityJob := testingraycluster.MakeCluster("raycluster-with-low-priority", ns.Name).Queue(localQueue.Name).
			RequestHead(corev1.ResourceCPU, "1").
			RequestWorkerGroup(corev1.ResourceCPU, "2").
			Obj()
		gomega.Expect(k8sClient.Create(ctx, lowPriorityJob)).Should(gomega.Succeed())

		ginkgo.By("Await for the low priority workload to be admitted")
		createdJob := &rayv1.RayCluster{}
		gomega.Eventually(func() bool {
			gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: lowPriorityJob.Name, Namespace: lowPriorityJob.Namespace}, createdJob)).
				Should(gomega.Succeed())
			return *createdJob.Spec.Suspend
		}, util.Timeout, util.Interval).Should(gomega.BeFalse())

		ginkgo.By("Create a high priority RayCluster which will preempt the lower one")
		highPriorityJob := testingraycluster.MakeCluster("raycluster-with-high-priority", ns.Name).Queue(localQueue.Name).
			RequestHead(corev1.ResourceCPU, "2").
			WithPriorityClassName(priorityClassName).
			RequestWorkerGroup(corev1.ResourceCPU, "2").
			Obj()
		gomega.Expect(k8sClient.Create(ctx, highPriorityJob)).Should(gomega.Succeed())

		ginkgo.By("High priority workload should be admitted")
		highPriorityWL := &kueue.Workload{}
		highPriorityLookupKey := types.NamespacedName{Name: workloadraycluster.GetWorkloadNameForRayCluster(highPriorityJob.Name, highPriorityJob.UID), Namespace: ns.Name}

		gomega.Eventually(func() error {
			return k8sClient.Get(ctx, highPriorityLookupKey, highPriorityWL)
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		apimeta.IsStatusConditionTrue(highPriorityWL.Status.Conditions, kueue.WorkloadAdmitted)

		ginkgo.By("Low priority workload should not be admitted")
		createdWorkload := &kueue.Workload{}
		lowPriorityLookupKey := types.NamespacedName{Name: workloadraycluster.GetWorkloadNameForRayCluster(lowPriorityJob.Name, lowPriorityJob.UID), Namespace: ns.Name}

		gomega.Eventually(func() error {
			return k8sClient.Get(ctx, lowPriorityLookupKey, createdWorkload)
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		apimeta.IsStatusConditionFalse(createdWorkload.Status.Conditions, kueue.WorkloadAdmitted)

		ginkgo.By("Low priority RayCluster should be suspended")
		createdJob = &rayv1.RayCluster{}
		gomega.Eventually(func() bool {
			gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: lowPriorityJob.Name, Namespace: lowPriorityJob.Namespace}, createdJob)).
				Should(gomega.Succeed())
			return *createdJob.Spec.Suspend
		}, util.Timeout, util.Interval).Should(gomega.BeTrue())

		ginkgo.By("Delete high priority raycluster")
		gomega.Expect(k8sClient.Delete(ctx, highPriorityJob)).To(gomega.Succeed())
		gomega.EventuallyWithOffset(1, func() error {
			raycluster := &rayv1.RayCluster{}
			return k8sClient.Get(ctx, client.ObjectKeyFromObject(highPriorityJob), raycluster)
		}, util.Timeout, util.Interval).Should(testing.BeNotFoundError())
		// Manually delete workload because no garbage collection controller.
		gomega.Expect(k8sClient.Delete(ctx, highPriorityWL)).To(gomega.Succeed())
		gomega.EventuallyWithOffset(1, func() error {
			wl := &kueue.Workload{}
			return k8sClient.Get(ctx, highPriorityLookupKey, wl)
		}, util.Timeout, util.Interval).Should(testing.BeNotFoundError())

		ginkgo.By("Low priority workload should be admitted again")
		createdWorkload = &kueue.Workload{}
		gomega.Eventually(func() error {
			return k8sClient.Get(ctx, lowPriorityLookupKey, createdWorkload)
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		apimeta.IsStatusConditionTrue(createdWorkload.Status.Conditions, kueue.WorkloadAdmitted)

		ginkgo.By("Low priority RayCluster should be unsuspended")
		createdJob = &rayv1.RayCluster{}
		gomega.Eventually(func() bool {
			gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: lowPriorityJob.Name, Namespace: lowPriorityJob.Namespace}, createdJob)).
				Should(gomega.Succeed())
			return *createdJob.Spec.Suspend
		}, util.Timeout, util.Interval).Should(gomega.BeFalse())
	})
})
