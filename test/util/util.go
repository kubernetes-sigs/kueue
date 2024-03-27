/*
Copyright 2022 The Kubernetes Authors.

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
	"fmt"
	"time"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/component-base/metrics/testutil"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
)

func DeleteAdmissionCheck(ctx context.Context, c client.Client, ac *kueue.AdmissionCheck) error {
	if ac != nil {
		if err := c.Delete(ctx, ac); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func DeleteProvisioningRequestConfig(ctx context.Context, c client.Client, ac *kueue.ProvisioningRequestConfig) error {
	if ac != nil {
		if err := c.Delete(ctx, ac); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func DeleteWorkload(ctx context.Context, c client.Client, wl *kueue.Workload) error {
	if wl != nil {
		if err := c.Delete(ctx, wl); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func DeleteClusterQueue(ctx context.Context, c client.Client, cq *kueue.ClusterQueue) error {
	if cq == nil {
		return nil
	}
	if err := c.Delete(ctx, cq); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func DeleteResourceFlavor(ctx context.Context, c client.Client, rf *kueue.ResourceFlavor) error {
	if rf == nil {
		return nil
	}
	if err := c.Delete(ctx, rf); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func DeleteLocalQueue(ctx context.Context, c client.Client, q *kueue.LocalQueue) error {
	if q == nil {
		return nil
	}
	if err := c.Delete(ctx, q); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// DeleteNamespace deletes all objects the tests typically create in the namespace.
func DeleteNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	if ns == nil {
		return nil
	}
	if err := DeleteAllJobsInNamespace(ctx, c, ns); err != nil {
		return err
	}
	if err := c.DeleteAllOf(ctx, &kueue.LocalQueue{}, client.InNamespace(ns.Name)); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err := DeleteAllPodsInNamespace(ctx, c, ns); err != nil {
		return err
	}
	if err := DeleteWorkloadsInNamespace(ctx, c, ns); err != nil {
		return err
	}
	err := c.DeleteAllOf(ctx, &corev1.LimitRange{}, client.InNamespace(ns.Name), client.PropagationPolicy(metav1.DeletePropagationBackground))
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err := c.Delete(ctx, ns); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func DeleteAllJobsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	err := c.DeleteAllOf(ctx, &batchv1.Job{}, client.InNamespace(ns.Name), client.PropagationPolicy(metav1.DeletePropagationBackground))
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func DeleteAllJobsetsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	err := c.DeleteAllOf(ctx, &jobset.JobSet{}, client.InNamespace(ns.Name), client.PropagationPolicy(metav1.DeletePropagationBackground))
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func DeleteAllPodsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	err := c.DeleteAllOf(ctx, &corev1.Pod{}, client.InNamespace(ns.Name))
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting all Pods in namespace %q: %w", ns.Name, err)
	}

	gomega.Eventually(func() error {
		lst := corev1.PodList{}
		err := c.List(ctx, &lst, client.InNamespace(ns.Name))
		if err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("listing Pods with a finalizer in namespace %q: %w", ns.Name, err)
		}

		for _, p := range lst.Items {
			if controllerutil.RemoveFinalizer(&p, pod.PodFinalizer) {
				err = c.Update(ctx, &p)
				if err != nil && !apierrors.IsNotFound(err) {
					return fmt.Errorf("removing finalizer: %w", err)
				}
			}
		}

		return nil
	}, LongTimeout, Interval).Should(gomega.Succeed())

	return nil
}

func DeleteWorkloadsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	if err := c.DeleteAllOf(ctx, &kueue.Workload{}, client.InNamespace(ns.Name)); err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	gomega.Eventually(func() error {
		lst := kueue.WorkloadList{}
		err := c.List(ctx, &lst, client.InNamespace(ns.Name))
		if err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("listing Workloads with a finalizer in namespace %q: %w", ns.Name, err)
		}

		for _, wl := range lst.Items {
			if controllerutil.RemoveFinalizer(&wl, kueue.ResourceInUseFinalizerName) {
				err = c.Update(ctx, &wl)
				if err != nil && !apierrors.IsNotFound(err) {
					return fmt.Errorf("removing finalizer: %w", err)
				}
			}
		}

		return nil
	}, LongTimeout, Interval).Should(gomega.Succeed())

	return nil
}

func DeleteRuntimeClass(ctx context.Context, c client.Client, runtimeClass *nodev1.RuntimeClass) error {
	if runtimeClass == nil {
		return nil
	}
	if err := c.Delete(ctx, runtimeClass); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func UnholdQueue(ctx context.Context, k8sClient client.Client, cq *kueue.ClusterQueue) {
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		var cqCopy kueue.ClusterQueue
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &cqCopy)).To(gomega.Succeed())
		if ptr.Deref(cqCopy.Spec.StopPolicy, kueue.None) == kueue.None {
			return
		}
		cqCopy.Spec.StopPolicy = ptr.To(kueue.None)
		g.Expect(k8sClient.Update(ctx, &cqCopy)).To(gomega.Succeed())
	}, Timeout, Interval).Should(gomega.Succeed())
}

func FinishWorkloads(ctx context.Context, k8sClient client.Client, workloads ...*kueue.Workload) {
	for _, w := range workloads {
		gomega.EventuallyWithOffset(1, func() error {
			var newWL kueue.Workload
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(w), &newWL)).To(gomega.Succeed())
			newWL.Status.Conditions = append(w.Status.Conditions, metav1.Condition{
				Type:               kueue.WorkloadFinished,
				Status:             metav1.ConditionTrue,
				LastTransitionTime: metav1.Now(),
				Reason:             "ByTest",
				Message:            "Finished by test",
			})
			return k8sClient.Status().Update(ctx, &newWL)
		}, Timeout, Interval).Should(gomega.Succeed())
	}
}

func ExpectWorkloadsToHaveQuotaReservation(ctx context.Context, k8sClient client.Client, cqName string, wls ...*kueue.Workload) {
	gomega.EventuallyWithOffset(1, func() int {
		admitted := 0
		var updatedWorkload kueue.Workload
		for _, wl := range wls {
			gomega.ExpectWithOffset(1, k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWorkload)).To(gomega.Succeed())
			if workload.HasQuotaReservation(&updatedWorkload) && string(updatedWorkload.Status.Admission.ClusterQueue) == cqName {
				admitted++
			}
		}
		return admitted
	}, Timeout, Interval).Should(gomega.Equal(len(wls)), "Not enough workloads were admitted")
}

func FilterAdmittedWorkloads(ctx context.Context, k8sClient client.Client, wls ...*kueue.Workload) []*kueue.Workload {
	return filterWorkloads(ctx, k8sClient, workload.HasQuotaReservation, wls...)
}

func FilterEvictedWorkloads(ctx context.Context, k8sClient client.Client, wls ...*kueue.Workload) []*kueue.Workload {
	return filterWorkloads(ctx, k8sClient, func(wl *kueue.Workload) bool {
		return apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadEvicted)
	}, wls...)
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
	gomega.EventuallyWithOffset(1, func() int {
		pending := 0
		var updatedWorkload kueue.Workload
		for _, wl := range wls {
			gomega.ExpectWithOffset(1, k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWorkload)).To(gomega.Succeed())
			cond := apimeta.FindStatusCondition(updatedWorkload.Status.Conditions, kueue.WorkloadQuotaReserved)
			if cond == nil {
				continue
			}
			if cond.Status == metav1.ConditionFalse && cond.Reason == "Pending" {
				pending++
			}
		}
		return pending
	}, Timeout, Interval).Should(gomega.Equal(len(wls)), "Not enough workloads are pending")
}

func ExpectWorkloadsToBeAdmitted(ctx context.Context, k8sClient client.Client, wls ...*kueue.Workload) {
	expectWorkloadsToBeAdmittedCountWithOffset(ctx, 2, k8sClient, len(wls), wls...)
}

func ExpectWorkloadsToBeAdmittedCount(ctx context.Context, k8sClient client.Client, count int, wls ...*kueue.Workload) {
	expectWorkloadsToBeAdmittedCountWithOffset(ctx, 2, k8sClient, count, wls...)
}

func expectWorkloadsToBeAdmittedCountWithOffset(ctx context.Context, offset int, k8sClient client.Client, count int, wls ...*kueue.Workload) {
	gomega.EventuallyWithOffset(offset, func() int {
		admitted := 0
		var updatedWorkload kueue.Workload
		for _, wl := range wls {
			gomega.ExpectWithOffset(offset, k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWorkload)).To(gomega.Succeed())
			if apimeta.IsStatusConditionTrue(updatedWorkload.Status.Conditions, kueue.WorkloadAdmitted) {
				admitted++
			}
		}
		return admitted
	}, Timeout, Interval).Should(gomega.Equal(count), "Not enough workloads are admitted")
}

func ExpectWorkloadToFinish(ctx context.Context, k8sClient client.Client, wlKey client.ObjectKey) {
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		var wl kueue.Workload
		g.Expect(k8sClient.Get(ctx, wlKey, &wl)).To(gomega.Succeed())
		g.Expect(apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadFinished)).
			To(gomega.BeTrueBecause("it's finished"))
	}, LongTimeout, Interval).Should(gomega.Succeed())
}

func ExpectWorkloadToHaveRequeueCount(ctx context.Context, k8sClient client.Client, wlKey client.ObjectKey, requeueCount *int32) {
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		var wl kueue.Workload
		g.Expect(k8sClient.Get(ctx, wlKey, &wl)).Should(gomega.Succeed())
		g.Expect(ptr.Deref(wl.Status.RequeueState, kueue.RequeueState{})).Should(gomega.BeComparableTo(kueue.RequeueState{
			Count: requeueCount,
		}, cmpopts.IgnoreFields(kueue.RequeueState{}, "RequeueAt")))
		if requeueCount != nil {
			g.Expect(wl.Status.RequeueState.RequeueAt).ShouldNot(gomega.BeNil())
		}
	}, Timeout, Interval).Should(gomega.Succeed())
}

func ExpectWorkloadsToBePreempted(ctx context.Context, k8sClient client.Client, wls ...*kueue.Workload) {
	gomega.EventuallyWithOffset(1, func() int {
		preempted := 0
		var updatedWorkload kueue.Workload
		for _, wl := range wls {
			gomega.ExpectWithOffset(1, k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWorkload)).To(gomega.Succeed())
			cond := apimeta.FindStatusCondition(updatedWorkload.Status.Conditions, kueue.WorkloadEvicted)
			if cond == nil {
				continue
			}
			if cond.Status == metav1.ConditionTrue {
				preempted++
			}
		}
		return preempted
	}, Timeout, Interval).Should(gomega.Equal(len(wls)), "Not enough workloads are preempted")
}

func ExpectWorkloadsToBeWaiting(ctx context.Context, k8sClient client.Client, wls ...*kueue.Workload) {
	gomega.EventuallyWithOffset(1, func() int {
		pending := 0
		var updatedWorkload kueue.Workload
		for _, wl := range wls {
			gomega.ExpectWithOffset(1, k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWorkload)).To(gomega.Succeed())
			cond := apimeta.FindStatusCondition(updatedWorkload.Status.Conditions, kueue.WorkloadQuotaReserved)
			if cond == nil {
				continue
			}
			if cond.Status == metav1.ConditionFalse && cond.Reason == "Waiting" {
				pending++
			}
		}
		return pending
	}, Timeout, Interval).Should(gomega.Equal(len(wls)), "Not enough workloads are waiting")
}

func ExpectWorkloadsToBeFrozen(ctx context.Context, k8sClient client.Client, cq string, wls ...*kueue.Workload) {
	gomega.EventuallyWithOffset(1, func() int {
		frozen := 0
		var updatedWorkload kueue.Workload
		for _, wl := range wls {
			gomega.ExpectWithOffset(1, k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWorkload)).To(gomega.Succeed())
			cond := apimeta.FindStatusCondition(updatedWorkload.Status.Conditions, kueue.WorkloadQuotaReserved)
			if cond == nil {
				continue
			}
			msg := fmt.Sprintf("ClusterQueue %s is inactive", cq)
			if cond.Status == metav1.ConditionFalse && cond.Reason == "Inadmissible" && cond.Message == msg {
				frozen++
			}
		}
		return frozen
	}, Timeout, Interval).Should(gomega.Equal(len(wls)), "Not enough workloads are frozen")
}

func ExpectWorkloadToBeAdmittedAs(ctx context.Context, k8sClient client.Client, wl *kueue.Workload, admission *kueue.Admission) {
	var updatedWorkload kueue.Workload
	gomega.EventuallyWithOffset(1, func() *kueue.Admission {
		gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWorkload)).To(gomega.Succeed())
		return updatedWorkload.Status.Admission
	}, Timeout, Interval).Should(gomega.BeComparableTo(admission))
}

var attemptStatuses = []metrics.AdmissionResult{metrics.AdmissionResultInadmissible, metrics.AdmissionResultSuccess}

func ExpectAdmissionAttemptsMetric(pending, admitted int) {
	vals := []int{pending, admitted}

	for i, status := range attemptStatuses {
		metric := metrics.AdmissionAttemptsTotal.WithLabelValues(string(status))
		gomega.EventuallyWithOffset(1, func() int {
			v, err := testutil.GetCounterMetricValue(metric)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			return int(v)
		}, Timeout, Interval).Should(gomega.Equal(vals[i]), "pending_workloads with status=%s", status)
	}
}

var pendingStatuses = []string{metrics.PendingStatusActive, metrics.PendingStatusInadmissible}

func ExpectPendingWorkloadsMetric(cq *kueue.ClusterQueue, active, inadmissible int) {
	vals := []int{active, inadmissible}
	for i, status := range pendingStatuses {
		metric := metrics.PendingWorkloads.WithLabelValues(cq.Name, status)
		gomega.EventuallyWithOffset(1, func() int {
			v, err := testutil.GetGaugeMetricValue(metric)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			return int(v)
		}, Timeout, Interval).Should(gomega.Equal(vals[i]), "pending_workloads with status=%s", status)
	}
}

func ExpectReservingActiveWorkloadsMetric(cq *kueue.ClusterQueue, v int) {
	metric := metrics.ReservingActiveWorkloads.WithLabelValues(cq.Name)
	gomega.EventuallyWithOffset(1, func() int {
		v, err := testutil.GetGaugeMetricValue(metric)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		return int(v)
	}, Timeout, Interval).Should(gomega.Equal(v))
}

func ExpectAdmittedWorkloadsTotalMetric(cq *kueue.ClusterQueue, v int) {
	metric := metrics.AdmittedWorkloadsTotal.WithLabelValues(cq.Name)
	gomega.EventuallyWithOffset(1, func() int {
		v, err := testutil.GetCounterMetricValue(metric)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		return int(v)
	}, Timeout, Interval).Should(gomega.Equal(v))
}

func ExpectClusterQueueStatusMetric(cq *kueue.ClusterQueue, status metrics.ClusterQueueStatus) {
	for i, s := range metrics.CQStatuses {
		var wantV float64
		if metrics.CQStatuses[i] == status {
			wantV = 1
		}
		metric := metrics.ClusterQueueByStatus.WithLabelValues(cq.Name, string(s))
		gomega.EventuallyWithOffset(1, func() float64 {
			v, err := testutil.GetGaugeMetricValue(metric)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			return v
		}, Timeout, Interval).Should(gomega.Equal(wantV), "cluster_queue_status with status=%s", s)
	}
}

func ExpectAdmissionCheckToBeDeleted(ctx context.Context, k8sClient client.Client, ac *kueue.AdmissionCheck, deleteAC bool) {
	if ac == nil {
		return
	}
	if deleteAC {
		gomega.Expect(client.IgnoreNotFound(DeleteAdmissionCheck(ctx, k8sClient, ac))).To(gomega.Succeed())
	}
	gomega.EventuallyWithOffset(1, func() error {
		var newAC kueue.AdmissionCheck
		return k8sClient.Get(ctx, client.ObjectKeyFromObject(ac), &newAC)
	}, Timeout, Interval).Should(testing.BeNotFoundError())
}

func ExpectProvisioningRequestConfigToBeDeleted(ctx context.Context, k8sClient client.Client, prc *kueue.ProvisioningRequestConfig, deleteAC bool) {
	if prc == nil {
		return
	}
	if deleteAC {
		gomega.ExpectWithOffset(1, client.IgnoreNotFound(DeleteProvisioningRequestConfig(ctx, k8sClient, prc))).To(gomega.Succeed())
	}
	gomega.EventuallyWithOffset(1, func() error {
		var newAC kueue.AdmissionCheck
		return k8sClient.Get(ctx, client.ObjectKeyFromObject(prc), &newAC)
	}, Timeout, Interval).Should(testing.BeNotFoundError())
}

func ExpectClusterQueueToBeDeleted(ctx context.Context, k8sClient client.Client, cq *kueue.ClusterQueue, deleteCq bool) {
	if deleteCq {
		gomega.Expect(DeleteClusterQueue(ctx, k8sClient, cq)).ToNot(gomega.HaveOccurred())
	}
	gomega.EventuallyWithOffset(1, func() error {
		var newCQ kueue.ClusterQueue
		return k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &newCQ)
	}, Timeout, Interval).Should(testing.BeNotFoundError())
}

func ExpectResourceFlavorToBeDeleted(ctx context.Context, k8sClient client.Client, rf *kueue.ResourceFlavor, deleteRf bool) {
	if rf == nil {
		return
	}
	if deleteRf {
		gomega.Expect(DeleteResourceFlavor(ctx, k8sClient, rf)).To(gomega.Succeed())
	}
	gomega.EventuallyWithOffset(1, func() error {
		var newRF kueue.ResourceFlavor
		return k8sClient.Get(ctx, client.ObjectKeyFromObject(rf), &newRF)
	}, Timeout, Interval).Should(testing.BeNotFoundError())
}

func ExpectCQResourceNominalQuota(cq *kueue.ClusterQueue, flavor, resource string, v float64) {
	metric := metrics.ClusterQueueResourceNominalQuota.WithLabelValues(cq.Spec.Cohort, cq.Name, flavor, resource)
	gomega.EventuallyWithOffset(1, func() float64 {
		v, err := testutil.GetGaugeMetricValue(metric)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		return v
	}, Timeout, Interval).Should(gomega.Equal(v))
}

func ExpectCQResourceBorrowingQuota(cq *kueue.ClusterQueue, flavor, resource string, v float64) {
	metric := metrics.ClusterQueueResourceBorrowingLimit.WithLabelValues(cq.Spec.Cohort, cq.Name, flavor, resource)
	gomega.EventuallyWithOffset(1, func() float64 {
		v, err := testutil.GetGaugeMetricValue(metric)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		return v
	}, Timeout, Interval).Should(gomega.Equal(v))
}

func ExpectCQResourceReservations(cq *kueue.ClusterQueue, flavor, resource string, v float64) {
	metric := metrics.ClusterQueueResourceReservations.WithLabelValues(cq.Spec.Cohort, cq.Name, flavor, resource)
	gomega.EventuallyWithOffset(1, func() float64 {
		v, err := testutil.GetGaugeMetricValue(metric)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		return v
	}, Timeout, Interval).Should(gomega.Equal(v))
}

func SetQuotaReservation(ctx context.Context, k8sClient client.Client, wl *kueue.Workload, admission *kueue.Admission) error {
	wl = wl.DeepCopy()
	if admission == nil {
		workload.UnsetQuotaReservationWithCondition(wl, "EvictedByTest", "Evicted By Test")
	} else {
		workload.SetQuotaReservation(wl, admission)
	}
	return workload.ApplyAdmissionStatus(ctx, k8sClient, wl, false)
}

// SyncAdmittedConditionForWorkloads sets the Admission condition of the provided workloads based on
// the state of quota reservation and admission checks. It should be use in tests that are not running
// the workload controller.
func SyncAdmittedConditionForWorkloads(ctx context.Context, k8sClient client.Client, wls ...*kueue.Workload) {
	var updatedWorkload kueue.Workload
	for _, wl := range wls {
		gomega.ExpectWithOffset(1, k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWorkload)).To(gomega.Succeed())
		if workload.SyncAdmittedCondition(&updatedWorkload) {
			gomega.ExpectWithOffset(1, workload.ApplyAdmissionStatus(ctx, k8sClient, &updatedWorkload, false)).To(gomega.Succeed())
		}
	}
}

func FinishEvictionForWorkloads(ctx context.Context, k8sClient client.Client, wls ...*kueue.Workload) {
	gomega.EventuallyWithOffset(1, func() int {
		evicting := 0
		var updatedWorkload kueue.Workload
		for _, wl := range wls {
			gomega.ExpectWithOffset(1, k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWorkload)).To(gomega.Succeed())
			if cond := apimeta.FindStatusCondition(updatedWorkload.Status.Conditions, kueue.WorkloadEvicted); cond != nil && cond.Status == metav1.ConditionTrue {
				evicting++
			}
		}
		return evicting
	}, Timeout, Interval).Should(gomega.Equal(len(wls)), "Not enough workloads were marked for eviction")
	// unset the quota reservation
	for i := range wls {
		key := client.ObjectKeyFromObject(wls[i])
		gomega.EventuallyWithOffset(1, func() error {
			var updatedWorkload kueue.Workload
			if err := k8sClient.Get(ctx, key, &updatedWorkload); err != nil {
				return err
			}

			if apimeta.IsStatusConditionTrue(updatedWorkload.Status.Conditions, kueue.WorkloadQuotaReserved) {
				workload.UnsetQuotaReservationWithCondition(&updatedWorkload, "Pending", "By test")
				return workload.ApplyAdmissionStatus(ctx, k8sClient, &updatedWorkload, true)
			}
			return nil
		}, Timeout, Interval).Should(gomega.Succeed(), fmt.Sprintf("Unable to unset quota reservation for %q", key))
	}
}

func SetAdmissionCheckActive(ctx context.Context, k8sClient client.Client, admissionCheck *kueue.AdmissionCheck, status metav1.ConditionStatus) {
	gomega.EventuallyWithOffset(1, func() error {
		var updatedAc kueue.AdmissionCheck
		err := k8sClient.Get(ctx, client.ObjectKeyFromObject(admissionCheck), &updatedAc)
		if err != nil {
			return err
		}
		apimeta.SetStatusCondition(&updatedAc.Status.Conditions, metav1.Condition{
			Type:    kueue.AdmissionCheckActive,
			Status:  status,
			Reason:  "ByTest",
			Message: "by test",
		})
		return k8sClient.Status().Update(ctx, &updatedAc)
	}, Timeout, Interval).Should(gomega.Succeed())
}

func SetWorkloadsAdmissionCheck(ctx context.Context, k8sClient client.Client, wl *kueue.Workload, check string, state kueue.CheckState, expectExisting bool) {
	var updatedWorkload kueue.Workload
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWorkload)).To(gomega.Succeed())
		if expectExisting {
			currentCheck := workload.FindAdmissionCheck(updatedWorkload.Status.AdmissionChecks, check)
			g.Expect(currentCheck).NotTo(gomega.BeNil(), "the check %s was not found in %s", check, workload.Key(wl))
			currentCheck.State = state
		} else {
			workload.SetAdmissionCheckState(&updatedWorkload.Status.AdmissionChecks, kueue.AdmissionCheckState{
				Name:  check,
				State: state,
			})
		}
		g.Expect(k8sClient.Status().Update(ctx, &updatedWorkload)).To(gomega.Succeed())
	}, Timeout, Interval).Should(gomega.Succeed())
}

func AwaitAndVerifyWorkloadQueueName(ctx context.Context, client client.Client, createdWorkload *kueue.Workload, wlLookupKey types.NamespacedName, jobQueueName string) {
	gomega.EventuallyWithOffset(1, func() bool {
		if err := client.Get(ctx, wlLookupKey, createdWorkload); err != nil {
			return false
		}
		return createdWorkload.Spec.QueueName == jobQueueName
	}, Timeout, Interval).Should(gomega.BeTrue())
}

func AwaitAndVerifyCreatedWorkload(ctx context.Context, client client.Client, wlLookupKey types.NamespacedName, createdJob metav1.Object) *kueue.Workload {
	createdWorkload := &kueue.Workload{}
	gomega.EventuallyWithOffset(1, func() error {
		return client.Get(ctx, wlLookupKey, createdWorkload)
	}, Timeout, Interval).Should(gomega.Succeed())
	gomega.ExpectWithOffset(1, metav1.IsControlledBy(createdWorkload, createdJob)).To(gomega.BeTrue(), "The Workload should be owned by the Job")
	return createdWorkload
}

func VerifyWorkloadPriority(createdWorkload *kueue.Workload, priorityClassName string, priorityValue int32) {
	ginkgo.By("checking the workload is created with priority and priorityName")
	gomega.ExpectWithOffset(1, createdWorkload.Spec.PriorityClassName).Should(gomega.Equal(priorityClassName))
	gomega.ExpectWithOffset(1, *createdWorkload.Spec.Priority).Should(gomega.Equal(priorityValue))
}

func SetPodsPhase(ctx context.Context, k8sClient client.Client, phase corev1.PodPhase, pods ...*corev1.Pod) {
	for _, p := range pods {
		updatedPod := corev1.Pod{}
		gomega.ExpectWithOffset(1, k8sClient.Get(ctx, client.ObjectKeyFromObject(p), &updatedPod)).To(gomega.Succeed())
		updatedPod.Status.Phase = phase
		gomega.ExpectWithOffset(1, k8sClient.Status().Update(ctx, &updatedPod)).To(gomega.Succeed())
	}
}

func ExpectPodUnsuspendedWithNodeSelectors(ctx context.Context, k8sClient client.Client, key types.NamespacedName, ns map[string]string) {
	createdPod := &corev1.Pod{}
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, key, createdPod)).To(gomega.Succeed())
		g.Expect(createdPod.Spec.SchedulingGates).NotTo(gomega.ContainElement(corev1.PodSchedulingGate{Name: pod.SchedulingGateName}))
		g.Expect(createdPod.Spec.NodeSelector).To(gomega.BeComparableTo(ns))
	}, Timeout, Interval).Should(gomega.Succeed())
}

func ExpectPodsFinalized(ctx context.Context, k8sClient client.Client, keys ...types.NamespacedName) {
	for _, key := range keys {
		createdPod := &corev1.Pod{}
		gomega.EventuallyWithOffset(1, func(g gomega.Gomega) []string {
			g.Expect(k8sClient.Get(ctx, key, createdPod)).To(gomega.Succeed())
			return createdPod.Finalizers
		}, Timeout, Interval).Should(gomega.BeEmpty(), "Expected pod to be finalized")
	}
}

func ExpectEventsForObjects(eventWatcher watch.Interface, objs sets.Set[types.NamespacedName], filter func(*corev1.Event) bool) {
	gotObjs := sets.New[types.NamespacedName]()
	timeoutCh := time.After(Timeout)
readCh:
	for !gotObjs.Equal(objs) {
		select {

		case evt, ok := <-eventWatcher.ResultChan():
			gomega.Expect(ok).To(gomega.BeTrue())
			event, ok := evt.Object.(*corev1.Event)
			gomega.Expect(ok).To(gomega.BeTrue())
			if filter(event) {
				objKey := types.NamespacedName{Namespace: event.InvolvedObject.Namespace, Name: event.InvolvedObject.Name}
				gotObjs.Insert(objKey)
			}
		case <-timeoutCh:
			break readCh
		}
	}
	gomega.ExpectWithOffset(1, gotObjs).To(gomega.Equal(objs))
}
