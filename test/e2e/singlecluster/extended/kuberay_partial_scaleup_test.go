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
	partialScaleUpWorkerGroup0 kueue.PodSetReference = "workers-group-0"
	partialScaleUpWorkerGroup1 kueue.PodSetReference = "workers-group-1"

	partialScaleUpHeadCPU   = "500m"
	partialScaleUpWorkerCPU = "100m"
)

// expectPodSetFlavor verifies that the PodSet was admitted with the expected flavor.
func expectPodSetFlavor(wl *kueue.Workload, podSetName kueue.PodSetReference, expectedFlavor string) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
		g.Expect(wl.Status.Admission).NotTo(gomega.BeNil())
		var found bool
		for _, psa := range wl.Status.Admission.PodSetAssignments {
			if psa.Name == podSetName {
				found = true
				g.Expect(psa.Flavors[corev1.ResourcePods]).To(gomega.Equal(kueue.ResourceFlavorReference(expectedFlavor)))
				break
			}
		}
		g.Expect(found).To(gomega.BeTrue(), "PodSet %s not found in admission assignments", podSetName)
	}, util.Timeout, util.Interval).Should(gomega.Succeed())
}

func configureTwoWorkerGroupsWithFlavors(spec *rayv1.RayClusterSpec, minReplicas int32) {
	spec.HeadGroupSpec.Template.Spec.NodeSelector = map[string]string{"instance-type": "on-demand"}

	makeWorkerGroup := func(name kueue.PodSetReference, instanceType string) rayv1.WorkerGroupSpec {
		wg := spec.WorkerGroupSpecs[0].DeepCopy()
		wg.GroupName = string(name)
		wg.Template.Spec.NodeSelector = map[string]string{"instance-type": instanceType}
		wg.Replicas = new(int32(1))
		wg.MinReplicas = new(minReplicas)
		wg.MaxReplicas = new(int32(10))
		return *wg
	}

	spec.WorkerGroupSpecs = []rayv1.WorkerGroupSpec{
		makeWorkerGroup(partialScaleUpWorkerGroup0, "on-demand"),
		makeWorkerGroup(partialScaleUpWorkerGroup1, "spot"),
	}
}

// expectInitialAdmission asserts the KEP's step 0: one head plus one worker in each worker group
// fits the quotas across both flavors and is admitted in full. Returns the admitted slice.
func expectInitialAdmission(namespace, clusterQueueName string, rayClusterKey client.ObjectKey, onDemandFlavorName, spotFlavorName string) *kueue.Workload {
	ginkgo.GinkgoHelper()
	initialSlice := util.ExpectSingleActiveWorkload(ctx, k8sClient, namespace)
	util.ExpectPodSetAdmittedCountWithTimeout(ctx, k8sClient, initialSlice, partialScaleUpWorkerGroup0, 1, util.LongTimeout)
	util.ExpectPodSetAdmittedCountWithTimeout(ctx, k8sClient, initialSlice, partialScaleUpWorkerGroup1, 1, util.LongTimeout)
	expectPodSetFlavor(initialSlice, partialScaleUpWorkerGroup0, onDemandFlavorName)
	expectPodSetFlavor(initialSlice, partialScaleUpWorkerGroup1, spotFlavorName)

	rayCluster := &rayv1.RayCluster{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, rayClusterKey, rayCluster)).To(gomega.Succeed())
		g.Expect(apimeta.IsStatusConditionTrue(rayCluster.Status.Conditions, string(rayv1.HeadPodReady))).To(gomega.BeTrue())
	}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())

	util.ExpectRayClusterWorkerPods(ctx, k8sClient, rayClusterKey, 2, 0)
	util.ExpectClusterQueueResourceUsage(ctx, k8sClient, client.ObjectKey{Name: clusterQueueName}, corev1.ResourcePods, 3)
	return initialSlice
}

