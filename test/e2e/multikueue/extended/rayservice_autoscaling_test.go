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

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadraycluster "sigs.k8s.io/kueue/pkg/controller/jobs/raycluster"
	"sigs.k8s.io/kueue/pkg/util/podset"
	testingrayservice "sigs.k8s.io/kueue/pkg/util/testingjobs/rayservice"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/util"
)

func runRayServiceAutoscalingTest(
	managerNs *corev1.Namespace,
	managerLq *kueue.LocalQueue,
	multiKueueAc *kueue.AdmissionCheck,
	kubernetesClients kubernetesClientsMap,
) {
	const (
		workerResource = "worker-unit"
		actorA         = "rayservice-autoscaling-actor-a"
		actorB         = "rayservice-autoscaling-actor-b"
	)

	configMap := &corev1.ConfigMap{
		Name:      "rayservice-hello",
		Namespace: managerNs.Name,
		Data: map[string]string{
			"hello_serve.py": `from ray import serve

@serve.deployment
class HelloWorld:
    def __call__(self, request):
        return "Hello, World!"

app = HelloWorld.bind()`,
		},
	}
	ginkgo.By("Creating the RayService application ConfigMap on all clusters", func() {
		util.MustCreate(ctx, k8sManagerClient, configMap.DeepCopy())
		for _, worker := range kubernetesClients {
			util.MustCreate(ctx, worker.client, configMap.DeepCopy())
		}
	})

	serveConfig := `applications:
  - name: hello_app
    import_path: hello_serve:app
    route_prefix: /
    deployments:
      - name: HelloWorld
        num_replicas: 1
        ray_actor_options:
          num_cpus: 0`
	codeVolume := corev1.Volume{
		Name: "code-sample",
		ConfigMap: &corev1.ConfigMapVolumeSource{
			Name: configMap.Name,
		},
	}
	codeMount := corev1.VolumeMount{Name: codeVolume.Name, MountPath: "/home/ray/samples"}
	pythonPath := corev1.EnvVar{Name: "PYTHONPATH", Value: "/home/ray/samples:$PYTHONPATH"}

	rayService := testingrayservice.MakeService("rayservice-autoscale", managerNs.Name).
		Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
		Queue(managerLq.Name).
		EnableInTreeAutoscaling().
		WithServeConfigV2(serveConfig).
		RayStartParam(rayv1.HeadNode, "num-cpus", "1").
		RayStartParam(rayv1.WorkerNode, "resources", fmt.Sprintf(`'{%q: 1}'`, workerResource)).
		Request(rayv1.HeadNode, corev1.ResourceCPU, "750m").
		Limit(rayv1.HeadNode, corev1.ResourceCPU, "1").
		Request(rayv1.WorkerNode, corev1.ResourceCPU, "250m").
		Limit(rayv1.WorkerNode, corev1.ResourceCPU, "400m").
		Image(rayv1.HeadNode, util.GetKuberayTestImage()).
		Image(rayv1.WorkerNode, util.GetKuberayTestImage()).
		Env(rayv1.HeadNode, []corev1.EnvVar{pythonPath}).
		Env(rayv1.WorkerNode, []corev1.EnvVar{pythonPath}).
		Volumes(rayv1.HeadNode, []corev1.Volume{codeVolume}).
		Volumes(rayv1.WorkerNode, []corev1.Volume{codeVolume}).
		VolumeMounts(rayv1.HeadNode, []corev1.VolumeMount{codeMount}).
		VolumeMounts(rayv1.WorkerNode, []corev1.VolumeMount{codeMount}).
		TerminationGracePeriod(1).
		Obj()
	rayService.Spec.RayClusterSpec.AutoscalerOptions = &rayv1.AutoscalerOptions{
		IdleTimeoutSeconds: ptr.To[int32](1),
		Env: []corev1.EnvVar{{
			Name:  "AUTOSCALER_UPDATE_INTERVAL_S",
			Value: "1",
		}},
	}
	rayService.Spec.RayClusterSpec.WorkerGroupSpecs[0].Replicas = ptr.To[int32](0)
	rayService.Spec.RayClusterSpec.WorkerGroupSpecs[0].MinReplicas = ptr.To[int32](0)
	rayService.Spec.RayClusterSpec.WorkerGroupSpecs[0].MaxReplicas = ptr.To[int32](2)

	ginkgo.By("Creating the elastic autoscaling RayService", func() {
		util.MustCreate(ctx, k8sManagerClient, rayService)
	})

	workloads := util.ExpectWorkloadsInNamespace(ctx, k8sManagerClient, managerNs.Name, 1)
	wlLookupKey := client.ObjectKeyFromObject(&workloads[0])
	admittedWorkerName := util.ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlLookupKey, multiKueueAc.Name)
	// The head and autoscaler containers request 1250m in total, which exceeds
	// worker2's quota and deterministically places the service on worker1.
	gomega.Expect(admittedWorkerName).To(gomega.HavePrefix("worker1-"))
	admittedWorker := kubernetesClients[admittedWorkerName]
	workerClient := admittedWorker.client

	workerService := &rayv1.RayService{}
	workerCluster := &rayv1.RayCluster{}
	ginkgo.By("Waiting for the RayService active RayCluster on the worker", func() {
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(workerClient.Get(ctx, client.ObjectKeyFromObject(rayService), workerService)).To(gomega.Succeed())
			childName := workerService.Status.ActiveServiceStatus.RayClusterName
			g.Expect(childName).NotTo(gomega.BeEmpty())
			g.Expect(workerClient.Get(ctx, client.ObjectKey{Name: childName, Namespace: workerService.Namespace}, workerCluster)).To(gomega.Succeed())
			g.Expect(apimeta.IsStatusConditionTrue(workerCluster.Status.Conditions, string(rayv1.HeadPodReady))).To(gomega.BeTrue())
			g.Expect(workerCluster.Status.DesiredWorkerReplicas).To(gomega.Equal(int32(0)))
		}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed(), util.AssertMsg("RayService did not create a ready active RayCluster", workerService))
	})
	childKey := client.ObjectKeyFromObject(workerCluster)
	initialSlice := liveRayWorkloadSlice(gomega.Default, k8sManagerClient, managerNs.Name, wlLookupKey.Name)

	ginkgo.By("Creating two detached actors so the RayService autoscaler adds two workers", func() {
		util.CreateDetachedRayActors(ctx, workerClient, admittedWorker.cfg, admittedWorker.restClient, childKey, workerResource, actorA, actorB)
	})

	var upSliceName string
	ginkgo.By("Checking the RayService scale-up is reflected on the manager", func() {
		gomega.Eventually(func(g gomega.Gomega) {
			workerPods, err := util.GetRayClusterWorkerPods(ctx, workerClient, childKey, corev1.PodRunning)
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(workerPods).To(gomega.HaveLen(2))

			managerService := &rayv1.RayService{}
			g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(rayService), managerService)).To(gomega.Succeed())
			g.Expect(managerService.Annotations).To(gomega.HaveKeyWithValue(
				workloadraycluster.RayClusterPodsetReplicaSizesAnnotation, `[{"name":"workers-group-0","count":2}]`))

			upSlice := liveRayWorkloadSlice(g, k8sManagerClient, managerNs.Name, wlLookupKey.Name)
			upSliceName = upSlice.Name
			g.Expect(upSlice.Name).NotTo(gomega.Equal(initialSlice.Name))
			g.Expect(podset.FindPodSetByName(upSlice.Spec.PodSets, "workers-group-0").Count).To(gomega.Equal(int32(2)))
		}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.By("Terminating one actor so the RayService autoscaler removes one worker", func() {
		util.TerminateDetachedRayActor(ctx, workerClient, admittedWorker.cfg, admittedWorker.restClient, childKey, actorB)
	})

	ginkgo.By("Checking the RayService scale-down updates the admitted slice in place", func() {
		gomega.Eventually(func(g gomega.Gomega) {
			workerPods, err := util.GetRayClusterWorkerPods(ctx, workerClient, childKey, corev1.PodRunning)
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(workerPods).To(gomega.HaveLen(1))

			managerService := &rayv1.RayService{}
			g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(rayService), managerService)).To(gomega.Succeed())
			g.Expect(managerService.Annotations).To(gomega.HaveKeyWithValue(
				workloadraycluster.RayClusterPodsetReplicaSizesAnnotation, `[{"name":"workers-group-0","count":1}]`))

			downSlice := liveRayWorkloadSlice(g, k8sManagerClient, managerNs.Name, wlLookupKey.Name)
			g.Expect(downSlice.Name).To(gomega.Equal(upSliceName))
			g.Expect(podset.FindPodSetByName(downSlice.Spec.PodSets, "workers-group-0").Count).To(gomega.Equal(int32(1)))
		}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
	})
}
