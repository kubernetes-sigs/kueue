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

package e2e

import (
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/util/slices"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

// +kubebuilder:docs-gen:collapse=Imports

var _ = ginkgo.Describe("Kueue", func() {
	var ns *corev1.Namespace
	var sampleJob *batchv1.Job
	var jobKey types.NamespacedName

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "e2e-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
		sampleJob = testingjob.MakeJob("test-job", ns.Name).
			Queue("main").
			Request("cpu", "1").
			Request("memory", "20Mi").
			Obj()
		jobKey = client.ObjectKeyFromObject(sampleJob)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.When("Creating a Job without a matching LocalQueue", func() {
		ginkgo.It("Should stay in suspended", func() {
			gomega.Expect(k8sClient.Create(ctx, sampleJob)).Should(gomega.Succeed())

			createdJob := &batchv1.Job{}
			gomega.Eventually(func() bool {
				if err := k8sClient.Get(ctx, jobKey, createdJob); err != nil {
					return false
				}
				return *createdJob.Spec.Suspend
			}, util.Timeout, util.Interval).Should(gomega.BeTrue())
			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(jobKey.Name), Namespace: ns.Name}
			createdWorkload := &kueue.Workload{}
			gomega.Eventually(func() bool {
				if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
					return false
				}
				return workload.HasQuotaReservation(createdWorkload)

			}, util.Timeout, util.Interval).Should(gomega.BeFalse())
			gomega.Expect(k8sClient.Delete(ctx, sampleJob)).Should(gomega.Succeed())
		})
	})

	ginkgo.When("Creating a Job With Queueing", func() {
		var (
			onDemandRF   *kueue.ResourceFlavor
			spotRF       *kueue.ResourceFlavor
			localQueue   *kueue.LocalQueue
			clusterQueue *kueue.ClusterQueue
		)
		ginkgo.BeforeEach(func() {
			onDemandRF = testing.MakeResourceFlavor("on-demand").
				Label("instance-type", "on-demand").Obj()
			gomega.Expect(k8sClient.Create(ctx, onDemandRF)).Should(gomega.Succeed())
			spotRF = testing.MakeResourceFlavor("spot").
				Label("instance-type", "spot").Obj()
			gomega.Expect(k8sClient.Create(ctx, spotRF)).Should(gomega.Succeed())
			clusterQueue = testing.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*testing.MakeFlavorQuotas("on-demand").
						Resource(corev1.ResourceCPU, "1").
						Resource(corev1.ResourceMemory, "1Gi").
						Obj(),
					*testing.MakeFlavorQuotas("spot").
						Resource(corev1.ResourceCPU, "1").
						Resource(corev1.ResourceMemory, "1Gi").
						Obj(),
				).
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
				}).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).Should(gomega.Succeed())
			localQueue = testing.MakeLocalQueue("main", ns.Name).ClusterQueue("cluster-queue").Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).Should(gomega.Succeed())
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteLocalQueue(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, onDemandRF, true)
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, spotRF, true)
		})

		ginkgo.It("Should unsuspend a job and set nodeSelectors", func() {
			// Use a binary that ends.
			sampleJob = (&testingjob.JobWrapper{Job: *sampleJob}).Image("gcr.io/k8s-staging-perf-tests/sleep:v0.0.3", []string{"5s"}).Obj()
			gomega.Expect(k8sClient.Create(ctx, sampleJob)).Should(gomega.Succeed())

			createdWorkload := &kueue.Workload{}
			expectJobUnsuspendedWithNodeSelectors(jobKey, map[string]string{
				"instance-type": "on-demand",
			})
			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(jobKey.Name), Namespace: ns.Name}
			gomega.Eventually(func() bool {
				if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
					return false
				}
				return workload.HasQuotaReservation(createdWorkload) &&
					apimeta.IsStatusConditionTrue(createdWorkload.Status.Conditions, kueue.WorkloadFinished)

			}, util.LongTimeout, util.Interval).Should(gomega.BeTrue())
		})

		ginkgo.It("Should run with prebuilt workload", func() {
			var wl *kueue.Workload
			ginkgo.By("Create the pebuilt workload and the job adopting it", func() {
				sampleJob = (&testingjob.JobWrapper{Job: *sampleJob}).
					Label(constants.PrebuiltWorkloadLabel, "prebuilt-wl").
					Image("gcr.io/k8s-staging-perf-tests/sleep:v0.0.3", []string{"5s", "-termination-grace-period", "0s"}).
					BackoffLimit(0).
					TerminationGracePeriod(1).
					Obj()
				testingjob.SetContainerDefaults(&sampleJob.Spec.Template.Spec.Containers[0])

				wl = testing.MakeWorkload("prebuilt-wl", ns.Name).
					Finalizers(kueue.ResourceInUseFinalizerName).
					Queue(localQueue.Name).
					PodSets(
						*testing.MakePodSet("main", 1).Containers(sampleJob.Spec.Template.Spec.Containers[0]).Obj(),
					).
					Obj()
				gomega.Expect(k8sClient.Create(ctx, wl)).Should(gomega.Succeed())
				gomega.Expect(k8sClient.Create(ctx, sampleJob)).Should(gomega.Succeed())
			})

			createdWorkload := &kueue.Workload{}
			wlLookupKey := client.ObjectKeyFromObject(wl)
			createdJob := &batchv1.Job{}
			jobLookupKey := client.ObjectKeyFromObject(sampleJob)

			ginkgo.By("Verify the prebuilt workload is adopted by the job", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, jobLookupKey, createdJob)).To(gomega.Succeed())
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					g.Expect(wl.Spec.PodSets[0].Template.Spec.Containers).To(gomega.BeComparableTo(createdJob.Spec.Template.Spec.Containers), "Check the way the job and workload is created")
					g.Expect(createdWorkload.OwnerReferences).To(gomega.ContainElement(
						gomega.BeComparableTo(metav1.OwnerReference{
							Name: sampleJob.Name,
							UID:  sampleJob.UID,
						}, cmpopts.IgnoreFields(metav1.OwnerReference{}, "APIVersion", "Kind", "Controller", "BlockOwnerDeletion"))))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Verify the job is running", func() {
				expectJobUnsuspendedWithNodeSelectors(jobKey, map[string]string{
					"instance-type": "on-demand",
				})
			})

			ginkgo.By("Delete all pods", func() {
				gomega.Expect(util.DeleteAllPodsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			})

			ginkgo.By("Await for jobs completion", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), createdWorkload)).To(gomega.Succeed())
					g.Expect(createdWorkload.Finalizers).NotTo(gomega.ContainElement(kueue.ResourceInUseFinalizerName))
					g.Expect(createdWorkload.Status.Conditions).To(gomega.ContainElement(
						gomega.BeComparableTo(metav1.Condition{
							Type:   kueue.WorkloadFinished,
							Status: metav1.ConditionTrue,
							Reason: "JobFinished",
						}, cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime", "Message"))))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should readmit preempted job with priorityClass into a separate flavor", func() {
			gomega.Expect(k8sClient.Create(ctx, sampleJob)).Should(gomega.Succeed())

			highPriorityClass := testing.MakePriorityClass("high").PriorityValue(100).Obj()
			gomega.Expect(k8sClient.Create(ctx, highPriorityClass))
			ginkgo.DeferCleanup(func() {
				gomega.Expect(k8sClient.Delete(ctx, highPriorityClass)).To(gomega.Succeed())
			})

			ginkgo.By("Job is admitted using the first flavor", func() {
				expectJobUnsuspendedWithNodeSelectors(jobKey, map[string]string{
					"instance-type": "on-demand",
				})
			})

			ginkgo.By("Job is preempted by higher priority job", func() {
				job := testingjob.MakeJob("high", ns.Name).
					Queue("main").
					PriorityClass("high").
					Request(corev1.ResourceCPU, "1").
					NodeSelector("instance-type", "on-demand"). // target the same flavor to cause preemption
					Obj()
				gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())

				expectJobUnsuspendedWithNodeSelectors(client.ObjectKeyFromObject(job), map[string]string{
					"instance-type": "on-demand",
				})
			})

			ginkgo.By("Job is re-admitted using the second flavor", func() {
				expectJobUnsuspendedWithNodeSelectors(jobKey, map[string]string{
					"instance-type": "spot",
				})
			})
		})

		ginkgo.It("Should readmit preempted job with workloadPriorityClass into a separate flavor", func() {
			gomega.Expect(k8sClient.Create(ctx, sampleJob)).Should(gomega.Succeed())

			highWorkloadPriorityClass := testing.MakeWorkloadPriorityClass("high-workload").PriorityValue(300).Obj()
			gomega.Expect(k8sClient.Create(ctx, highWorkloadPriorityClass)).Should(gomega.Succeed())
			ginkgo.DeferCleanup(func() {
				gomega.Expect(k8sClient.Delete(ctx, highWorkloadPriorityClass)).To(gomega.Succeed())
			})

			ginkgo.By("Job is admitted using the first flavor", func() {
				expectJobUnsuspendedWithNodeSelectors(jobKey, map[string]string{
					"instance-type": "on-demand",
				})
			})

			ginkgo.By("Job is preempted by higher priority job", func() {
				job := testingjob.MakeJob("high-with-wpc", ns.Name).
					Queue("main").
					WorkloadPriorityClass("high-workload").
					Request(corev1.ResourceCPU, "1").
					NodeSelector("instance-type", "on-demand"). // target the same flavor to cause preemption
					Obj()
				gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())

				expectJobUnsuspendedWithNodeSelectors(client.ObjectKeyFromObject(job), map[string]string{
					"instance-type": "on-demand",
				})
			})

			ginkgo.By("Job is re-admitted using the second flavor", func() {
				expectJobUnsuspendedWithNodeSelectors(jobKey, map[string]string{
					"instance-type": "spot",
				})
			})
		})
		ginkgo.It("Should partially admit the Job if configured and not fully fits", func() {
			// Use a binary that ends.
			job := testingjob.MakeJob("job", ns.Name).
				Queue("main").
				Image("gcr.io/k8s-staging-perf-tests/sleep:v0.0.3", []string{"1s"}).
				Request("cpu", "500m").
				Parallelism(3).
				Completions(4).
				SetAnnotation(workloadjob.JobMinParallelismAnnotation, "1").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())

			ginkgo.By("Wait for the job to start and check the updated Parallelism and Completions", func() {
				jobKey := client.ObjectKeyFromObject(job)
				expectJobUnsuspendedWithNodeSelectors(jobKey, map[string]string{
					"instance-type": "on-demand",
				})

				updatedJob := &batchv1.Job{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, jobKey, updatedJob)).To(gomega.Succeed())
					g.Expect(ptr.Deref(updatedJob.Spec.Parallelism, 0)).To(gomega.Equal(int32(2)))
					g.Expect(ptr.Deref(updatedJob.Spec.Completions, 0)).To(gomega.Equal(int32(4)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

			})

			ginkgo.By("Wait for the job to finish", func() {
				createdWorkload := &kueue.Workload{}
				wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name), Namespace: ns.Name}
				gomega.Eventually(func() bool {
					if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
						return false
					}
					return workload.HasQuotaReservation(createdWorkload) &&
						apimeta.IsStatusConditionTrue(createdWorkload.Status.Conditions, kueue.WorkloadFinished)

				}, util.LongTimeout, util.Interval).Should(gomega.BeTrue())
			})
		})
	})

	ginkgo.When("Creating a Job In a Twostepadmission Queue", func() {
		var (
			onDemandRF   *kueue.ResourceFlavor
			localQueue   *kueue.LocalQueue
			clusterQueue *kueue.ClusterQueue
			check        *kueue.AdmissionCheck
		)
		ginkgo.BeforeEach(func() {
			check = testing.MakeAdmissionCheck("check1").ControllerName("ac-controller").Obj()
			gomega.Expect(k8sClient.Create(ctx, check)).Should(gomega.Succeed())
			util.SetAdmissionCheckActive(ctx, k8sClient, check, metav1.ConditionTrue)
			onDemandRF = testing.MakeResourceFlavor("on-demand").
				Label("instance-type", "on-demand").Obj()
			gomega.Expect(k8sClient.Create(ctx, onDemandRF)).Should(gomega.Succeed())
			clusterQueue = testing.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*testing.MakeFlavorQuotas("on-demand").
						Resource(corev1.ResourceCPU, "1").
						Resource(corev1.ResourceMemory, "1Gi").
						Obj(),
				).
				AdmissionChecks("check1").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).Should(gomega.Succeed())
			localQueue = testing.MakeLocalQueue("main", ns.Name).ClusterQueue("cluster-queue").Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).Should(gomega.Succeed())
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteLocalQueue(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, onDemandRF, true)
			gomega.Expect(k8sClient.Delete(ctx, check)).Should(gomega.Succeed())
		})

		ginkgo.It("Should unsuspend a job only after all checks are cleared", func() {
			// Use a binary that ends.
			sampleJob = (&testingjob.JobWrapper{Job: *sampleJob}).Image("gcr.io/k8s-staging-perf-tests/sleep:v0.0.3", []string{"5s"}).Obj()
			gomega.Expect(k8sClient.Create(ctx, sampleJob)).Should(gomega.Succeed())

			createdWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(jobKey.Name), Namespace: ns.Name}

			ginkgo.By("verify the check is added to the workload", func() {
				gomega.Eventually(func() map[string]string {
					if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
						return nil
					}
					return slices.ToMap(createdWorkload.Status.AdmissionChecks, func(i int) (string, string) { return createdWorkload.Status.AdmissionChecks[i].Name, "" })

				}, util.Timeout, util.Interval).Should(gomega.BeComparableTo(map[string]string{"check1": ""}))
			})

			ginkgo.By("waiting for the workload to be assigned", func() {
				gomega.Eventually(func() bool {
					if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
						return false
					}
					return apimeta.IsStatusConditionTrue(createdWorkload.Status.Conditions, kueue.WorkloadQuotaReserved)

				}, util.Timeout, util.Interval).Should(gomega.BeTrue())
			})

			ginkgo.By("checking the job remains suspended", func() {
				createdJob := &batchv1.Job{}
				jobKey := client.ObjectKeyFromObject(sampleJob)
				gomega.Consistently(func() bool {
					if err := k8sClient.Get(ctx, jobKey, createdJob); err != nil {
						return false
					}
					return ptr.Deref(createdJob.Spec.Suspend, false)

				}, util.ConsistentDuration, util.Interval).Should(gomega.BeTrue())
			})

			ginkgo.By("setting the check as successful", func() {
				gomega.Eventually(func() error {
					if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
						return err
					}
					patch := workload.BaseSSAWorkload(createdWorkload)
					workload.SetAdmissionCheckState(&patch.Status.AdmissionChecks, kueue.AdmissionCheckState{
						Name:  "check1",
						State: kueue.CheckStateReady,
					})
					return k8sClient.Status().Patch(ctx, patch, client.Apply, client.FieldOwner("test-admission-check-controller"), client.ForceOwnership)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			expectJobUnsuspendedWithNodeSelectors(jobKey, map[string]string{
				"instance-type": "on-demand",
			})
			gomega.Eventually(func() bool {
				if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
					return false
				}
				return workload.HasQuotaReservation(createdWorkload) &&
					apimeta.IsStatusConditionTrue(createdWorkload.Status.Conditions, kueue.WorkloadFinished)

			}, util.LongTimeout, util.Interval).Should(gomega.BeTrue())
		})

		ginkgo.It("Should suspend a job when its checks become invalid", func() {
			// Use a binary that ends.
			sampleJob = (&testingjob.JobWrapper{Job: *sampleJob}).Image("gcr.io/k8s-staging-perf-tests/sleep:v0.0.3", []string{"5s"}).Obj()
			gomega.Expect(k8sClient.Create(ctx, sampleJob)).Should(gomega.Succeed())

			createdWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(jobKey.Name), Namespace: ns.Name}

			ginkgo.By("verify the check is added to the workload", func() {
				gomega.Eventually(func() map[string]string {
					if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
						return nil
					}
					return slices.ToMap(createdWorkload.Status.AdmissionChecks, func(i int) (string, string) { return createdWorkload.Status.AdmissionChecks[i].Name, "" })

				}, util.Timeout, util.Interval).Should(gomega.BeComparableTo(map[string]string{"check1": ""}))
			})

			ginkgo.By("setting the check as successful", func() {
				gomega.Eventually(func() error {
					if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
						return err
					}
					patch := workload.BaseSSAWorkload(createdWorkload)
					workload.SetAdmissionCheckState(&patch.Status.AdmissionChecks, kueue.AdmissionCheckState{
						Name:  "check1",
						State: kueue.CheckStateReady,
					})
					return k8sClient.Status().Patch(ctx, patch, client.Apply, client.FieldOwner("test-admission-check-controller"), client.ForceOwnership)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			expectJobUnsuspendedWithNodeSelectors(jobKey, map[string]string{
				"instance-type": "on-demand",
			})

			ginkgo.By("setting the check as failed (Retry)", func() {
				gomega.Eventually(func() error {
					if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
						return err
					}
					patch := workload.BaseSSAWorkload(createdWorkload)
					workload.SetAdmissionCheckState(&patch.Status.AdmissionChecks, kueue.AdmissionCheckState{
						Name:  "check1",
						State: kueue.CheckStateRetry,
					})
					return k8sClient.Status().Patch(ctx, patch, client.Apply, client.FieldOwner("test-admission-check-controller"), client.ForceOwnership)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("checking the job gets suspended", func() {
				createdJob := &batchv1.Job{}
				jobKey := client.ObjectKeyFromObject(sampleJob)
				gomega.Eventually(func() bool {
					if err := k8sClient.Get(ctx, jobKey, createdJob); err != nil {
						return false
					}
					return ptr.Deref(createdJob.Spec.Suspend, false)

				}, util.Timeout, util.Interval).Should(gomega.BeTrue())
			})
		})
	})
})

func expectJobUnsuspended(key types.NamespacedName) {
	job := &batchv1.Job{}
	gomega.EventuallyWithOffset(1, func() *bool {
		gomega.Expect(k8sClient.Get(ctx, key, job)).To(gomega.Succeed())
		return job.Spec.Suspend
	}, util.Timeout, util.Interval).Should(gomega.Equal(ptr.To(false)))
}

func expectJobUnsuspendedWithNodeSelectors(key types.NamespacedName, ns map[string]string) {
	job := &batchv1.Job{}
	gomega.EventuallyWithOffset(1, func() []any {
		gomega.Expect(k8sClient.Get(ctx, key, job)).To(gomega.Succeed())
		return []any{*job.Spec.Suspend, job.Spec.Template.Spec.NodeSelector}
	}, util.Timeout, util.Interval).Should(gomega.Equal([]any{false, ns}))
}

func defaultOwnerReferenceForJob(name string) []metav1.OwnerReference {
	return []metav1.OwnerReference{
		{
			APIVersion: "batch/v1",
			Kind:       "Job",
			Name:       name,
		},
	}
}
