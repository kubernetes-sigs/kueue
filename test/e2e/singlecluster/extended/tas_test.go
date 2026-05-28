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

package extended

import (
	"fmt"

	kftrainerapi "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobs/jobset"
	workloadtrainjob "sigs.k8s.io/kueue/pkg/controller/jobs/trainjob"
	"sigs.k8s.io/kueue/pkg/util/tas"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjobset "sigs.k8s.io/kueue/pkg/util/testingjobs/jobset"
	testingtrainjob "sigs.k8s.io/kueue/pkg/util/testingjobs/trainjob"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("TopologyAwareScheduling", ginkgo.Label("area:singlecluster", "feature:tas"), func() {
	var ns *corev1.Namespace

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-tas-")
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.When("Creating a JobSet requesting TAS", func() {
		var (
			topology     *kueue.Topology
			onDemandRF   *kueue.ResourceFlavor
			clusterQueue *kueue.ClusterQueue
			localQueue   *kueue.LocalQueue
		)
		ginkgo.BeforeEach(func() {
			topology = utiltestingapi.MakeDefaultOneLevelTopology("hostname-" + ns.Name)
			util.MustCreate(ctx, k8sClient, topology)

			onDemandRF = utiltestingapi.MakeResourceFlavor("on-demand-"+ns.Name).
				NodeLabel("instance-type", "on-demand").
				TopologyName(topology.Name).
				Obj()

			util.MustCreate(ctx, k8sClient, onDemandRF)
			clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue-" + ns.Name).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(onDemandRF.Name).
						Resource(corev1.ResourceCPU, "1").
						Resource(corev1.ResourceMemory, "1Gi").
						Obj(),
				).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

			localQueue = utiltestingapi.MakeLocalQueue("main", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			// Force remove workloads to be sure that cluster queue can be removed.
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandRF, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		})

		ginkgo.It("should admit a JobSet via TAS", func() {
			jobSet := testingjobset.MakeJobSet("test-jobset", ns.Name).
				Queue(localQueue.Name).
				ReplicatedJobs(
					testingjobset.ReplicatedJobRequirements{
						Name:        "rj1",
						Image:       util.GetAgnHostImage(),
						Args:        util.BehaviorExitFast,
						Replicas:    1,
						Parallelism: 1,
						Completions: 1,
						PodAnnotations: map[string]string{
							kueue.PodSetRequiredTopologyAnnotation: corev1.LabelHostname,
						},
					},
					testingjobset.ReplicatedJobRequirements{
						Name:        "rj2",
						Image:       util.GetAgnHostImage(),
						Args:        util.BehaviorExitFast,
						Replicas:    1,
						Parallelism: 1,
						Completions: 1,
						PodAnnotations: map[string]string{
							kueue.PodSetRequiredTopologyAnnotation: corev1.LabelHostname,
						},
					},
				).
				RequestAndLimit("rj1", corev1.ResourceCPU, "200m").
				RequestAndLimit("rj1", corev1.ResourceMemory, "20Mi").
				RequestAndLimit("rj2", corev1.ResourceCPU, "200m").
				RequestAndLimit("rj2", corev1.ResourceMemory, "20Mi").
				Obj()

			ginkgo.By("Creating the JobSet", func() {
				util.MustCreate(ctx, k8sClient, jobSet)
			})

			ginkgo.By("waiting for the JobSet to be unsuspended", func() {
				jobSetKey := client.ObjectKeyFromObject(jobSet)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, jobSetKey, jobSet)).To(gomega.Succeed())
					g.Expect(jobSet.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
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
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
				gomega.Expect(createdWorkload.Status.Admission).ShouldNot(gomega.BeNil())
				gomega.Expect(createdWorkload.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(2))
				gomega.Expect(createdWorkload.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
					tas.V1Beta2From(&tas.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []tas.TopologyDomainAssignment{{
							Count:  1,
							Values: []string{"kind-worker"},
						}},
					}),
				))
				gomega.Expect(createdWorkload.Status.Admission.PodSetAssignments[1].TopologyAssignment).Should(gomega.BeComparableTo(
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
				util.ExpectWorkloadToFinishWithTimeout(ctx, k8sClient, wlLookupKey, util.LongTimeout)
			})
		})
	})

	ginkgo.When("Creating a TrainJob requesting TAS", func() {
		var (
			topology     *kueue.Topology
			onDemandRF   *kueue.ResourceFlavor
			clusterQueue *kueue.ClusterQueue
			localQueue   *kueue.LocalQueue
		)
		ginkgo.BeforeEach(func() {
			topology = utiltestingapi.MakeDefaultOneLevelTopology("hostname-" + ns.Name)
			util.MustCreate(ctx, k8sClient, topology)

			onDemandRF = utiltestingapi.MakeResourceFlavor("on-demand-"+ns.Name).
				NodeLabel("instance-type", "on-demand").
				TopologyName(topology.Name).
				Obj()

			util.MustCreate(ctx, k8sClient, onDemandRF)

			clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue-" + ns.Name).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(onDemandRF.Name).
						Resource(corev1.ResourceCPU, "1").
						Resource(corev1.ResourceMemory, "1Gi").
						Obj(),
				).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

			localQueue = utiltestingapi.MakeLocalQueue("main", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteAllTrainJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			// Force remove workloads to be sure that cluster queue can be removed.
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandRF, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		})

		ginkgo.It("should admit a TrainJob via TAS", func() {
			trainjob := testingtrainjob.MakeTrainJob("trainjob-test", ns.Name).
				RuntimeRefName("torch-distributed").
				Queue(localQueue.Name).
				// Even if we override the image coming from the TrainingRuntime, we still need to set the command and args
				TrainerImage(util.GetAgnHostImage(), []string{"/agnhost"}, util.BehaviorExitFast).
				TrainerRequest(corev1.ResourceCPU, "500m").
				TrainerRequest(corev1.ResourceMemory, "200Mi").
				RuntimePatches([]kftrainerapi.RuntimePatch{
					testingtrainjob.MakeRuntimePatch("test-e2e/tas").
						ReplicatedJobs(
							testingtrainjob.MakeReplicatedJobPatch("node").
								PodAnnotation(kueue.PodSetRequiredTopologyAnnotation, corev1.LabelHostname).
								Obj(),
						).
						Obj(),
				}).
				Obj()

			ginkgo.By("Creating the TrainJob", func() {
				util.MustCreate(ctx, k8sClient, trainjob)
			})

			ginkgo.By("waiting for the TrainJob to be unsuspended", func() {
				trainjobKey := client.ObjectKeyFromObject(trainjob)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, trainjobKey, trainjob)).To(gomega.Succeed())
					g.Expect(trainjob.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Verify that the trainjob has a runtime patch from kueue", func() {
				kueueRuntimePatch := testingtrainjob.KueueRuntimePatch(trainjob)
				gomega.Expect(kueueRuntimePatch).ToNot(gomega.BeNil())
				rJobs := kueueRuntimePatch.TrainingRuntimeSpec.Template.Spec.ReplicatedJobs
				gomega.Expect(rJobs).To(gomega.HaveLen(1))
				gomega.Expect(rJobs[0].Template.Spec.Template.Spec.NodeSelector).To(gomega.Equal(
					map[string]string{
						"instance-type": "on-demand",
					},
				))
			})

			wlLookupKey := types.NamespacedName{Name: workloadtrainjob.GetWorkloadNameForTrainJob(trainjob.Name, trainjob.UID), Namespace: ns.Name}
			createdWorkload := &kueue.Workload{}

			ginkgo.By(fmt.Sprintf("await for admission of workload %q and verify TopologyAssignment", wlLookupKey), func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					g.Expect(createdWorkload.Status.Admission).ShouldNot(gomega.BeNil())
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
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
				util.ExpectWorkloadToFinishWithTimeout(ctx, k8sClient, wlLookupKey, util.LongTimeout)
			})
		})
	})
})
