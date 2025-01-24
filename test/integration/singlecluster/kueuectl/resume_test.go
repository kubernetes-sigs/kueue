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
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/cmd/kueuectl/app"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Kueuectl Resume", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns *corev1.Namespace
	)

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "ns-"}}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.When("Resuming the Workload", func() {
		ginkgo.It("Should resume the Workload", func() {
			wl := testing.MakeWorkload("wl", ns.Name).Active(false).Obj()
			ginkgo.By("Create a Workload")
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			createdWorkload := &v1beta1.Workload{}

			ginkgo.By("Get the created Workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: wl.Name, Namespace: ns.Name}, createdWorkload)).To(gomega.Succeed())
					g.Expect(workload.IsActive(wl)).Should(gomega.BeFalse())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Resume the created Workload", func() {
				streams, _, output, _ := genericiooptions.NewTestIOStreams()
				configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
				kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{ConfigFlags: configFlags, IOStreams: streams, Clock: testingclock.NewFakeClock(time.Now())})

				kueuectl.SetArgs([]string{"resume", "workload", wl.Name, "--namespace", ns.Name})
				err := kueuectl.Execute()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
			})

			ginkgo.By("Check that the Workload successfully resumed", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: wl.Name, Namespace: ns.Name}, wl)).To(gomega.Succeed())
					g.Expect(workload.IsActive(wl)).Should(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("Resuming a LocalQueue", func() {
		ginkgo.DescribeTable("Should resume a LocalQueue",
			func(name string, wantInitialStopPolicy v1beta1.StopPolicy) {
				lq := testing.MakeLocalQueue(name, ns.Name).StopPolicy(wantInitialStopPolicy).Obj()

				ginkgo.By("Create a LocalQueue", func() {
					gomega.Expect(k8sClient.Create(ctx, lq)).To(gomega.Succeed())
				})

				createdLocalQueue := &v1beta1.LocalQueue{}
				ginkgo.By("Get created LocalQueue", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lq), createdLocalQueue)).To(gomega.Succeed())
						g.Expect(ptr.Deref(createdLocalQueue.Spec.StopPolicy, v1beta1.None)).Should(gomega.Equal(wantInitialStopPolicy))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("Resume created LocalQueue", func() {
					streams, _, output, _ := genericiooptions.NewTestIOStreams()
					configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
					kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{ConfigFlags: configFlags, IOStreams: streams})

					kueuectl.SetArgs([]string{"resume", "localqueue", createdLocalQueue.Name, "--namespace", ns.Name})
					err := kueuectl.Execute()
					gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
				})

				ginkgo.By("Check that the LocalQueue is successfully resumed", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(createdLocalQueue), createdLocalQueue)).To(gomega.Succeed())
						g.Expect(ptr.Deref(createdLocalQueue.Spec.StopPolicy, v1beta1.None)).Should(gomega.Equal(v1beta1.None))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})
			},
			ginkgo.Entry("HoldAndDrain",
				"lq-1",
				v1beta1.HoldAndDrain,
			),
			ginkgo.Entry("Hold",
				"lq-2",
				v1beta1.Hold,
			),
		)
	})

	ginkgo.When("Resuming a ClusterQueue", func() {
		ginkgo.DescribeTable("Should resume a ClusterQueue",
			func(cq *v1beta1.ClusterQueue, wantInitialStopPolicy v1beta1.StopPolicy) {
				ginkgo.By("Create a ClusterQueue", func() {
					gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())
				})

				ginkgo.DeferCleanup(func() {
					util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
				})

				createdClusterQueue := &v1beta1.ClusterQueue{}
				ginkgo.By("Get created ClusterQueue", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), createdClusterQueue)).To(gomega.Succeed())
						g.Expect(ptr.Deref(createdClusterQueue.Spec.StopPolicy, v1beta1.None)).Should(gomega.Equal(wantInitialStopPolicy))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("Resume created ClusterQueue", func() {
					streams, _, output, _ := genericiooptions.NewTestIOStreams()
					configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
					kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{ConfigFlags: configFlags, IOStreams: streams, Clock: testingclock.NewFakeClock(time.Now())})

					kueuectl.SetArgs([]string{"resume", "clusterqueue", createdClusterQueue.Name})
					err := kueuectl.Execute()
					gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
				})

				ginkgo.By("Check that the ClusterQueue is successfully resumed", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(createdClusterQueue), createdClusterQueue)).To(gomega.Succeed())
						g.Expect(ptr.Deref(createdClusterQueue.Spec.StopPolicy, v1beta1.None)).Should(gomega.Equal(v1beta1.None))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})
			},
			ginkgo.Entry("HoldAndDrain",
				testing.MakeClusterQueue("cq-1").StopPolicy(v1beta1.HoldAndDrain).Obj(),
				v1beta1.HoldAndDrain,
			),
			ginkgo.Entry("Hold",
				testing.MakeClusterQueue("cq-2").StopPolicy(v1beta1.Hold).Obj(),
				v1beta1.Hold,
			),
		)
	})
})
