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
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	jobcontroller "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/integration"
)

var _ = ginkgo.Describe("Job readmission", ginkgo.Label("job:batch", "area:jobs"), func() {
	var ns *corev1.Namespace
	var flavors []*kueue.ResourceFlavor

	ginkgo.BeforeEach(func() {
		fwk.StartManagerForSpec(ctx, cfg, managerSetup())
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "readmission-")
		for _, pool := range []string{"pool-1", "pool-2"} {
			flavor := utiltestingapi.MakeResourceFlavor(pool).NodeLabel("pool", pool).Obj()
			gomega.Expect(k8sClient.Create(ctx, flavor)).To(gomega.Succeed())
			flavors = append(flavors, flavor)
		}
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		for _, flavor := range flavors {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
		}
		flavors = nil
	})

	ginkgo.It("retains admission until the native controller deletes old pods and acknowledges suspension", func() {
		ginkgo.By("admitting the job on the first flavor")
		job := testingjob.MakeJob("job", ns.Name).Queue("q").
			NodeSelector("user-restriction", "retained").Obj()
		gomega.Expect(k8sClient.Create(ctx, job)).To(gomega.Succeed())
		key := client.ObjectKeyFromObject(job)
		wlKey := client.ObjectKey{Namespace: ns.Name, Name: jobcontroller.GetWorkloadNameForJob(job.Name, job.UID)}
		wl := &kueue.Workload{}
		gomega.Eventually(func() error { return k8sClient.Get(ctx, wlKey, wl) }, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		// An acknowledgement from a previous suspension can survive until the
		// native controller updates status after creating resumed pods.
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, key, job)).To(gomega.Succeed())
			job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobSuspended, Status: corev1.ConditionTrue}}
			g.Expect(k8sClient.Status().Update(ctx, job)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		reserve := func(pool string) {
			admission := utiltestingapi.MakeAdmission("cq").PodSets(
				utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(corev1.ResourceCPU, kueue.ResourceFlavorReference(pool), "1").Count(1).Obj(),
			).Obj()
			integration.SetQuotaReservation(ctx, k8sClient, wlKey, admission)
			integration.SyncAdmittedConditionForWorkloads(ctx, k8sClient, wl)
		}
		reserve("pool-1")
		behavioral.ExpectJobUnsuspended(ctx, k8sClient, key)
		gomega.Expect(k8sClient.Get(ctx, key, job)).To(gomega.Succeed())
		old := testingpod.MakePod("old", ns.Name).
			OwnerReferenceWithUID(job.Name, batchv1.SchemeGroupVersion.WithKind("Job"), string(job.UID)).Obj()
		old.Labels = job.Spec.Template.Labels
		old.Spec = *job.Spec.Template.Spec.DeepCopy()
		old.Spec.SchedulingGates = []corev1.PodSchedulingGate{{Name: kueue.TopologySchedulingGate}}
		gomega.Expect(k8sClient.Create(ctx, old)).To(gomega.Succeed())

		ginkgo.By("evicting the job while an old Pod still exists")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
			apimeta.SetStatusCondition(&wl.Status.Conditions, metav1.Condition{
				Type: kueue.WorkloadEvicted, Status: metav1.ConditionTrue,
				Reason: kueue.WorkloadEvictedByPreemption, Message: "preempted",
				LastTransitionTime: metav1.Now(),
			})
			g.Expect(k8sClient.Status().Update(ctx, wl)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, key, job)).To(gomega.Succeed())
			g.Expect(job.Annotations[jobcontroller.StoppingAnnotation]).To(gomega.Equal("true"))
			g.Expect(job.Spec.Suspend).To(gomega.Equal(new(true)))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		gomega.Consistently(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
			g.Expect(wl.Status.Admission).NotTo(gomega.BeNil())
		}, behavioral.ShortTimeout, behavioral.Interval).Should(gomega.Succeed())

		ginkgo.By("reasserting suspension if the job is resumed while stopping")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, key, job)).To(gomega.Succeed())
			job.Spec.Suspend = new(false)
			g.Expect(k8sClient.Update(ctx, job)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, key, job)).To(gomega.Succeed())
			g.Expect(job.Spec.Suspend).To(gomega.Equal(new(true)))
			g.Expect(job.Annotations).To(gomega.HaveKeyWithValue(jobcontroller.StoppingAnnotation, "true"))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(old), &corev1.Pod{})).To(gomega.Succeed())
		gomega.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
		gomega.Expect(wl.Status.Admission).NotTo(gomega.BeNil())

		ginkgo.By("releasing admission after a fresh suspension acknowledgement")
		// Envtest has no native Job controller; emulate its deletion and status acknowledgement.
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, old, true)
		integration.AcknowledgeJobSuspension(ctx, k8sClient, key)
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
			g.Expect(wl.Status.Admission).To(gomega.BeNil())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		ginkgo.By("readmitting the job on the second flavor")
		reserve("pool-2")
		behavioral.ExpectJobUnsuspendedWithNodeSelectors(ctx, k8sClient, key, map[string]string{
			"pool": "pool-2", "user-restriction": "retained",
		})
	})

	ginkgo.It("retains admission when the Job was suspended before eviction without acknowledgement", func() {
		// Add the queue label last so reconciliation first observes the suspended,
		// evicted Job, without racing admission or Kueue's suspension request.
		job := testingjob.MakeJob("job", ns.Name).Suspend(true).
			Request(corev1.ResourceCPU, "1").NodeSelector("pool", "pool-1").Obj()
		gomega.Expect(k8sClient.Create(ctx, job)).To(gomega.Succeed())
		key := client.ObjectKeyFromObject(job)
		old := testingpod.MakePod("old", ns.Name).
			OwnerReferenceWithUID(job.Name, batchv1.SchemeGroupVersion.WithKind("Job"), string(job.UID)).Obj()
		old.Labels = job.Spec.Template.Labels
		old.Spec = *job.Spec.Template.Spec.DeepCopy()
		old.Spec.SchedulingGates = []corev1.PodSchedulingGate{{Name: kueue.TopologySchedulingGate}}
		gomega.Expect(k8sClient.Create(ctx, old)).To(gomega.Succeed())
		podSets, err := (*jobcontroller.Job)(job).PodSets(ctx, k8sClient)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		wl := utiltestingapi.MakeWorkload(jobcontroller.GetWorkloadNameForJob(job.Name, job.UID), ns.Name).
			Queue("q").PodSets(podSets...).
			ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), job.Name, string(job.UID))
		gomega.Expect(k8sClient.Create(ctx, wl.Obj())).To(gomega.Succeed())
		wl.SimpleReserveQuota("cq", "pool-1", behavioral.RealClock.Now())
		integration.SetQuotaReservation(ctx, k8sClient, client.ObjectKeyFromObject(wl.Obj()), wl.Status.Admission)
		integration.SyncAdmittedConditionForWorkloads(ctx, k8sClient, wl.Obj())
		gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl.Obj()), wl.Obj())).To(gomega.Succeed())
		wl.EvictedAt(behavioral.RealClock.Now())
		gomega.Expect(k8sClient.Status().Update(ctx, wl.Obj())).To(gomega.Succeed())
		job.Labels = map[string]string{constants.QueueLabel: "q"}
		gomega.Expect(k8sClient.Update(ctx, job)).To(gomega.Succeed())

		ginkgo.By("retaining admission while the old Pod still exists")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, key, job)).To(gomega.Succeed())
			g.Expect(job.Annotations).To(gomega.HaveKeyWithValue(jobcontroller.StoppingAnnotation, "true"))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		gomega.Consistently(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl.Obj()), wl.Obj())).To(gomega.Succeed())
			g.Expect(wl.Status.Admission).NotTo(gomega.BeNil())
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(old), old)).To(gomega.Succeed())
		}, behavioral.ShortTimeout, behavioral.Interval).Should(gomega.Succeed())

		ginkgo.By("completing the stop after the native controller acknowledges suspension")
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, old, true)
		integration.AcknowledgeJobSuspension(ctx, k8sClient, key)
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl.Obj()), wl.Obj())).To(gomega.Succeed())
			g.Expect(wl.Status.Admission).To(gomega.BeNil())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		gomega.Consistently(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, key, job)).To(gomega.Succeed())
			g.Expect(job.Spec.Suspend).To(gomega.Equal(new(true)))
			g.Expect(job.Annotations).NotTo(gomega.HaveKey(jobcontroller.StoppingAnnotation))
		}, behavioral.ShortTimeout, behavioral.Interval).Should(gomega.Succeed())
	})
})
