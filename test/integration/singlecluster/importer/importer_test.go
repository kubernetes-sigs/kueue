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

package importer

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	importercache "sigs.k8s.io/kueue/cmd/importer/cache"
	importermapping "sigs.k8s.io/kueue/cmd/importer/mapping"
	importerpod "sigs.k8s.io/kueue/cmd/importer/pod"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	"sigs.k8s.io/kueue/pkg/metrics"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	utiltestingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("Importer", func() {
	var (
		flavor *kueue.ResourceFlavor
		lqName string
		ns1    *corev1.Namespace
		ns2    *corev1.Namespace
		lq1    *kueue.LocalQueue
		lq2    *kueue.LocalQueue
		cq1    *kueue.ClusterQueue
		cq2    *kueue.ClusterQueue
	)

	ginkgo.BeforeEach(func() {
		lqName = "shared-lq-name"

		flavor = utiltestingapi.MakeResourceFlavor("f").Obj()
		behavioral.MustCreate(ctx, k8sClient, flavor)

		ns1 = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "import-ns1-")

		cq1 = utiltestingapi.MakeClusterQueue("cq1").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("f").Resource(corev1.ResourceCPU, "4").Obj(),
			).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, cq1)

		lq1 = utiltestingapi.MakeLocalQueue(lqName, ns1.Name).ClusterQueue("cq1").Obj()
		behavioral.MustCreate(ctx, k8sClient, lq1)

		ns2 = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "import-ns2-")

		cq2 = utiltestingapi.MakeClusterQueue("cq2").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("f").Resource(corev1.ResourceCPU, "4").Obj(),
			).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, cq2)

		lq2 = utiltestingapi.MakeLocalQueue(lqName, ns2.Name).ClusterQueue("cq2").Obj()
		behavioral.MustCreate(ctx, k8sClient, lq2)
	})

	ginkgo.AfterEach(func() {
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, lq1, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, lq2, true)
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns1)).To(gomega.Succeed())
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns2)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq1, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq2, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
	})

	ginkgo.When("Kueue is started after import", func() {
		ginkgo.It("Should keep the imported pods admitted", framework.SlowSpec, func() {
			pod1 := utiltestingpod.MakePod("pod1", ns1.Name).
				Label("src.lbl", "src-val").
				Request(corev1.ResourceCPU, "2").
				Obj()
			pod2 := utiltestingpod.MakePod("pod2", ns1.Name).
				Label("src.lbl", "src-val").
				Request(corev1.ResourceCPU, "2").
				Obj()

			ginkgo.By("Creating the initial pods", func() {
				behavioral.MustCreate(ctx, k8sClient, pod1)
				behavioral.MustCreate(ctx, k8sClient, pod2)
			})

			ginkgo.By("Running the import", func() {
				mapping, err := importercache.Load(ctx, k8sClient, []string{ns1.Name}, importermapping.RulesForLabel("src.lbl", map[string]string{"src-val": lqName}), nil, nil)
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				gomega.Expect(mapping).ToNot(gomega.BeNil())

				gomega.Expect(importerpod.Check(ctx, k8sClient, mapping, 8)).To(gomega.Succeed())
				gomega.Expect(importerpod.Import(ctx, k8sClient, mapping, 8)).To(gomega.Succeed())
			})

			wl1LookupKey := types.NamespacedName{Name: pod.GetWorkloadNameForPod(pod1.Name, pod1.UID), Namespace: ns1.Name}
			wl2LookupKey := types.NamespacedName{Name: pod.GetWorkloadNameForPod(pod2.Name, pod2.UID), Namespace: ns1.Name}
			wl1 := &kueue.Workload{}
			wl2 := &kueue.Workload{}

			ginkgo.By("Checking the Workloads are created and admitted", func() {
				gomega.Expect(k8sClient.Get(ctx, wl1LookupKey, wl1)).To(gomega.Succeed())
				gomega.Expect(k8sClient.Get(ctx, wl2LookupKey, wl2)).To(gomega.Succeed())

				ginkgo.By("Verify workload2 is correct")
				behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1, wl2)
			})

			wl1UID := wl1.UID
			wl2UID := wl2.UID

			ginkgo.By("Starting kueue, the cluster queue status should account for the imported Workloads", func() {
				fwk.StartManager(ctx, cfg, managerAndSchedulerSetup)

				behavioral.ExpectClusterQueueStatusMetric(cq1, metrics.CQStatusActive)
				gomega.Eventually(func(g gomega.Gomega) {
					updatedQueue := &kueue.ClusterQueue{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq1), updatedQueue)).To(gomega.Succeed())
					g.Expect(updatedQueue.Status.AdmittedWorkloads).To(gomega.Equal(int32(2)))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			pod3 := utiltestingpod.MakePod("pod3", ns1.Name).
				Queue(lqName).
				Label(constants.ManagedByKueueLabelKey, constants.ManagedByKueueLabelValue).
				Request(corev1.ResourceCPU, "2").
				KueueSchedulingGate().
				Obj()

			ginkgo.By("Creating a new pod", func() {
				behavioral.MustCreate(ctx, k8sClient, pod3)
			})

			wl3LookupKey := types.NamespacedName{Name: pod.GetWorkloadNameForPod(pod3.Name, pod3.UID), Namespace: ns1.Name}
			wl3 := &kueue.Workload{}

			ginkgo.By("Checking the Workload is created and pending while the old ones remain admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wl3LookupKey, wl3)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

				behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wl3)
				behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1, wl2)
			})

			ginkgo.By("By finishing an imported pod, the new one's Workload should be admitted", func() {
				behavioral.SetPodsPhase(ctx, k8sClient, corev1.PodSucceeded, pod2)

				behavioral.ExpectWorkloadToFinish(ctx, k8sClient, wl2LookupKey)
				behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1, wl3)
			})

			ginkgo.By("Checking the imported Workloads are not recreated", func() {
				gomega.Expect(k8sClient.Get(ctx, wl1LookupKey, wl1)).To(gomega.Succeed())
				gomega.Expect(wl1.UID).To(gomega.Equal(wl1UID))
				gomega.Expect(k8sClient.Get(ctx, wl2LookupKey, wl2)).To(gomega.Succeed())
				gomega.Expect(wl2.UID).To(gomega.Equal(wl2UID))
			})
		})
	})

	ginkgo.It("Should assign local queues to appropriate namespaces", framework.SlowSpec, func() {
		var mapping *importercache.ImportCache
		var err error

		ginkgo.By("Importing across all namespaces", func() {
			mapping, err = importercache.Load(ctx, k8sClient, []string{ns1.Name, ns2.Name}, importermapping.RulesForLabel("src.lbl", map[string]string{"src-val": lqName}), nil, nil)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Expect(mapping).ToNot(gomega.BeNil())
		})

		ginkgo.By("Checking the local queues are assigned correctly", func() {
			gomega.Expect(mapping.LocalQueues).To(gomega.HaveLen(2))

			gomega.Expect(mapping.LocalQueues[ns1.Name]).To(gomega.HaveLen(1))
			gomega.Expect(mapping.LocalQueues[ns2.Name]).To(gomega.HaveLen(1))

			gomega.Expect(mapping.LocalQueues[ns1.Name]).To(gomega.HaveKey(lqName))
			gomega.Expect(mapping.LocalQueues[ns2.Name]).To(gomega.HaveKey(lqName))

			gomega.Expect(mapping.LocalQueues[ns1.Name][lqName].Namespace).To(gomega.Equal(ns1.Name))
			gomega.Expect(mapping.LocalQueues[ns2.Name][lqName].Namespace).To(gomega.Equal(ns2.Name))
		})
	})
})
