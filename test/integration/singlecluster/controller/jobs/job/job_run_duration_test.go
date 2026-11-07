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

package job

import (
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
	workloadpatching "sigs.k8s.io/kueue/pkg/workload/patching"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("Job run duration", ginkgo.Label("job:batch", "area:jobs", "feature:workload-run-duration"),
	ginkgo.Ordered, func() {
		var (
			ns *corev1.Namespace
			cq *kueue.ClusterQueue
			lq *kueue.LocalQueue
			fl *kueue.ResourceFlavor
		)
		ginkgo.BeforeAll(func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.WorkloadRunDuration, true)
			fwk.StartManager(ctx, cfg, managerAndControllersSetup(false, true, nil))
		})
		ginkgo.AfterAll(func() { fwk.StopManager(ctx) })
		ginkgo.BeforeEach(func() {
			ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "run-duration-")
			fl = utiltestingapi.MakeResourceFlavor("run-duration").Obj()
			behavioral.MustCreate(ctx, k8sClient, fl)
			cq = utiltestingapi.MakeClusterQueue("run-duration").
				MaximumRunDurationSeconds(3600).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(fl.Name).Resource(corev1.ResourceCPU, "1").Obj()).Obj()
			behavioral.MustCreate(ctx, k8sClient, cq)
			lq = utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
			behavioral.MustCreate(ctx, k8sClient, lq)
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, fl, true)
		})

		ginkgo.It("restores an unlimited captured default after removing a live override", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).To(gomega.Succeed())
				cq.Spec.MaximumRunDurationSeconds = nil
				g.Expect(k8sClient.Update(ctx, cq)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			job := testingjob.MakeJob("unlimited", ns.Name).Queue(kueue.LocalQueueName(lq.Name)).
				Suspend(true).Parallelism(1).Completions(1).Request(corev1.ResourceCPU, "1").Obj()
			behavioral.MustCreate(ctx, k8sClient, job)
			key := client.ObjectKey{Namespace: ns.Name, Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID)}
			wl := &kueue.Workload{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(wl)).To(gomega.BeTrue())
				g.Expect(wl.Status.RunDuration).NotTo(gomega.BeNil())
				g.Expect(wl.Status.Admission.RunDurationPolicy).NotTo(gomega.BeNil())
				g.Expect(wl.Status.Admission.RunDurationPolicy.DefaultSeconds).To(gomega.BeNil())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			start := wl.Status.RunDuration.AdmittedAt.Time
			for _, seconds := range []*int32{new(int32(3600)), nil} {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
					wl.Spec.MaximumRunDurationSeconds = seconds
					g.Expect(k8sClient.Update(ctx, wl)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
					g.Expect(wl.Status.RunDuration.AdmittedAt.Time).To(gomega.Equal(start))
					if seconds == nil {
						g.Expect(wl.Status.RunDuration.Deadline).To(gomega.BeNil())
					} else {
						g.Expect(wl.Status.RunDuration.Deadline).NotTo(gomega.BeNil())
						g.Expect(wl.Status.RunDuration.Deadline.Time).To(gomega.Equal(start.Add(time.Duration(*seconds) * time.Second)))
					}
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			}
		})

		ginkgo.It("captures defaults, applies live edits, retains quota until drain, and rotates the same Workload", func() {
			job := testingjob.MakeJob("running", ns.Name).Queue(kueue.LocalQueueName(lq.Name)).
				Suspend(true).Parallelism(1).Completions(1).Request(corev1.ResourceCPU, "1").Obj()
			behavioral.MustCreate(ctx, k8sClient, job)
			key := client.ObjectKey{Namespace: ns.Name, Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID)}
			wl := &kueue.Workload{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(wl)).To(gomega.BeTrue())
				g.Expect(wl.Status.RunDuration).NotTo(gomega.BeNil())
				g.Expect(wl.Status.Admission.RunDurationPolicy.DefaultSeconds).To(gomega.HaveValue(gomega.Equal(int32(3600))))
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), job)).To(gomega.Succeed())
				g.Expect(job.Spec.Suspend).To(gomega.HaveValue(gomega.BeFalse()))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			uid := wl.UID
			start := wl.Status.RunDuration.AdmittedAt.Time

			ginkgo.By("capturing the queue default only once per reservation")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).To(gomega.Succeed())
				cq.Spec.MaximumRunDurationSeconds = new(int32(7200))
				g.Expect(k8sClient.Update(ctx, cq)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			setDuration := func(seconds *int32) {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
					wl.Spec.MaximumRunDurationSeconds = seconds
					g.Expect(k8sClient.Update(ctx, wl)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			}
			for _, edit := range []struct {
				seconds *int32
				want    time.Duration
			}{{new(int32(9000)), 9000 * time.Second}, {nil, 3600 * time.Second}} {
				setDuration(edit.seconds)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
					g.Expect(wl.UID).To(gomega.Equal(uid))
					g.Expect(wl.Status.RunDuration.AdmittedAt.Time).To(gomega.Equal(start))
					g.Expect(wl.Status.RunDuration.Deadline).NotTo(gomega.BeNil())
					g.Expect(wl.Status.RunDuration.Deadline.Time).To(gomega.Equal(start.Add(edit.want)))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			}

			ginkgo.By("rejecting a stale expiry after a live extension with both status patch paths")
			stale := wl.DeepCopy()
			setDuration(new(int32(8000)))
			for _, options := range [][]workloadpatching.PatchStatusOption{nil, {workloadpatching.WithForceApply()}} {
				err := workloadpatching.PatchAdmissionStatus(ctx, k8sClient, stale.DeepCopy(), clock.RealClock{},
					func(old *kueue.Workload) (bool, error) {
						return workloadevict.SetEvictedCondition(old, time.Now(), kueue.WorkloadEvictedByRunDuration, "stale expiry"), nil
					}, options...)
				gomega.Expect(apierrors.IsConflict(err)).To(gomega.BeTrue(), "stale status patch must conflict: %v", err)
			}
			gomega.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
			gomega.Expect(apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadEvicted)).To(gomega.BeFalse())

			ginkgo.By("keeping quota while an old nonterminal Pod remains even with status.active zero")
			pod := testingpod.MakePod("old-pod", ns.Name).Label(batchv1.ControllerUidLabel, string(job.UID)).Obj()
			behavioral.MustCreate(ctx, k8sClient, pod)
			waiting := testingjob.MakeJob("waiting", ns.Name).Queue(kueue.LocalQueueName(lq.Name)).
				Suspend(true).Parallelism(1).Completions(1).Request(corev1.ResourceCPU, "1").Obj()
			behavioral.MustCreate(ctx, k8sClient, waiting)
			waitingKey := client.ObjectKey{Namespace: ns.Name, Name: workloadjob.GetWorkloadNameForJob(waiting.Name, waiting.UID)}
			setDuration(new(int32(1)))
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
				condition := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadEvicted)
				g.Expect(condition).NotTo(gomega.BeNil())
				g.Expect(condition.Status).To(gomega.Equal(metav1.ConditionTrue))
				g.Expect(condition.Reason).To(gomega.Equal(kueue.WorkloadEvictedByRunDuration))
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), job)).To(gomega.Succeed())
				g.Expect(job.Spec.Suspend).To(gomega.HaveValue(gomega.BeTrue()))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(wl)).To(gomega.BeTrue())
				other := &kueue.Workload{}
				g.Expect(k8sClient.Get(ctx, waitingKey, other)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(other)).To(gomega.BeFalse())
			}, time.Second, behavioral.Interval).Should(gomega.Succeed())

			ginkgo.By("not canceling drain on a late extension, then admitting waiting work first")
			setDuration(new(int32(60)))
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), job)).To(gomega.Succeed())
				job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobSuspended, Status: corev1.ConditionTrue}}
				g.Expect(k8sClient.Status().Update(ctx, job)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(wl)).To(gomega.BeTrue())
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), pod)).To(gomega.Succeed())
			}, time.Second, behavioral.Interval).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Delete(ctx, pod)).To(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
				g.Expect(wl.UID).To(gomega.Equal(uid))
				g.Expect(workload.HasQuotaReservation(wl)).To(gomega.BeFalse())
				g.Expect(wl.Status.RequeueState.QueueOrderTime).NotTo(gomega.BeNil())
				other := &kueue.Workload{}
				g.Expect(k8sClient.Get(ctx, waitingKey, other)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(other)).To(gomega.BeTrue())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			ginkgo.By("finishing the waiting job and readmitting the original Workload with a fresh timer")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(waiting), waiting)).To(gomega.Succeed())
				waiting.Status.Succeeded = 1
				now := metav1.Now()
				waiting.Status.StartTime = &now
				waiting.Status.CompletionTime = &now
				waiting.Status.Conditions = []batchv1.JobCondition{
					{Type: batchv1.JobSuccessCriteriaMet, Status: corev1.ConditionTrue, LastTransitionTime: now},
					{Type: batchv1.JobComplete, Status: corev1.ConditionTrue, LastTransitionTime: now},
				}
				g.Expect(k8sClient.Status().Update(ctx, waiting)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
				g.Expect(wl.UID).To(gomega.Equal(uid))
				g.Expect(workload.IsAdmitted(wl)).To(gomega.BeTrue())
				g.Expect(wl.Status.RunDuration.AdmittedAt.Time.After(start)).To(gomega.BeTrue())
				g.Expect(wl.Status.Admission.RunDurationPolicy.DefaultSeconds).To(gomega.HaveValue(gomega.Equal(int32(7200))))
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), job)).To(gomega.Succeed())
				g.Expect(job.Spec.Suspend).To(gomega.HaveValue(gomega.BeFalse()))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})
	})
