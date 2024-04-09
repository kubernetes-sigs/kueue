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
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/util"
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
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.When("Creating a ResourceFlavor", func() {
		ginkgo.It("Should be valid", func() {
			resourceFlavor := testing.MakeResourceFlavor("resource-flavor").Label("foo", "bar").
				Taint(corev1.Taint{
					Key:    "spot",
					Value:  "true",
					Effect: corev1.TaintEffectNoSchedule,
				}).Obj()
			gomega.Expect(k8sClient.Create(ctx, resourceFlavor)).Should(gomega.Succeed())
			defer func() {
				var rf kueue.ResourceFlavor
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(resourceFlavor), &rf)).Should(gomega.Succeed())
				controllerutil.RemoveFinalizer(&rf, kueue.ResourceInUseFinalizerName)
				gomega.Expect(k8sClient.Update(ctx, &rf)).Should(gomega.Succeed())
				util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, resourceFlavor, true)
			}()
		})
		ginkgo.It("Should have a finalizer", func() {
			ginkgo.By("Creating a new empty resourceFlavor")
			resourceFlavor := testing.MakeResourceFlavor("resource-flavor").Obj()
			gomega.Expect(k8sClient.Create(ctx, resourceFlavor)).Should(gomega.Succeed())
			defer func() {
				var rf kueue.ResourceFlavor
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(resourceFlavor), &rf)).Should(gomega.Succeed())
				controllerutil.RemoveFinalizer(&rf, kueue.ResourceInUseFinalizerName)
				gomega.Expect(k8sClient.Update(ctx, &rf)).Should(gomega.Succeed())
				util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, resourceFlavor, true)
			}()

			var created kueue.ResourceFlavor
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(resourceFlavor), &created)).Should(gomega.Succeed())
			gomega.Expect(created.GetFinalizers()).Should(gomega.Equal([]string{kueue.ResourceInUseFinalizerName}))
		})
	})

	ginkgo.When("Creating a ResourceFlavor with invalid values", func() {
		ginkgo.It("Should fail to create with invalid taints", func() {
			resourceFlavor := testing.MakeResourceFlavor("resource-flavor").
				Taint(corev1.Taint{
					Key: "skdajf",
				}).
				Taint(corev1.Taint{
					Key:    "@foo",
					Value:  "bar",
					Effect: corev1.TaintEffectNoSchedule,
				}).Obj()
			err := k8sClient.Create(ctx, resourceFlavor)
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(err).Should(testing.BeAPIError(testing.InvalidError))
		})
		ginkgo.It("Should fail to create with invalid label name", func() {
			resourceFlavor := testing.MakeResourceFlavor("resource-flavor").Label("@abc", "foo").Obj()
			err := k8sClient.Create(ctx, resourceFlavor)
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(errors.IsForbidden(err)).To(gomega.BeTrue(), "error: %v", err)
		})
		ginkgo.It("Should fail to create with invalid tolerations", func() {
			resourceFlavor := testing.MakeResourceFlavor("resource-flavor").
				Toleration(corev1.Toleration{
					Key:      "@abc",
					Operator: corev1.TolerationOpEqual,
					Value:    "v",
					Effect:   corev1.TaintEffectNoSchedule,
				}).
				Toleration(corev1.Toleration{
					Key:      "abc",
					Operator: corev1.TolerationOpExists,
					Value:    "v",
					Effect:   corev1.TaintEffectNoSchedule,
				}).
				Toleration(corev1.Toleration{
					Key:      "abc",
					Operator: corev1.TolerationOpEqual,
					Value:    "v",
					Effect:   corev1.TaintEffect("not-valid"),
				}).
				Toleration(corev1.Toleration{
					Key:      "abc",
					Operator: corev1.TolerationOpEqual,
					Value:    "v",
					Effect:   corev1.TaintEffectNoSchedule,
				}).
				Obj()
			err := k8sClient.Create(ctx, resourceFlavor)
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(err).Should(testing.BeAPIError(testing.InvalidError))
		})
	})

	ginkgo.DescribeTable("invalid number of properties", func(taintsCount int, nodeSelectorCount int, isInvalid bool) {
		rf := testing.MakeResourceFlavor("resource-flavor")
		for i := 0; i < taintsCount; i++ {
			rf = rf.Taint(corev1.Taint{
				Key:    fmt.Sprintf("t%d", i),
				Effect: corev1.TaintEffectNoExecute,
			})
		}
		for i := 0; i < nodeSelectorCount; i++ {
			rf = rf.Label(fmt.Sprintf("l%d", i), "")
		}
		resourceFlavor := rf.Obj()
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
				util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, resourceFlavor, true)
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
				util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, resourceFlavor, true)
			}()

			var created kueue.ResourceFlavor
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(resourceFlavor), &created)).To(gomega.Succeed())
			created.Spec.NodeTaints = []corev1.Taint{{
				Key:    "foo",
				Value:  "bar",
				Effect: "Invalid",
			}}

			ginkgo.By("Updating the resourceFlavor with invalid labels")
			err := k8sClient.Update(ctx, &created)
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(err).Should(testing.BeAPIError(testing.InvalidError))
		})
	})
})
