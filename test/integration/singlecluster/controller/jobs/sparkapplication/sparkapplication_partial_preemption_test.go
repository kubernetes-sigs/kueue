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

package sparkapplication

import (
	"fmt"
	"time"

	sparkv1beta2 "github.com/kubeflow/spark-operator/v2/api/v1beta2"
	sparkcommon "github.com/kubeflow/spark-operator/v2/pkg/common"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	pkgconstants "sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	testingsparkapplication "sigs.k8s.io/kueue/pkg/util/testingjobs/sparkapplication"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

// holdTerminationFinalizer keeps an evicted executor Pod terminating, as a kubelet does while the
// executor shuts down, so the test can observe the quota held until the Pod is gone.
const holdTerminationFinalizer = "test.kueue.x-k8s.io/hold-termination"

// This exercises the reactive partial-preemption loop for an elastic SparkApplication:
//
//	request  - the scheduler sets partialPreemption.retainedCount on the executor PodSet,
//	drain    - the SparkApplication reconciler evicts the youngest executors down to the target,
//	release  - once they are gone, the ungater lowers the admitted count (QuotaReleased),
//	admit    - the freed quota admits the preemptor, while replacement executors stay gated,
//	re-grow  - once the preemptor leaves, a scale-up slice admits the replacement executors.
var _ = ginkgo.Describe("SparkApplication partial preemption", ginkgo.Label("job:sparkapplication", "area:jobs"), func() {
	var (
		ns             *corev1.Namespace
		resourceFlavor *kueue.ResourceFlavor
		clusterQueue   *kueue.ClusterQueue
		localQueue     *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlicesForSparkApplication, true)
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.PartialPreemption, true)
		fwk.StartManagerForSpec(ctx, cfg, managerAndSchedulerSetup(false))

		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "spark-partial-preemption-")

		resourceFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
		behavioral.MustCreate(ctx, k8sClient, resourceFlavor)

		// "pods" is the constrained resource, as in the elastic tests: the victim's driver and
		// 4 executors fill the 5 pods.
		clusterQueue = utiltestingapi.MakeClusterQueue("default").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(resourceFlavor.Name).
				Resource(corev1.ResourcePods, "5").
				Resource(corev1.ResourceCPU, "100").
				Resource(corev1.ResourceMemory, "100Gi").
				Obj()).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("default", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, localQueue)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, resourceFlavor, true)
	})

	ginkgo.It("shrinks the executors to the floor to admit a preemptor, then re-grows them once it leaves", framework.SlowSpec, func() {
		const executors int32 = 4
		// The preemptor needs 2 of the 5 pods the victim fully occupies, so the reclaim target is
		// deterministic: 4 executors minus 2 = 2, the floor.
		const target int32 = 2

		executorKey := func(id int32) client.ObjectKey {
			return client.ObjectKey{Namespace: ns.Name, Name: fmt.Sprintf("victim-exec-%d", id)}
		}
		countGatedExecutors := func(g gomega.Gomega, ids ...int32) int {
			gated := 0
			for _, id := range ids {
				pod := &corev1.Pod{}
				g.Expect(k8sClient.Get(ctx, executorKey(id), pod)).Should(gomega.Succeed())
				if utilpod.HasGate(pod, kueue.ElasticJobSchedulingGate) {
					gated++
				}
			}
			return gated
		}

		// The suite installs no webhook configurations, so the app is suspended and its executor
		// template gated here, as the SparkApplication webhook does in production.
		sparkApp := testingsparkapplication.MakeSparkApplication("victim", ns.Name).
			Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			Annotation(pkgconstants.PartialPreemptionAnnotation, "true").
			ExecutorAnnotation(pkgconstants.PodSetPartialPreemptionMinRetainedCountAnnotation, fmt.Sprint(target)).
			Suspend(true).
			ExecutorSchedulingGates(corev1.PodSchedulingGate{Name: kueue.ElasticJobSchedulingGate}).
			Queue(localQueue.Name).
			ExecutorInstances(executors).
			DynamicAllocation(&sparkv1beta2.DynamicAllocation{Enabled: true, MaxExecutors: new(executors)}).
			Obj()

		var victimWl *kueue.Workload
		ginkgo.By("admitting the victim that occupies the whole ClusterQueue", func() {
			behavioral.MustCreate(ctx, k8sClient, sparkApp)
			victimWl = &behavioral.ExpectWorkloadsInNamespace(ctx, k8sClient, ns.Name, 1)[0]
			behavioral.ExpectPodSetAdmittedCount(ctx, k8sClient, victimWl, executorPodSetName, executors)
			behavioral.ExpectClusterQueueResourceUsage(ctx, k8sClient, client.ObjectKeyFromObject(clusterQueue), corev1.ResourcePods, 5)
		})

		driver := testingpod.MakePod("victim-driver", ns.Name).
			Label(sparkcommon.LabelSparkAppName, sparkApp.Name).
			Label(sparkcommon.LabelSparkRole, sparkcommon.SparkRoleDriver).
			Label(sparkcommon.LabelSubmissionID, submissionID).
			Obj()
		ginkgo.By("simulating the running driver and executors, and letting the ungater ungate the executors", func() {
			behavioral.MustCreate(ctx, k8sClient, driver)
			// Creation timestamps have one-second precision, so executors 3 and 4 are created in
			// a later second than 1 and 2, as the Spark driver creates its executors in batches a
			// second apart. Partial preemption evicts the youngest executors.
			for id := int32(1); id <= executors; id++ {
				if id == 3 {
					first := &corev1.Pod{}
					gomega.Expect(k8sClient.Get(ctx, executorKey(2), first)).Should(gomega.Succeed())
					time.Sleep(time.Until(first.CreationTimestamp.Add(time.Second)))
				}
				setExecutorPodReady(ctx, k8sClient, sparkApp, executorKey(id).Name)
			}
			// Kueue follows the executor Pods, so the status reports only the driver state.
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sparkApp), sparkApp)).Should(gomega.Succeed())
				sparkApp.Status.AppState.State = sparkv1beta2.ApplicationStateRunning
				sparkApp.Status.SubmissionAttempts = 1
				sparkApp.Status.SubmissionID = submissionID
				g.Expect(k8sClient.Status().Update(ctx, sparkApp)).Should(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			gomega.Eventually(countGatedExecutors, behavioral.Timeout, behavioral.Interval).
				WithArguments(int32(1), int32(2), int32(3), int32(4)).Should(gomega.Equal(0))
		})

		ginkgo.By("holding the termination of the executors expected to be evicted", func() {
			ginkgo.DeferCleanup(func() {
				for _, id := range []int32{3, 4} {
					pod := &corev1.Pod{Namespace: ns.Name, Name: executorKey(id).Name}
					gomega.Expect(client.IgnoreNotFound(k8sClient.Patch(ctx, pod,
						client.RawPatch(types.MergePatchType, []byte(`{"metadata":{"finalizers":null}}`))))).To(gomega.Succeed())
				}
			})
			for _, id := range []int32{3, 4} {
				gomega.Eventually(func(g gomega.Gomega) {
					pod := &corev1.Pod{}
					g.Expect(k8sClient.Get(ctx, executorKey(id), pod)).Should(gomega.Succeed())
					pod.Finalizers = append(pod.Finalizers, holdTerminationFinalizer)
					g.Expect(k8sClient.Update(ctx, pod)).Should(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			}
		})

		preemptor := utiltestingapi.MakeWorkload("preemptor", ns.Name).
			Priority(10).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			PodSets(*utiltestingapi.MakePodSet("main", 2).Request(corev1.ResourceCPU, "1").Obj()).
			Obj()

		ginkgo.By("requesting a partial preemption of the executors when the preemptor arrives", func() {
			behavioral.MustCreate(ctx, k8sClient, preemptor)
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(victimWl), victimWl)).Should(gomega.Succeed())
				got, ok := workload.PreemptionRetainedCount(victimWl, executorPodSetName)
				g.Expect(ok).Should(gomega.BeTrue())
				g.Expect(got).Should(gomega.Equal(target))
				g.Expect(victimWl.Status.Conditions).Should(utiltesting.HaveConditionStatusTrue(kueue.WorkloadPartiallyPreempted))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("evicting the youngest executors, never the driver", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				for _, id := range []int32{3, 4} {
					pod := &corev1.Pod{}
					g.Expect(k8sClient.Get(ctx, executorKey(id), pod)).Should(gomega.Succeed())
					g.Expect(pod.DeletionTimestamp).ShouldNot(gomega.BeNil())
				}
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			gomega.Consistently(func(g gomega.Gomega) {
				for _, key := range []client.ObjectKey{executorKey(1), executorKey(2), client.ObjectKeyFromObject(driver)} {
					pod := &corev1.Pod{}
					g.Expect(k8sClient.Get(ctx, key, pod)).Should(gomega.Succeed())
					g.Expect(pod.DeletionTimestamp).Should(gomega.BeNil())
				}
			}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
		})

		ginkgo.By("keeping the preemptor pending while the evicted executors are terminating", func() {
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(preemptor), preemptor)).Should(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(preemptor)).Should(gomega.BeFalse())
			}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
		})

		ginkgo.By("releasing the quota by lowering the admitted count once the evicted executors are gone", func() {
			for _, id := range []int32{3, 4} {
				gomega.Eventually(func(g gomega.Gomega) {
					pod := &corev1.Pod{}
					g.Expect(k8sClient.Get(ctx, executorKey(id), pod)).Should(gomega.Succeed())
					pod.Finalizers = nil
					g.Expect(k8sClient.Update(ctx, pod)).Should(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, executorKey(id), &corev1.Pod{})).Should(utiltesting.BeNotFoundError())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(victimWl), victimWl)).Should(gomega.Succeed())
				// The executor count follows the live executor Pods, so either the in-place
				// scale-down lowers the PodSet count or the ungater lowers the admitted count
				// first; the granted count is the smaller of both.
				g.Expect(workload.ExtractGrantedPodSetCounts(victimWl)[executorPodSetName]).Should(gomega.Equal(target))
				cond := apimeta.FindStatusCondition(victimWl.Status.Conditions, kueue.WorkloadPartiallyPreempted)
				g.Expect(cond).ShouldNot(gomega.BeNil())
				g.Expect(cond.Status).Should(gomega.Equal(metav1.ConditionFalse))
				g.Expect(cond.Reason).Should(gomega.Equal(kueue.WorkloadPartiallyPreemptedReasonQuotaReleased))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("admitting the preemptor with the released quota", func() {
			behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, preemptor)
			behavioral.ExpectClusterQueueResourceUsage(ctx, k8sClient, client.ObjectKeyFromObject(clusterQueue), corev1.ResourcePods, 5)
		})

		var scaleUpSlice *kueue.Workload
		ginkgo.By("keeping the replacement executors gated while the preemptor holds the quota", func() {
			// The driver replaces the evicted executors with new IDs, as Spark never reuses one.
			createExecutorPod(ctx, k8sClient, sparkApp, executorKey(5).Name)
			createExecutorPod(ctx, k8sClient, sparkApp, executorKey(6).Name)
			scaleUpSlice = behavioral.ExpectNewWorkloadSlice(ctx, k8sClient, victimWl)
			behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, scaleUpSlice)
			gomega.Consistently(countGatedExecutors, behavioral.ConsistentDuration, behavioral.ShortInterval).
				WithArguments(int32(5), int32(6)).Should(gomega.Equal(2))
		})

		ginkgo.By("re-growing the executors through the scale-up slice once the preemptor leaves", func() {
			gomega.Expect(k8sClient.Delete(ctx, preemptor)).Should(gomega.Succeed())
			behavioral.ExpectPodSetAdmittedCount(ctx, k8sClient, scaleUpSlice, executorPodSetName, executors)
			behavioral.ExpectWorkloadToFinish(ctx, k8sClient, client.ObjectKeyFromObject(victimWl))
			gomega.Eventually(countGatedExecutors, behavioral.Timeout, behavioral.Interval).
				WithArguments(int32(5), int32(6)).Should(gomega.Equal(0))
			behavioral.ExpectClusterQueueResourceUsage(ctx, k8sClient, client.ObjectKeyFromObject(clusterQueue), corev1.ResourcePods, 5)
		})
	})
})
