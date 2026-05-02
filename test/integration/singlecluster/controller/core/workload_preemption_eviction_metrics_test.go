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
	"fmt"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util"
)

const (
	pePendingLowPriority  int32 = -1
	pePendingHighPriority int32 = 1
)

var _ = ginkgo.Describe("Workload eviction to pending metrics", ginkgo.Label("controller:workload", "area:core"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup)
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	var (
		ns          *corev1.Namespace
		alphaFlavor *kueue.ResourceFlavor
		cq          *kueue.ClusterQueue
		q           *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "pe-pending-metrics-")
		alphaFlavor = utiltestingapi.MakeResourceFlavor("alpha").Obj()
		util.MustCreate(ctx, k8sClient, alphaFlavor)

		cqName := fmt.Sprintf("cq-pe-%d", time.Now().UnixNano())
		cq = utiltestingapi.MakeClusterQueue(cqName).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("alpha").Resource(corev1.ResourceCPU, "4").Obj()).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerOrNewerEqualPriority,
			}).
			Obj()
		util.MustCreate(ctx, k8sClient, cq)

		q = utiltestingapi.MakeLocalQueue("q", ns.Name).ClusterQueue(cq.Name).Obj()
		util.MustCreate(ctx, k8sClient, q)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, alphaFlavor, true)
	})

	ginkgo.It("should record workload_eviction_latency_seconds when a preemptee returns to Pending", func() {
		lowWl := utiltestingapi.MakeWorkload("low-wl", ns.Name).
			Queue(kueue.LocalQueueName(q.Name)).
			Priority(pePendingLowPriority).
			Request(corev1.ResourceCPU, "1").
			Obj()
		util.MustCreate(ctx, k8sClient, lowWl)
		util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, lowWl)

		highWl := utiltestingapi.MakeWorkload("high-wl", ns.Name).
			Queue(kueue.LocalQueueName(q.Name)).
			Priority(pePendingHighPriority).
			Request(corev1.ResourceCPU, "4").
			Obj()
		util.MustCreate(ctx, k8sClient, highWl)

		util.FinishEvictionForWorkloads(ctx, k8sClient, lowWl)

		util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, highWl)
		util.ExpectWorkloadsToBePending(ctx, k8sClient, lowWl)

		util.ExpectWorkloadEvictionLatencyHistogramMetricAtLeast(kueue.ClusterQueueReference(cq.Name), kueue.WorkloadEvictedByPreemption, 1)
	})
})
