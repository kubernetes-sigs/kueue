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

package e2e

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

func ExpectJobToBeRunning(ctx context.Context, c client.Client, job *batchv1.Job) {
	ginkgo.GinkgoHelper()
	createdJob := &batchv1.Job{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(c.Get(ctx, client.ObjectKeyFromObject(job), createdJob)).To(gomega.Succeed())
		g.Expect(createdJob.Status.StartTime).NotTo(gomega.BeNil())
		g.Expect(createdJob.Status.CompletionTime).To(gomega.BeNil())
	}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed(), behavioral.AssertMsg("Job is not running", createdJob))
}

func ExpectJobToBeCompletedWithTimeout(ctx context.Context, c client.Client, job *batchv1.Job, timeout time.Duration) {
	ginkgo.GinkgoHelper()
	createdJob := &batchv1.Job{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(c.Get(ctx, client.ObjectKeyFromObject(job), createdJob)).To(gomega.Succeed())
		g.Expect(createdJob.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(
			batchv1.JobCondition{
				Type:   batchv1.JobComplete,
				Status: corev1.ConditionTrue,
			},
			cmpopts.IgnoreFields(batchv1.JobCondition{}, "LastTransitionTime", "LastProbeTime", "Reason", "Message"))))
	}, timeout, behavioral.Interval).Should(gomega.Succeed(), behavioral.AssertMsg("Job did not complete", createdJob))
}

func ExpectJobToBeCompleted(ctx context.Context, c client.Client, job *batchv1.Job) {
	ginkgo.GinkgoHelper()
	ExpectJobToBeCompletedWithTimeout(ctx, c, job, behavioral.MediumTimeout)
}

func IsDeepIntegrationEnabled() bool {
	return os.Getenv("E2E_CONFIG_FOLDER") == "tasapi/deepintegration" ||
		strings.Contains(os.Getenv("E2E_EXTRA_KUEUE_FEATURE_GATES"), string(features.SchedulerLibraryDeepIntegration)+"=true") ||
		strings.Contains(ginkgo.GinkgoLabelFilter(), "feature:was_deepintegration") ||
		strings.Contains(ginkgo.CurrentSpecReport().FileName(), "deepintegration")
}

func ExpectPodsScheduledAndRunning(ctx context.Context, c client.Client, ns string, count int, opts ...client.ListOption) {
	ginkgo.GinkgoHelper()
	if !IsDeepIntegrationEnabled() {
		return
	}
	listOpts := append([]client.ListOption{client.InNamespace(ns)}, opts...)
	gomega.Eventually(func(g gomega.Gomega) {
		pods := &corev1.PodList{}
		g.Expect(c.List(ctx, pods, listOpts...)).To(gomega.Succeed())
		g.Expect(pods.Items).Should(gomega.HaveLen(count))
		for _, pod := range pods.Items {
			g.Expect(pod.Spec.NodeName).ShouldNot(gomega.BeEmpty())
			g.Expect(pod.Status.Phase).To(gomega.Or(
				gomega.Equal(corev1.PodRunning),
				gomega.Equal(corev1.PodSucceeded),
			))
		}
	}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
}
