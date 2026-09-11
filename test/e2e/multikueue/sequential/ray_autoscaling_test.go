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

package sequential

import (
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueueconfig "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	workloadraycluster "sigs.k8s.io/kueue/pkg/controller/jobs/raycluster"
	workloadrayjob "sigs.k8s.io/kueue/pkg/controller/jobs/rayjob"
	"sigs.k8s.io/kueue/pkg/util/podset"
	testingraycluster "sigs.k8s.io/kueue/pkg/util/testingjobs/raycluster"
	testingrayjob "sigs.k8s.io/kueue/pkg/util/testingjobs/rayjob"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/util"
)

type rayAutoscalingTestContext struct {
	managerNs      *corev1.Namespace
	managerCq      *kueue.ClusterQueue
	managerLq      *kueue.LocalQueue
	multiKueueAc   *kueue.AdmissionCheck
	workerCluster1 *kueue.MultiKueueCluster
	workerCluster2 *kueue.MultiKueueCluster
}

type rayKubernetesClientsMap map[string]struct {
	client     client.Client
	restClient *rest.RESTClient
	cfg        *rest.Config
}

const rayActorNamespace = "kueue-e2e"

func createDetachedActorScript(actorName, resourceName string) string {
	return fmt.Sprintf(`import ray

ray.init(namespace=%q)

@ray.remote(num_cpus=0, resources={%q: 1})
class Actor:
    pass

try:
    ray.get_actor(%q)
except ValueError:
    Actor.options(name=%q, lifetime="detached").remote()
`, rayActorNamespace, resourceName, actorName, actorName)
}

func terminateDetachedActorScript(actorName string) string {
	return fmt.Sprintf(`import ray

ray.init(namespace=%q)
try:
    actor = ray.get_actor(%q)
except ValueError:
    pass
else:
    ray.kill(actor)
`, rayActorNamespace, actorName)
}

func liveRayWorkloadSlice(g gomega.Gomega, c client.Client, ns string) *kueue.Workload {
	wls := &kueue.WorkloadList{}
	g.Expect(c.List(ctx, wls, client.InNamespace(ns))).To(gomega.Succeed())
	var live []kueue.Workload
	for i := range wls.Items {
		if !apimeta.IsStatusConditionTrue(wls.Items[i].Status.Conditions, kueue.WorkloadFinished) {
			live = append(live, wls.Items[i])
		}
	}
	g.Expect(live).To(gomega.HaveLen(1))
	return &live[0]
}

