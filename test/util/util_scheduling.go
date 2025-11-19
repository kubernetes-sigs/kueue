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

package util

import (
	"context"
	"time"

	"github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func FinishRunningWorkloadsInCQ(ctx context.Context, k8sClient client.Client, cq *kueue.ClusterQueue, n int) {
	var wList kueue.WorkloadList
	gomega.ExpectWithOffset(1, k8sClient.List(ctx, &wList)).To(gomega.Succeed())
	finished := 0
	for i := 0; i < len(wList.Items) && finished < n; i++ {
		wl := wList.Items[i]
		if wl.Status.Admission != nil && string(wl.Status.Admission.ClusterQueue) == cq.Name && !meta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadFinished) {
			FinishWorkloads(ctx, k8sClient, &wl)
			finished++
		}
	}
	gomega.ExpectWithOffset(1, finished).To(gomega.Equal(n), "Not enough workloads finished")
}

func finishEvictionsOfAnyWorkloadsInCq(ctx context.Context, k8sClient client.Client, cq *kueue.ClusterQueue) sets.Set[types.UID] {
	finished := sets.New[types.UID]()
	var wList kueue.WorkloadList
	gomega.Expect(k8sClient.List(ctx, &wList)).To(gomega.Succeed())
	for _, wl := range wList.Items {
		if wl.Status.Admission == nil || string(wl.Status.Admission.ClusterQueue) != cq.Name {
			continue
		}
		evicted := meta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadEvicted)
		quotaReserved := meta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
		if evicted && quotaReserved {
			gomega.Expect(workload.PatchAdmissionStatus(ctx, k8sClient, &wl, RealClock, func() (bool, error) {
				return workload.UnsetQuotaReservationWithCondition(&wl, "Pending", "Eviction finished by test", time.Now()), nil
			}),
			).To(gomega.Succeed())
			finished.Insert(wl.UID)
		}
	}
	return finished
}

func FinishEvictionOfWorkloadsInCQ(ctx context.Context, k8sClient client.Client, cq *kueue.ClusterQueue, n int) {
	finished := sets.New[types.UID]()
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		finished.Insert(finishEvictionsOfAnyWorkloadsInCq(ctx, k8sClient, cq).UnsortedList()...)
		g.Expect(finished.Len()).Should(gomega.Equal(n), "Not enough workloads evicted")
	}, Timeout, Interval).Should(gomega.Succeed())
}
