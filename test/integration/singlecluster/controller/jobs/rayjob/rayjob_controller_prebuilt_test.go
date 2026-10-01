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

package rayjob

import (
	"context"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	rayjobcontroller "sigs.k8s.io/kueue/pkg/controller/jobs/rayjob"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingrayjob "sigs.k8s.io/kueue/pkg/util/testingjobs/rayjob"
	"sigs.k8s.io/kueue/pkg/webhooks"
	workloadfinish "sigs.k8s.io/kueue/pkg/workload/finish"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Prebuilt RayJob slice recovery", ginkgo.Label("job:ray", "area:jobs"), ginkgo.Ordered, func() {
	var rec jobframework.JobReconcilerInterface
	var cachedClient client.Client
	var ns *corev1.Namespace

	ginkgo.BeforeAll(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlices, true)
		fwk.StartManager(ctx, cfg, func(ctx context.Context, mgr manager.Manager) {
			gomega.Expect(indexer.Setup(ctx, mgr.GetFieldIndexer())).To(gomega.Succeed())
			gomega.Expect(rayjobcontroller.SetupIndexes(ctx, mgr.GetFieldIndexer())).To(gomega.Succeed())
			gomega.Expect(rayjobcontroller.SetupRayJobWebhook(mgr)).To(gomega.Succeed())
			failedWebhook, err := webhooks.Setup(mgr, nil)
			gomega.Expect(err).NotTo(gomega.HaveOccurred(), "webhook", failedWebhook)
			cachedClient = mgr.GetClient()
			rec, err = rayjobcontroller.NewReconciler(ctx, cachedClient, mgr.GetFieldIndexer(), mgr.GetEventRecorder(constants.JobControllerName))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			// Reconcile explicitly: no scheduler or controller may clean up the seeded failure state.
		})
	})
	ginkgo.AfterAll(func() { fwk.StopManager(ctx) })
	ginkgo.BeforeEach(func() { ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "prebuilt-recovery-") })
	ginkgo.AfterEach(func() { gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed()) })

	ginkgo.It("Should recover a missed slice finish with an ownerless admitted replacement", func() {
		job := testingrayjob.MakeJob("job", ns.Name).Queue("q").PrebuiltWorkloadLabel("replacement").
			WithSubmissionMode(rayv1.InteractiveMode).
			Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).Obj()
		util.MustCreate(ctx, k8sClient, job)
		podSets, err := (*rayjobcontroller.RayJob)(job).PodSets(ctx, k8sClient)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		old := utiltestingapi.MakeWorkload("old", ns.Name).Queue("q").
			ControllerReference(rayv1.SchemeGroupVersion.WithKind("RayJob"), job.Name, string(job.UID)).
			PodSets(podSets...).Obj()
		replacementPodSets := old.DeepCopy().Spec.PodSets
		replacementPodSets[1].Count = 2
		replacement := utiltestingapi.MakeWorkload("replacement", ns.Name).Queue("q").
			Annotation(kueue.WorkloadSliceNameAnnotation, old.Name).
			Annotation(workloadslicing.WorkloadSliceReplacementFor, ns.Name+"/"+old.Name).
			PodSets(replacementPodSets...).Obj()
		for _, wl := range []*kueue.Workload{old, replacement} {
			util.MustCreate(ctx, k8sClient, wl)
			assignments := make([]kueue.PodSetAssignment, 0, len(wl.Spec.PodSets))
			for _, ps := range wl.Spec.PodSets {
				assignments = append(assignments, utiltestingapi.MakePodSetAssignment(ps.Name).Count(ps.Count).Obj())
			}
			wl.Status = utiltestingapi.MakeWorkload(wl.Name, wl.Namespace).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(assignments...).Obj(), time.Now()).AdmittedAt(true, time.Now()).Status
			gomega.Expect(k8sClient.Status().Update(ctx, wl)).To(gomega.Succeed())
		}

		ginkgo.By("reproducing a real stale-resource-version conflict when finishing the old slice")
		stale := old.DeepCopy()
		apimeta.SetStatusCondition(&old.Status.Conditions, metav1.Condition{
			Type: kueue.WorkloadPodsReady, Status: metav1.ConditionFalse,
			Reason: kueue.WorkloadWaitForStart, Message: "Pods are not ready",
			LastTransitionTime: metav1.Now(),
		})
		gomega.Expect(k8sClient.Status().Update(ctx, old)).To(gomega.Succeed())
		err = workloadfinish.Finish(ctx, k8sClient, stale, kueue.WorkloadSliceReplaced, "Replaced to accommodate a new workload slice", clock.RealClock{})
		gomega.Expect(apierrors.IsConflict(err)).To(gomega.BeTrue(), "expected a resourceVersion conflict, got %v", err)
		gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(old), old)).To(gomega.Succeed())
		gomega.Expect(workloadfinish.IsFinished(old)).To(gomega.BeFalse())
		gomega.Expect(replacement.OwnerReferences).To(gomega.BeEmpty())

		beforeOld, beforeReplacement := old.Spec.DeepCopy(), replacement.Spec.DeepCopy()
		ginkgo.By("recovering through the prebuilt RayJob reconciler, without a scheduler retry")
		// The manager has repointed the running job, but has not synced its replica count yet.
		gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), job)).To(gomega.Succeed())
		job.Spec.Suspend = false
		gomega.Expect(k8sClient.Update(ctx, job)).To(gomega.Succeed())
		request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(job)}
		gomega.Eventually(func(g gomega.Gomega) {
			cached := &rayv1.RayJob{}
			g.Expect(cachedClient.Get(ctx, request.NamespacedName, cached)).To(gomega.Succeed())
			g.Expect(cached.Spec.Suspend).To(gomega.BeFalse())
			_, err := rec.Reconcile(ctx, request)
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(old), old)).To(gomega.Succeed())
			g.Expect(workloadslicing.IsReplaced(old.Status)).To(gomega.BeTrue())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("remaining idempotent and preserving remotely managed workload specs")
		gomega.Eventually(func(g gomega.Gomega) {
			_, err := rec.Reconcile(ctx, request)
			g.Expect(err).NotTo(gomega.HaveOccurred())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
		gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(replacement), replacement)).To(gomega.Succeed())
		gomega.Expect(workloadfinish.IsFinished(replacement)).To(gomega.BeFalse())
		gomega.Expect(old.Spec).To(gomega.Equal(*beforeOld))
		gomega.Expect(replacement.Spec).To(gomega.Equal(*beforeReplacement))
		gomega.Expect(util.ExpectWorkloadsInNamespace(ctx, k8sClient, ns.Name, 2)).To(gomega.HaveLen(2))
	})
})
