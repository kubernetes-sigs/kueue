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
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	kuberayutils "github.com/ray-project/kuberay/ray-operator/controllers/ray/utils"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	workloadraycluster "sigs.k8s.io/kueue/pkg/controller/jobs/raycluster"
	workloadrayjob "sigs.k8s.io/kueue/pkg/controller/jobs/rayjob"
	workloadrayservice "sigs.k8s.io/kueue/pkg/controller/jobs/rayservice"
	utilpodset "sigs.k8s.io/kueue/pkg/util/podset"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjobspod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	testingraycluster "sigs.k8s.io/kueue/pkg/util/testingjobs/raycluster"
	testingrayjob "sigs.k8s.io/kueue/pkg/util/testingjobs/rayjob"
	testingrayservice "sigs.k8s.io/kueue/pkg/util/testingjobs/rayservice"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadfinish "sigs.k8s.io/kueue/pkg/workload/finish"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/util"
)

// parsePodSetReplicaCount parses the PodsetReplicaSizesAnnotation JSON and
// returns the count for the given group name.
func parsePodSetReplicaCount(annotationValue, groupName string) (int32, error) {
	var podSets []struct {
		Name  string `json:"name"`
		Count int32  `json:"count"`
	}
	if err := json.Unmarshal([]byte(annotationValue), &podSets); err != nil {
		return 0, fmt.Errorf("failed to parse annotation: %w", err)
	}
	for _, ps := range podSets {
		if ps.Name == groupName {
			return ps.Count, nil
		}
	}
	return 0, fmt.Errorf("group %q not found in annotation", groupName)
}

// findContainer returns the container with the given name, or nil if absent.
func findContainer(containers []corev1.Container, name string) *corev1.Container {
	for i := range containers {
		if containers[i].Name == name {
			return &containers[i]
		}
	}
	return nil
}

const (
	// objectStoreMemory is the Ray object store memory (bytes) set on each node
	// to avoid Ray auto-sizing it too large for the constrained test environment.
	objectStoreMemory = "100000000"
)

