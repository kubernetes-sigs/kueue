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

	gocmp "github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobs/leaderworkerset"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
	workloadfinish "sigs.k8s.io/kueue/pkg/workload/finish"
	workloadpatching "sigs.k8s.io/kueue/pkg/workload/patching"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

func DeleteWorkloadsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	return deleteWorkloadsInNamespace(ctx, c, ns, 2)
}

func deleteWorkloadsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace, offset int) error {
	if err := c.DeleteAllOf(ctx, &kueue.Workload{}, client.InNamespace(ns.Name)); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	workloads := kueue.WorkloadList{}
	gomega.EventuallyWithOffset(offset, func(g gomega.Gomega) {
		g.Expect(c.List(ctx, &workloads, client.InNamespace(ns.Name))).Should(gomega.Succeed())
		for _, wl := range workloads.Items {
			if controllerutil.RemoveFinalizer(&wl, kueue.ResourceInUseFinalizerName) {
				g.Expect(client.IgnoreNotFound(c.Update(ctx, &wl))).Should(gomega.Succeed())
			}
		}
	}, MediumTimeout, Interval).Should(gomega.Succeed(), AssertMsgObjList(fmt.Sprintf("Failed to clean up workloads in namespace %s", ns.Name), &workloads))
	return nil
}

func ExpectWorkloadsToHaveQuotaReservation(ctx context.Context, k8sClient client.Client, cqName string, wls ...*kueue.Workload) {
	ginkgo.GinkgoHelper()
	wlKeys := workloadKeys(wls)
	ExpectWorkloadsToHaveQuotaReservationByKey(ctx, k8sClient, cqName, wlKeys...)
}

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

func ExpectWorkloadsToBePending(ctx context.Context, k8sClient client.Client, wls ...*kueue.Workload) {
	ginkgo.GinkgoHelper()
	wlKeys := workloadKeys(wls)
	ExpectWorkloadsToBePendingByKeys(ctx, k8sClient, wlKeys...)
}

var pendingQuotaReservedReasons = sets.New(
	kueue.WorkloadPending, //nolint:staticcheck // SA1019: legacy reason
	kueue.WorkloadWaiting, //nolint:staticcheck // SA1019: legacy reason
	kueue.WorkloadQuotaReservedReasonPendingEvaluation,
	kueue.WorkloadQuotaReservedReasonWaitingForQuota,
	kueue.WorkloadQuotaReservedReasonExceedsMaxQuota,
	kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
	kueue.WorkloadQuotaReservedReasonTopologyPlacementFailed,
	kueue.WorkloadQuotaReservedReasonWaitingForPodsReady,
	kueue.WorkloadQuotaReservedReasonNoMatchingFlavor,
	kueue.PreemptionGated,
)

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
			if cond != nil && cond.Status == metav1.ConditionFalse && cond.Reason == "Inadmissible" {
				inadmissible = append(inadmissible, wlKey)
			}
			wlObjects[i] = wl
		}
		g.Expect(inadmissible).Should(gomega.Equal(wlKeys))
	}, MediumTimeout, Interval).Should(gomega.Succeed(), AssertMsg("Unexpected workloads are inadmissible", wlObjects...))
}

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

func ExpectWorkloadsToBeAdmitted(ctx context.Context, k8sClient client.Client, wls ...*kueue.Workload) {
	ginkgo.GinkgoHelper()
	wlKeys := workloadKeys(wls)
	ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sClient, wlKeys...)
}

func ExpectWorkloadsToBeAdmittedByKeys(ctx context.Context, k8sClient client.Client, wlKeys ...client.ObjectKey) {
	ginkgo.GinkgoHelper()
	expectWorkloadsToBeAdmittedByKeysWithTimeout(ctx, k8sClient, Timeout, wlKeys...)
}

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

