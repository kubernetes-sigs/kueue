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

package webhook

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/util"
)

const queueName = "queue-test"

var _ = ginkgo.Describe("Queue validating webhook", func() {
	ginkgo.When("Updating a Queue", func() {
		ginkgo.It("Should allow the change of status", func() {
			ginkgo.By("Creating a new Queue")
			obj := testing.MakeLocalQueue(queueName, ns.Name).ClusterQueue("foo").Obj()
			gomega.Expect(k8sClient.Create(ctx, obj)).Should(gomega.Succeed())

			ginkgo.By("Updating the Queue status")
			gomega.Eventually(func() error {
				var updatedQ kueue.LocalQueue
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(obj), &updatedQ)).Should(gomega.Succeed())
				updatedQ.Status.PendingWorkloads = 3
				return k8sClient.Status().Update(ctx, &updatedQ)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			var after kueue.LocalQueue
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: obj.Name, Namespace: obj.Namespace}, &after)).Should(gomega.Succeed())
			gomega.Expect(after.Status.PendingWorkloads).Should(gomega.Equal(int32(3)))
		})

		ginkgo.It("Should reject the change of spec.clusterQueue", func() {
			ginkgo.By("Creating a new Queue")
			obj := testing.MakeLocalQueue(queueName, ns.Name).ClusterQueue("foo").Obj()
			gomega.Expect(k8sClient.Create(ctx, obj)).Should(gomega.Succeed())

			ginkgo.By("Updating the Queue")
			gomega.Eventually(func() error {
				var updatedQ kueue.LocalQueue
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(obj), &updatedQ)).Should(gomega.Succeed())
				updatedQ.Spec.ClusterQueue = "bar"
				return k8sClient.Update(ctx, &updatedQ)
			}, util.Timeout, util.Interval).Should(testing.BeForbiddenError())
		})
	})
})
