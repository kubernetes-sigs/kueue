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
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	workloadraycluster "sigs.k8s.io/kueue/pkg/controller/jobs/raycluster"
	workloadrayjob "sigs.k8s.io/kueue/pkg/controller/jobs/rayjob"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingraycluster "sigs.k8s.io/kueue/pkg/util/testingjobs/raycluster"
	testingrayjob "sigs.k8s.io/kueue/pkg/util/testingjobs/rayjob"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Kuberay", func() {
	const (
		resourceFlavorName = "kuberay-rf"
		clusterQueueName   = "kuberay-cq"
		localQueueName     = "kuberay-lq"
	)

	var (
		ns *corev1.Namespace
		rf *kueue.ResourceFlavor
		cq *kueue.ClusterQueue
		lq *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "kuberay-e2e-")
		rf = testing.MakeResourceFlavor(resourceFlavorName).
			NodeLabel("instance-type", "on-demand").
			Obj()
		gomega.Expect(k8sClient.Create(ctx, rf)).To(gomega.Succeed())

		cq = testing.MakeClusterQueue(clusterQueueName).
			ResourceGroup(
				*testing.MakeFlavorQuotas(rf.Name).
					Resource(corev1.ResourceCPU, "1").Obj()).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())

		lq = testing.MakeLocalQueue(localQueueName, ns.Name).ClusterQueue(cq.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, lq)).To(gomega.Succeed())
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.It("Should run a rayjob if admitted", func() {
		kuberayTestImage := util.GetKuberayTestImage()

		rayJob := testingrayjob.MakeJob("rayjob", ns.Name).
			Queue(localQueueName).
			WithSubmissionMode(rayv1.K8sJobMode).
			Entrypoint("python -c \"import ray; ray.init(); print(ray.cluster_resources())\"").
			RequestAndLimit(rayv1.HeadNode, corev1.ResourceCPU, "300m").
			RequestAndLimit(rayv1.WorkerNode, corev1.ResourceCPU, "300m").
			WithSubmitterPodTemplate(corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "rayjob-submitter",
							Image: kuberayTestImage,
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU: resource.MustParse("300m"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU: resource.MustParse("300m"),
								},
							},
						},
					},
					RestartPolicy: corev1.RestartPolicyOnFailure,
				},
			}).
			Image(rayv1.HeadNode, kuberayTestImage).
			Image(rayv1.WorkerNode, kuberayTestImage).Obj()

		ginkgo.By("Creating the rayJob", func() {
			gomega.Expect(k8sClient.Create(ctx, rayJob)).Should(gomega.Succeed())
		})

		wlLookupKey := types.NamespacedName{Name: workloadrayjob.GetWorkloadNameForRayJob(rayJob.Name, rayJob.UID), Namespace: ns.Name}
		createdWorkload := &kueue.Workload{}
		ginkgo.By("Checking workload is created", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking workload is admitted", func() {
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, createdWorkload)
		})

		ginkgo.By("Waiting for the RayJob cluster become ready", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdRayJob := &rayv1.RayJob{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rayJob), createdRayJob)).To(gomega.Succeed())
				g.Expect(createdRayJob.Spec.Suspend).To(gomega.BeFalse())
				g.Expect(createdRayJob.Status.JobDeploymentStatus).To(gomega.Equal(rayv1.JobDeploymentStatusRunning))
			}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Waiting for the RayJob to finish", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdRayJob := &rayv1.RayJob{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rayJob), createdRayJob)).To(gomega.Succeed())
				g.Expect(createdRayJob.Status.JobDeploymentStatus).To(gomega.Equal(rayv1.JobDeploymentStatusComplete))
				g.Expect(createdRayJob.Status.JobStatus).To(gomega.Equal(rayv1.JobStatusSucceeded))
			}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should run a RayCluster on worker if admitted", func() {
		kuberayTestImage := util.GetKuberayTestImage()

		raycluster := testingraycluster.MakeCluster("raycluster1", ns.Name).
			Suspend(true).
			Queue(localQueueName).
			RequestAndLimit(rayv1.HeadNode, corev1.ResourceCPU, "300m").
			RequestAndLimit(rayv1.WorkerNode, corev1.ResourceCPU, "300m").
			Image(rayv1.HeadNode, kuberayTestImage, []string{}).
			Image(rayv1.WorkerNode, kuberayTestImage, []string{}).
			Obj()

		ginkgo.By("Creating the RayCluster", func() {
			gomega.Expect(k8sClient.Create(ctx, raycluster)).Should(gomega.Succeed())
		})

		wlLookupKey := types.NamespacedName{Name: workloadraycluster.GetWorkloadNameForRayCluster(raycluster.Name, raycluster.UID), Namespace: ns.Name}
		createdWorkload := &kueue.Workload{}
		ginkgo.By("Checking workload is created", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking workload is admitted", func() {
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, createdWorkload)
		})

		ginkgo.By("Checking the RayCluster is ready", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdRayCluster := &rayv1.RayCluster{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(raycluster), createdRayCluster)).To(gomega.Succeed())
				g.Expect(createdRayCluster.Status.DesiredWorkerReplicas).To(gomega.Equal(int32(1)))
				g.Expect(createdRayCluster.Status.ReadyWorkerReplicas).To(gomega.Equal(int32(1)))
				g.Expect(createdRayCluster.Status.AvailableWorkerReplicas).To(gomega.Equal(int32(1)))
			}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
