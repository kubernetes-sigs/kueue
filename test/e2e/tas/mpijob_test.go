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

	kfmpi "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingmpijob "sigs.k8s.io/kueue/pkg/util/testingjobs/mpijob"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("TopologyAwareScheduling for MPIJob", func() {
	var (
		ns           *corev1.Namespace
		topology     *kueuealpha.Topology
		tasFlavor    *kueue.ResourceFlavor
		clusterQueue *kueue.ClusterQueue
		localQueue   *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "e2e-tas-mpijob-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

		topology = testing.MakeTopology("datacenter").
			Levels([]string{topologyLevelBlock, topologyLevelRack, corev1.LabelHostname}).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, topology)).Should(gomega.Succeed())

		tasFlavor = testing.MakeResourceFlavor("tas-flavor").
			NodeLabel(tasNodeGroupLabel, instanceType).
			TopologyName(topology.Name).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, tasFlavor)).Should(gomega.Succeed())

		clusterQueue = testing.MakeClusterQueue("cluster-queue").
			ResourceGroup(
				*testing.MakeFlavorQuotas(tasFlavor.Name).
					Resource(corev1.ResourceCPU, "1").
					Resource(extraResource, "8").
					Obj(),
			).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, clusterQueue)).Should(gomega.Succeed())
		util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

		localQueue = testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, localQueue)).Should(gomega.Succeed())
		util.ExpectLocalQueuesToBeActive(ctx, k8sClient, localQueue)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteAllMPIJobsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
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
						Image:         util.E2eTestSleepImage,
						Args:          []string{"60s"},
						ReplicaType:   kfmpi.MPIReplicaTypeLauncher,
						ReplicaCount:  launcherReplicas,
						RestartPolicy: corev1.RestartPolicyOnFailure,
						Annotations: map[string]string{
							kueuealpha.PodSetPreferredTopologyAnnotation: topologyLevelRack,
						},
					},
					testingmpijob.MPIJobReplicaSpecRequirement{
						Image:         util.E2eTestSleepImage,
						Args:          []string{"60s"},
						ReplicaType:   kfmpi.MPIReplicaTypeWorker,
						ReplicaCount:  workerReplicas,
						RestartPolicy: corev1.RestartPolicyOnFailure,
						Annotations: map[string]string{
							kueuealpha.PodSetPreferredTopologyAnnotation: topologyLevelBlock,
						},
					},
				).
				Request(kfmpi.MPIReplicaTypeLauncher, corev1.ResourceCPU, "100m").
				Limit(kfmpi.MPIReplicaTypeLauncher, corev1.ResourceCPU, "100m").
				Request(kfmpi.MPIReplicaTypeWorker, extraResource, "1").
				Limit(kfmpi.MPIReplicaTypeWorker, extraResource, "1").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, mpijob)).Should(gomega.Succeed())

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
				gotAssignment := readRankAssignmentsFromMPIJobPods(pods.Items)
				wantAssignment := map[string]string{
					"worker/0": "kind-worker",
					"worker/1": "kind-worker2",
					"worker/2": "kind-worker3",
				}
				gomega.Expect(wantAssignment).Should(gomega.BeComparableTo(gotAssignment))
			})
		})
	})
})

func readRankAssignmentsFromMPIJobPods(pods []corev1.Pod) map[string]string {
	assignment := make(map[string]string, len(pods))
	for _, pod := range pods {
		if role := pod.Labels[kftraining.JobRoleLabel]; role == "worker" {
			key := fmt.Sprintf("%s/%s", role, pod.Labels[kftraining.ReplicaIndexLabel])
			assignment[key] = pod.Spec.NodeName
		}
	}
	return assignment
}
