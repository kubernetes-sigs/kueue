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

package rayservice

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingrayservice "sigs.k8s.io/kueue/pkg/util/testingjobs/rayservice"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("RayService controller", ginkgo.Label("job:rayservice", "area:jobs"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup(jobframework.WithManageJobsWithoutQueueName(true)))
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	var (
		ns             *corev1.Namespace
		resourceFlavor *kueue.ResourceFlavor
		clusterQueue   *kueue.ClusterQueue
		localQueue     *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "rayservice-")

		resourceFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
		util.MustCreate(ctx, k8sClient, resourceFlavor)

		clusterQueue = utiltestingapi.MakeClusterQueue("default").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(resourceFlavor.Name).
				Resource(corev1.ResourceCPU, "3").
				Resource(corev1.ResourceMemory, "1Gi").
				Obj()).
			Obj()
		util.MustCreate(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("default", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		util.MustCreate(ctx, k8sClient, localQueue)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, resourceFlavor, true)
	})

	ginkgo.It("Should create redis-cleanup job when RayService with GCS FT is deleted", func() {
		rayService := testingrayservice.MakeService("rayservice", ns.Name).
			Suspend(true).
			Queue(localQueue.Name).
			Obj()

		rayService.Spec.RayClusterSpec.GcsFaultToleranceOptions = &rayv1.GcsFaultToleranceOptions{
			RedisAddress: "redis:6379",
		}

		ginkgo.By("Creating the RayService", func() {
			gomega.Expect(k8sClient.Create(ctx, rayService)).Should(gomega.Succeed())
		})

		createdWorkload := &kueue.Workload{}
		ginkgo.By("Checking workload is created", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				workloadList := &kueue.WorkloadList{}
				g.Expect(k8sClient.List(ctx, workloadList, client.InNamespace(ns.Name))).To(gomega.Succeed())
				var found bool
				for _, wl := range workloadList.Items {
					if metav1.IsControlledBy(&wl, rayService) {
						*createdWorkload = wl
						found = true
						break
					}
				}
				g.Expect(found).To(gomega.BeTrue(), "Expected to find workload controlled by RayService")
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking workload is admitted", func() {
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, createdWorkload)
		})

		// Kueue should have unsuspended the RayService after admission
		gomega.Eventually(func(g gomega.Gomega) {
			err := k8sClient.Get(ctx, client.ObjectKeyFromObject(rayService), rayService)
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(rayService.Spec.RayClusterSpec.Suspend).To(gomega.Equal(new(false)))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		// Simulate GCS FT deletion flow
		// Add finalizer to the RayService to prevent immediate deletion,
		// mimicking what KubeRay operator does.
		ginkgo.By("Adding finalizer to RayService", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				err := k8sClient.Get(ctx, client.ObjectKeyFromObject(rayService), rayService)
				g.Expect(err).NotTo(gomega.HaveOccurred())
				controllerutil.AddFinalizer(rayService, "ray.io/gcs-ft-redis-cleanup-finalizer")
				g.Expect(k8sClient.Update(ctx, rayService)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Deleting the RayService", func() {
			gomega.Expect(k8sClient.Delete(ctx, rayService)).Should(gomega.Succeed())
		})

		// Now, since KubeRay operator isn't running in envtest to create the redis-cleanup job,
		// we manually create it in the test. The job's mutating webhook will execute.
		// We set its controller owner reference to the RayService.
		redisCleanupJob := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "rayservice-redis-cleanup",
				Namespace: ns.Name,
				Labels: map[string]string{
					"ray.io/node-type": "redis-cleanup",
				},
			},
			Spec: batchv1.JobSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						RestartPolicy: corev1.RestartPolicyNever,
						Containers: []corev1.Container{
							{
								Name:  "redis-cleanup",
								Image: "redis:6.2",
							},
						},
					},
				},
			},
		}

		ginkgo.By("Creating the redis-cleanup Job with RayService owner reference", func() {
			gomega.Expect(controllerutil.SetControllerReference(rayService, redisCleanupJob, k8sClient.Scheme())).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, redisCleanupJob)).To(gomega.Succeed())
		})

		ginkgo.By("Checking that the redis-cleanup Job is NOT suspended by Kueue", func() {
			createdJob := &batchv1.Job{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(redisCleanupJob), createdJob)).To(gomega.Succeed())
				// Since its ancestor was the Kueue-managed RayService, it should NOT be suspended
				// even if it does not have a queue label.
				g.Expect(ptr.Deref(createdJob.Spec.Suspend, false)).To(gomega.BeFalse())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		// Clean up finalizer to allow namespace deletion to succeed
		ginkgo.By("Removing finalizer from RayService", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				err := k8sClient.Get(ctx, client.ObjectKeyFromObject(rayService), rayService)
				g.Expect(err).NotTo(gomega.HaveOccurred())
				if controllerutil.RemoveFinalizer(rayService, "ray.io/gcs-ft-redis-cleanup-finalizer") {
					g.Expect(k8sClient.Update(ctx, rayService)).To(gomega.Succeed())
				}
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
