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
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ExpectJobToBeCompleted waits until a Job is completed
func ExpectJobToBeCompleted(ctx context.Context, k8sClient client.Client, jobKey client.ObjectKey) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		job := &batchv1.Job{}
		g.Expect(k8sClient.Get(ctx, jobKey, job)).To(gomega.Succeed())
		g.Expect(job.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(batchv1.JobCondition{
			Type:   batchv1.JobComplete,
			Status: corev1.ConditionTrue,
		}, IgnoreJobConditionTimestamps)))
	}, MediumTimeout, Interval).Should(gomega.Succeed())
}

// ExpectJobToFail waits until a Job fails
func ExpectJobToFail(ctx context.Context, k8sClient client.Client, jobKey client.ObjectKey) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		job := &batchv1.Job{}
		g.Expect(k8sClient.Get(ctx, jobKey, job)).To(gomega.Succeed())
		g.Expect(job.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(batchv1.JobCondition{
			Type:   batchv1.JobFailed,
			Status: corev1.ConditionTrue,
		}, IgnoreJobConditionTimestamps)))
	}, MediumTimeout, Interval).Should(gomega.Succeed())
}

// ExpectJobStatus waits for a Job to have specific status
func ExpectJobStatus(ctx context.Context, k8sClient client.Client, jobKey client.ObjectKey, expectedActive, expectedSucceeded, expectedFailed int32) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		job := &batchv1.Job{}
		g.Expect(k8sClient.Get(ctx, jobKey, job)).To(gomega.Succeed())
		g.Expect(job.Status.Active).Should(gomega.Equal(expectedActive))
		g.Expect(job.Status.Succeeded).Should(gomega.Equal(expectedSucceeded))
		g.Expect(job.Status.Failed).Should(gomega.Equal(expectedFailed))
	}, Timeout, Interval).Should(gomega.Succeed())
}

var IgnoreJobConditionTimestamps = IgnoreConditionTimestamps
