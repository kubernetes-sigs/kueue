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
	sparkv1beta2 "github.com/kubeflow/spark-operator/v2/api/v1beta2"
	sparkcommon "github.com/kubeflow/spark-operator/v2/pkg/common"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/controller/jobs/sparkapplication"
	"sigs.k8s.io/kueue/pkg/features"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	"sigs.k8s.io/kueue/pkg/util/podset"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingrayjob "sigs.k8s.io/kueue/pkg/util/testingjobs/rayjob"
	sparkapplicationtesting "sigs.k8s.io/kueue/pkg/util/testingjobs/sparkapplication"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/e2e"
)

// kubernetes-sigs/kueue#975: when borrowed capacity is given back, an elastic job is shrunk
// only as much as needed instead of being evicted whole, and it keeps running to completion.
//
// The cohort has 4 CPUs. The elastic batch job runs 4 workers or executors and takes 3 CPUs,
// 2 of them borrowed from prod-cq. A prod Job needing 1.5 CPUs, with 1 CPU free, makes prod-cq
// reclaim 500m: one worker or executor.
var _ = ginkgo.Describe("Partial preemption", ginkgo.Label("feature:partialpreemption"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns      *corev1.Namespace
		rf      *kueue.ResourceFlavor
		batchCQ *kueue.ClusterQueue
		prodCQ  *kueue.ClusterQueue
		batchLQ *kueue.LocalQueue
		prodLQ  *kueue.LocalQueue
	)

	ginkgo.BeforeAll(func() {
		e2e.UpdateKueueConfigurationAndRestart(ctx, k8sClient, defaultKueueCfg, kindClusterName, func(cfg *configapi.Configuration) {
			cfg.Integrations.Frameworks = append(cfg.Integrations.Frameworks, sparkapplication.FrameworkName)
			cfg.FeatureGates[string(features.SparkApplicationIntegration)] = true
			cfg.FeatureGates[string(features.PartialPreemption)] = true
		})
	})

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "partial-preemption-e2e-")
		rf = utiltestingapi.MakeResourceFlavor("partial-preemption-rf-" + ns.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, rf)
		cohort := kueue.CohortReference("partial-preemption-" + ns.Name)
		batchCQ = utiltestingapi.MakeClusterQueue("batch-cq-" + ns.Name).
			Cohort(cohort).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).
				Resource(corev1.ResourceCPU, "1").
				Resource(corev1.ResourceMemory, "8Gi").
				Obj()).
			Obj()
		prodCQ = utiltestingapi.MakeClusterQueue("prod-cq-" + ns.Name).
			Cohort(cohort).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).
				Resource(corev1.ResourceCPU, "3").
				Resource(corev1.ResourceMemory, "8Gi").
				Obj()).
			Preemption(kueue.ClusterQueuePreemption{ReclaimWithinCohort: kueue.PreemptionPolicyAny}).
			Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, batchCQ, prodCQ)
		batchLQ = utiltestingapi.MakeLocalQueue("batch-lq", ns.Name).ClusterQueue(batchCQ.Name).Obj()
		prodLQ = utiltestingapi.MakeLocalQueue("prod-lq", ns.Name).ClusterQueue(prodCQ.Name).Obj()
		behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, batchLQ, prodLQ)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteAllRayJobsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(behavioral.DeleteAllSparkApplicationsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(behavioral.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
		gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, batchLQ, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, prodLQ, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, batchCQ, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, prodCQ, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	// expectActiveSlice returns the admitted, not finished workload slice of the job.
	expectActiveSlice := func(jobUID types.UID) *kueue.Workload {
		ginkgo.GinkgoHelper()
		slice := &kueue.Workload{}
		gomega.Eventually(func(g gomega.Gomega) {
			wls := &kueue.WorkloadList{}
			g.Expect(k8sClient.List(ctx, wls, client.InNamespace(ns.Name),
				client.MatchingLabels{controllerconstants.JobUIDLabel: string(jobUID)})).To(gomega.Succeed())
			active := behavioral.FindNonFinishedWorkloads(wls.Items)
			g.Expect(active).To(gomega.HaveLen(1))
			g.Expect(workload.IsAdmitted(&active[0])).To(gomega.BeTrue())
			slice = &active[0]
		}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())
		return slice
	}

	// reclaimForProdJob submits the prod Job and expects the slice to be shrunk to count instead of evicted.
	reclaimForProdJob := func(slice *kueue.Workload, podSetName kueue.PodSetReference, count int32) *batchv1.Job {
		ginkgo.GinkgoHelper()
		prodJob := testingjob.MakeJob("prod", ns.Name).
			Queue(kueue.LocalQueueName(prodLQ.Name)).
			Image(e2e.GetAgnHostImage(), e2e.BehaviorWaitForDeletion).
			RequestAndLimit(corev1.ResourceCPU, "1500m").
			Obj()
		behavioral.MustCreate(ctx, k8sClient, prodJob)
		behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, &kueue.Workload{ObjectMeta: metav1.ObjectMeta{
			Name:      workloadjob.GetWorkloadNameForJob(prodJob.Name, prodJob.UID),
			Namespace: ns.Name,
		}})
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(slice), slice)).To(gomega.Succeed())
			g.Expect(slice.Annotations).To(gomega.HaveKeyWithValue(constants.PartiallyPreemptedAnnotation, "true"))
			ps := podset.FindPodSetByName(slice.Spec.PodSets, podSetName)
			g.Expect(ps).NotTo(gomega.BeNil())
			g.Expect(ps.Count).To(gomega.Equal(count), "the slice should shrink only by what prod-cq needs")
			g.Expect(slice.Status.Conditions).NotTo(utiltesting.HaveConditionStatusTrue(kueue.WorkloadEvicted))
		}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())
		return prodJob
	}

	deleteProdJob := func(prodJob *batchv1.Job) {
		ginkgo.GinkgoHelper()
		gomega.Expect(k8sClient.Delete(ctx, prodJob, client.PropagationPolicy(metav1.DeletePropagationBackground))).To(gomega.Succeed())
	}

	expectNeverEvicted := func(jobUID types.UID) {
		ginkgo.GinkgoHelper()
		wls := &kueue.WorkloadList{}
		gomega.Expect(k8sClient.List(ctx, wls, client.InNamespace(ns.Name),
			client.MatchingLabels{controllerconstants.JobUIDLabel: string(jobUID)})).To(gomega.Succeed())
		for _, wl := range wls.Items {
			gomega.Expect(wl.Status.Conditions).NotTo(utiltesting.HaveConditionStatusTrue(kueue.WorkloadEvicted), "workload %s", wl.Name)
		}
	}

	expectPodKept := func(pod *corev1.Pod) {
		ginkgo.GinkgoHelper()
		got := &corev1.Pod{}
		gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), got)).To(gomega.Succeed())
		gomega.Expect(got.UID).To(gomega.Equal(pod.UID), "pod %s was restarted", pod.Name)
	}

	ginkgo.It("should shrink a RayJob instead of evicting it when its borrowed quota is reclaimed", ginkgo.Label("feature:kuberay"), func() {
		kuberayTestImage := e2e.GetKuberayTestImage()
		configMap := &corev1.ConfigMap{
			Name:      "rayjob-partial-preemption",
			Namespace: ns.Name,
			Data: map[string]string{
				// 80 tasks of 10s keep 4 workers busy for about 200s. Tasks on a deleted worker are retried.
				"tasks.py": `import time
import ray

ray.init()

@ray.remote(num_cpus=1)
def task(i):
    time.sleep(10)
    return i

print(sum(ray.get([task.remote(i) for i in range(80)])))`,
			},
		}
		behavioral.MustCreate(ctx, k8sClient, configMap)

		// Quota: head 400m + autoscaler 100m + submitter 500m + 4 workers of 500m = 3 CPUs.
		rayJob := testingrayjob.MakeJob("rayjob-partial-preemption", ns.Name).
			Queue(batchLQ.Name).
			Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			Annotation(constants.ElasticJobScaleUpStrategyAnnotationKey, constants.ElasticJobScaleUpStrategyPartial).
			EnableInTreeAutoscaling().
			MaxWorkerReplicas(4).
			WithSubmissionMode(rayv1.K8sJobMode).
			Entrypoint("python /home/ray/samples/tasks.py").
			Request(rayv1.HeadNode, corev1.ResourceCPU, "400m").
			RayStartParam(rayv1.HeadNode, "num-cpus", "0").
			RayStartParam(rayv1.HeadNode, "object-store-memory", "100000000").
			Request(rayv1.WorkerNode, corev1.ResourceCPU, "500m").
			RayStartParam(rayv1.WorkerNode, "num-cpus", "1").
			RayStartParam(rayv1.WorkerNode, "object-store-memory", "100000000").
			WithSubmitterPodTemplate(corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "rayjob-submitter",
						Image: kuberayTestImage,
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m")},
						},
					}},
					RestartPolicy: corev1.RestartPolicyNever,
				},
			}).
			Image(rayv1.HeadNode, kuberayTestImage).
			Image(rayv1.WorkerNode, kuberayTestImage).
			Volumes(rayv1.HeadNode, []corev1.Volume{{
				Name: "code",
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: configMap.Name},
					},
				},
			}}).
			VolumeMounts(rayv1.HeadNode, []corev1.VolumeMount{{Name: "code", MountPath: "/home/ray/samples"}}).
			TerminationGracePeriod(1).
			Obj()
		rayJob.Spec.RayClusterSpec.AutoscalerOptions.Resources = &corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
		}
		behavioral.MustCreate(ctx, k8sClient, rayJob)

		var rayClusterKey client.ObjectKey
		headPod := &corev1.Pod{}
		ginkgo.By("the RayJob scales up to 4 workers, borrowing from prod-cq", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rayJob), rayJob)).To(gomega.Succeed())
				g.Expect(rayJob.Status.JobDeploymentStatus).To(gomega.Equal(rayv1.JobDeploymentStatusRunning))
				g.Expect(rayJob.Status.RayClusterStatus.Head.PodName).NotTo(gomega.BeEmpty())
			}, behavioral.VeryLongTimeout, behavioral.Interval).Should(gomega.Succeed())
			rayClusterKey = client.ObjectKey{Namespace: ns.Name, Name: rayJob.Status.RayClusterName}
			e2e.ExpectRayClusterWorkerPods(ctx, k8sClient, rayClusterKey, 4, 0)
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns.Name, Name: rayJob.Status.RayClusterStatus.Head.PodName}, headPod)).To(gomega.Succeed())
		})

		slice := expectActiveSlice(rayJob.UID)
		var prodJob *batchv1.Job
		ginkgo.By("prod-cq reclaims 500m: the RayJob loses one worker instead of being evicted", func() {
			prodJob = reclaimForProdJob(slice, "workers-group-0", 3)
			// The deleted worker is recreated by KubeRay and stays gated.
			e2e.ExpectRayClusterWorkerPods(ctx, k8sClient, rayClusterKey, 3, 1)
			expectPodKept(headPod)
		})

		ginkgo.By("the prod Job finishes: the RayJob grows back to 4 workers", func() {
			deleteProdJob(prodJob)
			e2e.ExpectRayClusterWorkerPods(ctx, k8sClient, rayClusterKey, 4, 0)
			expectPodKept(headPod)
		})

		ginkgo.By("the RayJob succeeds, and none of its slices was evicted", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rayJob), rayJob)).To(gomega.Succeed())
				g.Expect(rayJob.Status.JobStatus).To(gomega.Equal(rayv1.JobStatusSucceeded))
			}, behavioral.VeryLongTimeout, behavioral.Interval).Should(gomega.Succeed())
			expectNeverEvicted(rayJob.UID)
		})
	})

	ginkgo.It("should shrink a SparkApplication instead of evicting it when its borrowed quota is reclaimed", ginkgo.Label("feature:spark"), func() {
		sa := &corev1.ServiceAccount{Name: "spark", Namespace: ns.Name}
		behavioral.MustCreate(ctx, k8sClient, sa)
		behavioral.MustCreate(ctx, k8sClient, &rbacv1.RoleBinding{
			Name:      "spark-edit",
			Namespace: ns.Name,
			Subjects:  []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: sa.Name, Namespace: ns.Name}},
			RoleRef:   rbacv1.RoleRef{Kind: "ClusterRole", Name: "edit", APIGroup: rbacv1.GroupName},
		})

		// Quota: driver 1 CPU + 4 executors of 500m = 3 CPUs.
		sparkImage := e2e.GetSparkTestImage()
		sparkApp := sparkapplicationtesting.MakeSparkApplication("sparkapplication-partial-preemption", ns.Name).
			Image(sparkImage).
			SparkVersion(e2e.VersionFromImage(sparkImage)).
			Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			Annotation(constants.ElasticJobScaleUpStrategyAnnotationKey, constants.ElasticJobScaleUpStrategyPartial).
			DriverServiceAccount(sa.Name).
			DriverCoreRequest("1").
			DriverMemoryRequest("512m").
			ExecutorServiceAccount(sa.Name).
			ExecutorCoreRequest("500m").
			ExecutorMemoryRequest("512m").
			ExecutorInstances(1).
			DynamicAllocation(&sparkv1beta2.DynamicAllocation{
				Enabled:          true,
				InitialExecutors: new(int32(1)),
				MinExecutors:     new(int32(1)),
				MaxExecutors:     new(int32(4)),
			}).
			Queue(batchLQ.Name).
			Obj()
		// SparkPi with 300000 partitions keeps 4 executors busy for a few minutes.
		sparkApp.Spec.Arguments = []string{"300000"}
		sparkApp.Spec.SparkConf = map[string]string{
			"spark.dynamicAllocation.schedulerBacklogTimeout":          "1s",
			"spark.dynamicAllocation.sustainedSchedulerBacklogTimeout": "1s",
		}
		behavioral.MustCreate(ctx, k8sClient, sparkApp)

		expectExecutorPods := func(wantRunning, wantGated int) {
			ginkgo.GinkgoHelper()
			gomega.Eventually(func(g gomega.Gomega) {
				pods := &corev1.PodList{}
				g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name), client.MatchingLabels{
					sparkcommon.LabelSparkAppName: sparkApp.Name,
					sparkcommon.LabelSparkRole:    sparkcommon.SparkRoleExecutor,
				})).To(gomega.Succeed())
				running, gated := 0, 0
				for i := range pods.Items {
					pod := &pods.Items[i]
					switch {
					case pod.DeletionTimestamp != nil:
					case utilpod.HasGate(pod, kueue.ElasticJobSchedulingGate):
						gated++
					case pod.Status.Phase == corev1.PodRunning:
						running++
					}
				}
				g.Expect(running).To(gomega.Equal(wantRunning), "running executor Pods")
				g.Expect(gated).To(gomega.Equal(wantGated), "gated executor Pods")
			}, behavioral.VeryLongTimeout, behavioral.Interval).Should(gomega.Succeed())
		}

		driverPod := &corev1.Pod{}
		ginkgo.By("the SparkApplication scales up to 4 executors, borrowing from prod-cq", func() {
			expectExecutorPods(4, 0)
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sparkApp), sparkApp)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns.Name, Name: sparkApp.Status.DriverInfo.PodName}, driverPod)).To(gomega.Succeed())
		})

		slice := expectActiveSlice(sparkApp.UID)
		var prodJob *batchv1.Job
		ginkgo.By("prod-cq reclaims 500m: the SparkApplication loses one executor instead of being evicted", func() {
			prodJob = reclaimForProdJob(slice, sparkcommon.SparkRoleExecutor, 3)
			// The driver replaces the deleted executor, and it stays gated.
			expectExecutorPods(3, 1)
			expectPodKept(driverPod)
		})

		ginkgo.By("the prod Job finishes: the SparkApplication grows back to 4 executors", func() {
			deleteProdJob(prodJob)
			expectExecutorPods(4, 0)
			expectPodKept(driverPod)
		})

		ginkgo.By("the SparkApplication completes in its first submission, and none of its slices was evicted", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sparkApp), sparkApp)).To(gomega.Succeed())
				g.Expect(sparkApp.Status.AppState.State).To(gomega.Equal(sparkv1beta2.ApplicationStateCompleted))
				g.Expect(sparkApp.Status.SubmissionAttempts).To(gomega.Equal(int32(1)))
			}, behavioral.VeryLongTimeout, behavioral.Interval).Should(gomega.Succeed())
			expectNeverEvicted(sparkApp.UID)
		})
	})
})
