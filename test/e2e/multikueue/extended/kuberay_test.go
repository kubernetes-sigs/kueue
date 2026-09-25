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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	workloadraycluster "sigs.k8s.io/kueue/pkg/controller/jobs/raycluster"
	workloadrayjob "sigs.k8s.io/kueue/pkg/controller/jobs/rayjob"
	workloadrayservice "sigs.k8s.io/kueue/pkg/controller/jobs/rayservice"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingraycluster "sigs.k8s.io/kueue/pkg/util/testingjobs/raycluster"
	testingrayjob "sigs.k8s.io/kueue/pkg/util/testingjobs/rayjob"
	testingrayservice "sigs.k8s.io/kueue/pkg/util/testingjobs/rayservice"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/util"
)

type kubeRayTestContext struct {
	managerNs         *corev1.Namespace
	managerLq         *kueue.LocalQueue
	managerHighWPC    *kueue.WorkloadPriorityClass
	managerLowWPC     *kueue.WorkloadPriorityClass
	multiKueueAc      *kueue.AdmissionCheck
	kubernetesClients kubernetesClientsMap
}

func registerKubeRayTests(contextProvider func() kubeRayTestContext) {
	ginkgo.When("Ray integration tests", ginkgo.Ordered, ginkgo.Label("feature:kuberay"), func() {
		var (
			managerNs         *corev1.Namespace
			managerLq         *kueue.LocalQueue
			managerHighWPC    *kueue.WorkloadPriorityClass
			managerLowWPC     *kueue.WorkloadPriorityClass
			multiKueueAc      *kueue.AdmissionCheck
			kubernetesClients kubernetesClientsMap
		)

		ginkgo.BeforeEach(func() {
			tc := contextProvider()
			managerNs = tc.managerNs
			managerLq = tc.managerLq
			managerHighWPC = tc.managerHighWPC
			managerLowWPC = tc.managerLowWPC
			multiKueueAc = tc.multiKueueAc
			kubernetesClients = tc.kubernetesClients
		})
		ginkgo.It("Should run a RayJob on worker if admitted", func() {
			kuberayTestImage := util.GetKuberayTestImage()
			rayjob := testingrayjob.MakeJob("rayjob1", managerNs.Name).
				Suspend(true).
				Queue(managerLq.Name).
				WithSubmissionMode(rayv1.K8sJobMode).
				Entrypoint("python -c \"import ray; ray.init(); print(ray.cluster_resources())\"").
				RequestAndLimit(rayv1.HeadNode, corev1.ResourceCPU, "1").
				RequestAndLimit(rayv1.WorkerNode, corev1.ResourceCPU, "0.5").
				Image(rayv1.HeadNode, kuberayTestImage).
				Image(rayv1.WorkerNode, kuberayTestImage).
				TerminationGracePeriod(1).
				Obj()

			ginkgo.By("Creating the RayJob", func() {
				util.MustCreate(ctx, k8sManagerClient, rayjob)
			})

			wlLookupKey := types.NamespacedName{Name: workloadrayjob.GetWorkloadNameForRayJob(rayjob.Name, rayjob.UID), Namespace: managerNs.Name}

			admittedWorker := util.ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlLookupKey, multiKueueAc.Name)
			ginkgo.GinkgoLogr.Info(fmt.Sprintf("RayJob %s/%s is admitted in worker cluster %s", rayjob.Name, rayjob.Namespace, admittedWorker))

			ginkgo.By("Waiting for the RayJob to finish", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdRayJob := &rayv1.RayJob{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(rayjob), createdRayJob)).To(gomega.Succeed())
					g.Expect(createdRayJob.Status.JobDeploymentStatus).To(gomega.Equal(rayv1.JobDeploymentStatusComplete))
				}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
				util.ExpectWorkloadToFinish(ctx, k8sManagerClient, wlLookupKey)
			})

			ginkgo.By("Checking no objects are left in the worker clusters and the RayJob is completed", func() {
				wl := &kueue.Workload{
					Name:      wlLookupKey.Name,
					Namespace: wlLookupKey.Namespace,
				}
				util.ExpectObjectToBeDeletedOnClusters(ctx, wl, k8sWorker1Client, k8sWorker2Client)
				util.ExpectObjectToBeDeletedOnClusters(ctx, rayjob, k8sWorker1Client, k8sWorker2Client)
			})
		})

		ginkgo.It("Should run a RayCluster on worker if admitted", func() {
			kuberayTestImage := util.GetKuberayTestImage()
			raycluster := testingraycluster.MakeCluster("raycluster1", managerNs.Name).
				Suspend(true).
				Queue(managerLq.Name).
				RequestAndLimit(rayv1.HeadNode, corev1.ResourceCPU, "1").
				RequestAndLimit(rayv1.WorkerNode, corev1.ResourceCPU, "0.5").
				Image(rayv1.HeadNode, kuberayTestImage, []string{}).
				Image(rayv1.WorkerNode, kuberayTestImage, []string{}).
				Obj()

			ginkgo.By("Creating the RayCluster", func() {
				util.MustCreate(ctx, k8sManagerClient, raycluster)
			})

			wlLookupKey := types.NamespacedName{Name: workloadraycluster.GetWorkloadNameForRayCluster(raycluster.Name, raycluster.UID), Namespace: managerNs.Name}
			// the execution should be given to the worker1
			admittedWorker := util.ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlLookupKey, multiKueueAc.Name)
			ginkgo.GinkgoLogr.Info(fmt.Sprintf("RayCluster %s/%s is admitted in worker cluster %s", raycluster.Name, raycluster.Namespace, admittedWorker))

			ginkgo.By("Checking the RayCluster is ready", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdRayCluster := &rayv1.RayCluster{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(raycluster), createdRayCluster)).To(gomega.Succeed())
					g.Expect(createdRayCluster.Status.DesiredWorkerReplicas).To(gomega.Equal(int32(1)))
					g.Expect(createdRayCluster.Status.ReadyWorkerReplicas).To(gomega.Equal(int32(1)))
					g.Expect(createdRayCluster.Status.AvailableWorkerReplicas).To(gomega.Equal(int32(1)))
				}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should scale an elastic RayCluster on worker if admitted", func() {
			kuberayTestImage := util.GetKuberayTestImage()
			raycluster := testingraycluster.MakeCluster("raycluster-elastic", managerNs.Name).
				Suspend(true).
				SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				Queue(managerLq.Name).
				ScaleFirstWorkerGroup(1).
				RequestAndLimit(rayv1.HeadNode, corev1.ResourceCPU, "200m").
				RequestAndLimit(rayv1.WorkerNode, corev1.ResourceCPU, "200m").
				Image(rayv1.HeadNode, kuberayTestImage, []string{}).
				Image(rayv1.WorkerNode, kuberayTestImage, []string{}).
				Obj()

			ginkgo.By("Creating the elastic RayCluster", func() {
				util.MustCreate(ctx, k8sManagerClient, raycluster)
			})

			// Elastic (workload-slicing) RayCluster workloads are named with the
			// object's generation, so fetch the created object to derive the name
			// of its current slice.
			gomega.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(raycluster), raycluster)).To(gomega.Succeed())
			wlLookupKey := types.NamespacedName{
				Name:      jobframework.GetWorkloadNameForOwnerWithGVKAndGeneration(raycluster.Name, raycluster.UID, rayv1.GroupVersion.WithKind("RayCluster"), raycluster.GetGeneration()),
				Namespace: managerNs.Name,
			}
			admittedWorker := util.ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlLookupKey, multiKueueAc.Name)
			ginkgo.GinkgoLogr.Info(fmt.Sprintf("elastic RayCluster %s/%s is admitted in worker cluster %s", raycluster.Name, raycluster.Namespace, admittedWorker))

			// The assertions below check DesiredWorkerReplicas, which KubeRay derives
			// directly from the worker cluster's RayCluster spec. This is exactly what
			// the manager-driven elastic sync propagates, and it does not depend on the
			// Ray runtime becoming healthy (Ray pod readiness is KubeRay's own concern).
			ginkgo.By("Checking the RayCluster starts with one worker on the worker cluster", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdRayCluster := &rayv1.RayCluster{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(raycluster), createdRayCluster)).To(gomega.Succeed())
					g.Expect(createdRayCluster.Status.DesiredWorkerReplicas).To(gomega.Equal(int32(1)))
				}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Scaling the first worker group up to three on the manager", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdRayCluster := &rayv1.RayCluster{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(raycluster), createdRayCluster)).To(gomega.Succeed())
					createdRayCluster.Spec.WorkerGroupSpecs[0].Replicas = new(int32(3))
					g.Expect(k8sManagerClient.Update(ctx, createdRayCluster)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking the scaled-up worker replicas propagate to the worker cluster", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdRayCluster := &rayv1.RayCluster{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(raycluster), createdRayCluster)).To(gomega.Succeed())
					g.Expect(createdRayCluster.Status.DesiredWorkerReplicas).To(gomega.Equal(int32(3)))
				}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Scaling the first worker group back down to one on the manager", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdRayCluster := &rayv1.RayCluster{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(raycluster), createdRayCluster)).To(gomega.Succeed())
					createdRayCluster.Spec.WorkerGroupSpecs[0].Replicas = new(int32(1))
					g.Expect(k8sManagerClient.Update(ctx, createdRayCluster)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking the reduced worker replicas propagate to the worker cluster", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdRayCluster := &rayv1.RayCluster{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(raycluster), createdRayCluster)).To(gomega.Succeed())
					g.Expect(createdRayCluster.Status.DesiredWorkerReplicas).To(gomega.Equal(int32(1)))
				}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should delete a preempted elastic RayCluster from the worker cluster", func() {
			runElasticRayClusterCleanupAfterPreemptionTest(
				managerNs,
				managerLq,
				managerHighWPC,
				managerLowWPC,
				multiKueueAc,
				kubernetesClients,
			)
		})

		ginkgo.It("Should run a RayService on worker if admitted", func() {
			kuberayTestImage := util.GetKuberayTestImage()

			// Create ConfigMap with a simple Ray Serve application
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

			serveConfigV2 := `applications:
  - name: hello_app
    import_path: hello_serve:app
    route_prefix: /
    deployments:
      - name: HelloWorld
        num_replicas: 1
        max_replicas_per_node: 1
        ray_actor_options:
          num_cpus: 0.2`

			volumes := []corev1.Volume{
				{
					Name: "code-sample",
					ConfigMap: &corev1.ConfigMapVolumeSource{
						Name: "rayservice-hello",
						Items: []corev1.KeyToPath{
							{
								Key:  "hello_serve.py",
								Path: "hello_serve.py",
							},
						},
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

			rayService := testingrayservice.MakeService("rayservice1", managerNs.Name).
				Suspend(true).
				Queue(managerLq.Name).
				RequestAndLimit(rayv1.HeadNode, corev1.ResourceCPU, "1").
				RequestAndLimit(rayv1.WorkerNode, corev1.ResourceCPU, "0.5").
				Image(rayv1.HeadNode, kuberayTestImage).
				Image(rayv1.WorkerNode, kuberayTestImage).
				RayStartParam(rayv1.HeadNode, "object-store-memory", "100000000").
				WithServeConfigV2(serveConfigV2).
				Env(rayv1.HeadNode, env).
				Env(rayv1.WorkerNode, env).
				Volumes(rayv1.HeadNode, volumes).
				Volumes(rayv1.WorkerNode, volumes).
				VolumeMounts(rayv1.HeadNode, volumeMounts).
				VolumeMounts(rayv1.WorkerNode, volumeMounts).
				TerminationGracePeriod(1).
				Obj()

			rayService.Spec.RayClusterSpec.WorkerGroupSpecs[0].GroupName = "small-group"
			rayService.Spec.RayClusterSpec.WorkerGroupSpecs[0].MinReplicas = new(int32(1))
			rayService.Spec.RayClusterSpec.WorkerGroupSpecs[0].MaxReplicas = new(int32(2))

			ginkgo.By("Creating the ConfigMap on all clusters", func() {
				worker1ConfigMap := configMap.DeepCopy()
				worker2ConfigMap := configMap.DeepCopy()
				util.MustCreate(ctx, k8sManagerClient, configMap)
				util.MustCreate(ctx, k8sWorker1Client, worker1ConfigMap)
				util.MustCreate(ctx, k8sWorker2Client, worker2ConfigMap)
			})

			ginkgo.By("Creating the RayService", func() {
				util.MustCreate(ctx, k8sManagerClient, rayService)
			})

			wlLookupKey := types.NamespacedName{Name: workloadrayservice.GetWorkloadNameForRayService(rayService.Name, rayService.UID), Namespace: managerNs.Name}

			admittedWorker := util.ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlLookupKey, multiKueueAc.Name)
			ginkgo.GinkgoLogr.Info(fmt.Sprintf("RayService %s/%s is admitted in worker cluster %s", rayService.Name, rayService.Namespace, admittedWorker))

			ginkgo.By("Checking the RayService is running", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdRayService := &rayv1.RayService{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(rayService), createdRayService)).To(gomega.Succeed())
					g.Expect(createdRayService.Spec.Suspend).To(gomega.BeFalse())
					g.Expect(apimeta.IsStatusConditionTrue(createdRayService.Status.Conditions, string(rayv1.RayServiceReady))).To(gomega.BeTrue())
				}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
			})

			// An in-place serveConfigV2 edit is quota-neutral, so MultiKueue forwards it
			// to the worker copy without re-admission. num_replicas 1 -> 2 is a clear,
			// observable change within serveConfigV2.
			updatedServeConfigV2 := `applications:
  - name: hello_app
    import_path: hello_serve:app
    route_prefix: /
    deployments:
      - name: HelloWorld
        num_replicas: 2
        max_replicas_per_node: 1
        ray_actor_options:
          num_cpus: 0.2`

			gomega.Expect(updatedServeConfigV2).NotTo(gomega.Equal(serveConfigV2), "the updated serveConfigV2 must differ from the initial config so the forward assertion is meaningful")

			workerClient := kubernetesClients[admittedWorker].client
			var workerRayServiceUID types.UID
			ginkgo.By("Recording the existing worker copy and its initial serveConfigV2", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					workerRayService := &rayv1.RayService{}
					g.Expect(workerClient.Get(ctx, client.ObjectKeyFromObject(rayService), workerRayService)).To(gomega.Succeed())
					g.Expect(workerRayService.Spec.ServeConfigV2).To(gomega.Equal(serveConfigV2))
					workerRayServiceUID = workerRayService.UID
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Updating serveConfigV2 on the manager", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdRayService := &rayv1.RayService{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(rayService), createdRayService)).To(gomega.Succeed())
					createdRayService.Spec.ServeConfigV2 = updatedServeConfigV2
					g.Expect(k8sManagerClient.Update(ctx, createdRayService)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking the change is promptly forwarded to the same worker copy (in-place, no re-admission)", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					workerRayService := &rayv1.RayService{}
					g.Expect(workerClient.Get(ctx, client.ObjectKeyFromObject(rayService), workerRayService)).To(gomega.Succeed())
					g.Expect(workerRayService.Spec.ServeConfigV2).To(gomega.Equal(updatedServeConfigV2))
					g.Expect(workerRayService.UID).To(gomega.Equal(workerRayServiceUID))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
}

func runElasticRayClusterCleanupAfterPreemptionTest(
	managerNs *corev1.Namespace,
	managerLq *kueue.LocalQueue,
	managerHighWPC *kueue.WorkloadPriorityClass,
	managerLowWPC *kueue.WorkloadPriorityClass,
	multiKueueAc *kueue.AdmissionCheck,
	kubernetesClients kubernetesClientsMap,
) {
	rayCluster := testingraycluster.MakeCluster("raycluster-elastic-preemption", managerNs.Name).
		Suspend(true).
		SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
		Queue(managerLq.Name).
		WorkloadPriorityClass(managerLowWPC.Name).
		ScaleFirstWorkerGroup(1).
		RequestAndLimit(rayv1.HeadNode, corev1.ResourceCPU, "500m").
		RequestAndLimit(rayv1.HeadNode, corev1.ResourceName(extraResourceGPUHighCost), "2").
		RequestAndLimit(rayv1.WorkerNode, corev1.ResourceCPU, "250m").
		Image(rayv1.HeadNode, util.GetKuberayTestImage(), []string{}).
		Image(rayv1.WorkerNode, util.GetKuberayTestImage(), []string{}).
		Obj()

	ginkgo.By("Creating the low-priority elastic RayCluster with one worker", func() {
		util.MustCreate(ctx, k8sManagerClient, rayCluster)
	})

	gomega.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(rayCluster), rayCluster)).To(gomega.Succeed())
	wlLookupKey := types.NamespacedName{
		Name:      jobframework.GetWorkloadNameForOwnerWithGVKAndGeneration(rayCluster.Name, rayCluster.UID, rayv1.GroupVersion.WithKind("RayCluster"), rayCluster.GetGeneration()),
		Namespace: managerNs.Name,
	}
	admittedWorkerName := util.ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlLookupKey, multiKueueAc.Name)
	// Requesting two units of the virtual high-cost GPU resource forces both the
	// RayCluster and its preemptor onto worker1 because worker2 has quota for only one.
	gomega.Expect(admittedWorkerName).To(gomega.HavePrefix("worker1-"))
	workerClient := kubernetesClients[admittedWorkerName].client
	rayClusterKey := client.ObjectKeyFromObject(rayCluster)

	var workerRayClusterUID types.UID
	ginkgo.By("Waiting for the one-worker RayCluster on worker1", func() {
		workerRayCluster := &rayv1.RayCluster{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(workerClient.Get(ctx, rayClusterKey, workerRayCluster)).To(gomega.Succeed())
			g.Expect(ptr.Deref(workerRayCluster.Spec.Suspend, true)).To(gomega.BeFalse())
			g.Expect(ptr.Deref(workerRayCluster.Spec.WorkerGroupSpecs[0].Replicas, -1)).To(gomega.Equal(int32(1)))
			workerRayClusterUID = workerRayCluster.UID
		}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed(), util.AssertMsg("RayCluster did not start on worker1", workerRayCluster))
	})

	initialSlice := &kueue.Workload{}
	gomega.Expect(k8sManagerClient.Get(ctx, wlLookupKey, initialSlice)).To(gomega.Succeed())
	ginkgo.By("Scaling the RayCluster from one worker to two on the manager", func() {
		gomega.Eventually(func(g gomega.Gomega) {
			createdRayCluster := &rayv1.RayCluster{}
			g.Expect(k8sManagerClient.Get(ctx, rayClusterKey, createdRayCluster)).To(gomega.Succeed())
			createdRayCluster.Spec.WorkerGroupSpecs[0].Replicas = ptr.To[int32](2)
			g.Expect(k8sManagerClient.Update(ctx, createdRayCluster)).To(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	var scaledSlice *kueue.Workload
	ginkgo.By("Checking the scale-up replacement slice is admitted on worker1", func() {
		scaledSlice = util.ExpectNewWorkloadSliceWithTimeout(ctx, k8sManagerClient, initialSlice, util.MediumTimeout)
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(scaledSlice), scaledSlice)).To(gomega.Succeed())
			g.Expect(workload.IsAdmitted(scaledSlice)).To(gomega.BeTrue())
			g.Expect(workloadslicing.ScaledUp(scaledSlice)).To(gomega.BeTrue())

			workerRayCluster := &rayv1.RayCluster{}
			g.Expect(workerClient.Get(ctx, rayClusterKey, workerRayCluster)).To(gomega.Succeed())
			g.Expect(workerRayCluster.UID).To(gomega.Equal(workerRayClusterUID))
			g.Expect(ptr.Deref(workerRayCluster.Spec.WorkerGroupSpecs[0].Replicas, -1)).To(gomega.Equal(int32(2)))
		}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
	})

	highJob := testingjob.MakeJob("raycluster-elastic-preemptor", managerNs.Name).
		Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
		WorkloadPriorityClass(managerHighWPC.Name).
		Queue(kueue.LocalQueueName(managerLq.Name)).
		RequestAndLimit(corev1.ResourceCPU, "100m").
		RequestAndLimit(corev1.ResourceName(extraResourceGPUHighCost), "2").
		TerminationGracePeriod(1).
		Obj()
	ginkgo.By("Creating a high-priority Job that preempts the scaled-up RayCluster", func() {
		util.MustCreate(ctx, k8sManagerClient, highJob)
	})
	highWlKey := types.NamespacedName{
		Name:      workloadjob.GetWorkloadNameForJob(highJob.Name, highJob.UID),
		Namespace: managerNs.Name,
	}

	ginkgo.By("Checking the RayCluster is preempted and the high-priority Job is admitted on worker1", func() {
		gomega.Eventually(func(g gomega.Gomega) {
			createdSlice := &kueue.Workload{}
			g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(scaledSlice), createdSlice)).To(gomega.Succeed())
			g.Expect(createdSlice.Status.SchedulingStats).NotTo(gomega.BeNil())
			g.Expect(createdSlice.Status.SchedulingStats.Evictions).To(gomega.ContainElement(gomega.And(
				gomega.HaveField("Reason", kueue.WorkloadEvictedByPreemption),
				gomega.HaveField("Count", gomega.BeNumerically(">=", 1)),
			)))
		}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())

		highJobWorkerName := util.ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, highWlKey, multiKueueAc.Name)
		gomega.Expect(highJobWorkerName).To(gomega.HavePrefix("worker1-"))
	})

	ginkgo.By("Checking the preempted RayCluster is deleted from worker1", func() {
		util.ExpectObjectToBeDeletedWithTimeout(ctx, workerClient, rayCluster, false, util.MediumTimeout)
	})
}
