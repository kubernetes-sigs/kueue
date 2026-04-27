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

package extended

import (
	"fmt"
	"strings"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	// "k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingrayservice "sigs.k8s.io/kueue/pkg/util/testingjobs/rayservice"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Kuberay integration", ginkgo.Label("feature:kuberay"), func() {
	var (
		ns                 *corev1.Namespace
		rf                 *kueue.ResourceFlavor
		cq                 *kueue.ClusterQueue
		lq                 *kueue.LocalQueue
		resourceFlavorName string
		clusterQueueName   string
		localQueueName     string
	)

	getRunningWorkerPodNames := func(podList *corev1.PodList) []string {
		var podNames []string
		for _, pod := range podList.Items {
			if strings.Contains(pod.Name, "workers") && pod.Status.Phase == corev1.PodRunning {
				podNames = append(podNames, pod.Name)
			}
		}
		return podNames
	}

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "kuberay-e2e-")
		resourceFlavorName = "kuberay-rf-" + ns.Name
		clusterQueueName = "kuberay-cq-" + ns.Name
		localQueueName = "kuberay-lq-" + ns.Name

		rf = utiltestingapi.MakeResourceFlavor(resourceFlavorName).
			NodeLabel("instance-type", "on-demand").
			Obj()
		gomega.Expect(k8sClient.Create(ctx, rf)).To(gomega.Succeed())

		cq = utiltestingapi.MakeClusterQueue(clusterQueueName).
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(rf.Name).
					Resource(corev1.ResourceCPU, "3").Obj()).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

		lq = utiltestingapi.MakeLocalQueue(localQueueName, ns.Name).ClusterQueue(cq.Name).Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.It("Should create redis-cleanup job when RayService with GCS FT is deleted", func() {
		kuberayTestImage := util.GetKuberayTestImage()

		// Create ConfigMap with Python script
		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "rayservice-autoscaling",
				Namespace: ns.Name,
			},
			Data: map[string]string{
				"sample_code.py": `import ray
import time

ray.init(address='auto')

@ray.remote(num_cpus=1)
def my_task(x):
    time.sleep(10)
    return x

print(ray.get([my_task.remote(i) for i in range(10)]))`,
			},
		}

		volumes := []corev1.Volume{
			{
				Name: "script-volume",
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: "rayservice-autoscaling",
						},
					},
				},
			},
		}
		volumeMounts := []corev1.VolumeMount{
			{
				Name:      "script-volume",
				MountPath: "/home/ray/samples",
			},
		}

		ginkgo.By("Creating the ConfigMap", func() {
			gomega.Expect(k8sClient.Create(ctx, configMap)).Should(gomega.Succeed())
		})

		rayService := testingrayservice.MakeService("rayservice", ns.Name).
			Suspend(true).
			Queue(localQueueName).
			Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			RequestAndLimit(rayv1.HeadNode, corev1.ResourceCPU, "300m").
			RequestAndLimit(rayv1.WorkerNode, corev1.ResourceCPU, "300m").
			Image(rayv1.HeadNode, kuberayTestImage).
			Image(rayv1.WorkerNode, kuberayTestImage).
			Volumes(rayv1.HeadNode, volumes).
			VolumeMounts(rayv1.HeadNode, volumeMounts).
			Obj()

		rayService.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.ServiceAccountName = "default"
		for i := range rayService.Spec.RayClusterSpec.WorkerGroupSpecs {
			rayService.Spec.RayClusterSpec.WorkerGroupSpecs[i].Template.Spec.ServiceAccountName = "default"
		}

		redisPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "redis",
				Namespace: ns.Name,
				Labels: map[string]string{
					"app": "redis",
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "redis",
						Image: "redis:6.2",
						Ports: []corev1.ContainerPort{
							{
								ContainerPort: 6379,
							},
						},
					},
				},
			},
		}

		redisService := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "redis",
				Namespace: ns.Name,
			},
			Spec: corev1.ServiceSpec{
				Selector: map[string]string{
					"app": "redis",
				},
				Ports: []corev1.ServicePort{
					{
						Port: 6379,
					},
				},
			},
		}

		ginkgo.By("Creating Redis Pod", func() {
			gomega.Expect(k8sClient.Create(ctx, redisPod)).Should(gomega.Succeed())
		})

		ginkgo.By("Creating Redis Service", func() {
			gomega.Expect(k8sClient.Create(ctx, redisService)).Should(gomega.Succeed())
		})

		ginkgo.By("Waiting for Redis Pod to be running", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdRedisPod := &corev1.Pod{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(redisPod), createdRedisPod)).To(gomega.Succeed())
				g.Expect(createdRedisPod.Status.Phase).To(gomega.Equal(corev1.PodRunning))
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})

		rayService.Spec.RayClusterSpec.GcsFaultToleranceOptions = &rayv1.GcsFaultToleranceOptions{
			RedisAddress: "redis:6379",
		}

		// Enable autoscaling
		enable := true
		aggressive := rayv1.UpscalingMode("Aggressive")
		idleTimeoutSeconds := int32(5)
		rayService.Spec.RayClusterSpec.EnableInTreeAutoscaling = &enable
		rayService.Spec.RayClusterSpec.AutoscalerOptions = &rayv1.AutoscalerOptions{
			UpscalingMode:      &aggressive,
			IdleTimeoutSeconds: &idleTimeoutSeconds,
		}
		// rayService.Spec.RayClusterSpec.Suspend = ptr.To(false)

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
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking workload is admitted", func() {
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, createdWorkload)
		})

		ginkgo.By("Checking RayCluster is created", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				rayClusters := &rayv1.RayClusterList{}
				g.Expect(k8sClient.List(ctx, rayClusters, client.InNamespace(ns.Name))).To(gomega.Succeed())
				g.Expect(rayClusters.Items).To(gomega.HaveLen(1), "Expected exactly one RayCluster to be created by RayService")
			}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Waiting for head pod to be created", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				pods := &corev1.PodList{}
				g.Expect(k8sClient.List(ctx, pods,
					client.InNamespace(ns.Name),
					client.MatchingLabels{
						"ray.io/node-type": "head",
					},
				)).To(gomega.Succeed())
				g.Expect(pods.Items).To(gomega.HaveLen(1), "Expected exactly one head pod")
				g.Expect(pods.Items[0].Status.Phase).To(gomega.Equal(corev1.PodRunning))
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})

		var headPodName string
		ginkgo.By("Getting head pod name", func() {
			pods := &corev1.PodList{}
			gomega.Expect(k8sClient.List(ctx, pods,
				client.InNamespace(ns.Name),
				client.MatchingLabels{
					"ray.io/node-type": "head",
				},
			)).To(gomega.Succeed())
			gomega.Expect(pods.Items).To(gomega.HaveLen(1))
			headPodName = pods.Items[0].Name
		})

		done := make(chan struct{})
		ginkgo.By("Triggering autoscaling by running tasks on head pod", func() {
			cmd := []string{
				"python", "/home/ray/samples/sample_code.py",
			}
			go func() {
				defer ginkgo.GinkgoRecover()
				defer close(done)
				stdout, stderr, err := util.KExecute(ctx, cfg, restClient, ns.Name, headPodName, "ray-head", cmd)
				if err != nil {
					fmt.Fprintf(ginkgo.GinkgoWriter, "KExecute failed: %v\nStdout: %s\nStderr: %s\n", err, string(stdout), string(stderr))
				}
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			}()
		})

		ginkgo.By("Waiting for cluster to scale up", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				pods := &corev1.PodList{}
				g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name))).To(gomega.Succeed())
				workerPodNames := getRunningWorkerPodNames(pods)
				g.Expect(len(workerPodNames)).To(gomega.BeNumerically(">", 1), "Expected cluster to scale up to more than 1 worker pod")
			}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Waiting for tasks to finish", func() {
			gomega.Eventually(done, util.LongTimeout, util.Interval).Should(gomega.BeClosed())
		})

		ginkgo.By("Deleting the RayService", func() {
			gomega.Expect(k8sClient.Delete(ctx, rayService)).Should(gomega.Succeed())
		})

		ginkgo.By("Checking that redis-cleanup job is created", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				jobs := &batchv1.JobList{}
				g.Expect(k8sClient.List(ctx, jobs,
					client.InNamespace(ns.Name),
					client.MatchingLabels{
						"ray.io/node-type": "redis-cleanup",
					},
				)).To(gomega.Succeed())
				g.Expect(jobs.Items).NotTo(gomega.BeEmpty(), "Expected at least one redis-cleanup job")
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking that redis-cleanup pod is created", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				pods := &corev1.PodList{}
				g.Expect(k8sClient.List(ctx, pods,
					client.InNamespace(ns.Name),
					client.MatchingLabels{
						"ray.io/node-type": "redis-cleanup",
					},
				)).To(gomega.Succeed())
				g.Expect(pods.Items).NotTo(gomega.BeEmpty(), "Expected at least one redis-cleanup pod")
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking that RayCluster is deleted", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				rayClusters := &rayv1.RayClusterList{}
				g.Expect(k8sClient.List(ctx, rayClusters, client.InNamespace(ns.Name))).To(gomega.Succeed())
				g.Expect(rayClusters.Items).To(gomega.BeEmpty(), "Expected RayCluster to be deleted")
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking that RayService is deleted", func() {
			createdRayService := &rayv1.RayService{}
			gomega.Eventually(func(g gomega.Gomega) {
				err := k8sClient.Get(ctx, client.ObjectKeyFromObject(rayService), createdRayService)
				g.Expect(client.IgnoreNotFound(err)).To(gomega.Succeed())
				g.Expect(apierrors.IsNotFound(err)).To(gomega.BeTrue(), "Expected RayService to be deleted")
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
