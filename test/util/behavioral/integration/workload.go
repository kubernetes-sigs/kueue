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

package integration

import (
	"context"
	"fmt"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadpatching "sigs.k8s.io/kueue/pkg/workload/patching"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

// SetPodsScheduledCondition simulates a tracker observation in the current admission.
func SetPodsScheduledCondition(ctx context.Context, k8sClient client.Client, wlKey client.ObjectKey, condition metav1.Condition) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		wl := &kueue.Workload{}
		g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
		admitted := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadAdmitted)
		g.Expect(admitted).NotTo(gomega.BeNil())
		g.Expect(admitted.Status).To(gomega.Equal(metav1.ConditionTrue))
		g.Expect(behavioral.RealClock.Now().Truncate(time.Second)).To(gomega.BeTemporally(">", admitted.LastTransitionTime.Time))
		g.Expect(workload.SetConditionAndUpdate(ctx, k8sClient, wl, kueue.WorkloadPodsScheduled,
			condition.Status, condition.Reason, condition.Message, "test", behavioral.RealClock)).To(gomega.Succeed())
	}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
}

func SetQuotaReservation(ctx context.Context, k8sClient client.Client, wlKey client.ObjectKey, admission *kueue.Admission) {
	clk := testingclock.NewFakeClock(time.Now())
	updatedWl := &kueue.Workload{}
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		g.ExpectWithOffset(1, k8sClient.Get(ctx, wlKey, updatedWl)).To(gomega.Succeed())
		g.ExpectWithOffset(1, workloadpatching.PatchAdmissionStatus(ctx, k8sClient, updatedWl, clk, func(wl *kueue.Workload) (bool, error) {
			var updated bool
			if admission == nil {
				updated = workload.UnsetQuotaReservationWithCondition(wl, "EvictedByTest", "Evicted By Test", clk.Now())
			} else {
				updated = workload.SetQuotaReservation(wl, admission, clk)
			}
			return updated, nil
		})).To(gomega.Succeed())
	}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed(), behavioral.AssertMsg("Failed to set quota reservation for workload", updatedWl))
}

func FinishWorkloads(ctx context.Context, k8sClient client.Client, workloads ...*kueue.Workload) {
	for _, w := range workloads {
		var newWL kueue.Workload
		gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(w), &newWL)).To(gomega.Succeed())
			apimeta.SetStatusCondition(&newWL.Status.Conditions, metav1.Condition{
				Type:               kueue.WorkloadFinished,
				Status:             metav1.ConditionTrue,
				LastTransitionTime: metav1.Now(),
				Reason:             "ByTest",
				Message:            "Finished by test",
			})
			g.Expect(k8sClient.Status().Update(ctx, &newWL)).Should(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed(), behavioral.AssertMsg("Failed to finish workload", &newWL))
	}
}

// SyncAdmittedConditionForWorkloads sets the Admission condition of the provided workloads based on
// the state of quota reservation and admission checks. It should be used in tests that are not running
// the workload controller.
func SyncAdmittedConditionForWorkloads(ctx context.Context, k8sClient client.Client, wls ...*kueue.Workload) {
	var updatedWorkload kueue.Workload
	for _, wl := range wls {
		gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
			g.ExpectWithOffset(1, k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWorkload)).To(gomega.Succeed())
			g.ExpectWithOffset(1, workloadpatching.PatchAdmissionStatus(ctx, k8sClient, &updatedWorkload, behavioral.RealClock, func(wl *kueue.Workload) (bool, error) {
				return workload.SyncAdmittedCondition(wl, time.Now()), nil
			})).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed(), behavioral.AssertMsg("Failed to sync admitted condition for workload", &updatedWorkload))
	}
}

func UpdateReclaimablePods(ctx context.Context, c client.Client, wl *kueue.Workload, reclaimablePods []kueue.ReclaimablePod) {
	ginkgo.GinkgoHelper()
	createdWl := &kueue.Workload{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(c.Get(ctx, client.ObjectKeyFromObject(wl), createdWl)).To(gomega.Succeed())
		g.Expect(workload.UpdateReclaimablePods(ctx, c, createdWl, reclaimablePods)).To(gomega.Succeed())
	}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed(), behavioral.AssertMsg("Failed to update reclaimable pods for workload", createdWl))
}

func SetRequeuedConditionWithPodsReadyTimeout(ctx context.Context, k8sClient client.Client, wlKey client.ObjectKey) {
	var wl kueue.Workload
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, wlKey, &wl)).Should(gomega.Succeed())
		g.Expect(workloadpatching.PatchAdmissionStatus(ctx, k8sClient, &wl, behavioral.RealClock, func(wl *kueue.Workload) (bool, error) {
			return workload.SetRequeuedCondition(wl, kueue.WorkloadEvictedByPodsReadyTimeout, fmt.Sprintf("Exceeded the PodsReady timeout %s", klog.KObj(wl).String()), false), nil
		})).Should(gomega.Succeed())
	}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed(), behavioral.AssertMsg("Failed to set requeued condition with PodsReady timeout", &wl))
}
