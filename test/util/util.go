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
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/go-logr/logr"
	"github.com/google/go-cmp/cmp/cmpopts"
	kfmpi "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	zaplog "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/component-base/metrics/testutil"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption"
	"sigs.k8s.io/kueue/pkg/util/slices"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
)

type objAsPtr[T any] interface {
	client.Object
	*T
}

func DeleteObject[PtrT objAsPtr[T], T any](ctx context.Context, c client.Client, o PtrT) error {
	if o != nil {
		if err := c.Delete(ctx, o); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func ExpectObjectToBeDeleted[PtrT objAsPtr[T], T any](ctx context.Context, k8sClient client.Client, o PtrT, deleteNow bool) {
	expectObjectToBeDeletedWithTimeout(ctx, k8sClient, o, deleteNow, Timeout)
}

func ExpectObjectToBeDeletedWithTimeout[PtrT objAsPtr[T], T any](ctx context.Context, k8sClient client.Client, o PtrT, deleteNow bool, timeout time.Duration) {
	expectObjectToBeDeletedWithTimeout(ctx, k8sClient, o, deleteNow, timeout)
}

func expectObjectToBeDeletedWithTimeout[PtrT objAsPtr[T], T any](ctx context.Context, k8sClient client.Client, o PtrT, deleteNow bool, timeout time.Duration) {
	if o == nil {
		return
	}
	if deleteNow {
		gomega.ExpectWithOffset(2, client.IgnoreNotFound(DeleteObject(ctx, k8sClient, o))).To(gomega.Succeed())
	}
	gomega.EventuallyWithOffset(2, func(g gomega.Gomega) {
		newObj := PtrT(new(T))
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(o), newObj)).Should(testing.BeNotFoundError())
	}, timeout, Interval).Should(gomega.Succeed())
}

// DeleteNamespace deletes all objects the tests typically create in the namespace.
func DeleteNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	if ns == nil {
		return nil
	}
	if err := DeleteAllJobsetsInNamespace(ctx, c, ns); err != nil {
		return err
	}
	if err := DeleteAllJobsInNamespace(ctx, c, ns); err != nil {
		return err
	}
	if err := c.DeleteAllOf(ctx, &appsv1.StatefulSet{}, client.InNamespace(ns.Name)); err != nil && !apierrors.IsNotFound(err) {
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
	if err != nil && !apierrors.IsNotFound(err) && !errors.Is(err, &apimeta.NoKindMatchError{}) {
		return err
	}
	return nil
}

func DeleteAllMPIJobsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	err := c.DeleteAllOf(ctx, &kfmpi.MPIJob{}, client.InNamespace(ns.Name), client.PropagationPolicy(metav1.DeletePropagationBackground))
	if err != nil && !apierrors.IsNotFound(err) && !errors.Is(err, &apimeta.NoKindMatchError{}) {
		return err
	}
	return nil
}

func DeleteAllPyTorchJobsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	err := c.DeleteAllOf(ctx, &kftraining.PyTorchJob{}, client.InNamespace(ns.Name), client.PropagationPolicy(metav1.DeletePropagationBackground))
	if err != nil && !apierrors.IsNotFound(err) && !errors.Is(err, &apimeta.NoKindMatchError{}) {
		return err
	}
	return nil
}

func DeleteAllPodsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	if err := client.IgnoreNotFound(c.DeleteAllOf(ctx, &corev1.Pod{}, client.InNamespace(ns.Name))); err != nil {
		return fmt.Errorf("deleting all Pods in namespace %q: %w", ns.Name, err)
	}

	gomega.Eventually(func(g gomega.Gomega) {
		lst := corev1.PodList{}
		g.Expect(client.IgnoreNotFound(c.List(ctx, &lst, client.InNamespace(ns.Name)))).
			Should(gomega.Succeed(), "listing Pods with a finalizer in namespace %q", ns.Name)
		for _, p := range lst.Items {
			if controllerutil.RemoveFinalizer(&p, pod.PodFinalizer) {
				g.Expect(client.IgnoreNotFound(c.Update(ctx, &p))).Should(gomega.Succeed(), "removing finalizer")
			}
		}
	}, LongTimeout, Interval).Should(gomega.Succeed())

	return nil
}

func DeleteWorkloadsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	if err := c.DeleteAllOf(ctx, &kueue.Workload{}, client.InNamespace(ns.Name)); err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	gomega.Eventually(func(g gomega.Gomega) {
		lst := kueue.WorkloadList{}
		g.Expect(c.List(ctx, &lst, client.InNamespace(ns.Name))).Should(gomega.Succeed())
		for _, wl := range lst.Items {
			if controllerutil.RemoveFinalizer(&wl, kueue.ResourceInUseFinalizerName) {
				g.Expect(client.IgnoreNotFound(c.Update(ctx, &wl))).Should(gomega.Succeed())
			}
		}
	}, LongTimeout, Interval).Should(gomega.Succeed())

	return nil
}

func UnholdClusterQueue(ctx context.Context, k8sClient client.Client, cq *kueue.ClusterQueue) {
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

func UnholdLocalQueue(ctx context.Context, k8sClient client.Client, lq *kueue.LocalQueue) {
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		var lqCopy kueue.LocalQueue
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lq), &lqCopy)).To(gomega.Succeed())
		if ptr.Deref(lqCopy.Spec.StopPolicy, kueue.None) == kueue.None {
			return
		}
		lqCopy.Spec.StopPolicy = ptr.To(kueue.None)
		g.Expect(k8sClient.Update(ctx, &lqCopy)).To(gomega.Succeed())
	}, Timeout, Interval).Should(gomega.Succeed())
}

func FinishWorkloads(ctx context.Context, k8sClient client.Client, workloads ...*kueue.Workload) {
	for _, w := range workloads {
		gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
			var newWL kueue.Workload
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(w), &newWL)).To(gomega.Succeed())
			newWL.Status.Conditions = append(w.Status.Conditions, metav1.Condition{
				Type:               kueue.WorkloadFinished,
				Status:             metav1.ConditionTrue,
				LastTransitionTime: metav1.Now(),
				Reason:             "ByTest",
				Message:            "Finished by test",
			})
			g.Expect(k8sClient.Status().Update(ctx, &newWL)).Should(gomega.Succeed())
		}, Timeout, Interval).Should(gomega.Succeed())
	}
}

func ExpectWorkloadsToHaveQuotaReservation(ctx context.Context, k8sClient client.Client, cqName string, wls ...*kueue.Workload) {
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		admitted := 0
		var updatedWorkload kueue.Workload
		for _, wl := range wls {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWorkload)).To(gomega.Succeed())
			if workload.HasQuotaReservation(&updatedWorkload) && string(updatedWorkload.Status.Admission.ClusterQueue) == cqName {
				admitted++
			}
		}
		g.Expect(admitted).Should(gomega.Equal(len(wls)), "Not enough workloads were admitted")
	}, Timeout, Interval).Should(gomega.Succeed())
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
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		pending := 0
		var updatedWorkload kueue.Workload
		for _, wl := range wls {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWorkload)).To(gomega.Succeed())
			cond := apimeta.FindStatusCondition(updatedWorkload.Status.Conditions, kueue.WorkloadQuotaReserved)
			if cond == nil {
				continue
			}
			if cond.Status == metav1.ConditionFalse && cond.Reason == "Pending" {
				pending++
			}
		}
		g.Expect(pending).Should(gomega.Equal(len(wls)), "Not enough workloads are pending")
	}, Timeout, Interval).Should(gomega.Succeed())
}

func ExpectWorkloadsToBeAdmitted(ctx context.Context, k8sClient client.Client, wls ...*kueue.Workload) {
	expectWorkloadsToBeAdmittedCountWithOffset(ctx, 2, k8sClient, len(wls), wls...)
}

func ExpectWorkloadsToBeAdmittedCount(ctx context.Context, k8sClient client.Client, count int, wls ...*kueue.Workload) {
	expectWorkloadsToBeAdmittedCountWithOffset(ctx, 2, k8sClient, count, wls...)
}

