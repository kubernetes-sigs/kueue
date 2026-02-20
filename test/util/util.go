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
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/google/go-cmp/cmp/cmpopts"
	kfmpi "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	kftrainerapi "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"github.com/onsi/gomega/format"
	awv1beta2 "github.com/project-codeflare/appwrapper/api/v1beta2"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	zaplog "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/klog/v2"
	testingclock "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobs/leaderworkerset"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	utillogging "sigs.k8s.io/kueue/pkg/util/logging"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

func init() {
	// Use large MaxLength to make sure the diff contains relevant output
	format.MaxLength = 500000
}

var SetupLogger = sync.OnceFunc(func() {
	ctrl.SetLogger(NewTestingLogger(ginkgo.GinkgoWriter))
})

var SetupLoggerGetObservedLogs = sync.OnceValue(func() *observer.ObservedLogs {
	logger, observedLogs := NewTestingLoggerAndObservedLogs(ginkgo.GinkgoWriter)
	ctrl.SetLogger(logger)
	return observedLogs
})

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
	expectObjectToBeDeletedWithTimeout(ctx, k8sClient, o, deleteNow, LongTimeout)
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
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(o), newObj)).Should(utiltesting.BeNotFoundError())
	}, timeout, Interval).Should(gomega.Succeed())
}

// DeleteNamespace deletes all objects the tests typically create in the namespace.
func DeleteNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	if ns == nil {
		return nil
	}
	if err := DeleteAllAppWrappersInNamespace(ctx, c, ns); err != nil {
		return err
	}
	if err := DeleteAllJobSetsInNamespace(ctx, c, ns); err != nil {
		return err
	}
	if err := DeleteAllJobsInNamespace(ctx, c, ns); err != nil {
		return err
	}
	if err := DeleteAllRayJobsInNamespace(ctx, c, ns); err != nil {
		return err
	}
	if err := DeleteAllTrainingRuntimesInNamespace(ctx, c, ns); err != nil {
		return err
	}
	if err := DeleteAllMPIJobsInNamespace(ctx, c, ns); err != nil {
		return err
	}
	if err := c.DeleteAllOf(ctx, &appsv1.StatefulSet{}, client.InNamespace(ns.Name)); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err := c.DeleteAllOf(ctx, &kueue.LocalQueue{}, client.InNamespace(ns.Name)); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err := c.DeleteAllOf(ctx, &corev1.ConfigMap{}, client.InNamespace(ns.Name)); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err := deleteAllPodsInNamespace(ctx, c, ns, 2); err != nil {
		return err
	}
	if err := deleteWorkloadsInNamespace(ctx, c, ns, 2); err != nil {
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

func DeleteAllCronJobsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	return deleteAllObjectsInNamespace(ctx, c, ns, &batchv1.CronJob{})
}

func DeleteAllJobsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	return deleteAllObjectsInNamespace(ctx, c, ns, &batchv1.Job{})
}

func DeleteAllJobSetsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	return deleteAllObjectsInNamespace(ctx, c, ns, &jobset.JobSet{})
}

func DeleteAllTrainJobsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	return deleteAllObjectsInNamespace(ctx, c, ns, &kftrainerapi.TrainJob{})
}

func DeleteAllTrainingRuntimesInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	return deleteAllObjectsInNamespace(ctx, c, ns, &kftrainerapi.TrainingRuntime{})
}

func DeleteAllLeaderWorkerSetsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	return deleteAllObjectsInNamespace(ctx, c, ns, &leaderworkersetv1.LeaderWorkerSet{})
}

func DeleteAllMPIJobsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	return deleteAllObjectsInNamespace(ctx, c, ns, &kfmpi.MPIJob{})
}

func DeleteAllPyTorchJobsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	return deleteAllObjectsInNamespace(ctx, c, ns, &kftraining.PyTorchJob{})
}

func DeleteAllJAXJobsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	return deleteAllObjectsInNamespace(ctx, c, ns, &kftraining.JAXJob{})
}

func DeleteAllAppWrappersInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	return deleteAllObjectsInNamespace(ctx, c, ns, &awv1beta2.AppWrapper{})
}

func DeleteAllRayJobsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	return deleteAllObjectsInNamespace(ctx, c, ns, &rayv1.RayJob{})
}

func DeleteAllPodsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	return deleteAllPodsInNamespace(ctx, c, ns, 2)
}

func deleteAllObjectsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace, obj client.Object) error {
	err := c.DeleteAllOf(ctx, obj, client.InNamespace(ns.Name), client.PropagationPolicy(metav1.DeletePropagationBackground))
	if err != nil && !apierrors.IsNotFound(err) && !errors.Is(err, &apimeta.NoKindMatchError{}) {
		return err
	}
	return nil
}

func deleteAllPodsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace, offset int) error {
	if err := client.IgnoreNotFound(c.DeleteAllOf(ctx, &corev1.Pod{}, client.InNamespace(ns.Name))); err != nil {
		return fmt.Errorf("deleting all Pods in namespace %q: %w", ns.Name, err)
	}
	gomega.EventuallyWithOffset(offset, func(g gomega.Gomega) {
		pods := corev1.PodList{}
		g.Expect(client.IgnoreNotFound(c.List(ctx, &pods, client.InNamespace(ns.Name)))).
			Should(gomega.Succeed(), "listing Pods with a finalizer in namespace %q", ns.Name)
		for _, p := range pods.Items {
			if controllerutil.RemoveFinalizer(&p, podconstants.PodFinalizer) {
				g.Expect(client.IgnoreNotFound(c.Update(ctx, &p))).Should(gomega.Succeed(), "removing finalizer")
			}
		}
	}, LongTimeout, Interval).Should(gomega.Succeed())
	return nil
}

func ExpectAllPodsInNamespaceDeleted(ctx context.Context, c client.Client, ns *corev1.Namespace) {
	ginkgo.GinkgoHelper()
	pods := corev1.PodList{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(c.List(ctx, &pods, client.InNamespace(ns.Name))).Should(gomega.Succeed())
		g.Expect(pods.Items).Should(gomega.BeEmpty())
	}, LongTimeout, Interval).Should(gomega.Succeed())
}

func DeleteWorkloadsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	return deleteWorkloadsInNamespace(ctx, c, ns, 2)
}

func deleteWorkloadsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace, offset int) error {
	if err := c.DeleteAllOf(ctx, &kueue.Workload{}, client.InNamespace(ns.Name)); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	gomega.EventuallyWithOffset(offset, func(g gomega.Gomega) {
		workloads := kueue.WorkloadList{}
		g.Expect(c.List(ctx, &workloads, client.InNamespace(ns.Name))).Should(gomega.Succeed())
		for _, wl := range workloads.Items {
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
	ginkgo.GinkgoHelper()
	wlKeys := workloadKeys(wls)
	ExpectWorkloadsToHaveQuotaReservationByKey(ctx, k8sClient, cqName, wlKeys...)
}

func ExpectWorkloadsToHaveQuotaReservationByKey(ctx context.Context, k8sClient client.Client, cqName string, wlKeys ...client.ObjectKey) {
	ginkgo.GinkgoHelper()
	wlKeys = uniqueKeys(wlKeys)
	wl := &kueue.Workload{}
	gomega.Eventually(func(g gomega.Gomega) {
		admitted := make([]client.ObjectKey, 0, len(wlKeys))
		for _, wlKey := range wlKeys {
			g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
			if workload.HasQuotaReservation(wl) && string(wl.Status.Admission.ClusterQueue) == cqName {
				admitted = append(admitted, wlKey)
			}
		}
		g.Expect(admitted).Should(gomega.Equal(wlKeys), "Unexpected workloads were admitted")
	}, Timeout, Interval).Should(gomega.Succeed())
}

func FilterEvictedWorkloads(ctx context.Context, k8sClient client.Client, wls ...*kueue.Workload) []*kueue.Workload {
	return filterWorkloads(ctx, k8sClient, workload.IsEvicted, wls...)
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

func ExpectWorkloadsToBePendingByKeys(ctx context.Context, k8sClient client.Client, wlKeys ...client.ObjectKey) {
	ginkgo.GinkgoHelper()
	wlKeys = uniqueKeys(wlKeys)
	wl := &kueue.Workload{}
	gomega.Eventually(func(g gomega.Gomega) {
		pending := make([]client.ObjectKey, 0, len(wlKeys))
		for _, wlKey := range wlKeys {
			g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
			cond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
			if cond != nil && cond.Status == metav1.ConditionFalse && cond.Reason == "Pending" {
				pending = append(pending, wlKey)
			}
		}
		g.Expect(pending).Should(gomega.Equal(wlKeys), "Unexpected workloads are pending")
	}, Timeout, Interval).Should(gomega.Succeed())
}

func admittedWorkloadKeys(ctx context.Context, k8sClient client.Client, wlKeys []client.ObjectKey) ([]client.ObjectKey, error) {
	admitted := make([]client.ObjectKey, 0, len(wlKeys))
	var updatedWorkload kueue.Workload
	for _, wl := range wlKeys {
		if err := k8sClient.Get(ctx, wl, &updatedWorkload); err != nil {
			return nil, err
		}
		if workload.IsAdmitted(&updatedWorkload) {
			admitted = append(admitted, wl)
		}
	}
	return admitted, nil
}

func ExpectWorkloadsToBeAdmitted(ctx context.Context, k8sClient client.Client, wls ...*kueue.Workload) {
	ginkgo.GinkgoHelper()
	wlKeys := workloadKeys(wls)
	ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sClient, wlKeys...)
}

func ExpectWorkloadsToBeAdmittedByKeys(ctx context.Context, k8sClient client.Client, wlKeys ...client.ObjectKey) {
	ginkgo.GinkgoHelper()
	wlKeys = uniqueKeys(wlKeys)
	gomega.Eventually(func(g gomega.Gomega) {
		admitted, err := admittedWorkloadKeys(ctx, k8sClient, wlKeys)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(admitted).Should(gomega.Equal(wlKeys), "Unexpected workloads are admitted")
	}, Timeout, Interval).Should(gomega.Succeed())
}

func ExpectWorkloadsToBeAdmittedCount(ctx context.Context, k8sClient client.Client, count int, wls ...*kueue.Workload) {
	ginkgo.GinkgoHelper()
	wlKeys := uniqueKeys(workloadKeys(wls))
	gomega.Eventually(func(g gomega.Gomega) {
		admitted, err := admittedWorkloadKeys(ctx, k8sClient, wlKeys)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(admitted).Should(gomega.HaveLen(count), "Not enough workloads are admitted from the list: %v", wlKeys)
	}, Timeout, Interval).Should(gomega.Succeed())
}

func ExpectWorkloadsWithWorkloadPriority(ctx context.Context, c client.Client, name string, value int32, wlKeys ...client.ObjectKey) {
	ginkgo.GinkgoHelper()
	expectWorkloadsWithPriority(ctx, c, kueue.WorkloadPriorityClassGroup, kueue.WorkloadPriorityClassKind, name, value, wlKeys...)
}

func ExpectWorkloadsWithPodPriority(ctx context.Context, c client.Client, name string, value int32, wlKeys ...client.ObjectKey) {
	ginkgo.GinkgoHelper()
	expectWorkloadsWithPriority(ctx, c, kueue.PodPriorityClassGroup, kueue.PodPriorityClassKind, name, value, wlKeys...)
}

func expectWorkloadsWithPriority(ctx context.Context, c client.Client, priorityClassGroup kueue.PriorityClassGroup, priorityClassKind kueue.PriorityClassKind, name string, value int32, wlKeys ...client.ObjectKey) {
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
	}, Timeout, Interval).Should(gomega.Succeed())
}

func ExpectWorkloadToFinish(ctx context.Context, k8sClient client.Client, wlKey client.ObjectKey) {
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		var wl kueue.Workload
		g.Expect(k8sClient.Get(ctx, wlKey, &wl)).To(gomega.Succeed())
		g.Expect(wl.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadFinished), "it's finished")
	}, LongTimeout, Interval).Should(gomega.Succeed())
}

func ExpectPodsReadyCondition(ctx context.Context, k8sClient client.Client, wlKey client.ObjectKey) {
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		var wl kueue.Workload
		g.Expect(k8sClient.Get(ctx, wlKey, &wl)).To(gomega.Succeed())
		g.Expect(wl.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadPodsReady), "pods are ready")
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
		g.Expect(workload.PatchAdmissionStatus(ctx, k8sClient, &wl, RealClock, func(wl *kueue.Workload) (bool, error) {
			return workload.SetRequeuedCondition(wl, kueue.WorkloadEvictedByPodsReadyTimeout, fmt.Sprintf("Exceeded the PodsReady timeout %s", klog.KObj(wl).String()), false), nil
		})).Should(gomega.Succeed())
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
	ginkgo.GinkgoHelper()
	wlKeys := workloadKeys(wls)
	ExpectWorkloadsToBePreemptedByKeys(ctx, k8sClient, wlKeys...)
}