// expectPartialAdmission asserts the KEP's step 1, taking over from the worker groups
// expectInitialAdmission left admitted: a scale-up that exceeds quotas across flavors is
// admitted up to available capacity per flavor, the workers it could not cover are left gated,
// and the pre-scale-up slice is finished.
//
// Returns the partially admitted slice and the pending probe.
func expectPartialAdmission(
	clusterQueueName string,
	rayClusterKey client.ObjectKey,
	initialSlice *kueue.Workload,
	wantCounts map[kueue.PodSetReference]int32,
	wantGranted map[kueue.PodSetReference]int32,
	wantBaseline map[kueue.PodSetReference]int32,
) (partialSlice, probe *kueue.Workload) {
	ginkgo.GinkgoHelper()
	partialSlice = util.ExpectNewWorkloadSliceWithTimeout(ctx, k8sClient, initialSlice, util.LongTimeout)
	var totalGranted, totalRequested int
	for group, count := range wantCounts {
		granted := wantGranted[group]
		baseline := wantBaseline[group]
		util.ExpectWorkloadPodSet(ctx, k8sClient, client.ObjectKeyFromObject(partialSlice), group, count, new(baseline))
		util.ExpectPodSetAdmittedCountWithTimeout(ctx, k8sClient, partialSlice, group, granted, util.LongTimeout)
		totalGranted += int(granted)
		totalRequested += int(count)
	}

	util.ExpectRayClusterWorkerPods(ctx, k8sClient, rayClusterKey, totalGranted, totalRequested-totalGranted)
	util.ExpectClusterQueueResourceUsage(ctx, k8sClient, client.ObjectKey{Name: clusterQueueName}, corev1.ResourcePods, int64(totalGranted+1))
	util.ExpectWorkloadToFinishWithTimeout(ctx, k8sClient, client.ObjectKeyFromObject(initialSlice), util.LongTimeout)

	probe = util.ExpectNewWorkloadSliceWithTimeout(ctx, k8sClient, partialSlice, util.LongTimeout)
	for group, count := range wantCounts {
		util.ExpectWorkloadPodSetCount(ctx, k8sClient, client.ObjectKeyFromObject(probe), group, count)
	}
	util.ExpectWorkloadsToBePendingByKeysWithTimeout(ctx, k8sClient, util.MediumTimeout, client.ObjectKeyFromObject(probe))
	return partialSlice, probe
}

// expectProbeCompletesScaleUp asserts the KEP's step 3: once capacity appears across flavors the probe
// is admitted opportunistically for the full request, replacing the partially admitted slice, and the ungater
// releases every worker that was still held back.
func expectProbeCompletesScaleUp(
	clusterQueueName string,
	rayClusterKey client.ObjectKey,
	partialSlice, probe *kueue.Workload,
	wantCounts map[kueue.PodSetReference]int32,
) {
	ginkgo.GinkgoHelper()
	var totalCount int
	for group, count := range wantCounts {
		util.ExpectPodSetAdmittedCountWithTimeout(ctx, k8sClient, probe, group, count, util.LongTimeout)
		totalCount += int(count)
	}
	util.ExpectWorkloadToFinishWithTimeout(ctx, k8sClient, client.ObjectKeyFromObject(partialSlice), util.LongTimeout)
	util.ExpectRayClusterWorkerPods(ctx, k8sClient, rayClusterKey, totalCount, 0)
	util.ExpectClusterQueueResourceUsage(ctx, k8sClient, client.ObjectKey{Name: clusterQueueName}, corev1.ResourcePods, int64(totalCount+1))
}