func registerRayAutoscalingTests(testContext func() rayAutoscalingTestContext) {
	ginkgo.Describe("Ray worker-side autoscaling", ginkgo.Ordered, ginkgo.Label("feature:kuberay", util.Shard1), func() {
		var defaultManagerKueueCfg, defaultWorker1KueueCfg, defaultWorker2KueueCfg *kueueconfig.Configuration

		ginkgo.BeforeAll(func() {
			defaultManagerKueueCfg = util.GetKueueConfiguration(ctx, k8sManagerClient)
			defaultWorker1KueueCfg = util.GetKueueConfiguration(ctx, k8sWorker1Client)
			defaultWorker2KueueCfg = util.GetKueueConfiguration(ctx, k8sWorker2Client)

			updateCfg := func(cfg *kueueconfig.Configuration) {
				cfg.Integrations.Frameworks = append(
					cfg.Integrations.Frameworks,
					workloadraycluster.FrameworkName,
					workloadrayjob.FrameworkName,
				)
			}
			util.UpdateKueueConfigurationAndRestart(ctx, k8sManagerClient, defaultManagerKueueCfg.DeepCopy(), managerClusterName, updateCfg)
			updateAndRestartConcurrently(
				func() {
					util.UpdateKueueConfigurationAndRestart(ctx, k8sWorker1Client, defaultWorker1KueueCfg.DeepCopy(), worker1ClusterName, updateCfg)
				},
				func() {
					util.UpdateKueueConfigurationAndRestart(ctx, k8sWorker2Client, defaultWorker2KueueCfg.DeepCopy(), worker2ClusterName, updateCfg)
				},
			)
		})

		ginkgo.AfterAll(func() {
			util.UpdateKueueConfigurationAndRestart(ctx, k8sManagerClient, defaultManagerKueueCfg, managerClusterName)
			updateAndRestartConcurrently(
				func() {
					util.UpdateKueueConfigurationAndRestart(ctx, k8sWorker1Client, defaultWorker1KueueCfg, worker1ClusterName)
				},
				func() {
					util.UpdateKueueConfigurationAndRestart(ctx, k8sWorker2Client, defaultWorker2KueueCfg, worker2ClusterName)
				},
			)
		})

		ginkgo.It("Should reflect worker-side autoscaler resizes (up, down, and up again) of an elastic RayJob's child RayCluster back on the manager", func() {
			tc := testContext()
			kubernetesClients := rayKubernetesClientsMap{
				tc.workerCluster1.Name: {client: k8sWorker1Client, restClient: worker1RestClient, cfg: worker1Cfg},
				tc.workerCluster2.Name: {client: k8sWorker2Client, restClient: worker2RestClient, cfg: worker2Cfg},
			}
			runRayJobAutoscalingTest(tc.managerNs, tc.managerCq, tc.managerLq, tc.multiKueueAc, kubernetesClients)
		})

		ginkgo.It("Should reflect worker-side autoscaler resizes (up, down, and up again) of an elastic RayCluster back on the manager", func() {
			tc := testContext()
			kubernetesClients := rayKubernetesClientsMap{
				tc.workerCluster1.Name: {client: k8sWorker1Client, restClient: worker1RestClient, cfg: worker1Cfg},
				tc.workerCluster2.Name: {client: k8sWorker2Client, restClient: worker2RestClient, cfg: worker2Cfg},
			}
			runRayClusterAutoscalingTest(tc.managerNs, tc.managerCq, tc.managerLq, tc.multiKueueAc, kubernetesClients)
		})
	})
}

