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
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingraycluster "sigs.k8s.io/kueue/pkg/util/testingjobs/raycluster"
	testingrayjob "sigs.k8s.io/kueue/pkg/util/testingjobs/rayjob"
	testingrayservice "sigs.k8s.io/kueue/pkg/util/testingjobs/rayservice"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/util"
)

const (
	partialScaleUpWorkerGroup kueue.PodSetReference = "workers-group-0"

	partialScaleUpHeadCPU   = "500m"
	partialScaleUpWorkerCPU = "100m"
)

// expectInitialAdmission asserts the KEP's step 0: one head plus one worker fits the quota and is
// admitted in full. Returns the admitted slice.
func expectInitialAdmission(namespace, clusterQueueName string, rayClusterKey client.ObjectKey) *kueue.Workload {
	ginkgo.GinkgoHelper()
	initialSlice := util.ExpectSingleActiveWorkload(ctx, k8sClient, namespace)
	util.ExpectPodSetAdmittedCountWithTimeout(ctx, k8sClient, initialSlice, partialScaleUpWorkerGroup, 1, util.LongTimeout)

	rayCluster := &rayv1.RayCluster{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, rayClusterKey, rayCluster)).To(gomega.Succeed())
		g.Expect(apimeta.IsStatusConditionTrue(rayCluster.Status.Conditions, string(rayv1.HeadPodReady))).To(gomega.BeTrue())
	}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())

	util.ExpectRayClusterWorkerPods(ctx, k8sClient, rayClusterKey, 1, 0)
	util.ExpectClusterQueueResourceUsage(ctx, k8sClient, client.ObjectKey{Name: clusterQueueName}, corev1.ResourcePods, 2)
	return initialSlice
}

// expectPartialAdmission asserts the KEP's step 1, taking over from the single worker
// expectInitialAdmission left admitted: a scale-up to wantCount workers that does not fit the quota
// is admitted for only wantGranted of them instead of being rejected, the workers it could not
// cover are left gated, and the pre-scale-up slice is finished.
//
// Returns the partially admitted slice and the pending probe.
func expectPartialAdmission(
	clusterQueueName string,
	rayClusterKey client.ObjectKey,
	initialSlice *kueue.Workload,
	wantCount, wantGranted int32,
) (partialSlice, probe *kueue.Workload) {
	ginkgo.GinkgoHelper()
	partialSlice = util.ExpectNewWorkloadSliceWithTimeout(ctx, k8sClient, initialSlice, util.LongTimeout)
	util.ExpectWorkloadPodSet(ctx, k8sClient, client.ObjectKeyFromObject(partialSlice), partialScaleUpWorkerGroup, wantCount, new(int32(1)))

	util.ExpectPodSetAdmittedCountWithTimeout(ctx, k8sClient, partialSlice, partialScaleUpWorkerGroup, wantGranted, util.LongTimeout)
	util.ExpectRayClusterWorkerPods(ctx, k8sClient, rayClusterKey, int(wantGranted), int(wantCount-wantGranted))
	util.ExpectClusterQueueResourceUsage(ctx, k8sClient, client.ObjectKey{Name: clusterQueueName}, corev1.ResourcePods, int64(wantGranted)+1)
	util.ExpectWorkloadToFinishWithTimeout(ctx, k8sClient, client.ObjectKeyFromObject(initialSlice), util.LongTimeout)

	probe = util.ExpectNewWorkloadSliceWithTimeout(ctx, k8sClient, partialSlice, util.LongTimeout)
	util.ExpectWorkloadPodSetCount(ctx, k8sClient, client.ObjectKeyFromObject(probe), partialScaleUpWorkerGroup, wantCount)
	util.ExpectWorkloadsToBePendingByKeysWithTimeout(ctx, k8sClient, util.MediumTimeout, client.ObjectKeyFromObject(probe))
	return partialSlice, probe
}

