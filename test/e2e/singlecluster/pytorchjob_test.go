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

package e2e

import (
	"github.com/google/go-cmp/cmp/cmpopts"
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/jobs/pytorchjob"
	"sigs.k8s.io/kueue/pkg/util/testing"
	pytorchjobtesting "sigs.k8s.io/kueue/pkg/util/testingjobs/pytorchjob"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("PyTorch integration", func() {
	const (
		resourceFlavorName = "pytorch-rf"
		clusterQueueName   = "pytorch-cq"
		localQueueName     = "pytorch-lq"
	)

	var (
		ns *corev1.Namespace
		rf *kueue.ResourceFlavor
		cq *kueue.ClusterQueue
		lq *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "pytorch-e2e-")

		rf = testing.MakeResourceFlavor(resourceFlavorName).Obj()
		util.MustCreate(ctx, k8sClient, rf)

		cq = testing.MakeClusterQueue(clusterQueueName).
			ResourceGroup(
				*testing.MakeFlavorQuotas(resourceFlavorName).
					Resource(corev1.ResourceCPU, "5").
					Resource(corev1.ResourceMemory, "10Gi").
					Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj()
		util.MustCreate(ctx, k8sClient, cq)

		lq = testing.MakeLocalQueue(localQueueName, ns.Name).ClusterQueue(cq.Name).Obj()
		util.MustCreate(ctx, k8sClient, lq)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteAllPyTorchJobsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.When("PyTorch created", func() {
		ginkgo.It("should admit group with leader only", func() {
			pytorch := pytorchjobtesting.MakePyTorchJob("pytorch-simple", ns.Name).
				Queue(localQueueName).
				Suspend(false).
				SetTypeMeta().
				PyTorchReplicaSpecsDefault().
				Parallelism(2).
				Image(kftraining.PyTorchJobReplicaTypeMaster, util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Request(kftraining.PyTorchJobReplicaTypeMaster, corev1.ResourceCPU, "1").
				Request(kftraining.PyTorchJobReplicaTypeMaster, corev1.ResourceMemory, "200Mi").
				Image(kftraining.PyTorchJobReplicaTypeWorker, util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Request(kftraining.PyTorchJobReplicaTypeWorker, corev1.ResourceCPU, "1").
				Request(kftraining.PyTorchJobReplicaTypeWorker, corev1.ResourceMemory, "200Mi").
				Obj()

			ginkgo.By("Create a PyTorch", func() {
				util.MustCreate(ctx, k8sClient, pytorch)
			})

			ginkgo.By("Waiting for replicas to be ready", func() {
				createdPyTorch := &kftraining.PyTorchJob{}

				gomega.Eventually(func(g gomega.Gomega) {
					// Fetch the PyTorch object
					err := k8sClient.Get(ctx, client.ObjectKeyFromObject(pytorch), createdPyTorch)
					g.Expect(err).ToNot(gomega.HaveOccurred(), "Failed to fetch PyTorch")

					// Check the worker replica status exists
					masterReplicaStatus, ok := createdPyTorch.Status.ReplicaStatuses[kftraining.PyTorchJobReplicaTypeMaster]
					g.Expect(ok).To(gomega.BeTrue(), "Master replica status not found in PyTorch status")
					workerReplicaStatus, ok := createdPyTorch.Status.ReplicaStatuses[kftraining.PyTorchJobReplicaTypeWorker]
					g.Expect(ok).To(gomega.BeTrue(), "Worker replica status not found in PyTorch status")

					// Check the number of active replicas
					g.Expect(masterReplicaStatus.Active).To(gomega.Equal(int32(1)), "Unexpected number of active %s replicas", kftraining.PyTorchJobReplicaTypeMaster)
					g.Expect(workerReplicaStatus.Active).To(gomega.Equal(int32(2)), "Unexpected number of active %s replicas", kftraining.PyTorchJobReplicaTypeWorker)

					// Ensure PyTorch job has "Running" condition with status "True"
					g.Expect(createdPyTorch.Status.Conditions).To(gomega.ContainElements(
						gomega.BeComparableTo(kftraining.JobCondition{
							Type:   kftraining.JobRunning,
							Status: corev1.ConditionTrue,
						}, cmpopts.IgnoreFields(kftraining.JobCondition{}, "Reason", "Message", "LastUpdateTime", "LastTransitionTime")),
					))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			wlLookupKey := types.NamespacedName{
				Name:      pytorchjob.GetWorkloadNameForPyTorchJob(pytorch.Name, pytorch.UID),
				Namespace: ns.Name,
			}
			createdWorkload := &kueue.Workload{}
			ginkgo.By("Check workload is created", func() {
				gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			})

			ginkgo.By("Check workload is admitted", func() {
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, createdWorkload)
			})

			ginkgo.By("Check workload is finished", func() {
				// Wait for active pods and terminate them
				util.WaitForActivePodsAndTerminate(ctx, k8sClient, restClient, cfg, ns.Name, 3, 0, client.InNamespace(ns.Name))

				util.ExpectWorkloadToFinish(ctx, k8sClient, wlLookupKey)
			})

			ginkgo.By("Delete the PyTorch", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, pytorch, true)
			})

			ginkgo.By("Check workload is deleted", func() {
				util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, createdWorkload, false, util.LongTimeout)
			})
		})
	})
})