func expectWorkloadsToBeAdmittedCountWithOffset(ctx context.Context, offset int, k8sClient client.Client, count int, wls ...*kueue.Workload) {
	gomega.EventuallyWithOffset(offset, func(g gomega.Gomega) {
		admitted := 0
		var updatedWorkload kueue.Workload
		for _, wl := range wls {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWorkload)).To(gomega.Succeed())
			if apimeta.IsStatusConditionTrue(updatedWorkload.Status.Conditions, kueue.WorkloadAdmitted) {
				admitted++
			}
		}
		g.Expect(admitted).Should(gomega.Equal(count), "Not enough workloads are admitted")
	}, Timeout, Interval).Should(gomega.Succeed())
}

func ExpectWorkloadToFinish(ctx context.Context, k8sClient client.Client, wlKey client.ObjectKey) {
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		var wl kueue.Workload
		g.Expect(k8sClient.Get(ctx, wlKey, &wl)).To(gomega.Succeed())
		g.Expect(wl.Status.Conditions).To(testing.HaveConditionStatusTrue(kueue.WorkloadFinished), "it's finished")
	}, LongTimeout, Interval).Should(gomega.Succeed())
}

func AwaitWorkloadEvictionByPodsReadyTimeout(ctx context.Context, k8sClient client.Client, wlKey client.ObjectKey, sleep time.Duration) {
	if sleep > 0 {
		time.Sleep(sleep)
		ginkgo.By(fmt.Sprintf("exceeded the timeout %q for the %q workload", sleep.String(), wlKey.String()))
	}
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		var wl kueue.Workload
		g.Expect(k8sClient.Get(ctx, wlKey, &wl)).Should(gomega.Succeed())
		g.Expect(wl.Status.Conditions).Should(gomega.ContainElements(gomega.BeComparableTo(metav1.Condition{
			Type:    kueue.WorkloadEvicted,
			Status:  metav1.ConditionTrue,
			Reason:  kueue.WorkloadEvictedByPodsReadyTimeout,
			Message: fmt.Sprintf("Exceeded the PodsReady timeout %s", klog.KObj(&wl).String()),
		}, IgnoreConditionTimestampsAndObservedGeneration)))
	}, Timeout, Interval).Should(gomega.Succeed())
}

func SetRequeuedConditionWithPodsReadyTimeout(ctx context.Context, k8sClient client.Client, wlKey client.ObjectKey) {
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		var wl kueue.Workload
		g.Expect(k8sClient.Get(ctx, wlKey, &wl)).Should(gomega.Succeed())
		workload.SetRequeuedCondition(&wl, kueue.WorkloadEvictedByPodsReadyTimeout,
			fmt.Sprintf("Exceeded the PodsReady timeout %s", klog.KObj(&wl).String()), false)
		g.Expect(workload.ApplyAdmissionStatus(ctx, k8sClient, &wl, true)).Should(gomega.Succeed())
	}, Timeout, Interval).Should(gomega.Succeed())
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
	}, Timeout, Interval).Should(gomega.Succeed())
}

func ExpectWorkloadsToBePreempted(ctx context.Context, k8sClient client.Client, wls ...*kueue.Workload) {
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		preempted := 0
		var updatedWorkload kueue.Workload
		for _, wl := range wls {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWorkload)).To(gomega.Succeed())
			cond := apimeta.FindStatusCondition(updatedWorkload.Status.Conditions, kueue.WorkloadEvicted)
			if cond == nil {
				continue
			}
			if cond.Status == metav1.ConditionTrue {
				preempted++
			}
		}
		g.Expect(preempted).Should(gomega.Equal(len(wls)), "Not enough workloads are preempted")
	}, Timeout, Interval).Should(gomega.Succeed())
}

