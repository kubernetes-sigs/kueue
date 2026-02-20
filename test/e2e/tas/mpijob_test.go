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

package tase2e

import (
	"fmt"

	kfmpi "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingmpijob "sigs.k8s.io/kueue/pkg/util/testingjobs/mpijob"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("TopologyAwareScheduling for MPIJob", func() {
	var (
		ns           *corev1.Namespace
		topology     *kueue.Topology
		tasFlavor    *kueue.ResourceFlavor
		clusterQueue *kueue.ClusterQueue
		localQueue   *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-tas-mpijob-")

		topology = utiltestingapi.MakeDefaultThreeLevelTopology("datacenter")
		util.MustCreate(ctx, k8sClient, topology)

		tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
			NodeLabel(tasNodeGroupLabel, instanceType).
			TopologyName(topology.Name).
			Obj()
		util.MustCreate(ctx, k8sClient, tasFlavor)

		clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).
					Resource(corev1.ResourceCPU, "1").
					Resource(extraResource, "8").
					Obj(),
			).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteAllMPIJobsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.When("Creating a MPIJob", func() {
		ginkgo.It("Should place pods based on the ranks-ordering", func() {
			const (
				launcherReplicas = 1
				workerReplicas   = 3
			)

			numPods := launcherReplicas + workerReplicas

			mpijob := testingmpijob.MakeMPIJob("ranks-mpi", ns.Name).
				Queue(localQueue.Name).
				MPIJobReplicaSpecs(
					testingmpijob.MPIJobReplicaSpecRequirement{
						ReplicaType:   kfmpi.MPIReplicaTypeLauncher,
						ReplicaCount:  launcherReplicas,
						RestartPolicy: corev1.RestartPolicyOnFailure,
						Annotations: map[string]string{
							kueue.PodSetPreferredTopologyAnnotation: utiltesting.DefaultRackTopologyLevel,
						},
						Image: util.GetAgnHostImage(),
						Args:  util.BehaviorExitFast,
					},
					testingmpijob.MPIJobReplicaSpecRequirement{
						ReplicaType:   kfmpi.MPIReplicaTypeWorker,
						ReplicaCount:  workerReplicas,
						RestartPolicy: corev1.RestartPolicyOnFailure,
						Annotations: map[string]string{
							kueue.PodSetPreferredTopologyAnnotation: utiltesting.DefaultBlockTopologyLevel,
						},
						Image: util.GetAgnHostImage(),
						Args:  util.BehaviorExitFast,
					},
				).
				RequestAndLimit(kfmpi.MPIReplicaTypeLauncher, corev1.ResourceCPU, "200m").
				RequestAndLimit(kfmpi.MPIReplicaTypeWorker, extraResource, "1").
				Obj()
			util.MustCreate(ctx, k8sClient, mpijob)

			ginkgo.By("MPIJob is unsuspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(mpijob), mpijob)).To(gomega.Succeed())
					g.Expect(mpijob.Spec.RunPolicy.Suspend).Should(gomega.Equal(ptr.To(false)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			pods := &corev1.PodList{}
			ginkgo.By("ensure all pods are created", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name))).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.HaveLen(numPods))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("ensure all pods are scheduled", func() {
				listOpts := &client.ListOptions{
					FieldSelector: fields.OneTermNotEqualSelector("spec.nodeName", ""),
				}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name), listOpts)).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.HaveLen(numPods))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verify the assignment of pods are as expected with rank-based ordering", func() {
				gomega.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name))).To(gomega.Succeed())
				gotAssignment := readRankAssignmentsFromMPIJobPods(pods.Items, false)
				wantAssignment := map[string]string{
					"worker/0": "kind-worker",
					"worker/1": "kind-worker2",
					"worker/2": "kind-worker3",
				}
				gomega.Expect(wantAssignment).Should(gomega.BeComparableTo(gotAssignment))
			})
		})
	})

	ginkgo.When("Creating a MPIJob with runLauncherAsWorker", func() {
		ginkgo.It("Should place MPIJob pods based on the ranks-ordering (kueue.x-k8s.io/pod-index-offset Pods annotation)", func() {
			const (
				launcherReplicas = 1
				workerReplicas   = 3
			)

			numPods := launcherReplicas + workerReplicas

			mpijob := testingmpijob.MakeMPIJob("ranks-mpi-launcherasworker", ns.Name).
				Queue(localQueue.Name).
				RunLauncherAsWorker(true).
				MPIJobReplicaSpecs(
					testingmpijob.MPIJobReplicaSpecRequirement{
						ReplicaType:   kfmpi.MPIReplicaTypeLauncher,
						ReplicaCount:  launcherReplicas,
						RestartPolicy: corev1.RestartPolicyOnFailure,
						Annotations: map[string]string{
							kueue.PodSetPreferredTopologyAnnotation: utiltesting.DefaultRackTopologyLevel,
						},
						Image: util.GetAgnHostImage(),
						Args:  util.BehaviorExitFast,
					},
					testingmpijob.MPIJobReplicaSpecRequirement{
						ReplicaType:   kfmpi.MPIReplicaTypeWorker,
						ReplicaCount:  workerReplicas,
						RestartPolicy: corev1.RestartPolicyOnFailure,
						Annotations: map[string]string{
							kueue.PodSetPreferredTopologyAnnotation: utiltesting.DefaultBlockTopologyLevel,
						},
						Image: util.GetAgnHostImage(),
						Args:  util.BehaviorExitFast,
					},
				).
				RequestAndLimit(kfmpi.MPIReplicaTypeLauncher, corev1.ResourceCPU, "200m").
				RequestAndLimit(kfmpi.MPIReplicaTypeWorker, extraResource, "1").
				Obj()
			util.MustCreate(ctx, k8sClient, mpijob)

			ginkgo.By("verify the webhook adds pod-index-offset annotation to Worker", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(mpijob), mpijob)).To(gomega.Succeed())
					g.Expect(mpijob.Spec.MPIReplicaSpecs[kfmpi.MPIReplicaTypeWorker].Template.Annotations).Should(
						gomega.HaveKeyWithValue(kueue.PodIndexOffsetAnnotation, "1"))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("MPIJob is unsuspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(mpijob), mpijob)).To(gomega.Succeed())
					g.Expect(mpijob.Spec.RunPolicy.Suspend).Should(gomega.Equal(ptr.To(false)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			pods := &corev1.PodList{}
			ginkgo.By("ensure all pods are created", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name))).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.HaveLen(numPods))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("ensure all pods are scheduled", func() {
				listOpts := &client.ListOptions{
					FieldSelector: fields.OneTermNotEqualSelector("spec.nodeName", ""),
				}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name), listOpts)).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.HaveLen(numPods))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verify the assignment of all pods (launcher + workers) with rank-based ordering", func() {
				gomega.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name))).To(gomega.Succeed())
				gotAssignment := readRankAssignmentsFromMPIJobPods(pods.Items, false)
				wantAssignment := map[string]string{
					"worker/1": "kind-worker",
					"worker/2": "kind-worker2",
					"worker/3": "kind-worker3",
				}
				gomega.Expect(wantAssignment).Should(gomega.BeComparableTo(gotAssignment))
			})
		})
	})

	ginkgo.When("Creating a launcher and workers grouped MPIJob with runLauncherAsWorker", func() {
		ginkgo.It("Should place MPIJob launcher and workers grouped pods based on the ranks-ordering (kueue.x-k8s.io/podset-group-name Pods annotation)", func() {
			const (
				launcherReplicas = 1
				workerReplicas   = 3
			)

			numPods := launcherReplicas + workerReplicas

			mpiJob := testingmpijob.MakeMPIJob("ranks-mpi-podsetgroup", ns.Name).
				Queue(localQueue.Name).
				RunLauncherAsWorker(true).
				MPIJobReplicaSpecs(
					testingmpijob.MPIJobReplicaSpecRequirement{
						ReplicaType:   kfmpi.MPIReplicaTypeLauncher,
						ReplicaCount:  launcherReplicas,
						RestartPolicy: corev1.RestartPolicyOnFailure,
						Annotations: map[string]string{
							kueue.PodSetRequiredTopologyAnnotation: utiltesting.DefaultBlockTopologyLevel,
							kueue.PodSetGroupName:                  "same-group",
						},
						Image: util.GetAgnHostImage(),
						Args:  util.BehaviorExitFast,
					},
					testingmpijob.MPIJobReplicaSpecRequirement{
						ReplicaType:   kfmpi.MPIReplicaTypeWorker,
						ReplicaCount:  workerReplicas,
						RestartPolicy: corev1.RestartPolicyOnFailure,
						Annotations: map[string]string{
							kueue.PodSetRequiredTopologyAnnotation: utiltesting.DefaultBlockTopologyLevel,
							kueue.PodSetGroupName:                  "same-group",
						},
						Image: util.GetAgnHostImage(),
						Args:  util.BehaviorExitFast,
					},
				).
				RequestAndLimit(kfmpi.MPIReplicaTypeLauncher, corev1.ResourceCPU, "200m").
				RequestAndLimit(kfmpi.MPIReplicaTypeWorker, extraResource, "1").
				Obj()
			util.MustCreate(ctx, k8sClient, mpiJob)

			ginkgo.By("MPIJob is unsuspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(mpiJob), mpiJob)).To(gomega.Succeed())
					g.Expect(mpiJob.Spec.RunPolicy.Suspend).Should(gomega.Equal(ptr.To(false)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			pods := &corev1.PodList{}
			ginkgo.By("ensure all pods are created", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name))).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.HaveLen(numPods))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("ensure all pods are scheduled", func() {
				listOpts := &client.ListOptions{
					FieldSelector: fields.OneTermNotEqualSelector("spec.nodeName", ""),
				}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name), listOpts)).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.HaveLen(numPods))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verify the assignment of all pods (launcher + workers) with rank-based ordering within the same block", func() {
				gomega.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name))).To(gomega.Succeed())
				gotAssignment := readRankAssignmentsFromMPIJobPods(pods.Items, true)
				wantAssignment := map[string]string{
					"launcher/0": "kind-worker",
					"worker/1":   "kind-worker",
					"worker/2":   "kind-worker2",
					"worker/3":   "kind-worker3",
				}
				gomega.Expect(wantAssignment).Should(gomega.BeComparableTo(gotAssignment))
			})
		})
	})
})

func readRankAssignmentsFromMPIJobPods(pods []corev1.Pod, isPodSetGroupRunLauncherAsWorker bool) map[string]string {
	assignment := make(map[string]string, len(pods))
	for _, pod := range pods {
		role := pod.Labels[kftraining.JobRoleLabel]
		if role == "worker" || (isPodSetGroupRunLauncherAsWorker && role == "launcher") {
			key := fmt.Sprintf("%s/%s", role, pod.Labels[kftraining.ReplicaIndexLabel])
			assignment[key] = pod.Spec.NodeName
		}
	}
	return assignment
}