func ExpectWorkloadsToBeAdmittedCount(ctx context.Context, k8sClient client.Client, count int, wls ...*kueue.Workload) {
	ginkgo.GinkgoHelper()
	wlKeys := uniqueKeys(workloadKeys(wls))
	wlObjects := make([]*kueue.Workload, len(wlKeys))
	gomega.Eventually(func(g gomega.Gomega) {
		all := getWorkloadsByWlKeys(ctx, g, k8sClient, wlKeys)
		admitted := filter(all, workload.IsAdmitted)
		copy(wlObjects, all)
		g.Expect(admitted).Should(gomega.HaveLen(count))
	}, Timeout, Interval).Should(gomega.Succeed(), AssertMsg("Not enough workloads are admitted", wlObjects...))
}

func ExpectWorkloadsWithWorkloadPriority(ctx context.Context, c client.Client, name string, value int32, wlKeys ...client.ObjectKey) {
	ginkgo.GinkgoHelper()
	expectWorkloadsWithPriority(ctx, c, kueue.WorkloadPriorityClassGroup, kueue.WorkloadPriorityClassKind, name, value, wlKeys...)
}

func ExpectWorkloadsWithPodPriority(ctx context.Context, c client.Client, name string, value int32, wlKeys ...client.ObjectKey) {
	ginkgo.GinkgoHelper()
	expectWorkloadsWithPriority(ctx, c, kueue.PodPriorityClassGroup, kueue.PodPriorityClassKind, name, value, wlKeys...)
}

func expectWorkloadsWithPriority(
	ctx context.Context,
	c client.Client,
	priorityClassGroup kueue.PriorityClassGroup,
	priorityClassKind kueue.PriorityClassKind,
	name string,
	value int32,
	wlKeys ...client.ObjectKey,
) {
	ginkgo.GinkgoHelper()
	createdWl := &kueue.Workload{}
	gomega.Eventually(func(g gomega.Gomega) {
		for _, wlKey := range wlKeys {
			g.Expect(c.Get(ctx, wlKey, createdWl)).To(gomega.Succeed())
			g.Expect(createdWl.Spec.PriorityClassRef).ToNot(gomega.BeNil())
			g.Expect(createdWl.Spec.PriorityClassRef.Group).To(gomega.Equal(priorityClassGroup))
			g.Expect(createdWl.Spec.PriorityClassRef.Kind).To(gomega.Equal(priorityClassKind))
			g.Expect(createdWl.Spec.PriorityClassRef.Name).To(gomega.Equal(name))
			g.Expect(createdWl.Spec.Priority).To(gomega.Equal(&value))
		}
	}, Timeout, Interval).Should(gomega.Succeed(), AssertMsg("Workload priority does not match expected", createdWl))
}

func ExpectWorkloadToFinishWithTimeout(ctx context.Context, k8sClient client.Client, wlKey client.ObjectKey, timeout time.Duration) {
	ginkgo.GinkgoHelper()
	var wl kueue.Workload
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, wlKey, &wl)).To(gomega.Succeed())
		g.Expect(wl.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadFinished), "it's finished")
	}, timeout, Interval).Should(gomega.Succeed(), AssertMsg("Workload did not finish", &wl))
}

func ExpectWorkloadToFinish(ctx context.Context, k8sClient client.Client, wlKey client.ObjectKey) {
	ginkgo.GinkgoHelper()
	ExpectWorkloadToFinishWithTimeout(ctx, k8sClient, wlKey, MediumTimeout)
}

func ExpectWorkloadResourceUsage(ctx context.Context, k8sClient client.Client, wlKey client.ObjectKey, resourceName corev1.ResourceName, expected string) {
	ginkgo.GinkgoHelper()
	var wl kueue.Workload
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, wlKey, &wl)).To(gomega.Succeed())
		g.Expect(workload.HasQuotaReservation(&wl)).To(gomega.BeTrue())
		g.Expect(wl.Status.Admission).NotTo(gomega.BeNil())
		g.Expect(wl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))

		assignment := wl.Status.Admission.PodSetAssignments[0]
		g.Expect(assignment.ResourceUsage).To(gomega.HaveKey(resourceName))
		usage := assignment.ResourceUsage[resourceName]
		g.Expect(usage.Cmp(resource.MustParse(expected))).To(gomega.Equal(0))
	}, Timeout, Interval).Should(gomega.Succeed(), AssertMsg("workload should have resource usage of "+expected+" for "+string(resourceName), &wl))
}

