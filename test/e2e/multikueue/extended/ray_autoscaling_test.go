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
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	workloadraycluster "sigs.k8s.io/kueue/pkg/controller/jobs/raycluster"
	"sigs.k8s.io/kueue/pkg/util/podset"
	testingraycluster "sigs.k8s.io/kueue/pkg/util/testingjobs/raycluster"
	testingrayjob "sigs.k8s.io/kueue/pkg/util/testingjobs/rayjob"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/util"
)

type rayAutoscalingTestContext struct {
	managerNs         *corev1.Namespace
	managerCq         *kueue.ClusterQueue
	managerLq         *kueue.LocalQueue
	multiKueueAc      *kueue.AdmissionCheck
	kubernetesClients kubernetesClientsMap
}

func liveRayWorkloadSlice(g gomega.Gomega, c client.Client, ns, sliceName string) *kueue.Workload {
	wls := &kueue.WorkloadList{}
	g.Expect(c.List(ctx, wls, client.InNamespace(ns))).To(gomega.Succeed())
	var live []kueue.Workload
	for i := range wls.Items {
		if workloadslicing.SliceName(&wls.Items[i]) == sliceName &&
			!apimeta.IsStatusConditionTrue(wls.Items[i].Status.Conditions, kueue.WorkloadFinished) {
			live = append(live, wls.Items[i])
		}
	}
	g.Expect(live).To(gomega.HaveLen(1))
	return &live[0]
}

func registerRayAutoscalingTests(testContext func() rayAutoscalingTestContext) {
	ginkgo.Describe("Ray worker-side autoscaling", ginkgo.Label("feature:kuberay-multikueue-autoscaling"), func() {
		ginkgo.It("Should reflect worker-side autoscaler resizes (up, down, and up again) of a RayJob's child RayCluster back on the manager", func() {
			tc := testContext()
			runRayJobAutoscalingTest(tc.managerNs, tc.managerCq, tc.managerLq, tc.multiKueueAc, tc.kubernetesClients)
		})

		ginkgo.It("Should reflect worker-side autoscaler resizes (up, down, and up again) of an elastic RayCluster back on the manager", func() {
			tc := testContext()
			runRayClusterAutoscalingTest(tc.managerNs, tc.managerCq, tc.managerLq, tc.multiKueueAc, tc.kubernetesClients)
		})

		ginkgo.It("Should admit two consecutive RayCluster scale-ups one worker at a time", func() {
			tc := testContext()
			runRayClusterSequentialScaleUpTest(tc.managerNs, tc.managerLq, tc.multiKueueAc, tc.kubernetesClients)
		})
	})
}

