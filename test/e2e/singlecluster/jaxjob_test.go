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

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/jobs/jaxjob"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	jaxjobtesting "sigs.k8s.io/kueue/pkg/util/testingjobs/jaxjob"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("JAX integration", func() {
	var (
		ns                 *corev1.Namespace
		rf                 *kueue.ResourceFlavor
		cq                 *kueue.ClusterQueue
		lq                 *kueue.LocalQueue
		resourceFlavorName string
		clusterQueueName   string
		localQueueName     string
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "jax-e2e-")
		resourceFlavorName = "jax-rf-" + ns.Name
		clusterQueueName = "jax-cq-" + ns.Name
		localQueueName = "jax-lq-" + ns.Name

		rf = utiltestingapi.MakeResourceFlavor(resourceFlavorName).Obj()
		util.MustCreate(ctx, k8sClient, rf)

		cq = utiltestingapi.MakeClusterQueue(clusterQueueName).
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(resourceFlavorName).
					Resource(corev1.ResourceCPU, "5").
					Resource(corev1.ResourceMemory, "10Gi").
					Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

		lq = utiltestingapi.MakeLocalQueue(localQueueName, ns.Name).ClusterQueue(cq.Name).Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteAllJAXJobsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.When("JAX created", func() {
		ginkgo.It("should admit group with leader only", func() {
			jax := jaxjobtesting.MakeJAXJob("jax-simple", ns.Name).
				Label("kueue.x-k8s.io/queue-name", localQueueName).
				Suspend(false).
				SetTypeMeta().
				JAXReplicaSpecsDefault().
				Parallelism(kftraining.JAXJobReplicaTypeWorker, 2).
				Image(kftraining.JAXJobReplicaTypeWorker, util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Request(kftraining.JAXJobReplicaTypeWorker, corev1.ResourceCPU, "1").
				Request(kftraining.JAXJobReplicaTypeWorker, corev1.ResourceMemory, "200Mi").
				Obj()

			ginkgo.By("Create a JAX", func() {
				util.MustCreate(ctx, k8sClient, jax)
			})

			ginkgo.By("Waiting for replicas to be ready", func() {
				createdJAX := &kftraining.JAXJob{}

				gomega.Eventually(func(g gomega.Gomega) {
					// Fetch the JAX object
					err := k8sClient.Get(ctx, client.ObjectKeyFromObject(jax), createdJAX)
					g.Expect(err).ToNot(gomega.HaveOccurred(), "Failed to fetch JAX")

					// Check the worker replica status exists
					workerReplicaStatus, ok := createdJAX.Status.ReplicaStatuses[kftraining.JAXJobReplicaTypeWorker]
					g.Expect(ok).To(gomega.BeTrue(), "Worker replica status not found in JAX status")

					// Check the number of active worker replicas
					g.Expect(workerReplicaStatus.Active).To(gomega.Equal(int32(2)), "Unexpected number of active worker replicas")

					// Ensure JAX job has "Running" condition with status "True"
					g.Expect(createdJAX.Status.Conditions).To(gomega.ContainElements(
						gomega.BeComparableTo(kftraining.JobCondition{
							Type:   kftraining.JobRunning,
							Status: corev1.ConditionTrue,
						}, cmpopts.IgnoreFields(kftraining.JobCondition{}, "Reason", "Message", "LastUpdateTime", "LastTransitionTime")),
					))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			wlLookupKey := types.NamespacedName{
				Name:      jaxjob.GetWorkloadNameForJAXJob(jax.Name, jax.UID),
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
				util.WaitForActivePodsAndTerminate(ctx, k8sClient, restClient, cfg, ns.Name, 2, 0, client.InNamespace(ns.Name))

				util.ExpectWorkloadToFinish(ctx, k8sClient, wlLookupKey)
			})

			ginkgo.By("Delete the JAX", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, jax, true)
			})

			ginkgo.By("Check workload is deleted", func() {
				util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, createdWorkload, false, util.LongTimeout)
			})
		})
	})
})
