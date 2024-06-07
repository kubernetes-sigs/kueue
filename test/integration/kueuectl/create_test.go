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
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	testingclock "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/cmd/kueuectl/app"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Kueuectl Create", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns *corev1.Namespace
		cq *v1beta1.ClusterQueue
	)

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "ns-"}}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

		cq = testing.MakeClusterQueue("cq").Obj()
		gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, cq, true)
	})

	ginkgo.When("Creating a LocalQueue", func() {
		ginkgo.It("Should create a local queue", func() {
			lqName := "lq"

			ginkgo.By("Create a local queue with full flags", func() {
				streams, _, output, _ := genericiooptions.NewTestIOStreams()
				configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
				kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{ConfigFlags: configFlags, IOStreams: streams, Clock: testingclock.NewFakeClock(time.Now())})
				kueuectl.SetOut(output)
				kueuectl.SetErr(output)

				kueuectl.SetArgs([]string{"create", "localqueue", lqName, "--clusterqueue", cq.Name, "--namespace", ns.Name})
				err := kueuectl.Execute()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
			})

			ginkgo.By("Check that the local queue successfully created", func() {
				var createdQueue v1beta1.LocalQueue
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: lqName, Namespace: ns.Name}, &createdQueue)).To(gomega.Succeed())
					g.Expect(createdQueue.Name).Should(gomega.Equal(lqName))
					g.Expect(createdQueue.Spec.ClusterQueue).Should(gomega.Equal(v1beta1.ClusterQueueReference(cq.Name)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should create a local queue with unknown cluster queue", func() {
			lqName := "lq"
			cqName := "cq-unknown"

			ginkgo.By("Create a local queue with unknown cluster queue", func() {
				streams, _, output, _ := genericiooptions.NewTestIOStreams()
				configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
				kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{ConfigFlags: configFlags, IOStreams: streams})
				kueuectl.SetOut(output)
				kueuectl.SetErr(output)

				kueuectl.SetArgs([]string{"create", "localqueue", lqName, "--clusterqueue", cqName, "--namespace", ns.Name, "--ignore-unknown-cq"})
				err := kueuectl.Execute()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
			})

			ginkgo.By("Check that the local queue successfully created", func() {
				var createdQueue v1beta1.LocalQueue
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: lqName, Namespace: ns.Name}, &createdQueue)).To(gomega.Succeed())
					g.Expect(createdQueue.Name).Should(gomega.Equal(lqName))
					g.Expect(createdQueue.Spec.ClusterQueue).Should(gomega.Equal(v1beta1.ClusterQueueReference(cqName)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should create a local queue with default namespace", func() {
			lqName := "lq"

			ginkgo.By("Create a local queue with default namespace", func() {
				streams, _, output, _ := genericiooptions.NewTestIOStreams()
				configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
				// Setting default namespace
				configFlags.Namespace = ptr.To(ns.Name)
				kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{ConfigFlags: configFlags, IOStreams: streams})
				kueuectl.SetOut(output)
				kueuectl.SetErr(output)

				kueuectl.SetArgs([]string{"create", "lq", lqName, "-c", cq.Name})
				err := kueuectl.Execute()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
			})

			ginkgo.By("Check that the local queue successfully created", func() {
				var createdQueue v1beta1.LocalQueue
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: lqName, Namespace: ns.Name}, &createdQueue)).To(gomega.Succeed())
					g.Expect(createdQueue.Name).Should(gomega.Equal(lqName))
					g.Expect(createdQueue.Spec.ClusterQueue).Should(gomega.Equal(v1beta1.ClusterQueueReference(cq.Name)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})