func ExpectWorkloadsToBePreemptedByKeys(ctx context.Context, k8sClient client.Client, wlKeys ...client.ObjectKey) {
	ginkgo.GinkgoHelper()
	wlKeys = uniqueKeys(wlKeys)
	wl := &kueue.Workload{}
	gomega.Eventually(func(g gomega.Gomega) {
		preempted := make([]client.ObjectKey, 0, len(wlKeys))
		for _, wlKey := range wlKeys {
			g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
			cond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadEvicted)
			if cond != nil && cond.Status == metav1.ConditionTrue && cond.Reason == kueue.WorkloadPreempted {
				preempted = append(preempted, wlKey)
			}
		}
		g.Expect(preempted).Should(gomega.Equal(wlKeys), "Unexpected workloads are preempted")
	}, Timeout, Interval).Should(gomega.Succeed())
}

func ExpectWorkloadsToBeWaiting(ctx context.Context, k8sClient client.Client, wls ...*kueue.Workload) {
	ginkgo.GinkgoHelper()
	wlKeys := uniqueKeys(workloadKeys(wls))
	wl := &kueue.Workload{}
	gomega.Eventually(func(g gomega.Gomega) {
		waiting := make([]client.ObjectKey, 0, len(wlKeys))
		for _, wlKey := range wlKeys {
			g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
			cond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
			if cond != nil && cond.Status == metav1.ConditionFalse && cond.Reason == "Waiting" {
				waiting = append(waiting, wlKey)
			}
		}
		g.Expect(waiting).Should(gomega.Equal(wlKeys), "Unexpected workloads are waiting")
	}, Timeout, Interval).Should(gomega.Succeed())
}

func ExpectWorkloadsToBeFrozen(ctx context.Context, k8sClient client.Client, cq string, wls ...*kueue.Workload) {
	wlKeys := uniqueKeys(workloadKeys(wls))
	wl := &kueue.Workload{}
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		frozen := make([]client.ObjectKey, 0, len(wlKeys))
		for _, wlKey := range wlKeys {
			g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
			cond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
			msg := fmt.Sprintf("ClusterQueue %s is inactive", cq)
			if cond != nil && cond.Status == metav1.ConditionFalse && cond.Reason == "Inadmissible" && cond.Message == msg {
				frozen = append(frozen, wlKey)
			}
		}
		g.Expect(frozen).Should(gomega.Equal(wlKeys), "Unexpected workloads are frozen")
	}, Timeout, Interval).Should(gomega.Succeed())
}

func ExpectWorkloadToBeAdmittedAs(ctx context.Context, k8sClient client.Client, wl *kueue.Workload, admission *kueue.Admission) {
	var updatedWorkload kueue.Workload
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWorkload)).To(gomega.Succeed())
		g.Expect(updatedWorkload.Status.Admission).Should(gomega.BeComparableTo(admission))
	}, Timeout, Interval).Should(gomega.Succeed())
}

func SetQuotaReservation(ctx context.Context, k8sClient client.Client, wlKey client.ObjectKey, admission *kueue.Admission) {
	clk := testingclock.NewFakeClock(time.Now())
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		updatedWl := &kueue.Workload{}
		g.ExpectWithOffset(1, k8sClient.Get(ctx, wlKey, updatedWl)).To(gomega.Succeed())
		g.ExpectWithOffset(1, workload.PatchAdmissionStatus(ctx, k8sClient, updatedWl, clk, func(wl *kueue.Workload) (bool, error) {
			var updated bool
			if admission == nil {
				updated = workload.UnsetQuotaReservationWithCondition(wl, "EvictedByTest", "Evicted By Test", clk.Now())
			} else {
				updated = workload.SetQuotaReservation(wl, admission, clk)
			}
			return updated, nil
		})).To(gomega.Succeed())
	}, Timeout, Interval).Should(gomega.Succeed())
}

// SyncAdmittedConditionForWorkloads sets the Admission condition of the provided workloads based on
// the state of quota reservation and admission checks. It should be use in tests that are not running
// the workload controller.
func SyncAdmittedConditionForWorkloads(ctx context.Context, k8sClient client.Client, wls ...*kueue.Workload) {
	var updatedWorkload kueue.Workload
	for _, wl := range wls {
		gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
			g.ExpectWithOffset(1, k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWorkload)).To(gomega.Succeed())
			g.ExpectWithOffset(1, workload.PatchAdmissionStatus(ctx, k8sClient, &updatedWorkload, RealClock, func(wl *kueue.Workload) (bool, error) {
				return workload.SyncAdmittedCondition(wl, time.Now()), nil
			})).To(gomega.Succeed())
		}, Timeout, Interval).Should(gomega.Succeed())
	}
}

