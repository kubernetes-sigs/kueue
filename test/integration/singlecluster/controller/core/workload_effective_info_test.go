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
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/metrics"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Effective resources in queued Info", ginkgo.Label("controller:workload", "area:core"), func() {
	var ns *corev1.Namespace
	var cq *kueue.ClusterQueue
	var flavor *kueue.ResourceFlavor
	var runtimeClass *nodev1.RuntimeClass

	ginkgo.BeforeEach(func() {
		fwk.StartManager(ctx, cfg, managerSetup)
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "effective-info-")
		flavor = utiltestingapi.MakeResourceFlavor("effective-info").Obj()
		util.MustCreate(ctx, k8sClient, flavor)
		cq = utiltestingapi.MakeClusterQueue("effective-info").ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).Resource(corev1.ResourceCPU, "0").Obj()).Obj()
		util.MustCreate(ctx, k8sClient, cq)
		util.ExpectClusterQueuesToBeActive(ctx, k8sClient, cq)
		lq := utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
		util.MustCreate(ctx, k8sClient, lq)
		util.ExpectLocalQueuesToBeActive(ctx, k8sClient, lq)
		runtimeClass = utiltesting.MakeRuntimeClass("effective-info", "handler").PodOverhead(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}).Obj()
		util.MustCreate(ctx, k8sClient, runtimeClass)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, runtimeClass, true)
		fwk.StopManager(ctx)
		metrics.InitMetricVectors(nil)
	})

	ginkgo.It("refreshes defaults through informer events while retaining the raw Workload", func() {
		lr := utiltesting.MakeLimitRange("defaults", ns.Name).WithValue("DefaultRequest", corev1.ResourceCPU, "2").Obj()
		util.MustCreate(ctx, k8sClient, lr)
		wl := utiltestingapi.MakeWorkload("pending", ns.Name).Queue("queue").PodSets(*utiltestingapi.MakePodSet("main", 2).RuntimeClass(runtimeClass.Name).Obj()).Obj()
		util.MustCreate(ctx, k8sClient, wl)
		var initialHash workload.EquivalenceHash
		expectCPU := func(cpu int64, changedHash bool) {
			gomega.Eventually(func(g gomega.Gomega) {
				infos := qManager.PendingWorkloadsInfo(kueue.ClusterQueueReference(cq.Name))
				g.Expect(infos).To(gomega.HaveLen(1))
				g.Expect(infos[0].TotalRequests[0].Requests.ResourceValue(corev1.ResourceCPU)).To(gomega.Equal(cpu))
				g.Expect(infos[0].Obj.Spec).To(gomega.Equal(wl.Spec))
				if changedHash {
					g.Expect(infos[0].SchedulingHash).NotTo(gomega.Equal(initialHash))
				} else {
					initialHash = infos[0].SchedulingHash
				}
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		}
		ginkgo.By("accounting two Pods with 2 CPU default requests and 1 CPU overhead", func() { expectCPU(6000, false) })
		ginkgo.By("refreshing pending Info after a LimitRange update", func() {
			lr.Spec.Limits[0].DefaultRequest[corev1.ResourceCPU] = resource.MustParse("3")
			gomega.Expect(k8sClient.Update(ctx, lr)).To(gomega.Succeed())
			expectCPU(8000, true)
		})
		ginkgo.By("removing deleted LimitRange defaults", func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, lr, true)
			expectCPU(2000, true)
		})
		ginkgo.By("refreshing overhead after a RuntimeClass deletion", func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, runtimeClass, true)
			expectCPU(0, true)
		})
	})
})
