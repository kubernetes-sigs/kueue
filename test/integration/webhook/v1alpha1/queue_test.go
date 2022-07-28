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

package v1alpha1

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/util/testing"
)

const queueName = "queue-test"

var _ = ginkgo.Describe("Queue validating webhook", func() {
	ginkgo.When("Updating a Queue", func() {
		ginkgo.It("Should allow the change of status", func() {
			ginkgo.By("Creating a new Queue")
			obj := testing.MakeQueue(queueName, ns.Name).ClusterQueue("foo").Obj()
			gomega.Expect(k8sClient.Create(ctx, obj)).Should(gomega.Succeed())

			ginkgo.By("Updating the Queue status")
			obj.Status.PendingWorkloads = 3
			gomega.Expect(k8sClient.Status().Update(ctx, obj)).Should(gomega.Succeed())
			var after v1alpha1.Queue
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: obj.Name, Namespace: obj.Namespace}, &after)).Should(gomega.Succeed())
			gomega.Expect(after.Status.PendingWorkloads).Should(gomega.Equal(int32(3)))
		})

		ginkgo.It("Should reject the change of spec.clusterQueue", func() {
			ginkgo.By("Creating a new Queue")
			obj := testing.MakeQueue(queueName, ns.Name).ClusterQueue("foo").Obj()
			gomega.Expect(k8sClient.Create(ctx, obj)).Should(gomega.Succeed())

			ginkgo.By("Updating the Queue")
			obj.Spec.ClusterQueue = "bar"
			err := k8sClient.Update(ctx, obj)
			gomega.Expect(err).Should(gomega.HaveOccurred())
			gomega.Expect(errors.IsForbidden(err)).Should(gomega.BeTrue(), "error: %v", err)
		})
	})
})
