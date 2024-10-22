/*
Copyright 2022 The Kubernetes Authors.

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
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

// +kubebuilder:docs-gen:collapse=Imports

const (
	topologyLevel = "kubernetes.io/hostname"
)

var _ = ginkgo.Describe("Kueue", func() {
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

	ginkgo.When("Creating a Job With Queueing", func() {
		var (
			topology     *kueuealpha.Topology
			onDemandRF   *kueue.ResourceFlavor
			localQueue   *kueue.LocalQueue
			clusterQueue *kueue.ClusterQueue
		)
		ginkgo.BeforeEach(func() {
			topology = testing.MakeTopology("hostname").Levels([]string{
				topologyLevel,
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
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
				}).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).Should(gomega.Succeed())
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
				PodAnnotation(kueuealpha.PodSetRequiredTopologyAnnotation, topologyLevel).
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
				gomega.Eventually(func() bool {
					if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
						return false
					}
					return createdWorkload.Status.Admission != nil
				}, util.LongTimeout, util.Interval).Should(gomega.BeTrue())
				gomega.Expect(createdWorkload.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
				gomega.Expect(createdWorkload.Status.Admission.PodSetAssignments[0].TopologyAssignment).ShouldNot(gomega.BeNil())
				gomega.Expect(createdWorkload.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
					&kueue.TopologyAssignment{
						Levels: []string{
							topologyLevel,
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
				gomega.Eventually(func() bool {
					if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
						return false
					}
					return workload.HasQuotaReservation(createdWorkload) &&
						apimeta.IsStatusConditionTrue(createdWorkload.Status.Conditions, kueue.WorkloadFinished)
				}, util.LongTimeout, util.Interval).Should(gomega.BeTrue())
			})
		})

		ginkgo.It("should allow to evict a TAS Job", func() {
			ginkgo.By("Create the high-priority")
			highPriorityClass := testing.MakePriorityClass("high").PriorityValue(100).Obj()
			gomega.Expect(k8sClient.Create(ctx, highPriorityClass)).Should(gomega.Succeed())
			ginkgo.DeferCleanup(func() {
				gomega.Expect(k8sClient.Delete(ctx, highPriorityClass)).To(gomega.Succeed())
			})

			ginkgo.By("Create the low priority Job")
			lowPriorityJob := testingjob.MakeJob("low-priority-test-job", ns.Name).
				Queue(localQueue.Name).
				Request("cpu", "700m").
				Request("memory", "20Mi").
				Obj()
			lowPriorityJob = (&testingjob.JobWrapper{Job: *lowPriorityJob}).
				PodAnnotation(kueuealpha.PodSetRequiredTopologyAnnotation, topologyLevel).
				Image(util.E2eTestSleepImage, []string{"-termination-code=1", "10m"}).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, lowPriorityJob)).Should(gomega.Succeed())

			ginkgo.By("Verify the low-priority Job is running", func() {
				lowPriorityKey := client.ObjectKeyFromObject(lowPriorityJob)
				expectJobUnsuspendedWithNodeSelectors(lowPriorityKey, map[string]string{
					"instance-type": "on-demand",
				})
			})

			ginkgo.By("Create the high-priority Job")
			highPriorityJob := testingjob.MakeJob("high-priority-test-job", ns.Name).
				PriorityClass(highPriorityClass.Name).
				Queue(localQueue.Name).
				Request("cpu", "700m").
				Request("memory", "20Mi").
				Obj()
			highPriorityJob = (&testingjob.JobWrapper{Job: *highPriorityJob}).
				PodAnnotation(kueuealpha.PodSetRequiredTopologyAnnotation, topologyLevel).
				Image(util.E2eTestSleepImage, []string{"1ms"}).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, highPriorityJob)).Should(gomega.Succeed())

			highPriorityWlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(highPriorityJob.Name, highPriorityJob.UID), Namespace: ns.Name}
			ginkgo.By(fmt.Sprintf("verify the high-priority workload %q gets finished", highPriorityWlKey), func() {
				highPriorityWorkload := &kueue.Workload{}
				gomega.Eventually(func() bool {
					if err := k8sClient.Get(ctx, highPriorityWlKey, highPriorityWorkload); err != nil {
						return false
					}
					return workload.HasQuotaReservation(highPriorityWorkload) &&
						apimeta.IsStatusConditionTrue(highPriorityWorkload.Status.Conditions, kueue.WorkloadFinished)
				}, util.LongTimeout, util.Interval).Should(gomega.BeTrue())
			})
		})
	})
})
