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

package integration

import (
	"strconv"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/cmd/experimental/kueue-priority-booster/pkg/constants"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("PriorityBoostReconciler envtest", ginkgo.Serial, func() {
	var ns *corev1.Namespace

	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup)
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "priority-boost-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		if ns != nil {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			ns = nil
		}
	})

	admittedWorkloadKey := func(name string) client.ObjectKey {
		return client.ObjectKey{Namespace: ns.Name, Name: name}
	}

	setAdmittedCondition := func(wl *kueue.Workload, admittedAt metav1.Time) {
		admitted := metav1.Condition{
			Type:               string(kueue.WorkloadAdmitted),
			Status:             metav1.ConditionTrue,
			LastTransitionTime: admittedAt,
			Reason:             "Admitted",
		}
		apimeta.SetStatusCondition(&wl.Status.Conditions, admitted)
	}

	ginkgo.It("does not set boost annotation during time-sharing window", func() {
		wl := utiltestingapi.MakeWorkload("wl-in-window", ns.Name).
			Request(corev1.ResourceCPU, "1").
			Obj()
		gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, admittedWorkloadKey(wl.Name), wl)).To(gomega.Succeed())
			setAdmittedCondition(wl, metav1.NewTime(time.Now().Add(-5*time.Second)))
			g.Expect(k8sClient.Status().Update(ctx, wl)).To(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		gomega.Consistently(func(g gomega.Gomega) {
			var got kueue.Workload
			g.Expect(k8sClient.Get(ctx, admittedWorkloadKey("wl-in-window"), &got)).To(gomega.Succeed())
			_, has := got.Annotations[constants.PriorityBoostAnnotationKey]
			g.Expect(has).To(gomega.BeFalse())
		}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
	})

	ginkgo.It("sets negative boost annotation after time-sharing window", func() {
		wl := utiltestingapi.MakeWorkload("wl-post-window", ns.Name).
			Request(corev1.ResourceCPU, "1").
			Obj()
		gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, admittedWorkloadKey(wl.Name), wl)).To(gomega.Succeed())
			setAdmittedCondition(wl, metav1.NewTime(time.Now().Add(-integrationTimeSharingWindow-10*time.Second)))
			g.Expect(k8sClient.Status().Update(ctx, wl)).To(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		gomega.Eventually(func(g gomega.Gomega) {
			var got kueue.Workload
			g.Expect(k8sClient.Get(ctx, admittedWorkloadKey("wl-post-window"), &got)).To(gomega.Succeed())
			val, ok := got.Annotations[constants.PriorityBoostAnnotationKey]
			g.Expect(ok).To(gomega.BeTrue())
			want := strconv.FormatInt(-int64(integrationNegativeBoost), 10)
			g.Expect(val).To(gomega.Equal(want))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})
})
