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

package jobset

import (
	"fmt"

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
	jobsetapi "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	workloadjobset "sigs.k8s.io/kueue/pkg/controller/jobs/jobset"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingjobset "sigs.k8s.io/kueue/pkg/util/testingjobs/jobset"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

const (
	jobSetName              = "test-job"
	instanceKey             = "cloud.provider.com/instance"
	priorityClassName       = "test-priority-class"
	priorityValue     int32 = 10
)

var _ = ginkgo.Describe("JobSet controller", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	ginkgo.BeforeAll(func() {
		fwk = &framework.Framework{
			CRDPath:     crdPath,
			DepCRDPaths: []string{jobsetCrdPath},
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
				GenerateName: "jobset-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.It("Should reconcile JobSets", func() {
		ginkgo.By("checking the JobSet gets suspended when created unsuspended")
		priorityClass := testing.MakePriorityClass(priorityClassName).
			PriorityValue(priorityValue).Obj()
		gomega.Expect(k8sClient.Create(ctx, priorityClass)).Should(gomega.Succeed())

		jobSet := testingjobset.MakeJobSet(jobSetName, ns.Name).ReplicatedJobs(
			testingjobset.ReplicatedJobRequirements{
				Name:        "replicated-job-1",
				Replicas:    1,
				Parallelism: 1,
				Completions: 1,
			}, testingjobset.ReplicatedJobRequirements{
				Name:        "replicated-job-2",
				Replicas:    3,
				Parallelism: 1,
				Completions: 1,
			},
		).Suspend(false).
			PriorityClass(priorityClassName).
			Obj()
		err := k8sClient.Create(ctx, jobSet)
		gomega.Expect(err).To(gomega.Succeed())
		createdJobSet := &jobsetapi.JobSet{}

		gomega.Eventually(func() bool {
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: jobSetName, Namespace: ns.Name}, createdJobSet); err != nil {
				return false
			}
			return ptr.Deref(createdJobSet.Spec.Suspend, false)
		}, util.Timeout, util.Interval).Should(gomega.BeTrue())

		ginkgo.By("checking the workload is created without queue assigned")
		createdWorkload := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadjobset.GetWorkloadNameForJobSet(jobSet.Name, jobSet.UID), Namespace: ns.Name}
		gomega.Eventually(func() error {
			return k8sClient.Get(ctx, wlLookupKey, createdWorkload)
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		gomega.Expect(createdWorkload.Spec.QueueName).Should(gomega.Equal(""), "The Workload shouldn't have .spec.queueName set")
		gomega.Expect(metav1.IsControlledBy(createdWorkload, createdJobSet)).To(gomega.BeTrue(), "The Workload should be owned by the JobSet")

		ginkgo.By("checking the workload is created with priority and priorityName")
		gomega.Expect(createdWorkload.Spec.PriorityClassName).Should(gomega.Equal(priorityClassName))
		gomega.Expect(*createdWorkload.Spec.Priority).Should(gomega.Equal(priorityValue))

		ginkgo.By("checking the workload is updated with queue name when the JobSet does")
		jobSetQueueName := "test-queue"
		createdJobSet.Annotations = map[string]string{constants.QueueLabel: jobSetQueueName}
		gomega.Expect(k8sClient.Update(ctx, createdJobSet)).Should(gomega.Succeed())
		gomega.Eventually(func() bool {
			if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
				return false
			}
			return createdWorkload.Spec.QueueName == jobSetQueueName
		}, util.Timeout, util.Interval).Should(gomega.BeTrue())

		ginkgo.By("checking a second non-matching workload is deleted")
		secondWl := &kueue.Workload{
			ObjectMeta: metav1.ObjectMeta{
				Name:      workloadjobset.GetWorkloadNameForJobSet("second-workload", "test-uid"),
				Namespace: createdWorkload.Namespace,
			},
			Spec: *createdWorkload.Spec.DeepCopy(),
		}
		gomega.Expect(ctrl.SetControllerReference(createdJobSet, secondWl, scheme.Scheme)).Should(gomega.Succeed())
		secondWl.Spec.PodSets[0].Count += 1
		gomega.Expect(k8sClient.Create(ctx, secondWl)).Should(gomega.Succeed())
		gomega.Eventually(func() error {
			wl := &kueue.Workload{}
			key := types.NamespacedName{Name: secondWl.Name, Namespace: secondWl.Namespace}
			return k8sClient.Get(ctx, key, wl)
		}, util.Timeout, util.Interval).Should(testing.BeNotFoundError())
		// check the original wl is still there
		//gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
		gomega.Eventually(func() error {
			return k8sClient.Get(ctx, wlLookupKey, createdWorkload)
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("checking the JobSet is unsuspended when workload is assigned")
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
		lookupKey := types.NamespacedName{Name: jobSetName, Namespace: ns.Name}
		gomega.Eventually(func() bool {
			if err := k8sClient.Get(ctx, lookupKey, createdJobSet); err != nil {
				return false
			}
			return !ptr.Deref(createdJobSet.Spec.Suspend, false)
		}, util.Timeout, util.Interval).Should(gomega.BeTrue())
		gomega.Eventually(func() bool {
			ok, _ := testing.CheckLatestEvent(ctx, k8sClient, "Started", corev1.EventTypeNormal, fmt.Sprintf("Admitted by clusterQueue %v", clusterQueue.Name))
			return ok
		}, util.Timeout, util.Interval).Should(gomega.BeTrue())
		gomega.Expect(createdJobSet.Spec.ReplicatedJobs[0].Template.Spec.Template.Spec.NodeSelector).Should(gomega.Equal(map[string]string{instanceKey: onDemandFlavor.Name}))
		gomega.Expect(createdJobSet.Spec.ReplicatedJobs[1].Template.Spec.Template.Spec.NodeSelector).Should(gomega.Equal(map[string]string{instanceKey: spotFlavor.Name}))
		gomega.Eventually(func() bool {
			if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
				return false
			}
			return apimeta.IsStatusConditionTrue(createdWorkload.Status.Conditions, kueue.WorkloadQuotaReserved)
		}, util.Timeout, util.Interval).Should(gomega.BeTrue())

		ginkgo.By("checking the JobSet gets suspended when parallelism changes and the added node selectors are removed")
		parallelism := jobSet.Spec.ReplicatedJobs[0].Replicas
		newParallelism := parallelism + 1
		createdJobSet.Spec.ReplicatedJobs[0].Replicas = newParallelism
		gomega.Expect(k8sClient.Update(ctx, createdJobSet)).Should(gomega.Succeed())
		gomega.Eventually(func() bool {
			if err := k8sClient.Get(ctx, lookupKey, createdJobSet); err != nil {
				return false
			}
			return createdJobSet.Spec.Suspend != nil && *createdJobSet.Spec.Suspend &&
				len(jobSet.Spec.ReplicatedJobs[0].Template.Spec.Template.Spec.NodeSelector) == 0
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
			return createdWorkload.Spec.PodSets[0].Count == newParallelism
		}, util.Timeout, util.Interval).Should(gomega.BeTrue())
		gomega.Expect(createdWorkload.Status.Admission).Should(gomega.BeNil())

		ginkgo.By("checking the JobSet is unsuspended and selectors added when workload is assigned again")
		admission = testing.MakeAdmission(clusterQueue.Name).
			PodSets(
				kueue.PodSetAssignment{
					Name: "replicated-job-1",
					Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
						corev1.ResourceCPU: "on-demand",
					},
					Count: ptr.To(createdWorkload.Spec.PodSets[0].Count),
				},
				kueue.PodSetAssignment{
					Name: "replicated-job-2",
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
			if err := k8sClient.Get(ctx, lookupKey, createdJobSet); err != nil {
				return false
			}
			return !*createdJobSet.Spec.Suspend
		}, util.Timeout, util.Interval).Should(gomega.BeTrue())

		gomega.Expect(createdJobSet.Spec.ReplicatedJobs[0].Template.Spec.Template.Spec.NodeSelector).Should(gomega.HaveLen(1))
		gomega.Expect(createdJobSet.Spec.ReplicatedJobs[0].Template.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))
		gomega.Expect(createdJobSet.Spec.ReplicatedJobs[1].Template.Spec.Template.Spec.NodeSelector).Should(gomega.HaveLen(1))
		gomega.Expect(createdJobSet.Spec.ReplicatedJobs[1].Template.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(spotFlavor.Name))

		ginkgo.By("checking the workload is finished when JobSet is completed")
		createdJobSet.Status.Conditions = append(createdJobSet.Status.Conditions,
			metav1.Condition{
				Type:               string(jobsetapi.JobSetCompleted),
				Status:             metav1.ConditionStatus(corev1.ConditionTrue),
				Reason:             "AllJobsCompleted",
				Message:            "jobset completed successfully",
				LastTransitionTime: metav1.Now(),
			})
		gomega.Expect(k8sClient.Status().Update(ctx, createdJobSet)).Should(gomega.Succeed())
		gomega.Eventually(func() bool {
			err := k8sClient.Get(ctx, wlLookupKey, createdWorkload)
			if err != nil {
				return false
			}
			return apimeta.IsStatusConditionTrue(createdWorkload.Status.Conditions, kueue.WorkloadFinished)
		}, util.Timeout, util.Interval).Should(gomega.BeTrue())
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
			testFlavor = testing.MakeResourceFlavor("test-flavor").Label(instanceKey, "test-flavor").Obj()
			gomega.Expect(k8sClient.Create(ctx, testFlavor)).Should(gomega.Succeed())

			jobLookupKey = &types.NamespacedName{Name: jobSetName, Namespace: ns.Name}
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteAdmissionCheck(ctx, k8sClient, admissionCheck)).To(gomega.Succeed())
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, testFlavor, true)
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, clusterQueueAc, true)
		})

		ginkgo.It("labels and annotations should be propagated from admission check to job", func() {
			createdJob := &jobsetapi.JobSet{}
			createdWorkload := &kueue.Workload{}
			job := testingjobset.MakeJobSet(jobSetName, ns.Name).ReplicatedJobs(
				testingjobset.ReplicatedJobRequirements{
					Name:        "replicated-job-1",
					Replicas:    1,
					Parallelism: 1,
					Completions: 1,
				}, testingjobset.ReplicatedJobRequirements{
					Name:        "replicated-job-2",
					Replicas:    3,
					Parallelism: 1,
					Completions: 1,
				},
			).
				Queue("queue").
				Request("replicated-job-1", corev1.ResourceCPU, "1").
				Request("replicated-job-2", corev1.ResourceCPU, "1").
				Obj()
			job.Spec.ReplicatedJobs[0].Template.Spec.Template.Annotations = map[string]string{
				"old-ann-key": "old-ann-value",
			}
			job.Spec.ReplicatedJobs[0].Template.Spec.Template.Labels = map[string]string{
				"old-label-key": "old-label-value",
			}

			ginkgo.By("creating the job", func() {
				gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
			})

			ginkgo.By("fetch the job and verify it is suspended as the checks are not ready", func() {
				gomega.Eventually(func() *bool {
					gomega.Expect(k8sClient.Get(ctx, *jobLookupKey, createdJob)).Should(gomega.Succeed())
					return createdJob.Spec.Suspend
				}, util.Timeout, util.Interval).Should(gomega.Equal(ptr.To(true)))
			})

			wlLookupKey := &types.NamespacedName{Name: workloadjobset.GetWorkloadNameForJobSet(job.Name, job.UID), Namespace: ns.Name}
			ginkgo.By("checking the workload is created", func() {
				gomega.Eventually(func() error {
					return k8sClient.Get(ctx, *wlLookupKey, createdWorkload)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("add labels & annotations to the admission check in PodSetUpdates", func() {
				gomega.Eventually(func() error {
					var newWL kueue.Workload
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(createdWorkload), &newWL)).To(gomega.Succeed())
					workload.SetAdmissionCheckState(&newWL.Status.AdmissionChecks, kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: "replicated-job-1",
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
							{
								Name: "replicated-job-2",
								Annotations: map[string]string{
									"ann1": "ann-value2",
								},
								Labels: map[string]string{
									"label1": "label-value2",
								},
								NodeSelector: map[string]string{
									"selector1": "selector-value2",
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
							Name: createdWorkload.Spec.PodSets[0].Name,
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "test-flavor",
							},
						}, kueue.PodSetAssignment{
							Name: createdWorkload.Spec.PodSets[1].Name,
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "test-flavor",
							},
						},
					).
					Obj()
				gomega.Expect(k8sClient.Get(ctx, *wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, admission)).Should(gomega.Succeed())
				util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
			})

			ginkgo.By("await for the job to be admitted", func() {
				gomega.Eventually(func() *bool {
					gomega.Expect(k8sClient.Get(ctx, *jobLookupKey, createdJob)).Should(gomega.Succeed())
					return createdJob.Spec.Suspend
				}, util.Timeout, util.Interval).Should(gomega.Equal(ptr.To(false)))
			})

			ginkgo.By("verify the PodSetUpdates are propagated to the running job, for replicated-job-1", func() {
				replica1 := createdJob.Spec.ReplicatedJobs[0].Template.Spec.Template
				gomega.Expect(replica1.Annotations).Should(gomega.HaveKeyWithValue("ann1", "ann-value1"))
				gomega.Expect(replica1.Annotations).Should(gomega.HaveKeyWithValue("old-ann-key", "old-ann-value"))
				gomega.Expect(replica1.Labels).Should(gomega.HaveKeyWithValue("label1", "label-value1"))
				gomega.Expect(replica1.Labels).Should(gomega.HaveKeyWithValue("old-label-key", "old-label-value"))
				gomega.Expect(replica1.Spec.NodeSelector).Should(gomega.HaveKeyWithValue("selector1", "selector-value1"))
				gomega.Expect(replica1.Spec.Tolerations).Should(gomega.BeComparableTo(
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

			ginkgo.By("verify the PodSetUpdates are propagated to the running job, for replicated-job-2", func() {
				replica2 := createdJob.Spec.ReplicatedJobs[1].Template.Spec.Template
				gomega.Expect(replica2.Spec.NodeSelector).Should(gomega.HaveKeyWithValue("selector1", "selector-value2"))
				gomega.Expect(replica2.Annotations).Should(gomega.HaveKeyWithValue("ann1", "ann-value2"))
				gomega.Expect(replica2.Labels).Should(gomega.HaveKeyWithValue("label1", "label-value2"))
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
					return createdJob.Spec.Suspend
				}, util.Timeout, util.Interval).Should(gomega.Equal(ptr.To(true)))
			})

			ginkgo.By("verify the PodSetUpdates are restored for replicated-job-1", func() {
				replica1 := createdJob.Spec.ReplicatedJobs[0].Template.Spec.Template
				gomega.Expect(replica1.Annotations).ShouldNot(gomega.HaveKey("ann1"))
				gomega.Expect(replica1.Annotations).Should(gomega.HaveKeyWithValue("old-ann-key", "old-ann-value"))
				gomega.Expect(replica1.Labels).ShouldNot(gomega.HaveKey("label1"))
				gomega.Expect(replica1.Labels).Should(gomega.HaveKeyWithValue("old-label-key", "old-label-value"))
				gomega.Expect(replica1.Spec.NodeSelector).ShouldNot(gomega.HaveKey("selector1"))
			})

			ginkgo.By("verify the PodSetUpdates are restored for replicated-job-2", func() {
				replica2 := createdJob.Spec.ReplicatedJobs[1].Template.Spec.Template
				gomega.Expect(replica2.Spec.NodeSelector).ShouldNot(gomega.HaveKey("selector1"))
				gomega.Expect(replica2.Annotations).ShouldNot(gomega.HaveKey("ann1"))
				gomega.Expect(replica2.Labels).ShouldNot(gomega.HaveKey("label1"))
			})
		})
	})
})

var _ = ginkgo.Describe("JobSet controller for workloads when only jobs with queue are managed", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	ginkgo.BeforeAll(func() {
		fwk = &framework.Framework{
			CRDPath:     crdPath,
			DepCRDPaths: []string{jobsetCrdPath},
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
				GenerateName: "jobset-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.It("Should reconcile jobs only when queue is set", func() {
		ginkgo.By("checking the workload is not created when queue name is not set")
		jobSet := testingjobset.MakeJobSet(jobSetName, ns.Name).ReplicatedJobs(
			testingjobset.ReplicatedJobRequirements{
				Name:        "replicated-job-1",
				Replicas:    1,
				Parallelism: 1,
				Completions: 1,
			}, testingjobset.ReplicatedJobRequirements{
				Name:        "replicated-job-2",
				Replicas:    3,
				Parallelism: 1,
				Completions: 1,
			},
		).Suspend(false).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, jobSet)).Should(gomega.Succeed())
		lookupKey := types.NamespacedName{Name: jobSetName, Namespace: ns.Name}
		createdJobSet := &jobsetapi.JobSet{}
		gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJobSet)).Should(gomega.Succeed())

		createdWorkload := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadjobset.GetWorkloadNameForJobSet(jobSet.Name, jobSet.UID), Namespace: ns.Name}
		gomega.Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, wlLookupKey, createdWorkload))
		}, util.Timeout, util.Interval).Should(gomega.BeTrue())

		ginkgo.By("checking the workload is created when queue name is set")
		jobQueueName := "test-queue"
		if createdJobSet.Labels == nil {
			createdJobSet.Labels = map[string]string{constants.QueueLabel: jobQueueName}
		} else {
			createdJobSet.Labels[constants.QueueLabel] = jobQueueName
		}
		gomega.Expect(k8sClient.Update(ctx, createdJobSet)).Should(gomega.Succeed())
		gomega.Eventually(func() error {
			return k8sClient.Get(ctx, wlLookupKey, createdWorkload)
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})
})