func ExpectPodsReadyCondition(ctx context.Context, k8sClient client.Client, wlKey client.ObjectKey) {
	var wl kueue.Workload
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, wlKey, &wl)).To(gomega.Succeed())
		g.Expect(wl.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadPodsReady), "pods are ready")
	}, MediumTimeout, Interval).Should(gomega.Succeed(), AssertMsg("Workload pods are not ready", &wl))
}

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

func ExpectWorkloadsToBePreempted(ctx context.Context, k8sClient client.Client, wls ...*kueue.Workload) {
	ginkgo.GinkgoHelper()
	wlKeys := workloadKeys(wls)
	ExpectWorkloadsToBePreemptedByKeys(ctx, k8sClient, wlKeys...)
}

func ExpectWorkloadsToBePreemptedByKeys(ctx context.Context, k8sClient client.Client, wlKeys ...client.ObjectKey) {
	ginkgo.GinkgoHelper()
	wlKeys = uniqueKeys(wlKeys)
	wlObjects := make([]*kueue.Workload, len(wlKeys))
	gomega.Eventually(func(g gomega.Gomega) {
		preempted := make([]client.ObjectKey, 0, len(wlKeys))
		for i, wlKey := range wlKeys {
			wl := &kueue.Workload{}
			g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
			cond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadEvicted)
			if cond != nil && cond.Status == metav1.ConditionTrue && cond.Reason == kueue.WorkloadPreempted {
				preempted = append(preempted, wlKey)
			}
			wlObjects[i] = wl
		}
		g.Expect(preempted).Should(gomega.Equal(wlKeys))
	}, Timeout, Interval).Should(gomega.Succeed(), AssertMsg("Unexpected workloads are preempted", wlObjects...))
}

func ExpectWorkloadsToBeWaiting(ctx context.Context, k8sClient client.Client, wls ...*kueue.Workload) {
	ginkgo.GinkgoHelper()
	wlKeys := uniqueKeys(workloadKeys(wls))
	wlObjects := make([]*kueue.Workload, len(wlKeys))
	gomega.Eventually(func(g gomega.Gomega) {
		waiting := make([]client.ObjectKey, 0, len(wlKeys))
		for i, wlKey := range wlKeys {
			wl := &kueue.Workload{}
			g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
			cond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
			if cond != nil && cond.Status == metav1.ConditionFalse && cond.Reason == kueue.WorkloadWaiting { //nolint:staticcheck // SA1019: legacy reason
				waiting = append(waiting, wlKey)
			}
			wlObjects[i] = wl
		}
		g.Expect(waiting).Should(gomega.Equal(wlKeys))
	}, Timeout, Interval).Should(gomega.Succeed(), AssertMsg("Unexpected workloads are waiting", wlObjects...))
}

func ExpectWorkloadsToBeFrozen(ctx context.Context, k8sClient client.Client, cq string, wls ...*kueue.Workload) {
	wlKeys := uniqueKeys(workloadKeys(wls))
	wlObjects := make([]*kueue.Workload, len(wlKeys))
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		frozen := make([]client.ObjectKey, 0, len(wlKeys))
		for i, wlKey := range wlKeys {
			wl := &kueue.Workload{}
			g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
			cond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
			msg := fmt.Sprintf("ClusterQueue %s is inactive", cq)
			if cond != nil && cond.Status == metav1.ConditionFalse && cond.Reason == "Inadmissible" && cond.Message == msg {
				frozen = append(frozen, wlKey)
			}
			wlObjects[i] = wl
		}
		g.Expect(frozen).Should(gomega.Equal(wlKeys))
	}, Timeout, Interval).Should(gomega.Succeed(), AssertMsg("Unexpected workloads are frozen", wlObjects...))
}

