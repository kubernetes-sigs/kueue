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
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

// Requests over all Pods of a PodSet are int64 values that saturate at
// MaxInt64, so these specs use requests near that bound to check what the
// scheduler charges when a PodSet is scaled down.
var _ = ginkgo.Describe("Scheduler request scaling", func() {
	const gpu = corev1.ResourceName("example.com/gpu")

	var (
		ns           *corev1.Namespace
		flavor       *kueue.ResourceFlavor
		clusterQueue *kueue.ClusterQueue
		localQueue   *kueue.LocalQueue
	)

	var createQueues = func(quota string) {
		clusterQueue = utiltestingapi.MakeClusterQueue("scaling-cq").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).
				Resource(gpu, quota).
				Obj()).
			Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("scaling-lq", ns.Name).
			ClusterQueue(clusterQueue.Name).
			Obj()
		behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
	}

	var expectAdmittedWith = func(wl *kueue.Workload, count int32, usage string) {
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
			g.Expect(wl.Status.Admission).NotTo(gomega.BeNil())
			g.Expect(wl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))
			psa := wl.Status.Admission.PodSetAssignments[0]
			g.Expect(ptr.Deref(psa.Count, 0)).To(gomega.Equal(count))
			got := psa.ResourceUsage[gpu]
			g.Expect(got.Equal(resource.MustParse(usage))).To(gomega.BeTrue(), "resourceUsage %s", got.String())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	}

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "scaling-")

		flavor = utiltestingapi.MakeResourceFlavor("scaling-flavor").Obj()
		behavioral.MustCreate(ctx, k8sClient, flavor)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
		gomega.Expect(behavioral.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	// 3 Pods x 4e18 overflow to the MaxInt64 saturation value. Dividing that
	// total by 3 made 2 Pods look like 6.1e18 and fit a 7e18 quota, although
	// they need 8e18; only 1 Pod really fits.
	ginkgo.It("should partially admit the count whose per-Pod requests fit, not a share of a saturated total", func() {
		createQueues("7000000000000000000")

		wl := utiltestingapi.MakeWorkload("saturated", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).
				SetMinimumCount(1).
				Request(gpu, "4000000000000000000").
				Obj()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, wl)

		behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
		expectAdmittedWith(wl, 1, "4000000000000000000")
	})

	// 7 Pods x MaxInt64/7 total exactly MaxInt64 with no overflow, so the
	// value is not a saturation marker and reclaiming 6 Pods must release
	// their share of the quota.
	ginkgo.It("should release the quota of reclaimed Pods when the admitted total is exactly MaxInt64", func() {
		createQueues("9223372036854775807")

		first := utiltestingapi.MakeWorkload("exact-max", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 7).
				Request(gpu, "1317624576693539401").
				Obj()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, first)
		behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, first)
		expectAdmittedWith(first, 7, "9223372036854775807")

		second := utiltestingapi.MakeWorkload("one-unit", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Request(gpu, "1").
			Obj()
		behavioral.MustCreate(ctx, k8sClient, second)
		behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, second)

		behavioral.UpdateReclaimablePods(ctx, k8sClient, first, []kueue.ReclaimablePod{{Name: kueue.DefaultPodSetName, Count: 6}})
		behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, first, second)
	})
})