var _ = ginkgo.Describe("JobSet controller when waitForPodsReady enabled", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	type podsReadyTestSpec struct {
		beforeJobSetStatus *jobsetapi.JobSetStatus
		beforeCondition    *metav1.Condition
		jobSetStatus       jobsetapi.JobSetStatus
		suspended          bool
		wantCondition      *metav1.Condition
	}

	var defaultFlavor = testing.MakeResourceFlavor("default").Label(instanceKey, "default").Obj()

	ginkgo.BeforeAll(func() {
		fwk = &framework.Framework{
			CRDPath:     crdPath,
			DepCRDPaths: []string{jobsetCrdPath},
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
				GenerateName: "jobset-",
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
			jobSet := testingjobset.MakeJobSet(jobSetName, ns.Name).ReplicatedJobs(
				testingjobset.ReplicatedJobRequirements{
					Name:        "replicated-job-1",
					Replicas:    1,
					Parallelism: 1,
					Completions: 1,
				}, testingjobset.ReplicatedJobRequirements{
					Name:        "replicated-job-2",
					Replicas:    3,
					Parallelism: 1,
					Completions: 1,
				},
			).Obj()
			jobSetQueueName := "test-queue"
			jobSet.Annotations = map[string]string{constants.QueueLabel: jobSetQueueName}
			gomega.Expect(k8sClient.Create(ctx, jobSet)).Should(gomega.Succeed())
			lookupKey := types.NamespacedName{Name: jobSetName, Namespace: ns.Name}
			createdJobSet := &jobsetapi.JobSet{}
			gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJobSet)).Should(gomega.Succeed())

			ginkgo.By("Fetch the workload created for the JobSet")
			createdWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: workloadjobset.GetWorkloadNameForJobSet(jobSet.Name, jobSet.UID), Namespace: ns.Name}
			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, wlLookupKey, createdWorkload)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Admit the workload created for the JobSet")
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

			ginkgo.By("Await for the JobSet to be unsuspended")
			gomega.Eventually(func() bool {
				gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJobSet)).Should(gomega.Succeed())
				return ptr.Deref(createdJobSet.Spec.Suspend, false)
			}, util.Timeout, util.Interval).Should(gomega.BeFalse())

			if podsReadyTestSpec.beforeJobSetStatus != nil {
				ginkgo.By("Update the JobSet status to simulate its initial progress towards completion")
				createdJobSet.Status = *podsReadyTestSpec.beforeJobSetStatus
				gomega.Expect(k8sClient.Status().Update(ctx, createdJobSet)).Should(gomega.Succeed())
				gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJobSet)).Should(gomega.Succeed())
			}

			if podsReadyTestSpec.beforeCondition != nil {
				ginkgo.By("Update the workload status")
				gomega.Eventually(func() *metav1.Condition {
					gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					return apimeta.FindStatusCondition(createdWorkload.Status.Conditions, kueue.WorkloadPodsReady)
				}, util.Timeout, util.Interval).Should(gomega.BeComparableTo(podsReadyTestSpec.beforeCondition, util.IgnoreConditionTimestampsAndObservedGeneration))
			}

			ginkgo.By("Update the JobSet status to simulate its progress towards completion")
			createdJobSet.Status = podsReadyTestSpec.jobSetStatus
			gomega.Expect(k8sClient.Status().Update(ctx, createdJobSet)).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJobSet)).Should(gomega.Succeed())

			if podsReadyTestSpec.suspended {
				ginkgo.By("Unset admission of the workload to suspend the JobSet")
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
			}, util.Timeout, util.Interval).Should(gomega.BeComparableTo(podsReadyTestSpec.wantCondition, util.IgnoreConditionTimestampsAndObservedGeneration))
		},
		ginkgo.Entry("No progress", podsReadyTestSpec{
			wantCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionFalse,
				Reason:  "PodsReady",
				Message: "Not all pods are ready or succeeded",
			},
		}),
		ginkgo.Entry("Running JobSet", podsReadyTestSpec{
			jobSetStatus: jobsetapi.JobSetStatus{
				ReplicatedJobsStatus: []jobsetapi.ReplicatedJobStatus{
					{
						Name:      "replicated-job-1",
						Ready:     1,
						Succeeded: 0,
					},
					{
						Name:      "replicated-job-2",
						Ready:     2,
						Succeeded: 1,
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
		ginkgo.Entry("Running JobSet; PodsReady=False before", podsReadyTestSpec{
			beforeCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionFalse,
				Reason:  "PodsReady",
				Message: "Not all pods are ready or succeeded",
			},
			jobSetStatus: jobsetapi.JobSetStatus{
				ReplicatedJobsStatus: []jobsetapi.ReplicatedJobStatus{
					{
						Name:      "replicated-job-1",
						Ready:     1,
						Succeeded: 0,
					},
					{
						Name:      "replicated-job-2",
						Ready:     2,
						Succeeded: 1,
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
		ginkgo.Entry("JobSet suspended; PodsReady=True before", podsReadyTestSpec{
			beforeJobSetStatus: &jobsetapi.JobSetStatus{
				ReplicatedJobsStatus: []jobsetapi.ReplicatedJobStatus{
					{
						Name:      "replicated-job-1",
						Ready:     1,
						Succeeded: 0,
					},
					{
						Name:      "replicated-job-2",
						Ready:     2,
						Succeeded: 1,
					},
				},
			},
			beforeCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionTrue,
				Reason:  "PodsReady",
				Message: "All pods were ready or succeeded since the workload admission",
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

var _ = ginkgo.Describe("JobSet controller interacting with scheduler", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	ginkgo.BeforeAll(func() {
		fwk = &framework.Framework{
			CRDPath:     crdPath,
			DepCRDPaths: []string{jobsetCrdPath},
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
				GenerateName: "jobset-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

		onDemandFlavor = testing.MakeResourceFlavor("on-demand").Label(instanceKey, "on-demand").Obj()
		gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).Should(gomega.Succeed())

		spotUntaintedFlavor = testing.MakeResourceFlavor("spot-untainted").Label(instanceKey, "spot-untainted").Obj()
		gomega.Expect(k8sClient.Create(ctx, spotUntaintedFlavor)).Should(gomega.Succeed())

		clusterQueue = testing.MakeClusterQueue("dev-clusterqueue").
			ResourceGroup(
				*testing.MakeFlavorQuotas("spot-untainted").Resource(corev1.ResourceCPU, "1").Obj(),
				*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj(),
			).Obj()
		gomega.Expect(k8sClient.Create(ctx, clusterQueue)).Should(gomega.Succeed())
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, spotUntaintedFlavor, true)
	})

	ginkgo.It("Should schedule JobSets as they fit in their ClusterQueue", func() {
		ginkgo.By("creating localQueue")
		localQueue = testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, localQueue)).Should(gomega.Succeed())

		ginkgo.By("checking a dev job starts")
		jobSet := testingjobset.MakeJobSet("dev-job", ns.Name).ReplicatedJobs(
			testingjobset.ReplicatedJobRequirements{
				Name:        "replicated-job-1",
				Replicas:    1,
				Parallelism: 1,
				Completions: 1,
			}, testingjobset.ReplicatedJobRequirements{
				Name:        "replicated-job-2",
				Replicas:    3,
				Parallelism: 1,
				Completions: 1,
			},
		).Queue(localQueue.Name).
			Request("replicated-job-1", corev1.ResourceCPU, "1").
			Request("replicated-job-2", corev1.ResourceCPU, "1").
			Obj()
		gomega.Expect(k8sClient.Create(ctx, jobSet)).Should(gomega.Succeed())
		createdJobSet := &jobsetapi.JobSet{}
		gomega.Eventually(func() bool {
			gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: jobSet.Name, Namespace: jobSet.Namespace}, createdJobSet)).
				Should(gomega.Succeed())
			return ptr.Deref(createdJobSet.Spec.Suspend, false)
		}, util.Timeout, util.Interval).Should(gomega.BeFalse())
		fmt.Println(createdJobSet.Spec.ReplicatedJobs[0].Template.Spec.Template.Spec.NodeSelector)
		gomega.Expect(createdJobSet.Spec.ReplicatedJobs[0].Template.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(spotUntaintedFlavor.Name))
		gomega.Expect(createdJobSet.Spec.ReplicatedJobs[1].Template.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))
		util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
		util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)

	})

	ginkgo.It("Should allow reclaim of resources that are no longer needed", func() {
		ginkgo.By("creating localQueue", func() {
			localQueue = testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).Should(gomega.Succeed())
		})

		jobSet1 := testingjobset.MakeJobSet("dev-jobset1", ns.Name).ReplicatedJobs(
			testingjobset.ReplicatedJobRequirements{
				Name:        "replicated-job-1",
				Replicas:    2,
				Parallelism: 4,
				Completions: 8,
			}, testingjobset.ReplicatedJobRequirements{
				Name:        "replicated-job-2",
				Replicas:    3,
				Parallelism: 4,
				Completions: 4,
			},
		).Queue(localQueue.Name).
			Request("replicated-job-1", corev1.ResourceCPU, "250m").
			Request("replicated-job-2", corev1.ResourceCPU, "250m").
			Obj()
		lookupKey1 := types.NamespacedName{Name: jobSet1.Name, Namespace: jobSet1.Namespace}

		ginkgo.By("checking the first jobset starts", func() {
			gomega.Expect(k8sClient.Create(ctx, jobSet1)).Should(gomega.Succeed())
			createdJobSet1 := &jobsetapi.JobSet{}
			gomega.Eventually(func() *bool {
				gomega.Expect(k8sClient.Get(ctx, lookupKey1, createdJobSet1)).Should(gomega.Succeed())
				return createdJobSet1.Spec.Suspend
			}, util.Timeout, util.Interval).Should(gomega.Equal(ptr.To(false)))
			util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
			util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
		})

		jobSet2 := testingjobset.MakeJobSet("dev-jobset2", ns.Name).ReplicatedJobs(
			testingjobset.ReplicatedJobRequirements{
				Name:        "replicated-job-1",
				Replicas:    2,
				Parallelism: 1,
				Completions: 1,
			}, testingjobset.ReplicatedJobRequirements{
				Name:        "replicated-job-2",
				Replicas:    1,
				Parallelism: 1,
				Completions: 1,
			},
		).Queue(localQueue.Name).
			Request("replicated-job-1", corev1.ResourceCPU, "1").
			Request("replicated-job-2", corev1.ResourceCPU, "1").
			Obj()

		lookupKey2 := types.NamespacedName{Name: jobSet2.Name, Namespace: jobSet2.Namespace}

		ginkgo.By("checking a second no-fit jobset does not start", func() {
			gomega.Expect(k8sClient.Create(ctx, jobSet2)).Should(gomega.Succeed())
			createdJobSet2 := &jobsetapi.JobSet{}
			gomega.Eventually(func() *bool {
				gomega.Expect(k8sClient.Get(ctx, lookupKey2, createdJobSet2)).Should(gomega.Succeed())
				return createdJobSet2.Spec.Suspend
			}, util.Timeout, util.Interval).Should(gomega.Equal(ptr.To(true)))
			util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
			util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
		})

		ginkgo.By("checking the second job starts when the first one needs less then two cpus", func() {
			createdJobSet1 := &jobsetapi.JobSet{}
			gomega.Expect(k8sClient.Get(ctx, lookupKey1, createdJobSet1)).Should(gomega.Succeed())
			createdJobSet1 = (&testingjobset.JobSetWrapper{JobSet: *createdJobSet1}).JobsStatus(
				jobsetapi.ReplicatedJobStatus{
					Name:      "replicated-job-1",
					Succeeded: 2,
				},
				jobsetapi.ReplicatedJobStatus{
					Name:      "replicated-job-2",
					Succeeded: 1,
				},
			).Obj()
			gomega.Expect(k8sClient.Status().Update(ctx, createdJobSet1)).Should(gomega.Succeed())

			wl := &kueue.Workload{}
			wlKey := types.NamespacedName{Name: workloadjobset.GetWorkloadNameForJobSet(jobSet1.Name, jobSet1.UID), Namespace: jobSet1.Namespace}
			gomega.Eventually(func() []kueue.ReclaimablePod {
				gomega.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
				return wl.Status.ReclaimablePods

			}, util.Timeout, util.Interval).Should(gomega.BeComparableTo([]kueue.ReclaimablePod{
				{
					Name:  "replicated-job-1",
					Count: 8,
				},
				{
					Name:  "replicated-job-2",
					Count: 4,
				},
			}))

			createdJobSet2 := &jobsetapi.JobSet{}
			gomega.Eventually(func() *bool {
				gomega.Expect(k8sClient.Get(ctx, lookupKey2, createdJobSet2)).Should(gomega.Succeed())
				return createdJobSet2.Spec.Suspend
			}, util.Timeout, util.Interval).Should(gomega.Equal(ptr.To(false)))
			util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
			util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 2)
		})
	})
})
