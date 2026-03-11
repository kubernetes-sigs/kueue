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
	"context"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	kueueconstants "sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/workload"
)

// IsAdmissible returns false when the workload HasQuotaReservation or is Finished already
// So there's a chance the workload gets admitted before we get here to test whether it's admissible or not
func IsAdmissibleOrPastQuotaReservation(wl *kueue.Workload) bool {
	return (workload.IsAdmissible(wl) || workload.IsFinished(wl) || workload.HasQuotaReservation(wl)) && workload.IsActive(wl)
}

// Verify that a job with the AdmissionGatedBy annotation is inadmissible.
// This can be used across all job types.
func VerifyAdmissionGatedByJobIsInadmissible(
	ctx context.Context,
	k8sClient client.Client,
	job client.Object,
	wlLookupKey types.NamespacedName,
	admissionGateValue string,
) {
	lookupKey := types.NamespacedName{Name: job.GetName(), Namespace: job.GetNamespace()}

	ginkgo.By("Checking the workload is created with the admission gate annotation")
	createdWorkload := &kueue.Workload{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
		g.Expect(createdWorkload.Annotations).Should(gomega.HaveKeyWithValue(
			kueueconstants.AdmissionGatedByAnnotation, admissionGateValue))
		ExpectEventAppeared(ctx, k8sClient, corev1.Event{
			Reason:  "AdmissionGated",
			Type:    corev1.EventTypeNormal,
			Message: "Workload admission is gated by: " + admissionGateValue,
		})
	}, Timeout, Interval).Should(gomega.Succeed())

	ginkgo.By("Checking the Workload remains InAdmissible")
	gomega.Consistently(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, lookupKey, job)).Should(gomega.Succeed())
		g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
		admissible := workload.IsAdmissible(createdWorkload)
		g.Expect(admissible).Should(gomega.BeFalse())
	}, ConsistentDuration, ShortInterval).Should(gomega.Succeed())

	ginkgo.By("Checking the workload has QuotaReserved condition with AdmissionGated reason")
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
		cond := apimeta.FindStatusCondition(createdWorkload.Status.Conditions, kueue.WorkloadQuotaReserved)
		g.Expect(cond).NotTo(gomega.BeNil())
		g.Expect(cond.Status).To(gomega.Equal(metav1.ConditionFalse))
		g.Expect(cond.Reason).To(gomega.Equal(kueue.WorkloadAdmissionGated))
	}, Timeout, Interval).Should(gomega.Succeed())

	ginkgo.By("Checking the workload remains unadmitted even with available quota")
	gomega.Consistently(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
		g.Expect(createdWorkload.Status.Admission).Should(gomega.BeNil())
	}, ConsistentDuration, ShortInterval).Should(gomega.Succeed())
}

// Verify that removing the AdmissionGatedBy annotation allows the job to be admitted.
// This can be used across all job types.
func VerifyAdmissionGatedByJobBecomesAdmissibleWhenGateRemoved(
	ctx context.Context,
	k8sClient client.Client,
	job client.Object,
	wlLookupKey types.NamespacedName,
) {
	jobLookupKey := types.NamespacedName{Name: job.GetName(), Namespace: job.GetNamespace()}
	createdWorkload := &kueue.Workload{}

	ginkgo.By("The Job  has non-empty AdmissionGatedBy before removal")
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, jobLookupKey, job)).Should(gomega.Succeed())
		annotations := job.GetAnnotations()
		g.Expect(annotations).ShouldNot(gomega.BeNil())
		g.Expect(annotations[kueueconstants.AdmissionGatedByAnnotation]).ShouldNot(gomega.BeEmpty())
	}, Timeout, Interval).Should(gomega.Succeed())

	ginkgo.By("The workload has non-empty AdmissionGatedBy before removal")
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
		g.Expect(createdWorkload.Annotations[kueueconstants.AdmissionGatedByAnnotation]).ShouldNot(gomega.BeNil())
	}, Timeout, Interval).Should(gomega.Succeed())

	ginkgo.By("Updating the job to remove the AdmissionGatedBy annotation")
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, jobLookupKey, job)).Should(gomega.Succeed())
		annotations := job.GetAnnotations()
		delete(annotations, kueueconstants.AdmissionGatedByAnnotation)
		job.SetAnnotations(annotations)
		g.Expect(k8sClient.Update(ctx, job)).Should(gomega.Succeed())
	}, Timeout, Interval).Should(gomega.Succeed())

	ginkgo.By("Waiting for the workload annotation to be removed by the controller")
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
		g.Expect(createdWorkload.Annotations).ShouldNot(gomega.HaveKey(kueueconstants.AdmissionGatedByAnnotation))
	}, Timeout, Interval).Should(gomega.Succeed())

	ginkgo.By("Checking the workload becomes admissible")
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
		hasAdmissionGate := workload.HasAdmissionGate(createdWorkload)
		g.Expect(hasAdmissionGate).ShouldNot(gomega.BeTrue())
		admissibleNowOrBefore := IsAdmissibleOrPastQuotaReservation(createdWorkload)
		g.Expect(admissibleNowOrBefore).Should(gomega.BeTrue())
	}, Timeout, Interval).Should(gomega.Succeed())

	ginkgo.By("Checking the workload gets admitted")
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
		g.Expect(createdWorkload.Status.Admission).ShouldNot(gomega.BeNil())
	}, Timeout, Interval).Should(gomega.Succeed())

	ginkgo.By("Checking the QuotaReserved condition is no longer AdmissionGated")
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
		cond := apimeta.FindStatusCondition(createdWorkload.Status.Conditions, kueue.WorkloadQuotaReserved)
		// The condition should either not exist, or exist with a reason other than AdmissionGated
		if cond != nil {
			g.Expect(cond.Reason).NotTo(gomega.Equal(kueue.WorkloadAdmissionGated))
		}
		ExpectEventAppeared(ctx, k8sClient, corev1.Event{
			Reason:  "AdmissionGateCleared",
			Type:    corev1.EventTypeNormal,
			Message: "Admission gate cleared, workload is now admissible",
		})
	}, Timeout, Interval).Should(gomega.Succeed())
}

