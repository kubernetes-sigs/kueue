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

package podsready

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha2"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/util"
)

// +kubebuilder:docs-gen:collapse=Imports

var _ = ginkgo.Describe("SchedulerWithWaitForPodsReady", func() {

	var (
		ns *corev1.Namespace
	)

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "core-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
	})

	ginkgo.When("Scheduling workloads on clusterQueues", func() {
		var (
			prodClusterQ *kueue.ClusterQueue
			devClusterQ  *kueue.ClusterQueue
			prodQueue    *kueue.LocalQueue
			devQueue     *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {

			prodClusterQ = testing.MakeClusterQueue("prod-cq").
				Cohort("all").
				Resource(testing.MakeResource(corev1.ResourceCPU).
					Flavor(testing.MakeFlavor(defaultFlavor.Name, "5").Obj()).
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, prodClusterQ)).Should(gomega.Succeed())

			devClusterQ = testing.MakeClusterQueue("dev-cq").
				Cohort("all").
				Resource(testing.MakeResource(corev1.ResourceCPU).
					Flavor(testing.MakeFlavor(defaultFlavor.Name, "5").Obj()).
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, devClusterQ)).Should(gomega.Succeed())

			prodQueue = testing.MakeLocalQueue("prod-queue", ns.Name).ClusterQueue(prodClusterQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, prodQueue)).Should(gomega.Succeed())

			devQueue = testing.MakeLocalQueue("dev-queue", ns.Name).ClusterQueue(devClusterQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, devQueue)).Should(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, prodClusterQ, true)
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, devClusterQ, true)
		})

		ginkgo.It("Should unblock admission of new workloads once the admitted workload is in PodsReady condition", func() {
			ginkgo.By("checking the first prod workload gets admitted while the second is waiting")
			prodWl := testing.MakeWorkload("prod-wl", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "2").Obj()
			gomega.Expect(k8sClient.Create(ctx, prodWl)).Should(gomega.Succeed())
			devWl := testing.MakeWorkload("dev-wl", ns.Name).Queue(devQueue.Name).Request(corev1.ResourceCPU, "2").Obj()
			gomega.Expect(k8sClient.Create(ctx, devWl)).Should(gomega.Succeed())
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, prodClusterQ.Name, prodWl)
			util.ExpectWorkloadsToBeWaiting(ctx, k8sClient, devWl)

			ginkgo.By("update the first workload to be in the PodsReady condition and verify the second workload is admitted")
			prodKey := types.NamespacedName{Name: prodWl.Name, Namespace: prodWl.Namespace}
			gomega.Expect(k8sClient.Get(ctx, prodKey, prodWl)).Should(gomega.Succeed())
			apimeta.SetStatusCondition(&prodWl.Status.Conditions, metav1.Condition{
				Type:   kueue.WorkloadPodsReady,
				Status: metav1.ConditionTrue,
				Reason: "PodsReady",
			})
			gomega.Expect(k8sClient.Status().Update(ctx, prodWl)).Should(gomega.Succeed())
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, devClusterQ.Name, devWl)
		})

		ginkgo.It("Should unblock admission of new workloads once the admitted workload is deleted", func() {
			ginkgo.By("checking the first prod workload gets admitted while the second is waiting")
			prodWl := testing.MakeWorkload("prod-wl", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "2").Obj()
			gomega.Expect(k8sClient.Create(ctx, prodWl)).Should(gomega.Succeed())
			devWl := testing.MakeWorkload("dev-wl", ns.Name).Queue(devQueue.Name).Request(corev1.ResourceCPU, "2").Obj()
			gomega.Expect(k8sClient.Create(ctx, devWl)).Should(gomega.Succeed())
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, prodClusterQ.Name, prodWl)
			util.ExpectWorkloadsToBeWaiting(ctx, k8sClient, devWl)

			ginkgo.By("delete the first workload and verify the second workload is admitted")
			gomega.Expect(k8sClient.Delete(ctx, prodWl)).Should(gomega.Succeed())
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, devClusterQ.Name, devWl)
		})

		ginkgo.It("Should block admission of one new workload if two are considered in the same scheduling cycle", func() {
			ginkgo.By("creating two workloads but delaying cluster queue creation which has enough capacity")
			prodWl := testing.MakeWorkload("prod-wl", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "11").Obj()
			gomega.Expect(k8sClient.Create(ctx, prodWl)).Should(gomega.Succeed())
			devWl := testing.MakeWorkload("dev-wl", ns.Name).Queue(devQueue.Name).Request(corev1.ResourceCPU, "11").Obj()
			gomega.Expect(k8sClient.Create(ctx, devWl)).Should(gomega.Succeed())
			util.ExpectWorkloadsToBePending(ctx, k8sClient, prodWl, devWl)

			ginkgo.By("creating the cluster queue")
			// Delay cluster queue creation to make sure workloads are in the same
			// scheduling cycle.
			testCQ := testing.MakeClusterQueue("test-cq").
				Cohort("all").
				Resource(testing.MakeResource(corev1.ResourceCPU).
					Flavor(testing.MakeFlavor(defaultFlavor.Name,
						"25").Max("25").Obj()).
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, testCQ)).Should(gomega.Succeed())
			defer func() {
				gomega.Expect(util.DeleteClusterQueue(ctx, k8sClient, testCQ)).Should(gomega.Succeed())
			}()

			ginkgo.By("verifying that the first created workload is admitted and the second workload is waiting as the first one has PodsReady=False")
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, prodClusterQ.Name, prodWl)
			util.ExpectWorkloadsToBeWaiting(ctx, k8sClient, devWl)
		})
	})

})