func ExpectWorkloadsToBeWaiting(ctx context.Context, k8sClient client.Client, wls ...*kueue.Workload) {
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		pending := 0
		var updatedWorkload kueue.Workload
		for _, wl := range wls {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWorkload)).To(gomega.Succeed())
			cond := apimeta.FindStatusCondition(updatedWorkload.Status.Conditions, kueue.WorkloadQuotaReserved)
			if cond == nil {
				continue
			}
			if cond.Status == metav1.ConditionFalse && cond.Reason == "Waiting" {
				pending++
			}
		}
		g.Expect(pending).Should(gomega.Equal(len(wls)), "Not enough workloads are waiting")
	}, Timeout, Interval).Should(gomega.Succeed())
}

func ExpectWorkloadsToBeFrozen(ctx context.Context, k8sClient client.Client, cq string, wls ...*kueue.Workload) {
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		frozen := 0
		var updatedWorkload kueue.Workload
		for _, wl := range wls {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWorkload)).To(gomega.Succeed())
			cond := apimeta.FindStatusCondition(updatedWorkload.Status.Conditions, kueue.WorkloadQuotaReserved)
			if cond == nil {
				continue
			}
			msg := fmt.Sprintf("ClusterQueue %s is inactive", cq)
			if cond.Status == metav1.ConditionFalse && cond.Reason == "Inadmissible" && cond.Message == msg {
				frozen++
			}
		}
		g.Expect(frozen).Should(gomega.Equal(len(wls)), "Not enough workloads are frozen")
	}, Timeout, Interval).Should(gomega.Succeed())
}

func ExpectWorkloadToBeAdmittedAs(ctx context.Context, k8sClient client.Client, wl *kueue.Workload, admission *kueue.Admission) {
	var updatedWorkload kueue.Workload
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWorkload)).To(gomega.Succeed())
		g.Expect(updatedWorkload.Status.Admission).Should(gomega.BeComparableTo(admission))
	}, Timeout, Interval).Should(gomega.Succeed())
}

var attemptStatuses = []metrics.AdmissionResult{metrics.AdmissionResultInadmissible, metrics.AdmissionResultSuccess}

func ExpectAdmissionAttemptsMetric(pending, admitted int) {
	vals := []int{pending, admitted}

	for i, status := range attemptStatuses {
		metric := metrics.AdmissionAttemptsTotal.WithLabelValues(string(status))
		gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
			v, err := testutil.GetCounterMetricValue(metric)
			g.Expect(err).ToNot(gomega.HaveOccurred())
			g.Expect(int(v)).Should(gomega.Equal(vals[i]), "pending_workloads with status=%s", status)
		}, Timeout, Interval).Should(gomega.Succeed())
	}
}

var pendingStatuses = []string{metrics.PendingStatusActive, metrics.PendingStatusInadmissible}

func ExpectLQPendingWorkloadsMetric(lq *kueue.LocalQueue, active, inadmissible int) {
	vals := []int{active, inadmissible}
	for i, status := range pendingStatuses {
		metric := metrics.LocalQueuePendingWorkloads.WithLabelValues(lq.Name, lq.Namespace, status)
		gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
			v, err := testutil.GetGaugeMetricValue(metric)
			g.Expect(err).ToNot(gomega.HaveOccurred())
			g.Expect(int(v)).Should(gomega.Equal(vals[i]), "pending_workloads with status=%s", status)
		}, Timeout, Interval).Should(gomega.Succeed())
	}
}

func ExpectLQReservingActiveWorkloadsMetric(lq *kueue.LocalQueue, value int) {
	metric := metrics.LocalQueueReservingActiveWorkloads.WithLabelValues(lq.Name, lq.Namespace)
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		v, err := testutil.GetGaugeMetricValue(metric)
		g.Expect(err).ToNot(gomega.HaveOccurred())
		g.Expect(int(v)).To(gomega.Equal(value))
	}, Timeout, Interval).Should(gomega.Succeed())
}