func ExpectWorkloadsToBeEvictedByKeys(ctx context.Context, k8sClient client.Client, wlKeys ...client.ObjectKey) {
	ginkgo.GinkgoHelper()
	wlKeys = uniqueKeys(wlKeys)
	wl := &kueue.Workload{}
	gomega.Eventually(func(g gomega.Gomega) {
		evicted := make([]client.ObjectKey, 0, len(wlKeys))
		for _, wlKey := range wlKeys {
			g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
			if workload.IsEvicted(wl) {
				evicted = append(evicted, wlKey)
			}
		}
		g.Expect(evicted).Should(gomega.Equal(wlKeys), "Unexpected workloads were marked for eviction")
	}, Timeout, Interval).Should(gomega.Succeed())
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
					workload.PatchAdmissionStatus(ctx, k8sClient, wl, RealClock, func(wl *kueue.Workload) (bool, error) {
						return workload.UnsetQuotaReservationWithCondition(wl, "Pending", "By test", time.Now()), nil
					}),
				).Should(gomega.Succeed(), fmt.Sprintf("Unable to unset quota reservation for %q", key))
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

func SetWorkloadsAdmissionCheck(ctx context.Context, k8sClient client.Client, wl *kueue.Workload, check kueue.AdmissionCheckReference, state kueue.CheckState, expectExisting bool) {
	var updatedWorkload kueue.Workload
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWorkload)).To(gomega.Succeed())
		if expectExisting {
			currentCheck := admissioncheck.FindAdmissionCheck(updatedWorkload.Status.AdmissionChecks, check)
			g.Expect(currentCheck).NotTo(gomega.BeNil(), "the check %s was not found in %s", check, workload.Key(wl))
			currentCheck.State = state
		} else {
			workload.SetAdmissionCheckState(&updatedWorkload.Status.AdmissionChecks, kueue.AdmissionCheckState{
				Name:  check,
				State: state,
			}, RealClock)
		}
		g.Expect(k8sClient.Status().Update(ctx, &updatedWorkload)).To(gomega.Succeed())
	}, Timeout, Interval).Should(gomega.Succeed())
}

func AwaitAndVerifyWorkloadQueueName(ctx context.Context, client client.Client, createdWorkload *kueue.Workload, wlLookupKey types.NamespacedName, jobQueueName kueue.LocalQueueName) {
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

func SetPodsPhase(ctx context.Context, k8sClient client.Client, phase corev1.PodPhase, pods ...*corev1.Pod) {
	ginkgo.GinkgoHelper()
	for _, p := range pods {
		updatedPod := corev1.Pod{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(p), &updatedPod)).To(gomega.Succeed())
			updatedPod.Status.Phase = phase
			g.Expect(k8sClient.Status().Update(ctx, &updatedPod)).To(gomega.Succeed())
		}, Timeout, Interval).Should(gomega.Succeed())
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
		g.Expect(createdPod.Spec.SchedulingGates).NotTo(gomega.ContainElement(corev1.PodSchedulingGate{Name: podconstants.SchedulingGateName}))
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

func ExpectPreemptedCondition(ctx context.Context, k8sClient client.Client, reason string, status metav1.ConditionStatus, preemptedWl, preempteeWl *kueue.Workload, preemteeWorkloadUID, preempteeJobUID, preemptorPath, preempteePath string) {
	conditionCmpOpts := cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime", "ObservedGeneration")
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(preemptedWl), preemptedWl)).To(gomega.Succeed())
		g.Expect(preemptedWl.Status.Conditions).To(gomega.ContainElements(gomega.BeComparableTo(metav1.Condition{
			Type:    kueue.WorkloadPreempted,
			Status:  status,
			Reason:  reason,
			Message: fmt.Sprintf("Preempted to accommodate a workload (UID: %s, JobUID: %s) due to %s; preemptor path: %s; preemptee path: %s", preemteeWorkloadUID, preempteeJobUID, preemption.HumanReadablePreemptionReasons[reason], preemptorPath, preempteePath),
		}, conditionCmpOpts)))
	}, Timeout, Interval).Should(gomega.Succeed())
}

func NewTestingLogger(writer io.Writer) logr.Logger {
	logger, _ := NewTestingLoggerAndObservedLogs(writer)
	return logger
}

