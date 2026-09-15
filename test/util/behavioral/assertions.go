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
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

const (
	ComponentTimeout       = 5 * time.Second
	ComponentMediumTimeout = 10 * time.Second
	ComponentInterval      = 100 * time.Millisecond
)

var pendingQuotaReservedReasons = sets.New(
	kueue.WorkloadQuotaReservedReasonPendingEvaluation,
	kueue.WorkloadQuotaReservedReasonWaitingForQuota,
	kueue.WorkloadQuotaReservedReasonExceedsMaxQuota,
	kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
	kueue.WorkloadQuotaReservedReasonTopologyPlacementFailed,
	kueue.WorkloadQuotaReservedReasonWaitingForPodsReady,
	kueue.WorkloadQuotaReservedReasonNoMatchingFlavor,
)

// ExpectWorkloadsToBeAdmitted waits until all workloads are admitted
func ExpectWorkloadsToBeAdmitted(ctx context.Context, k8sClient client.Client, wls ...*kueue.Workload) {
	ginkgo.GinkgoHelper()
	wlKeys := workloadKeys(wls)
	ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sClient, wlKeys...)
}

// ExpectWorkloadsToBeAdmittedByKeys waits until all workloads with given keys are admitted
func ExpectWorkloadsToBeAdmittedByKeys(ctx context.Context, k8sClient client.Client, wlKeys ...client.ObjectKey) {
	ginkgo.GinkgoHelper()
	expectWorkloadsToBeAdmittedByKeysWithTimeout(ctx, k8sClient, Timeout, wlKeys...)
}

// ExpectWorkloadsToBeAdmittedByKeysWithTimeout waits with custom timeout
func ExpectWorkloadsToBeAdmittedByKeysWithTimeout(ctx context.Context, k8sClient client.Client, timeout time.Duration, wlKeys ...client.ObjectKey) {
	ginkgo.GinkgoHelper()
	expectWorkloadsToBeAdmittedByKeysWithTimeout(ctx, k8sClient, timeout, wlKeys...)
}

func expectWorkloadsToBeAdmittedByKeysWithTimeout(ctx context.Context, k8sClient client.Client, timeout time.Duration, wlKeys ...client.ObjectKey) {
	ginkgo.GinkgoHelper()
	wlKeys = uniqueKeys(wlKeys)
	wlObjects := make([]*kueue.Workload, len(wlKeys))
	gomega.Eventually(func(g gomega.Gomega) {
		all := getWorkloadsByWlKeys(ctx, g, k8sClient, wlKeys)
		admitted := filter(all, workload.IsAdmitted)
		copy(wlObjects, all)
		g.Expect(workloadKeys(admitted)).Should(gomega.Equal(wlKeys))
	}, timeout, Interval).Should(gomega.Succeed(), AssertMsg("Unexpected workloads are admitted", wlObjects...))
}

// ExpectWorkloadsToBePending waits until all workloads are pending
func ExpectWorkloadsToBePending(ctx context.Context, k8sClient client.Client, wls ...*kueue.Workload) {
	ginkgo.GinkgoHelper()
	wlKeys := workloadKeys(wls)
	ExpectWorkloadsToBePendingByKeys(ctx, k8sClient, wlKeys...)
}

// ExpectWorkloadsToBePendingByKeys waits until all workloads with given keys are pending
func ExpectWorkloadsToBePendingByKeys(ctx context.Context, k8sClient client.Client, wlKeys ...client.ObjectKey) {
	ginkgo.GinkgoHelper()
	wlKeys = uniqueKeys(wlKeys)
	wlObjects := make([]*kueue.Workload, len(wlKeys))
	gomega.Eventually(func(g gomega.Gomega) {
		pending := make([]client.ObjectKey, 0, len(wlKeys))
		for i, wlKey := range wlKeys {
			wl := &kueue.Workload{}
			g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
			cond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
			if cond != nil && cond.Status == metav1.ConditionFalse && pendingQuotaReservedReasons.Has(cond.Reason) {
				pending = append(pending, wlKey)
			}
			wlObjects[i] = wl
		}
		g.Expect(pending).Should(gomega.Equal(wlKeys))
	}, Timeout, Interval).Should(gomega.Succeed(), AssertMsg("Unexpected workloads are pending", wlObjects...))
}

// ExpectWorkloadsToBeInadmissible waits until all workloads are inadmissible
func ExpectWorkloadsToBeInadmissible(ctx context.Context, k8sClient client.Client, wls ...*kueue.Workload) {
	ginkgo.GinkgoHelper()
	wlKeys := workloadKeys(wls)
	ExpectWorkloadsToBeInadmissibleByKeys(ctx, k8sClient, wlKeys...)
}