var _ = ginkgo.Describe("Kuberay", ginkgo.Label("area:singlecluster", "feature:kuberay"), func() {
	var (
		ns                 *corev1.Namespace
		rf                 *kueue.ResourceFlavor
		cq                 *kueue.ClusterQueue
		lq                 *kueue.LocalQueue
		resourceFlavorName string
		clusterQueueName   string
		localQueueName     string
	)

	getRunningRayWorkerPodNames := func(g gomega.Gomega) []string {
		pods := &corev1.PodList{}
		g.Expect(k8sClient.List(ctx, pods,
			client.InNamespace(ns.Name),
			client.MatchingLabels{
				"ray.io/node-type": "worker",
			},
		)).To(gomega.Succeed())
		var podNames []string
		for _, pod := range pods.Items {
			if strings.Contains(pod.Name, "workers") && pod.Status.Phase == corev1.PodRunning {
				podNames = append(podNames, pod.Name)
			}
		}
		return podNames
	}

	getRayHeadPod := func(g gomega.Gomega) corev1.Pod {
		pods := &corev1.PodList{}
		g.Expect(k8sClient.List(ctx, pods,
			client.InNamespace(ns.Name),
			client.MatchingLabels{
				"ray.io/node-type": "head",
			},
		)).To(gomega.Succeed())
		g.Expect(pods.Items).NotTo(gomega.BeEmpty())
		return pods.Items[0]
	}

	createCurlPod := func(name string) *corev1.Pod {
		pod := testingjobspod.MakePod(name, ns.Name).
			Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
			TerminationGracePeriod(1).
			Obj()
		util.MustCreate(ctx, k8sClient, pod)
		util.WaitForPodRunning(ctx, k8sClient, pod)
		return pod
	}

	requestRayService := func(curlPod *corev1.Pod, rayService *rayv1.RayService, path string) ([]byte, []byte, error) {
		return util.KExecute(ctx, cfg, restClient, ns.Name, curlPod.Name, curlPod.Spec.Containers[0].Name, []string{
			"curl",
			"--fail",
			"--silent",
			"--show-error",
			"--max-time",
			"60",
			fmt.Sprintf("http://%s:8000%s", kuberayutils.GenerateServeServiceName(rayService.Name), path),
		})
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
					// Head + workers, plus 500m for the autoscaler sidecar that
					// BuildPodSets accounts for on in-tree-autoscaling clusters,
					// with enough headroom for the multi-step scale-up test to
					// reach 5 running workers (head 1 + sidecar 500m + submitter
					// 400m + 5x400m workers = 3.9 CPU).
					Resource(corev1.ResourceCPU, "4").
					Resource(corev1.ResourceMemory, "2Gi").Obj()).
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

	ginkgo.It("Should run a rayjob if admitted", ginkgo.Label("shard:kuberay-a"), func() {
		kuberayTestImage := util.GetKuberayTestImage()

		rayJob := testingrayjob.MakeJob("rayjob", ns.Name).
			Queue(localQueueName).
			WithSubmissionMode(rayv1.K8sJobMode).
			Entrypoint("python -c \"import ray; ray.init(); print(ray.cluster_resources())\"").
			RequestAndLimit(rayv1.HeadNode, corev1.ResourceCPU, "1").
			RayStartParam(rayv1.HeadNode, "object-store-memory", objectStoreMemory).
			RequestAndLimit(rayv1.WorkerNode, corev1.ResourceCPU, "400m").
			RayStartParam(rayv1.WorkerNode, "object-store-memory", objectStoreMemory).
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
			TerminationGracePeriod(1).
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
			createdRayJob := &rayv1.RayJob{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rayJob), createdRayJob)).To(gomega.Succeed())
				g.Expect(createdRayJob.Spec.Suspend).To(gomega.BeFalse())
				g.Expect(createdRayJob.Status.JobDeploymentStatus).To(gomega.Equal(rayv1.JobDeploymentStatusRunning))
			}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed(), util.AssertMsg("RayJob cluster did not become ready", createdRayJob))
		})

		ginkgo.By("Verify ray job worker pods have queue labels assigned", func() {
			pods := &corev1.PodList{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name))).To(gomega.Succeed())
				g.Expect(pods.Items).ToNot(gomega.BeEmpty())
				for _, pod := range pods.Items {
					g.Expect(pod.Labels[constants.ClusterQueueLabel]).To(gomega.Equal(clusterQueueName))
					g.Expect(pod.Labels[constants.LocalQueueLabel]).To(gomega.Equal(localQueueName))
				}
			}, util.Timeout, util.Interval).Should(gomega.Succeed(), util.AssertMsgObjList("Worker pods missing expected queue labels", pods))
		})

		ginkgo.By("Waiting for the RayJob to finish", func() {
			createdRayJob := &rayv1.RayJob{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rayJob), createdRayJob)).To(gomega.Succeed())
				g.Expect(createdRayJob.Status.JobDeploymentStatus).To(gomega.Equal(rayv1.JobDeploymentStatusComplete))
				g.Expect(createdRayJob.Status.JobStatus).To(gomega.Equal(rayv1.JobStatusSucceeded))
			}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed(), util.AssertMsg("RayJob did not finish successfully", createdRayJob))
		})
	})

	ginkgo.It("Should run a rayjob with InTreeAutoscaling", ginkgo.Label("shard:kuberay-b"), func() {
		kuberayTestImage := util.GetKuberayTestImage()

		// Create ConfigMap with Python script
		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "rayjob-autoscaling",
				Namespace: ns.Name,
			},
			Data: map[string]string{
				"sample_code.py": `import ray
import os

ray.init()

@ray.remote(num_cpus=1)
def my_task(x, s):
    import time
    time.sleep(s)
    return x * x

# A queue of 20 short tasks (~200 task-seconds over at most 3 slots:
# 1 on head + 1 per worker) triggers scale-up to 2 workers and keeps
# both busy through pod replacement verification: pending tasks hold
# the demand no matter how long scale-up or replacement takes, and the
# job drains quickly once the queue is empty. With a few long tasks
# instead, their expiry races the autoscaler's idle scale-down
# (idleTimeoutSeconds=10) during verification.
print(ray.get([my_task.remote(i, 10) for i in range(20)]))`,
			},
		}

		volumes := []corev1.Volume{
			{
				Name: "script-volume",
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: "rayjob-autoscaling",
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

		rayJob := testingrayjob.MakeJob("rayjob-autoscaling", ns.Name).
			Queue(localQueueName).
			Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			EnableInTreeAutoscaling().
			MaxWorkerReplicas(2).
			WithSubmissionMode(rayv1.K8sJobMode).
			Entrypoint("python /home/ray/samples/sample_code.py").
			RequestAndLimit(rayv1.HeadNode, corev1.ResourceCPU, "1").
			RayStartParam(rayv1.HeadNode, "object-store-memory", objectStoreMemory).
			RequestAndLimit(rayv1.WorkerNode, corev1.ResourceCPU, "400m").
			RayStartParam(rayv1.WorkerNode, "object-store-memory", objectStoreMemory).
			WithSubmitterPodTemplate(corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "rayjob-submitter",
							Image: kuberayTestImage,
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU: resource.MustParse("400m"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU: resource.MustParse("400m"),
								},
							},
						},
					},
					RestartPolicy: corev1.RestartPolicyOnFailure,
				},
			}).
			Image(rayv1.HeadNode, kuberayTestImage).
			Image(rayv1.WorkerNode, kuberayTestImage).
			Volumes(rayv1.HeadNode, volumes).
			VolumeMounts(rayv1.HeadNode, volumeMounts).
			TerminationGracePeriod(1).
			Obj()

		ginkgo.By("Creating the ConfigMap", func() {
			gomega.Expect(k8sClient.Create(ctx, configMap)).Should(gomega.Succeed())
		})

		ginkgo.By("Creating the rayJob", func() {
			gomega.Expect(k8sClient.Create(ctx, rayJob)).Should(gomega.Succeed())
		})

		ginkgo.By("Checking one workload is created", func() {
			workloadList := &kueue.WorkloadList{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.List(ctx, workloadList, client.InNamespace(ns.Name))).To(gomega.Succeed())
				g.Expect(workloadList.Items).NotTo(gomega.BeEmpty(), "Expected at least one workload in namespace")
				hasAdmittedWorkload := false
				for _, wl := range workloadList.Items {
					if workload.IsAdmitted(&wl) || workloadfinish.IsFinished(&wl) {
						hasAdmittedWorkload = true
						break
					}
				}
				g.Expect(hasAdmittedWorkload).To(gomega.BeTrue(), "Expected admitted workload")
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed(), util.AssertMsgObjList("No admitted workload found in namespace", workloadList))
		})

		ginkgo.By("Waiting for the RayJob cluster become ready", func() {
			createdRayJob := &rayv1.RayJob{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rayJob), createdRayJob)).To(gomega.Succeed())
				g.Expect(createdRayJob.Spec.Suspend).To(gomega.BeFalse())
				g.Expect(createdRayJob.Status.JobDeploymentStatus).To(gomega.Equal(rayv1.JobDeploymentStatusRunning))
			}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed(), util.AssertMsg("RayJob cluster did not become ready", createdRayJob))
		})

		ginkgo.By("Waiting for 3 pods in rayjob namespace", func() {
			// 3 rayjob pods: head, worker, submitter job
			podList := &corev1.PodList{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.List(ctx, podList, client.InNamespace(ns.Name))).To(gomega.Succeed())
				g.Expect(podList.Items).To(gomega.HaveLen(3), "Expected exactly 3 pods in rayjob namespace")
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Waiting for exactly 1 worker pod", func() {
			podList := &corev1.PodList{}
			gomega.Eventually(func(g gomega.Gomega) {
				workerPodNames := getRunningRayWorkerPodNames(g)
				g.Expect(workerPodNames).To(gomega.HaveLen(1), "Expected exactly 1 pod with 'workers' in the name")
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed(), util.AssertMsgObjList("Expected exactly 1 worker pod", podList))
		})

		ginkgo.By("Waiting for exactly 1 non-finished admitted workload", func() {
			workloadList := &kueue.WorkloadList{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.List(ctx, workloadList, client.InNamespace(ns.Name))).To(gomega.Succeed())
				activeWorkloads := util.FindNonFinishedWorkloads(workloadList.Items)
				g.Expect(activeWorkloads).To(gomega.HaveLen(1), "Expected exactly 1 non-finished workload")
				g.Expect(workload.IsAdmitted(&activeWorkloads[0])).To(gomega.BeTrue(), "Expected admitted workload")
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Waiting for all 2 worker pods to be running", func() {
			podList := &corev1.PodList{}
			gomega.Eventually(func(g gomega.Gomega) {
				runningWorkers := getRunningRayWorkerPodNames(g)
				g.Expect(runningWorkers).To(gomega.HaveLen(2), "Expected 2 running worker pods")
			}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed(), util.AssertMsgObjList("Did not observe 2 running worker pods", podList))
		})

		var deletedPodName string
		ginkgo.By("Deleting one worker pod", func() {
			runningWorkers := getRunningRayWorkerPodNames(gomega.Default)
			gomega.Expect(runningWorkers).NotTo(gomega.BeEmpty())
			deletedPodName = runningWorkers[0]
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      deletedPodName,
					Namespace: ns.Name,
				},
			}
			gomega.Expect(k8sClient.Delete(ctx, pod)).To(gomega.Succeed())
		})

		ginkgo.By("Waiting for a new worker pod to replace the deleted one", func() {
			podList := &corev1.PodList{}
			gomega.Eventually(func(g gomega.Gomega) {
				runningWorkers := getRunningRayWorkerPodNames(g)
				g.Expect(runningWorkers).To(gomega.HaveLen(2), "Expected 2 running worker pods after replacement")
				g.Expect(runningWorkers).NotTo(gomega.ContainElement(deletedPodName),
					"Deleted pod should not be present among running workers")
			}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed(), util.AssertMsgObjList(fmt.Sprintf("Replacement worker pod did not appear after deleting %q", deletedPodName), podList))
		})

		ginkgo.By("Waiting for the RayJob to finish", func() {
			createdRayJob := &rayv1.RayJob{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rayJob), createdRayJob)).To(gomega.Succeed())
				g.Expect(createdRayJob.Status.JobDeploymentStatus).To(gomega.Equal(rayv1.JobDeploymentStatusComplete))
				g.Expect(createdRayJob.Status.JobStatus).To(gomega.Equal(rayv1.JobStatusSucceeded))
			}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed(), util.AssertMsg("RayJob did not finish successfully", createdRayJob))
		})
	})

	ginkgo.It("Should run a rayjob with multi scale-up steps", ginkgo.Label("shard:kuberay-a"), func() {
		kuberayTestImage := util.GetKuberayTestImage()

		// Create ConfigMap with Python script that triggers multiple scale-up phases
		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "rayjob-multi-scaleup",
				Namespace: ns.Name,
			},
			Data: map[string]string{
				"sample_code.py": `import ray
import os

ray.init()

# Explicitly request 1 CPU per task to ensure deterministic resource demand.
# Without num_cpus, Ray may detect high logical CPU count from the host
# and not trigger autoscaling.
@ray.remote(num_cpus=1)
def my_task(x, s):
    import time
    time.sleep(s)
    return x * x

# run tasks in sequence to avoid triggering autoscaling in the beginning
print([ray.get(my_task.remote(i, 1)) for i in range(4)])

# run tasks in with low parallelism to trigger autoscaling (scaling up)
# but to still not scale up to the max size
print(ray.get([my_task.remote(i, 8) for i in range(4)]))

# run tasks in parallel to trigger autoscaling (scaling up)
# Use longer sleep (8s) to give autoscaler time to detect demand,
# create workload slices, and schedule new workers.
print(ray.get([my_task.remote(i, 8) for i in range(16)]))

# run tasks in sequence to trigger scaling down; 20 tasks (~25s with
# scheduling overhead) keep the job alive through idle detection
# (idleTimeoutSeconds=10) and the scale-down annotation update
print([ray.get(my_task.remote(i, 1)) for i in range(20)])`,
			},
		}

		volumes := []corev1.Volume{
			{
				Name: "script-volume",
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: "rayjob-multi-scaleup",
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

		rayJob := testingrayjob.MakeJob("rayjob-multi-scaleup", ns.Name).
			Queue(localQueueName).
			Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			EnableInTreeAutoscaling().
			WithSubmissionMode(rayv1.K8sJobMode).
			Entrypoint("python /home/ray/samples/sample_code.py").
			RequestAndLimit(rayv1.HeadNode, corev1.ResourceCPU, "1").
			RayStartParam(rayv1.HeadNode, "object-store-memory", objectStoreMemory).
			RequestAndLimit(rayv1.WorkerNode, corev1.ResourceCPU, "400m").
			RayStartParam(rayv1.WorkerNode, "object-store-memory", objectStoreMemory).
			WithSubmitterPodTemplate(corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "rayjob-submitter",
							Image: kuberayTestImage,
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU: resource.MustParse("400m"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU: resource.MustParse("400m"),
								},
							},
						},
					},
					RestartPolicy: corev1.RestartPolicyOnFailure,
				},
			}).
			Image(rayv1.HeadNode, kuberayTestImage).
			Image(rayv1.WorkerNode, kuberayTestImage).
			Volumes(rayv1.HeadNode, volumes).
			VolumeMounts(rayv1.HeadNode, volumeMounts).
			TerminationGracePeriod(1).
			Obj()

		ginkgo.By("Creating the ConfigMap", func() {
			gomega.Expect(k8sClient.Create(ctx, configMap)).Should(gomega.Succeed())
		})

		ginkgo.By("Creating the rayJob", func() {
			gomega.Expect(k8sClient.Create(ctx, rayJob)).Should(gomega.Succeed())
		})

		ginkgo.By("Checking one workload is created", func() {
			workloadList := &kueue.WorkloadList{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.List(ctx, workloadList, client.InNamespace(ns.Name))).To(gomega.Succeed())
				g.Expect(workloadList.Items).NotTo(gomega.BeEmpty(), "Expected at least one workload in namespace")
				hasAdmittedWorkload := false
				for _, wl := range workloadList.Items {
					if workload.IsAdmitted(&wl) || workloadfinish.IsFinished(&wl) {
						hasAdmittedWorkload = true
						break
					}
				}
				g.Expect(hasAdmittedWorkload).To(gomega.BeTrue(), "Expected admitted workload")
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed(), util.AssertMsgObjList("No admitted workload found in namespace", workloadList))
		})

		ginkgo.By("Waiting for the RayJob cluster become ready", func() {
			createdRayJob := &rayv1.RayJob{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rayJob), createdRayJob)).To(gomega.Succeed())
				g.Expect(createdRayJob.Spec.Suspend).To(gomega.BeFalse())
				g.Expect(createdRayJob.Status.JobDeploymentStatus).To(gomega.Equal(rayv1.JobDeploymentStatusRunning))
			}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed(), util.AssertMsg("RayJob cluster did not become ready", createdRayJob))
		})

		// The Ray autoscaler can scale workers past minReplicas before the RayJob
		// transitions to JobDeploymentStatusRunning, so the initial pod and worker
		// counts are lower bounds rather than exact values (see issue #11582).
		ginkgo.By("Waiting for at least 3 pods in rayjob namespace", func() {
			// at least head + worker + submitter job
			podList := &corev1.PodList{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.List(ctx, podList, client.InNamespace(ns.Name))).To(gomega.Succeed())
				g.Expect(len(podList.Items)).To(gomega.BeNumerically(">=", 3), "Expected at least 3 pods in rayjob namespace")
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Waiting for at least 1 worker pod", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				workerPodNames := getRunningRayWorkerPodNames(g)
				g.Expect(workerPodNames).ToNot(gomega.BeEmpty(), "Expected at least 1 pod with 'workers' in the name")
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed(), "Expected at least 1 worker pod")
		})

		ginkgo.By("Waiting for exactly 1 non-finished admitted workload", func() {
			workloadList := &kueue.WorkloadList{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.List(ctx, workloadList, client.InNamespace(ns.Name))).To(gomega.Succeed())
				activeWorkloads := util.FindNonFinishedWorkloads(workloadList.Items)
				g.Expect(activeWorkloads).To(gomega.HaveLen(1), "Expected exactly 1 non-finished workload")
				g.Expect(workload.IsAdmitted(&activeWorkloads[0])).To(gomega.BeTrue(), "Expected admitted workload")
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Waiting for second scale-up to 5 workers due to high parallelism tasks", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				currentPodNames := getRunningRayWorkerPodNames(g)
				g.Expect(currentPodNames).To(gomega.HaveLen(5), "Expected exactly 5 pods with 'workers' in the name after second scale-up")
			}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed(), "Did not scale up to 5 worker pods")
		})

		ginkgo.By("Checking podset-replica-sizes annotation is set on the RayJob after second scale-up", func() {
			createdRayJob := &rayv1.RayJob{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rayJob), createdRayJob)).To(gomega.Succeed())
				g.Expect(createdRayJob.Annotations).To(gomega.HaveKey(workloadraycluster.RayClusterPodsetReplicaSizesAnnotation),
					"Expected podset-replica-sizes annotation on RayJob after second scale-up")
				count, err := parsePodSetReplicaCount(createdRayJob.Annotations[workloadraycluster.RayClusterPodsetReplicaSizesAnnotation], "workers-group-0")
				g.Expect(err).NotTo(gomega.HaveOccurred())
				g.Expect(count).To(gomega.Equal(int32(5)),
					"Expected workers-group-0 count = 5 after second scale-up")
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed(), util.AssertMsg("podset-replica-sizes annotation not updated after second scale-up", createdRayJob))
		})

		ginkgo.By("Waiting for at least 3 total workloads due to multiple scale-ups", func() {
			// Use >= 3 since multiple scale-up events should create additional workload slices.
			workloadList := &kueue.WorkloadList{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.List(ctx, workloadList, client.InNamespace(ns.Name))).To(gomega.Succeed())
				g.Expect(len(workloadList.Items)).To(gomega.BeNumerically(">=", 3), "Expected at least 3 workloads due to multiple scale-ups")
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed(), util.AssertMsgObjList("Did not observe >=3 workloads from multiple scale-ups", workloadList))
		})

		ginkgo.By("Checking podset-replica-sizes annotation updated after scaling down", func() {
			createdRayJob := &rayv1.RayJob{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rayJob), createdRayJob)).To(gomega.Succeed())
				g.Expect(createdRayJob.Annotations).To(gomega.HaveKey(workloadraycluster.RayClusterPodsetReplicaSizesAnnotation),
					"Expected podset-replica-sizes annotation on RayJob after scaling down")
				count, err := parsePodSetReplicaCount(createdRayJob.Annotations[workloadraycluster.RayClusterPodsetReplicaSizesAnnotation], "workers-group-0")
				g.Expect(err).NotTo(gomega.HaveOccurred())
				g.Expect(count).To(gomega.Equal(int32(1)),
					"Expected workers-group-0 count = 1 after scaling down")
			}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed(), util.AssertMsg("podset-replica-sizes annotation not updated after scaling down", createdRayJob))
		})

		ginkgo.By("Waiting for the RayJob to finish", func() {
			createdRayJob := &rayv1.RayJob{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rayJob), createdRayJob)).To(gomega.Succeed())
				g.Expect(createdRayJob.Status.JobDeploymentStatus).To(gomega.Equal(rayv1.JobDeploymentStatusComplete))
				g.Expect(createdRayJob.Status.JobStatus).To(gomega.Equal(rayv1.JobStatusSucceeded))
			}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed(), util.AssertMsg("RayJob did not finish successfully", createdRayJob))
		})
	})

	ginkgo.It("Should account for the SidecarMode submitter in the head PodSet quota", ginkgo.Label("shard:kuberay-b"), func() {
		kuberayTestImage := util.GetKuberayTestImage()

		rayJob := testingrayjob.MakeJob("rayjob-sidecar", ns.Name).
			Queue(localQueueName).
			WithSubmissionMode(rayv1.SidecarMode).
			Entrypoint("python -c \"import ray; ray.init(); print(ray.cluster_resources())\"").
			RequestAndLimit(rayv1.HeadNode, corev1.ResourceCPU, "1").
			RayStartParam(rayv1.HeadNode, "object-store-memory", objectStoreMemory).
			RequestAndLimit(rayv1.WorkerNode, corev1.ResourceCPU, "400m").
			RayStartParam(rayv1.WorkerNode, "object-store-memory", objectStoreMemory).
			TerminationGracePeriod(1).
			Image(rayv1.HeadNode, kuberayTestImage).
			Image(rayv1.WorkerNode, kuberayTestImage).Obj()

		ginkgo.By("Creating the rayJob", func() {
			gomega.Expect(k8sClient.Create(ctx, rayJob)).Should(gomega.Succeed())
		})

		// In SidecarMode KubeRay injects the job submitter as a container in the
		// head Pod, so Kueue must account for it on the head PodSet.
		createdWorkload := &kueue.Workload{}
		var kueueSubmitterRequests corev1.ResourceList
		ginkgo.By("Checking the workload is admitted and its head PodSet includes the submitter", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				workloadList := &kueue.WorkloadList{}
				g.Expect(k8sClient.List(ctx, workloadList, client.InNamespace(ns.Name))).To(gomega.Succeed())
				active := util.FindNonFinishedWorkloads(workloadList.Items)
				g.Expect(active).To(gomega.HaveLen(1), "expected exactly one non-finished workload")
				g.Expect(workload.IsAdmitted(&active[0])).To(gomega.BeTrue(), "workload should be admitted")
				*createdWorkload = active[0]
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

			headPodSet := utilpodset.FindPodSetByName(createdWorkload.Spec.PodSets, kueue.NewPodSetReference("head"))
			gomega.Expect(headPodSet).NotTo(gomega.BeNil(), "workload should have a head PodSet")

			submitter := findContainer(headPodSet.Template.Spec.Containers, "ray-job-submitter")
			gomega.Expect(submitter).NotTo(gomega.BeNil(), "head PodSet should include the submitter sidecar container")
			kueueSubmitterRequests = submitter.Resources.Requests
		})

		// Drift check: the resources Kueue accounted for must match the submitter
		// container KubeRay actually injects into the head Pod.
		ginkgo.By("Checking the accounted submitter resources match the real head Pod", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				headPod := getRayHeadPod(g)
				submitter := findContainer(headPod.Spec.Containers, "ray-job-submitter")
				g.Expect(submitter).NotTo(gomega.BeNil(), "KubeRay should inject the submitter container into the head Pod")
				g.Expect(kueueSubmitterRequests.Cpu().Cmp(*submitter.Resources.Requests.Cpu())).To(gomega.Equal(0),
					"Kueue's accounted submitter CPU should match the head Pod's")
				g.Expect(kueueSubmitterRequests.Memory().Cmp(*submitter.Resources.Requests.Memory())).To(gomega.Equal(0),
					"Kueue's accounted submitter memory should match the head Pod's")
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should run a RayCluster on worker if admitted", ginkgo.Label("shard:kuberay-a"), func() {
		kuberayTestImage := util.GetKuberayTestImage()

		raycluster := testingraycluster.MakeCluster("raycluster1", ns.Name).
			Suspend(true).
			Queue(localQueueName).
			RequestAndLimit(rayv1.HeadNode, corev1.ResourceCPU, "1").
			RayStartParam(rayv1.HeadNode, "object-store-memory", objectStoreMemory).
			RequestAndLimit(rayv1.WorkerNode, corev1.ResourceCPU, "400m").
			RayStartParam(rayv1.WorkerNode, "object-store-memory", objectStoreMemory).
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
			createdRayCluster := &rayv1.RayCluster{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(raycluster), createdRayCluster)).To(gomega.Succeed())
				g.Expect(createdRayCluster.Status.DesiredWorkerReplicas).To(gomega.Equal(int32(1)))
				g.Expect(createdRayCluster.Status.ReadyWorkerReplicas).To(gomega.Equal(int32(1)))
				g.Expect(createdRayCluster.Status.AvailableWorkerReplicas).To(gomega.Equal(int32(1)))
			}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed(), util.AssertMsg("RayCluster did not become ready", createdRayCluster))
		})
	})

	ginkgo.It("Should account for the autoscaler sidecar in the head PodSet quota when in-tree autoscaling is enabled", ginkgo.Label("shard:kuberay-b"), func() {
		kuberayTestImage := util.GetKuberayTestImage()

		raycluster := testingraycluster.MakeCluster("raycluster-autoscaling", ns.Name).
			Suspend(true).
			Queue(localQueueName).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			WithEnableAutoscaling(new(true)).
			RequestAndLimit(rayv1.HeadNode, corev1.ResourceCPU, "1").
			RayStartParam(rayv1.HeadNode, "object-store-memory", objectStoreMemory).
			RequestAndLimit(rayv1.WorkerNode, corev1.ResourceCPU, "400m").
			RayStartParam(rayv1.WorkerNode, "object-store-memory", objectStoreMemory).
			Image(rayv1.HeadNode, kuberayTestImage, []string{}).
			Image(rayv1.WorkerNode, kuberayTestImage, []string{}).
			Obj()

		ginkgo.By("Creating the RayCluster", func() {
			gomega.Expect(k8sClient.Create(ctx, raycluster)).Should(gomega.Succeed())
		})

		// Elastic (workload-slice) jobs name their Workload with a generation
		// suffix, so look it up by listing the namespace rather than computing
		// the name.
		createdWorkload := &kueue.Workload{}
		ginkgo.By("Checking the workload is created and admitted", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				workloadList := &kueue.WorkloadList{}
				g.Expect(k8sClient.List(ctx, workloadList, client.InNamespace(ns.Name))).To(gomega.Succeed())
				active := util.FindNonFinishedWorkloads(workloadList.Items)
				g.Expect(active).To(gomega.HaveLen(1), "expected exactly one non-finished workload")
				g.Expect(workload.IsAdmitted(&active[0])).To(gomega.BeTrue(), "workload should be admitted")
				*createdWorkload = active[0]
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		// Kueue's view: the head PodSet must include the autoscaler sidecar that
		// KubeRay injects, otherwise its resources are not counted against quota.
		var kueueAutoscalerRequests corev1.ResourceList
		ginkgo.By("Checking the head PodSet includes the autoscaler sidecar container", func() {
			var headPodSet *kueue.PodSet
			for i := range createdWorkload.Spec.PodSets {
				if createdWorkload.Spec.PodSets[i].Name == "head" {
					headPodSet = &createdWorkload.Spec.PodSets[i]
					break
				}
			}
			gomega.Expect(headPodSet).NotTo(gomega.BeNil(), "workload should have a head PodSet")

			var autoscaler *corev1.Container
			for i := range headPodSet.Template.Spec.Containers {
				if headPodSet.Template.Spec.Containers[i].Name == "autoscaler" {
					autoscaler = &headPodSet.Template.Spec.Containers[i]
					break
				}
			}
			gomega.Expect(autoscaler).NotTo(gomega.BeNil(), "head PodSet should include the autoscaler sidecar container")
			kueueAutoscalerRequests = autoscaler.Resources.Requests
		})

		// Drift check: the resources Kueue accounted for must match the autoscaler
		// container that KubeRay actually injects into the head Pod.
		ginkgo.By("Checking the accounted autoscaler resources match the real head Pod", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				headPod := getRayHeadPod(g)
				var autoscaler *corev1.Container
				for i := range headPod.Spec.Containers {
					if headPod.Spec.Containers[i].Name == "autoscaler" {
						autoscaler = &headPod.Spec.Containers[i]
						break
					}
				}
				g.Expect(autoscaler).NotTo(gomega.BeNil(), "KubeRay should inject an autoscaler container into the head Pod")
				g.Expect(kueueAutoscalerRequests.Cpu().Cmp(*autoscaler.Resources.Requests.Cpu())).To(gomega.Equal(0),
					"Kueue's accounted autoscaler CPU should match the head Pod's")
				g.Expect(kueueAutoscalerRequests.Memory().Cmp(*autoscaler.Resources.Requests.Memory())).To(gomega.Equal(0),
					"Kueue's accounted autoscaler memory should match the head Pod's")
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should run a RayService if admitted", ginkgo.Label("shard:kuberay-a", "requires:fullray"), func() {
		kuberayTestImage := util.GetKuberayTestImage()

		// Create ConfigMap with a simple Ray Serve application
		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "rayservice-hello",
				Namespace: ns.Name,
			},
			Data: map[string]string{
				"hello_serve.py": `from ray import serve

@serve.deployment
class HelloWorld:
    def __call__(self, request):
        return "Hello, World!"

app = HelloWorld.bind()`,
			},
		}

		// serveConfigV2 configuration for the simple serve app
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
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: "rayservice-hello",
						},
						Items: []corev1.KeyToPath{
							{
								Key:  "hello_serve.py",
								Path: "hello_serve.py",
							},
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

		rayService := testingrayservice.MakeService("rayservice-hello", ns.Name).
			Suspend(true).
			Queue(localQueueName).
			RequestAndLimit(rayv1.HeadNode, corev1.ResourceCPU, "1").
			RequestAndLimit(rayv1.WorkerNode, corev1.ResourceCPU, "600m").
			Image(rayv1.HeadNode, kuberayTestImage).
			Image(rayv1.WorkerNode, kuberayTestImage).
			RayStartParam(rayv1.HeadNode, "object-store-memory", objectStoreMemory).
			WithServeConfigV2(serveConfigV2).
			Env(rayv1.HeadNode, env).
			Env(rayv1.WorkerNode, env).
			Volumes(rayv1.HeadNode, volumes).
			Volumes(rayv1.WorkerNode, volumes).
			VolumeMounts(rayv1.HeadNode, volumeMounts).
			VolumeMounts(rayv1.WorkerNode, volumeMounts).
			TerminationGracePeriod(1).
			Obj()

		ginkgo.By("Creating the ConfigMap", func() {
			gomega.Expect(k8sClient.Create(ctx, configMap)).Should(gomega.Succeed())
		})

		ginkgo.By("Creating the RayService", func() {
			gomega.Expect(k8sClient.Create(ctx, rayService)).Should(gomega.Succeed())
		})

		wlLookupKey := types.NamespacedName{Name: workloadrayservice.GetWorkloadNameForRayService(rayService.Name, rayService.UID), Namespace: ns.Name}
		createdWorkload := &kueue.Workload{}
		ginkgo.By("Checking workload is created", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking workload is admitted", func() {
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, createdWorkload)
		})

		ginkgo.By("Checking the RayService is running", func() {
			createdRayService := &rayv1.RayService{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rayService), createdRayService)).To(gomega.Succeed())
				g.Expect(createdRayService.Spec.RayClusterSpec.Suspend).To(gomega.Equal(new(false)))
				g.Expect(apimeta.IsStatusConditionTrue(createdRayService.Status.Conditions, string(rayv1.RayServiceReady))).To(gomega.BeTrue())
			}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed(), util.AssertMsg("RayService did not become ready", createdRayService))
		})

		curlPod := createCurlPod("curl-rayservice")
		ginkgo.By("Verifying the RayService responds through the Serve service", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				body, stderr, err := requestRayService(curlPod, rayService, "/")
				g.Expect(err).NotTo(gomega.HaveOccurred(), "stderr: %s", string(stderr))
				g.Expect(strings.TrimSpace(string(body))).To(gomega.ContainSubstring("Hello, World!"))
			}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should run the Redis cleanup Job when a GCS fault-tolerant RayService is deleted", ginkgo.Label("shard:kuberay-a", "requires:fullray"), func() {
		kuberayTestImage := util.GetKuberayTestImage()

		// Deploy an in-cluster Redis backing the RayCluster's GCS fault tolerance.
		redisLabels := map[string]string{"app": "redis-gcs-ft"}
		redisDeployment := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "redis", Namespace: ns.Name},
			Spec: appsv1.DeploymentSpec{
				Selector: &metav1.LabelSelector{MatchLabels: redisLabels},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: redisLabels},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{
							Name:            "redis",
							Image:           util.GetRedisTestImage(),
							ImagePullPolicy: corev1.PullIfNotPresent,
							Ports:           []corev1.ContainerPort{{ContainerPort: 6379}},
						}},
					},
				},
			},
		}
		redisService := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "redis", Namespace: ns.Name},
			Spec: corev1.ServiceSpec{
				Selector: redisLabels,
				Ports:    []corev1.ServicePort{{Port: 6379}},
			},
		}
		ginkgo.By("Deploying Redis", func() {
			gomega.Expect(k8sClient.Create(ctx, redisDeployment)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, redisService)).To(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(redisDeployment), redisDeployment)).To(gomega.Succeed())
				g.Expect(redisDeployment.Status.ReadyReplicas).To(gomega.Equal(int32(1)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		rayService := testingrayservice.MakeService("rayservice-gcs-ft", ns.Name).
			Suspend(true).
			Queue(localQueueName).
			GCSFaultTolerance("redis:6379").
			RequestAndLimit(rayv1.HeadNode, corev1.ResourceCPU, "1").
			RequestAndLimit(rayv1.WorkerNode, corev1.ResourceCPU, "600m").
			Image(rayv1.HeadNode, kuberayTestImage).
			Image(rayv1.WorkerNode, kuberayTestImage).
			RayStartParam(rayv1.HeadNode, "object-store-memory", objectStoreMemory).
			TerminationGracePeriod(1).
			Obj()

		ginkgo.By("Creating the RayService", func() {
			gomega.Expect(k8sClient.Create(ctx, rayService)).To(gomega.Succeed())
		})

		wlLookupKey := types.NamespacedName{Name: workloadrayservice.GetWorkloadNameForRayService(rayService.Name, rayService.UID), Namespace: ns.Name}
		createdWorkload := &kueue.Workload{}
		ginkgo.By("Checking the workload is created and admitted", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, createdWorkload)
		})

		ginkgo.By("Waiting for the RayCluster to be provisioned with the GCS cleanup finalizer", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				clusters := &rayv1.RayClusterList{}
				g.Expect(k8sClient.List(ctx, clusters, client.InNamespace(ns.Name))).To(gomega.Succeed())
				g.Expect(clusters.Items).To(gomega.HaveLen(1))
				g.Expect(clusters.Items[0].Finalizers).To(gomega.ContainElement("ray.io/gcs-ft-redis-cleanup-finalizer"))
			}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
		})

		// redisDBSize returns the number of keys in the Redis backing store by running
		// redis-cli in the Redis pod.
		redisDBSize := func(g gomega.Gomega) int {
			pods := &corev1.PodList{}
			g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name), client.MatchingLabels{"app": "redis-gcs-ft"})).To(gomega.Succeed())
			g.Expect(pods.Items).To(gomega.HaveLen(1))
			out, _, err := util.KExecute(ctx, cfg, restClient, ns.Name, pods.Items[0].Name, "redis", []string{"redis-cli", "DBSIZE"})
			g.Expect(err).NotTo(gomega.HaveOccurred())
			size, err := strconv.Atoi(strings.TrimSpace(string(out)))
			g.Expect(err).NotTo(gomega.HaveOccurred())
			return size
		}

		ginkgo.By("Checking GCS fault tolerance populated Redis", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(redisDBSize(g)).To(gomega.BeNumerically(">", 0))
			}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Deleting the RayService", func() {
			gomega.Expect(k8sClient.Delete(ctx, rayService)).To(gomega.Succeed())
		})

		ginkgo.By("Verifying that the RayService is actually deleted", func() {
			util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, rayService, false, util.LongTimeout)
		})

		// Deferred finalization keeps the parent Workload admitted while KubeRay's Redis
		// cleanup Job runs, so the Job purges the RayCluster's GCS metadata namespace.
		// Without it the cleanup Job is gated forever and these keys leak until KubeRay's
		// ~300s safety timeout force-removes the finalizer.
		ginkgo.By("Checking Redis is cleaned up after the RayService is deleted", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(redisDBSize(g)).To(gomega.Equal(0))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should run a rayservice with InTreeAutoscaling", ginkgo.Label("shard:kuberay-b", "requires:fullray"), func() {
		kuberayTestImage := util.GetKuberayTestImage()

		// Create ConfigMap with a Ray Serve application that supports a delay parameter
		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "rayservice-autoscale",
				Namespace: ns.Name,
			},
			Data: map[string]string{
				"hello_serve.py": `import asyncio
from ray import serve

@serve.deployment
class HelloWorld:
    async def __call__(self, request):
        delay = request.query_params.get("delay", "0")
        if float(delay) > 0:
            await asyncio.sleep(float(delay))
        return "Hello, World!"

app = HelloWorld.bind()`,
			},
		}

		// serveConfigV2 configuration for the simple serve app
		serveConfigV2 := `applications:
  - name: hello_app
    import_path: hello_serve:app
    route_prefix: /
    deployments:
      - name: HelloWorld
        autoscaling_config:
          min_replicas: 0
          max_replicas: 5
          target_ongoing_requests: 1
          upscaling_factor: 1.0
          upscale_delay_s: 2
          downscale_delay_s: 2
        max_replicas_per_node: 1
        ray_actor_options:
          num_cpus: 0.2`

		volumes := []corev1.Volume{
			{
				Name: "code-sample",
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: "rayservice-autoscale",
						},
						Items: []corev1.KeyToPath{
							{
								Key:  "hello_serve.py",
								Path: "hello_serve.py",
							},
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

		rayService := testingrayservice.MakeService("rayservice-autoscale", ns.Name).
			Suspend(true).
			Queue(localQueueName).
			RequestAndLimit(rayv1.HeadNode, corev1.ResourceCPU, "1").
			RequestAndLimit(rayv1.WorkerNode, corev1.ResourceCPU, "400m").
			Image(rayv1.HeadNode, kuberayTestImage).
			Image(rayv1.WorkerNode, kuberayTestImage).
			RayStartParam(rayv1.HeadNode, "object-store-memory", objectStoreMemory).
			WithServeConfigV2(serveConfigV2).
			Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			EnableInTreeAutoscaling().
			Env(rayv1.HeadNode, env).
			Env(rayv1.WorkerNode, env).
			Volumes(rayv1.HeadNode, volumes).
			Volumes(rayv1.WorkerNode, volumes).
			VolumeMounts(rayv1.HeadNode, volumeMounts).
			VolumeMounts(rayv1.WorkerNode, volumeMounts).
			TerminationGracePeriod(1).
			Obj()

		ginkgo.By("Creating the ConfigMap", func() {
			gomega.Expect(k8sClient.Create(ctx, configMap)).Should(gomega.Succeed())
		})

		ginkgo.By("Creating the RayService", func() {
			gomega.Expect(k8sClient.Create(ctx, rayService)).Should(gomega.Succeed())
		})

		ginkgo.By("Checking one non-finished workload is created and admitted", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				workloadList := &kueue.WorkloadList{}
				g.Expect(k8sClient.List(ctx, workloadList, client.InNamespace(ns.Name))).To(gomega.Succeed())
				var activeWorkloads []kueue.Workload
				for i := range workloadList.Items {
					if !workloadfinish.IsFinished(&workloadList.Items[i]) {
						activeWorkloads = append(activeWorkloads, workloadList.Items[i])
					}
				}
				g.Expect(activeWorkloads).To(gomega.HaveLen(1), "Expected exactly 1 non-finished workload")
				g.Expect(workload.IsAdmitted(&activeWorkloads[0])).To(gomega.BeTrue(), "Expected admitted workload")
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking the RayService is running", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdRayService := &rayv1.RayService{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rayService), createdRayService)).To(gomega.Succeed())
				g.Expect(createdRayService.Spec.RayClusterSpec.Suspend).To(gomega.Equal(new(false)))
				g.Expect(apimeta.IsStatusConditionTrue(createdRayService.Status.Conditions, string(rayv1.RayServiceReady))).To(gomega.BeTrue())
			}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
		})

		curlPod := createCurlPod("curl-rayservice-autoscale")
		ginkgo.By("Verifying the RayService responds through the Serve service", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				body, stderr, err := requestRayService(curlPod, rayService, "/")
				g.Expect(err).NotTo(gomega.HaveOccurred(), "stderr: %s", string(stderr))
				g.Expect(strings.TrimSpace(string(body))).To(gomega.ContainSubstring("Hello, World!"))
			}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Waiting for worker count to be zero due to autoscaling", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				runningWorkers := getRunningRayWorkerPodNames(g)
				g.Expect(runningWorkers).To(gomega.BeEmpty(), "Expected 0 running worker pod before sending load")
			}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Sending concurrent requests to trigger autoscaling", func() {
			// Send many concurrent slow requests so ongoing request count exceeds
			// the target_ongoing_requests=1 threshold, triggering Ray Serve autoscaling.
			// With max_replicas_per_node=1, new serve replicas require new Ray worker
			// nodes, which triggers the RayCluster autoscaler to add workers.
			for range 10 {
				go func() {
					_, _, _ = requestRayService(curlPod, rayService, "/?delay=10")
				}()
			}
		})

		ginkgo.By("Waiting for worker count to increase due to autoscaling", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				runningWorkers := getRunningRayWorkerPodNames(g)
				g.Expect(len(runningWorkers)).To(gomega.BeNumerically(">", 1),
					fmt.Sprintf("Expected more than %d running worker pods after autoscaling", 1))
			}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
