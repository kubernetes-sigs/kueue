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
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
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

func FinishEvictionOfWorkloadsInCQ(ctx context.Context, k8sClient client.Client, cq *kueue.ClusterQueue, n int) {
	var realClock = clock.RealClock{}
	finished := sets.New[types.UID]()
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		var wList kueue.WorkloadList
		g.Expect(k8sClient.List(ctx, &wList)).To(gomega.Succeed())
		for i := 0; i < len(wList.Items) && finished.Len() < n; i++ {
			wl := wList.Items[i]
			if wl.Status.Admission == nil || string(wl.Status.Admission.ClusterQueue) != cq.Name {
				continue
			}
			evicted := meta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadEvicted)
			quotaReserved := meta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
			if evicted && quotaReserved {
				workload.UnsetQuotaReservationWithCondition(&wl, "Pending", "Eviction finished by test", time.Now())
				g.Expect(workload.ApplyAdmissionStatus(ctx, k8sClient, &wl, true, realClock)).To(gomega.Succeed())
				finished.Insert(wl.UID)
			}
		}
		g.Expect(finished.Len()).Should(gomega.Equal(n), "Not enough workloads evicted")
	}, Timeout, Interval).Should(gomega.Succeed())
}
