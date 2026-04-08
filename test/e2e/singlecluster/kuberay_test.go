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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	workloadraycluster "sigs.k8s.io/kueue/pkg/controller/jobs/raycluster"
	workloadrayjob "sigs.k8s.io/kueue/pkg/controller/jobs/rayjob"
	workloadrayservice "sigs.k8s.io/kueue/pkg/controller/jobs/rayservice"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingraycluster "sigs.k8s.io/kueue/pkg/util/testingjobs/raycluster"
	testingrayjob "sigs.k8s.io/kueue/pkg/util/testingjobs/rayjob"
	testingrayservice "sigs.k8s.io/kueue/pkg/util/testingjobs/rayservice"
	"sigs.k8s.io/kueue/pkg/workload"
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

	// getRunningWorkerPodNames returns the names of running pods that have "workers" in their name
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

		ginkgo.By("Verify ray job worker pods have queue labels assigned", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				var pods corev1.PodList
				g.Expect(k8sClient.List(ctx, &pods, client.InNamespace(ns.Name))).To(gomega.Succeed())
				g.Expect(pods.Items).ToNot(gomega.BeEmpty())
				for _, pod := range pods.Items {
					g.Expect(pod.Labels[constants.ClusterQueueLabel]).To(gomega.Equal(clusterQueueName))
					g.Expect(pod.Labels[constants.LocalQueueLabel]).To(gomega.Equal(localQueueName))
				}
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
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

	ginkgo.It("Should run a rayjob with InTreeAutoscaling", func() {
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

# run tasks in parallel to trigger autoscaling (scaling up)
# Use longer sleep (8s) to give autoscaler time to detect demand,
# create workload slices, and schedule new workers.
print(ray.get([my_task.remote(i, 8) for i in range(16)]))

# run tasks in sequence to trigger scaling down
print([ray.get(my_task.remote(i, 1)) for i in range(32)])`,
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
			WithSubmissionMode(rayv1.K8sJobMode).
			Entrypoint("python /home/ray/samples/sample_code.py").
			RequestAndLimit(rayv1.HeadNode, corev1.ResourceCPU, "200m").
			RequestAndLimit(rayv1.WorkerNode, corev1.ResourceCPU, "200m").
			WithSubmitterPodTemplate(corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "rayjob-submitter",
							Image: kuberayTestImage,
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU: resource.MustParse("200m"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU: resource.MustParse("200m"),
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
			TerminationGracePeriodSeconds(int64(5)).
			Obj()

		ginkgo.By("Creating the ConfigMap", func() {
			gomega.Expect(k8sClient.Create(ctx, configMap)).Should(gomega.Succeed())
		})

		ginkgo.By("Creating the rayJob", func() {
			gomega.Expect(k8sClient.Create(ctx, rayJob)).Should(gomega.Succeed())
		})

		// Variable to store initial pod names for verification during scaling
		var initialPodNames []string
		// Variable to store scaled-up pod names for verification during scaling down
		var scaledUpPodNames []string

		ginkgo.By("Checking one workload is created", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				workloadList := &kueue.WorkloadList{}
				g.Expect(k8sClient.List(ctx, workloadList, client.InNamespace(ns.Name))).To(gomega.Succeed())
				g.Expect(workloadList.Items).NotTo(gomega.BeEmpty(), "Expected at least one workload in namespace")
				hasAdmittedWorkload := false
				for _, wl := range workloadList.Items {
					if workload.IsAdmitted(&wl) || workload.IsFinished(&wl) {
						hasAdmittedWorkload = true
						break
					}
				}
				g.Expect(hasAdmittedWorkload).To(gomega.BeTrue(), "Expected admitted workload")
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Waiting for the RayJob cluster become ready", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdRayJob := &rayv1.RayJob{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rayJob), createdRayJob)).To(gomega.Succeed())
				g.Expect(createdRayJob.Spec.Suspend).To(gomega.BeFalse())
				g.Expect(createdRayJob.Status.JobDeploymentStatus).To(gomega.Equal(rayv1.JobDeploymentStatusRunning))
			}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Waiting for 3 pods in rayjob namespace", func() {
			// 3 rayjob pods: head, worker, submitter job
			gomega.Eventually(func(g gomega.Gomega) {
				podList := &corev1.PodList{}
				g.Expect(k8sClient.List(ctx, podList, client.InNamespace(ns.Name))).To(gomega.Succeed())
				g.Expect(podList.Items).To(gomega.HaveLen(3), "Expected exactly 3 pods in rayjob namespace")
				// Get worker pod names and check count
				workerPodNames := getRunningWorkerPodNames(podList)
				g.Expect(workerPodNames).To(gomega.HaveLen(1), "Expected exactly 1 pod with 'workers' in the name")

				// Store initial pod names for later verification
				initialPodNames = workerPodNames
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})

		// RayJob is top level job, the submitter job created by RayJob will not create its own workload, there will be only 1 workload
		ginkgo.By("Waiting for 1 workloads", func() {
			// 1 workload for the ray cluster
			gomega.Eventually(func(g gomega.Gomega) {
				workloadList := &kueue.WorkloadList{}
				g.Expect(k8sClient.List(ctx, workloadList, client.InNamespace(ns.Name))).To(gomega.Succeed())
				g.Expect(workloadList.Items).To(gomega.HaveLen(1), "Expected exactly 1 workload")
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Waiting for 5 workers due to scaling up", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				podList := &corev1.PodList{}
				g.Expect(k8sClient.List(ctx, podList, client.InNamespace(ns.Name))).To(gomega.Succeed())
				// Get worker pod names and check count
				currentPodNames := getRunningWorkerPodNames(podList)
				g.Expect(currentPodNames).To(gomega.HaveLen(5), "Expected exactly 5 pods with 'workers' in the name")

				// Verify that the current pod names are a superset of the initial pod names
				g.Expect(currentPodNames).To(gomega.ContainElements(initialPodNames),
					"Current worker pod names should be a superset of initial pod names")

				// Store scaled-up pod names for later verification during scaling down
				scaledUpPodNames = currentPodNames
			}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking podset-replica-sizes annotation is set on the RayJob after scaling up", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdRayJob := &rayv1.RayJob{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rayJob), createdRayJob)).To(gomega.Succeed())
				g.Expect(createdRayJob.Annotations).To(gomega.HaveKey(workloadraycluster.RayClusterPodsetReplicaSizesAnnotation),
					"Expected podset-replica-sizes annotation on RayJob after scaling up")
				count, err := parsePodSetReplicaCount(createdRayJob.Annotations[workloadraycluster.RayClusterPodsetReplicaSizesAnnotation], "workers-group-0")
				g.Expect(err).NotTo(gomega.HaveOccurred())
				g.Expect(count).To(gomega.Equal(int32(5)),
					"Expected workers-group-0 count = 5 after scaling up")
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Waiting for at least 2 total workloads due to scaling up creating another workload", func() {
			// Use >= 2 since finished slices from intermediate scaling decisions are retained.
			gomega.Eventually(func(g gomega.Gomega) {
				workloadList := &kueue.WorkloadList{}
				g.Expect(k8sClient.List(ctx, workloadList, client.InNamespace(ns.Name))).To(gomega.Succeed())
				g.Expect(len(workloadList.Items)).To(gomega.BeNumerically(">=", 2), "Expected at least 2 workloads")
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Waiting for all 5 worker pods to be running", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				podList := &corev1.PodList{}
				g.Expect(k8sClient.List(ctx, podList, client.InNamespace(ns.Name))).To(gomega.Succeed())
				runningWorkers := getRunningWorkerPodNames(podList)
				g.Expect(runningWorkers).To(gomega.HaveLen(5), "Expected 5 running worker pods")
			}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
		})

		var deletedPodName string
		ginkgo.By("Deleting one worker pod", func() {
			podList := &corev1.PodList{}
			gomega.Expect(k8sClient.List(ctx, podList, client.InNamespace(ns.Name))).To(gomega.Succeed())
			runningWorkers := getRunningWorkerPodNames(podList)
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
			gomega.Eventually(func(g gomega.Gomega) {
				podList := &corev1.PodList{}
				g.Expect(k8sClient.List(ctx, podList, client.InNamespace(ns.Name))).To(gomega.Succeed())
				runningWorkers := getRunningWorkerPodNames(podList)
				g.Expect(runningWorkers).To(gomega.HaveLen(5), "Expected 5 running worker pods after replacement")
				g.Expect(runningWorkers).NotTo(gomega.ContainElement(deletedPodName),
					"Deleted pod should not be present among running workers")
				// Update scaledUpPodNames to include the replacement pod,
				// so the later scale-down superset check accounts for it.
				scaledUpPodNames = runningWorkers
			}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Waiting for workers reduced to 1 due to scaling down", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				podList := &corev1.PodList{}
				g.Expect(k8sClient.List(ctx, podList, client.InNamespace(ns.Name))).To(gomega.Succeed())
				// Get worker pod names and check count
				currentPodNames := getRunningWorkerPodNames(podList)
				g.Expect(currentPodNames).To(gomega.HaveLen(1), "Expected exactly 1 pods with 'workers' in the name")

				// Verify that the previous scaled-up pod names are a superset of the current pod names
				g.Expect(scaledUpPodNames).To(gomega.ContainElements(currentPodNames),
					"Previous scaled-up worker pod names should be a superset of current pod names")
			}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking podset-replica-sizes annotation updated after scaling down", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdRayJob := &rayv1.RayJob{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rayJob), createdRayJob)).To(gomega.Succeed())
				g.Expect(createdRayJob.Annotations).To(gomega.HaveKey(workloadraycluster.RayClusterPodsetReplicaSizesAnnotation),
					"Expected podset-replica-sizes annotation on RayJob after scaling down")
				count, err := parsePodSetReplicaCount(createdRayJob.Annotations[workloadraycluster.RayClusterPodsetReplicaSizesAnnotation], "workers-group-0")
				g.Expect(err).NotTo(gomega.HaveOccurred())
				g.Expect(count).To(gomega.Equal(int32(1)),
					"Expected workers-group-0 count = 1 after scaling down")
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
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

	ginkgo.It("Should run a rayjob with multi scale-up steps", func() {
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

# run tasks in sequence to trigger scaling down
print([ray.get(my_task.remote(i, 1)) for i in range(32)])`,
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
			RequestAndLimit(rayv1.HeadNode, corev1.ResourceCPU, "200m").
			RequestAndLimit(rayv1.WorkerNode, corev1.ResourceCPU, "200m").
			WithSubmitterPodTemplate(corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "rayjob-submitter",
							Image: kuberayTestImage,
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU: resource.MustParse("200m"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU: resource.MustParse("200m"),
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
			TerminationGracePeriodSeconds(int64(5)).
			Obj()

		ginkgo.By("Creating the ConfigMap", func() {
			gomega.Expect(k8sClient.Create(ctx, configMap)).Should(gomega.Succeed())
		})

		ginkgo.By("Creating the rayJob", func() {
			gomega.Expect(k8sClient.Create(ctx, rayJob)).Should(gomega.Succeed())
		})

		ginkgo.By("Checking one workload is created", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				workloadList := &kueue.WorkloadList{}
				g.Expect(k8sClient.List(ctx, workloadList, client.InNamespace(ns.Name))).To(gomega.Succeed())
				g.Expect(workloadList.Items).NotTo(gomega.BeEmpty(), "Expected at least one workload in namespace")
				hasAdmittedWorkload := false
				for _, wl := range workloadList.Items {
					if workload.IsAdmitted(&wl) || workload.IsFinished(&wl) {
						hasAdmittedWorkload = true
						break
					}
				}
				g.Expect(hasAdmittedWorkload).To(gomega.BeTrue(), "Expected admitted workload")
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Waiting for the RayJob cluster become ready", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdRayJob := &rayv1.RayJob{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rayJob), createdRayJob)).To(gomega.Succeed())
				g.Expect(createdRayJob.Spec.Suspend).To(gomega.BeFalse())
				g.Expect(createdRayJob.Status.JobDeploymentStatus).To(gomega.Equal(rayv1.JobDeploymentStatusRunning))
			}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Waiting for 3 pods in rayjob namespace", func() {
			// 3 rayjob pods: head, worker, submitter job
			gomega.Eventually(func(g gomega.Gomega) {
				podList := &corev1.PodList{}
				g.Expect(k8sClient.List(ctx, podList, client.InNamespace(ns.Name))).To(gomega.Succeed())
				g.Expect(podList.Items).To(gomega.HaveLen(3), "Expected exactly 3 pods in rayjob namespace")
				// Get worker pod names and check count
				workerPodNames := getRunningWorkerPodNames(podList)
				g.Expect(workerPodNames).To(gomega.HaveLen(1), "Expected exactly 1 pod with 'workers' in the name")
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})

		// RayJob is top level job, the submitter job created by RayJob will not create its own workload, there will be only 1 workload
		ginkgo.By("Waiting for 1 workloads", func() {
			// 1 workload for the ray cluster
			gomega.Eventually(func(g gomega.Gomega) {
				workloadList := &kueue.WorkloadList{}
				g.Expect(k8sClient.List(ctx, workloadList, client.InNamespace(ns.Name))).To(gomega.Succeed())
				g.Expect(workloadList.Items).To(gomega.HaveLen(1), "Expected exactly 1 workload")
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Waiting for second scale-up to 5 workers due to high parallelism tasks", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				podList := &corev1.PodList{}
				g.Expect(k8sClient.List(ctx, podList, client.InNamespace(ns.Name))).To(gomega.Succeed())
				// Get worker pod names and check count
				currentPodNames := getRunningWorkerPodNames(podList)
				g.Expect(currentPodNames).To(gomega.HaveLen(5), "Expected exactly 5 pods with 'workers' in the name after second scale-up")
			}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking podset-replica-sizes annotation is set on the RayJob after second scale-up", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdRayJob := &rayv1.RayJob{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rayJob), createdRayJob)).To(gomega.Succeed())
				g.Expect(createdRayJob.Annotations).To(gomega.HaveKey(workloadraycluster.RayClusterPodsetReplicaSizesAnnotation),
					"Expected podset-replica-sizes annotation on RayJob after second scale-up")
				count, err := parsePodSetReplicaCount(createdRayJob.Annotations[workloadraycluster.RayClusterPodsetReplicaSizesAnnotation], "workers-group-0")
				g.Expect(err).NotTo(gomega.HaveOccurred())
				g.Expect(count).To(gomega.Equal(int32(5)),
					"Expected workers-group-0 count = 5 after second scale-up")
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Waiting for at least 3 total workloads due to multiple scale-ups", func() {
			// Use >= 3 since multiple scale-up events should create additional workload slices.
			gomega.Eventually(func(g gomega.Gomega) {
				workloadList := &kueue.WorkloadList{}
				g.Expect(k8sClient.List(ctx, workloadList, client.InNamespace(ns.Name))).To(gomega.Succeed())
				g.Expect(len(workloadList.Items)).To(gomega.BeNumerically(">=", 3), "Expected at least 3 workloads due to multiple scale-ups")
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Waiting for workers reduced to 1 due to scaling down", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				podList := &corev1.PodList{}
				g.Expect(k8sClient.List(ctx, podList, client.InNamespace(ns.Name))).To(gomega.Succeed())
				// Get worker pod names and check count
				currentPodNames := getRunningWorkerPodNames(podList)
				g.Expect(currentPodNames).To(gomega.HaveLen(1), "Expected exactly 1 pods with 'workers' in the name")
			}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking podset-replica-sizes annotation updated after scaling down", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdRayJob := &rayv1.RayJob{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rayJob), createdRayJob)).To(gomega.Succeed())
				g.Expect(createdRayJob.Annotations).To(gomega.HaveKey(workloadraycluster.RayClusterPodsetReplicaSizesAnnotation),
					"Expected podset-replica-sizes annotation on RayJob after scaling down")
				count, err := parsePodSetReplicaCount(createdRayJob.Annotations[workloadraycluster.RayClusterPodsetReplicaSizesAnnotation], "workers-group-0")
				g.Expect(err).NotTo(gomega.HaveOccurred())
				g.Expect(count).To(gomega.Equal(int32(1)),
					"Expected workers-group-0 count = 1 after scaling down")
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
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

	ginkgo.It("Should run a RayService if admitted", func() {
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
			RequestAndLimit(rayv1.HeadNode, corev1.ResourceCPU, "600m").
			RequestAndLimit(rayv1.WorkerNode, corev1.ResourceCPU, "600m").
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
			Obj()

		// Configure worker group with minReplicas and maxReplicas
		rayService.Spec.RayClusterSpec.WorkerGroupSpecs[0].GroupName = "small-group"
		rayService.Spec.RayClusterSpec.WorkerGroupSpecs[0].MinReplicas = ptr.To[int32](1)
		rayService.Spec.RayClusterSpec.WorkerGroupSpecs[0].MaxReplicas = ptr.To[int32](2)

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
			gomega.Eventually(func(g gomega.Gomega) {
				createdRayService := &rayv1.RayService{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rayService), createdRayService)).To(gomega.Succeed())
				g.Expect(createdRayService.Spec.RayClusterSpec.Suspend).To(gomega.Equal(ptr.To(false)))
				g.Expect(apimeta.IsStatusConditionTrue(createdRayService.Status.Conditions, string(rayv1.RayServiceReady))).To(gomega.BeTrue())
			}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Verifying the RayService responds to HTTP requests via port-forward", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				// Find the head pod in this namespace
				pods := &corev1.PodList{}
				g.Expect(k8sClient.List(ctx, pods,
					client.InNamespace(ns.Name),
					client.MatchingLabels{
						"ray.io/node-type": "head",
					},
				)).To(gomega.Succeed())
				g.Expect(pods.Items).NotTo(gomega.BeEmpty())

				headPodName := pods.Items[0].Name

				// Port-forward to the head pod on port 8000 (Ray Serve default)
				localPort, stopChan, err := util.KPortForward(cfg, restClient, ns.Name, headPodName, 8000)
				g.Expect(err).NotTo(gomega.HaveOccurred())
				defer close(stopChan)

				// Send HTTP GET to the Ray Serve endpoint
				resp, err := http.Get(fmt.Sprintf("http://localhost:%d/", localPort))
				g.Expect(err).NotTo(gomega.HaveOccurred())
				defer resp.Body.Close()

				g.Expect(resp.StatusCode).To(gomega.Equal(http.StatusOK))

				body, err := io.ReadAll(resp.Body)
				g.Expect(err).NotTo(gomega.HaveOccurred())
				g.Expect(strings.TrimSpace(string(body))).To(gomega.ContainSubstring("Hello, World!"))
			}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
