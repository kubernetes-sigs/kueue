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

package core

import (
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/controller-runtime/pkg/client"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/integration/framework"
)

// +kubebuilder:docs-gen:collapse=Imports

var _ = ginkgo.Describe("Workload controller", func() {
	var (
		ns                   *corev1.Namespace
		updatedQueueWorkload kueue.Workload
		queue                *kueue.Queue
		wl                   *kueue.Workload
		message              string
	)

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "core-workload-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
	})

	ginkgo.When("the queue is not defined in the workload", func() {
		ginkgo.AfterEach(func() {
			updatedQueueWorkload = kueue.Workload{}
			gomega.Expect(framework.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		})
		ginkgo.It("Should update status when workloads are created", func() {
			wl = testing.MakeWorkload("one", ns.Name).Request(corev1.ResourceCPU, "1").Obj()
			message = fmt.Sprintf("Queue %s doesn't exist", "")
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())
			gomega.Eventually(func() int {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedQueueWorkload)).To(gomega.Succeed())
				return len(updatedQueueWorkload.Status.Conditions)
			}, framework.Timeout, framework.Interval).Should(testing.Equal(1))
			gomega.Expect(updatedQueueWorkload.Status.Conditions[0].Message).To(testing.Equal(message))
		})
	})

	ginkgo.When("the queue doesn't exist", func() {
		ginkgo.AfterEach(func() {
			updatedQueueWorkload = kueue.Workload{}
			gomega.Expect(framework.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		})
		ginkgo.It("Should update status when workloads are created", func() {
			wl = testing.MakeWorkload("two", ns.Name).Queue("nonCreatedQueue").Request(corev1.ResourceCPU, "1").Obj()
			message = fmt.Sprintf("Queue %s doesn't exist", "nonCreatedQueue")
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())
			gomega.Eventually(func() int {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedQueueWorkload)).To(gomega.Succeed())
				return len(updatedQueueWorkload.Status.Conditions)
			}, framework.Timeout, framework.Interval).Should(testing.Equal(1))
			gomega.Expect(updatedQueueWorkload.Status.Conditions[0].Message).To(testing.Equal(message))
		})
	})

	ginkgo.When("the clusterqueue doesn't exist", func() {
		ginkgo.BeforeEach(func() {
			queue = testing.MakeQueue("queue", ns.Name).ClusterQueue("fooclusterqueue").Obj()
			gomega.Expect(k8sClient.Create(ctx, queue)).To(gomega.Succeed())
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(framework.DeleteQueue(ctx, k8sClient, queue)).To(gomega.Succeed())
			gomega.Expect(framework.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			updatedQueueWorkload = kueue.Workload{}
		})
		ginkgo.It("Should update status when workloads are created", func() {
			wl = testing.MakeWorkload("three", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "1").Obj()
			message = fmt.Sprintf("ClusterQueue %s doesn't exist", "fooclusterqueue")
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())
			gomega.Eventually(func() []kueue.WorkloadCondition {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedQueueWorkload)).To(gomega.Succeed())
				return updatedQueueWorkload.Status.Conditions
			}, framework.Timeout, framework.Interval).ShouldNot(gomega.BeNil())
			gomega.Expect(updatedQueueWorkload.Status.Conditions[0].Message).To(testing.Equal(message))
		})
	})
})