func ExpectLQAdmittedWorkloadsTotalMetric(lq *kueue.LocalQueue, value int) {
	metric := metrics.LocalQueueAdmittedWorkloadsTotal.WithLabelValues(lq.Name, lq.Namespace)
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		v, err := testutil.GetCounterMetricValue(metric)
		g.Expect(err).ToNot(gomega.HaveOccurred())
		g.Expect(int(v)).Should(gomega.Equal(value))
	}, Timeout, Interval).Should(gomega.Succeed())
}

func ExpectLQByStatusMetric(lq *kueue.LocalQueue, status metav1.ConditionStatus) {
	for i, s := range metrics.ConditionStatusValues {
		var wantV float64
		if metrics.ConditionStatusValues[i] == status {
			wantV = 1
		}
		metric := metrics.LocalQueueByStatus.WithLabelValues(lq.Name, lq.Namespace, string(s))
		gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
			v, err := testutil.GetGaugeMetricValue(metric)
			g.Expect(err).ToNot(gomega.HaveOccurred())
			g.Expect(v).Should(gomega.Equal(wantV), "local_queue_status with status=%s", s)
		}, Timeout, Interval).Should(gomega.Succeed())
	}
}

func ExpectPendingWorkloadsMetric(cq *kueue.ClusterQueue, active, inadmissible int) {
	vals := []int{active, inadmissible}
	for i, status := range pendingStatuses {
		metric := metrics.PendingWorkloads.WithLabelValues(cq.Name, status)
		gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
			v, err := testutil.GetGaugeMetricValue(metric)
			g.Expect(err).ToNot(gomega.HaveOccurred())
			g.Expect(int(v)).Should(gomega.Equal(vals[i]), "pending_workloads with status=%s", status)
		}, Timeout, Interval).Should(gomega.Succeed())
	}
}

func ExpectReservingActiveWorkloadsMetric(cq *kueue.ClusterQueue, value int) {
	metric := metrics.ReservingActiveWorkloads.WithLabelValues(cq.Name)
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		v, err := testutil.GetGaugeMetricValue(metric)
		g.Expect(err).ToNot(gomega.HaveOccurred())
		g.Expect(int(v)).To(gomega.Equal(value))
	}, Timeout, Interval).Should(gomega.Succeed())
}

func ExpectAdmittedWorkloadsTotalMetric(cq *kueue.ClusterQueue, v int) {
	metric := metrics.AdmittedWorkloadsTotal.WithLabelValues(cq.Name)
	expectCounterMetric(metric, v)
}

func ExpectEvictedWorkloadsTotalMetric(cqName, reason string, v int) {
	metric := metrics.EvictedWorkloadsTotal.WithLabelValues(cqName, reason)
	expectCounterMetric(metric, v)
}

func ExpectPreemptedWorkloadsTotalMetric(preemptorCqName, reason string, v int) {
	metric := metrics.PreemptedWorkloadsTotal.WithLabelValues(preemptorCqName, reason)
	expectCounterMetric(metric, v)
}

func ExpectQuotaReservedWorkloadsTotalMetric(cq *kueue.ClusterQueue, v int) {
	metric := metrics.QuotaReservedWorkloadsTotal.WithLabelValues(cq.Name)
	expectCounterMetric(metric, v)
}

func expectCounterMetric(metric prometheus.Counter, count int) {
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		v, err := testutil.GetCounterMetricValue(metric)
		g.Expect(err).ToNot(gomega.HaveOccurred())
		g.Expect(int(v)).Should(gomega.Equal(count))
	}, Timeout, Interval).Should(gomega.Succeed())
}

func ExpectClusterQueueStatusMetric(cq *kueue.ClusterQueue, status metrics.ClusterQueueStatus) {
	for i, s := range metrics.CQStatuses {
		var wantV float64
		if metrics.CQStatuses[i] == status {
			wantV = 1
		}
		metric := metrics.ClusterQueueByStatus.WithLabelValues(cq.Name, string(s))
		gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
			v, err := testutil.GetGaugeMetricValue(metric)
			g.Expect(err).ToNot(gomega.HaveOccurred())
			g.Expect(v).Should(gomega.Equal(wantV), "cluster_queue_status with status=%s", s)
		}, Timeout, Interval).Should(gomega.Succeed())
	}
}