func runRayClusterSequentialScaleUpTest(
	managerNs *corev1.Namespace,
	managerLq *kueue.LocalQueue,
	multiKueueAc *kueue.AdmissionCheck,
	kubernetesClients kubernetesClientsMap,
) {
	const (
		workerResource = "worker-unit"
		actorA         = "raycluster-sequential-scale-up-actor-a"
		actorB         = "raycluster-sequential-scale-up-actor-b"
	)

	rayCluster := testingraycluster.MakeCluster("raycluster-sequential-scale-up", managerNs.Name).
		Suspend(true).
		SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
		Queue(managerLq.Name).
		WithEnableAutoscaling(new(true)).
		WithAutoscalerOptions(&rayv1.AutoscalerOptions{
			IdleTimeoutSeconds: ptr.To[int32](1),
			Env: []corev1.EnvVar{{
				Name:  "AUTOSCALER_UPDATE_INTERVAL_S",
				Value: "1",
			}},
		}).
		FirstWorkerGroupReplicas(0, 0, 2).
		RayStartParam(rayv1.HeadNode, "num-cpus", "0").
		RayStartParam(rayv1.WorkerNode, "resources", fmt.Sprintf(`'{%q: 1}'`, workerResource)).
		RequestAndLimit(rayv1.HeadNode, corev1.ResourceCPU, "750m").
		RequestAndLimit(rayv1.WorkerNode, corev1.ResourceCPU, "250m").
		Image(rayv1.HeadNode, util.GetKuberayTestImage(), []string{}).
		Image(rayv1.WorkerNode, util.GetKuberayTestImage(), []string{}).
		Obj()

	ginkgo.By("Creating the elastic RayCluster with zero initial workers", func() {
		util.MustCreate(ctx, k8sManagerClient, rayCluster)
	})

	gomega.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(rayCluster), rayCluster)).To(gomega.Succeed())
	wlLookupKey := types.NamespacedName{
		Name:      jobframework.GetWorkloadNameForOwnerWithGVKAndGeneration(rayCluster.Name, rayCluster.UID, rayv1.GroupVersion.WithKind("RayCluster"), rayCluster.GetGeneration()),
		Namespace: managerNs.Name,
	}
	admittedWorkerName := util.ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlLookupKey, multiKueueAc.Name)
	// The Ray and autoscaler containers in the head Pod request 1250m in total,
	// exceeding worker2's 1200m ClusterQueue quota. This ensures placement on
	// worker1, where the Ray autoscaler has enough quota to scale up to two worker Pods.
	gomega.Expect(admittedWorkerName).To(gomega.HavePrefix("worker1-"))
	admittedWorker := kubernetesClients[admittedWorkerName]
	workerClient := admittedWorker.client
	rayClusterKey := client.ObjectKeyFromObject(rayCluster)

	ginkgo.By("Waiting for the zero-worker RayCluster to become ready on the worker cluster", func() {
		workerRayCluster := &rayv1.RayCluster{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(workerClient.Get(ctx, rayClusterKey, workerRayCluster)).To(gomega.Succeed())
			g.Expect(ptr.Deref(workerRayCluster.Spec.Suspend, true)).To(gomega.BeFalse())
			g.Expect(apimeta.IsStatusConditionTrue(workerRayCluster.Status.Conditions, string(rayv1.HeadPodReady))).To(gomega.BeTrue())
			g.Expect(workerRayCluster.Status.DesiredWorkerReplicas).To(gomega.Equal(int32(0)))
		}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed(), util.AssertMsg("RayCluster did not become ready", workerRayCluster))
	})

	initialSlice := liveRayWorkloadSlice(gomega.Default, k8sManagerClient, managerNs.Name, wlLookupKey.Name)
	ginkgo.By("Creating the first actor so the autoscaler scales from zero to one worker", func() {
		util.CreateDetachedRayActor(
			ctx, workerClient, admittedWorker.cfg, admittedWorker.restClient, rayClusterKey, actorA, workerResource,
		)
	})

	var firstScaleUpSlice *kueue.Workload
	ginkgo.By("Checking the first scale-up is admitted and exactly one worker runs", func() {
		firstScaleUpSlice = util.ExpectNewWorkloadSlice(ctx, k8sManagerClient, initialSlice)
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(firstScaleUpSlice), firstScaleUpSlice)).To(gomega.Succeed())

			workerPods, err := util.GetRayClusterWorkerPods(ctx, workerClient, rayClusterKey, corev1.PodRunning)
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(workerPods).To(gomega.HaveLen(1))

			g.Expect(podset.FindPodSetByName(firstScaleUpSlice.Spec.PodSets, "workers-group-0").Count).To(gomega.Equal(int32(1)))
			g.Expect(apimeta.IsStatusConditionTrue(firstScaleUpSlice.Status.Conditions, kueue.WorkloadAdmitted)).To(gomega.BeTrue())
		}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.By("Creating the second actor so the autoscaler requests a second worker", func() {
		util.CreateDetachedRayActor(
			ctx, workerClient, admittedWorker.cfg, admittedWorker.restClient, rayClusterKey, actorB, workerResource,
		)
	})

	ginkgo.By("Checking the second scale-up is admitted and exactly two workers run", func() {
		secondScaleUpSlice := util.ExpectNewWorkloadSlice(ctx, k8sManagerClient, firstScaleUpSlice)
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(secondScaleUpSlice), secondScaleUpSlice)).To(gomega.Succeed())

			workerPods, err := util.GetRayClusterWorkerPods(ctx, workerClient, rayClusterKey, corev1.PodRunning)
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(workerPods).To(gomega.HaveLen(2))

			g.Expect(podset.FindPodSetByName(secondScaleUpSlice.Spec.PodSets, "workers-group-0").Count).To(gomega.Equal(int32(2)))
			g.Expect(apimeta.IsStatusConditionTrue(secondScaleUpSlice.Status.Conditions, kueue.WorkloadAdmitted)).To(gomega.BeTrue())
		}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
	})
}