// expectProbeCompletesScaleUp asserts the KEP's step 3: once capacity appears the probe is admitted
// opportunistically for the full request, replacing the partially admitted slice, and the ungater
// releases every worker that was still held back.
func expectProbeCompletesScaleUp(
	clusterQueueName string,
	rayClusterKey client.ObjectKey,
	partialSlice, probe *kueue.Workload,
	wantCount int32,
) {
	ginkgo.GinkgoHelper()
	util.ExpectPodSetAdmittedCountWithTimeout(ctx, k8sClient, probe, partialScaleUpWorkerGroup, wantCount, util.LongTimeout)
	util.ExpectWorkloadToFinishWithTimeout(ctx, k8sClient, client.ObjectKeyFromObject(partialSlice), util.LongTimeout)
	util.ExpectRayClusterWorkerPods(ctx, k8sClient, rayClusterKey, int(wantCount), 0)
	util.ExpectClusterQueueResourceUsage(ctx, k8sClient, client.ObjectKey{Name: clusterQueueName}, corev1.ResourcePods, int64(wantCount)+1)
}

var _ = ginkgo.Describe("KubeRay partial replica scale-up", ginkgo.Label("area:singlecluster", "feature:kuberay"), func() {
	var (
		ns                 *corev1.Namespace
		rf                 *kueue.ResourceFlavor
		cq                 *kueue.ClusterQueue
		lq                 *kueue.LocalQueue
		resourceFlavorName string
		clusterQueueName   string
		localQueueName     string
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "kuberay-partial-scaleup-e2e-")
		resourceFlavorName = "kuberay-partial-rf-" + ns.Name
		clusterQueueName = "kuberay-partial-cq-" + ns.Name
		localQueueName = "kuberay-partial-lq-" + ns.Name

		rf = utiltestingapi.MakeResourceFlavor(resourceFlavorName).
			NodeLabel("instance-type", "on-demand").
			Obj()
		util.MustCreate(ctx, k8sClient, rf)

		cq = utiltestingapi.MakeClusterQueue(clusterQueueName).
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(resourceFlavorName).
					Resource(corev1.ResourcePods, "3").
					Resource(corev1.ResourceCPU, "10").
					Resource(corev1.ResourceMemory, "8Gi").
					Obj()).
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

	// raisePodsQuota models the KEP's "quota increases" step.
	raisePodsQuota := func(pods string) {
		ginkgo.GinkgoHelper()
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).To(gomega.Succeed())
			util.SetResourceNominalQuota(cq, corev1.ResourcePods, pods)
			g.Expect(k8sClient.Update(ctx, cq)).To(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	}

	ginkgo.It("Should partially admit a RayCluster scale-up and finish it opportunistically", ginkgo.Label("shard:kuberay-a"), func() {
		kuberayTestImage := util.GetKuberayTestImage()

		rayCluster := testingraycluster.MakeCluster("raycluster-partial-scaleup", ns.Name).
			Suspend(true).
			Queue(localQueueName).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			SetAnnotation(constants.ElasticJobScaleUpStrategyAnnotationKey, constants.ElasticJobScaleUpStrategyPartial).
			Request(rayv1.HeadNode, corev1.ResourceCPU, partialScaleUpHeadCPU).
			RayStartParam(rayv1.HeadNode, "object-store-memory", objectStoreMemory).
			Request(rayv1.WorkerNode, corev1.ResourceCPU, partialScaleUpWorkerCPU).
			RayStartParam(rayv1.WorkerNode, "object-store-memory", objectStoreMemory).
			Image(rayv1.HeadNode, kuberayTestImage, []string{}).
			Image(rayv1.WorkerNode, kuberayTestImage, []string{}).
			Obj()
		rayClusterKey := client.ObjectKeyFromObject(rayCluster)

		ginkgo.By("Creating the RayCluster with a single worker", func() {
			util.MustCreate(ctx, k8sClient, rayCluster)
		})

		var initialSlice *kueue.Workload
		ginkgo.By("Admitting the initial workload in full", func() {
			initialSlice = expectInitialAdmission(ns.Name, clusterQueueName, rayClusterKey)
		})

		// KEP step 1: the full request of 1 head + 3 workers exceeds the 3-Pod quota, so instead of
		// being rejected the scale-up is admitted up to the available capacity.
		ginkgo.By("Scaling the worker group to 3 replicas", func() {
			util.SetRayClusterWorkerReplicas(ctx, k8sClient, rayClusterKey, 3, false)
		})

		var partialSlice, probe *kueue.Workload
		ginkgo.By("Admitting only as much of the scale-up as the quota allows", func() {
			partialSlice, probe = expectPartialAdmission(clusterQueueName, rayClusterKey, initialSlice, 3, 2)
		})

		// KEP step 2: a further scale-up arrives while the probe is still pending. The probe is
		// updated in place to carry the new target instead of a second probe being created, so the
		// job never accumulates a chain of never-admitted Workloads.
		probeKey := client.ObjectKeyFromObject(probe)
		ginkgo.By("Scaling the worker group to 4 replicas while the probe is pending", func() {
			util.SetRayClusterWorkerReplicas(ctx, k8sClient, rayClusterKey, 4, false)
		})

		ginkgo.By("Updating the pending probe in place rather than adding another slice", func() {
			util.ExpectWorkloadPodSetCount(ctx, k8sClient, probeKey, partialScaleUpWorkerGroup, 4)
			util.ConsistentlyActiveWorkloadNames(ctx, k8sClient, ns.Name, partialSlice.Name, probe.Name)
		})

		ginkgo.By("Holding the partially admitted slice and the newly created worker at the quota", func() {
			util.ExpectPodSetAdmittedCountWithTimeout(ctx, k8sClient, partialSlice, partialScaleUpWorkerGroup, 2, util.LongTimeout)
			util.ExpectWorkloadsToBePendingByKeysWithTimeout(ctx, k8sClient, util.MediumTimeout, probeKey)
			util.ExpectRayClusterWorkerPods(ctx, k8sClient, rayClusterKey, 2, 2)
			util.ExpectClusterQueueResourceUsage(ctx, k8sClient, client.ObjectKey{Name: clusterQueueName}, corev1.ResourcePods, 3)
		})

		// KEP step 3.
		ginkgo.By("Raising the ClusterQueue's pod quota from 3 to 5", func() {
			raisePodsQuota("5")
		})

		ginkgo.By("Admitting the probe for the full request and ungating the remaining workers", func() {
			expectProbeCompletesScaleUp(clusterQueueName, rayClusterKey, partialSlice, probe, 4)
		})

		// KEP step 4: scale down after opportunistic scale-up.
		ginkgo.By("Scaling the worker group back down to 2 replicas", func() {
			util.SetRayClusterWorkerReplicas(ctx, k8sClient, rayClusterKey, 2, false)
		})

		ginkgo.By("Recording the scale-down on the admitted slice and releasing its quota", func() {
			util.ExpectWorkloadPodSetCount(ctx, k8sClient, probeKey, partialScaleUpWorkerGroup, 2)
			util.ConsistentlyActiveWorkloadNames(ctx, k8sClient, ns.Name, probe.Name)
			util.ExpectPodSetAdmittedCountWithTimeout(ctx, k8sClient, probe, partialScaleUpWorkerGroup, 4, util.LongTimeout)
			util.ExpectRayClusterWorkerPods(ctx, k8sClient, rayClusterKey, 2, 0)
			util.ExpectClusterQueueResourceUsage(ctx, k8sClient, client.ObjectKey{Name: clusterQueueName}, corev1.ResourcePods, 3)
		})
	})

	ginkgo.It("Should partially admit a RayJob scale-up and finish it opportunistically", ginkgo.Label("shard:kuberay-a"), func() {
		kuberayTestImage := util.GetKuberayTestImage()

		rayJob := testingrayjob.MakeJob("rayjob-partial-scaleup", ns.Name).
			Queue(localQueueName).
			Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			Annotation(constants.ElasticJobScaleUpStrategyAnnotationKey, constants.ElasticJobScaleUpStrategyPartial).
			EnableInTreeAutoscaling().
			WithSubmissionMode(rayv1.InteractiveMode).
			Request(rayv1.HeadNode, corev1.ResourceCPU, partialScaleUpHeadCPU).
			RayStartParam(rayv1.HeadNode, "object-store-memory", objectStoreMemory).
			Request(rayv1.WorkerNode, corev1.ResourceCPU, partialScaleUpWorkerCPU).
			RayStartParam(rayv1.WorkerNode, "object-store-memory", objectStoreMemory).
			Image(rayv1.HeadNode, kuberayTestImage).
			Image(rayv1.WorkerNode, kuberayTestImage).
			TerminationGracePeriod(1).
			Obj()

		ginkgo.By("Creating the RayJob with a single worker", func() {
			util.MustCreate(ctx, k8sClient, rayJob)
		})

		var rayClusterKey client.ObjectKey
		ginkgo.By("Waiting for the RayJob's cluster to be provisioned", func() {
			createdRayJob := &rayv1.RayJob{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rayJob), createdRayJob)).To(gomega.Succeed())
				g.Expect(createdRayJob.Spec.Suspend).To(gomega.BeFalse())
				g.Expect(createdRayJob.Status.RayClusterName).NotTo(gomega.BeEmpty())
			}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed(),
				util.AssertMsg("RayJob did not get a RayCluster", createdRayJob))
			rayClusterKey = client.ObjectKey{Namespace: ns.Name, Name: createdRayJob.Status.RayClusterName}
		})

		var initialSlice *kueue.Workload
		ginkgo.By("Admitting the initial workload in full", func() {
			initialSlice = expectInitialAdmission(ns.Name, clusterQueueName, rayClusterKey)
		})

		ginkgo.By("Emulating the autoscaler raising the worker group to 3 replicas", func() {
			util.SetRayClusterWorkerReplicas(ctx, k8sClient, rayClusterKey, 3, true)
		})

		var partialSlice, probe *kueue.Workload
		ginkgo.By("Admitting only as much of the scale-up as the quota allows", func() {
			partialSlice, probe = expectPartialAdmission(clusterQueueName, rayClusterKey, initialSlice, 3, 2)
		})

		ginkgo.By("Raising the ClusterQueue's pod quota from 3 to 4", func() {
			raisePodsQuota("4")
		})

		ginkgo.By("Admitting the probe for the full request and ungating the remaining worker", func() {
			expectProbeCompletesScaleUp(clusterQueueName, rayClusterKey, partialSlice, probe, 3)
		})

		probeKey := client.ObjectKeyFromObject(probe)
		ginkgo.By("Scaling the worker group back down to 2 replicas", func() {
			util.SetRayClusterWorkerReplicas(ctx, k8sClient, rayClusterKey, 2, true)
		})

		ginkgo.By("Recording the scale-down on the admitted slice and releasing its quota", func() {
			util.ExpectWorkloadPodSetCount(ctx, k8sClient, probeKey, partialScaleUpWorkerGroup, 2)
			util.ConsistentlyActiveWorkloadNames(ctx, k8sClient, ns.Name, probe.Name)
			util.ExpectPodSetAdmittedCountWithTimeout(ctx, k8sClient, probe, partialScaleUpWorkerGroup, 3, util.LongTimeout)
			util.ExpectRayClusterWorkerPods(ctx, k8sClient, rayClusterKey, 2, 0)
			util.ExpectClusterQueueResourceUsage(ctx, k8sClient, client.ObjectKey{Name: clusterQueueName}, corev1.ResourcePods, 3)
		})
	})

	ginkgo.It("Should partially admit a RayService scale-up and finish it opportunistically", ginkgo.Label("shard:kuberay-b"), func() {
		kuberayTestImage := util.GetKuberayTestImage()

		configMap := &corev1.ConfigMap{
			Name:      "rayservice-partial-scaleup",
			Namespace: ns.Name,
			Data: map[string]string{
				"hello_serve.py": `from ray import serve

@serve.deployment
class HelloWorld:
    async def __call__(self, request):
        return "Hello, World!"

app = HelloWorld.bind()`,
			},
		}
		serveConfigV2 := `applications:
  - name: hello_app
    import_path: hello_serve:app
    route_prefix: /
    deployments:
      - name: HelloWorld
        num_replicas: 1
        ray_actor_options:
          num_cpus: 0`

		volumes := []corev1.Volume{
			{
				Name: "code-sample",
				ConfigMap: &corev1.ConfigMapVolumeSource{
					Name: configMap.Name,
				},
			},
		}
		volumeMounts := []corev1.VolumeMount{
			{
				Name:      "code-sample",
				MountPath: "/home/ray/samples",
			},
		}
		env := []corev1.EnvVar{
			{
				Name:  "PYTHONPATH",
				Value: "/home/ray/samples:$PYTHONPATH",
			},
		}

		rayService := testingrayservice.MakeService("rayservice-partial-scaleup", ns.Name).
			Suspend(true).
			Queue(localQueueName).
			Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			Annotation(constants.ElasticJobScaleUpStrategyAnnotationKey, constants.ElasticJobScaleUpStrategyPartial).
			EnableInTreeAutoscaling().
			WithServeConfigV2(serveConfigV2).
			Request(rayv1.HeadNode, corev1.ResourceCPU, partialScaleUpHeadCPU).
			RayStartParam(rayv1.HeadNode, "object-store-memory", objectStoreMemory).
			Request(rayv1.WorkerNode, corev1.ResourceCPU, partialScaleUpWorkerCPU).
			Image(rayv1.HeadNode, kuberayTestImage).
			Image(rayv1.WorkerNode, kuberayTestImage).
			Env(rayv1.HeadNode, env).
			Env(rayv1.WorkerNode, env).
			Volumes(rayv1.HeadNode, volumes).
			Volumes(rayv1.WorkerNode, volumes).
			VolumeMounts(rayv1.HeadNode, volumeMounts).
			VolumeMounts(rayv1.WorkerNode, volumeMounts).
			TerminationGracePeriod(1).
			Obj()
		rayService.Spec.RayClusterSpec.WorkerGroupSpecs[0].MinReplicas = new(int32(1))

		ginkgo.By("Creating the ConfigMap", func() {
			util.MustCreate(ctx, k8sClient, configMap)
		})

		ginkgo.By("Creating the RayService with a single worker", func() {
			util.MustCreate(ctx, k8sClient, rayService)
		})

		var rayClusterKey client.ObjectKey
		ginkgo.By("Waiting for the RayService to be ready to serve traffic", func() {
			createdRayService := util.WaitForRayServiceReadyToServe(ctx, k8sClient, client.ObjectKeyFromObject(rayService))
			rayClusterKey = client.ObjectKey{
				Namespace: ns.Name,
				Name:      createdRayService.Status.ActiveServiceStatus.RayClusterName,
			}
			gomega.Expect(rayClusterKey.Name).NotTo(gomega.BeEmpty())
		})

		var initialSlice *kueue.Workload
		ginkgo.By("Admitting the initial workload in full", func() {
			initialSlice = expectInitialAdmission(ns.Name, clusterQueueName, rayClusterKey)
		})

		ginkgo.By("Emulating the autoscaler raising the worker group to 3 replicas", func() {
			util.SetRayClusterWorkerReplicas(ctx, k8sClient, rayClusterKey, 3, true)
		})

		var partialSlice, probe *kueue.Workload
		ginkgo.By("Admitting only as much of the scale-up as the quota allows", func() {
			partialSlice, probe = expectPartialAdmission(clusterQueueName, rayClusterKey, initialSlice, 3, 2)
		})

		ginkgo.By("Raising the ClusterQueue's pod quota from 3 to 4", func() {
			raisePodsQuota("4")
		})

		ginkgo.By("Admitting the probe for the full request and ungating the remaining worker", func() {
			expectProbeCompletesScaleUp(clusterQueueName, rayClusterKey, partialSlice, probe, 3)
		})

		probeKey := client.ObjectKeyFromObject(probe)
		ginkgo.By("Scaling the worker group back down to 2 replicas", func() {
			util.SetRayClusterWorkerReplicas(ctx, k8sClient, rayClusterKey, 2, true)
		})

		ginkgo.By("Recording the scale-down on the admitted slice and releasing its quota", func() {
			util.ExpectWorkloadPodSetCount(ctx, k8sClient, probeKey, partialScaleUpWorkerGroup, 2)
			util.ConsistentlyActiveWorkloadNames(ctx, k8sClient, ns.Name, probe.Name)
			util.ExpectPodSetAdmittedCountWithTimeout(ctx, k8sClient, probe, partialScaleUpWorkerGroup, 3, util.LongTimeout)
			util.ExpectRayClusterWorkerPods(ctx, k8sClient, rayClusterKey, 2, 0)
			util.ExpectClusterQueueResourceUsage(ctx, k8sClient, client.ObjectKey{Name: clusterQueueName}, corev1.ResourcePods, 3)
		})
	})
})
