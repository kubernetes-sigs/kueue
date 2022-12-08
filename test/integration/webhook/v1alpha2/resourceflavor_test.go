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

package v1alpha2

import (
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha2"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/integration/framework"
)

const (
	nodeSelectorMaxProperties = 8
	taintsMaxItems            = 8
)

var _ = ginkgo.Describe("ResourceFlavor Webhook", func() {
	var ns *corev1.Namespace

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "core-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(framework.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.When("Creating a ResourceFlavor", func() {
		ginkgo.It("Should have a finalizer", func() {
			ginkgo.By("Creating a new resourceFlavor")
			resourceFlavor := testing.MakeResourceFlavor("resource-flavor").Obj()
			gomega.Expect(k8sClient.Create(ctx, resourceFlavor)).Should(gomega.Succeed())
			defer func() {
				var rf kueue.ResourceFlavor
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(resourceFlavor), &rf)).Should(gomega.Succeed())
				controllerutil.RemoveFinalizer(&rf, kueue.ResourceInUseFinalizerName)
				gomega.Expect(k8sClient.Update(ctx, &rf)).Should(gomega.Succeed())
				framework.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, resourceFlavor, true)
			}()

			var created kueue.ResourceFlavor
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(resourceFlavor), &created)).Should(gomega.Succeed())
			gomega.Expect(created.GetFinalizers()).Should(gomega.Equal([]string{kueue.ResourceInUseFinalizerName}))
		})
	})

	ginkgo.When("Creating a ResourceFlavor with invalid taints", func() {
		ginkgo.It("Should fail to create", func() {
			ginkgo.By("Creating a new resourceFlavor")
			resourceFlavor := testing.MakeResourceFlavor("resource-flavor").Taint(corev1.Taint{
				Key:    "@foo",
				Value:  "bar",
				Effect: corev1.TaintEffectNoSchedule,
			}).Obj()
			err := k8sClient.Create(ctx, resourceFlavor)
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(errors.IsForbidden(err)).To(gomega.BeTrue(), "error: %v", err)
		})
	})

	ginkgo.DescribeTable("invalid number of properties", func(taintsCount int, nodeSelectorCount int, isInvalid bool) {
		rf := testing.MakeResourceFlavor("resource-flavor")
		for i := 0; i < taintsCount; i++ {
			rf.Taint(corev1.Taint{
				Key:    fmt.Sprintf("t%d", i),
				Effect: corev1.TaintEffectNoExecute,
			})
		}

		m := make(map[string]string)
		for i := 0; i < nodeSelectorCount; i++ {
			m[fmt.Sprintf("l%d", i)] = ""
		}

		resourceFlavor := rf.MultiLabels(m).Obj()
		err := k8sClient.Create(ctx, resourceFlavor)
		if isInvalid {
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(errors.IsInvalid(err)).To(gomega.BeTrue(), "error: %v", err)
		} else {
			gomega.Expect(err).To(gomega.Succeed())
			defer func() {
				var rf kueue.ResourceFlavor
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(resourceFlavor), &rf)).To(gomega.Succeed())
				controllerutil.RemoveFinalizer(&rf, kueue.ResourceInUseFinalizerName)
				gomega.Expect(k8sClient.Update(ctx, &rf)).Should(gomega.Succeed())
				framework.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, resourceFlavor, true)
			}()
		}
	},
		ginkgo.Entry("invalid number of taint", taintsMaxItems+1, 0, true),
		ginkgo.Entry("invalid number of nodeSelector", 0, nodeSelectorMaxProperties+1, true),
		ginkgo.Entry("valid number of nodeSelector and taint", taintsMaxItems, nodeSelectorMaxProperties, false),
	)

	ginkgo.When("Updating a ResourceFlavor with invalid taints", func() {
		ginkgo.It("Should fail to update", func() {
			ginkgo.By("Creating a new resourceFlavor")
			resourceFlavor := testing.MakeResourceFlavor("resource-flavor").Obj()
			gomega.Expect(k8sClient.Create(ctx, resourceFlavor)).Should(gomega.Succeed())
			defer func() {
				var rf kueue.ResourceFlavor
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(resourceFlavor), &rf)).To(gomega.Succeed())
				controllerutil.RemoveFinalizer(&rf, kueue.ResourceInUseFinalizerName)
				gomega.Expect(k8sClient.Update(ctx, &rf)).Should(gomega.Succeed())
				framework.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, resourceFlavor, true)
			}()

			var created kueue.ResourceFlavor
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(resourceFlavor), &created)).To(gomega.Succeed())
			created.Taints = []corev1.Taint{{
				Key:    "foo",
				Value:  "bar",
				Effect: "Invalid",
			}}

			ginkgo.By("Updating the resourceFlavor with invalid labels")
			err := k8sClient.Update(ctx, &created)
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(errors.IsForbidden(err)).To(gomega.BeTrue(), "error: %v", err)
		})
	})
})
