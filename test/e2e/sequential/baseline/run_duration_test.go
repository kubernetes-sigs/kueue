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

package baseline

import (
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/e2e"
)

var _ = ginkgo.Describe("Workload run duration", ginkgo.Label("feature:workload-run-duration", e2e.Shard1), ginkgo.Ordered, func() {
	var (
		ns *corev1.Namespace
		cq *kueue.ClusterQueue
		fl *kueue.ResourceFlavor
	)
	ginkgo.BeforeAll(func() {
		e2e.UpdateKueueConfigurationAndRestart(ctx, k8sClient, defaultKueueCfg, kindClusterName, func(cfg *configapi.Configuration) {
			if cfg.FeatureGates == nil {
				cfg.FeatureGates = make(map[string]bool)
			}
			cfg.FeatureGates[string(features.WorkloadRunDuration)] = true
		})
	})
	ginkgo.AfterAll(func() {
		e2e.UpdateKueueConfigurationAndRestart(ctx, k8sClient, defaultKueueCfg, kindClusterName)
	})
	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "run-duration-")
		fl = utiltestingapi.MakeResourceFlavor("run-duration").Obj()
		behavioral.MustCreate(ctx, k8sClient, fl)
		cq = utiltestingapi.MakeClusterQueue("run-duration").MaximumRunDurationSeconds(120).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(fl.Name).Resource(corev1.ResourceCPU, "1").Obj()).Obj()
		behavioral.MustCreate(ctx, k8sClient, cq)
		behavioral.MustCreate(ctx, k8sClient, utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj())
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, fl, true)
	})

	ginkgo.It("drains actual Pods before quota release and restarts the same Job and Workload after a FIFO turn", func() {
		job := testingjob.MakeJob("running", ns.Name).Queue("queue").Suspend(true).
			Image(e2e.GetAgnHostImage(), e2e.BehaviorWaitForDeletion).
			Parallelism(1).Completions(1).RequestAndLimit(corev1.ResourceCPU, "1").Obj()
		job.Spec.Template.Spec.Containers[0].Lifecycle = &corev1.Lifecycle{
			PreStop: &corev1.LifecycleHandler{Sleep: &corev1.SleepAction{Seconds: 5}},
		}
		behavioral.MustCreate(ctx, k8sClient, job)
		e2e.ExpectJobToBeRunning(ctx, k8sClient, job)
		wl := &kueue.Workload{}
		key := client.ObjectKey{Namespace: ns.Name, Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID)}
		gomega.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
		uid := wl.UID
		start := wl.Status.RunDuration.AdmittedAt.Time
		pods := &corev1.PodList{}
		gomega.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name),
			client.MatchingLabels{batchv1.ControllerUidLabel: string(job.UID)})).To(gomega.Succeed())
		gomega.Expect(pods.Items).To(gomega.HaveLen(1))
		oldPodUID := pods.Items[0].UID

		waiting := testingjob.MakeJob("waiting", ns.Name).Queue("queue").Suspend(true).
			Image(e2e.GetAgnHostImage(), e2e.BehaviorWaitForDeletion).
			Parallelism(1).Completions(1).RequestAndLimit(corev1.ResourceCPU, "1").Obj()
		behavioral.MustCreate(ctx, k8sClient, waiting)
		waitingKey := client.ObjectKey{Namespace: ns.Name, Name: workloadjob.GetWorkloadNameForJob(waiting.Name, waiting.UID)}
		gomega.Eventually(func(g gomega.Gomega) {
			other := &kueue.Workload{}
			g.Expect(k8sClient.Get(ctx, waitingKey, other)).To(gomega.Succeed())
			other.Spec.MaximumRunDurationSeconds = new(int32(30))
			g.Expect(k8sClient.Update(ctx, other)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
			wl.Spec.MaximumRunDurationSeconds = new(int32(1))
			g.Expect(k8sClient.Update(ctx, wl)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
			condition := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadEvicted)
			g.Expect(condition).NotTo(gomega.BeNil())
			g.Expect(condition.Reason).To(gomega.Equal(kueue.WorkloadEvictedByRunDuration))
			g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name),
				client.MatchingLabels{batchv1.ControllerUidLabel: string(job.UID)})).To(gomega.Succeed())
			g.Expect(pods.Items).To(gomega.HaveLen(1))
			g.Expect(pods.Items[0].DeletionTimestamp).NotTo(gomega.BeNil())
			g.Expect(pods.Items[0].Status.Phase).To(gomega.Equal(corev1.PodRunning))
			g.Expect(workload.HasQuotaReservation(wl)).To(gomega.BeTrue())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
			wl.Spec.MaximumRunDurationSeconds = new(int32(120))
			g.Expect(k8sClient.Update(ctx, wl)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		gomega.Eventually(func(g gomega.Gomega) {
			other := &kueue.Workload{}
			g.Expect(k8sClient.Get(ctx, waitingKey, other)).To(gomega.Succeed())
			g.Expect(workload.IsAdmitted(other)).To(gomega.BeTrue())
			g.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
			g.Expect(workload.HasQuotaReservation(wl)).To(gomega.BeFalse())
			g.Expect(wl.UID).To(gomega.Equal(uid))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		e2e.ExpectJobToBeRunning(ctx, k8sClient, waiting)
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
			g.Expect(workload.IsAdmitted(wl)).To(gomega.BeTrue())
			g.Expect(wl.UID).To(gomega.Equal(uid))
			g.Expect(wl.Status.RunDuration.AdmittedAt.Time.After(start)).To(gomega.BeTrue())
		}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())
		e2e.ExpectJobToBeRunning(ctx, k8sClient, job)
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name),
				client.MatchingLabels{batchv1.ControllerUidLabel: string(job.UID)})).To(gomega.Succeed())
			g.Expect(pods.Items).To(gomega.HaveLen(1))
			g.Expect(pods.Items[0].UID).NotTo(gomega.Equal(oldPodUID))
			g.Expect(pods.Items[0].Status.Phase).To(gomega.Equal(corev1.PodRunning))
		}, time.Minute, behavioral.Interval).Should(gomega.Succeed())
	})
})
