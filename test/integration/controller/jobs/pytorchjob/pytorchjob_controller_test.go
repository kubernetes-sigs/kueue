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
	"fmt"

	"github.com/google/go-cmp/cmp/cmpopts"
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
	workloadpytorchjob "sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/jobs/pytorchjob"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingpytorchjob "sigs.k8s.io/kueue/pkg/util/testingjobs/pytorchjob"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

const (
	jobName           = "test-job"
	instanceKey       = "cloud.provider.com/instance"
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
			CRDPath:     crdPath,
			DepCRDPaths: []string{pytorchCrdPath},
		}
		cfg = fwk.Init()
		ctx, k8sClient = fwk.RunManager(cfg, managerSetup(jobframework.WithManageJobsWithoutQueueName(true)))
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
		wlLookupKey = types.NamespacedName{Name: workloadpytorchjob.GetWorkloadNameForPyTorchJob(jobName), Namespace: ns.Name}
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.It("Should reconcile PyTorchJobs", func() {
		ginkgo.By("checking the job gets suspended when created unsuspended")
		priorityClass := testing.MakePriorityClass(priorityClassName).
			PriorityValue(int32(priorityValue)).Obj()
		gomega.Expect(k8sClient.Create(ctx, priorityClass)).Should(gomega.Succeed())

		job := testingpytorchjob.MakePyTorchJob(jobName, ns.Name).PriorityClass(priorityClassName).Obj()
		err := k8sClient.Create(ctx, job)
		gomega.Expect(err).To(gomega.Succeed())
		createdJob := &kftraining.PyTorchJob{}

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
				Name:      workloadpytorchjob.GetWorkloadNameForPyTorchJob("second-workload"),
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
		onDemandFlavor := testing.MakeResourceFlavor("on-demand").Label(instanceKey, "on-demand").Obj()
		gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).Should(gomega.Succeed())
		spotFlavor := testing.MakeResourceFlavor("spot").Label(instanceKey, "spot").Obj()
		gomega.Expect(k8sClient.Create(ctx, spotFlavor)).Should(gomega.Succeed())
		clusterQueue := testing.MakeClusterQueue("cluster-queue").
			ResourceGroup(
				*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj(),
				*testing.MakeFlavorQuotas("spot").Resource(corev1.ResourceCPU, "5").Obj(),
			).Obj()
		admission := testing.MakeAdmission(clusterQueue.Name).
			PodSets(
				kueue.PodSetAssignment{
					Name: "Master",
					Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
						corev1.ResourceCPU: "on-demand",
					},
					Count: ptr.To(createdWorkload.Spec.PodSets[0].Count),
				},
				kueue.PodSetAssignment{
					Name: "Worker",
					Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
						corev1.ResourceCPU: "spot",
					},
					Count: ptr.To(createdWorkload.Spec.PodSets[1].Count),
				},
			).
			Obj()
		gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, admission)).Should(gomega.Succeed())
		util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
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
		gomega.Expect(len(createdJob.Spec.PyTorchReplicaSpecs[kftraining.PyTorchJobReplicaTypeMaster].Template.Spec.NodeSelector)).Should(gomega.Equal(1))
		gomega.Expect(createdJob.Spec.PyTorchReplicaSpecs[kftraining.PyTorchJobReplicaTypeMaster].Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))
		gomega.Expect(len(createdJob.Spec.PyTorchReplicaSpecs[kftraining.PyTorchJobReplicaTypeWorker].Template.Spec.NodeSelector)).Should(gomega.Equal(1))
		gomega.Expect(createdJob.Spec.PyTorchReplicaSpecs[kftraining.PyTorchJobReplicaTypeWorker].Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(spotFlavor.Name))
		gomega.Consistently(func() bool {
			if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
				return false
			}
			return len(createdWorkload.Status.Conditions) == 2
		}, util.ConsistentDuration, util.Interval).Should(gomega.BeTrue())

		ginkgo.By("checking the job gets suspended when parallelism changes and the added node selectors are removed")
		parallelism := ptr.Deref(job.Spec.PyTorchReplicaSpecs[kftraining.PyTorchJobReplicaTypeWorker].Replicas, 1)
		newParallelism := parallelism + 1
		createdJob.Spec.PyTorchReplicaSpecs[kftraining.PyTorchJobReplicaTypeWorker].Replicas = &newParallelism
		gomega.Expect(k8sClient.Update(ctx, createdJob)).Should(gomega.Succeed())
		gomega.Eventually(func() bool {
			if err := k8sClient.Get(ctx, lookupKey, createdJob); err != nil {
				return false
			}
			return createdJob.Spec.RunPolicy.Suspend != nil && *createdJob.Spec.RunPolicy.Suspend &&
				len(createdJob.Spec.PyTorchReplicaSpecs[kftraining.PyTorchJobReplicaTypeWorker].Template.Spec.NodeSelector) == 0
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
					Name: "Master",
					Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
						corev1.ResourceCPU: "on-demand",
					},
					Count: ptr.To(createdWorkload.Spec.PodSets[0].Count),
				},
				kueue.PodSetAssignment{
					Name: "Worker",
					Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
						corev1.ResourceCPU: "spot",
					},
					Count: ptr.To(createdWorkload.Spec.PodSets[1].Count),
				},
			).
			Obj()
		gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, admission)).Should(gomega.Succeed())
		util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
		gomega.Eventually(func() bool {
			if err := k8sClient.Get(ctx, lookupKey, createdJob); err != nil {
				return false
			}
			return !*createdJob.Spec.RunPolicy.Suspend
		}, util.Timeout, util.Interval).Should(gomega.BeTrue())
		gomega.Expect(len(createdJob.Spec.PyTorchReplicaSpecs[kftraining.PyTorchJobReplicaTypeMaster].Template.Spec.NodeSelector)).Should(gomega.Equal(1))
		gomega.Expect(createdJob.Spec.PyTorchReplicaSpecs[kftraining.PyTorchJobReplicaTypeMaster].Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))
		gomega.Expect(len(createdJob.Spec.PyTorchReplicaSpecs[kftraining.PyTorchJobReplicaTypeWorker].Template.Spec.NodeSelector)).Should(gomega.Equal(1))
		gomega.Expect(createdJob.Spec.PyTorchReplicaSpecs[kftraining.PyTorchJobReplicaTypeWorker].Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(spotFlavor.Name))
		gomega.Consistently(func() bool {
			if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
				return false
			}
			return len(createdWorkload.Status.Conditions) == 2
		}, util.ConsistentDuration, util.Interval).Should(gomega.BeTrue())

		ginkgo.By("checking the workload is finished when job is completed")
		createdJob.Status.Conditions = append(createdJob.Status.Conditions,
			kftraining.JobCondition{
				Type:               kftraining.JobSucceeded,
				Status:             corev1.ConditionTrue,
				LastTransitionTime: metav1.Now(),
			})
		gomega.Expect(k8sClient.Status().Update(ctx, createdJob)).Should(gomega.Succeed())
		gomega.Eventually(func() bool {
			err := k8sClient.Get(ctx, wlLookupKey, createdWorkload)
			if err != nil || len(createdWorkload.Status.Conditions) == 2 {
				return false
			}

			return apimeta.IsStatusConditionTrue(createdWorkload.Status.Conditions, kueue.WorkloadFinished)
		}, util.Timeout, util.Interval).Should(gomega.BeTrue())
	})
})