func ExpectClusterQueueWeightedShareMetric(cq *kueue.ClusterQueue, value int64) {
	metric := metrics.ClusterQueueWeightedShare.WithLabelValues(cq.Name)
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		v, err := testutil.GetGaugeMetricValue(metric)
		g.Expect(err).ToNot(gomega.HaveOccurred())
		g.Expect(int64(v)).Should(gomega.Equal(value))
	}, Timeout, Interval).Should(gomega.Succeed())
}

func ExpectCQResourceNominalQuota(cq *kueue.ClusterQueue, flavor, resource string, value float64) {
	metric := metrics.ClusterQueueResourceNominalQuota.WithLabelValues(cq.Spec.Cohort, cq.Name, flavor, resource)
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		v, err := testutil.GetGaugeMetricValue(metric)
		g.Expect(err).ToNot(gomega.HaveOccurred())
		g.Expect(v).Should(gomega.Equal(value))
	}, Timeout, Interval).Should(gomega.Succeed())
}

func ExpectCQResourceBorrowingQuota(cq *kueue.ClusterQueue, flavor, resource string, value float64) {
	metric := metrics.ClusterQueueResourceBorrowingLimit.WithLabelValues(cq.Spec.Cohort, cq.Name, flavor, resource)
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		v, err := testutil.GetGaugeMetricValue(metric)
		g.Expect(err).ToNot(gomega.HaveOccurred())
		g.Expect(v).Should(gomega.Equal(value))
	}, Timeout, Interval).Should(gomega.Succeed())
}

func ExpectCQResourceReservations(cq *kueue.ClusterQueue, flavor, resource string, value float64) {
	metric := metrics.ClusterQueueResourceReservations.WithLabelValues(cq.Spec.Cohort, cq.Name, flavor, resource)
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		v, err := testutil.GetGaugeMetricValue(metric)
		g.Expect(err).ToNot(gomega.HaveOccurred())
		g.Expect(v).Should(gomega.Equal(value))
	}, Timeout, Interval).Should(gomega.Succeed())
}

func SetQuotaReservation(ctx context.Context, k8sClient client.Client, wl *kueue.Workload, admission *kueue.Admission) error {
	wl = wl.DeepCopy()
	if admission == nil {
		workload.UnsetQuotaReservationWithCondition(wl, "EvictedByTest", "Evicted By Test", time.Now())
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
		if workload.SyncAdmittedCondition(&updatedWorkload, time.Now()) {
			gomega.ExpectWithOffset(1, workload.ApplyAdmissionStatus(ctx, k8sClient, &updatedWorkload, false)).To(gomega.Succeed())
		}
	}
}

func FinishEvictionForWorkloads(ctx context.Context, k8sClient client.Client, wls ...*kueue.Workload) {
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		evicting := 0
		var updatedWorkload kueue.Workload
		for _, wl := range wls {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWorkload)).To(gomega.Succeed())
			if cond := apimeta.FindStatusCondition(updatedWorkload.Status.Conditions, kueue.WorkloadEvicted); cond != nil && cond.Status == metav1.ConditionTrue {
				evicting++
			}
		}
		g.Expect(evicting).Should(gomega.Equal(len(wls)), "Not enough workloads were marked for eviction")
	}, Timeout, Interval).Should(gomega.Succeed())
	// unset the quota reservation
	for i := range wls {
		key := client.ObjectKeyFromObject(wls[i])
		gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
			var updatedWorkload kueue.Workload
			g.Expect(k8sClient.Get(ctx, key, &updatedWorkload)).Should(gomega.Succeed())
			if apimeta.IsStatusConditionTrue(updatedWorkload.Status.Conditions, kueue.WorkloadQuotaReserved) {
				workload.UnsetQuotaReservationWithCondition(&updatedWorkload, "Pending", "By test", time.Now())
				g.Expect(workload.ApplyAdmissionStatus(ctx, k8sClient, &updatedWorkload, true)).Should(
					gomega.Succeed(),
					fmt.Sprintf("Unable to unset quota reservation for %q", key),
				)
			}
		}, Timeout, Interval).Should(gomega.Succeed())
	}
}

