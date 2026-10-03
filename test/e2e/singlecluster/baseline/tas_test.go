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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	podcontroller "sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/util/tas"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("TopologyAwareScheduling", ginkgo.Label("area:singlecluster", "feature:tas"), func() {
	var ns *corev1.Namespace

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-tas-")
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.When("Creating a Job requesting TAS", func() {
		var (
			topology     *kueue.Topology
			onDemandRF   *kueue.ResourceFlavor
			localQueue   *kueue.LocalQueue
			clusterQueue *kueue.ClusterQueue
		)
		ginkgo.BeforeEach(func() {
			topology = utiltestingapi.MakeDefaultOneLevelTopology("hostname-" + ns.Name)
			behavioral.MustCreate(ctx, k8sClient, topology)

			onDemandRF = utiltestingapi.MakeResourceFlavor("on-demand-"+ns.Name).
				NodeLabel("instance-type", "on-demand").TopologyName(topology.Name).Obj()
			behavioral.MustCreate(ctx, k8sClient, onDemandRF)
			clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue-" + ns.Name).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(onDemandRF.Name).
						Resource(corev1.ResourceCPU, "1").
						Resource(corev1.ResourceMemory, "1Gi").
						Obj(),
				).
				Obj()
			behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

			localQueue = utiltestingapi.MakeLocalQueue("main", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(behavioral.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			// Force remove workloads to be sure that cluster queue can be removed.
			gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(behavioral.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandRF, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		})

		ginkgo.It("should admit a Job via TAS", func() {
			sampleJob := testingjob.MakeJob("test-job", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				RequestAndLimit(corev1.ResourceCPU, "700m").
				RequestAndLimit(corev1.ResourceMemory, "20Mi").
				PodAnnotation(kueue.PodSetRequiredTopologyAnnotation, corev1.LabelHostname).
				Image(behavioral.GetAgnHostImage(), behavioral.BehaviorExitFast).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, sampleJob)

			createdWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(sampleJob.Name, sampleJob.UID), Namespace: ns.Name}

			ginkgo.By(fmt.Sprintf("await for admission of workload %q", wlLookupKey), func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					g.Expect(createdWorkload.Status.Admission).ShouldNot(gomega.BeNil())
				}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
			})

			// The job might have finished at this point. That shouldn't be a problem for the purpose of this test
			jobKey := client.ObjectKeyFromObject(sampleJob)
			behavioral.ExpectJobUnsuspendedWithNodeSelectors(ctx, k8sClient, jobKey, map[string]string{
				"instance-type": "on-demand",
			})

			ginkgo.By("verify TopologyAssignment", func() {
				gomega.Expect(createdWorkload.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
				gomega.Expect(createdWorkload.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
					tas.V1Beta2From(&tas.TopologyAssignment{
						Levels: []string{
							corev1.LabelHostname,
						},
						Domains: []tas.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"kind-worker",
								},
							},
						},
					}),
				))
			})

			ginkgo.By(fmt.Sprintf("verify the workload %q gets finished", wlLookupKey), func() {
				behavioral.ExpectWorkloadToFinishWithTimeout(ctx, k8sClient, wlLookupKey, behavioral.LongTimeout)
			})
		})
	})

	ginkgo.When("Creating a Pod requesting TAS", func() {
		var (
			topology     *kueue.Topology
			onDemandRF   *kueue.ResourceFlavor
			clusterQueue *kueue.ClusterQueue
			localQueue   *kueue.LocalQueue
		)
		ginkgo.BeforeEach(func() {
			topology = utiltestingapi.MakeDefaultOneLevelTopology("hostname-" + ns.Name)
			behavioral.MustCreate(ctx, k8sClient, topology)

			onDemandRF = utiltestingapi.MakeResourceFlavor("on-demand-"+ns.Name).
				NodeLabel("instance-type", "on-demand").
				TopologyName(topology.Name).
				Obj()

			behavioral.MustCreate(ctx, k8sClient, onDemandRF)
			clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue-" + ns.Name).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(onDemandRF.Name).
						Resource(corev1.ResourceCPU, "1").
						Resource(corev1.ResourceMemory, "1Gi").
						Obj(),
				).
				Obj()
			behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

			localQueue = utiltestingapi.MakeLocalQueue("main", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(behavioral.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			// Force remove workloads to be sure that cluster queue can be removed.
			gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(behavioral.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandRF, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		})

		ginkgo.It("should admit a single Pod via TAS", func() {
			p := testingpod.MakePod("test-pod", ns.Name).
				Queue(localQueue.Name).
				Image(behavioral.GetAgnHostImage(), behavioral.BehaviorExitFast).
				Annotation(kueue.PodSetRequiredTopologyAnnotation, corev1.LabelHostname).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				RequestAndLimit(corev1.ResourceMemory, "200Mi").
				Obj()

			ginkgo.By("Creating the Pod", func() {
				behavioral.MustCreate(ctx, k8sClient, p)
				gomega.Expect(p.Spec.SchedulingGates).To(gomega.ContainElements(
					corev1.PodSchedulingGate{Name: podconstants.SchedulingGateName},
					corev1.PodSchedulingGate{Name: kueue.TopologySchedulingGate},
				))
			})

			ginkgo.By("waiting for the Pod to be unsuspended", func() {
				jobSetKey := client.ObjectKeyFromObject(p)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, jobSetKey, p)).To(gomega.Succeed())
					g.Expect(p.Spec.NodeSelector).To(gomega.Equal(map[string]string{
						"instance-type":      "on-demand",
						corev1.LabelHostname: "kind-worker",
					}))
				}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
			})

			wlLookupKey := types.NamespacedName{Name: podcontroller.GetWorkloadNameForPod(p.Name, p.UID), Namespace: ns.Name}
			createdWorkload := &kueue.Workload{}

			ginkgo.By(fmt.Sprintf("await for admission of workload %q and verify TopologyAssignment", wlLookupKey), func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					g.Expect(createdWorkload.Status.Admission).ShouldNot(gomega.BeNil())
				}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
				gomega.Expect(createdWorkload.Status.Admission).ShouldNot(gomega.BeNil())
				gomega.Expect(createdWorkload.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
				gomega.Expect(createdWorkload.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
					tas.V1Beta2From(&tas.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []tas.TopologyDomainAssignment{{
							Count:  1,
							Values: []string{"kind-worker"},
						}},
					}),
				))
			})

			ginkgo.By(fmt.Sprintf("verify the workload %q gets finished", wlLookupKey), func() {
				behavioral.ExpectWorkloadToFinishWithTimeout(ctx, k8sClient, wlLookupKey, behavioral.LongTimeout)
			})
		})

		ginkgo.It("should admit a Pod group via TAS", func() {
			group := testingpod.MakePod("group", ns.Name).
				Queue(localQueue.Name).
				Image(behavioral.GetAgnHostImage(), behavioral.BehaviorExitFast).
				Annotation(kueue.PodSetRequiredTopologyAnnotation, corev1.LabelHostname).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				RequestAndLimit(corev1.ResourceMemory, "200Mi").
				MakeGroup(2)

			ginkgo.By("Creating the Pod group", func() {
				for _, p := range group {
					behavioral.MustCreate(ctx, k8sClient, p)
					gomega.Expect(p.Spec.SchedulingGates).To(gomega.ContainElements(
						corev1.PodSchedulingGate{Name: podconstants.SchedulingGateName},
						corev1.PodSchedulingGate{Name: kueue.TopologySchedulingGate},
					))
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
				}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
			})

			wlLookupKey := client.ObjectKey{Namespace: ns.Name, Name: "group"}
			createdWorkload := &kueue.Workload{}

			ginkgo.By(fmt.Sprintf("await for admission of workload %q and verify TopologyAssignment", wlLookupKey), func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					g.Expect(createdWorkload.Status.Admission).ShouldNot(gomega.BeNil())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				gomega.Expect(createdWorkload.Status.Admission).ShouldNot(gomega.BeNil())
				gomega.Expect(createdWorkload.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
				gomega.Expect(createdWorkload.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
					tas.V1Beta2From(&tas.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []tas.TopologyDomainAssignment{{
							Count:  2,
							Values: []string{"kind-worker"},
						}},
					}),
				))
			})

			ginkgo.By(fmt.Sprintf("verify the workload %q gets finished", wlLookupKey), func() {
				behavioral.ExpectWorkloadToFinishWithTimeout(ctx, k8sClient, wlLookupKey, behavioral.LongTimeout)
			})
		})
	})
})