// Removing one gate from a Job with multiple gates is allowed and the workload
// remains inadmissible until all gates are removed.
// This can be used across all job types.
func VerifyAdmissionGatedByJobAllowsRemovingOneGate(
	ctx context.Context,
	k8sClient client.Client,
	job client.Object,
	wlLookupKey types.NamespacedName,
	initialGateValue string,
	updatedGateValue string,
) {
	lookupKey := types.NamespacedName{Name: job.GetName(), Namespace: job.GetNamespace()}
	createdWorkload := &kueue.Workload{}

	ginkgo.By("Wait for the Workload to be created and have the original AdmissionGatedBy annotation")
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
		g.Expect(createdWorkload.Annotations).Should(gomega.HaveKeyWithValue(
			kueueconstants.AdmissionGatedByAnnotation, initialGateValue))
	}, Timeout, Interval).Should(gomega.Succeed())

	ginkgo.By("Updating the job to remove one gate")
	gomega.Eventually(func(g gomega.Gomega) {
		freshJob := job.DeepCopyObject().(client.Object)
		g.Expect(k8sClient.Get(ctx, lookupKey, freshJob)).Should(gomega.Succeed())
		annotations := freshJob.GetAnnotations()
		if annotations == nil {
			annotations = make(map[string]string)
		}
		annotations[kueueconstants.AdmissionGatedByAnnotation] = updatedGateValue
		freshJob.SetAnnotations(annotations)
		g.Expect(k8sClient.Update(ctx, freshJob)).Should(gomega.Succeed())
	}, Timeout, Interval).Should(gomega.Succeed())

	ginkgo.By("Waiting for the workload annotation to be updated by the controller")
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
		g.Expect(createdWorkload.Annotations).Should(gomega.HaveKey(kueueconstants.AdmissionGatedByAnnotation))
		g.Expect(createdWorkload.Annotations[kueueconstants.AdmissionGatedByAnnotation]).Should(gomega.Equal(updatedGateValue))
	}, Timeout, Interval).Should(gomega.Succeed())

	ginkgo.By("Verifying the workload remains inadmissible with the remaining gate")
	gomega.Consistently(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
		admissibleNowOrBefore := IsAdmissibleOrPastQuotaReservation(createdWorkload)
		g.Expect(admissibleNowOrBefore).Should(gomega.BeFalse())
		g.Expect(createdWorkload.Status.Admission).Should(gomega.BeNil())
	}, ConsistentDuration, ShortInterval).Should(gomega.Succeed())
}

// Verify that a job with the AdmissionGatedBy annotation gets admitted when
// the AdmissionGatedBy feature gate is disabled. The annotation should be
// ignored and the workload should be admitted normally.
// This can be used across all job types.
func VerifyJobAdmittedWhenFeatureGateDisabled(
	ctx context.Context,
	k8sClient client.Client,
	job client.Object,
	wlLookupKey types.NamespacedName,
) {
	lookupKey := types.NamespacedName{Name: job.GetName(), Namespace: job.GetNamespace()}

	ginkgo.By("Checking the job has the AdmissionGatedBy annotation")
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, lookupKey, job)).Should(gomega.Succeed())
		annotations := job.GetAnnotations()
		g.Expect(annotations).ShouldNot(gomega.BeNil())
		g.Expect(annotations[kueueconstants.AdmissionGatedByAnnotation]).ShouldNot(gomega.BeEmpty())
	}, Timeout, Interval).Should(gomega.Succeed())

	ginkgo.By("Checking the workload is created without the admission gate annotation")
	createdWorkload := &kueue.Workload{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
		g.Expect(createdWorkload.Annotations).ShouldNot(gomega.HaveKey(kueueconstants.AdmissionGatedByAnnotation))
	}, Timeout, Interval).Should(gomega.Succeed())

	ginkgo.By("Checking the workload is admissible (no gate applied)")
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
		hasAdmissionGate := workload.HasAdmissionGate(createdWorkload)
		g.Expect(hasAdmissionGate).Should(gomega.BeFalse())
		// IsAdmissible returns false when the workload is HasQuotaReservation or Finished already
		// So there's a chance the workload gets admitted before we get here to test whether it's admissible or not
		admissible := workload.IsAdmissible(createdWorkload)
		finished := workload.IsFinished(createdWorkload)
		hasQuotaReservation := workload.HasQuotaReservation(createdWorkload)
		g.Expect(admissible || finished || hasQuotaReservation).Should(gomega.BeTrue())
	}, Timeout, Interval).Should(gomega.Succeed())

	ginkgo.By("Checking the workload gets admitted")
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
		g.Expect(createdWorkload.Status.Admission).ShouldNot(gomega.BeNil())
	}, Timeout, Interval).Should(gomega.Succeed())
}
