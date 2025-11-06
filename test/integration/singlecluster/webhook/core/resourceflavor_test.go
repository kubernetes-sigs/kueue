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
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"github.com/onsi/gomega/types"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util"
)

const (
	nodeSelectorMaxProperties = 8
	taintsMaxItems            = 8
)

var _ = ginkgo.Describe("ResourceFlavor Webhook", ginkgo.Ordered, func() {
	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, func(ctx context.Context, mgr manager.Manager) {
			managerSetup(ctx, mgr)
		})
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})
	var ns *corev1.Namespace

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-")
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.When("Creating a ResourceFlavor", func() {
		ginkgo.It("Should be valid", func() {
			resourceFlavor := utiltestingapi.MakeResourceFlavor("resource-flavor").NodeLabel("foo", "bar").
				Taint(corev1.Taint{
					Key:    "spot",
					Value:  "true",
					Effect: corev1.TaintEffectNoSchedule,
				}).Obj()
			util.MustCreate(ctx, k8sClient, resourceFlavor)
			defer func() {
				var rf kueue.ResourceFlavor
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(resourceFlavor), &rf)).Should(gomega.Succeed())
				controllerutil.RemoveFinalizer(&rf, kueue.ResourceInUseFinalizerName)
				gomega.Expect(k8sClient.Update(ctx, &rf)).Should(gomega.Succeed())
				util.ExpectObjectToBeDeleted(ctx, k8sClient, resourceFlavor, true)
			}()
		})
		ginkgo.It("Should have a finalizer", func() {
			ginkgo.By("Creating a new empty resourceFlavor")
			resourceFlavor := utiltestingapi.MakeResourceFlavor("resource-flavor").Obj()
			util.MustCreate(ctx, k8sClient, resourceFlavor)
			defer func() {
				var rf kueue.ResourceFlavor
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(resourceFlavor), &rf)).Should(gomega.Succeed())
				controllerutil.RemoveFinalizer(&rf, kueue.ResourceInUseFinalizerName)
				gomega.Expect(k8sClient.Update(ctx, &rf)).Should(gomega.Succeed())
				util.ExpectObjectToBeDeleted(ctx, k8sClient, resourceFlavor, true)
			}()

			var created kueue.ResourceFlavor
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(resourceFlavor), &created)).Should(gomega.Succeed())
			gomega.Expect(created.GetFinalizers()).Should(gomega.Equal([]string{kueue.ResourceInUseFinalizerName}))
		})
	})

	ginkgo.DescribeTable("invalid number of properties", func(taintsCount int, nodeSelectorCount int, isInvalid bool) {
		rf := utiltestingapi.MakeResourceFlavor("resource-flavor")
		for i := range taintsCount {
			rf = rf.Taint(corev1.Taint{
				Key:    fmt.Sprintf("t%d", i),
				Effect: corev1.TaintEffectNoExecute,
			})
		}
		for i := range nodeSelectorCount {
			rf = rf.NodeLabel(fmt.Sprintf("l%d", i), "")
		}
		resourceFlavor := rf.Obj()
		err := k8sClient.Create(ctx, resourceFlavor)
		if isInvalid {
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(err).Should(utiltesting.BeInvalidError())
		} else {
			gomega.Expect(err).To(gomega.Succeed())
			defer func() {
				var rf kueue.ResourceFlavor
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(resourceFlavor), &rf)).To(gomega.Succeed())
				controllerutil.RemoveFinalizer(&rf, kueue.ResourceInUseFinalizerName)
				gomega.Expect(k8sClient.Update(ctx, &rf)).Should(gomega.Succeed())
				util.ExpectObjectToBeDeleted(ctx, k8sClient, resourceFlavor, true)
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
			resourceFlavor := utiltestingapi.MakeResourceFlavor("resource-flavor").Obj()
			util.MustCreate(ctx, k8sClient, resourceFlavor)
			defer func() {
				var rf kueue.ResourceFlavor
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(resourceFlavor), &rf)).To(gomega.Succeed())
				controllerutil.RemoveFinalizer(&rf, kueue.ResourceInUseFinalizerName)
				gomega.Expect(k8sClient.Update(ctx, &rf)).Should(gomega.Succeed())
				util.ExpectObjectToBeDeleted(ctx, k8sClient, resourceFlavor, true)
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
			gomega.Expect(err).Should(utiltesting.BeInvalidError())
		})
	})

	ginkgo.DescribeTable("Validate resourceFlavor on creation", func(rf *kueue.ResourceFlavor, matcher types.GomegaMatcher) {
		err := k8sClient.Create(ctx, rf)
		if err == nil {
			defer func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
			}()
		}
		gomega.Expect(err).Should(matcher)
	},
		ginkgo.Entry("Should fail to create with invalid taints",
			utiltestingapi.MakeResourceFlavor("resource-flavor").
				Taint(corev1.Taint{
					Key: "skdajf",
				}).
				Taint(corev1.Taint{
					Key:    "@foo",
					Value:  "bar",
					Effect: corev1.TaintEffectNoSchedule,
				}).Obj(),
			utiltesting.BeInvalidError()),
		ginkgo.Entry("Should fail to create with invalid label name",
			utiltestingapi.MakeResourceFlavor("resource-flavor").NodeLabel("@abc", "foo").Obj(),
			utiltesting.BeForbiddenError()),
		ginkgo.Entry("Should fail to create with invalid tolerations",
			utiltestingapi.MakeResourceFlavor("resource-flavor").
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
					Effect:   "not-valid",
				}).
				Toleration(corev1.Toleration{
					Key:      "abc",
					Operator: corev1.TolerationOpEqual,
					Value:    "v",
					Effect:   corev1.TaintEffectNoSchedule,
				}).
				Obj(),
			utiltesting.BeInvalidError()),
	)
})
