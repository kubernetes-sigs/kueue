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

package conversion

import (
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("v1beta2 conversions", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	const (
		flavorModelC = "model-c"
		flavorModelD = "model-d"

		resourceGPU corev1.ResourceName = "example.com/gpu"
	)
	var (
		ns              *corev1.Namespace
		localQueue      *kueue.LocalQueue
		clusterQueues   []*kueue.ClusterQueue
		workloads       []*kueue.Workload
		resourceFlavors = []kueue.ResourceFlavor{
			*utiltestingapi.MakeResourceFlavor(flavorModelC).NodeLabel(resourceGPU.String(), flavorModelC).Obj(),
			*utiltestingapi.MakeResourceFlavor(flavorModelD).NodeLabel(resourceGPU.String(), flavorModelD).Obj(),
		}
		emptyUsage = []kueue.LocalQueueFlavorUsage{
			{
				Name: flavorModelC,
				Resources: []kueue.LocalQueueResourceUsage{
					{
						Name:  resourceGPU,
						Total: resource.MustParse("0"),
					},
				},
			},
			{
				Name: flavorModelD,
				Resources: []kueue.LocalQueueResourceUsage{
					{
						Name:  resourceGPU,
						Total: resource.MustParse("0"),
					},
				},
			},
		}
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-queue-")
	})

	ginkgo.BeforeEach(func() {
		cq1 := utiltestingapi.MakeClusterQueue("cluster-queue.queue-controller").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(flavorModelC).Resource(resourceGPU, "2", "2").Obj(),
				*utiltestingapi.MakeFlavorQuotas(flavorModelD).Resource(resourceGPU, "2", "2").Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			Cohort("cohort").
			Obj()
		cq2 := utiltestingapi.MakeClusterQueue("shared-pool").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(flavorModelC).Resource(resourceGPU, "2", "2").Obj(),
				*utiltestingapi.MakeFlavorQuotas(flavorModelD).Resource(resourceGPU, "2", "2").Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			Cohort("cohort").
			Obj()
		clusterQueues = []*kueue.ClusterQueue{cq1, cq2}
		localQueue = utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq1.Name).Obj()
		util.MustCreate(ctx, k8sClient, localQueue)
	})

	ginkgo.AfterEach(func() {
		for _, wl := range workloads {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, wl, true)
		}
		util.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
		for _, cq := range clusterQueues {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		}
		for _, rf := range resourceFlavors {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, &rf, true)
		}
	})

	ginkgo.It("Should update status when workloads are created", framework.SlowSpec, func() {
		ginkgo.By("Creating resourceFlavors")
		for _, rf := range resourceFlavors {
			util.MustCreate(ctx, k8sClient, &rf)
		}

		ginkgo.By("Creating clusterQueues")
		for _, cq := range clusterQueues {
			util.MustCreate(ctx, k8sClient, cq)
		}

		ginkgo.By("Verify the cohort setting on the clusterQueue")
		gomega.Eventually(func(g gomega.Gomega) {
			var updatedCq kueue.ClusterQueue
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueues[0]), &updatedCq)).To(gomega.Succeed())
			g.Expect(updatedCq.Spec.CohortName).Should(gomega.Equal(kueue.CohortReference("cohort")))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("await for the LocalQueue to be ready")
		gomega.Eventually(func(g gomega.Gomega) {
			var updatedQueue kueue.LocalQueue
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(localQueue), &updatedQueue)).To(gomega.Succeed())
			g.Expect(updatedQueue.Status).Should(gomega.BeComparableTo(kueue.LocalQueueStatus{
				Conditions: []metav1.Condition{
					{
						Type:    kueue.LocalQueueActive,
						Status:  metav1.ConditionTrue,
						Reason:  "Ready",
						Message: "Can submit new workloads to localQueue",
					},
				},
				FlavorsReservation: emptyUsage,
				FlavorsUsage:       emptyUsage,
			}, util.IgnoreConditionTimestampsAndObservedGeneration, cmpopts.IgnoreFields(kueue.LocalQueueStatus{}, "Flavors")))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Creating workloads wave1")
		workload1 := utiltestingapi.MakeWorkload("one", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Request(resourceGPU, "4").
			Obj()
		util.MustCreate(ctx, k8sClient, workload1)
		workloads = append(workloads, workload1)

		ginkgo.By("Verify the LocalQueue flavor usage is updated correctly")
		partUsage := []kueue.LocalQueueFlavorUsage{
			{
				Name: flavorModelC,
				Resources: []kueue.LocalQueueResourceUsage{
					{
						Name:  resourceGPU,
						Total: resource.MustParse("4"),
					},
				},
			},
			{
				Name: flavorModelD,
				Resources: []kueue.LocalQueueResourceUsage{
					{
						Name:  resourceGPU,
						Total: resource.MustParse("0"),
					},
				},
			},
		}
		gomega.Eventually(func(g gomega.Gomega) {
			var updatedQueue kueue.LocalQueue
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(localQueue), &updatedQueue)).To(gomega.Succeed())
			g.Expect(updatedQueue.Status).Should(gomega.BeComparableTo(kueue.LocalQueueStatus{
				ReservingWorkloads: 1,
				AdmittedWorkloads:  1,
				PendingWorkloads:   0,
				Conditions: []metav1.Condition{
					{
						Type:    kueue.LocalQueueActive,
						Status:  metav1.ConditionTrue,
						Reason:  "Ready",
						Message: "Can submit new workloads to localQueue",
					},
				},
				FlavorsReservation: partUsage,
				FlavorsUsage:       partUsage,
			}, util.IgnoreConditionTimestampsAndObservedGeneration, cmpopts.IgnoreFields(kueue.LocalQueueStatus{}, "Flavors")))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Creating workloads wave2")
		workload2 := utiltestingapi.MakeWorkload("two", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Request(resourceGPU, "4").
			Obj()
		util.MustCreate(ctx, k8sClient, workload2)
		workloads = append(workloads, workload2)

		ginkgo.By("Verify the LocalQueue flavor usage is updated correctly")
		fullUsage := []kueue.LocalQueueFlavorUsage{
			{
				Name: flavorModelC,
				Resources: []kueue.LocalQueueResourceUsage{
					{
						Name:  resourceGPU,
						Total: resource.MustParse("4"),
					},
				},
			},
			{
				Name: flavorModelD,
				Resources: []kueue.LocalQueueResourceUsage{
					{
						Name:  resourceGPU,
						Total: resource.MustParse("4"),
					},
				},
			},
		}
		gomega.Eventually(func(g gomega.Gomega) {
			var updatedQueue kueue.LocalQueue
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(localQueue), &updatedQueue)).To(gomega.Succeed())
			g.Expect(updatedQueue.Status).Should(gomega.BeComparableTo(kueue.LocalQueueStatus{
				ReservingWorkloads: 2,
				AdmittedWorkloads:  2,
				PendingWorkloads:   0,
				Conditions: []metav1.Condition{
					{
						Type:    kueue.LocalQueueActive,
						Status:  metav1.ConditionTrue,
						Reason:  "Ready",
						Message: "Can submit new workloads to localQueue",
					},
				},
				FlavorsReservation: fullUsage,
				FlavorsUsage:       fullUsage,
			}, util.IgnoreConditionTimestampsAndObservedGeneration, cmpopts.IgnoreFields(kueue.LocalQueueStatus{}, "Flavors")))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})
})
