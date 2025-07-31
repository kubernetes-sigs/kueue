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

package jobset

import (
	"fmt"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	jobsetapi "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	workloadjobset "sigs.k8s.io/kueue/pkg/controller/jobs/jobset"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingjobset "sigs.k8s.io/kueue/pkg/util/testingjobs/jobset"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

const (
	jobSetName              = "test-job"
	instanceKey             = "cloud.provider.com/instance"
	priorityClassName       = "test-priority-class"
	priorityValue     int32 = 10
)

var _ = ginkgo.Describe("JobSet controller", ginkgo.Ordered, ginkgo.ContinueOnFailure, ginkgo.ContinueOnFailure, func() {
	var realClock = clock.RealClock{}

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
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "jobset-")
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.When("basic setup", func() {
		var (
			clusterQueue   *kueue.ClusterQueue
			localQueue     *kueue.LocalQueue
			onDemandFlavor *kueue.ResourceFlavor
			spotFlavor     *kueue.ResourceFlavor
		)

		ginkgo.BeforeEach(func() {
			clusterQueue = testing.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj(),
					*testing.MakeFlavorQuotas("spot").Resource(corev1.ResourceCPU, "5").Obj(),
				).Obj()

			util.MustCreate(ctx, k8sClient, clusterQueue)
			localQueue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			util.MustCreate(ctx, k8sClient, localQueue)
			onDemandFlavor = testing.MakeResourceFlavor("on-demand").NodeLabel(instanceKey, "on-demand").Obj()
			util.MustCreate(ctx, k8sClient, onDemandFlavor)
			spotFlavor = testing.MakeResourceFlavor("spot").NodeLabel(instanceKey, "spot").Obj()
			util.MustCreate(ctx, k8sClient, spotFlavor)
		})

		ginkgo.AfterEach(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, spotFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		})

		ginkgo.It("Should reconcile JobSets", func() {
			ginkgo.By("checking the JobSet gets suspended when created unsuspended")
			priorityClass := testing.MakePriorityClass(priorityClassName).
				PriorityValue(priorityValue).Obj()
			util.MustCreate(ctx, k8sClient, priorityClass)

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

			util.MustCreate(ctx, k8sClient, jobSet)
			createdJobSet := &jobsetapi.JobSet{}

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: jobSetName, Namespace: ns.Name}, createdJobSet)).Should(gomega.Succeed())
				g.Expect(ptr.Deref(createdJobSet.Spec.Suspend, false)).Should(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("checking the workload is created without queue assigned")
			createdWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: workloadjobset.GetWorkloadNameForJobSet(jobSet.Name, jobSet.UID), Namespace: ns.Name}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			gomega.Expect(createdWorkload.Spec.QueueName).Should(gomega.Equal(kueue.LocalQueueName("")), "The Workload shouldn't have .spec.queueName set")
			gomega.Expect(metav1.IsControlledBy(createdWorkload, createdJobSet)).To(gomega.BeTrue(), "The Workload should be owned by the JobSet")

			ginkgo.By("checking the workload is created with priority and priorityName")
			gomega.Expect(createdWorkload.Spec.PriorityClassName).Should(gomega.Equal(priorityClassName))
			gomega.Expect(*createdWorkload.Spec.Priority).Should(gomega.Equal(priorityValue))

			ginkgo.By("checking the workload is updated with queue name when the JobSet does")
			var jobSetQueueName kueue.LocalQueueName = "test-queue"
			createdJobSet.Annotations = map[string]string{constants.QueueLabel: string(jobSetQueueName)}
			gomega.Expect(k8sClient.Update(ctx, createdJobSet)).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				g.Expect(createdWorkload.Spec.QueueName).Should(gomega.Equal(jobSetQueueName))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("checking a second non-matching workload is deleted")
			secondWl := &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{
					Name:      workloadjobset.GetWorkloadNameForJobSet("second-workload", "test-uid"),
					Namespace: createdWorkload.Namespace,
				},
				Spec: *createdWorkload.Spec.DeepCopy(),
			}
			gomega.Expect(ctrl.SetControllerReference(createdJobSet, secondWl, k8sClient.Scheme())).Should(gomega.Succeed())
			secondWl.Spec.PodSets[0].Count++
			util.MustCreate(ctx, k8sClient, secondWl)
			key := types.NamespacedName{Name: secondWl.Name, Namespace: secondWl.Namespace}
			gomega.Eventually(func(g gomega.Gomega) {
				wl := &kueue.Workload{}
				g.Expect(k8sClient.Get(ctx, key, wl)).Should(testing.BeNotFoundError())
				// check the original wl is still there
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("checking the JobSet is unsuspended when workload is assigned")

			admission := testing.MakeAdmission(clusterQueue.Name).PodSets(
				kueue.PodSetAssignment{
					Name: createdWorkload.Spec.PodSets[0].Name,
					Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
						corev1.ResourceCPU: kueue.ResourceFlavorReference(onDemandFlavor.Name),
					},
				}, kueue.PodSetAssignment{
					Name: createdWorkload.Spec.PodSets[1].Name,
					Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
						corev1.ResourceCPU: kueue.ResourceFlavorReference(spotFlavor.Name),
					},
				},
			).Obj()
			gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, admission)).Should(gomega.Succeed())
			util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)

			lookupKey := types.NamespacedName{Name: jobSetName, Namespace: ns.Name}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, lookupKey, createdJobSet)).Should(gomega.Succeed())
				g.Expect(ptr.Deref(createdJobSet.Spec.Suspend, false)).Should(gomega.BeFalse())
				ok, _ := testing.CheckEventRecordedFor(ctx, k8sClient, "Started", corev1.EventTypeNormal, fmt.Sprintf("Admitted by clusterQueue %v", clusterQueue.Name), lookupKey)
				g.Expect(ok).Should(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			gomega.Expect(createdJobSet.Spec.ReplicatedJobs[0].Template.Spec.Template.Spec.NodeSelector).Should(gomega.Equal(map[string]string{instanceKey: onDemandFlavor.Name}))
			gomega.Expect(createdJobSet.Spec.ReplicatedJobs[1].Template.Spec.Template.Spec.NodeSelector).Should(gomega.Equal(map[string]string{instanceKey: spotFlavor.Name}))
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, clusterQueue.Name, createdWorkload)

			ginkgo.By("checking the JobSet gets suspended when parallelism changes and the added node selectors are removed")
			parallelism := jobSet.Spec.ReplicatedJobs[0].Replicas
			newParallelism := parallelism + 1
			createdJobSet.Spec.ReplicatedJobs[0].Replicas = newParallelism
			gomega.Expect(k8sClient.Update(ctx, createdJobSet)).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, lookupKey, createdJobSet)).Should(gomega.Succeed())
				g.Expect(*createdJobSet.Spec.Suspend).Should(gomega.BeTrue())
				g.Expect(jobSet.Spec.ReplicatedJobs[0].Template.Spec.Template.Spec.NodeSelector).Should(gomega.BeEmpty())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				ok, _ := testing.CheckEventRecordedFor(ctx, k8sClient, "DeletedWorkload", corev1.EventTypeNormal, fmt.Sprintf("Deleted not matching Workload: %v", wlLookupKey.String()), lookupKey)
				g.Expect(ok).Should(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("checking the workload is updated with new count")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				g.Expect(createdWorkload.Spec.PodSets[0].Count).Should(gomega.Equal(newParallelism))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			gomega.Expect(createdWorkload.Status.Admission).Should(gomega.BeNil())

			ginkgo.By("checking the JobSet is unsuspended and selectors added when workload is assigned again")
			admission = testing.MakeAdmission(clusterQueue.Name).
				PodSets(
					kueue.PodSetAssignment{
						Name: "replicated-job-1",
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: kueue.ResourceFlavorReference(onDemandFlavor.Name),
						},
						Count: ptr.To(createdWorkload.Spec.PodSets[0].Count),
					},
					kueue.PodSetAssignment{
						Name: "replicated-job-2",
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: kueue.ResourceFlavorReference(spotFlavor.Name),
						},
						Count: ptr.To(createdWorkload.Spec.PodSets[1].Count),
					},
				).
				Obj()
			gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, admission)).Should(gomega.Succeed())
			util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, lookupKey, createdJobSet)).Should(gomega.Succeed())
				g.Expect(*createdJobSet.Spec.Suspend).Should(gomega.BeFalse())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

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
			util.ExpectWorkloadToFinish(ctx, k8sClient, wlLookupKey)
		})

		ginkgo.It("A jobset created in an unmanaged namespace is not suspended and a workload is not created", func() {
			ginkgo.By("Creating an unsuspended job without a queue-name in unmanaged-ns")
			jobSet := testingjobset.MakeJobSet(jobSetName, "unmanaged-ns").ReplicatedJobs(
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

			util.MustCreate(ctx, k8sClient, jobSet)
			createdJobSet := &jobsetapi.JobSet{}
			wlLookupKey := types.NamespacedName{Name: workloadjobset.GetWorkloadNameForJobSet(jobSet.Name, jobSet.UID), Namespace: ns.Name}
			createdWorkload := &kueue.Workload{}

			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: jobSetName, Namespace: jobSet.Namespace}, createdJobSet)).Should(gomega.Succeed())
				g.Expect(ptr.Deref(createdJobSet.Spec.Suspend, false)).Should(gomega.BeFalse())
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(testing.BeNotFoundError())
			}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
		})

		ginkgo.It("Should finish the preemption when the jobset becomes inactive", func() {
			jobSet := testingjobset.MakeJobSet(jobSetName, ns.Name).ReplicatedJobs(
				testingjobset.ReplicatedJobRequirements{
					Name:        "replicated-job-1",
					Replicas:    1,
					Parallelism: 1,
					Completions: 1,
				},
			).Queue(localQueue.Name).
				Suspend(false).
				Obj()
			createdWorkload := &kueue.Workload{}
			var wlLookupKey types.NamespacedName

			ginkgo.By("create the jobset and admit the workload", func() {
				util.MustCreate(ctx, k8sClient, jobSet)
				wlLookupKey = types.NamespacedName{Name: workloadjobset.GetWorkloadNameForJobSet(jobSet.Name, jobSet.UID), Namespace: ns.Name}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
				admission := testing.MakeAdmission(localQueue.Name).PodSets(
					kueue.PodSetAssignment{
						Name: createdWorkload.Spec.PodSets[0].Name,
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: kueue.ResourceFlavorReference(onDemandFlavor.Name),
						},
					},
				).Obj()
				gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, admission)).To(gomega.Succeed())
				util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
			})

			ginkgo.By("wait for the jobset to be unsuspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: jobSetName, Namespace: ns.Name}, jobSet)).Should(gomega.Succeed())
					g.Expect(ptr.Deref(jobSet.Spec.Suspend, false)).Should(gomega.BeFalse())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("mark the jobset as active", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(jobSet), jobSet)).To(gomega.Succeed())
					jobSet.Status.ReplicatedJobsStatus = make([]jobsetapi.ReplicatedJobStatus, 1)
					jobSet.Status.ReplicatedJobsStatus[0].Active = 1
					jobSet.Status.ReplicatedJobsStatus[0].Name = jobSet.Spec.ReplicatedJobs[0].Name
					g.Expect(k8sClient.Status().Update(ctx, jobSet)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("preempt the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					g.Expect(workload.UpdateStatus(ctx, k8sClient, createdWorkload, kueue.WorkloadEvicted, metav1.ConditionTrue, kueue.WorkloadEvictedByPreemption, "By test", "evict", clock.RealClock{})).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("wait for the jobset to be suspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(jobSet), jobSet)).To(gomega.Succeed())
					g.Expect(*jobSet.Spec.Suspend).To(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("the workload should stay admitted", func() {
				gomega.Consistently(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					g.Expect(createdWorkload.Status.Conditions).To(testing.HaveConditionStatusTrue(kueue.WorkloadQuotaReserved))
				}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
			})

			ginkgo.By("mark the jobset as inactive", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(jobSet), jobSet)).To(gomega.Succeed())
					jobSet.Status.ReplicatedJobsStatus[0].Active = 0
					g.Expect(k8sClient.Status().Update(ctx, jobSet)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("the workload should get unadmitted", func() {
				util.ExpectWorkloadsToBePending(ctx, k8sClient, createdWorkload)
			})
		})

		ginkgo.It("Should allow to create jobset with one replicated job replica count 0", func() {
			mixJobSet := testingjobset.MakeJobSet("mix-jobset", ns.Name).ReplicatedJobs(
				testingjobset.ReplicatedJobRequirements{
					Name:        "replicated-job-1",
					Replicas:    2,
					Parallelism: 2,
					Completions: 2,
				}, testingjobset.ReplicatedJobRequirements{
					Name:        "replicated-job-2-empty",
					Replicas:    0,
					Parallelism: 0,
					Completions: 0,
				},
			).Queue(localQueue.Name).
				Request("replicated-job-1", corev1.ResourceCPU, "250m").
				Request("replicated-job-2-empty", corev1.ResourceCPU, "250m").
				Obj()

			jobSetLookupKey := types.NamespacedName{Name: mixJobSet.Name, Namespace: ns.Name}
			ginkgo.By("create the jobset", func() {
				util.MustCreate(ctx, k8sClient, mixJobSet)
				gomega.Eventually(func(g gomega.Gomega) {
					createdJobSet1 := &jobsetapi.JobSet{}
					g.Expect(k8sClient.Get(ctx, jobSetLookupKey, createdJobSet1)).Should(gomega.Succeed())
					g.Expect(*createdJobSet1.Spec.Suspend).Should(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			wlLookupKey := types.NamespacedName{Name: workloadjobset.GetWorkloadNameForJobSet(mixJobSet.Name, mixJobSet.UID), Namespace: ns.Name}
			ginkgo.By("waiting for workload to be created", func() {
				createdWorkload := &kueue.Workload{}
				checkPSOpts := cmpopts.IgnoreFields(kueue.PodSet{}, "Template", "MinCount")
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					g.Expect(createdWorkload.Spec.PodSets).To(gomega.ContainElements(
						gomega.BeComparableTo(kueue.PodSet{Name: "replicated-job-1", Count: 4}, checkPSOpts),
						gomega.BeComparableTo(kueue.PodSet{Name: "replicated-job-2-empty", Count: 0}, checkPSOpts),
					))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("admit the workload", func() {
				createdWorkload := &kueue.Workload{}
				gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				admission := testing.MakeAdmission(clusterQueue.Name).
					PodSets(
						kueue.PodSetAssignment{
							Name: createdWorkload.Spec.PodSets[0].Name,
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: kueue.ResourceFlavorReference(spotFlavor.Name),
							},
						}, kueue.PodSetAssignment{
							Name: createdWorkload.Spec.PodSets[1].Name,
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: kueue.ResourceFlavorReference(spotFlavor.Name),
							},
						},
					).
					Obj()
				gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, admission)).Should(gomega.Succeed())
				util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
			})

			ginkgo.By("checking the jobset gets unsuspended", func() {
				createdJobSet1 := &jobsetapi.JobSet{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, jobSetLookupKey, createdJobSet1)).Should(gomega.Succeed())
					g.Expect(ptr.Deref(createdJobSet1.Spec.Suspend, false)).To(gomega.BeFalse())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
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

			jobLookupKey = &types.NamespacedName{Name: jobSetName, Namespace: ns.Name}
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteObject(ctx, k8sClient, admissionCheck)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, testFlavor, true)
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueueAc, true)
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
				util.MustCreate(ctx, k8sClient, job)
			})

			ginkgo.By("fetch the job and verify it is suspended as the checks are not ready", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, *jobLookupKey, createdJob)).Should(gomega.Succeed())
					g.Expect(*createdJob.Spec.Suspend).Should(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			wlLookupKey := &types.NamespacedName{Name: workloadjobset.GetWorkloadNameForJobSet(job.Name, job.UID), Namespace: ns.Name}
			ginkgo.By("checking the workload is created", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, *wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("add labels & annotations to the admission check in PodSetUpdates", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					var newWL kueue.Workload
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(createdWorkload), &newWL)).To(gomega.Succeed())
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
					}, realClock)
					g.Expect(k8sClient.Status().Update(ctx, &newWL)).Should(gomega.Succeed())
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
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, *jobLookupKey, createdJob)).Should(gomega.Succeed())
					g.Expect(*createdJob.Spec.Suspend).Should(gomega.BeFalse())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
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
					g.Expect(*createdJob.Spec.Suspend).Should(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
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
		fwk.StartManager(ctx, cfg, managerSetup())
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	var (
		ns *corev1.Namespace
	)
	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "jobset-")
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
		util.MustCreate(ctx, k8sClient, jobSet)
		lookupKey := types.NamespacedName{Name: jobSetName, Namespace: ns.Name}
		createdJobSet := &jobsetapi.JobSet{}
		gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJobSet)).Should(gomega.Succeed())

		createdWorkload := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadjobset.GetWorkloadNameForJobSet(jobSet.Name, jobSet.UID), Namespace: ns.Name}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(testing.BeNotFoundError())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("checking the workload is created when queue name is set")
		jobQueueName := "test-queue"
		if createdJobSet.Labels == nil {
			createdJobSet.Labels = map[string]string{constants.QueueLabel: jobQueueName}
		} else {
			createdJobSet.Labels[constants.QueueLabel] = jobQueueName
		}
		gomega.Expect(k8sClient.Update(ctx, createdJobSet)).Should(gomega.Succeed())
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
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

	var defaultFlavor = testing.MakeResourceFlavor("default").NodeLabel(instanceKey, "default").Obj()

	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup(jobframework.WithWaitForPodsReady(&configapi.WaitForPodsReady{Enable: true})))

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
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "jobset-")
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
			util.MustCreate(ctx, k8sClient, jobSet)
			lookupKey := types.NamespacedName{Name: jobSetName, Namespace: ns.Name}
			createdJobSet := &jobsetapi.JobSet{}
			gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJobSet)).Should(gomega.Succeed())

			ginkgo.By("Fetch the workload created for the JobSet")
			createdWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: workloadjobset.GetWorkloadNameForJobSet(jobSet.Name, jobSet.UID), Namespace: ns.Name}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
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
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, lookupKey, createdJobSet)).Should(gomega.Succeed())
				g.Expect(createdJobSet.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			if podsReadyTestSpec.beforeJobSetStatus != nil {
				ginkgo.By("Update the JobSet status to simulate its initial progress towards completion")
				createdJobSet.Status = *podsReadyTestSpec.beforeJobSetStatus
				gomega.Expect(k8sClient.Status().Update(ctx, createdJobSet)).Should(gomega.Succeed())
				gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJobSet)).Should(gomega.Succeed())
			}

			if podsReadyTestSpec.beforeCondition != nil {
				ginkgo.By("Update the workload status")
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					cond := apimeta.FindStatusCondition(createdWorkload.Status.Conditions, kueue.WorkloadPodsReady)
					g.Expect(cond).Should(gomega.BeComparableTo(podsReadyTestSpec.beforeCondition, util.IgnoreConditionTimestampsAndObservedGeneration))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			}

			ginkgo.By("Update the JobSet status to simulate its progress towards completion")
			createdJobSet.Status = podsReadyTestSpec.jobSetStatus
			gomega.Expect(k8sClient.Status().Update(ctx, createdJobSet)).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJobSet)).Should(gomega.Succeed())

			if podsReadyTestSpec.suspended {
				ginkgo.By("Unset admission of the workload to suspend the JobSet")
				gomega.Eventually(func(g gomega.Gomega) {
					// the update may need to be retried due to a conflict as the workload gets
					// also updated due to setting of the job status.
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					g.Expect(util.SetQuotaReservation(ctx, k8sClient, createdWorkload, nil)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
				util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
			}

			ginkgo.By("Verify the PodsReady condition is added")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				cond := apimeta.FindStatusCondition(createdWorkload.Status.Conditions, kueue.WorkloadPodsReady)
				g.Expect(cond).Should(gomega.BeComparableTo(podsReadyTestSpec.wantCondition, util.IgnoreConditionTimestampsAndObservedGeneration))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		},
		ginkgo.Entry("No progress", podsReadyTestSpec{
			wantCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionFalse,
				Reason:  kueue.WorkloadWaitForStart,
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
				Reason:  kueue.WorkloadStarted,
				Message: "All pods reached readiness and the workload is running",
			},
		}),
		ginkgo.Entry("Running JobSet; PodsReady=False before", podsReadyTestSpec{
			beforeCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionFalse,
				Reason:  kueue.WorkloadWaitForStart,
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
				Reason:  kueue.WorkloadStarted,
				Message: "All pods reached readiness and the workload is running",
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
				Reason:  kueue.WorkloadStarted,
				Message: "All pods reached readiness and the workload is running",
			},
			suspended: true,
			wantCondition: &metav1.Condition{
				Type:    kueue.WorkloadPodsReady,
				Status:  metav1.ConditionFalse,
				Reason:  kueue.WorkloadWaitForStart,
				Message: "Not all pods are ready or succeeded",
			},
		}),
	)
})

