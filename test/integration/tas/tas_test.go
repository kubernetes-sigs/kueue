/*
Copyright 2024 The Kubernetes Authors.

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
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/testing"
)

var _ = ginkgo.Describe("Topology Aware Scheduling", ginkgo.Ordered, func() {
	var (
		ns *corev1.Namespace
	)

	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup)
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		_ = features.SetEnable(features.TopologyAwareScheduling, true)
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "tas-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		_ = features.SetEnable(features.TopologyAwareScheduling, false)
	})

	ginkgo.When("Negative scenarios for ResourceFlavor configuration", func() {
		ginkgo.It("should not allow to create ResourceFlavor with invalid topology name", func() {
			tasFlavor := testing.MakeResourceFlavor("tas-flavor").
				NodeLabel("node-group", "tas").
				TopologyName("invalid topology name").Obj()
			gomega.Expect(k8sClient.Create(ctx, tasFlavor)).Should(gomega.HaveOccurred())
		})

		ginkgo.It("should not allow to create ResourceFlavor without any node labels", func() {
			tasFlavor := testing.MakeResourceFlavor("tas-flavor").TopologyName("default").Obj()
			gomega.Expect(k8sClient.Create(ctx, tasFlavor)).Should(gomega.HaveOccurred())
		})
	})
})
