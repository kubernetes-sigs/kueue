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
	"time"

	"github.com/google/go-cmp/cmp/cmpopts"
	kfmpi "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	sparkv1beta2 "github.com/kubeflow/spark-operator/v2/api/v1beta2"
	kftrainerapi "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	awv1beta2 "github.com/project-codeflare/appwrapper/api/v1beta2"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
)

func DeleteAllCronJobsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	return deleteAllObjectsInNamespace(ctx, c, ns, &batchv1.CronJob{})
}

func DeleteAllJobsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	return deleteAllObjectsInNamespace(ctx, c, ns, &batchv1.Job{})
}

func DeleteAllJobSetsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	return deleteAllObjectsInNamespace(ctx, c, ns, &jobset.JobSet{})
}

func DeleteAllTrainJobsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	return deleteAllObjectsInNamespace(ctx, c, ns, &kftrainerapi.TrainJob{})
}

func DeleteAllTrainingRuntimesInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	return deleteAllObjectsInNamespace(ctx, c, ns, &kftrainerapi.TrainingRuntime{})
}

func DeleteAllLeaderWorkerSetsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	return deleteAllObjectsInNamespace(ctx, c, ns, &leaderworkersetv1.LeaderWorkerSet{})
}

func DeleteAllMPIJobsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	return deleteAllObjectsInNamespace(ctx, c, ns, &kfmpi.MPIJob{})
}

func DeleteAllPyTorchJobsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	return deleteAllObjectsInNamespace(ctx, c, ns, &kftraining.PyTorchJob{})
}

func DeleteAllJAXJobsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	return deleteAllObjectsInNamespace(ctx, c, ns, &kftraining.JAXJob{})
}

func DeleteAllAppWrappersInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	return deleteAllObjectsInNamespace(ctx, c, ns, &awv1beta2.AppWrapper{})
}

func DeleteAllRayJobsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	return deleteAllObjectsInNamespace(ctx, c, ns, &rayv1.RayJob{})
}

func DeleteAllRayServicesInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	return deleteAllObjectsInNamespace(ctx, c, ns, &rayv1.RayService{})
}

func DeleteAllSparkApplicationsInNamespace(ctx context.Context, c client.Client, ns *corev1.Namespace) error {
	return deleteAllObjectsInNamespace(ctx, c, ns, &sparkv1beta2.SparkApplication{})
}

func ExpectJobUnsuspended(ctx context.Context, c client.Client, key types.NamespacedName) {
	ginkgo.GinkgoHelper()
	job := &batchv1.Job{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(c.Get(ctx, key, job)).To(gomega.Succeed())
		g.Expect(job.Spec.Suspend).Should(gomega.Equal(new(false)))
	}, MediumTimeout, Interval).Should(gomega.Succeed(), AssertMsg("Job was not unsuspended", job))
}

func ExpectJobUnsuspendedWithNodeSelectors(ctx context.Context, c client.Client, key types.NamespacedName, nodeSelector map[string]string) {
	ginkgo.GinkgoHelper()
	ExpectJobUnsuspended(ctx, c, key)
	job := &batchv1.Job{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(c.Get(ctx, key, job)).To(gomega.Succeed())
		g.Expect(job.Spec.Template.Spec.NodeSelector).Should(gomega.Equal(nodeSelector))
	}, MediumTimeout, Interval).Should(gomega.Succeed(), AssertMsg("Job does not have expected node selectors", job))
}

func ExpectJobToBeRunning(ctx context.Context, c client.Client, job *batchv1.Job) {
	ginkgo.GinkgoHelper()
	createdJob := &batchv1.Job{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(c.Get(ctx, client.ObjectKeyFromObject(job), createdJob)).To(gomega.Succeed())
		g.Expect(createdJob.Status.StartTime).NotTo(gomega.BeNil())
		g.Expect(createdJob.Status.CompletionTime).To(gomega.BeNil())
	}, MediumTimeout, Interval).Should(gomega.Succeed(), AssertMsg("Job is not running", createdJob))
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
	}, timeout, Interval).Should(gomega.Succeed(), AssertMsg("Job did not complete", createdJob))
}

func ExpectJobToBeCompleted(ctx context.Context, c client.Client, job *batchv1.Job) {
	ginkgo.GinkgoHelper()
	ExpectJobToBeCompletedWithTimeout(ctx, c, job, MediumTimeout)
}