func runRayJobAutoscalingTest(
	managerNs *corev1.Namespace,
	managerCq *kueue.ClusterQueue,
	managerLq *kueue.LocalQueue,
	multiKueueAc *kueue.AdmissionCheck,
	kubernetesClients rayKubernetesClientsMap,
) {
	kuberayTestImage := util.GetKuberayTestImage()
	const (
		workerResource = "worker-unit"
		actorA         = "rayjob-actor-a"
		actorB         = "rayjob-actor-b"
		actorC         = "rayjob-actor-c"
	)
	rayjob := testingrayjob.MakeJob("rayjob-autoscale", managerNs.Name).
		Suspend(true).
		Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
		Queue(managerLq.Name).
		WithSubmissionMode(rayv1.K8sJobMode).
		WithEnableAutoscaling(new(true)).
		WithAutoscalerOptions(&rayv1.AutoscalerOptions{IdleTimeoutSeconds: ptr.To[int32](1)}).
		FirstWorkerGroupReplicas(0, 0, 2).
		Entrypoint("python -c \"import time; time.sleep(3600)\"").
		RayStartParam(rayv1.HeadNode, "num-cpus", "0").
		RayStartParam(rayv1.WorkerNode, "resources", fmt.Sprintf(`'{%q: 1}'`, workerResource)).
		RequestAndLimit(rayv1.HeadNode, corev1.ResourceCPU, "500m").
		RequestAndLimit(rayv1.WorkerNode, corev1.ResourceCPU, "250m").
		Image(rayv1.HeadNode, kuberayTestImage).
		Image(rayv1.WorkerNode, kuberayTestImage).
		Obj()

	ginkgo.By("Creating the elastic autoscaling RayJob", func() {
		util.MustCreate(ctx, k8sManagerClient, rayjob)
	})

	gomega.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(rayjob), rayjob)).To(gomega.Succeed())
	wlLookupKey := types.NamespacedName{
		Name:      jobframework.GetWorkloadNameForOwnerWithGVKAndGeneration(rayjob.Name, rayjob.UID, rayv1.GroupVersion.WithKind("RayJob"), rayjob.GetGeneration()),
		Namespace: managerNs.Name,
	}
	admittedWorkerName := util.ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlLookupKey, multiKueueAc.Name)
	admittedWorker := kubernetesClients[admittedWorkerName]
	workerClient := admittedWorker.client
	ginkgo.GinkgoLogr.Info(fmt.Sprintf("elastic autoscaling RayJob %s/%s admitted in worker cluster %s", rayjob.Name, rayjob.Namespace, admittedWorkerName))

	var childKey client.ObjectKey
	var headPod *corev1.Pod
	ginkgo.By("Waiting for the child RayCluster head to become ready on the worker", func() {
		gomega.Eventually(func(g gomega.Gomega) {
			createdRayJob := &rayv1.RayJob{}
			g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(rayjob), createdRayJob)).To(gomega.Succeed())
			g.Expect(createdRayJob.Status.RayClusterName).NotTo(gomega.BeEmpty())
			childKey = client.ObjectKey{Name: createdRayJob.Status.RayClusterName, Namespace: managerNs.Name}

			child := &rayv1.RayCluster{}
			g.Expect(workerClient.Get(ctx, childKey, child)).To(gomega.Succeed())
			g.Expect(apimeta.IsStatusConditionTrue(child.Status.Conditions, string(rayv1.HeadPodReady))).To(gomega.BeTrue())

			pod, err := util.GetRayClusterHeadPod(ctx, workerClient, childKey)
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(pod.Status.Phase).To(gomega.Equal(corev1.PodRunning))
			headPod = pod
		}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
	})

	runOnHead := func(script string) {
		gomega.Eventually(func(g gomega.Gomega) {
			_, stderr, err := util.KExecute(
				ctx,
				admittedWorker.cfg,
				admittedWorker.restClient,
				managerNs.Name,
				headPod.Name,
				headPod.Spec.Containers[0].Name,
				[]string{"python", "-c", script},
			)
			g.Expect(err).NotTo(gomega.HaveOccurred(), string(stderr))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	}

	ginkgo.By("Creating two detached actors so the autoscaler scales the child up to two workers", func() {
		runOnHead(createDetachedActorScript(actorA, workerResource))
		runOnHead(createDetachedActorScript(actorB, workerResource))
	})

	// upSliceName tracks the live slice minted by the scale-up so later phases can
	// assert the scale-down keeps it (in place) and the next scale-up replaces it.
	var upSliceName string
	ginkgo.By("Checking two worker Pods run, the manager admits one slice, and size (2) is reflected onto the manager RayJob", func() {
		gomega.Eventually(func(g gomega.Gomega) {
			workerPods, err := util.GetRayClusterWorkerPods(ctx, workerClient, childKey, corev1.PodRunning)
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(workerPods).To(gomega.HaveLen(2))

			createdRayJob := &rayv1.RayJob{}
			g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(rayjob), createdRayJob)).To(gomega.Succeed())
			g.Expect(createdRayJob.Annotations).To(gomega.HaveKeyWithValue(
				workloadraycluster.RayClusterPodsetReplicaSizesAnnotation, `[{"name":"workers-group-0","count":2}]`))

			managerCQ := &kueue.ClusterQueue{}
			g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(managerCq), managerCQ)).To(gomega.Succeed())
			g.Expect(managerCQ.Status.AdmittedWorkloads).To(gomega.Equal(int32(1)))
			upSlice := liveRayWorkloadSlice(g, k8sManagerClient, managerNs.Name)
			upSliceName = upSlice.Name
			g.Expect(podset.FindPodSetByName(upSlice.Spec.PodSets, "workers-group-0").Count).To(gomega.Equal(int32(2)))
		}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.By("Terminating both actors so the autoscaler scales the child back down to zero workers", func() {
		runOnHead(terminateDetachedActorScript(actorA))
		runOnHead(terminateDetachedActorScript(actorB))
	})

	ginkgo.By("Checking no worker Pod runs, the manager still admits one slice, and size (0) is reflected onto the manager RayJob", func() {
		gomega.Eventually(func(g gomega.Gomega) {
			workerPods, err := util.GetRayClusterWorkerPods(ctx, workerClient, childKey, corev1.PodRunning)
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(workerPods).To(gomega.BeEmpty())

			createdRayJob := &rayv1.RayJob{}
			g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(rayjob), createdRayJob)).To(gomega.Succeed())
			g.Expect(createdRayJob.Annotations).To(gomega.HaveKeyWithValue(
				workloadraycluster.RayClusterPodsetReplicaSizesAnnotation, `[{"name":"workers-group-0","count":0}]`))

			managerCQ := &kueue.ClusterQueue{}
			g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(managerCq), managerCQ)).To(gomega.Succeed())
			g.Expect(managerCQ.Status.AdmittedWorkloads).To(gomega.Equal(int32(1)))
			// Scale-down updates the live slice in place: the same slice stays live (name
			// unchanged) and now reserves zero workers.
			downSlice := liveRayWorkloadSlice(g, k8sManagerClient, managerNs.Name)
			g.Expect(downSlice.Name).To(gomega.Equal(upSliceName))
			g.Expect(podset.FindPodSetByName(downSlice.Spec.PodSets, "workers-group-0").Count).To(gomega.Equal(int32(0)))
		}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.By("Creating one detached actor so the autoscaler scales the child back up to one worker", func() {
		runOnHead(createDetachedActorScript(actorC, workerResource))
	})

	ginkgo.By("Checking one worker Pod runs, size (1) is reflected onto the manager RayJob, and a fresh replacement slice is minted", func() {
		gomega.Eventually(func(g gomega.Gomega) {
			workerPods, err := util.GetRayClusterWorkerPods(ctx, workerClient, childKey, corev1.PodRunning)
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(workerPods).To(gomega.HaveLen(1))

			createdRayJob := &rayv1.RayJob{}
			g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(rayjob), createdRayJob)).To(gomega.Succeed())
			g.Expect(createdRayJob.Annotations).To(gomega.HaveKeyWithValue(
				workloadraycluster.RayClusterPodsetReplicaSizesAnnotation, `[{"name":"workers-group-0","count":1}]`))

			managerCQ := &kueue.ClusterQueue{}
			g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(managerCq), managerCQ)).To(gomega.Succeed())
			g.Expect(managerCQ.Status.AdmittedWorkloads).To(gomega.Equal(int32(1)))
			// Each scale-up mints a fresh replacement slice: exactly one slice stays live and
			// its name differs from the in-place-updated scale-down slice (the earlier slices
			// finish as WorkloadSliceReplaced). The total slice count is not asserted; a
			// transiently overshooting autoscaler leaves extra finished replacement slices.
			newUpSlice := liveRayWorkloadSlice(g, k8sManagerClient, managerNs.Name)
			g.Expect(newUpSlice.Name).NotTo(gomega.Equal(upSliceName))
			g.Expect(podset.FindPodSetByName(newUpSlice.Spec.PodSets, "workers-group-0").Count).To(gomega.Equal(int32(1)))
		}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
	})
}