var _ = ginkgo.Describe("JobSet controller interacting with scheduler", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup(false))
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	var (
		ns                  *corev1.Namespace
		onDemandFlavor      *kueue.ResourceFlavor
		spotUntaintedFlavor *kueue.ResourceFlavor
		clusterQueue        *kueue.ClusterQueue
		localQueue          *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "jobset-")

		onDemandFlavor = testing.MakeResourceFlavor("on-demand").NodeLabel(instanceKey, "on-demand").Obj()
		util.MustCreate(ctx, k8sClient, onDemandFlavor)

		spotUntaintedFlavor = testing.MakeResourceFlavor("spot-untainted").NodeLabel(instanceKey, "spot-untainted").Obj()
		util.MustCreate(ctx, k8sClient, spotUntaintedFlavor)

		clusterQueue = testing.MakeClusterQueue("dev-clusterqueue").
			ResourceGroup(
				*testing.MakeFlavorQuotas("spot-untainted").Resource(corev1.ResourceCPU, "1").Obj(),
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

	ginkgo.It("Should schedule JobSets as they fit in their ClusterQueue", func() {
		ginkgo.By("creating localQueue")
		localQueue = testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		util.MustCreate(ctx, k8sClient, localQueue)

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
		util.MustCreate(ctx, k8sClient, jobSet)
		createdJobSet := &jobsetapi.JobSet{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: jobSet.Name, Namespace: jobSet.Namespace}, createdJobSet)).
				Should(gomega.Succeed())
			g.Expect(*createdJobSet.Spec.Suspend).Should(gomega.BeFalse())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		fmt.Println(createdJobSet.Spec.ReplicatedJobs[0].Template.Spec.Template.Spec.NodeSelector)
		gomega.Expect(createdJobSet.Spec.ReplicatedJobs[0].Template.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(spotUntaintedFlavor.Name))
		gomega.Expect(createdJobSet.Spec.ReplicatedJobs[1].Template.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))
		util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
		util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
	})

	ginkgo.It("Should allow reclaim of resources that are no longer needed", func() {
		ginkgo.By("creating localQueue", func() {
			localQueue = testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			util.MustCreate(ctx, k8sClient, localQueue)
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
			util.MustCreate(ctx, k8sClient, jobSet1)
			createdJobSet1 := &jobsetapi.JobSet{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, lookupKey1, createdJobSet1)).Should(gomega.Succeed())
				g.Expect(*createdJobSet1.Spec.Suspend).Should(gomega.BeFalse())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
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
			util.MustCreate(ctx, k8sClient, jobSet2)
			createdJobSet2 := &jobsetapi.JobSet{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, lookupKey2, createdJobSet2)).Should(gomega.Succeed())
				g.Expect(*createdJobSet2.Spec.Suspend).Should(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
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
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).Should(gomega.Succeed())
				g.Expect(wl.Status.ReclaimablePods).Should(gomega.BeComparableTo([]kueue.ReclaimablePod{
					{
						Name:  "replicated-job-1",
						Count: 8,
					},
					{
						Name:  "replicated-job-2",
						Count: 4,
					},
				}))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			createdJobSet2 := &jobsetapi.JobSet{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, lookupKey2, createdJobSet2)).Should(gomega.Succeed())
				g.Expect(*createdJobSet2.Spec.Suspend).Should(gomega.BeFalse())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
			util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 2)
		})
	})
})