func NewTestingLoggerAndObservedLogs(writer io.Writer) (logr.Logger, *observer.ObservedLogs) {
	level := utiltesting.LogLevelWithDefault(utiltesting.DefaultLogLevel)
	zapcoreLevel := zapcore.Level(level)

	logsObserver, observedLogs := observer.New(zapcoreLevel)

	logsObserverWrapper := zaplog.WrapCore(func(core zapcore.Core) zapcore.Core {
		return utillogging.NewCustomLogProcessor(zapcore.NewTee(logsObserver, core))
	})

	opts := func(o *zap.Options) {
		o.TimeEncoder = zapcore.RFC3339NanoTimeEncoder
		o.ZapOpts = []zaplog.Option{zaplog.AddCaller(),
			logsObserverWrapper}
	}

	return zap.New(
		zap.WriteTo(writer),
		zap.UseDevMode(true),
		zap.Level(zapcoreLevel),
		opts), observedLogs
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
			g.Expect(readCq.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.ClusterQueueActive))
		}
	}, LongTimeout, Interval).Should(gomega.Succeed())
}

func ExpectLocalQueuesToBeActive(ctx context.Context, c client.Client, lqs ...*kueue.LocalQueue) {
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		readLq := &kueue.LocalQueue{}
		for _, lq := range lqs {
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(lq), readLq)).To(gomega.Succeed())
			g.Expect(readLq.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.LocalQueueActive))
		}
	}, LongTimeout, Interval).Should(gomega.Succeed())
}

func ExpectAdmissionChecksToBeActive(ctx context.Context, c client.Client, acs ...*kueue.AdmissionCheck) {
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		readAc := &kueue.AdmissionCheck{}
		for _, ac := range acs {
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(ac), readAc)).To(gomega.Succeed())
			g.Expect(readAc.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.AdmissionCheckActive))
		}
	}, Timeout, Interval).Should(gomega.Succeed())
}

func ExpectJobUnsuspended(ctx context.Context, c client.Client, key types.NamespacedName) {
	ginkgo.GinkgoHelper()
	job := &batchv1.Job{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(c.Get(ctx, key, job)).To(gomega.Succeed())
		g.Expect(job.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
	}, LongTimeout, Interval).Should(gomega.Succeed())
}

func ExpectJobUnsuspendedWithNodeSelectors(ctx context.Context, c client.Client, key types.NamespacedName, nodeSelector map[string]string) {
	ginkgo.GinkgoHelper()
	ExpectJobUnsuspended(ctx, c, key)
	job := &batchv1.Job{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(c.Get(ctx, key, job)).To(gomega.Succeed())
		g.Expect(job.Spec.Template.Spec.NodeSelector).Should(gomega.Equal(nodeSelector))
	}, LongTimeout, Interval).Should(gomega.Succeed())
}

func ExpectRayClusterUnsuspended(ctx context.Context, c client.Client, key types.NamespacedName) {
	rayCluster := &rayv1.RayCluster{}
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		g.Expect(c.Get(ctx, key, rayCluster)).To(gomega.Succeed())
		g.Expect(rayCluster.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
	}, Timeout, Interval).Should(gomega.Succeed())
}

func CreateNodesWithStatus(ctx context.Context, c client.Client, nodes []corev1.Node) {
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
	unmanaged = append(unmanaged, "kube-system", "kueue-system")
	ls := &metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{
			{
				Key:      corev1.LabelMetadataName,
				Operator: metav1.LabelSelectorOpNotIn,
				Values:   unmanaged,
			},
		},
	}
	sel, err := metav1.LabelSelectorAsSelector(ls)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())
	return sel
}

func KExecute(ctx context.Context, cfg *rest.Config, client *rest.RESTClient, ns, pod, container string, command []string) ([]byte, []byte, error) {
	var out, outErr bytes.Buffer

	req := client.Post().
		Resource("pods").
		Namespace(ns).
		Name(pod).
		SubResource("exec").
		VersionedParams(
			&corev1.PodExecOptions{
				Container: container,
				Command:   command,
				Stdout:    true,
				Stderr:    true,
			},
			scheme.ParameterCodec,
		)

	executor, err := remotecommand.NewSPDYExecutor(cfg, "POST", req.URL())
	if err != nil {
		return nil, nil, err
	}

	if err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: &out, Stderr: &outErr}); err != nil {
		return nil, nil, err
	}

	return out.Bytes(), outErr.Bytes(), nil
}