func runRayClusterAutoscalingTest(
	managerNs *corev1.Namespace,
	managerCq *kueue.ClusterQueue,
	managerLq *kueue.LocalQueue,
	multiKueueAc *kueue.AdmissionCheck,
	kubernetesClients rayKubernetesClientsMap,
) {
	kuberayTestImage := util.GetKuberayTestImage()
	const (
		workerResource = "worker-unit"
		actorA         = "raycluster-actor-a"
		actorB         = "raycluster-actor-b"
		actorC         = "raycluster-actor-c"
	)
	raycluster := testingraycluster.MakeCluster("raycluster-autoscale", managerNs.Name).
		Suspend(true).
		SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
		Queue(managerLq.Name).
		WithEnableAutoscaling(new(true)).
		WithAutoscalerOptions(&rayv1.AutoscalerOptions{IdleTimeoutSeconds: ptr.To[int32](1)}).
		FirstWorkerGroupReplicas(0, 0, 2).
		RayStartParam(rayv1.HeadNode, "num-cpus", "0").
		RayStartParam(rayv1.WorkerNode, "resources", fmt.Sprintf(`'{%q: 1}'`, workerResource)).
		RequestAndLimit(rayv1.HeadNode, corev1.ResourceCPU, "500m").
		RequestAndLimit(rayv1.WorkerNode, corev1.ResourceCPU, "250m").
		Image(rayv1.HeadNode, kuberayTestImage, []string{}).
		Image(rayv1.WorkerNode, kuberayTestImage, []string{}).
		Obj()

	ginkgo.By("Creating the elastic autoscaling RayCluster", func() {
		util.MustCreate(ctx, k8sManagerClient, raycluster)
	})

	gomega.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(raycluster), raycluster)).To(gomega.Succeed())
	wlLookupKey := types.NamespacedName{
		Name:      jobframework.GetWorkloadNameForOwnerWithGVKAndGeneration(raycluster.Name, raycluster.UID, rayv1.GroupVersion.WithKind("RayCluster"), raycluster.GetGeneration()),
		Namespace: managerNs.Name,
	}
	admittedWorkerName := util.ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlLookupKey, multiKueueAc.Name)
	admittedWorker := kubernetesClients[admittedWorkerName]
	workerClient := admittedWorker.client
	ginkgo.GinkgoLogr.Info(fmt.Sprintf("elastic autoscaling RayCluster %s/%s admitted in worker cluster %s", raycluster.Name, raycluster.Namespace, admittedWorkerName))

	var headPod *corev1.Pod
	ginkgo.By("Waiting for the RayCluster head to become ready on the worker", func() {
		gomega.Eventually(func(g gomega.Gomega) {
			workerRayCluster := &rayv1.RayCluster{}
			g.Expect(workerClient.Get(ctx, client.ObjectKeyFromObject(raycluster), workerRayCluster)).To(gomega.Succeed())
			g.Expect(apimeta.IsStatusConditionTrue(workerRayCluster.Status.Conditions, string(rayv1.HeadPodReady))).To(gomega.BeTrue())

			pod, err := util.GetRayClusterHeadPod(ctx, workerClient, client.ObjectKeyFromObject(raycluster))
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(pod.Status.Phase).To(gomega.Equal(corev1.PodRunning))
			headPod = pod
		}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
	})

	runOnHead := func(script string) {
		gomega.Eventually(func(g gomega.Gomega) {
			_, stderr, err := util.KExecute(
				ctx,
				admittedWorker.cfg,
				admittedWorker.restClient,
				managerNs.Name,
				headPod.Name,
				headPod.Spec.Containers[0].Name,
				[]string{"python", "-c", script},
			)
			g.Expect(err).NotTo(gomega.HaveOccurred(), string(stderr))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	}

	ginkgo.By("Creating two detached actors so the autoscaler scales up to two workers", func() {
		runOnHead(createDetachedActorScript(actorA, workerResource))
		runOnHead(createDetachedActorScript(actorB, workerResource))
	})

	// upSliceName tracks the live slice minted by the scale-up so later phases can
	// assert the scale-down keeps it (in place) and the next scale-up replaces it.
	var upSliceName string
	ginkgo.By("Checking two worker Pods run, the manager admits one slice, and size (2) is reflected back onto the manager's RayCluster", func() {
		gomega.Eventually(func(g gomega.Gomega) {
			workerPods, err := util.GetRayClusterWorkerPods(ctx, workerClient, client.ObjectKeyFromObject(raycluster), corev1.PodRunning)
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(workerPods).To(gomega.HaveLen(2))

			createdRayCluster := &rayv1.RayCluster{}
			g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(raycluster), createdRayCluster)).To(gomega.Succeed())
			// The manager spec keeps the user-declared replicas; the autoscaled
			// count is reflected as a runtime annotation that feeds the manager's
			// admitted PodSet counts.
			g.Expect(createdRayCluster.Annotations).To(gomega.HaveKeyWithValue(
				workloadraycluster.RayClusterPodsetReplicaSizesAnnotation, `[{"name":"workers-group-0","count":2}]`))
			g.Expect(createdRayCluster.Status.DesiredWorkerReplicas).To(gomega.Equal(int32(2)))

			managerCQ := &kueue.ClusterQueue{}
			g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(managerCq), managerCQ)).To(gomega.Succeed())
			g.Expect(managerCQ.Status.AdmittedWorkloads).To(gomega.Equal(int32(1)))
			upSlice := liveRayWorkloadSlice(g, k8sManagerClient, managerNs.Name)
			upSliceName = upSlice.Name
			g.Expect(podset.FindPodSetByName(upSlice.Spec.PodSets, "workers-group-0").Count).To(gomega.Equal(int32(2)))
			// The worker cluster mirrors exactly one live, admitted slice reserving the same count.
			workerSlice := liveRayWorkloadSlice(g, workerClient, managerNs.Name)
			g.Expect(apimeta.IsStatusConditionTrue(workerSlice.Status.Conditions, kueue.WorkloadAdmitted)).To(gomega.BeTrue())
			g.Expect(podset.FindPodSetByName(workerSlice.Spec.PodSets, "workers-group-0").Count).To(gomega.Equal(int32(2)))
		}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.By("Checking the worker RayCluster keeps the autoscaled size (not torn down mid-handover)", func() {
		gomega.Consistently(func(g gomega.Gomega) {
			workerRayCluster := &rayv1.RayCluster{}
			g.Expect(workerClient.Get(ctx, client.ObjectKeyFromObject(raycluster), workerRayCluster)).To(gomega.Succeed())
			g.Expect(ptr.Deref(workerRayCluster.Spec.WorkerGroupSpecs[0].Replicas, -1)).To(gomega.BeEquivalentTo(int32(2)))
			g.Expect(ptr.Deref(workerRayCluster.Spec.Suspend, false)).To(gomega.BeFalse())
		}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
	})

	ginkgo.By("Terminating both actors so the autoscaler scales back down to zero workers", func() {
		runOnHead(terminateDetachedActorScript(actorA))
		runOnHead(terminateDetachedActorScript(actorB))
	})

	ginkgo.By("Checking no worker Pod runs, the manager still admits one slice, and size (0) is reflected back onto the manager's RayCluster", func() {
		gomega.Eventually(func(g gomega.Gomega) {
			workerPods, err := util.GetRayClusterWorkerPods(ctx, workerClient, client.ObjectKeyFromObject(raycluster), corev1.PodRunning)
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(workerPods).To(gomega.BeEmpty())

			createdRayCluster := &rayv1.RayCluster{}
			g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(raycluster), createdRayCluster)).To(gomega.Succeed())
			g.Expect(createdRayCluster.Annotations).To(gomega.HaveKeyWithValue(
				workloadraycluster.RayClusterPodsetReplicaSizesAnnotation, `[{"name":"workers-group-0","count":0}]`))
			g.Expect(createdRayCluster.Status.DesiredWorkerReplicas).To(gomega.Equal(int32(0)))

			managerCQ := &kueue.ClusterQueue{}
			g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(managerCq), managerCQ)).To(gomega.Succeed())
			g.Expect(managerCQ.Status.AdmittedWorkloads).To(gomega.Equal(int32(1)))
			// Scale-down updates the live slice in place: the same slice stays live (name
			// unchanged) and now reserves zero workers.
			downSlice := liveRayWorkloadSlice(g, k8sManagerClient, managerNs.Name)
			g.Expect(downSlice.Name).To(gomega.Equal(upSliceName))
			g.Expect(podset.FindPodSetByName(downSlice.Spec.PodSets, "workers-group-0").Count).To(gomega.Equal(int32(0)))
			// The worker cluster still mirrors exactly one live, admitted slice, now reserving zero.
			workerSlice := liveRayWorkloadSlice(g, workerClient, managerNs.Name)
			g.Expect(apimeta.IsStatusConditionTrue(workerSlice.Status.Conditions, kueue.WorkloadAdmitted)).To(gomega.BeTrue())
			g.Expect(podset.FindPodSetByName(workerSlice.Spec.PodSets, "workers-group-0").Count).To(gomega.Equal(int32(0)))
		}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.By("Checking the worker RayCluster keeps running at the scaled-down size", func() {
		gomega.Consistently(func(g gomega.Gomega) {
			workerRayCluster := &rayv1.RayCluster{}
			g.Expect(workerClient.Get(ctx, client.ObjectKeyFromObject(raycluster), workerRayCluster)).To(gomega.Succeed())
			g.Expect(ptr.Deref(workerRayCluster.Spec.WorkerGroupSpecs[0].Replicas, -1)).To(gomega.BeEquivalentTo(int32(0)))
			g.Expect(ptr.Deref(workerRayCluster.Spec.Suspend, false)).To(gomega.BeFalse())
		}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
	})

	ginkgo.By("Creating one detached actor so the autoscaler scales back up to one worker", func() {
		runOnHead(createDetachedActorScript(actorC, workerResource))
	})

	ginkgo.By("Checking one worker Pod runs, size (1) is reflected back onto the manager's RayCluster, and a fresh replacement slice is minted", func() {
		gomega.Eventually(func(g gomega.Gomega) {
			workerPods, err := util.GetRayClusterWorkerPods(ctx, workerClient, client.ObjectKeyFromObject(raycluster), corev1.PodRunning)
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(workerPods).To(gomega.HaveLen(1))

			createdRayCluster := &rayv1.RayCluster{}
			g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(raycluster), createdRayCluster)).To(gomega.Succeed())
			g.Expect(createdRayCluster.Annotations).To(gomega.HaveKeyWithValue(
				workloadraycluster.RayClusterPodsetReplicaSizesAnnotation, `[{"name":"workers-group-0","count":1}]`))
			g.Expect(createdRayCluster.Status.DesiredWorkerReplicas).To(gomega.Equal(int32(1)))

			managerCQ := &kueue.ClusterQueue{}
			g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(managerCq), managerCQ)).To(gomega.Succeed())
			g.Expect(managerCQ.Status.AdmittedWorkloads).To(gomega.Equal(int32(1)))
			// Each scale-up mints a fresh replacement slice: exactly one slice stays live and
			// its name differs from the in-place-updated scale-down slice (the earlier slices
			// finish as WorkloadSliceReplaced). The total slice count is not asserted; a
			// transiently overshooting autoscaler leaves extra finished replacement slices.
			newUpSlice := liveRayWorkloadSlice(g, k8sManagerClient, managerNs.Name)
			g.Expect(newUpSlice.Name).NotTo(gomega.Equal(upSliceName))
			g.Expect(podset.FindPodSetByName(newUpSlice.Spec.PodSets, "workers-group-0").Count).To(gomega.Equal(int32(1)))
			// The worker cluster mirrors the freshly minted replacement slice: one live, admitted, reserving one.
			workerSlice := liveRayWorkloadSlice(g, workerClient, managerNs.Name)
			g.Expect(apimeta.IsStatusConditionTrue(workerSlice.Status.Conditions, kueue.WorkloadAdmitted)).To(gomega.BeTrue())
			g.Expect(podset.FindPodSetByName(workerSlice.Spec.PodSets, "workers-group-0").Count).To(gomega.Equal(int32(1)))
		}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.By("Checking the worker RayCluster keeps the re-scaled-up size (not torn down mid-handover)", func() {
		gomega.Consistently(func(g gomega.Gomega) {
			workerRayCluster := &rayv1.RayCluster{}
			g.Expect(workerClient.Get(ctx, client.ObjectKeyFromObject(raycluster), workerRayCluster)).To(gomega.Succeed())
			g.Expect(ptr.Deref(workerRayCluster.Spec.WorkerGroupSpecs[0].Replicas, -1)).To(gomega.BeEquivalentTo(int32(1)))
			g.Expect(ptr.Deref(workerRayCluster.Spec.Suspend, false)).To(gomega.BeFalse())
		}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
	})
}
