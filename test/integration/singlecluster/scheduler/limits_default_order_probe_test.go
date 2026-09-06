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

package scheduler

import (
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

// Probe for: when a namespace LimitRange sets defaultRequest and a container
// declares only limits, the real Pod will request its limits (requests default
// from limits at the API level, before the LimitRange admission plugin runs),
// so Kueue must account the limits value, not the LimitRange default.
var _ = ginkgo.Describe("LimitRange default vs limits-only accounting probe", func() {
	var (
		ns             *corev1.Namespace
		onDemandFlavor *kueue.ResourceFlavor
		clusterQueue   *kueue.ClusterQueue
		localQueue     *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "lr-order-probe-")
		onDemandFlavor = utiltestingapi.MakeResourceFlavor("on-demand").Obj()
		util.MustCreate(ctx, k8sClient, onDemandFlavor)
		clusterQueue = utiltestingapi.MakeClusterQueue("cq-lr-order").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(onDemandFlavor.Name).
				Resource(corev1.ResourceCPU, "10").Obj()).
			Obj()
		util.MustCreate(ctx, k8sClient, clusterQueue)
		util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)
		localQueue = utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		util.MustCreate(ctx, k8sClient, localQueue)
		util.ExpectLocalQueuesToBeActive(ctx, k8sClient, localQueue)

		limitRange := utiltesting.MakeLimitRange("limits", ns.Name).
			WithValue("DefaultRequest", corev1.ResourceCPU, "1").Obj()
		util.MustCreate(ctx, k8sClient, limitRange)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
	})

	ginkgo.It("accounts a limits-only workload by its limits, matching pod semantics", func() {
		wl := utiltestingapi.MakeWorkload("limits-only", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Limit(corev1.ResourceCPU, "3").
			Obj()
		util.MustCreate(ctx, k8sClient, wl)

		ginkgo.By("waiting for admission", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				read := kueue.Workload{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &read)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&read)).To(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("checking the queue books the limits value, not the LimitRange default", func() {
			updatedCQ := kueue.ClusterQueue{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCQ)).To(gomega.Succeed())
				g.Expect(updatedCQ.Status.FlavorsReservation).Should(gomega.BeComparableTo([]kueue.FlavorUsage{{
					Name: kueue.ResourceFlavorReference(onDemandFlavor.Name),
					Resources: []kueue.ResourceUsage{{
						Name:  corev1.ResourceCPU,
						Total: resource.MustParse("3"),
					}},
				}}, cmpopts.IgnoreFields(kueue.ResourceUsage{}, "Borrowed")))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