// ExpectWorkloadsToBeInadmissibleByKeys waits until all workloads with given keys are inadmissible
func ExpectWorkloadsToBeInadmissibleByKeys(ctx context.Context, k8sClient client.Client, wlKeys ...client.ObjectKey) {
	ginkgo.GinkgoHelper()
	wlKeys = uniqueKeys(wlKeys)
	wlObjects := make([]*kueue.Workload, len(wlKeys))
	gomega.Eventually(func(g gomega.Gomega) {
		inadmissible := make([]client.ObjectKey, 0, len(wlKeys))
		for i, wlKey := range wlKeys {
			wl := &kueue.Workload{}
			g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
			cond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
			if cond != nil && cond.Status == metav1.ConditionFalse && (cond.Reason == kueue.WorkloadInadmissible || cond.Reason == kueue.WorkloadQuotaReservedReasonMisconfigured) {
				inadmissible = append(inadmissible, wlKey)
			}
			wlObjects[i] = wl
		}
		g.Expect(inadmissible).Should(gomega.Equal(wlKeys))
	}, MediumTimeout, Interval).Should(gomega.Succeed(), AssertMsg("Unexpected workloads are inadmissible", wlObjects...))
}

// ExpectWorkloadsToBeAdmittedCount waits until exactly count workloads are admitted
func ExpectWorkloadsToBeAdmittedCount(ctx context.Context, k8sClient client.Client, count int, wls ...*kueue.Workload) {
	ginkgo.GinkgoHelper()
	wlKeys := uniqueKeys(workloadKeys(wls))
	wlObjects := make([]*kueue.Workload, len(wlKeys))
	gomega.Eventually(func(g gomega.Gomega) {
		all := getWorkloadsByWlKeys(ctx, g, k8sClient, wlKeys)
		admitted := filter(all, workload.IsAdmitted)
		copy(wlObjects, all)
		g.Expect(len(admitted)).Should(gomega.Equal(count))
	}, Timeout, Interval).Should(gomega.Succeed(), AssertMsg("Unexpected workload admitted count", wlObjects...))
}

// ExpectWorkloadToFinish waits until a workload is finished
func ExpectWorkloadToFinish(ctx context.Context, k8sClient client.Client, wlKey client.ObjectKey) {
	ginkgo.GinkgoHelper()
	ExpectWorkloadToFinishWithTimeout(ctx, k8sClient, wlKey, Timeout)
}

// ExpectWorkloadToFinishWithTimeout waits until a workload is finished with custom timeout
func ExpectWorkloadToFinishWithTimeout(ctx context.Context, k8sClient client.Client, wlKey client.ObjectKey, timeout time.Duration) {
	gomega.Eventually(func(g gomega.Gomega) {
		wl := &kueue.Workload{}
		g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
		cond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadFinished)
		g.Expect(cond).NotTo(gomega.BeNil())
		g.Expect(cond.Status).Should(gomega.Equal(metav1.ConditionTrue))
	}, timeout, Interval).Should(gomega.Succeed())
}

// ExpectWorkloadResourceUsage verifies workload resource usage
func ExpectWorkloadResourceUsage(ctx context.Context, k8sClient client.Client, wlKey client.ObjectKey, resourceName string, expected string) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		wl := &kueue.Workload{}
		g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
		g.Expect(workload.HasQuotaReservation(wl)).To(gomega.BeTrue())
		g.Expect(wl.Status.Admission).NotTo(gomega.BeNil())
		g.Expect(wl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))

		usage := wl.Status.Admission.PodSetAssignments[0].ResourceUsage
		resourceKey := corev1.ResourceName(resourceName)
		g.Expect(usage).To(gomega.HaveKey(resourceKey))
		actualValue := usage[resourceKey]
		g.Expect(actualValue.Cmp(resource.MustParse(expected))).To(gomega.Equal(0))
	}, Timeout, Interval).Should(gomega.Succeed())
}

func AssertMsg(message string, _ ...*kueue.Workload) string {
	return message
}

// Helper functions for workload operations
func getWorkloadsByWlKeys(ctx context.Context, g gomega.Gomega, k8sClient client.Client, wlKeys []client.ObjectKey) (workloads []*kueue.Workload) {
	ginkgo.GinkgoHelper()
	workloads = make([]*kueue.Workload, len(wlKeys))
	for i, wlKey := range wlKeys {
		wl := &kueue.Workload{}
		g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
		workloads[i] = wl
	}
	return workloads
}

func filter[T any](slice []T, keep func(T) bool) []T {
	var result []T
	for _, v := range slice {
		if keep(v) {
			result = append(result, v)
		}
	}
	return result
}

func workloadKeys(wls []*kueue.Workload) []client.ObjectKey {
	ret := make([]client.ObjectKey, len(wls))
	for i, wl := range wls {
		ret[i] = client.ObjectKeyFromObject(wl)
	}
	return ret
}

func uniqueKeys(keys []client.ObjectKey) []client.ObjectKey {
	// Keep unique keys (remove duplicates)
	seen := make(map[client.ObjectKey]bool)
	var result []client.ObjectKey
	for _, k := range keys {
		if !seen[k] {
			seen[k] = true
			result = append(result, k)
		}
	}
	return result
}
