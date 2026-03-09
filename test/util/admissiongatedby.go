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
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	kueueconstants "sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/workload"
)

// Verify that a job with the AdmissionGatedBy annotation is inadmissible.
// This can be used across all job types.
func VerifyAdmissionGatedByJobIsInadmissible(
	ctx context.Context,
	k8sClient client.Client,
	job client.Object,
	workloadName string,
	admissionGateValue string,
) {
	lookupKey := types.NamespacedName{Name: job.GetName(), Namespace: job.GetNamespace()}

	ginkgo.By("Checking the workload is created with the admission gate annotation")
	wlLookupKey := types.NamespacedName{
		Name:      workloadName,
		Namespace: job.GetNamespace(),
	}
	createdWorkload := &kueue.Workload{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
		g.Expect(createdWorkload.Annotations).Should(gomega.HaveKeyWithValue(
			kueueconstants.AdmissionGatedByAnnotation, admissionGateValue))
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
