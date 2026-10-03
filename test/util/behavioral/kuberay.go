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
	"sigs.k8s.io/controller-runtime/pkg/client"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
)

// ExpectRayJobPending waits until RayJob is pending
func ExpectRayJobPending(ctx context.Context, k8sClient client.Client, jobKey client.ObjectKey) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		job := &rayv1.RayJob{}
		g.Expect(k8sClient.Get(ctx, jobKey, job)).To(gomega.Succeed())
		g.Expect(job.Status.JobStatus).Should(gomega.Equal(rayv1.JobStatusPending))
	}, Timeout, Interval).Should(gomega.Succeed())
}

// ExpectRayJobRunning waits until RayJob is running
func ExpectRayJobRunning(ctx context.Context, k8sClient client.Client, jobKey client.ObjectKey) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		job := &rayv1.RayJob{}
		g.Expect(k8sClient.Get(ctx, jobKey, job)).To(gomega.Succeed())
		g.Expect(job.Status.JobStatus).Should(gomega.Equal(rayv1.JobStatusRunning))
	}, MediumTimeout, Interval).Should(gomega.Succeed())
}

// ExpectRayJobSucceeded waits until RayJob succeeds
func ExpectRayJobSucceeded(ctx context.Context, k8sClient client.Client, jobKey client.ObjectKey) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		job := &rayv1.RayJob{}
		g.Expect(k8sClient.Get(ctx, jobKey, job)).To(gomega.Succeed())
		g.Expect(job.Status.JobStatus).Should(gomega.Equal(rayv1.JobStatusSucceeded))
	}, MediumTimeout, Interval).Should(gomega.Succeed())
}

// ExpectRayJobFailed waits until RayJob fails
func ExpectRayJobFailed(ctx context.Context, k8sClient client.Client, jobKey client.ObjectKey) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		job := &rayv1.RayJob{}
		g.Expect(k8sClient.Get(ctx, jobKey, job)).To(gomega.Succeed())
		g.Expect(job.Status.JobStatus).Should(gomega.Equal(rayv1.JobStatusFailed))
	}, MediumTimeout, Interval).Should(gomega.Succeed())
}