func SetAdmissionCheckActive(ctx context.Context, k8sClient client.Client, admissionCheck *kueue.AdmissionCheck, status metav1.ConditionStatus) {
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		var updatedAc kueue.AdmissionCheck
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(admissionCheck), &updatedAc)).Should(gomega.Succeed())
		apimeta.SetStatusCondition(&updatedAc.Status.Conditions, metav1.Condition{
			Type:    kueue.AdmissionCheckActive,
			Status:  status,
			Reason:  "ByTest",
			Message: "by test",
		})
		g.Expect(k8sClient.Status().Update(ctx, &updatedAc)).Should(gomega.Succeed())
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
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		g.Expect(client.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
		g.Expect(createdWorkload.Spec.QueueName).Should(gomega.Equal(jobQueueName))
	}, Timeout, Interval).Should(gomega.Succeed())
}

func AwaitAndVerifyCreatedWorkload(ctx context.Context, client client.Client, wlLookupKey types.NamespacedName, createdJob metav1.Object) *kueue.Workload {
	createdWorkload := &kueue.Workload{}
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		g.Expect(client.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
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

func BindPodWithNode(ctx context.Context, k8sClient client.Client, nodeName string, pods ...*corev1.Pod) {
	for _, p := range pods {
		updatedPod := corev1.Pod{}
		gomega.ExpectWithOffset(1, k8sClient.Get(ctx, client.ObjectKeyFromObject(p), &updatedPod)).To(gomega.Succeed())
		binding := corev1.Binding{
			Target: corev1.ObjectReference{
				Kind: "Node",
				Name: nodeName,
			},
		}
		gomega.ExpectWithOffset(1, k8sClient.SubResource("binding").Create(ctx, &updatedPod, &binding)).To(gomega.Succeed())
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

func ExpectPodsJustFinalized(ctx context.Context, k8sClient client.Client, keys ...types.NamespacedName) {
	for _, key := range keys {
		createdPod := &corev1.Pod{}
		gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, key, createdPod)).To(gomega.Succeed())
			g.Expect(createdPod.Finalizers).Should(gomega.BeEmpty(), "Expected pod to be finalized")
		}, Timeout, Interval).Should(gomega.Succeed())
	}
}

func ExpectPodsFinalizedOrGone(ctx context.Context, k8sClient client.Client, keys ...types.NamespacedName) {
	for _, key := range keys {
		createdPod := &corev1.Pod{}
		gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
			err := k8sClient.Get(ctx, key, createdPod)
			// Skip further checks to avoid verifying finalizers on the old Pod object.
			if apierrors.IsNotFound(err) {
				return
			}
			g.Expect(err).To(gomega.Succeed())
			g.Expect(createdPod.Finalizers).Should(gomega.BeEmpty(), "Expected pod to be finalized")
		}, Timeout, Interval).Should(gomega.Succeed())
	}
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
			g.Expect(createdWorkload.Finalizers).Should(gomega.BeEmpty(), "Expected workload to be finalized")
		}, Timeout, Interval).Should(gomega.Succeed())
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

func ExpectPreemptedCondition(ctx context.Context, k8sClient client.Client, reason string, status metav1.ConditionStatus, preemptedWl, preempteeWl *kueue.Workload) {
	conditionCmpOpts := cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime", "ObservedGeneration")
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(preemptedWl), preemptedWl)).To(gomega.Succeed())
		g.Expect(preemptedWl.Status.Conditions).To(gomega.ContainElements(gomega.BeComparableTo(metav1.Condition{
			Type:    kueue.WorkloadPreempted,
			Status:  status,
			Reason:  reason,
			Message: fmt.Sprintf("Preempted to accommodate a workload (UID: %s) due to %s", preempteeWl.UID, preemption.HumanReadablePreemptionReasons[reason]),
		}, conditionCmpOpts)))
	}, Timeout, Interval).Should(gomega.Succeed())
}

