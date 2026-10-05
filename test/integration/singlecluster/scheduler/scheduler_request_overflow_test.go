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

package scheduler

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util"
)

// A PodSet total is the per-Pod request times the count. Seven Pods at 1.4e18
// sum to 9.8e18, past MaxInt64. That used to saturate at MaxInt64, which equals
// the quota below, so the Workload was admitted. The exact total does not fit.
var _ = ginkgo.Describe("Scheduler requests past int64", func() {
	const (
		gpu            = corev1.ResourceName("example.com/gpu")
		maxInt64       = "9223372036854775807"
		exactPerPod    = "1317624576693539401" // MaxInt64 / 7
		overflowPerPod = "1400000000000000000" // 7 * this = 9.8e18
	)

	var (
		ns           *corev1.Namespace
		flavor       *kueue.ResourceFlavor
		clusterQueue *kueue.ClusterQueue
		localQueue   *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		// On 0.19 this gate is off, so a quota failure is recorded as Pending.
		// The condition reason below is the granular one.
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.UnadmittedWorkloadsObservability, true)

		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "overflow-")

		flavor = utiltestingapi.MakeResourceFlavor("overflow-flavor").Obj()
		util.MustCreate(ctx, k8sClient, flavor)

		clusterQueue = utiltestingapi.MakeClusterQueue("overflow-cq").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).
				Resource(gpu, maxInt64).
				Obj()).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("overflow-lq", ns.Name).
			ClusterQueue(clusterQueue.Name).
			Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
		gomega.Expect(util.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.It("should leave pending a workload whose PodSet total is past int64", func() {
		wl := utiltestingapi.MakeWorkload("overflow", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 7).
				Request(gpu, overflowPerPod).
				Obj()).
			Obj()
		util.MustCreate(ctx, k8sClient, wl)

		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
			g.Expect(wl.Status.Admission).To(gomega.BeNil())
			cond := meta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
			g.Expect(cond).NotTo(gomega.BeNil())
			g.Expect(cond.Status).To(gomega.Equal(metav1.ConditionFalse))
			g.Expect(cond.Reason).To(gomega.Equal(kueue.WorkloadQuotaReservedReasonExceedsMaxQuota))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("should admit a workload whose PodSet total is exactly MaxInt64", func() {
		wl := utiltestingapi.MakeWorkload("exact", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 7).
				Request(gpu, exactPerPod).
				Obj()).
			Obj()
		util.MustCreate(ctx, k8sClient, wl)
		util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)

		gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
		gomega.Expect(wl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))
		psa := wl.Status.Admission.PodSetAssignments[0]
		gomega.Expect(ptr.Deref(psa.Count, 0)).To(gomega.Equal(int32(7)))
		got := psa.ResourceUsage[gpu]
		gomega.Expect(got.Equal(resource.MustParse(maxInt64))).To(gomega.BeTrue(), "resourceUsage %s", got.String())
	})
})