// getProjectBaseDir retrieves the project base directory either from an environment variable or by searching for a Makefile.
// The fallback to the search is useful for running in IDEs like vs-code which don't set the PROJECT_DIR env. variable by default.
func getProjectBaseDir() string {
	projectBasePath, found := os.LookupEnv("PROJECT_DIR")
	if found {
		return filepath.Dir(projectBasePath)
	}

	projectBaseDir, err := findMakefileDir()
	if err != nil {
		ginkgo.Fail(fmt.Sprintf("Failed to find project base directory: %v", err))
	}
	return projectBaseDir
}

// findMakefileDir traverses directories upward from the current directory until it finds a directory containing a Makefile.
func findMakefileDir() (string, error) {
	startDir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("could not get current working directory: %w", err)
	}

	for {
		makefilePath := filepath.Join(startDir, "Makefile")
		if _, err := os.Stat(makefilePath); err == nil {
			return startDir, nil
		}

		parentDir := filepath.Dir(startDir)
		if parentDir == startDir {
			return "", errors.New("not able to locate Makefile")
		}
		startDir = parentDir
	}
}

func FindDeploymentCondition(deployment *appsv1.Deployment, deploymentType appsv1.DeploymentConditionType) *appsv1.DeploymentCondition {
	for i := range deployment.Status.Conditions {
		c := deployment.Status.Conditions[i]
		if c.Type == deploymentType {
			return &c
		}
	}
	return nil
}

func GetListOptsFromLabel(label string) *client.ListOptions {
	ginkgo.GinkgoHelper()
	selector, err := labels.Parse(label)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return &client.ListOptions{
		LabelSelector: selector,
	}
}

func MustCreate(ctx context.Context, c client.Client, obj client.Object) {
	ginkgo.GinkgoHelper()
	gomega.Expect(c.Create(ctx, obj)).Should(gomega.Succeed())
}

func CreateClusterQueuesAndWaitForActive(ctx context.Context, c client.Client, cqs ...*kueue.ClusterQueue) {
	ginkgo.GinkgoHelper()
	for _, cq := range cqs {
		MustCreate(ctx, c, cq)
	}
	ExpectClusterQueuesToBeActive(ctx, c, cqs...)
}

func CreateLocalQueuesAndWaitForActive(ctx context.Context, c client.Client, lqs ...*kueue.LocalQueue) {
	ginkgo.GinkgoHelper()
	for _, lq := range lqs {
		MustCreate(ctx, c, lq)
	}
	ExpectLocalQueuesToBeActive(ctx, c, lqs...)
}

func CreateAdmissionChecksAndWaitForActive(ctx context.Context, c client.Client, acs ...*kueue.AdmissionCheck) {
	ginkgo.GinkgoHelper()
	for _, ac := range acs {
		MustCreate(ctx, c, ac)
	}
	ExpectAdmissionChecksToBeActive(ctx, c, acs...)
}

func MustHaveOwnerReference(g gomega.Gomega, ownerRefs []metav1.OwnerReference, obj client.Object, scheme *runtime.Scheme) {
	hasOwnerRef, err := controllerutil.HasOwnerReference(ownerRefs, obj, scheme)
	g.Expect(err).ToNot(gomega.HaveOccurred())
	g.Expect(hasOwnerRef).To(gomega.BeTrue())
}

func DeactivateWorkload(ctx context.Context, c client.Client, key client.ObjectKey) {
	ginkgo.GinkgoHelper()
	wl := &kueue.Workload{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(c.Get(ctx, key, wl)).To(gomega.Succeed())
		wl.Spec.Active = ptr.To(false)
		g.Expect(c.Update(ctx, wl)).To(gomega.Succeed())
	}, Timeout, Interval).Should(gomega.Succeed())
}

func SetNodeCondition(ctx context.Context, k8sClient client.Client, node *corev1.Node, newCondition *corev1.NodeCondition) {
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		var updatedNode corev1.Node
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(node), &updatedNode)).To(gomega.Succeed())
		condition := utiltas.GetNodeCondition(&updatedNode, newCondition.Type)
		changed := false
		if condition == nil {
			updatedNode.Status.Conditions = append(updatedNode.Status.Conditions, *newCondition)
			changed = true
		}
		if newCondition.LastTransitionTime.IsZero() {
			condition.LastTransitionTime = metav1.Now()
			changed = true
		} else if condition.LastTransitionTime != newCondition.LastTransitionTime {
			condition.LastTransitionTime = newCondition.LastTransitionTime
			changed = true
		}
		if condition.Status != newCondition.Status {
			condition.Status = newCondition.Status
			changed = true
		}
		if changed {
			g.Expect(k8sClient.Status().Update(ctx, &updatedNode)).To(gomega.Succeed())
		}
	}, Timeout, Interval).Should(gomega.Succeed(), "Failed to set node condition %s to %s for node %s", newCondition.Type, newCondition.Status, node.Name)
}

