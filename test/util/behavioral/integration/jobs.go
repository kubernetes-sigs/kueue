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
	"context"
	"slices"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

// AcknowledgeJobSuspension emulates the native Job controller, which envtest does not run.
func AcknowledgeJobSuspension(ctx context.Context, k8sClient client.Client, key client.ObjectKey) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		job := &batchv1.Job{}
		g.Expect(k8sClient.Get(ctx, key, job)).To(gomega.Succeed())
		g.Expect(job.Spec.Suspend).To(gomega.Equal(new(true)))
		g.Expect(job.Annotations).To(gomega.HaveKeyWithValue(workloadjob.StoppingAnnotation, "true"))
		job.Status.Conditions = slices.DeleteFunc(job.Status.Conditions, func(condition batchv1.JobCondition) bool {
			return condition.Type == batchv1.JobSuspended
		})
		job.Status.Conditions = append(job.Status.Conditions, batchv1.JobCondition{
			Type:               batchv1.JobSuspended,
			Status:             corev1.ConditionTrue,
			Reason:             "JobSuspended",
			Message:            "Job suspended",
			LastTransitionTime: metav1.Now(),
		})
		g.Expect(k8sClient.Status().Update(ctx, job)).To(gomega.Succeed())
	}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
}