func ExpectWorkloadToBeAdmittedAs(ctx context.Context, k8sClient client.Client, wl *kueue.Workload, admission *kueue.Admission) {
	var updatedWorkload kueue.Workload
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWorkload)).To(gomega.Succeed())
		g.Expect(updatedWorkload.Status.Admission).Should(gomega.BeComparableTo(admission))
	}, Timeout, Interval).Should(gomega.Succeed(), AssertMsg("Workload was not admitted with expected admission", &updatedWorkload))
}

func ExpectWorkloadsToBeEvictedByKeys(ctx context.Context, k8sClient client.Client, wlKeys ...client.ObjectKey) {
	ginkgo.GinkgoHelper()
	wlKeys = uniqueKeys(wlKeys)
	wlObjects := make([]*kueue.Workload, len(wlKeys))
	gomega.Eventually(func(g gomega.Gomega) {
		evicted := make([]client.ObjectKey, 0, len(wlKeys))
		for i, wlKey := range wlKeys {
			wl := &kueue.Workload{}
			g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
			if workloadevict.IsEvicted(wl) {
				evicted = append(evicted, wlKey)
			}
			wlObjects[i] = wl
		}
		g.Expect(evicted).Should(gomega.Equal(wlKeys))
	}, Timeout, Interval).Should(gomega.Succeed(), AssertMsg("Unexpected workloads were marked for eviction", wlObjects...))
}

func FinishEvictionForWorkloads(ctx context.Context, k8sClient client.Client, wls ...*kueue.Workload) {
	ginkgo.GinkgoHelper()
	wlKeys := uniqueKeys(workloadKeys(wls))
	ExpectWorkloadsToBeEvictedByKeys(ctx, k8sClient, wlKeys...)
	// unset the quota reservation
	for _, key := range wlKeys {
		wl := &kueue.Workload{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, key, wl)).Should(gomega.Succeed())
			if workload.HasQuotaReservation(wl) {
				g.Expect(
					workloadpatching.PatchAdmissionStatus(ctx, k8sClient, wl, RealClock, func(wl *kueue.Workload) (bool, error) {
						return workload.UnsetQuotaReservationWithCondition(wl, kueue.WorkloadPending, "By test", time.Now()), nil //nolint:staticcheck // SA1019: legacy reason
					}),
				).Should(gomega.Succeed(), fmt.Sprintf("Unable to unset quota reservation for %q", key))
			}
		}, Timeout, Interval).Should(gomega.Succeed(), AssertMsg("Failed to unset quota reservation for evicted workload", wl))
	}
}

func SetWorkloadsAdmissionCheck(ctx context.Context, k8sClient client.Client, wl *kueue.Workload, check kueue.AdmissionCheckReference, state kueue.CheckState, expectExisting bool) {
	var updatedWorkload kueue.Workload
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWorkload)).To(gomega.Succeed())
		if expectExisting {
			currentCheck := admissioncheck.FindAdmissionCheck(updatedWorkload.Status.AdmissionChecks, check)
			g.Expect(currentCheck).NotTo(gomega.BeNil(), "the check %s was not found in %s", check, workload.Key(wl))
			currentCheck.State = state
		} else {
			workloadpatching.SetAdmissionCheckState(&updatedWorkload.Status.AdmissionChecks, kueue.AdmissionCheckState{
				Name:  check,
				State: state,
			}, RealClock)
		}
		g.Expect(k8sClient.Status().Update(ctx, &updatedWorkload)).To(gomega.Succeed())
	}, Timeout, Interval).Should(gomega.Succeed(), AssertMsg("Failed to set admission check on workload", &updatedWorkload))
}

