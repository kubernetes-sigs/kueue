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

package importer

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	importerpod "sigs.k8s.io/kueue/cmd/importer/pod"
	importerutil "sigs.k8s.io/kueue/cmd/importer/util"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	"sigs.k8s.io/kueue/pkg/metrics"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Importer", func() {
	var (
		ns     *corev1.Namespace
		flavor *kueue.ResourceFlavor
		cq     *kueue.ClusterQueue
		lq     *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "import-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

		flavor = utiltesting.MakeResourceFlavor("f1").Obj()
		gomega.Expect(k8sClient.Create(ctx, flavor)).To(gomega.Succeed())

		cq = utiltesting.MakeClusterQueue("cq1").
			ResourceGroup(
				*utiltesting.MakeFlavorQuotas("f1").Resource(corev1.ResourceCPU, "4").Obj(),
			).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())

		lq = utiltesting.MakeLocalQueue("lq1", ns.Name).ClusterQueue("cq1").Obj()
		gomega.Expect(k8sClient.Create(ctx, lq)).To(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
	})

	ginkgo.When("Kueue is started after import", func() {
		ginkgo.It("Should keep the imported pods admitted", func() {
			pod1 := utiltestingpod.MakePod("pod1", ns.Name).
				Label("src.lbl", "src-val").
				Request(corev1.ResourceCPU, "2").
				Obj()
			pod2 := utiltestingpod.MakePod("pod2", ns.Name).
				Label("src.lbl", "src-val").
				Request(corev1.ResourceCPU, "2").
				Obj()

			ginkgo.By("Creating the initial pods", func() {
				gomega.Expect(k8sClient.Create(ctx, pod1)).To(gomega.Succeed())
				gomega.Expect(k8sClient.Create(ctx, pod2)).To(gomega.Succeed())
			})

			ginkgo.By("Running the import", func() {
				mapping, err := importerutil.LoadImportCache(ctx, k8sClient, []string{ns.Name}, importerutil.MappingRulesForLabel("src.lbl", map[string]string{"src-val": "lq1"}), nil)
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				gomega.Expect(mapping).ToNot(gomega.BeNil())

				gomega.Expect(importerpod.Check(ctx, k8sClient, mapping, 8)).To(gomega.Succeed())
				gomega.Expect(importerpod.Import(ctx, k8sClient, mapping, 8)).To(gomega.Succeed())
			})

			wl1LookupKey := types.NamespacedName{Name: pod.GetWorkloadNameForPod(pod1.Name, pod1.UID), Namespace: ns.Name}
			wl2LookupKey := types.NamespacedName{Name: pod.GetWorkloadNameForPod(pod2.Name, pod2.UID), Namespace: ns.Name}
			wl1 := &kueue.Workload{}
			wl2 := &kueue.Workload{}

			ginkgo.By("Checking the Workloads are created and admitted", func() {
				gomega.Expect(k8sClient.Get(ctx, wl1LookupKey, wl1)).To(gomega.Succeed())
				gomega.Expect(k8sClient.Get(ctx, wl2LookupKey, wl2)).To(gomega.Succeed())

				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1, wl2)
			})

			wl1UID := wl1.UID
			wl2UID := wl2.UID

			ginkgo.By("Starting kueue, the cluster queue status should account for the imported Workloads", func() {
				fwk.StartManager(ctx, cfg, managerAndSchedulerSetup)

				util.ExpectClusterQueueStatusMetric(cq, metrics.CQStatusActive)
				gomega.Eventually(func(g gomega.Gomega) {
					updatedQueue := &kueue.ClusterQueue{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), updatedQueue)).To(gomega.Succeed())
					g.Expect(updatedQueue.Status.AdmittedWorkloads).To(gomega.Equal(int32(2)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			pod3 := utiltestingpod.MakePod("pod3", ns.Name).
				Queue("lq1").
				Label(constants.ManagedByKueueLabel, "true").
				Request(corev1.ResourceCPU, "2").
				KueueSchedulingGate().
				Obj()

			ginkgo.By("Creating a new pod", func() {
				gomega.Expect(k8sClient.Create(ctx, pod3)).To(gomega.Succeed())
			})

			wl3LookupKey := types.NamespacedName{Name: pod.GetWorkloadNameForPod(pod3.Name, pod3.UID), Namespace: ns.Name}
			wl3 := &kueue.Workload{}

			ginkgo.By("Checking the Workload is created and pending while the old ones remain admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wl3LookupKey, wl3)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				util.ExpectWorkloadsToBePending(ctx, k8sClient, wl3)
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1, wl2)
			})

			ginkgo.By("By finishing an imported pod, the new one's Workload should be admitted", func() {
				util.SetPodsPhase(ctx, k8sClient, corev1.PodSucceeded, pod2)

				util.ExpectWorkloadToFinish(ctx, k8sClient, wl2LookupKey)
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1, wl3)
			})

			ginkgo.By("Checking the imported Workloads are not recreated", func() {
				gomega.Expect(k8sClient.Get(ctx, wl1LookupKey, wl1)).To(gomega.Succeed())
				gomega.Expect(wl1.UID).To(gomega.Equal(wl1UID))
				gomega.Expect(k8sClient.Get(ctx, wl2LookupKey, wl2)).To(gomega.Succeed())
				gomega.Expect(wl2.UID).To(gomega.Equal(wl2UID))
			})
		})
	})
})
