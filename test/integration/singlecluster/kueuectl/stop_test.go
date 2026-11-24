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
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("Kueuectl Stop", func() {
	var (
		ns *corev1.Namespace
	)

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "ns-")
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.When("Stopping the Workload", func() {
		ginkgo.It("Should stop the Workload", func() {
			wl := utiltestingapi.MakeWorkload("wl", ns.Name).Active(true).Obj()
			ginkgo.By("Create a Workload")
			behavioral.MustCreate(ctx, k8sClient, wl)

			createdWorkload := &kueue.Workload{}

			ginkgo.By("Get the created Workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: wl.Name, Namespace: ns.Name}, createdWorkload)).To(gomega.Succeed())
					g.Expect(workload.IsActive(wl)).Should(gomega.BeTrue())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Stop the created Workload", func() {
				executeKueuectl(cfg, []string{"stop", "workload", wl.Name, "--namespace", ns.Name})
			})

			ginkgo.By("Check that the Workload successfully stopped", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: wl.Name, Namespace: ns.Name}, wl)).To(gomega.Succeed())
					g.Expect(workload.IsActive(wl)).Should(gomega.BeFalse())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("Stopping a LocalQueue", func() {
		ginkgo.It("Should stop a LocalQueue", func() {
			ginkgo.By("Stop a LocalQueue and drain workloads", func() {
				lq := utiltestingapi.MakeLocalQueue("lq-1", ns.Name).Obj()
				behavioral.MustCreate(ctx, k8sClient, lq)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lq), lq)).To(gomega.Succeed())
					g.Expect(ptr.Deref(lq.Spec.StopPolicy, kueue.None)).Should(gomega.Equal(kueue.None))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

				executeKueuectl(cfg, []string{"stop", "localqueue", lq.Name, "--namespace", ns.Name})
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lq), lq)).To(gomega.Succeed())
					g.Expect(ptr.Deref(lq.Spec.StopPolicy, kueue.None)).Should(gomega.Equal(kueue.HoldAndDrain))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Stop a LocalQueue and let the admitted workloads finish", func() {
				lq := utiltestingapi.MakeLocalQueue("lq-2", ns.Name).Obj()
				behavioral.MustCreate(ctx, k8sClient, lq)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lq), lq)).To(gomega.Succeed())
					g.Expect(ptr.Deref(lq.Spec.StopPolicy, kueue.None)).Should(gomega.Equal(kueue.None))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

				executeKueuectl(cfg, []string{"stop", "localqueue", lq.Name, "--namespace", ns.Name, "--keep-already-running"})
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lq), lq)).To(gomega.Succeed())
					g.Expect(ptr.Deref(lq.Spec.StopPolicy, kueue.None)).Should(gomega.Equal(kueue.Hold))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should not stop a LocalQueue with --dry-run", func() {
			lq := utiltestingapi.MakeLocalQueue("lq-dry-run", ns.Name).Obj()
			behavioral.MustCreate(ctx, k8sClient, lq)
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lq), lq)).To(gomega.Succeed())
				g.Expect(ptr.Deref(lq.Spec.StopPolicy, kueue.None)).Should(gomega.Equal(kueue.None))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			ginkgo.By("Stop with --dry-run=client", func() {
				executeKueuectl(cfg, []string{"stop", "localqueue", lq.Name, "--namespace", ns.Name, "--dry-run", "client"})
				gomega.Consistently(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lq), lq)).To(gomega.Succeed())
					g.Expect(ptr.Deref(lq.Spec.StopPolicy, kueue.None)).Should(gomega.Equal(kueue.None))
				}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
			})

			ginkgo.By("Stop with --dry-run=server", func() {
				executeKueuectl(cfg, []string{"stop", "localqueue", lq.Name, "--namespace", ns.Name, "--dry-run", "server"})
				gomega.Consistently(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lq), lq)).To(gomega.Succeed())
					g.Expect(ptr.Deref(lq.Spec.StopPolicy, kueue.None)).Should(gomega.Equal(kueue.None))
				}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("Stopping a ClusterQueue", func() {
		ginkgo.It("Should stop a ClusterQueue", func() {
			ginkgo.By("Stop a ClusterQueue and drain workloads", func() {
				cq := utiltestingapi.MakeClusterQueue("cq-1").Obj()
				behavioral.MustCreate(ctx, k8sClient, cq)
				ginkgo.DeferCleanup(func() {
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
				})
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).To(gomega.Succeed())
					g.Expect(ptr.Deref(cq.Spec.StopPolicy, kueue.None)).Should(gomega.Equal(kueue.None))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

				executeKueuectl(cfg, []string{"stop", "clusterqueue", cq.Name})
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).To(gomega.Succeed())
					g.Expect(ptr.Deref(cq.Spec.StopPolicy, kueue.None)).Should(gomega.Equal(kueue.HoldAndDrain))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Stop a ClusterQueue and let the admitted workloads finish", func() {
				cq := utiltestingapi.MakeClusterQueue("cq-2").Obj()
				behavioral.MustCreate(ctx, k8sClient, cq)
				ginkgo.DeferCleanup(func() {
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
				})
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).To(gomega.Succeed())
					g.Expect(ptr.Deref(cq.Spec.StopPolicy, kueue.None)).Should(gomega.Equal(kueue.None))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

				executeKueuectl(cfg, []string{"stop", "clusterqueue", cq.Name, "--keep-already-running"})
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).To(gomega.Succeed())
					g.Expect(ptr.Deref(cq.Spec.StopPolicy, kueue.None)).Should(gomega.Equal(kueue.Hold))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should not stop a ClusterQueue with --dry-run", func() {
			cq := utiltestingapi.MakeClusterQueue("cq-dry-run").Obj()
			behavioral.MustCreate(ctx, k8sClient, cq)
			ginkgo.DeferCleanup(func() {
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			})
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).To(gomega.Succeed())
				g.Expect(ptr.Deref(cq.Spec.StopPolicy, kueue.None)).Should(gomega.Equal(kueue.None))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			ginkgo.By("Stop with --dry-run=client", func() {
				executeKueuectl(cfg, []string{"stop", "clusterqueue", cq.Name, "--dry-run", "client"})
				gomega.Consistently(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).To(gomega.Succeed())
					g.Expect(ptr.Deref(cq.Spec.StopPolicy, kueue.None)).Should(gomega.Equal(kueue.None))
				}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
			})

			ginkgo.By("Stop with --dry-run=server", func() {
				executeKueuectl(cfg, []string{"stop", "clusterqueue", cq.Name, "--dry-run", "server"})
				gomega.Consistently(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).To(gomega.Succeed())
					g.Expect(ptr.Deref(cq.Spec.StopPolicy, kueue.None)).Should(gomega.Equal(kueue.None))
				}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
			})
		})
	})
})
