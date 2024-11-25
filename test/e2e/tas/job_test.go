/*
Copyright 2024 The Kubernetes Authors.

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

package tase2e

import (
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

const (
	instanceType          = "tas-group"
	tasNodeGroupLabel     = "cloud.provider.com/node-group"
	topologyLevelRack     = "cloud.provider.com/topology-rack"
	topologyLevelBlock    = "cloud.provider.com/topology-block"
	topologyLevelHostname = "kubernetes.io/hostname"
	extraResource         = "example.com/gpu"
)

var _ = ginkgo.Describe("TopologyAwareScheduling for Job", func() {
	var ns *corev1.Namespace
	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "e2e-tas-job-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.When("Creating a Job", func() {
		var (
			topology     *kueuealpha.Topology
			tasFlavor    *kueue.ResourceFlavor
			localQueue   *kueue.LocalQueue
			clusterQueue *kueue.ClusterQueue
		)
		ginkgo.BeforeEach(func() {
			topology = testing.MakeTopology("datacenter").Levels([]string{
				topologyLevelBlock,
				topologyLevelRack,
				topologyLevelHostname,
			}).Obj()
			gomega.Expect(k8sClient.Create(ctx, topology)).Should(gomega.Succeed())

			tasFlavor = testing.MakeResourceFlavor("tas-flavor").
				NodeLabel(tasNodeGroupLabel, instanceType).TopologyName(topology.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, tasFlavor)).Should(gomega.Succeed())
			clusterQueue = testing.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*testing.MakeFlavorQuotas("tas-flavor").
						Resource(extraResource, "8").
						Obj(),
				).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).Should(gomega.Succeed())
			util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

			localQueue = testing.MakeLocalQueue("main", ns.Name).ClusterQueue("cluster-queue").Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).Should(gomega.Succeed())
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			// Force remove workloads to be sure that cluster queue can be removed.
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteObject(ctx, k8sClient, topology)).Should(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
		})

		ginkgo.It("Should not admit a Job if Rack required", func() {
			sampleJob := testingjob.MakeJob("test-job", ns.Name).
				Queue(localQueue.Name).
				Parallelism(3).
				Completions(3).
				Request(extraResource, "1").
				Limit(extraResource, "1").
				Obj()
			sampleJob = (&testingjob.JobWrapper{Job: *sampleJob}).
				PodAnnotation(kueuealpha.PodSetRequiredTopologyAnnotation, topologyLevelRack).
				Image(util.E2eTestSleepImage, []string{"100ms"}).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, sampleJob)).Should(gomega.Succeed())

			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(sampleJob.Name, sampleJob.UID), Namespace: ns.Name}
			ginkgo.By(fmt.Sprintf("workload %q not getting an admission", wlLookupKey), func() {
				createdWorkload := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					g.Expect(createdWorkload.Status.Admission).Should(gomega.BeNil())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should admit a Job to TAS Block if Rack preferred", func() {
			sampleJob := testingjob.MakeJob("test-job", ns.Name).
				Queue(localQueue.Name).
				Parallelism(3).
				Completions(3).
				Request(extraResource, "1").
				Limit(extraResource, "1").
				Obj()
			sampleJob = (&testingjob.JobWrapper{Job: *sampleJob}).
				PodAnnotation(kueuealpha.PodSetPreferredTopologyAnnotation, topologyLevelRack).
				Image(util.E2eTestSleepImage, []string{"100ms"}).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, sampleJob)).Should(gomega.Succeed())

			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(sampleJob.Name, sampleJob.UID), Namespace: ns.Name}
			createdWorkload := &kueue.Workload{}
			ginkgo.By(fmt.Sprintf("await for admission of workload %q and verify TopologyAssignment", wlLookupKey), func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					g.Expect(createdWorkload.Status.Admission).ShouldNot(gomega.BeNil())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
				gomega.Expect(createdWorkload.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
				gomega.Expect(createdWorkload.Status.Admission.PodSetAssignments[0].TopologyAssignment.Levels).Should(gomega.BeComparableTo(
					[]string{
						topologyLevelBlock,
						topologyLevelRack,
						topologyLevelHostname,
					},
				))
				podCountPerBlock := map[string]int32{}
				for _, d := range createdWorkload.Status.Admission.PodSetAssignments[0].TopologyAssignment.Domains {
					podCountPerBlock[d.Values[0]] += d.Count
				}
				// both pod assignments are in the same block
				gomega.Expect(podCountPerBlock).Should(gomega.HaveLen(1))
				// pod assignment count equals job parallelism
				for _, pd := range podCountPerBlock {
					gomega.Expect(pd).Should(gomega.Equal(ptr.Deref[int32](sampleJob.Spec.Parallelism, 0)))
				}
			})
			ginkgo.By(fmt.Sprintf("verify the workload %q gets finished", wlLookupKey), func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					g.Expect(workload.HasQuotaReservation(createdWorkload)).Should(gomega.BeTrue())
					g.Expect(createdWorkload.Status.Conditions).Should(testing.HaveConditionStatusTrue(kueue.WorkloadFinished))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should admit a Job to TAS Block if Block required", func() {
			sampleJob := testingjob.MakeJob("test-job", ns.Name).
				Queue(localQueue.Name).
				Parallelism(3).
				Completions(3).
				Request(extraResource, "1").
				Limit(extraResource, "1").
				Obj()
			sampleJob = (&testingjob.JobWrapper{Job: *sampleJob}).
				PodAnnotation(kueuealpha.PodSetRequiredTopologyAnnotation, topologyLevelBlock).
				Image(util.E2eTestSleepImage, []string{"100ms"}).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, sampleJob)).Should(gomega.Succeed())

			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(sampleJob.Name, sampleJob.UID), Namespace: ns.Name}
			createdWorkload := &kueue.Workload{}
			ginkgo.By(fmt.Sprintf("await for admission of workload %q and verify TopologyAssignment", wlLookupKey), func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					g.Expect(createdWorkload.Status.Admission).ShouldNot(gomega.BeNil())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
				gomega.Expect(createdWorkload.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
				gomega.Expect(createdWorkload.Status.Admission.PodSetAssignments[0].TopologyAssignment.Levels).Should(gomega.BeComparableTo(
					[]string{
						topologyLevelBlock,
						topologyLevelRack,
						topologyLevelHostname,
					},
				))
				podCountPerBlock := map[string]int32{}
				for _, d := range createdWorkload.Status.Admission.PodSetAssignments[0].TopologyAssignment.Domains {
					podCountPerBlock[d.Values[0]] += d.Count
				}
				// both pod assignments are in the same block
				gomega.Expect(podCountPerBlock).Should(gomega.HaveLen(1))
				// pod assignment count equals job parallelism
				for _, pd := range podCountPerBlock {
					gomega.Expect(pd).Should(gomega.Equal(ptr.Deref[int32](sampleJob.Spec.Parallelism, 0)))
				}
			})

			ginkgo.By(fmt.Sprintf("verify the workload %q gets finished", wlLookupKey), func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					g.Expect(workload.HasQuotaReservation(createdWorkload)).Should(gomega.BeTrue())
					g.Expect(createdWorkload.Status.Conditions).Should(testing.HaveConditionStatusTrue(kueue.WorkloadFinished))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should allow to run a Job with parallelism < completions", func() {
			sampleJob := testingjob.MakeJob("test-job", ns.Name).
				Queue(localQueue.Name).
				Parallelism(2).
				Completions(3).
				Request(extraResource, "1").
				Limit(extraResource, "1").
				Obj()
			sampleJob = (&testingjob.JobWrapper{Job: *sampleJob}).
				PodAnnotation(kueuealpha.PodSetRequiredTopologyAnnotation, topologyLevelBlock).
				Image(util.E2eTestSleepImage, []string{"10ms"}).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, sampleJob)).Should(gomega.Succeed())

			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(sampleJob.Name, sampleJob.UID), Namespace: ns.Name}
			createdWorkload := &kueue.Workload{}

			ginkgo.By(fmt.Sprintf("verify the workload %q gets TopologyAssignment becomes finished", wlLookupKey), func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					g.Expect(createdWorkload.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
					g.Expect(createdWorkload.Status.Admission.PodSetAssignments[0].TopologyAssignment).ShouldNot(gomega.BeNil())
					g.Expect(createdWorkload.Status.Conditions).Should(testing.HaveConditionStatusTrue(kueue.WorkloadFinished))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should place pods based on the ranks-ordering", func() {
			numPods := 4
			sampleJob := testingjob.MakeJob("ranks-job", ns.Name).
				Queue(localQueue.Name).
				Parallelism(int32(numPods)).
				Completions(int32(numPods)).
				Indexed(true).
				Request(extraResource, "1").
				Limit(extraResource, "1").
				Obj()
			sampleJob = (&testingjob.JobWrapper{Job: *sampleJob}).
				PodAnnotation(kueuealpha.PodSetRequiredTopologyAnnotation, topologyLevelBlock).
				Image(util.E2eTestSleepImage, []string{"60s"}).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, sampleJob)).Should(gomega.Succeed())

			ginkgo.By("Job is unsuspended, and has all Pods active and ready", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sampleJob), sampleJob)).To(gomega.Succeed())
					g.Expect(sampleJob.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sampleJob), sampleJob)).To(gomega.Succeed())
					g.Expect(sampleJob.Status.Active).Should(gomega.Equal(int32(numPods)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sampleJob), sampleJob)).To(gomega.Succeed())
					g.Expect(sampleJob.Status.Ready).Should(gomega.Equal(ptr.To[int32](int32(numPods))))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			pods := &corev1.PodList{}
			ginkgo.By("ensure all pods are created and scheduled", func() {
				listOpts := &client.ListOptions{
					FieldSelector: fields.OneTermNotEqualSelector("spec.nodeName", ""),
				}
				gomega.Eventually(func(g gomega.Gomega) {
					gomega.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name), listOpts)).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.HaveLen(numPods))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
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
	})
})
