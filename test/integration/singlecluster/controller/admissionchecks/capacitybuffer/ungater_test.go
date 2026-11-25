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

package capacitybuffer

import (
	"context"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/capacitybuffer"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	workloadpod "sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

// ungaterSetup starts only what Kueue runs with the CapacityBufferACC
// feature gate enabled on a cluster that no longer serves CapacityBuffers.
func ungaterSetup(_ context.Context, mgr manager.Manager) {
	ginkgo.GinkgoHelper()
	err := workloadpod.SetupWebhook(mgr, jobframework.WithManagedJobsNamespaceSelector(labels.Nothing()))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = capacitybuffer.NewUngater(mgr.GetClient(), nil).SetupWithManager(mgr)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
}

var _ = ginkgo.Describe("CapacityBuffer ungater", ginkgo.Label("controller:capacitybuffer", "area:admissionchecks", "feature:capacitybuffer"), func() {
	var ns *corev1.Namespace

	ginkgo.JustBeforeEach(func() {
		fwk.StartManager(ctx, cfg, ungaterSetup)
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "capacity-buffer-ungater-")
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		fwk.StopManager(ctx)
	})

	ginkgo.It("Should remove the CapacityBuffer gate from Pods when the CapacityBuffer API is not served", func() {
		const otherGate = "example.com/other"
		pod := testingpod.MakePod("gated", ns.Name).
			Gate(otherGate, kueuealpha.CapacityBufferSchedulingGate).
			Obj()

		ginkgo.By("Creating a Pod that is still gated by the CapacityBuffer check", func() {
			behavioral.MustCreate(ctx, k8sClient, pod)
		})

		ginkgo.By("Checking that only the CapacityBuffer gate is removed", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				got := &corev1.Pod{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), got)).To(gomega.Succeed())
				g.Expect(utilpod.HasGate(got, kueuealpha.CapacityBufferSchedulingGate)).To(gomega.BeFalse())
				g.Expect(utilpod.HasGate(got, otherGate)).To(gomega.BeTrue())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})
	})
})
