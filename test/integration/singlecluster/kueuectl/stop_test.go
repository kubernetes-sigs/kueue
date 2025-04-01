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

package kueuectl

import (
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
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

var _ = ginkgo.Describe("Kueuectl Stop", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns *corev1.Namespace
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "ns-")
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.When("Stopping the Workload", func() {
		ginkgo.It("Should stop the Workload", func() {
			wl := testing.MakeWorkload("wl", ns.Name).Active(true).Obj()
			ginkgo.By("Create a Workload")
			util.MustCreate(ctx, k8sClient, wl)

			createdWorkload := &v1beta1.Workload{}

			ginkgo.By("Get the created Workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: wl.Name, Namespace: ns.Name}, createdWorkload)).To(gomega.Succeed())
					g.Expect(workload.IsActive(wl)).Should(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Stop the created Workload", func() {
				streams, _, output, _ := genericiooptions.NewTestIOStreams()
				configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
				kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{ConfigFlags: configFlags, IOStreams: streams, Clock: testingclock.NewFakeClock(time.Now())})

				kueuectl.SetArgs([]string{"stop", "workload", wl.Name, "--namespace", ns.Name})
				err := kueuectl.Execute()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
			})

			ginkgo.By("Check that the Workload successfully stopped", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: wl.Name, Namespace: ns.Name}, wl)).To(gomega.Succeed())
					g.Expect(workload.IsActive(wl)).Should(gomega.BeFalse())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("Stopping a LocalQueue", func() {
		ginkgo.DescribeTable("Should stop a LocalQueue",
			func(name string, stopCmdArgs []string, wantStopPolicy v1beta1.StopPolicy) {
				lq := testing.MakeLocalQueue(name, ns.Name).Obj()

				ginkgo.By("Create a LocalQueue", func() {
					util.MustCreate(ctx, k8sClient, lq)
				})

				createdLocalQueue := &v1beta1.LocalQueue{}
				ginkgo.By("Get created LocalQueue", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lq), createdLocalQueue)).To(gomega.Succeed())
						g.Expect(ptr.Deref(createdLocalQueue.Spec.StopPolicy, v1beta1.None)).Should(gomega.Equal(v1beta1.None))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("Stop created LocalQueue", func() {
					streams, _, output, _ := genericiooptions.NewTestIOStreams()
					configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
					kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{ConfigFlags: configFlags, IOStreams: streams})

					kueuectl.SetArgs(append([]string{"stop", "localqueue", createdLocalQueue.Name, "--namespace", ns.Name}, stopCmdArgs...))
					err := kueuectl.Execute()
					gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
				})

				ginkgo.By("Check that the LocalQueue is successfully stopped", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(createdLocalQueue), createdLocalQueue)).To(gomega.Succeed())
						g.Expect(ptr.Deref(createdLocalQueue.Spec.StopPolicy, v1beta1.None)).Should(gomega.Equal(wantStopPolicy))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})
			},
			ginkgo.Entry("Stop a LocalQueue and drain workloads",
				"lq-1",
				[]string{},
				v1beta1.HoldAndDrain,
			),
			ginkgo.Entry("Stop a LocalQueue and let the admitted workloads finish",
				"lq-2",
				[]string{"--keep-already-running"},
				v1beta1.Hold,
			),
		)
	})

	ginkgo.When("Stopping a ClusterQueue", func() {
		ginkgo.DescribeTable("Should stop a ClusterQueue",
			func(cq *v1beta1.ClusterQueue, stopCmdArgs []string, wantStopPolicy v1beta1.StopPolicy) {
				ginkgo.By("Create a ClusterQueue", func() {
					util.MustCreate(ctx, k8sClient, cq)
				})

				ginkgo.DeferCleanup(func() {
					util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
				})

				createdClusterQueue := &v1beta1.ClusterQueue{}
				ginkgo.By("Get created ClusterQueue", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), createdClusterQueue)).To(gomega.Succeed())
						g.Expect(ptr.Deref(createdClusterQueue.Spec.StopPolicy, v1beta1.None)).Should(gomega.Equal(v1beta1.None))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("Stop created ClusterQueue", func() {
					streams, _, output, _ := genericiooptions.NewTestIOStreams()
					configFlags := CreateConfigFlagsWithRestConfig(cfg, streams)
					kueuectl := app.NewKueuectlCmd(app.KueuectlOptions{ConfigFlags: configFlags, IOStreams: streams, Clock: testingclock.NewFakeClock(time.Now())})

					kueuectl.SetArgs(append([]string{"stop", "clusterqueue", createdClusterQueue.Name}, stopCmdArgs...))
					err := kueuectl.Execute()
					gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
				})

				ginkgo.By("Check that the ClusterQueue is successfully stopped", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(createdClusterQueue), createdClusterQueue)).To(gomega.Succeed())
						g.Expect(ptr.Deref(createdClusterQueue.Spec.StopPolicy, v1beta1.None)).Should(gomega.Equal(wantStopPolicy))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})
			},
			ginkgo.Entry("Stop a ClusterQueue and drain workloads",
				testing.MakeClusterQueue("cq-1").Obj(),
				[]string{},
				v1beta1.HoldAndDrain,
			),
			ginkgo.Entry("Stop a ClusterQueue and let the admitted workloads finish",
				testing.MakeClusterQueue("cq-2").Obj(),
				[]string{"--keep-already-running"},
				v1beta1.Hold,
			),
		)
	})
})
