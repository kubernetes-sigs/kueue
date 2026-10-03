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
	kftrainerapi "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadtrainjob "sigs.k8s.io/kueue/pkg/controller/jobs/trainjob"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingtrainjob "sigs.k8s.io/kueue/pkg/util/testingjobs/trainjob"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("TrainJob", ginkgo.Label("area:singlecluster", "feature:trainjob"), func() {
	var ns *corev1.Namespace

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-")
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})
	ginkgo.When("Creating a TrainJob", func() {
		var (
			defaultRf    *kueue.ResourceFlavor
			localQueue   *kueue.LocalQueue
			clusterQueue *kueue.ClusterQueue
		)
		ginkgo.BeforeEach(func() {
			defaultRf = utiltestingapi.MakeResourceFlavor("default-" + ns.Name).Obj()
			behavioral.MustCreate(ctx, k8sClient, defaultRf)
			clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue-" + ns.Name).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(defaultRf.Name).
						Resource(corev1.ResourceCPU, "2").
						Resource(corev1.ResourceMemory, "2G").Obj()).Obj()
			behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)
			localQueue = utiltestingapi.MakeLocalQueue("main", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(behavioral.DeleteAllTrainJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			// Force remove workloads to be sure that cluster queue can be removed.
			gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(behavioral.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, defaultRf, true)
			behavioral.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
		})

		ginkgo.It("Should run a TrainJob if admitted", func() {
			trainjob := testingtrainjob.MakeTrainJob("trainjob-test", ns.Name).
				RuntimeRefName("torch-distributed").
				Queue("main").
				// Even if we override the image coming from the TrainingRuntime, we still need to set the command and args
				TrainerImage(behavioral.GetAgnHostImage(), []string{"/agnhost"}, behavioral.BehaviorExitFast).
				TrainerRequest(corev1.ResourceCPU, "500m").
				TrainerRequest(corev1.ResourceMemory, "200Mi").
				Obj()

			ginkgo.By("Creating the trainjob", func() {
				behavioral.MustCreate(ctx, k8sClient, trainjob)
			})

			createdWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: workloadtrainjob.GetWorkloadNameForTrainJob(trainjob.Name, trainjob.UID), Namespace: ns.Name}
			ginkgo.By("Ensuring that a workload is created and the job admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					g.Expect(createdWorkload.Status.Conditions).To(utiltesting.HaveConditionStatus(kueue.WorkloadAdmitted, metav1.ConditionTrue))
				}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Waiting for the trainjob to be unsuspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(trainjob), trainjob)).To(gomega.Succeed())
					g.Expect(trainjob.Spec.Suspend).Should(gomega.BeEquivalentTo(new(false)))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Waiting for the trainjob to finish", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(trainjob), trainjob)).To(gomega.Succeed())
					g.Expect(trainjob.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kftrainerapi.TrainJobComplete), "trainjob is complete")
				}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed(), behavioral.AssertMsg[runtime.Object]("TrainJob or Workload did not finish", trainjob))

				behavioral.ExpectWorkloadToFinishWithTimeout(ctx, k8sClient, wlLookupKey, behavioral.LongTimeout)
			})
		})
	})

	ginkgo.When("Using resource flavors with node selectors", func() {
		var (
			onDemandRF   *kueue.ResourceFlavor
			spotRF       *kueue.ResourceFlavor
			localQueue   *kueue.LocalQueue
			clusterQueue *kueue.ClusterQueue
		)
		ginkgo.BeforeEach(func() {
			onDemandRF = utiltestingapi.MakeResourceFlavor("on-demand-"+ns.Name).
				NodeLabel("instance-type", "on-demand").Obj()
			behavioral.MustCreate(ctx, k8sClient, onDemandRF)
			spotRF = utiltestingapi.MakeResourceFlavor("spot-"+ns.Name).
				NodeLabel("instance-type", "spot").Obj()
			behavioral.MustCreate(ctx, k8sClient, spotRF)
			clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue-"+ns.Name).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(onDemandRF.Name).
						Resource(corev1.ResourceCPU, "1").
						Resource(corev1.ResourceMemory, "1Gi").
						Obj(),
					*utiltestingapi.MakeFlavorQuotas(spotRF.Name).
						Resource(corev1.ResourceCPU, "1").
						Resource(corev1.ResourceMemory, "1Gi").
						Obj(),
				).
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
				}).
				Obj()
			behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)
			localQueue = utiltestingapi.MakeLocalQueue("main", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(behavioral.DeleteAllTrainJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			// Force remove workloads to be sure that cluster queue can be removed.
			gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(behavioral.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandRF, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, spotRF, true)
			behavioral.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
		})

		ginkgo.It("Should allow to suspend a TrainJob when injected nodeSelector", func() {
			trainjob := testingtrainjob.MakeTrainJob("trainjob-test", ns.Name).
				RuntimeRefName("torch-distributed").
				Queue("main").
				// Even if we override the image coming from the TrainingRuntime, we still need to set the command and args
				TrainerImage(behavioral.GetAgnHostImage(), []string{"/agnhost"}, behavioral.BehaviorWaitForDeletion).
				TrainerRequest(corev1.ResourceCPU, "500m").
				TrainerRequest(corev1.ResourceMemory, "200Mi").
				Obj()

			ginkgo.By("Creating the trainjob", func() {
				behavioral.MustCreate(ctx, k8sClient, trainjob)
			})

			createdWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: workloadtrainjob.GetWorkloadNameForTrainJob(trainjob.Name, trainjob.UID), Namespace: ns.Name}
			ginkgo.By("Ensuring that a workload is created and the job admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					g.Expect(createdWorkload.Status.Conditions).To(utiltesting.HaveConditionStatus(kueue.WorkloadAdmitted, metav1.ConditionTrue))
				}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Waiting for the trainjob to be unsuspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(trainjob), trainjob)).To(gomega.Succeed())
					g.Expect(trainjob.Spec.Suspend).Should(gomega.BeEquivalentTo(new(false)))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Verify that the trainjob has a runtime patch from kueue", func() {
				gomega.Expect(testingtrainjob.KueueRuntimePatch(trainjob)).ToNot(gomega.BeNil())
			})

			ginkgo.By("Verify the trainjob has nodeSelector set", func() {
				rJobs := testingtrainjob.KueueRuntimePatch(trainjob).TrainingRuntimeSpec.Template.Spec.ReplicatedJobs
				gomega.Expect(rJobs).To(gomega.HaveLen(1))
				gomega.Expect(rJobs[0].Template.Spec.Template.Spec.NodeSelector).To(gomega.Equal(
					map[string]string{
						"instance-type": "on-demand",
					},
				))
			})

			ginkgo.By("Stopping the ClusterQueue to make the TrainJob be stopped and suspended")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), clusterQueue)).To(gomega.Succeed())
				clusterQueue.Spec.StopPolicy = new(kueue.HoldAndDrain)
				g.Expect(k8sClient.Update(ctx, clusterQueue)).Should(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			ginkgo.By("Waiting for the TrainJob to be suspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(trainjob), trainjob)).To(gomega.Succeed())
					g.Expect(trainjob.Spec.Suspend).Should(gomega.BeEquivalentTo(new(true)))
				}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
			})
		})
	})
})
