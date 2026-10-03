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

package baseline

import (
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("TopologyAwareScheduling for Job", ginkgo.Label(behavioral.Shard1, "area:tas", "feature:job"), func() {
	var ns *corev1.Namespace
	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-tas-job-")
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.When("Creating a Job", func() {
		var (
			topology     *kueue.Topology
			tasFlavor    *kueue.ResourceFlavor
			localQueue   *kueue.LocalQueue
			clusterQueue *kueue.ClusterQueue
		)
		ginkgo.BeforeEach(func() {
			topology = utiltestingapi.MakeDefaultThreeLevelTopology("datacenter")
			behavioral.MustCreate(ctx, k8sClient, topology)

			tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
				NodeLabel(tasNodeGroupLabel, instanceType).TopologyName(topology.Name).Obj()
			behavioral.MustCreate(ctx, k8sClient, tasFlavor)

			clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("tas-flavor").
						Resource(extraResource, "8").
						Obj(),
				).
				Obj()
			behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

			localQueue = utiltestingapi.MakeLocalQueue("main", ns.Name).ClusterQueue("cluster-queue").Obj()
			behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(behavioral.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			// Force remove workloads to be sure that cluster queue can be removed.
			gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
			behavioral.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
		})

		ginkgo.It("Should not admit a Job if Rack required", func() {
			sampleJob := testingjob.MakeJob("test-job", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				Parallelism(3).
				Completions(3).
				RequestAndLimit(extraResource, "1").
				Obj()
			sampleJob = (&testingjob.JobWrapper{Job: *sampleJob}).
				PodAnnotation(kueue.PodSetRequiredTopologyAnnotation, utiltesting.DefaultRackTopologyLevel).
				Image(behavioral.GetAgnHostImage(), behavioral.BehaviorExitFast).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, sampleJob)

			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(sampleJob.Name, sampleJob.UID), Namespace: ns.Name}
			ginkgo.By(fmt.Sprintf("workload %q not getting an admission", wlLookupKey), func() {
				createdWorkload := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					g.Expect(createdWorkload.Status.Admission).Should(gomega.BeNil())
				}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should admit a Job to TAS Block if Rack preferred", func() {
			sampleJob := testingjob.MakeJob("test-job", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				Parallelism(3).
				Completions(3).
				RequestAndLimit(extraResource, "1").
				PodAnnotation(kueue.PodSetPreferredTopologyAnnotation, utiltesting.DefaultRackTopologyLevel).
				Image(behavioral.GetAgnHostImage(), behavioral.BehaviorExitFast).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, sampleJob)

			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(sampleJob.Name, sampleJob.UID), Namespace: ns.Name}
			createdWorkload := &kueue.Workload{}
			ginkgo.By(fmt.Sprintf("await for admission of workload %q and verify TopologyAssignment", wlLookupKey), func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					g.Expect(createdWorkload.Status.Admission).ShouldNot(gomega.BeNil())
				}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
				gomega.Expect(createdWorkload.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
				gomega.Expect(createdWorkload.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
					tas.V1Beta2From(&tas.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []tas.TopologyDomainAssignment{
							{
								Values: []string{"kind-worker"},
								Count:  1,
							},
							{
								Values: []string{"kind-worker2"},
								Count:  1,
							},
							{
								Values: []string{"kind-worker3"},
								Count:  1,
							},
						},
					})))
			})
			ginkgo.By(fmt.Sprintf("verify the workload %q gets finished", wlLookupKey), func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					g.Expect(workload.HasQuotaReservation(createdWorkload)).Should(gomega.BeTrue())
					g.Expect(createdWorkload.Status.Conditions).Should(utiltesting.HaveConditionStatusTrue(kueue.WorkloadFinished))
				}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should admit a Job to TAS Block if Block required", func() {
			sampleJob := testingjob.MakeJob("test-job", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				Parallelism(3).
				Completions(3).
				RequestAndLimit(extraResource, "1").
				PodAnnotation(kueue.PodSetRequiredTopologyAnnotation, utiltesting.DefaultBlockTopologyLevel).
				Image(behavioral.GetAgnHostImage(), behavioral.BehaviorExitFast).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, sampleJob)

			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(sampleJob.Name, sampleJob.UID), Namespace: ns.Name}
			createdWorkload := &kueue.Workload{}
			ginkgo.By(fmt.Sprintf("await for admission of workload %q and verify TopologyAssignment", wlLookupKey), func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					g.Expect(createdWorkload.Status.Admission).ShouldNot(gomega.BeNil())
				}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
				gomega.Expect(createdWorkload.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
				gomega.Expect(createdWorkload.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
					tas.V1Beta2From(&tas.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []tas.TopologyDomainAssignment{
							{
								Values: []string{"kind-worker"},
								Count:  1,
							},
							{
								Values: []string{"kind-worker2"},
								Count:  1,
							},
							{
								Values: []string{"kind-worker3"},
								Count:  1,
							},
						}})))
			})

			ginkgo.By(fmt.Sprintf("verify the workload %q gets finished", wlLookupKey), func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					g.Expect(workload.HasQuotaReservation(createdWorkload)).Should(gomega.BeTrue())
					g.Expect(createdWorkload.Status.Conditions).Should(utiltesting.HaveConditionStatusTrue(kueue.WorkloadFinished))
				}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should allow to run a Job with parallelism < completions", func() {
			sampleJob := testingjob.MakeJob("test-job", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				Parallelism(2).
				Completions(3).
				RequestAndLimit(extraResource, "1").
				PodAnnotation(kueue.PodSetRequiredTopologyAnnotation, utiltesting.DefaultBlockTopologyLevel).
				Image(behavioral.GetAgnHostImage(), behavioral.BehaviorExitFast).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, sampleJob)

			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(sampleJob.Name, sampleJob.UID), Namespace: ns.Name}
			createdWorkload := &kueue.Workload{}

			ginkgo.By(fmt.Sprintf("verify the workload %q gets TopologyAssignment becomes finished", wlLookupKey), func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					g.Expect(createdWorkload.Status.Admission).ShouldNot(gomega.BeNil())
					g.Expect(workload.HasQuotaReservation(createdWorkload)).Should(gomega.BeTrue())
					g.Expect(createdWorkload.Status.Conditions).Should(utiltesting.HaveConditionStatusTrue(kueue.WorkloadFinished))
				}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should place pods based on the ranks-ordering", func() {
			numPods := 4
			sampleJob := testingjob.MakeJob("ranks-job", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				Parallelism(int32(numPods)).
				Completions(int32(numPods)).
				Indexed(true).
				RequestAndLimit(extraResource, "1").
				PodAnnotation(kueue.PodSetRequiredTopologyAnnotation, utiltesting.DefaultBlockTopologyLevel).
				Image(behavioral.GetAgnHostImage(), behavioral.BehaviorWaitForDeletion).
				TerminationGracePeriod(1).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, sampleJob)

			ginkgo.By("Job is unsuspended, and has all Pods active and ready", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sampleJob), sampleJob)).To(gomega.Succeed())
					g.Expect(sampleJob.Spec.Suspend).Should(gomega.Equal(new(false)))
				}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sampleJob), sampleJob)).To(gomega.Succeed())
					g.Expect(sampleJob.Status.Active).Should(gomega.Equal(int32(numPods)))
				}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sampleJob), sampleJob)).To(gomega.Succeed())
					g.Expect(sampleJob.Status.Ready).Should(gomega.Equal(new(int32(numPods))))
				}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
			})

			pods := &corev1.PodList{}
			ginkgo.By("ensure all pods are created and scheduled", func() {
				listOpts := &client.ListOptions{
					FieldSelector: fields.OneTermNotEqualSelector("spec.nodeName", ""),
				}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name), listOpts)).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.HaveLen(numPods))
				}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verify the assignment of pods are as expected with rank-based ordering", func() {
				gomega.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name),
					client.MatchingLabels(sampleJob.Spec.Selector.MatchLabels))).To(gomega.Succeed())
				gotAssignment := make(map[string]string, numPods)
				for _, pod := range pods.Items {
					index := pod.Labels[batchv1.JobCompletionIndexAnnotation]
					gotAssignment[index] = pod.Spec.NodeName
				}
				wantAssignment := map[string]string{
					"0": "kind-worker",
					"1": "kind-worker2",
					"2": "kind-worker3",
					"3": "kind-worker4",
				}
				gomega.Expect(wantAssignment).Should(gomega.BeComparableTo(gotAssignment))
			})
		})

		ginkgo.It("Should place pods based on the ranks-ordering even if the Job has no TAS annotation (Implicit TAS)", func() {
			numPods := 4
			sampleJob := testingjob.MakeJob("ranks-job", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				Parallelism(int32(numPods)).
				Completions(int32(numPods)).
				Indexed(true).
				RequestAndLimit(extraResource, "1").
				Image(behavioral.GetAgnHostImage(), behavioral.BehaviorWaitForDeletion).
				TerminationGracePeriod(1).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, sampleJob)

			ginkgo.By("Job is unsuspended, and has all Pods active and ready", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sampleJob), sampleJob)).To(gomega.Succeed())
					g.Expect(sampleJob.Spec.Suspend).Should(gomega.Equal(new(false)))
				}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sampleJob), sampleJob)).To(gomega.Succeed())
					g.Expect(sampleJob.Status.Active).Should(gomega.Equal(int32(numPods)))
				}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sampleJob), sampleJob)).To(gomega.Succeed())
					g.Expect(sampleJob.Status.Ready).Should(gomega.Equal(new(int32(numPods))))
				}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
			})

			pods := &corev1.PodList{}
			ginkgo.By("ensure all pods are created and scheduled", func() {
				listOpts := &client.ListOptions{
					FieldSelector: fields.OneTermNotEqualSelector("spec.nodeName", ""),
				}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name), listOpts)).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.HaveLen(numPods))
				}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verify the assignment of pods are as expected with rank-based ordering", func() {
				gomega.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name),
					client.MatchingLabels(sampleJob.Spec.Selector.MatchLabels))).To(gomega.Succeed())
				gotAssignment := make(map[string]string, numPods)
				for _, pod := range pods.Items {
					index := pod.Labels[batchv1.JobCompletionIndexAnnotation]
					gotAssignment[index] = pod.Spec.NodeName
				}
				wantAssignment := map[string]string{
					"0": "kind-worker",
					"1": "kind-worker2",
					"2": "kind-worker3",
					"3": "kind-worker4",
				}
				gomega.Expect(wantAssignment).Should(gomega.BeComparableTo(gotAssignment))
			})
		})
		ginkgo.It("should preserve topology assignment during scale-up with elastic jobs", func() {
			sampleJob := testingjob.MakeJob("test-job", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				SetAnnotation("kueue.x-k8s.io/elastic-job", "true").
				Parallelism(1).
				Completions(10).
				RequestAndLimit(extraResource, "1").
				PodAnnotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
				Image(behavioral.GetAgnHostImage(), behavioral.BehaviorWaitForDeletion).
				TerminationGracePeriod(1).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, sampleJob)

			var createdWorkload *kueue.Workload
			ginkgo.By("await for admission of workload and record topology", func() {
				// Use ExpectWorkloadsInNamespace instead of computing workload name
				// because with workload slicing enabled, the name includes generation
				workloads := behavioral.ExpectWorkloadsInNamespace(ctx, k8sClient, ns.Name, 1)
				createdWorkload = &workloads[0]

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(createdWorkload), createdWorkload)).Should(gomega.Succeed())
					g.Expect(createdWorkload.Status.Admission).ShouldNot(gomega.BeNil())
					g.Expect(createdWorkload.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
					g.Expect(createdWorkload.Status.Admission.PodSetAssignments[0].TopologyAssignment).ShouldNot(gomega.BeNil())
				}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())

				ta := tas.InternalFrom(createdWorkload.Status.Admission.PodSetAssignments[0].TopologyAssignment)
				gomega.Expect(ta.Domains).ShouldNot(gomega.BeEmpty())
			})

			scaledParallelism := int32(2)
			ginkgo.By("scale up the job parallelism", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sampleJob), sampleJob)).To(gomega.Succeed())
					sampleJob.Spec.Parallelism = &scaledParallelism
					g.Expect(k8sClient.Update(ctx, sampleJob)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			var scaledWorkload *kueue.Workload
			ginkgo.By("verify the scaled workload has two assigned domains", func() {
				scaledWorkload = behavioral.ExpectNewWorkloadSlice(
					ctx,
					k8sClient,
					createdWorkload,
				)

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(scaledWorkload), scaledWorkload)).Should(gomega.Succeed())
					g.Expect(scaledWorkload.Status.Admission).ShouldNot(gomega.BeNil())
					g.Expect(scaledWorkload.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
					g.Expect(tas.TotalDomainCount(
						scaledWorkload.Status.Admission.PodSetAssignments[0].TopologyAssignment,
					)).Should(gomega.Equal(int(scaledParallelism)))
				}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verify both job pods are ready", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(
						k8sClient.Get(
							ctx,
							client.ObjectKeyFromObject(sampleJob),
							sampleJob,
						),
					).To(gomega.Succeed())

					g.Expect(sampleJob.Status.Ready).To(
						gomega.HaveValue(gomega.Equal(scaledParallelism)),
					)
				}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())
			})
		})
	})
})