var _ = ginkgo.Describe("KubeRay partial replica scale-up", ginkgo.Label("area:singlecluster", "feature:kuberay"), func() {
	var (
		ns                 *corev1.Namespace
		rfOnDemand         *kueue.ResourceFlavor
		rfSpot             *kueue.ResourceFlavor
		cq                 *kueue.ClusterQueue
		lq                 *kueue.LocalQueue
		onDemandFlavorName string
		spotFlavorName     string
		clusterQueueName   string
		localQueueName     string
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "kuberay-partial-scaleup-e2e-")
		onDemandFlavorName = "kuberay-partial-rf-ondemand-" + ns.Name
		spotFlavorName = "kuberay-partial-rf-spot-" + ns.Name
		clusterQueueName = "kuberay-partial-cq-" + ns.Name
		localQueueName = "kuberay-partial-lq-" + ns.Name

		rfOnDemand = utiltestingapi.MakeResourceFlavor(onDemandFlavorName).
			NodeLabel("instance-type", "on-demand").
			Obj()
		util.MustCreate(ctx, k8sClient, rfOnDemand)

		rfSpot = utiltestingapi.MakeResourceFlavor(spotFlavorName).
			NodeLabel("instance-type", "spot").
			Obj()
		util.MustCreate(ctx, k8sClient, rfSpot)

		cq = utiltestingapi.MakeClusterQueue(clusterQueueName).
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(onDemandFlavorName).
					Resource(corev1.ResourcePods, "3").
					Resource(corev1.ResourceCPU, "10").
					Resource(corev1.ResourceMemory, "8Gi").
					Obj(),
				*utiltestingapi.MakeFlavorQuotas(spotFlavorName).
					Resource(corev1.ResourcePods, "2").
					Resource(corev1.ResourceCPU, "10").
					Resource(corev1.ResourceMemory, "8Gi").
					Obj(),
			).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

		lq = utiltestingapi.MakeLocalQueue(localQueueName, ns.Name).ClusterQueue(cq.Name).Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, rfOnDemand, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, rfSpot, true)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	// raisePodsQuotas models the KEP's "quota increases" step across multiple flavors.
	raisePodsQuotas := func(quotas map[string]string) {
		ginkgo.GinkgoHelper()
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).To(gomega.Succeed())
			for flavorName, pods := range quotas {
				util.SetFlavorResourceNominalQuota(cq, flavorName, corev1.ResourcePods, pods)
			}
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

		configureTwoWorkerGroupsWithFlavors(&rayCluster.Spec, 0)
		rayClusterKey := client.ObjectKeyFromObject(rayCluster)

		ginkgo.By("Creating the RayCluster with two worker groups referencing different flavors", func() {
			util.MustCreate(ctx, k8sClient, rayCluster)
		})

		var initialSlice *kueue.Workload
		ginkgo.By("Admitting the initial workload in full", func() {
			initialSlice = expectInitialAdmission(ns.Name, clusterQueueName, rayClusterKey, onDemandFlavorName, spotFlavorName)
		})

		// KEP step 1: scaling both worker groups to 3 replicas exceeds quotas (4 on-demand > 3, 3 spot > 2).
		// Kueue reduces worker groups sequentially (order-based) and restores capacity in giveback phase.
		// Result: 1 head + 2 worker0 on on-demand (fits 3 quota), 2 worker1 on spot (fits 2 quota).
		ginkgo.By("Scaling both worker groups to 3 replicas", func() {
			util.SetRayClusterWorkerGroupsReplicas(ctx, k8sClient, rayClusterKey, map[string]int32{
				string(partialScaleUpWorkerGroup0): 3,
				string(partialScaleUpWorkerGroup1): 3,
			}, false)
		})

		var partialSlice, probe *kueue.Workload
		ginkgo.By("Admitting only as much of the scale-up as the quotas allow", func() {
			partialSlice, probe = expectPartialAdmission(
				clusterQueueName,
				rayClusterKey,
				initialSlice,
				map[kueue.PodSetReference]int32{
					partialScaleUpWorkerGroup0: 3,
					partialScaleUpWorkerGroup1: 3,
				},
				map[kueue.PodSetReference]int32{
					partialScaleUpWorkerGroup0: 2,
					partialScaleUpWorkerGroup1: 2,
				},
				map[kueue.PodSetReference]int32{
					partialScaleUpWorkerGroup0: 1,
					partialScaleUpWorkerGroup1: 1,
				},
			)
		})

		// KEP step 2: a further scale-up arrives while the probe is still pending. The probe is
		// updated in place to carry the new target instead of a second probe being created.
		probeKey := client.ObjectKeyFromObject(probe)
		ginkgo.By("Scaling worker group 0 to 4 replicas while the probe is pending", func() {
			util.SetRayClusterWorkerGroupsReplicas(ctx, k8sClient, rayClusterKey, map[string]int32{string(partialScaleUpWorkerGroup0): 4}, false)
		})

		ginkgo.By("Updating the pending probe in place rather than adding another slice", func() {
			util.ExpectWorkloadPodSetCount(ctx, k8sClient, probeKey, partialScaleUpWorkerGroup0, 4)
			util.ExpectWorkloadPodSetCount(ctx, k8sClient, probeKey, partialScaleUpWorkerGroup1, 3)
			util.ConsistentlyActiveWorkloadNames(ctx, k8sClient, ns.Name, partialSlice.Name, probe.Name)
		})

		ginkgo.By("Holding the partially admitted slice and the newly created worker at the quota", func() {
			util.ExpectPodSetAdmittedCountWithTimeout(ctx, k8sClient, partialSlice, partialScaleUpWorkerGroup0, 2, util.LongTimeout)
			util.ExpectPodSetAdmittedCountWithTimeout(ctx, k8sClient, partialSlice, partialScaleUpWorkerGroup1, 2, util.LongTimeout)
			util.ExpectWorkloadsToBePendingByKeysWithTimeout(ctx, k8sClient, util.MediumTimeout, probeKey)
			util.ExpectRayClusterWorkerPods(ctx, k8sClient, rayClusterKey, 4, 3)
			util.ExpectClusterQueueResourceUsage(ctx, k8sClient, client.ObjectKey{Name: clusterQueueName}, corev1.ResourcePods, 5)
		})

		// KEP step 3.
		ginkgo.By("Raising the ClusterQueue's pod quotas", func() {
			raisePodsQuotas(map[string]string{
				onDemandFlavorName: "5",
				spotFlavorName:     "3",
			})
		})

		ginkgo.By("Admitting the probe for the full request and ungating the remaining workers", func() {
			expectProbeCompletesScaleUp(clusterQueueName, rayClusterKey, partialSlice, probe, map[kueue.PodSetReference]int32{
				partialScaleUpWorkerGroup0: 4,
				partialScaleUpWorkerGroup1: 3,
			})
		})

		// KEP step 4: scale down after opportunistic scale-up.
		ginkgo.By("Scaling the worker groups back down", func() {
			util.SetRayClusterWorkerGroupsReplicas(ctx, k8sClient, rayClusterKey, map[string]int32{
				string(partialScaleUpWorkerGroup0): 2,
				string(partialScaleUpWorkerGroup1): 1,
			}, false)
		})

		ginkgo.By("Recording the scale-down on the admitted slice and releasing its quota", func() {
			util.ExpectWorkloadPodSetCount(ctx, k8sClient, probeKey, partialScaleUpWorkerGroup0, 2)
			util.ExpectWorkloadPodSetCount(ctx, k8sClient, probeKey, partialScaleUpWorkerGroup1, 1)
			util.ConsistentlyActiveWorkloadNames(ctx, k8sClient, ns.Name, probe.Name)
			util.ExpectPodSetAdmittedCountWithTimeout(ctx, k8sClient, probe, partialScaleUpWorkerGroup0, 4, util.LongTimeout)
			util.ExpectPodSetAdmittedCountWithTimeout(ctx, k8sClient, probe, partialScaleUpWorkerGroup1, 3, util.LongTimeout)
			util.ExpectRayClusterWorkerPods(ctx, k8sClient, rayClusterKey, 3, 0)
			util.ExpectClusterQueueResourceUsage(ctx, k8sClient, client.ObjectKey{Name: clusterQueueName}, corev1.ResourcePods, 4)
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

		configureTwoWorkerGroupsWithFlavors(rayJob.Spec.RayClusterSpec, 1)

		ginkgo.By("Creating the RayJob with two worker groups referencing different flavors", func() {
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
			initialSlice = expectInitialAdmission(ns.Name, clusterQueueName, rayClusterKey, onDemandFlavorName, spotFlavorName)
		})

		ginkgo.By("Emulating the autoscaler raising both worker groups to 3 replicas", func() {
			util.SetRayClusterWorkerGroupsReplicas(ctx, k8sClient, rayClusterKey, map[string]int32{
				string(partialScaleUpWorkerGroup0): 3,
				string(partialScaleUpWorkerGroup1): 3,
			}, true)
		})

		var partialSlice, probe *kueue.Workload
		ginkgo.By("Admitting only as much of the scale-up as the quotas allow", func() {
			partialSlice, probe = expectPartialAdmission(
				clusterQueueName,
				rayClusterKey,
				initialSlice,
				map[kueue.PodSetReference]int32{
					partialScaleUpWorkerGroup0: 3,
					partialScaleUpWorkerGroup1: 3,
				},
				map[kueue.PodSetReference]int32{
					partialScaleUpWorkerGroup0: 2,
					partialScaleUpWorkerGroup1: 2,
				},
				map[kueue.PodSetReference]int32{
					partialScaleUpWorkerGroup0: 1,
					partialScaleUpWorkerGroup1: 1,
				},
			)
		})

		ginkgo.By("Raising the ClusterQueue's pod quotas", func() {
			raisePodsQuotas(map[string]string{
				onDemandFlavorName: "4",
				spotFlavorName:     "3",
			})
		})

		ginkgo.By("Admitting the probe for the full request and ungating the remaining workers", func() {
			expectProbeCompletesScaleUp(clusterQueueName, rayClusterKey, partialSlice, probe, map[kueue.PodSetReference]int32{
				partialScaleUpWorkerGroup0: 3,
				partialScaleUpWorkerGroup1: 3,
			})
		})

		probeKey := client.ObjectKeyFromObject(probe)
		ginkgo.By("Scaling the worker groups back down", func() {
			util.SetRayClusterWorkerGroupsReplicas(ctx, k8sClient, rayClusterKey, map[string]int32{
				string(partialScaleUpWorkerGroup0): 2,
				string(partialScaleUpWorkerGroup1): 1,
			}, true)
		})

		ginkgo.By("Recording the scale-down on the admitted slice and releasing its quota", func() {
			util.ExpectWorkloadPodSetCount(ctx, k8sClient, probeKey, partialScaleUpWorkerGroup0, 2)
			util.ExpectWorkloadPodSetCount(ctx, k8sClient, probeKey, partialScaleUpWorkerGroup1, 1)
			util.ConsistentlyActiveWorkloadNames(ctx, k8sClient, ns.Name, probe.Name)
			util.ExpectPodSetAdmittedCountWithTimeout(ctx, k8sClient, probe, partialScaleUpWorkerGroup0, 3, util.LongTimeout)
			util.ExpectPodSetAdmittedCountWithTimeout(ctx, k8sClient, probe, partialScaleUpWorkerGroup1, 3, util.LongTimeout)
			util.ExpectRayClusterWorkerPods(ctx, k8sClient, rayClusterKey, 3, 0)
			util.ExpectClusterQueueResourceUsage(ctx, k8sClient, client.ObjectKey{Name: clusterQueueName}, corev1.ResourcePods, 4)
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

		configureTwoWorkerGroupsWithFlavors(&rayService.Spec.RayClusterSpec, 1)

		ginkgo.By("Creating the ConfigMap", func() {
			util.MustCreate(ctx, k8sClient, configMap)
		})

		ginkgo.By("Creating the RayService with two worker groups referencing different flavors", func() {
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
			initialSlice = expectInitialAdmission(ns.Name, clusterQueueName, rayClusterKey, onDemandFlavorName, spotFlavorName)
		})

		ginkgo.By("Emulating the autoscaler raising both worker groups to 3 replicas", func() {
			util.SetRayClusterWorkerGroupsReplicas(ctx, k8sClient, rayClusterKey, map[string]int32{
				string(partialScaleUpWorkerGroup0): 3,
				string(partialScaleUpWorkerGroup1): 3,
			}, true)
		})

		var partialSlice, probe *kueue.Workload
		ginkgo.By("Admitting only as much of the scale-up as the quotas allow", func() {
			partialSlice, probe = expectPartialAdmission(
				clusterQueueName,
				rayClusterKey,
				initialSlice,
				map[kueue.PodSetReference]int32{
					partialScaleUpWorkerGroup0: 3,
					partialScaleUpWorkerGroup1: 3,
				},
				map[kueue.PodSetReference]int32{
					partialScaleUpWorkerGroup0: 2,
					partialScaleUpWorkerGroup1: 2,
				},
				map[kueue.PodSetReference]int32{
					partialScaleUpWorkerGroup0: 1,
					partialScaleUpWorkerGroup1: 1,
				},
			)
		})

		ginkgo.By("Raising the ClusterQueue's pod quotas", func() {
			raisePodsQuotas(map[string]string{
				onDemandFlavorName: "4",
				spotFlavorName:     "3",
			})
		})

		ginkgo.By("Admitting the probe for the full request and ungating the remaining workers", func() {
			expectProbeCompletesScaleUp(clusterQueueName, rayClusterKey, partialSlice, probe, map[kueue.PodSetReference]int32{
				partialScaleUpWorkerGroup0: 3,
				partialScaleUpWorkerGroup1: 3,
			})
		})

		probeKey := client.ObjectKeyFromObject(probe)
		ginkgo.By("Scaling the worker groups back down", func() {
			util.SetRayClusterWorkerGroupsReplicas(ctx, k8sClient, rayClusterKey, map[string]int32{
				string(partialScaleUpWorkerGroup0): 2,
				string(partialScaleUpWorkerGroup1): 1,
			}, true)
		})

		ginkgo.By("Recording the scale-down on the admitted slice and releasing its quota", func() {
			util.ExpectWorkloadPodSetCount(ctx, k8sClient, probeKey, partialScaleUpWorkerGroup0, 2)
			util.ExpectWorkloadPodSetCount(ctx, k8sClient, probeKey, partialScaleUpWorkerGroup1, 1)
			util.ConsistentlyActiveWorkloadNames(ctx, k8sClient, ns.Name, probe.Name)
			util.ExpectPodSetAdmittedCountWithTimeout(ctx, k8sClient, probe, partialScaleUpWorkerGroup0, 3, util.LongTimeout)
			util.ExpectPodSetAdmittedCountWithTimeout(ctx, k8sClient, probe, partialScaleUpWorkerGroup1, 3, util.LongTimeout)
			util.ExpectRayClusterWorkerPods(ctx, k8sClient, rayClusterKey, 3, 0)
			util.ExpectClusterQueueResourceUsage(ctx, k8sClient, client.ObjectKey{Name: clusterQueueName}, corev1.ResourcePods, 4)
		})
	})
})
