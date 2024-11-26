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

package e2e

import (
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/controller/jobs/jobset"
	podcontroller "sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingjobset "sigs.k8s.io/kueue/pkg/util/testingjobs/jobset"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("TopologyAwareScheduling", func() {
	var ns *corev1.Namespace

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "e2e-tas-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.When("Creating a Job requesting TAS", func() {
		var (
			topology     *kueuealpha.Topology
			onDemandRF   *kueue.ResourceFlavor
			localQueue   *kueue.LocalQueue
			clusterQueue *kueue.ClusterQueue
		)
		ginkgo.BeforeEach(func() {
			topology = testing.MakeTopology("hostname").Levels([]string{
				corev1.LabelHostname,
			}).Obj()
			gomega.Expect(k8sClient.Create(ctx, topology)).Should(gomega.Succeed())

			onDemandRF = testing.MakeResourceFlavor("on-demand").
				NodeLabel("instance-type", "on-demand").TopologyName(topology.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, onDemandRF)).Should(gomega.Succeed())
			clusterQueue = testing.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*testing.MakeFlavorQuotas("on-demand").
						Resource(corev1.ResourceCPU, "1").
						Resource(corev1.ResourceMemory, "1Gi").
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
			util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandRF, true)
		})

		ginkgo.It("should admit a Job via TAS", func() {
			sampleJob := testingjob.MakeJob("test-job", ns.Name).
				Queue(localQueue.Name).
				Request("cpu", "700m").
				Request("memory", "20Mi").
				Obj()
			jobKey := client.ObjectKeyFromObject(sampleJob)
			sampleJob = (&testingjob.JobWrapper{Job: *sampleJob}).
				PodAnnotation(kueuealpha.PodSetRequiredTopologyAnnotation, corev1.LabelHostname).
				Image(util.E2eTestSleepImage, []string{"1ms"}).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, sampleJob)).Should(gomega.Succeed())

			createdWorkload := &kueue.Workload{}

			// The job might have finished at this point. That shouldn't be a problem for the purpose of this test
			expectJobUnsuspendedWithNodeSelectors(jobKey, map[string]string{
				"instance-type": "on-demand",
			})
			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(sampleJob.Name, sampleJob.UID), Namespace: ns.Name}

			ginkgo.By(fmt.Sprintf("await for admission of workload %q and verify TopologyAssignment", wlLookupKey), func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					g.Expect(createdWorkload.Status.Admission).ShouldNot(gomega.BeNil())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
				gomega.Expect(createdWorkload.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
				gomega.Expect(createdWorkload.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
					&kueue.TopologyAssignment{
						Levels: []string{
							corev1.LabelHostname,
						},
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"kind-worker",
								},
							},
						},
					},
				))
			})

			ginkgo.By(fmt.Sprintf("verify the workload %q gets finished", wlLookupKey), func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					g.Expect(workload.HasQuotaReservation(createdWorkload)).Should(gomega.BeTrue())
					g.Expect(createdWorkload.Status.Conditions).Should(testing.HaveConditionStatusTrue(kueue.WorkloadFinished))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("Creating a JobSet requesting TAS", func() {
		var (
			topology     *kueuealpha.Topology
			onDemandRF   *kueue.ResourceFlavor
			clusterQueue *kueue.ClusterQueue
			localQueue   *kueue.LocalQueue
		)
		ginkgo.BeforeEach(func() {
			topology = testing.MakeTopology("hostname").Levels([]string{corev1.LabelHostname}).Obj()
			gomega.Expect(k8sClient.Create(ctx, topology)).Should(gomega.Succeed())

			onDemandRF = testing.MakeResourceFlavor("on-demand").
				NodeLabel("instance-type", "on-demand").
				TopologyName(topology.Name).
				Obj()

			gomega.Expect(k8sClient.Create(ctx, onDemandRF)).Should(gomega.Succeed())
			clusterQueue = testing.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*testing.MakeFlavorQuotas("on-demand").
						Resource(corev1.ResourceCPU, "1").
						Resource(corev1.ResourceMemory, "1Gi").
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
			util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandRF, true)
		})

		ginkgo.It("should admit a JobSet via TAS", func() {
			jobSet := testingjobset.MakeJobSet("test-jobset", ns.Name).
				Queue(localQueue.Name).
				ReplicatedJobs(
					testingjobset.ReplicatedJobRequirements{
						Name:        "rj1",
						Replicas:    1,
						Parallelism: 1,
						Completions: 1,
						PodAnnotations: map[string]string{
							kueuealpha.PodSetRequiredTopologyAnnotation: corev1.LabelHostname,
						},
						Image: util.E2eTestSleepImage,
						Args:  []string{"1ms"},
					},
					testingjobset.ReplicatedJobRequirements{
						Name:        "rj2",
						Replicas:    1,
						Parallelism: 1,
						Completions: 1,
						PodAnnotations: map[string]string{
							kueuealpha.PodSetRequiredTopologyAnnotation: corev1.LabelHostname,
						},
						Image: util.E2eTestSleepImage,
						Args:  []string{"1ms"},
					},
				).
				Request("rj1", "cpu", "200m").
				Request("rj1", "memory", "20Mi").
				Request("rj2", "cpu", "200m").
				Request("rj2", "memory", "20Mi").
				Obj()

			ginkgo.By("Creating the JobSet", func() {
				gomega.Expect(k8sClient.Create(ctx, jobSet)).Should(gomega.Succeed())
			})

			ginkgo.By("waiting for the JobSet to be unsuspended", func() {
				jobSetKey := client.ObjectKeyFromObject(jobSet)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, jobSetKey, jobSet)).To(gomega.Succeed())
					g.Expect(jobSet.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verify the JobSet has nodeSelector set", func() {
				gomega.Expect(jobSet.Spec.ReplicatedJobs[0].Template.Spec.Template.Spec.NodeSelector).To(gomega.Equal(
					map[string]string{
						"instance-type": "on-demand",
					},
				))
			})

			wlLookupKey := types.NamespacedName{Name: jobset.GetWorkloadNameForJobSet(jobSet.Name, jobSet.UID), Namespace: ns.Name}
			createdWorkload := &kueue.Workload{}

			ginkgo.By(fmt.Sprintf("await for admission of workload %q and verify TopologyAssignment", wlLookupKey), func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					g.Expect(createdWorkload.Status.Admission).ShouldNot(gomega.BeNil())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
				gomega.Expect(createdWorkload.Status.Admission).ShouldNot(gomega.BeNil())
				gomega.Expect(createdWorkload.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(2))
				gomega.Expect(createdWorkload.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
					&kueue.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []kueue.TopologyDomainAssignment{{
							Count:  1,
							Values: []string{"kind-worker"},
						}},
					},
				))
				gomega.Expect(createdWorkload.Status.Admission.PodSetAssignments[1].TopologyAssignment).Should(gomega.BeComparableTo(
					&kueue.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []kueue.TopologyDomainAssignment{{
							Count:  1,
							Values: []string{"kind-worker"},
						}},
					},
				))
			})

			ginkgo.By(fmt.Sprintf("verify the workload %q gets finished", wlLookupKey), func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					g.Expect(workload.HasQuotaReservation(createdWorkload)).Should(gomega.BeTrue())
					g.Expect(createdWorkload.Status.Conditions).Should(testing.HaveConditionStatusTrue(kueue.WorkloadFinished))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("Creating a Pod requesting TAS", func() {
		var (
			topology     *kueuealpha.Topology
			onDemandRF   *kueue.ResourceFlavor
			clusterQueue *kueue.ClusterQueue
			localQueue   *kueue.LocalQueue
		)
		ginkgo.BeforeEach(func() {
			topology = testing.MakeTopology("hostname").Levels([]string{corev1.LabelHostname}).Obj()
			gomega.Expect(k8sClient.Create(ctx, topology)).Should(gomega.Succeed())

			onDemandRF = testing.MakeResourceFlavor("on-demand").
				NodeLabel("instance-type", "on-demand").
				TopologyName(topology.Name).
				Obj()

			gomega.Expect(k8sClient.Create(ctx, onDemandRF)).Should(gomega.Succeed())
			clusterQueue = testing.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*testing.MakeFlavorQuotas("on-demand").
						Resource(corev1.ResourceCPU, "1").
						Resource(corev1.ResourceMemory, "1Gi").
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
			util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandRF, true)
		})

		ginkgo.It("should admit a single Pod via TAS", func() {
			p := testingpod.MakePod("test-pod", ns.Name).
				Queue(localQueue.Name).
				Image(util.E2eTestSleepImage, []string{"1ms"}).
				Annotation(kueuealpha.PodSetRequiredTopologyAnnotation, corev1.LabelHostname).
				Request("cpu", "100m").
				Request("memory", "100Mi").
				Obj()

			ginkgo.By("Creating the Pod", func() {
				gomega.Expect(k8sClient.Create(ctx, p)).Should(gomega.Succeed())
				gomega.Expect(p.Spec.SchedulingGates).To(gomega.ContainElements(
					corev1.PodSchedulingGate{Name: podcontroller.SchedulingGateName},
					corev1.PodSchedulingGate{Name: kueuealpha.TopologySchedulingGate},
				))
				gomega.Expect(p.Labels).To(gomega.HaveKeyWithValue(kueuealpha.TASLabel, "true"))
			})

			ginkgo.By("waiting for the Pod to be unsuspended", func() {
				jobSetKey := client.ObjectKeyFromObject(p)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, jobSetKey, p)).To(gomega.Succeed())
					g.Expect(p.Spec.NodeSelector).To(gomega.Equal(map[string]string{
						"instance-type":      "on-demand",
						corev1.LabelHostname: "kind-worker",
					}))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			wlLookupKey := types.NamespacedName{Name: podcontroller.GetWorkloadNameForPod(p.Name, p.UID), Namespace: ns.Name}
			createdWorkload := &kueue.Workload{}

			ginkgo.By(fmt.Sprintf("await for admission of workload %q and verify TopologyAssignment", wlLookupKey), func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					g.Expect(createdWorkload.Status.Admission).ShouldNot(gomega.BeNil())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
				gomega.Expect(createdWorkload.Status.Admission).ShouldNot(gomega.BeNil())
				gomega.Expect(createdWorkload.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
				gomega.Expect(createdWorkload.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
					&kueue.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []kueue.TopologyDomainAssignment{{
							Count:  1,
							Values: []string{"kind-worker"},
						}},
					},
				))
			})

			ginkgo.By(fmt.Sprintf("verify the workload %q gets finished", wlLookupKey), func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					g.Expect(workload.HasQuotaReservation(createdWorkload)).Should(gomega.BeTrue())
					g.Expect(createdWorkload.Status.Conditions).Should(testing.HaveConditionStatusTrue(kueue.WorkloadFinished))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should admit a Pod group via TAS", func() {
			group := testingpod.MakePod("group", ns.Name).
				Queue(localQueue.Name).
				Image(util.E2eTestSleepImage, []string{"1ms"}).
				Annotation(kueuealpha.PodSetRequiredTopologyAnnotation, corev1.LabelHostname).
				Request("cpu", "100m").
				Request("memory", "100Mi").
				MakeGroup(2)

			ginkgo.By("Creating the Pod group", func() {
				for _, p := range group {
					gomega.Expect(k8sClient.Create(ctx, p)).To(gomega.Succeed())
					gomega.Expect(p.Spec.SchedulingGates).To(gomega.ContainElements(
						corev1.PodSchedulingGate{Name: podcontroller.SchedulingGateName},
						corev1.PodSchedulingGate{Name: kueuealpha.TopologySchedulingGate},
					))
					gomega.Expect(p.Labels).To(gomega.HaveKeyWithValue(kueuealpha.TASLabel, "true"))
				}
			})

			ginkgo.By("waiting for the Pod to be ungated", func() {
				// Verify that the Pods start with the appropriate selector.
				gomega.Eventually(func(g gomega.Gomega) {
					for _, origPod := range group {
						var p corev1.Pod
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(origPod), &p)).To(gomega.Succeed())
						g.Expect(p.Spec.SchedulingGates).To(gomega.BeEmpty())
						g.Expect(p.Spec.NodeSelector).To(gomega.Equal(map[string]string{
							"instance-type":      "on-demand",
							corev1.LabelHostname: "kind-worker",
						}))
					}
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			wlLookupKey := client.ObjectKey{Namespace: ns.Name, Name: "group"}
			createdWorkload := &kueue.Workload{}

			ginkgo.By(fmt.Sprintf("await for admission of workload %q and verify TopologyAssignment", wlLookupKey), func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					g.Expect(createdWorkload.Status.Admission).ShouldNot(gomega.BeNil())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
				gomega.Expect(createdWorkload.Status.Admission).ShouldNot(gomega.BeNil())
				gomega.Expect(createdWorkload.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
				gomega.Expect(createdWorkload.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
					&kueue.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []kueue.TopologyDomainAssignment{{
							Count:  2,
							Values: []string{"kind-worker"},
						}},
					},
				))
			})

			ginkgo.By(fmt.Sprintf("verify the workload %q gets finished", wlLookupKey), func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					g.Expect(workload.HasQuotaReservation(createdWorkload)).Should(gomega.BeTrue())
					g.Expect(createdWorkload.Status.Conditions).Should(testing.HaveConditionStatusTrue(kueue.WorkloadFinished))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})