func AwaitAndVerifyWorkloadQueueName(ctx context.Context, client client.Client, createdWorkload *kueue.Workload, wlLookupKey types.NamespacedName, jobQueueName kueue.LocalQueueName) {
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		g.Expect(client.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
		g.Expect(createdWorkload.Spec.QueueName).Should(gomega.Equal(jobQueueName))
	}, Timeout, Interval).Should(gomega.Succeed(), AssertMsg("Workload queue name does not match expected", createdWorkload))
}

func AwaitAndVerifyCreatedWorkload(ctx context.Context, client client.Client, wlLookupKey types.NamespacedName, createdJob metav1.Object) *kueue.Workload {
	createdWorkload := &kueue.Workload{}
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		g.Expect(client.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
	}, Timeout, Interval).Should(gomega.Succeed(), AssertMsg("Workload was not created", createdWorkload))
	gomega.ExpectWithOffset(1, metav1.IsControlledBy(createdWorkload, createdJob)).To(gomega.BeTrue(), "The Workload should be owned by the Job")
	return createdWorkload
}

func ExpectWorkloadsFinalizedOrGone(ctx context.Context, k8sClient client.Client, keys ...types.NamespacedName) {
	for _, key := range keys {
		createdWorkload := &kueue.Workload{}
		gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
			err := k8sClient.Get(ctx, key, createdWorkload)
			// Skip further checks to avoid verifying finalizers on the old Workload object.
			if apierrors.IsNotFound(err) {
				return
			}
			g.Expect(err).To(gomega.Succeed())
			g.Expect(createdWorkload.Finalizers).To(gomega.BeEmpty())
		}, Timeout, Interval).Should(gomega.Succeed(), AssertMsg("Expected workload to be finalized", createdWorkload))
	}
}

func ExpectPreemptedCondition(
	ctx context.Context,
	k8sClient client.Client,
	reason string,
	status metav1.ConditionStatus,
	preemptedWl, preempteeWl *kueue.Workload,
	preemteeWorkloadUID, preempteeJobUID, preemptorPath, preempteePath string,
) {
	conditionCmpOpts := cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime", "ObservedGeneration")
	preemptedWlCopy := &kueue.Workload{}
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(preemptedWl), preemptedWlCopy)).To(gomega.Succeed())
		g.Expect(preemptedWlCopy.Status.Conditions).To(gomega.ContainElements(gomega.BeComparableTo(metav1.Condition{
			Type:   kueue.WorkloadPreempted,
			Status: status,
			Reason: reason,
			Message: fmt.Sprintf(
				"Preempted to accommodate a workload (UID: %s, JobUID: %s) due to %s; preemptor path: %s; preemptee path: %s",
				preemteeWorkloadUID,
				preempteeJobUID,
				preemption.HumanReadablePreemptionReasons[reason],
				preemptorPath,
				preempteePath,
			),
		}, conditionCmpOpts)))
	}, Timeout, Interval).Should(gomega.Succeed(), AssertMsg("Preemption condition not set correctly", preemptedWlCopy, preempteeWl))
}

func DeactivateWorkload(ctx context.Context, c client.Client, key client.ObjectKey) {
	ginkgo.GinkgoHelper()
	wl := &kueue.Workload{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(c.Get(ctx, key, wl)).To(gomega.Succeed())
		wl.Spec.Active = new(false)
		g.Expect(c.Update(ctx, wl)).To(gomega.Succeed())
	}, Timeout, Interval).Should(gomega.Succeed(), AssertMsg("Failed to deactivate workload", wl))
}

// ExpectWorkloadsInNamespace waits until the specified number of kueue.Workload
// objects exist in the given namespace, then returns the list observed at that moment.
//
// It repeatedly lists Workloads using the provided client until the count of
// items matches the expected value. The poll frequency and maximum wait time are
// controlled by the test-scoped Interval and Timeout variables. If the expected
// count is not reached before Timeout, the test fails.
//
// Returns:
//
//	The slice of Workloads present in the namespace when the expectation is met.
func ExpectWorkloadsInNamespace(ctx context.Context, k8sClient client.Client, namespace string, count int) []kueue.Workload {
	ginkgo.GinkgoHelper()
	list := &kueue.WorkloadList{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.List(ctx, list, client.InNamespace(namespace))).To(gomega.Succeed())
		g.Expect(list.Items).Should(gomega.HaveLen(count))
	}, Timeout, Interval).Should(gomega.Succeed())
	return list.Items
}

