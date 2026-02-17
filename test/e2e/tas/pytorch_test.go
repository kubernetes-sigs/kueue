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
	testingpytorchjob "sigs.k8s.io/kueue/pkg/util/testingjobs/pytorchjob"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("TopologyAwareScheduling for PyTorchJob", func() {
	var (
		ns           *corev1.Namespace
		topology     *kueue.Topology
		tasFlavor    *kueue.ResourceFlavor
		clusterQueue *kueue.ClusterQueue
		localQueue   *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-tas-pytorchjob-")

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
		gomega.Expect(util.DeleteAllPyTorchJobsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.When("Creating a PyTorchJob", func() {
		ginkgo.It("Should place pods based on the ranks-ordering", func() {
			const (
				masterReplicas = 1
				workerReplicas = 3
			)

			numPods := masterReplicas + workerReplicas

			pytorchjob := testingpytorchjob.MakePyTorchJob("ranks-pytorch", ns.Name).
				Queue(localQueue.Name).
				PyTorchReplicaSpecs(
					testingpytorchjob.PyTorchReplicaSpecRequirement{
						ReplicaType:   kftraining.PyTorchJobReplicaTypeMaster,
						ReplicaCount:  masterReplicas,
						RestartPolicy: kftraining.RestartPolicyOnFailure,
						Annotations: map[string]string{
							kueue.PodSetPreferredTopologyAnnotation: utiltesting.DefaultRackTopologyLevel,
						},
						Image: util.GetAgnHostImage(),
						Args:  util.BehaviorExitFast,
					},
					testingpytorchjob.PyTorchReplicaSpecRequirement{
						ReplicaType:   kftraining.PyTorchJobReplicaTypeWorker,
						ReplicaCount:  workerReplicas,
						RestartPolicy: kftraining.RestartPolicyOnFailure,
						Annotations: map[string]string{
							kueue.PodSetPreferredTopologyAnnotation: utiltesting.DefaultBlockTopologyLevel,
						},
						Image: util.GetAgnHostImage(),
						Args:  util.BehaviorExitFast,
					},
				).
				RequestAndLimit(kftraining.PyTorchJobReplicaTypeMaster, corev1.ResourceCPU, "200m").
				RequestAndLimit(kftraining.PyTorchJobReplicaTypeWorker, extraResource, "1").
				Obj()
			util.MustCreate(ctx, k8sClient, pytorchjob)

			ginkgo.By("PyTorchJob is unsuspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pytorchjob), pytorchjob)).To(gomega.Succeed())
					g.Expect(pytorchjob.Spec.RunPolicy.Suspend).Should(gomega.Equal(ptr.To(false)))
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
				gotAssignment := readRankAssignmentsFromPyTorchJobPods(pods.Items)
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

func readRankAssignmentsFromPyTorchJobPods(pods []corev1.Pod) map[string]string {
	assignment := make(map[string]string, len(pods))
	for _, pod := range pods {
		if role := pod.Labels[kftraining.ReplicaTypeLabel]; role == "worker" {
			key := fmt.Sprintf("%s/%s", role, pod.Labels[kftraining.ReplicaIndexLabel])
			assignment[key] = pod.Spec.NodeName
		}
	}
	return assignment
}