func NewTestingLogger(writer io.Writer, level int) logr.Logger {
	opts := func(o *zap.Options) {
		o.TimeEncoder = zapcore.RFC3339NanoTimeEncoder
		o.ZapOpts = []zaplog.Option{zaplog.AddCaller()}
	}
	return zap.New(
		zap.WriteTo(writer),
		zap.UseDevMode(true),
		zap.Level(zapcore.Level(level)),
		opts)
}

// WaitForNextSecondAfterCreation wait time between the start of the next second
// after creationTimestamp and the current time to ensure that the new
// created object has a later creation time.
func WaitForNextSecondAfterCreation(obj client.Object) {
	time.Sleep(time.Until(obj.GetCreationTimestamp().Add(time.Second)))
}

func ExpectClusterQueuesToBeActive(ctx context.Context, c client.Client, cqs ...*kueue.ClusterQueue) {
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		readCq := &kueue.ClusterQueue{}
		for _, cq := range cqs {
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(cq), readCq)).To(gomega.Succeed())
			cond := apimeta.FindStatusCondition(readCq.Status.Conditions, kueue.ClusterQueueActive)
			g.Expect(cond).NotTo(gomega.BeNil(), "no %q condition found in %q cq status", kueue.ClusterQueueActive, cq.Name)
			g.Expect(cond.Status).To(gomega.Equal(metav1.ConditionTrue), "%q is not active status: %q message: %q", cq.Name, cond.Status, cond.Message)
		}
	}, Timeout, Interval).Should(gomega.Succeed())
}

func ExpectLocalQueuesToBeActive(ctx context.Context, c client.Client, lqs ...*kueue.LocalQueue) {
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		readLq := &kueue.LocalQueue{}
		for _, lq := range lqs {
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(lq), readLq)).To(gomega.Succeed())
			cond := apimeta.FindStatusCondition(readLq.Status.Conditions, kueue.LocalQueueActive)
			g.Expect(cond).NotTo(gomega.BeNil(), "no %q condition found in %q cq status", kueue.LocalQueueActive, lq.Name)
			g.Expect(cond.Status).To(gomega.Equal(metav1.ConditionTrue), "%q is not active status: %q message: %q", lq.Name, cond.Status, cond.Message)
		}
	}, Timeout, Interval).Should(gomega.Succeed())
}

func CreateNodes(ctx context.Context, c client.Client, nodes []corev1.Node) {
	for _, node := range nodes {
		// 1. Create a node
		gomega.ExpectWithOffset(1, c.Create(ctx, &node)).Should(gomega.Succeed())

		// 2. Update status based on the object
		createdNode := &corev1.Node{}
		gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(&node), createdNode)).Should(gomega.Succeed())
			createdNode.Status = node.Status
			g.Expect(c.Status().Update(ctx, createdNode)).Should(gomega.Succeed())
		}, Timeout, Interval).Should(gomega.Succeed())

		// 3. Removes the taint if the node is Ready
		gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(&node), createdNode)).Should(gomega.Succeed())
			if utiltas.IsNodeStatusConditionTrue(createdNode.Status.Conditions, corev1.NodeReady) {
				createdNode.Spec.Taints = slices.DeleteFunc(createdNode.Spec.Taints, func(taint corev1.Taint) bool {
					return taint.Key == corev1.TaintNodeNotReady
				})
				g.Expect(c.Update(ctx, createdNode)).Should(gomega.Succeed())
			}
		}, Timeout, Interval).Should(gomega.Succeed())
	}
}

func NewNamespaceSelectorExcluding(unmanaged ...string) labels.Selector {
	unmanaged = append(unmanaged, "kube-system", "kueue_system")
	ls := &metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{
			{
				Key:      "kubernetes.io/metadata.name",
				Operator: metav1.LabelSelectorOpNotIn,
				Values:   unmanaged,
			},
		},
	}
	sel, err := metav1.LabelSelectorAsSelector(ls)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return sel
}
