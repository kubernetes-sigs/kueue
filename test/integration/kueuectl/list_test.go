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

package kueuectl

import (
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericiooptions"

	"sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/cmd/kueuectl/app"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Kueuectl List", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns  *corev1.Namespace
		ns1 *corev1.Namespace
		cq1 *v1beta1.ClusterQueue
		lq1 *v1beta1.LocalQueue
		lq2 *v1beta1.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		ns1 = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "ns-"}}
		gomega.Expect(k8sClient.Create(ctx, ns1)).To(gomega.Succeed())

		cq1 = testing.MakeClusterQueue("cq1").Obj()
		gomega.Expect(k8sClient.Create(ctx, cq1)).To(gomega.Succeed())

		lq1 = testing.MakeLocalQueue("lq1", ns1.Name).ClusterQueue(cq1.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, lq1)).To(gomega.Succeed())

		lq2 = testing.MakeLocalQueue("lq2", ns1.Name).ClusterQueue(cq1.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, lq2)).To(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns1)).To(gomega.Succeed())
		util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, cq1, true)
	})

	ginkgo.When("List LocalQueue", func() {
		// Simple client set that are using on unit tests not allow to filter by field selector.
		ginkgo.It("Should print local queue filtered by field selector", func() {
			streams, _, output, errOutput := genericiooptions.NewTestIOStreams()
			configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
			kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{ConfigFlags: configFlags, IOStreams: streams})

			kueuectl.SetArgs([]string{"list", "localqueue", "--field-selector",
				fmt.Sprintf("metadata.name=%s", lq1.Name), "--namespace", ns1.Name})
			err := kueuectl.Execute()

			gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
			gomega.Expect(errOutput.String()).Should(gomega.BeEmpty())
			gomega.Expect(output.String()).ShouldNot(gomega.ContainSubstring(ns1.Name))
			gomega.Expect(output.String()).Should(gomega.ContainSubstring(cq1.Name))
			gomega.Expect(output.String()).Should(gomega.ContainSubstring(lq1.Name))
			gomega.Expect(output.String()).ShouldNot(gomega.ContainSubstring(lq2.Name))
		})
	})
})
