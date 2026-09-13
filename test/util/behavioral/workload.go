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

package behavioral

import (
	"context"
	"fmt"
	"slices"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
)

// FinishWorkloads marks workloads as finished
func FinishWorkloads(ctx context.Context, k8sClient client.Client, workloads ...*kueue.Workload) {
	for _, w := range workloads {
		var newWL kueue.Workload
		gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(w), &newWL)).To(gomega.Succeed())
			newWL.Status.Conditions = append(w.Status.Conditions, metav1.Condition{
				Type:               kueue.WorkloadFinished,
				Status:             metav1.ConditionTrue,
				LastTransitionTime: metav1.Now(),
				Reason:             "ByTest",
				Message:            "Finished by test",
			})
			g.Expect(k8sClient.Status().Update(ctx, &newWL)).Should(gomega.Succeed())
		}, Timeout, Interval).Should(gomega.Succeed(), AssertMsg("Failed to finish workload", &newWL))
	}
}

// ExpectPodSetAdmittedCount waits until wl is admitted with count pods assigned to the named PodSet
func ExpectPodSetAdmittedCount(ctx context.Context, k8sClient client.Client, wl *kueue.Workload, podSetName kueue.PodSetReference, count int32) {
	ginkgo.GinkgoHelper()
	ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).Should(gomega.Succeed())
		g.Expect(wl.Status.Admission).ShouldNot(gomega.BeNil())
		assignments := wl.Status.Admission.PodSetAssignments
		idx := slices.IndexFunc(assignments, func(psa kueue.PodSetAssignment) bool {
			return psa.Name == podSetName
		})
		g.Expect(idx).ShouldNot(gomega.Equal(-1), AssertMsg(fmt.Sprintf("No admitted podSet %q", podSetName), wl))
		g.Expect(assignments[idx].Count).Should(gomega.Equal(&count))
	}, Timeout, Interval).Should(gomega.Succeed())
}

// ExpectWorkloadsToHaveQuotaReservation waits until workloads have quota reservation in given ClusterQueue
func ExpectWorkloadsToHaveQuotaReservation(ctx context.Context, k8sClient client.Client, cqName string, wls ...*kueue.Workload) {
	ginkgo.GinkgoHelper()
	wlKeys := workloadKeys(wls)
	ExpectWorkloadsToHaveQuotaReservationByKey(ctx, k8sClient, cqName, wlKeys...)
}

// ExpectWorkloadsToHaveQuotaReservationByKey waits until workloads by key have quota reservation
func ExpectWorkloadsToHaveQuotaReservationByKey(ctx context.Context, k8sClient client.Client, cqName string, wlKeys ...client.ObjectKey) {
	ginkgo.GinkgoHelper()
	wlKeys = uniqueKeys(wlKeys)
	wlObjects := make([]*kueue.Workload, len(wlKeys))
	gomega.Eventually(func(g gomega.Gomega) {
		admitted := make([]client.ObjectKey, 0, len(wlKeys))
		for i, wlKey := range wlKeys {
			wl := &kueue.Workload{}
			g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
			if workload.HasQuotaReservation(wl) && string(wl.Status.Admission.ClusterQueue) == cqName {
				admitted = append(admitted, wlKey)
			}
			wlObjects[i] = wl
		}
		g.Expect(admitted).Should(gomega.Equal(wlKeys))
	}, Timeout, Interval).Should(gomega.Succeed(), AssertMsg("Unexpected workloads with QuotaReservation", wlObjects...))
}

// FilterEvictedWorkloads returns only evicted workloads from the given list
func FilterEvictedWorkloads(ctx context.Context, k8sClient client.Client, wls ...*kueue.Workload) []*kueue.Workload {
	return filterWorkloads(ctx, k8sClient, workloadevict.IsEvicted, wls...)
}

func filterWorkloads(ctx context.Context, k8sClient client.Client, filter func(*kueue.Workload) bool, wls ...*kueue.Workload) []*kueue.Workload {
	ret := make([]*kueue.Workload, 0, len(wls))
	var updatedWorkload kueue.Workload
	for _, wl := range wls {
		err := k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWorkload)
		if err == nil && filter(&updatedWorkload) {
			ret = append(ret, wl)
		}
	}
	return ret
}

// UnholdClusterQueue unholds a ClusterQueue
func UnholdClusterQueue(ctx context.Context, k8sClient client.Client, cq *kueue.ClusterQueue) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		var newCQ kueue.ClusterQueue
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &newCQ)).To(gomega.Succeed())
		controllerutil.RemoveFinalizer(&newCQ, kueue.ResourceInUseFinalizerName)
		g.Expect(k8sClient.Update(ctx, &newCQ)).To(gomega.Succeed())
	}, Timeout, Interval).Should(gomega.Succeed())
}

// UnholdLocalQueue unholds a LocalQueue
func UnholdLocalQueue(ctx context.Context, k8sClient client.Client, lq *kueue.LocalQueue) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		var newLQ kueue.LocalQueue
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lq), &newLQ)).To(gomega.Succeed())
		controllerutil.RemoveFinalizer(&newLQ, kueue.ResourceInUseFinalizerName)
		g.Expect(k8sClient.Update(ctx, &newLQ)).To(gomega.Succeed())
	}, Timeout, Interval).Should(gomega.Succeed())
}
