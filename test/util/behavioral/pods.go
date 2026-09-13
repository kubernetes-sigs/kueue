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
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadpatching "sigs.k8s.io/kueue/pkg/workload/patching"
	testutil "sigs.k8s.io/kueue/test/util"
)

// SetPodsScheduledCondition sets the PodsScheduled condition on a workload
func SetPodsScheduledCondition(ctx context.Context, k8sClient client.Client, wlKey client.ObjectKey, condition metav1.Condition) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		wl := &kueue.Workload{}
		g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
		g.Expect(workload.SetConditionAndUpdate(ctx, k8sClient, wl, kueue.WorkloadPodsScheduled,
			condition.Status, condition.Reason, condition.Message, "test", testutil.RealClock)).To(gomega.Succeed())
	}, Timeout, Interval).Should(gomega.Succeed())
}

// ExpectPodsReadyCondition waits until workload has PodsReady condition
func ExpectPodsReadyCondition(ctx context.Context, k8sClient client.Client, wlKey client.ObjectKey) {
	var wl kueue.Workload
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, wlKey, &wl)).To(gomega.Succeed())
		g.Expect(wl.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadPodsReady), "pods are ready")
	}, MediumTimeout, Interval).Should(gomega.Succeed(), AssertMsg("Workload pods are not ready", &wl))
}

// AwaitWorkloadEvictionByPodsReadyTimeout waits until workload is evicted by PodsReady timeout
func AwaitWorkloadEvictionByPodsReadyTimeout(ctx context.Context, k8sClient client.Client, wlKey client.ObjectKey, sleep time.Duration) {
	if sleep > 0 {
		time.Sleep(sleep)
		ginkgo.By(fmt.Sprintf("exceeded the timeout %q for the %q workload", sleep.String(), wlKey.String()))
	}
	var wl kueue.Workload
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, wlKey, &wl)).Should(gomega.Succeed())
		g.Expect(wl.Status.Conditions).Should(gomega.ContainElements(gomega.BeComparableTo(metav1.Condition{
			Type:    kueue.WorkloadEvicted,
			Status:  metav1.ConditionTrue,
			Reason:  kueue.WorkloadEvictedByPodsReadyTimeout,
			Message: fmt.Sprintf("Exceeded the PodsReady timeout %s", klog.KObj(&wl).String()),
		}, IgnoreConditionTimestampsAndObservedGeneration)))
	}, Timeout, Interval).Should(gomega.Succeed(), AssertMsg("Workload was not evicted by PodsReady timeout", &wl))
}

// SetRequeuedConditionWithPodsReadyTimeout sets the requeued condition with PodsReady timeout reason
func SetRequeuedConditionWithPodsReadyTimeout(ctx context.Context, k8sClient client.Client, wlKey client.ObjectKey) {
	var wl kueue.Workload
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, wlKey, &wl)).Should(gomega.Succeed())
		g.Expect(workloadpatching.PatchAdmissionStatus(ctx, k8sClient, &wl, testutil.RealClock, func(wl *kueue.Workload) (bool, error) {
			return workload.SetRequeuedCondition(wl, kueue.WorkloadEvictedByPodsReadyTimeout, fmt.Sprintf("Exceeded the PodsReady timeout %s", klog.KObj(wl).String()), false), nil
		})).Should(gomega.Succeed())
	}, Timeout, Interval).Should(gomega.Succeed(), AssertMsg("Failed to set requeued condition with PodsReady timeout", &wl))
}

// ExpectWorkloadToHaveRequeueState waits until workload has expected requeue state
func ExpectWorkloadToHaveRequeueState(ctx context.Context, k8sClient client.Client, wlKey client.ObjectKey, expected *kueue.RequeueState, hasRequeueAt bool) {
	var wl kueue.Workload
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, wlKey, &wl)).Should(gomega.Succeed())
		g.Expect(wl.Status.RequeueState).Should(gomega.BeComparableTo(expected, cmpopts.IgnoreFields(kueue.RequeueState{}, "RequeueAt")))
		if expected != nil {
			if hasRequeueAt {
				g.Expect(wl.Status.RequeueState.RequeueAt).ShouldNot(gomega.BeNil())
			} else {
				g.Expect(wl.Status.RequeueState.RequeueAt).Should(gomega.BeNil())
			}
		}
	}, Timeout, Interval).Should(gomega.Succeed(), AssertMsg("Workload requeue state does not match expected", &wl))
}

// ExpectWorkloadToHaveConditions waits until workload has all expected conditions
func ExpectWorkloadToHaveConditions(
	ctx context.Context,
	k8sClient client.Client,
	wlKey client.ObjectKey,
	wantConditions ...metav1.Condition,
) {
	ginkgo.GinkgoHelper()
	wl := &kueue.Workload{}
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
		for _, wantCond := range wantConditions {
			cond := apimeta.FindStatusCondition(wl.Status.Conditions, wantCond.Type)
			g.Expect(cond).NotTo(gomega.BeNil())
			opts := []cmp.Option{IgnoreConditionTimestampsAndObservedGeneration}
			if wantCond.Message == "" {
				opts = append(opts, IgnoreConditionMessage)
			}
			g.Expect(*cond).To(gomega.BeComparableTo(wantCond, opts...))
		}
	}, Timeout, Interval).Should(gomega.Succeed(), AssertMsg("Workload conditions did not match expectations", wl))
}