func ExpectLocalQueueFairSharingUsageToBe(ctx context.Context, k8sClient client.Client, lqKey client.ObjectKey, comparator string, compareTo any) {
	ginkgo.GinkgoHelper()
	lq := &kueue.LocalQueue{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, lqKey, lq)).Should(gomega.Succeed())
		g.Expect(lq.Status.FairSharing).ShouldNot(gomega.BeNil())
		g.Expect(lq.Status.FairSharing.AdmissionFairSharingStatus).ShouldNot(gomega.BeNil())
		g.Expect(lq.Status.FairSharing.AdmissionFairSharingStatus.ConsumedResources).Should(gomega.HaveLen(1))
		usage := lq.Status.FairSharing.AdmissionFairSharingStatus.ConsumedResources[corev1.ResourceCPU]
		g.Expect(usage.MilliValue()).To(gomega.BeNumerically(comparator, compareTo))
	}, Timeout, Interval).Should(gomega.Succeed())
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
		g.Expect(newWorkload).ShouldNot(gomega.BeNil(),
			"replacement workload for %s not found", workload.Key(oldWorkload))
	}, Timeout, Interval).Should(gomega.Succeed())
	return newWorkload
}

func ExpectJobToBeRunning(ctx context.Context, c client.Client, job *batchv1.Job) {
	ginkgo.GinkgoHelper()
	createdJob := &batchv1.Job{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(c.Get(ctx, client.ObjectKeyFromObject(job), createdJob)).To(gomega.Succeed())
		g.Expect(createdJob.Status.StartTime).NotTo(gomega.BeNil())
		g.Expect(createdJob.Status.CompletionTime).To(gomega.BeNil())
	}, LongTimeout, Interval).Should(gomega.Succeed())
}

func ExpectJobToBeCompleted(ctx context.Context, c client.Client, job *batchv1.Job) {
	ginkgo.GinkgoHelper()
	createdJob := &batchv1.Job{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(c.Get(ctx, client.ObjectKeyFromObject(job), createdJob)).To(gomega.Succeed())
		g.Expect(createdJob.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(
			batchv1.JobCondition{
				Type:   batchv1.JobComplete,
				Status: corev1.ConditionTrue,
			},
			cmpopts.IgnoreFields(batchv1.JobCondition{}, "LastTransitionTime", "LastProbeTime", "Reason", "Message"))))
	}, LongTimeout, Interval).Should(gomega.Succeed())
}

func UpdateReclaimablePods(ctx context.Context, c client.Client, wl *kueue.Workload, reclaimablePods []kueue.ReclaimablePod) {
	ginkgo.GinkgoHelper()
	createdWl := &kueue.Workload{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(c.Get(ctx, client.ObjectKeyFromObject(wl), createdWl)).To(gomega.Succeed())
		g.Expect(workload.UpdateReclaimablePods(ctx, c, createdWl, reclaimablePods)).To(gomega.Succeed())
	}, Timeout, Interval).Should(gomega.Succeed())
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

func VerifyLogs(observedLogs *observer.ObservedLogs) {
	errorOrMoreSevereLogs := observedLogs.Filter(func(le observer.LoggedEntry) bool {
		return le.Level >= zapcore.ErrorLevel
	})

	concurrentModificationErrorLogs := errorOrMoreSevereLogs.Filter(IsLoggedEntryAConcurrentModification)

	gomega.ExpectWithOffset(1, concurrentModificationErrorLogs.TakeAll()).To(gomega.BeEmpty())
}

func IsLoggedEntryAConcurrentModification(le observer.LoggedEntry) bool {
	if le.ContextMap()["error"] == nil {
		return false
	}
	errorDetals := le.ContextMap()["error"].(string)
	expectedConcurrentModiciationDetails := "the object has been modified; please apply your changes to the latest version and try again"
	return strings.Contains(errorDetals, expectedConcurrentModiciationDetails)
}