var _ = ginkgo.Describe("JobSet controller when TopologyAwareScheduling enabled", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
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

		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "tas-jobset-")

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
		jobSet := testingjobset.MakeJobSet(jobSetName, ns.Name).
			Queue(localQueue.Name).
			ReplicatedJobs(
				testingjobset.ReplicatedJobRequirements{
					Name:        "rj1",
					Replicas:    1,
					Parallelism: 1,
					Completions: 1,
					PodAnnotations: map[string]string{
						kueuealpha.PodSetRequiredTopologyAnnotation: testing.DefaultBlockTopologyLevel,
					},
					Image: util.GetAgnHostImage(),
					Args:  util.BehaviorExitFast,
				},
				testingjobset.ReplicatedJobRequirements{
					Name:        "rj2",
					Replicas:    1,
					Parallelism: 1,
					Completions: 1,
					PodAnnotations: map[string]string{
						kueuealpha.PodSetPreferredTopologyAnnotation: testing.DefaultRackTopologyLevel,
					},
					Image: util.GetAgnHostImage(),
					Args:  util.BehaviorExitFast,
				},
			).
			Request("rj1", corev1.ResourceCPU, "100m").
			Request("rj2", corev1.ResourceCPU, "100m").
			Obj()
		ginkgo.By("creating a JobSet", func() {
			util.MustCreate(ctx, k8sClient, jobSet)
		})

		wl := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{
			Name:      workloadjobset.GetWorkloadNameForJobSet(jobSet.Name, jobSet.UID),
			Namespace: ns.Name,
		}

		ginkgo.By("verify the workload is created", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, wl)).Should(gomega.Succeed())
				g.Expect(wl.Spec.PodSets).Should(gomega.BeComparableTo([]kueue.PodSet{
					{
						Name:  "rj1",
						Count: 1,
						TopologyRequest: &kueue.PodSetTopologyRequest{
							Required:           ptr.To(testing.DefaultBlockTopologyLevel),
							PodIndexLabel:      ptr.To(batchv1.JobCompletionIndexAnnotation),
							SubGroupIndexLabel: ptr.To(jobsetapi.JobIndexKey),
							SubGroupCount:      ptr.To[int32](1),
						},
					},
					{
						Name:  "rj2",
						Count: 1,
						TopologyRequest: &kueue.PodSetTopologyRequest{
							Preferred:          ptr.To(testing.DefaultRackTopologyLevel),
							PodIndexLabel:      ptr.To(batchv1.JobCompletionIndexAnnotation),
							SubGroupIndexLabel: ptr.To(jobsetapi.JobIndexKey),
							SubGroupCount:      ptr.To[int32](1),
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