// ExpectNewWorkloadSlice waits until a new kueue.Workload is created in the same
// namespace as the given oldWorkload, and whose replacement annotation points to
// the oldWorkload's key.
//
// This helper repeatedly lists Workloads in the oldWorkload's namespace until it
// finds one where workloadslicing.ReplacementForKey matches the key of the given
// oldWorkload. The search is retried until the test-scoped Timeout expires, polling
// at the configured Interval. If no such workload is found within the Timeout,
// the test fails.
//
// Returns:
//   - newWorkload: A pointer to the discovered replacement Workload. Guaranteed
//     non-nil if the function succeeds; otherwise, the test fails before returning.
func ExpectNewWorkloadSlice(ctx context.Context, k8sClient client.Client, oldWorkload *kueue.Workload) (newWorkload *kueue.Workload) {
	ginkgo.GinkgoHelper()
	return ExpectNewWorkloadSliceWithTimeout(ctx, k8sClient, oldWorkload, Timeout)
}

// ExpectNewWorkloadSliceWithTimeout is like ExpectNewWorkloadSlice, but allows
// callers to specify how long to wait for the replacement Workload.
func ExpectNewWorkloadSliceWithTimeout(ctx context.Context, k8sClient client.Client, oldWorkload *kueue.Workload, timeout time.Duration) (newWorkload *kueue.Workload) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		// Reset newWorkload each iteration to ensure the returned value is from
		// the current poll, not a stale pointer from a previous retry attempt.
		newWorkload = nil
		wlList := &kueue.WorkloadList{}
		g.Expect(k8sClient.List(ctx, wlList, client.InNamespace(oldWorkload.Namespace))).To(gomega.Succeed())
		for i := range wlList.Items {
			wl := &wlList.Items[i]
			if key := workloadslicing.ReplacementForKey(wl); key != nil && *key == workload.Key(oldWorkload) {
				newWorkload = wl
				break
			}
		}
		g.Expect(newWorkload).ShouldNot(gomega.BeNil())
	}, timeout, Interval).Should(gomega.Succeed(), AssertMsg("No replacement workload slice found for old workload", oldWorkload))
	return newWorkload
}

// FindNonFinishedWorkloads returns the subset of workloads that are not finished.
func FindNonFinishedWorkloads(workloads []kueue.Workload) []kueue.Workload {
	var active []kueue.Workload
	for i := range workloads {
		if !workloadfinish.IsFinished(&workloads[i]) {
			active = append(active, workloads[i])
		}
	}
	return active
}

// DeleteWorkloadSliceAndAwaitDeletion deletes the named workload slice and waits
// until it is gone, stripping the resource-in-use finalizer if it blocks removal.
// Used by elastic-job tests to emulate a rollout garbage-collecting an origin
// (root) slice while later slices and their pods still point at its name.
func DeleteWorkloadSliceAndAwaitDeletion(ctx context.Context, k8sClient client.Client, key types.NamespacedName) {
	ginkgo.GinkgoHelper()
	slice := &kueue.Workload{}
	gomega.Expect(k8sClient.Get(ctx, key, slice)).To(gomega.Succeed())
	gomega.Expect(k8sClient.Delete(ctx, slice)).To(gomega.Succeed())
	gomega.Eventually(func(g gomega.Gomega) {
		wl := &kueue.Workload{}
		err := k8sClient.Get(ctx, key, wl)
		if apierrors.IsNotFound(err) {
			return
		}
		g.Expect(err).To(gomega.Succeed())
		if controllerutil.RemoveFinalizer(wl, kueue.ResourceInUseFinalizerName) {
			g.Expect(client.IgnoreNotFound(k8sClient.Update(ctx, wl))).To(gomega.Succeed())
		}
		g.Expect(apierrors.IsNotFound(k8sClient.Get(ctx, key, wl))).To(gomega.BeTrue())
	}, Timeout, Interval).Should(gomega.Succeed())
}