func runRayJobAutoscalingTest(
	managerNs *corev1.Namespace,
	managerCq *kueue.ClusterQueue,
	managerLq *kueue.LocalQueue,
	multiKueueAc *kueue.AdmissionCheck,
	kubernetesClients kubernetesClientsMap,
) {
	const (
		workerResource = "worker-unit"
		actorA         = "rayjob-actor-a"
		actorB         = "rayjob-actor-b"
		actorC         = "rayjob-actor-c"
	)
	rayJob := testingrayjob.MakeJob("rayjob-autoscale", managerNs.Name).
		Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
		Queue(managerLq.Name).
		WithSubmissionMode(rayv1.K8sJobMode).
		EnableInTreeAutoscaling().
		Entrypoint("python -c \"import time; time.sleep(3600)\"").
		RayStartParam(rayv1.HeadNode, "num-cpus", "0").
		RayStartParam(rayv1.WorkerNode, "resources", fmt.Sprintf(`'{%q: 1}'`, workerResource)).
		RequestAndLimit(rayv1.HeadNode, corev1.ResourceCPU, "750m").
		RequestAndLimit(rayv1.WorkerNode, corev1.ResourceCPU, "250m").
		// Keep the total CPU request at 2 cores when the RayCluster scales to two
		// workers, matching the manager and worker1 ClusterQueue quotas.
		WithSubmitterPodTemplate(corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "rayjob-submitter",
						Image: util.GetKuberayTestImage(),
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m")},
							Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m")},
						},
					},
				},
				RestartPolicy: corev1.RestartPolicyNever,
			},
		}).
		Image(rayv1.HeadNode, util.GetKuberayTestImage()).
		Image(rayv1.WorkerNode, util.GetKuberayTestImage()).
		Obj()
	rayJob.Spec.RayClusterSpec.AutoscalerOptions = &rayv1.AutoscalerOptions{IdleTimeoutSeconds: ptr.To[int32](1)}
	rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[0].Replicas = ptr.To[int32](0)
	rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[0].MinReplicas = ptr.To[int32](0)
	rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[0].MaxReplicas = ptr.To[int32](2)

	ginkgo.By("Creating the elastic autoscaling RayJob", func() {
		util.MustCreate(ctx, k8sManagerClient, rayJob)
	})

	workloads := util.ExpectWorkloadsInNamespace(ctx, k8sManagerClient, managerNs.Name, 1)
	wlLookupKey := client.ObjectKeyFromObject(&workloads[0])
	admittedWorkerName := util.ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlLookupKey, multiKueueAc.Name)
	// The Ray and autoscaler containers in the head Pod request 1250m in total,
	// exceeding worker2's 1200m ClusterQueue quota. This ensures placement on
	// worker1, where the Ray autoscaler has enough quota to scale up to two worker Pods.
	gomega.Expect(admittedWorkerName).To(gomega.HavePrefix("worker1-"))
	admittedWorker := kubernetesClients[admittedWorkerName]
	workerClient := admittedWorker.client

	workerRayJob := &rayv1.RayJob{}
	workerRayCluster := &rayv1.RayCluster{}
	ginkgo.By("Waiting for the RayJob to create its child RayCluster on the worker cluster", func() {
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(workerClient.Get(ctx, client.ObjectKeyFromObject(rayJob), workerRayJob)).To(gomega.Succeed())
			g.Expect(workerRayJob.Status.RayClusterName).NotTo(gomega.BeEmpty())
			g.Expect(workerClient.Get(ctx, client.ObjectKey{Name: workerRayJob.Status.RayClusterName, Namespace: workerRayJob.Namespace}, workerRayCluster)).To(gomega.Succeed())
		}, util.MediumTimeout, util.Interval).Should(gomega.Succeed(), util.AssertMsg("RayJob did not create its child RayCluster", workerRayJob))
	})
	childKey := client.ObjectKeyFromObject(workerRayCluster)

	ginkgo.By("Waiting for the zero-worker child RayCluster to become ready on the worker cluster", func() {
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(workerClient.Get(ctx, childKey, workerRayCluster)).To(gomega.Succeed())
			g.Expect(ptr.Deref(workerRayCluster.Spec.Suspend, false)).To(gomega.BeFalse())
			g.Expect(apimeta.IsStatusConditionTrue(workerRayCluster.Status.Conditions, string(rayv1.HeadPodReady))).To(gomega.BeTrue())
			g.Expect(workerRayCluster.Status.DesiredWorkerReplicas).To(gomega.Equal(int32(0)))
		}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed(), util.AssertMsg("RayJob child RayCluster did not become ready", workerRayCluster))
	})

	ginkgo.By("Creating two detached actors so the autoscaler scales the child up to two workers", func() {
		util.CreateDetachedRayActor(
			ctx, workerClient, admittedWorker.cfg, admittedWorker.restClient, childKey, actorA, workerResource,
		)
		util.CreateDetachedRayActor(
			ctx, workerClient, admittedWorker.cfg, admittedWorker.restClient, childKey, actorB, workerResource,
		)
	})

	var upSliceName string
	ginkgo.By("Checking the scale-up is reflected on the manager", func() {
		gomega.Eventually(func(g gomega.Gomega) {
			workerPods, err := util.GetRayClusterWorkerPods(ctx, workerClient, childKey, corev1.PodRunning)
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(workerPods).To(gomega.HaveLen(2))

			createdRayJob := &rayv1.RayJob{}
			g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(rayJob), createdRayJob)).To(gomega.Succeed())
			g.Expect(createdRayJob.Annotations).To(gomega.HaveKeyWithValue(
				workloadraycluster.RayClusterPodsetReplicaSizesAnnotation, `[{"name":"workers-group-0","count":2}]`))

			managerCQ := &kueue.ClusterQueue{}
			g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(managerCq), managerCQ)).To(gomega.Succeed())
			g.Expect(managerCQ.Status.AdmittedWorkloads).To(gomega.Equal(int32(1)))
			upSlice := liveRayWorkloadSlice(g, k8sManagerClient, managerNs.Name, wlLookupKey.Name)
			upSliceName = upSlice.Name
			g.Expect(podset.FindPodSetByName(upSlice.Spec.PodSets, "workers-group-0").Count).To(gomega.Equal(int32(2)))
		}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.By("Terminating both actors so the autoscaler scales the child back down to zero workers", func() {
		util.TerminateDetachedRayActor(
			ctx, workerClient, admittedWorker.cfg, admittedWorker.restClient, childKey, actorA,
		)
		util.TerminateDetachedRayActor(
			ctx, workerClient, admittedWorker.cfg, admittedWorker.restClient, childKey, actorB,
		)
	})

	ginkgo.By("Checking the scale-down is reflected on the manager", func() {
		gomega.Eventually(func(g gomega.Gomega) {
			workerPods, err := util.GetRayClusterWorkerPods(ctx, workerClient, childKey, corev1.PodRunning)
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(workerPods).To(gomega.BeEmpty())

			createdRayJob := &rayv1.RayJob{}
			g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(rayJob), createdRayJob)).To(gomega.Succeed())
			g.Expect(createdRayJob.Annotations).To(gomega.HaveKeyWithValue(
				workloadraycluster.RayClusterPodsetReplicaSizesAnnotation, `[{"name":"workers-group-0","count":0}]`))

			managerCQ := &kueue.ClusterQueue{}
			g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(managerCq), managerCQ)).To(gomega.Succeed())
			g.Expect(managerCQ.Status.AdmittedWorkloads).To(gomega.Equal(int32(1)))
			downSlice := liveRayWorkloadSlice(g, k8sManagerClient, managerNs.Name, wlLookupKey.Name)
			g.Expect(downSlice.Name).To(gomega.Equal(upSliceName))
			g.Expect(podset.FindPodSetByName(downSlice.Spec.PodSets, "workers-group-0").Count).To(gomega.Equal(int32(0)))
		}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.By("Creating one detached actor so the autoscaler scales the child back up to one worker", func() {
		util.CreateDetachedRayActor(
			ctx, workerClient, admittedWorker.cfg, admittedWorker.restClient, childKey, actorC, workerResource,
		)
	})

	ginkgo.By("Checking the second scale-up is reflected on the manager", func() {
		gomega.Eventually(func(g gomega.Gomega) {
			workerPods, err := util.GetRayClusterWorkerPods(ctx, workerClient, childKey, corev1.PodRunning)
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(workerPods).To(gomega.HaveLen(1))

			createdRayJob := &rayv1.RayJob{}
			g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(rayJob), createdRayJob)).To(gomega.Succeed())
			g.Expect(createdRayJob.Annotations).To(gomega.HaveKeyWithValue(
				workloadraycluster.RayClusterPodsetReplicaSizesAnnotation, `[{"name":"workers-group-0","count":1}]`))

			managerCQ := &kueue.ClusterQueue{}
			g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(managerCq), managerCQ)).To(gomega.Succeed())
			g.Expect(managerCQ.Status.AdmittedWorkloads).To(gomega.Equal(int32(1)))
			newUpSlice := liveRayWorkloadSlice(g, k8sManagerClient, managerNs.Name, wlLookupKey.Name)
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
	kubernetesClients kubernetesClientsMap,
) {
	const (
		workerResource = "worker-unit"
		actorA         = "raycluster-actor-a"
		actorB         = "raycluster-actor-b"
		actorC         = "raycluster-actor-c"
	)
	rayCluster := testingraycluster.MakeCluster("raycluster-autoscale", managerNs.Name).
		Suspend(true).
		SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
		Queue(managerLq.Name).
		WithEnableAutoscaling(new(true)).
		WithAutoscalerOptions(&rayv1.AutoscalerOptions{IdleTimeoutSeconds: ptr.To[int32](1)}).
		FirstWorkerGroupReplicas(0, 0, 2).
		RayStartParam(rayv1.HeadNode, "num-cpus", "0").
		RayStartParam(rayv1.WorkerNode, "resources", fmt.Sprintf(`'{%q: 1}'`, workerResource)).
		RequestAndLimit(rayv1.HeadNode, corev1.ResourceCPU, "750m").
		RequestAndLimit(rayv1.WorkerNode, corev1.ResourceCPU, "250m").
		Image(rayv1.HeadNode, util.GetKuberayTestImage(), []string{}).
		Image(rayv1.WorkerNode, util.GetKuberayTestImage(), []string{}).
		Obj()

	ginkgo.By("Creating the elastic autoscaling RayCluster", func() {
		util.MustCreate(ctx, k8sManagerClient, rayCluster)
	})

	gomega.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(rayCluster), rayCluster)).To(gomega.Succeed())
	wlLookupKey := types.NamespacedName{
		Name:      jobframework.GetWorkloadNameForOwnerWithGVKAndGeneration(rayCluster.Name, rayCluster.UID, rayv1.GroupVersion.WithKind("RayCluster"), rayCluster.GetGeneration()),
		Namespace: managerNs.Name,
	}
	admittedWorkerName := util.ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlLookupKey, multiKueueAc.Name)
	// The Ray and autoscaler containers in the head Pod request 1250m in total,
	// exceeding worker2's 1200m ClusterQueue quota. This ensures placement on
	// worker1, where the Ray autoscaler has enough quota to scale up to two worker Pods.
	gomega.Expect(admittedWorkerName).To(gomega.HavePrefix("worker1-"))
	admittedWorker := kubernetesClients[admittedWorkerName]
	workerClient := admittedWorker.client
	rayClusterKey := client.ObjectKeyFromObject(rayCluster)

	ginkgo.By("Waiting for the zero-worker RayCluster to become ready on the worker cluster", func() {
		workerRayCluster := &rayv1.RayCluster{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(workerClient.Get(ctx, rayClusterKey, workerRayCluster)).To(gomega.Succeed())
			g.Expect(ptr.Deref(workerRayCluster.Spec.Suspend, true)).To(gomega.BeFalse())
			g.Expect(apimeta.IsStatusConditionTrue(workerRayCluster.Status.Conditions, string(rayv1.HeadPodReady))).To(gomega.BeTrue())
			g.Expect(workerRayCluster.Status.DesiredWorkerReplicas).To(gomega.Equal(int32(0)))
		}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed(), util.AssertMsg("RayCluster did not become ready", workerRayCluster))
	})

	ginkgo.By("Creating two detached actors so the autoscaler scales up to two workers", func() {
		util.CreateDetachedRayActor(
			ctx, workerClient, admittedWorker.cfg, admittedWorker.restClient, rayClusterKey, actorA, workerResource,
		)
		util.CreateDetachedRayActor(
			ctx, workerClient, admittedWorker.cfg, admittedWorker.restClient, rayClusterKey, actorB, workerResource,
		)
	})

	var upSliceName string
	ginkgo.By("Checking the scale-up is reflected on the manager and worker", func() {
		gomega.Eventually(func(g gomega.Gomega) {
			workerPods, err := util.GetRayClusterWorkerPods(ctx, workerClient, rayClusterKey, corev1.PodRunning)
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(workerPods).To(gomega.HaveLen(2))

			createdRayCluster := &rayv1.RayCluster{}
			g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(rayCluster), createdRayCluster)).To(gomega.Succeed())
			g.Expect(createdRayCluster.Annotations).To(gomega.HaveKeyWithValue(
				workloadraycluster.RayClusterPodsetReplicaSizesAnnotation, `[{"name":"workers-group-0","count":2}]`))
			g.Expect(createdRayCluster.Status.DesiredWorkerReplicas).To(gomega.Equal(int32(2)))

			managerCQ := &kueue.ClusterQueue{}
			g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(managerCq), managerCQ)).To(gomega.Succeed())
			g.Expect(managerCQ.Status.AdmittedWorkloads).To(gomega.Equal(int32(1)))
			upSlice := liveRayWorkloadSlice(g, k8sManagerClient, managerNs.Name, wlLookupKey.Name)
			upSliceName = upSlice.Name
			g.Expect(podset.FindPodSetByName(upSlice.Spec.PodSets, "workers-group-0").Count).To(gomega.Equal(int32(2)))

			workerSlice := liveRayWorkloadSlice(g, workerClient, managerNs.Name, wlLookupKey.Name)
			g.Expect(apimeta.IsStatusConditionTrue(workerSlice.Status.Conditions, kueue.WorkloadAdmitted)).To(gomega.BeTrue())
			g.Expect(podset.FindPodSetByName(workerSlice.Spec.PodSets, "workers-group-0").Count).To(gomega.Equal(int32(2)))
		}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.By("Checking the worker RayCluster keeps the autoscaled size", func() {
		gomega.Consistently(func(g gomega.Gomega) {
			workerRayCluster := &rayv1.RayCluster{}
			g.Expect(workerClient.Get(ctx, rayClusterKey, workerRayCluster)).To(gomega.Succeed())
			g.Expect(ptr.Deref(workerRayCluster.Spec.WorkerGroupSpecs[0].Replicas, -1)).To(gomega.BeEquivalentTo(int32(2)))
			g.Expect(ptr.Deref(workerRayCluster.Spec.Suspend, false)).To(gomega.BeFalse())
		}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
	})

	ginkgo.By("Terminating both actors so the autoscaler scales back down to zero workers", func() {
		util.TerminateDetachedRayActor(
			ctx, workerClient, admittedWorker.cfg, admittedWorker.restClient, rayClusterKey, actorA,
		)
		util.TerminateDetachedRayActor(
			ctx, workerClient, admittedWorker.cfg, admittedWorker.restClient, rayClusterKey, actorB,
		)
	})

	ginkgo.By("Checking the scale-down is reflected on the manager and worker", func() {
		gomega.Eventually(func(g gomega.Gomega) {
			workerPods, err := util.GetRayClusterWorkerPods(ctx, workerClient, rayClusterKey, corev1.PodRunning)
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(workerPods).To(gomega.BeEmpty())

			createdRayCluster := &rayv1.RayCluster{}
			g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(rayCluster), createdRayCluster)).To(gomega.Succeed())
			g.Expect(createdRayCluster.Annotations).To(gomega.HaveKeyWithValue(
				workloadraycluster.RayClusterPodsetReplicaSizesAnnotation, `[{"name":"workers-group-0","count":0}]`))
			g.Expect(createdRayCluster.Status.DesiredWorkerReplicas).To(gomega.Equal(int32(0)))

			managerCQ := &kueue.ClusterQueue{}
			g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(managerCq), managerCQ)).To(gomega.Succeed())
			g.Expect(managerCQ.Status.AdmittedWorkloads).To(gomega.Equal(int32(1)))
			downSlice := liveRayWorkloadSlice(g, k8sManagerClient, managerNs.Name, wlLookupKey.Name)
			g.Expect(downSlice.Name).To(gomega.Equal(upSliceName))
			g.Expect(podset.FindPodSetByName(downSlice.Spec.PodSets, "workers-group-0").Count).To(gomega.Equal(int32(0)))

			workerSlice := liveRayWorkloadSlice(g, workerClient, managerNs.Name, wlLookupKey.Name)
			g.Expect(apimeta.IsStatusConditionTrue(workerSlice.Status.Conditions, kueue.WorkloadAdmitted)).To(gomega.BeTrue())
			g.Expect(podset.FindPodSetByName(workerSlice.Spec.PodSets, "workers-group-0").Count).To(gomega.Equal(int32(0)))
		}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.By("Checking the worker RayCluster keeps running at the scaled-down size", func() {
		gomega.Consistently(func(g gomega.Gomega) {
			workerRayCluster := &rayv1.RayCluster{}
			g.Expect(workerClient.Get(ctx, rayClusterKey, workerRayCluster)).To(gomega.Succeed())
			g.Expect(ptr.Deref(workerRayCluster.Spec.WorkerGroupSpecs[0].Replicas, -1)).To(gomega.BeEquivalentTo(int32(0)))
			g.Expect(ptr.Deref(workerRayCluster.Spec.Suspend, false)).To(gomega.BeFalse())
		}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
	})

	ginkgo.By("Creating one detached actor so the autoscaler scales back up to one worker", func() {
		util.CreateDetachedRayActor(
			ctx, workerClient, admittedWorker.cfg, admittedWorker.restClient, rayClusterKey, actorC, workerResource,
		)
	})

	ginkgo.By("Checking the second scale-up is reflected on the manager and worker", func() {
		gomega.Eventually(func(g gomega.Gomega) {
			workerPods, err := util.GetRayClusterWorkerPods(ctx, workerClient, rayClusterKey, corev1.PodRunning)
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(workerPods).To(gomega.HaveLen(1))

			createdRayCluster := &rayv1.RayCluster{}
			g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(rayCluster), createdRayCluster)).To(gomega.Succeed())
			g.Expect(createdRayCluster.Annotations).To(gomega.HaveKeyWithValue(
				workloadraycluster.RayClusterPodsetReplicaSizesAnnotation, `[{"name":"workers-group-0","count":1}]`))
			g.Expect(createdRayCluster.Status.DesiredWorkerReplicas).To(gomega.Equal(int32(1)))

			managerCQ := &kueue.ClusterQueue{}
			g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(managerCq), managerCQ)).To(gomega.Succeed())
			g.Expect(managerCQ.Status.AdmittedWorkloads).To(gomega.Equal(int32(1)))
			newUpSlice := liveRayWorkloadSlice(g, k8sManagerClient, managerNs.Name, wlLookupKey.Name)
			g.Expect(newUpSlice.Name).NotTo(gomega.Equal(upSliceName))
			g.Expect(podset.FindPodSetByName(newUpSlice.Spec.PodSets, "workers-group-0").Count).To(gomega.Equal(int32(1)))

			workerSlice := liveRayWorkloadSlice(g, workerClient, managerNs.Name, wlLookupKey.Name)
			g.Expect(apimeta.IsStatusConditionTrue(workerSlice.Status.Conditions, kueue.WorkloadAdmitted)).To(gomega.BeTrue())
			g.Expect(podset.FindPodSetByName(workerSlice.Spec.PodSets, "workers-group-0").Count).To(gomega.Equal(int32(1)))
		}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.By("Checking the worker RayCluster keeps the re-scaled-up size", func() {
		gomega.Consistently(func(g gomega.Gomega) {
			workerRayCluster := &rayv1.RayCluster{}
			g.Expect(workerClient.Get(ctx, rayClusterKey, workerRayCluster)).To(gomega.Succeed())
			g.Expect(ptr.Deref(workerRayCluster.Spec.WorkerGroupSpecs[0].Replicas, -1)).To(gomega.BeEquivalentTo(int32(1)))
			g.Expect(ptr.Deref(workerRayCluster.Spec.Suspend, false)).To(gomega.BeFalse())
		}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
	})
}