var _ = ginkgo.Describe("Job controller for workloads when only jobs with queue are managed", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	ginkgo.BeforeAll(func() {
		fwk = &framework.Framework{
			CRDPath:     crdPath,
			DepCRDPaths: []string{pytorchCrdPath},
		}
		cfg := fwk.Init()
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
		job := testingpytorchjob.MakePyTorchJob(jobName, ns.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
		lookupKey := types.NamespacedName{Name: jobName, Namespace: ns.Name}
		createdJob := &kftraining.PyTorchJob{}
		gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())

		createdWorkload := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadpytorchjob.GetWorkloadNameForPyTorchJob(jobName), Namespace: ns.Name}
		gomega.Consistently(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, wlLookupKey, createdWorkload))
		}, util.ConsistentDuration, util.Interval).Should(gomega.BeTrue())

		ginkgo.By("checking the workload is created when queue name is set")
		jobQueueName := "test-queue"
		createdJob.Annotations = map[string]string{constants.QueueAnnotation: jobQueueName}
		gomega.Expect(k8sClient.Update(ctx, createdJob)).Should(gomega.Succeed())
		gomega.Eventually(func() error {
			return k8sClient.Get(ctx, wlLookupKey, createdWorkload)
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.When("the queue has admission checks", func() {
		var (
			clusterQueueAc *kueue.ClusterQueue
			localQueue     *kueue.LocalQueue
			testFlavor     *kueue.ResourceFlavor
			jobLookupKey   *types.NamespacedName
			wlLookupKey    *types.NamespacedName
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
			testFlavor = testing.MakeResourceFlavor("test-flavor").Label(instanceKey, "test-flavor").Obj()
			gomega.Expect(k8sClient.Create(ctx, testFlavor)).Should(gomega.Succeed())

			jobLookupKey = &types.NamespacedName{Name: jobName, Namespace: ns.Name}
			wlLookupKey = &types.NamespacedName{Name: workloadpytorchjob.GetWorkloadNameForPyTorchJob(jobName), Namespace: ns.Name}
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteAdmissionCheck(ctx, k8sClient, admissionCheck)).To(gomega.Succeed())
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, testFlavor, true)
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, clusterQueueAc, true)
		})

		ginkgo.It("labels and annotations should be propagated from admission check to job", func() {
			createdJob := &kftraining.PyTorchJob{}
			createdWorkload := &kueue.Workload{}

			ginkgo.By("creating the job with pod labels & annotations", func() {
				job := testingpytorchjob.MakePyTorchJob(jobName, ns.Name).
					Queue(localQueue.Name).
					PodAnnotation(kftraining.TFJobReplicaTypeWorker, "old-ann-key", "old-ann-value").
					PodLabel(kftraining.TFJobReplicaTypeWorker, "old-label-key", "old-label-value").
					Obj()
				gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
			})

			ginkgo.By("fetch the job and verify it is suspended as the checks are not ready", func() {
				gomega.Eventually(func() *bool {
					gomega.Expect(k8sClient.Get(ctx, *jobLookupKey, createdJob)).Should(gomega.Succeed())
					return createdJob.Spec.RunPolicy.Suspend
				}, util.Timeout, util.Interval).Should(gomega.Equal(ptr.To(true)))
			})

			ginkgo.By("fetch the created workload", func() {
				gomega.Eventually(func() error {
					return k8sClient.Get(ctx, *wlLookupKey, createdWorkload)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("add labels & annotations to the admission check", func() {
				gomega.Eventually(func() error {
					var newWL kueue.Workload
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(createdWorkload), &newWL)).To(gomega.Succeed())
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
					return k8sClient.Status().Update(ctx, &newWL)
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
				gomega.Eventually(func() *bool {
					gomega.Expect(k8sClient.Get(ctx, *jobLookupKey, createdJob)).Should(gomega.Succeed())
					return createdJob.Spec.RunPolicy.Suspend
				}, util.Timeout, util.Interval).Should(gomega.Equal(ptr.To(false)))
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
				gomega.Expect(util.DeleteLocalQueue(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
			})

			ginkgo.By("clear the workload's admission to stop the job", func() {
				gomega.Expect(k8sClient.Get(ctx, *wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, nil)).Should(gomega.Succeed())
				util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
			})

			ginkgo.By("await for the job to be suspended", func() {
				gomega.Eventually(func() *bool {
					gomega.Expect(k8sClient.Get(ctx, *jobLookupKey, createdJob)).Should(gomega.Succeed())
					return createdJob.Spec.RunPolicy.Suspend
				}, util.Timeout, util.Interval).Should(gomega.Equal(ptr.To(true)))
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
	type podsReadyTestSpec struct {
		beforeJobStatus *kftraining.JobStatus
		beforeCondition *metav1.Condition
		jobStatus       kftraining.JobStatus
		suspended       bool
		wantCondition   *metav1.Condition
	}

	var (
		ns            *corev1.Namespace
		wlLookupKey   types.NamespacedName
		defaultFlavor = testing.MakeResourceFlavor("default").Label(instanceKey, "default").Obj()
	)

	ginkgo.BeforeAll(func() {
		fwk = &framework.Framework{
			CRDPath:     crdPath,
			DepCRDPaths: []string{pytorchCrdPath},
		}
		cfg := fwk.Init()
		ctx, k8sClient = fwk.RunManager(cfg, managerSetup(jobframework.WithWaitForPodsReady(true)))

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
		wlLookupKey = types.NamespacedName{Name: workloadpytorchjob.GetWorkloadNameForPyTorchJob(jobName), Namespace: ns.Name}
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.DescribeTable("Single job at different stages of progress towards completion",
		func(podsReadyTestSpec podsReadyTestSpec) {
			ginkgo.By("Create a job")
			job := testingpytorchjob.MakePyTorchJob(jobName, ns.Name).Parallelism(2).Obj()
			jobQueueName := "test-queue"
			job.Annotations = map[string]string{constants.QueueAnnotation: jobQueueName}
			gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
			lookupKey := types.NamespacedName{Name: jobName, Namespace: ns.Name}
			createdJob := &kftraining.PyTorchJob{}
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
						Name: "Master",
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: "default",
						},
						Count: ptr.To(createdWorkload.Spec.PodSets[0].Count),
					},
					kueue.PodSetAssignment{
						Name: "Worker",
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: "default",
						},
						Count: ptr.To(createdWorkload.Spec.PodSets[1].Count),
					},
				).
				Obj()
			gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, admission)).Should(gomega.Succeed())
			util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
			gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())

			ginkgo.By("Await for the job to be unsuspended")
			gomega.Eventually(func() *bool {
				gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
				return createdJob.Spec.RunPolicy.Suspend
			}, util.Timeout, util.Interval).Should(gomega.Equal(ptr.To(false)))

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
		ginkgo.Entry("Running PyTorchJob", podsReadyTestSpec{
			jobStatus: kftraining.JobStatus{
				Conditions: []kftraining.JobCondition{
					{
						Type:   kftraining.JobRunning,
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
		ginkgo.Entry("Running PyTorchJob; PodsReady=False before", podsReadyTestSpec{
			beforeCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionFalse,
				Reason:  "PodsReady",
				Message: "Not all pods are ready or succeeded",
			},
			jobStatus: kftraining.JobStatus{
				Conditions: []kftraining.JobCondition{
					{
						Type:   kftraining.JobRunning,
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
			beforeJobStatus: &kftraining.JobStatus{
				Conditions: []kftraining.JobCondition{
					{
						Type:   kftraining.JobRunning,
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
			jobStatus: kftraining.JobStatus{
				Conditions: []kftraining.JobCondition{
					{
						Type:   kftraining.JobRunning,
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
	var (
		ns                  *corev1.Namespace
		onDemandFlavor      *kueue.ResourceFlavor
		spotUntaintedFlavor *kueue.ResourceFlavor
		clusterQueue        *kueue.ClusterQueue
		localQueue          *kueue.LocalQueue
	)

	ginkgo.BeforeAll(func() {
		fwk = &framework.Framework{
			CRDPath:     crdPath,
			DepCRDPaths: []string{pytorchCrdPath},
		}
		cfg := fwk.Init()
		ctx, k8sClient = fwk.RunManager(cfg, managerAndSchedulerSetup())
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
		job := testingpytorchjob.MakePyTorchJob("dev-job", ns.Name).Queue(localQueue.Name).
			Request(kftraining.PyTorchJobReplicaTypeMaster, corev1.ResourceCPU, "3").
			Request(kftraining.PyTorchJobReplicaTypeWorker, corev1.ResourceCPU, "4").
			Obj()
		gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
		createdJob := &kftraining.PyTorchJob{}
		gomega.Eventually(func() *bool {
			gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: job.Name, Namespace: job.Namespace}, createdJob)).
				Should(gomega.Succeed())
			return createdJob.Spec.RunPolicy.Suspend
		}, util.Timeout, util.Interval).Should(gomega.Equal(ptr.To(false)))
		gomega.Expect(createdJob.Spec.PyTorchReplicaSpecs[kftraining.PyTorchJobReplicaTypeMaster].Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(spotUntaintedFlavor.Name))
		gomega.Expect(createdJob.Spec.PyTorchReplicaSpecs[kftraining.PyTorchJobReplicaTypeWorker].Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))
		util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
		util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
	})

	ginkgo.When("The workload's admission is removed", func() {
		ginkgo.It("Should restore the original node selectors", func() {

			localQueue := testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			job := testingpytorchjob.MakePyTorchJob(jobName, ns.Name).Queue(localQueue.Name).
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
				gomega.Eventually(func() *bool {
					gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
					return createdJob.Spec.RunPolicy.Suspend
				}, util.Timeout, util.Interval).Should(gomega.Equal(ptr.To(true)))
			})

			// backup the node selectors
			originalNodeSelectors := nodeSelectors(createdJob)

			ginkgo.By("create a localQueue", func() {
				gomega.Expect(k8sClient.Create(ctx, localQueue)).Should(gomega.Succeed())
			})

			ginkgo.By("job should be unsuspended", func() {
				gomega.Eventually(func() *bool {
					gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
					return createdJob.Spec.RunPolicy.Suspend
				}, util.Timeout, util.Interval).Should(gomega.Equal(ptr.To(false)))
			})

			ginkgo.By("the node selectors should be updated", func() {
				gomega.Eventually(func() map[kftraining.ReplicaType]map[string]string {
					gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
					return nodeSelectors(createdJob)
				}, util.Timeout, util.Interval).ShouldNot(gomega.Equal(originalNodeSelectors))
			})

			ginkgo.By("delete the localQueue to prevent readmission", func() {
				gomega.Expect(util.DeleteLocalQueue(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
			})

			ginkgo.By("clear the workload's admission to stop the job", func() {
				wl := &kueue.Workload{}
				wlKey := types.NamespacedName{Name: workloadpytorchjob.GetWorkloadNameForPyTorchJob(job.Name), Namespace: job.Namespace}
				gomega.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
				gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, wl, nil)).Should(gomega.Succeed())
				util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, wl)
			})

			ginkgo.By("the node selectors should be restored", func() {
				gomega.Eventually(func() map[kftraining.ReplicaType]map[string]string {
					gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
					return nodeSelectors(createdJob)
				}, util.Timeout, util.Interval).Should(gomega.Equal(originalNodeSelectors))
			})
		})
	})
})
