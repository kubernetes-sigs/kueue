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
	"context"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util"
)

const queueName = "queue-test"

var _ = ginkgo.Describe("Queue validating webhook", ginkgo.Ordered, func() {
	var _ = ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-")
	})
	var _ = ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})
	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, func(ctx context.Context, mgr manager.Manager) {
			managerSetup(ctx, mgr)
		})
	})
	ginkgo.AfterAll(func() {
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
})
