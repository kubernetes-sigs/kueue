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

package core

import (
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util"
)

const queueName = "queue-test"

var _ = ginkgo.Describe("Queue validating webhook", func() {
	var _ = ginkgo.BeforeEach(func() {
		fwk.StartManager(ctx, cfg, managerSetup)
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-")
	})
	var _ = ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		fwk.StopManager(ctx)
	})
	ginkgo.When("Updating a Queue", func() {
		ginkgo.It("Should reject bad value for spec.clusterQueue", func() {
			ginkgo.By("Creating a new Queue")
			obj := utiltestingapi.MakeLocalQueue(queueName, ns.Name).ClusterQueue("invalid_name").Obj()
			gomega.Expect(k8sClient.Create(ctx, obj)).Should(utiltesting.BeInvalidError())
		})
		ginkgo.It("Should reject the change of spec.clusterQueue", func() {
			ginkgo.By("Creating a new Queue")
			obj := utiltestingapi.MakeLocalQueue(queueName, ns.Name).ClusterQueue("foo").Obj()
			util.MustCreate(ctx, k8sClient, obj)

			ginkgo.By("Updating the Queue")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedQ kueue.LocalQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(obj), &updatedQ)).Should(gomega.Succeed())
				updatedQ.Spec.ClusterQueue = "bar"
				g.Expect(k8sClient.Update(ctx, &updatedQ)).Should(utiltesting.BeInvalidError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
	ginkgo.When("Updating the status of a Queue", func() {
		ginkgo.It("Should allow flavors quantity up to the limit in flavorsReservation and flavorsUsage", func() {
			ginkgo.By("Creating a new Queue")
			obj := utiltestingapi.MakeLocalQueue(queueName, ns.Name).ClusterQueue("foo").Obj()
			util.MustCreate(ctx, k8sClient, obj)

			ginkgo.By("Updating the Queue status")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedQ kueue.LocalQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(obj), &updatedQ)).Should(gomega.Succeed())
				updatedQ.Status.FlavorsReservation = makeLocalQueueFlavorUsage(flavorsMaxItems)
				updatedQ.Status.FlavorsUsage = makeLocalQueueFlavorUsage(flavorsMaxItems)
				g.Expect(k8sClient.Status().Update(ctx, &updatedQ)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
		ginkgo.It("Should reject flavors quantity over the limit in flavorsReservation", func() {
			ginkgo.By("Creating a new Queue")
			obj := utiltestingapi.MakeLocalQueue(queueName, ns.Name).ClusterQueue("foo").Obj()
			util.MustCreate(ctx, k8sClient, obj)

			ginkgo.By("Updating the Queue status")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedQ kueue.LocalQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(obj), &updatedQ)).Should(gomega.Succeed())
				updatedQ.Status.FlavorsReservation = makeLocalQueueFlavorUsage(flavorsMaxItems + 1)
				g.Expect(k8sClient.Status().Update(ctx, &updatedQ)).Should(utiltesting.BeInvalidError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
		ginkgo.It("Should reject flavors quantity over the limit in flavorsUsage", func() {
			ginkgo.By("Creating a new Queue")
			obj := utiltestingapi.MakeLocalQueue(queueName, ns.Name).ClusterQueue("foo").Obj()
			util.MustCreate(ctx, k8sClient, obj)

			ginkgo.By("Updating the Queue status")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedQ kueue.LocalQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(obj), &updatedQ)).Should(gomega.Succeed())
				updatedQ.Status.FlavorsUsage = makeLocalQueueFlavorUsage(flavorsMaxItems + 1)
				g.Expect(k8sClient.Status().Update(ctx, &updatedQ)).Should(utiltesting.BeInvalidError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})

func makeLocalQueueFlavorUsage(quantity int) []kueue.LocalQueueFlavorUsage {
	usage := make([]kueue.LocalQueueFlavorUsage, quantity)
	for i := range usage {
		usage[i] = kueue.LocalQueueFlavorUsage{
			Name: kueue.ResourceFlavorReference(fmt.Sprintf("f%d", i)),
			Resources: []kueue.LocalQueueResourceUsage{{
				Name: corev1.ResourceCPU,
			}},
		}
	}
	return usage
}
