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

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func mustAdmissionCheckState(g gomega.Gomega, updatedWl *kueue.Workload, admissionCheckName string, expectedState kueue.CheckState, expectedMessage string, podSetUpdates ...kueue.PodSetUpdate) {
	ginkgo.GinkgoHelper()
	check := admissioncheck.FindAdmissionCheck(updatedWl.Status.AdmissionChecks, kueue.AdmissionCheckReference(admissionCheckName))
	g.Expect(check).NotTo(gomega.BeNil())
	g.Expect(check.State).To(gomega.Equal(expectedState))
	if expectedMessage != "" {
		g.Expect(check.Message).To(gomega.Equal(expectedMessage))
	}
	if len(podSetUpdates) > 0 {
		g.Expect(check.PodSetUpdates).To(gomega.BeComparableTo(podSetUpdates))
	}
}

func SetAdmissionCheckActive(ctx context.Context, k8sClient client.Client, admissionCheck *kueue.AdmissionCheck, status metav1.ConditionStatus) {
	var updatedAc kueue.AdmissionCheck
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(admissionCheck), &updatedAc)).Should(gomega.Succeed())
		apimeta.SetStatusCondition(&updatedAc.Status.Conditions, metav1.Condition{
			Type:    kueue.AdmissionCheckActive,
			Status:  status,
			Reason:  "ByTest",
			Message: "by test",
		})
		g.Expect(k8sClient.Status().Update(ctx, &updatedAc)).Should(gomega.Succeed())
	}, Timeout, Interval).Should(gomega.Succeed(), AssertMsg("Failed to set admission check active status", &updatedAc))
}

func ExpectAdmissionChecksToBeActive(ctx context.Context, c client.Client, acs ...*kueue.AdmissionCheck) {
	readAc := &kueue.AdmissionCheck{}
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		for _, ac := range acs {
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(ac), readAc)).To(gomega.Succeed())
			g.Expect(readAc.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.AdmissionCheckActive))
		}
	}, Timeout, Interval).Should(gomega.Succeed(), AssertMsg("AdmissionChecks did not become active", readAc))
}

func CreateAdmissionChecksAndWaitForActive(ctx context.Context, c client.Client, acs ...*kueue.AdmissionCheck) {
	ginkgo.GinkgoHelper()
	for _, ac := range acs {
		MustCreate(ctx, c, ac)
	}
	ExpectAdmissionChecksToBeActive(ctx, c, acs...)
}

func ExpectAdmissionCheckStateWithMessage(
	ctx context.Context,
	c client.Client,
	wlKey client.ObjectKey,
	admissionCheckName string,
	expectedState kueue.CheckState,
	expectedMessage string,
	podSetUpdates ...kueue.PodSetUpdate,
) {
	ginkgo.GinkgoHelper()
	updatedWl := &kueue.Workload{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(c.Get(ctx, wlKey, updatedWl)).To(gomega.Succeed())
		mustAdmissionCheckState(g, updatedWl, admissionCheckName, expectedState, expectedMessage, podSetUpdates...)
	}, MediumTimeout, Interval).Should(gomega.Succeed(), AssertMsg("Message or state did not match for the admission check", updatedWl))
}

func ExpectAdmissionCheckState(ctx context.Context, c client.Client, wlKey client.ObjectKey, admissionCheckName string, expectedState kueue.CheckState, podSetUpdates ...kueue.PodSetUpdate) {
	ginkgo.GinkgoHelper()
	ExpectAdmissionCheckStateWithMessage(ctx, c, wlKey, admissionCheckName, expectedState, "", podSetUpdates...)
}

func ConsistentlyAdmissionCheckStateWithMessage(
	ctx context.Context,
	c client.Client,
	wlKey client.ObjectKey,
	admissionCheckName string,
	expectedState kueue.CheckState,
	expectedMessage string,
	podSetUpdates ...kueue.PodSetUpdate,
) {
	ginkgo.GinkgoHelper()
	updatedWl := &kueue.Workload{}
	gomega.Consistently(func(g gomega.Gomega) {
		g.Expect(c.Get(ctx, wlKey, updatedWl)).To(gomega.Succeed())
		mustAdmissionCheckState(g, updatedWl, admissionCheckName, expectedState, expectedMessage, podSetUpdates...)
	}, ConsistentDuration, ShortInterval).Should(gomega.Succeed(), AssertMsg("Message or state did not match for the admission check", updatedWl))
}

func ConsistentlyAdmissionCheckState(ctx context.Context, c client.Client, wlKey client.ObjectKey, admissionCheckName string, expectedState kueue.CheckState, podSetUpdates ...kueue.PodSetUpdate) {
	ginkgo.GinkgoHelper()
	ConsistentlyAdmissionCheckStateWithMessage(ctx, c, wlKey, admissionCheckName, expectedState, "", podSetUpdates...)
}
