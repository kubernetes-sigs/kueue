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
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

// ExpectAdmissionCheckStateWithMessage waits until an admission check reaches expected state with message
func ExpectAdmissionCheckStateWithMessage(
	ctx context.Context,
	k8sClient client.Client,
	wlKey client.ObjectKey,
	acName string,
	expectedState kueue.CheckState,
	expectedMessage string,
) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		wl := &kueue.Workload{}
		g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
		g.Expect(wl.Status.AdmissionChecks).ShouldNot(gomega.BeEmpty())

		var acStatus *kueue.AdmissionCheckState
		for i := range wl.Status.AdmissionChecks {
			if wl.Status.AdmissionChecks[i].Name == kueue.AdmissionCheckReference(acName) {
				acStatus = &wl.Status.AdmissionChecks[i]
				break
			}
		}
		g.Expect(acStatus).ShouldNot(gomega.BeNil(), fmt.Sprintf("AdmissionCheck %s not found", acName))
		g.Expect(acStatus.State).Should(gomega.Equal(expectedState))
		g.Expect(acStatus.Message).Should(gomega.ContainSubstring(expectedMessage))
	}, Timeout, Interval).Should(gomega.Succeed())
}

// ExpectAdmissionCheckState waits until an admission check reaches expected state
func ExpectAdmissionCheckState(
	ctx context.Context,
	k8sClient client.Client,
	wlKey client.ObjectKey,
	acName string,
	expectedState kueue.CheckState,
) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		wl := &kueue.Workload{}
		g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
		g.Expect(wl.Status.AdmissionChecks).ShouldNot(gomega.BeEmpty())

		var acStatus *kueue.AdmissionCheckState
		for i := range wl.Status.AdmissionChecks {
			if wl.Status.AdmissionChecks[i].Name == kueue.AdmissionCheckReference(acName) {
				acStatus = &wl.Status.AdmissionChecks[i]
				break
			}
		}
		g.Expect(acStatus).ShouldNot(gomega.BeNil(), fmt.Sprintf("AdmissionCheck %s not found", acName))
		g.Expect(acStatus.State).Should(gomega.Equal(expectedState))
	}, Timeout, Interval).Should(gomega.Succeed())
}

// ExpectAllAdmissionChecksInState waits until all admission checks are in expected state
func ExpectAllAdmissionChecksInState(
	ctx context.Context,
	k8sClient client.Client,
	wlKey client.ObjectKey,
	expectedState kueue.CheckState,
) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		wl := &kueue.Workload{}
		g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
		g.Expect(wl.Status.AdmissionChecks).ShouldNot(gomega.BeEmpty())

		for _, acStatus := range wl.Status.AdmissionChecks {
			g.Expect(acStatus.State).Should(gomega.Equal(expectedState))
		}
	}, Timeout, Interval).Should(gomega.Succeed())
}