// ExpectWorkloadSliceAdmittedBeforeOldFinished watches workload events and asserts
// that the old workload slice is not marked Finished before the new slice is Admitted.
// The watcher must be started before the scale-up that triggers the replacement.
func ExpectWorkloadSliceAdmittedBeforeOldFinished(watcher watch.Interface, oldWorkloadName string, timeout time.Duration) {
	ginkgo.GinkgoHelper()
	oldSliceFinished := false
	newSliceAdmitted := false
	timeoutCh := time.After(timeout)
	for !newSliceAdmitted {
		select {
		case evt, ok := <-watcher.ResultChan():
			gomega.Expect(ok).Should(gomega.BeTrue(), "watch channel closed unexpectedly")
			if evt.Type == watch.Error {
				status, _ := evt.Object.(*metav1.Status)
				gomega.Expect(evt.Type).ShouldNot(gomega.Equal(watch.Error), fmt.Sprintf("watch error: %v", status))
			}
			if evt.Type != watch.Modified {
				continue
			}
			wl, isWorkload := evt.Object.(*kueue.Workload)
			gomega.Expect(isWorkload).Should(gomega.BeTrue())

			if wl.Name == oldWorkloadName && workloadfinish.IsFinished(wl) {
				oldSliceFinished = true
			}
			if wl.Name != oldWorkloadName && workload.IsAdmitted(wl) {
				gomega.Expect(oldSliceFinished).Should(gomega.BeFalse(),
					"old workload slice was finished before new slice was admitted")
				newSliceAdmitted = true
			}
		case <-timeoutCh:
			gomega.Expect(newSliceAdmitted).Should(gomega.BeTrue(),
				"timed out waiting for new workload slice to be admitted")
		}
	}
}

func ExpectWorkloadAdmittedWithCheck(ctx context.Context, wlLookupKey types.NamespacedName, acName, clusterName string, client client.Client) {
	ginkgo.GinkgoHelper()
	ginkgo.By(fmt.Sprintf("Waiting to be admitted in %s and manager clusters", clusterName))
	ExpectWorkloadsToBeAdmittedByKeysWithTimeout(ctx, client, MediumTimeout, wlLookupKey)
	ExpectAdmissionCheckStateWithMessage(
		ctx, client, wlLookupKey,
		acName,
		kueue.CheckStateReady,
		fmt.Sprintf(`The workload was admitted on "%s"`, clusterName),
	)
}

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
			opts := []gocmp.Option{IgnoreConditionTimestampsAndObservedGeneration}
			if wantCond.Message == "" {
				opts = append(opts, IgnoreConditionMessage)
			}
			g.Expect(*cond).To(gomega.BeComparableTo(wantCond, opts...))
		}
	}, Timeout, Interval).Should(gomega.Succeed(), AssertMsg("Workload conditions did not match expectations", wl))
}

func WorkloadKeyForLeaderWorkerSet(lws *leaderworkersetv1.LeaderWorkerSet, group string) client.ObjectKey {
	return types.NamespacedName{
		Name:      leaderworkerset.GetWorkloadName(lws.UID, lws.Name, group),
		Namespace: lws.Namespace,
	}
}

func workloadKeys(wls []*kueue.Workload) []client.ObjectKey {
	wlKeys := make([]client.ObjectKey, 0, len(wls))
	for _, wl := range wls {
		wlKeys = append(wlKeys, client.ObjectKeyFromObject(wl))
	}
	return wlKeys
}

func uniqueKeys(keys []client.ObjectKey) []client.ObjectKey {
	return sets.New[client.ObjectKey](keys...).UnsortedList()
}
