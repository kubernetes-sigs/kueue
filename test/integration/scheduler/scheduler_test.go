/*
Copyright 2022 The Kubernetes Authors.

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

package scheduler

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/util/pointer"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/integration/framework"
)

// +kubebuilder:docs-gen:collapse=Imports

var _ = ginkgo.Describe("Scheduler", func() {
	const (
		instanceKey = "cloud.provider.com/instance"
	)

	var (
		ns                  *corev1.Namespace
		onDemandFlavor      *kueue.ResourceFlavor
		spotTaintedFlavor   *kueue.ResourceFlavor
		spotUntaintedFlavor *kueue.ResourceFlavor
		spotToleration      corev1.Toleration
	)

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "core-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

		onDemandFlavor = testing.MakeResourceFlavor("on-demand").Label(instanceKey, "on-demand").Obj()

		spotTaintedFlavor = testing.MakeResourceFlavor("spot-tainted").
			Label(instanceKey, "spot-tainted").
			Taint(corev1.Taint{
				Key:    instanceKey,
				Value:  "spot-tainted",
				Effect: corev1.TaintEffectNoSchedule,
			}).Obj()

		spotToleration = corev1.Toleration{
			Key:      instanceKey,
			Operator: corev1.TolerationOpEqual,
			Value:    spotTaintedFlavor.Name,
			Effect:   corev1.TaintEffectNoSchedule,
		}

		spotUntaintedFlavor = testing.MakeResourceFlavor("spot-untainted").Label(instanceKey, "spot-untainted").Obj()
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(framework.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.When("Scheduling v1.Job", func() {
		var (
			prodClusterQ *kueue.ClusterQueue
			devClusterQ  *kueue.ClusterQueue
			prodQueue    *kueue.Queue
			devQueue     *kueue.Queue
		)

		ginkgo.BeforeEach(func() {
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, spotTaintedFlavor)).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, spotUntaintedFlavor)).Should(gomega.Succeed())

			prodClusterQ = testing.MakeClusterQueue("prod-cq").
				Cohort("prod").
				Resource(testing.MakeResource(corev1.ResourceCPU).
					Flavor(testing.MakeFlavor(spotTaintedFlavor.Name, "5").Max("5").Obj()).
					Flavor(testing.MakeFlavor(onDemandFlavor.Name, "5").Obj()).
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, prodClusterQ)).Should(gomega.Succeed())

			devClusterQ = testing.MakeClusterQueue("dev-clusterqueue").
				Resource(testing.MakeResource(corev1.ResourceCPU).
					Flavor(testing.MakeFlavor(spotUntaintedFlavor.Name, "5").Obj()).
					Flavor(testing.MakeFlavor(onDemandFlavor.Name, "5").Obj()).
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, devClusterQ)).Should(gomega.Succeed())

			prodQueue = testing.MakeQueue("prod-queue", ns.Name).ClusterQueue(prodClusterQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, prodQueue)).Should(gomega.Succeed())

			devQueue = testing.MakeQueue("dev-queue", ns.Name).ClusterQueue(devClusterQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, devQueue)).Should(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(framework.DeleteClusterQueue(ctx, k8sClient, prodClusterQ)).To(gomega.Succeed())
			gomega.Expect(framework.DeleteClusterQueue(ctx, k8sClient, devClusterQ)).To(gomega.Succeed())
			gomega.Expect(framework.DeleteResourceFlavor(ctx, k8sClient, onDemandFlavor)).To(gomega.Succeed())
			gomega.Expect(framework.DeleteResourceFlavor(ctx, k8sClient, spotTaintedFlavor)).To(gomega.Succeed())
			gomega.Expect(framework.DeleteResourceFlavor(ctx, k8sClient, spotUntaintedFlavor)).To(gomega.Succeed())
		})

		ginkgo.It("Should schedule jobs as they fit in their ClusterQueue", func() {
			ginkgo.By("checking the first prod job starts")
			prodJob1 := testing.MakeJob("prod-job1", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "2").Obj()
			gomega.Expect(k8sClient.Create(ctx, prodJob1)).Should(gomega.Succeed())
			lookupKey1 := types.NamespacedName{Name: prodJob1.Name, Namespace: prodJob1.Namespace}
			createdProdJob1 := &batchv1.Job{}
			gomega.Eventually(func() *bool {
				gomega.Expect(k8sClient.Get(ctx, lookupKey1, createdProdJob1)).Should(gomega.Succeed())
				return createdProdJob1.Spec.Suspend
			}, framework.Timeout, framework.Interval).Should(gomega.Equal(pointer.Bool(false)))
			gomega.Expect(createdProdJob1.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))
			framework.ExpectPendingWorkloadsMetric(prodQueue, 0)

			ginkgo.By("checking a second no-fit prod job does not start")
			prodJob2 := testing.MakeJob("prod-job2", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "5").Obj()
			gomega.Expect(k8sClient.Create(ctx, prodJob2)).Should(gomega.Succeed())
			lookupKey2 := types.NamespacedName{Name: prodJob2.Name, Namespace: prodJob2.Namespace}
			createdProdJob2 := &batchv1.Job{}
			gomega.Consistently(func() *bool {
				gomega.Expect(k8sClient.Get(ctx, lookupKey2, createdProdJob2)).Should(gomega.Succeed())
				return createdProdJob2.Spec.Suspend
			}, framework.ConsistentDuration, framework.Interval).Should(gomega.Equal(pointer.Bool(true)))
			framework.ExpectPendingWorkloadsMetric(prodQueue, 1)

			ginkgo.By("checking a dev job starts")
			devJob := testing.MakeJob("dev-job", ns.Name).Queue(devQueue.Name).Request(corev1.ResourceCPU, "5").Obj()
			gomega.Expect(k8sClient.Create(ctx, devJob)).Should(gomega.Succeed())
			createdDevJob := &batchv1.Job{}
			gomega.Eventually(func() *bool {
				gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: devJob.Name, Namespace: devJob.Namespace}, createdDevJob)).
					Should(gomega.Succeed())
				return createdDevJob.Spec.Suspend
			}, framework.Timeout, framework.Interval).Should(gomega.Equal(pointer.Bool(false)))
			gomega.Expect(createdDevJob.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(spotUntaintedFlavor.Name))
			framework.ExpectPendingWorkloadsMetric(devQueue, 0)

			ginkgo.By("checking the second prod job starts when the first finishes")
			createdProdJob1.Status.Conditions = append(createdProdJob1.Status.Conditions,
				batchv1.JobCondition{
					Type:               batchv1.JobComplete,
					Status:             corev1.ConditionTrue,
					LastProbeTime:      metav1.Now(),
					LastTransitionTime: metav1.Now(),
				})
			gomega.Expect(k8sClient.Status().Update(ctx, createdProdJob1)).Should(gomega.Succeed())
			gomega.Eventually(func() *bool {
				gomega.Expect(k8sClient.Get(ctx, lookupKey2, createdProdJob2)).Should(gomega.Succeed())
				return createdProdJob2.Spec.Suspend
			}, framework.Timeout, framework.Interval).Should(gomega.Equal(pointer.Bool(false)))
			gomega.Expect(createdProdJob2.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))
			framework.ExpectPendingWorkloadsMetric(prodQueue, 0)
		})

		ginkgo.It("Should schedule jobs according to their priorities", func() {
			queue := testing.MakeQueue("queue", ns.Name).ClusterQueue(prodClusterQ.Name).Obj()

			highPriorityClass := testing.MakePriorityClass("high-priority-class").PriorityValue(100).Obj()
			gomega.Expect(k8sClient.Create(ctx, highPriorityClass)).Should(gomega.Succeed())

			lowPriorityClass := testing.MakePriorityClass("low-priority-class").PriorityValue(10).Obj()
			gomega.Expect(k8sClient.Create(ctx, lowPriorityClass)).Should(gomega.Succeed())

			jobLowPriority := testing.MakeJob("job-low-priority", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "5").PriorityClass(lowPriorityClass.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, jobLowPriority)).Should(gomega.Succeed())
			jobHighPriority := testing.MakeJob("job-high-priority", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "5").PriorityClass(highPriorityClass.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, jobHighPriority)).Should(gomega.Succeed())

			ginkgo.By("checking that workload1 is created with priority and priorityName")
			createdLowPriorityWorkload := &kueue.Workload{}
			gomega.Eventually(func() error {
				lookupKey := types.NamespacedName{Name: jobLowPriority.Name, Namespace: jobLowPriority.Namespace}
				return k8sClient.Get(ctx, lookupKey, createdLowPriorityWorkload)
			}, framework.Timeout, framework.Interval).Should(gomega.Succeed())
			gomega.Expect(createdLowPriorityWorkload.Spec.PriorityClassName).Should(gomega.Equal(lowPriorityClass.Name))
			gomega.Expect(*createdLowPriorityWorkload.Spec.Priority).Should(gomega.Equal(lowPriorityClass.Value))

			ginkgo.By("checking that workload2 is created with priority and priorityName")
			createdHighPriorityWorkload := &kueue.Workload{}
			gomega.Eventually(func() error {
				lookupKey := types.NamespacedName{Name: jobHighPriority.Name, Namespace: jobHighPriority.Namespace}
				return k8sClient.Get(ctx, lookupKey, createdHighPriorityWorkload)
			}, framework.Timeout, framework.Interval).Should(gomega.Succeed())
			gomega.Expect(createdHighPriorityWorkload.Spec.PriorityClassName).Should(gomega.Equal(highPriorityClass.Name))
			gomega.Expect(*createdHighPriorityWorkload.Spec.Priority).Should(gomega.Equal(highPriorityClass.Value))

			// delay creating the queue until after workloads are created.
			gomega.Expect(k8sClient.Create(ctx, queue)).Should(gomega.Succeed())

			ginkgo.By("checking the job with low priority continues to be suspended")
			createdJob1 := &batchv1.Job{}
			gomega.Consistently(func() *bool {
				gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: jobLowPriority.Name, Namespace: ns.Name},
					createdJob1)).Should(gomega.Succeed())
				return createdJob1.Spec.Suspend
			}, framework.ConsistentDuration, framework.Interval).Should(gomega.Equal(pointer.Bool(true)))

			ginkgo.By("checking the job with high priority starts")
			createdJob2 := &batchv1.Job{}
			gomega.Eventually(func() *bool {
				gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: jobHighPriority.Name, Namespace: ns.Name}, createdJob2)).Should(gomega.Succeed())
				return createdJob2.Spec.Suspend
			}, framework.Timeout, framework.Interval).Should(gomega.Equal(pointer.Bool(false)))

			framework.ExpectPendingWorkloadsMetric(queue, 1)
		})
	})

	ginkgo.When("Handling workloads events", func() {
		var (
			cq    *kueue.ClusterQueue
			queue *kueue.Queue
		)

		ginkgo.BeforeEach(func() {
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, spotTaintedFlavor)).Should(gomega.Succeed())

			cq = testing.MakeClusterQueue("cluster-queue").
				Cohort("prod").
				Resource(testing.MakeResource(corev1.ResourceCPU).
					Flavor(testing.MakeFlavor(spotTaintedFlavor.Name, "5").Max("5").Obj()).
					Flavor(testing.MakeFlavor(onDemandFlavor.Name, "5").Obj()).
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, cq)).Should(gomega.Succeed())
			queue = testing.MakeQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, queue)).Should(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(framework.DeleteClusterQueue(ctx, k8sClient, cq)).To(gomega.Succeed())
			gomega.Expect(framework.DeleteResourceFlavor(ctx, k8sClient, onDemandFlavor)).To(gomega.Succeed())
			gomega.Expect(framework.DeleteResourceFlavor(ctx, k8sClient, spotTaintedFlavor)).To(gomega.Succeed())
		})

		ginkgo.It("Should re-enqueue by the delete event of workload belonging to the same ClusterQueue", func() {
			job1 := testing.MakeJob("on-demand-job1", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "4").Obj()
			gomega.Expect(k8sClient.Create(ctx, job1)).Should(gomega.Succeed())

			createdJob1 := &batchv1.Job{}
			gomega.Eventually(func() *bool {
				lookupKey := types.NamespacedName{Name: job1.Name, Namespace: job1.Namespace}
				gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob1)).Should(gomega.Succeed())
				return createdJob1.Spec.Suspend
			}, framework.Timeout, framework.Interval).Should(gomega.Equal(pointer.Bool(false)))
			gomega.Expect(createdJob1.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))
			framework.ExpectPendingWorkloadsMetric(queue, 0)

			job2 := testing.MakeJob("on-demand-job2", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "4").Obj()
			gomega.Expect(k8sClient.Create(ctx, job2)).Should(gomega.Succeed())
			createdJob2 := &batchv1.Job{}
			gomega.Consistently(func() *bool {
				lookupKey := types.NamespacedName{Name: job2.Name, Namespace: job2.Namespace}
				gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob2)).Should(gomega.Succeed())
				return createdJob2.Spec.Suspend
			}, framework.ConsistentDuration, framework.Interval).Should(gomega.Equal(pointer.Bool(true)))
			framework.ExpectPendingWorkloadsMetric(queue, 1)

			job3 := testing.MakeJob("on-demand-job3", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "1").Obj()
			gomega.Expect(k8sClient.Create(ctx, job3)).Should(gomega.Succeed())
			createdJob3 := &batchv1.Job{}
			gomega.Eventually(func() *bool {
				lookupKey := types.NamespacedName{Name: job3.Name, Namespace: job3.Namespace}
				gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob3)).Should(gomega.Succeed())
				return createdJob3.Spec.Suspend
			}, framework.Timeout, framework.Interval).Should(gomega.Equal(pointer.Bool(false)))
			gomega.Expect(createdJob3.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))
			framework.ExpectPendingWorkloadsMetric(queue, 1)

			ginkgo.By("deleting job1")
			gomega.Expect(k8sClient.Delete(ctx, job1, client.PropagationPolicy(metav1.DeletePropagationBackground))).Should(gomega.Succeed())
			// Simulated deletion of workload by ownerReference of job1.
			gomega.Expect(k8sClient.Delete(ctx,
				&kueue.Workload{ObjectMeta: metav1.ObjectMeta{
					Name:      job1.Name,
					Namespace: job1.Namespace,
				}})).Should(gomega.Succeed())
			gomega.Eventually(func() *bool {
				lookupKey := types.NamespacedName{Name: job2.Name, Namespace: job2.Namespace}
				gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob2)).Should(gomega.Succeed())
				return createdJob2.Spec.Suspend
			}, framework.Timeout, framework.Interval).Should(gomega.Equal(pointer.Bool(false)))
			gomega.Expect(createdJob2.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))
			framework.ExpectPendingWorkloadsMetric(queue, 0)
		})

		ginkgo.It("Should re-enqueue by the delete event of workload belonging to the same Cohort", func() {
			fooCq := testing.MakeClusterQueue("foo-clusterqueue").
				Cohort(cq.Spec.Cohort).
				Resource(testing.MakeResource(corev1.ResourceCPU).
					Flavor(testing.MakeFlavor(onDemandFlavor.Name, "5").Obj()).
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, fooCq)).Should(gomega.Succeed())
			defer func() {
				gomega.Expect(framework.DeleteClusterQueue(ctx, k8sClient, fooCq)).Should(gomega.Succeed())
			}()

			fooQ := testing.MakeQueue("foo-queue", ns.Name).ClusterQueue(fooCq.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, fooQ)).Should(gomega.Succeed())

			job1 := testing.MakeJob("on-demand-job1", ns.Name).Queue(fooQ.Name).Request(corev1.ResourceCPU, "8").Obj()
			gomega.Expect(k8sClient.Create(ctx, job1)).Should(gomega.Succeed())

			createdJob1 := &batchv1.Job{}
			gomega.Eventually(func() *bool {
				lookupKey := types.NamespacedName{Name: job1.Name, Namespace: job1.Namespace}
				gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob1)).Should(gomega.Succeed())
				return createdJob1.Spec.Suspend
			}, framework.Timeout, framework.Interval).Should(gomega.Equal(pointer.Bool(false)))
			gomega.Expect(createdJob1.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))
			framework.ExpectPendingWorkloadsMetric(fooQ, 0)

			job2 := testing.MakeJob("on-demand-job2",
				ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "8").Obj()
			gomega.Expect(k8sClient.Create(ctx, job2)).Should(gomega.Succeed())
			createdJob2 := &batchv1.Job{}
			gomega.Consistently(func() *bool {
				lookupKey := types.NamespacedName{Name: job2.Name, Namespace: job2.Namespace}
				gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob2)).Should(gomega.Succeed())
				return createdJob2.Spec.Suspend
			}, framework.ConsistentDuration, framework.Interval).Should(gomega.Equal(pointer.Bool(true)))
			framework.ExpectPendingWorkloadsMetric(queue, 1)

			job3 := testing.MakeJob("on-demand-job3", ns.Name).Queue(fooQ.Name).Request(corev1.ResourceCPU, "2").Obj()
			gomega.Expect(k8sClient.Create(ctx, job3)).Should(gomega.Succeed())
			createdJob3 := &batchv1.Job{}
			gomega.Eventually(func() *bool {
				lookupKey := types.NamespacedName{Name: job3.Name, Namespace: job3.Namespace}
				gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob3)).Should(gomega.Succeed())
				return createdJob3.Spec.Suspend
			}, framework.Timeout, framework.Interval).Should(gomega.Equal(pointer.Bool(false)))
			gomega.Expect(createdJob3.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))
			framework.ExpectPendingWorkloadsMetric(fooQ, 0)

			ginkgo.By("deleting job1")
			gomega.Expect(k8sClient.Delete(ctx, job1, client.PropagationPolicy(metav1.DeletePropagationBackground))).Should(gomega.Succeed())
			// Simulated deletion of workload by ownerReference of job1.
			gomega.Expect(k8sClient.Delete(ctx,
				&kueue.Workload{ObjectMeta: metav1.ObjectMeta{
					Name:      job1.Name,
					Namespace: job1.Namespace,
				}})).Should(gomega.Succeed())

			gomega.Eventually(func() *bool {
				lookupKey := types.NamespacedName{Name: job2.Name, Namespace: job2.Namespace}
				gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob2)).Should(gomega.Succeed())
				return createdJob2.Spec.Suspend
			}, framework.Timeout, framework.Interval).Should(gomega.Equal(pointer.Bool(false)))
			gomega.Expect(createdJob2.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))
			framework.ExpectPendingWorkloadsMetric(queue, 0)
		})
	})

	ginkgo.When("Handling clusterQueue events", func() {
		var (
			cq    *kueue.ClusterQueue
			queue *kueue.Queue
		)

		ginkgo.BeforeEach(func() {
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).Should(gomega.Succeed())

			cq = testing.MakeClusterQueue("cluster-queue").
				Resource(testing.MakeResource(corev1.ResourceCPU).
					Flavor(testing.MakeFlavor(onDemandFlavor.Name, "5").Obj()).
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, cq)).Should(gomega.Succeed())
			queue = testing.MakeQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, queue)).Should(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(framework.DeleteClusterQueue(ctx, k8sClient, cq)).To(gomega.Succeed())
			gomega.Expect(framework.DeleteResourceFlavor(ctx, k8sClient, onDemandFlavor)).To(gomega.Succeed())
		})
		ginkgo.It("Should re-enqueue by the update event of ClusterQueue", func() {
			job := testing.MakeJob("on-demand-job", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "6").Obj()
			gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
			createdJob2 := &batchv1.Job{}
			gomega.Consistently(func() *bool {
				lookupKey := types.NamespacedName{Name: job.Name, Namespace: job.Namespace}
				gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob2)).Should(gomega.Succeed())
				return createdJob2.Spec.Suspend
			}, framework.ConsistentDuration, framework.Interval).Should(gomega.Equal(pointer.Bool(true)))
			framework.ExpectPendingWorkloadsMetric(queue, 1)

			ginkgo.By("updating ClusterQueue")
			updatedCq := &kueue.ClusterQueue{}
			gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: cq.Name}, updatedCq)).Should(gomega.Succeed())

			updatedResource := testing.MakeResource(corev1.ResourceCPU).Flavor(testing.MakeFlavor(onDemandFlavor.Name, "6").Max("6").Obj()).Obj()
			updatedCq.Spec.Resources = []kueue.Resource{*updatedResource}
			gomega.Expect(k8sClient.Update(ctx, updatedCq)).Should(gomega.Succeed())

			gomega.Eventually(func() *bool {
				lookupKey := types.NamespacedName{Name: job.Name, Namespace: job.Namespace}
				gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob2)).Should(gomega.Succeed())
				return createdJob2.Spec.Suspend
			}, framework.Timeout, framework.Interval).Should(gomega.Equal(pointer.Bool(false)))
			gomega.Expect(createdJob2.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))
			framework.ExpectPendingWorkloadsMetric(queue, 0)
		})
	})

	ginkgo.When("Using clusterQueue NamespaceSelector", func() {
		var (
			cq    *kueue.ClusterQueue
			queue *kueue.Queue
		)

		ginkgo.BeforeEach(func() {
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).Should(gomega.Succeed())

			cq = testing.MakeClusterQueue("cluster-queue-with-selector").
				QueueingStrategy(kueue.StrictFIFO).
				NamespaceSelector(&metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{
							Key:      "dep",
							Operator: metav1.LabelSelectorOpIn,
							Values:   []string{"eng"},
						},
					},
				}).
				Resource(testing.MakeResource(corev1.ResourceCPU).Flavor(testing.MakeFlavor(onDemandFlavor.Name, "5").Obj()).Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, cq)).Should(gomega.Succeed())

			queue = testing.MakeQueue("queue-for-selector", ns.Name).ClusterQueue(cq.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, queue)).Should(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(framework.DeleteClusterQueue(ctx, k8sClient, cq)).ToNot(gomega.HaveOccurred())
			gomega.Expect(framework.DeleteResourceFlavor(ctx, k8sClient, onDemandFlavor)).To(gomega.Succeed())
		})

		ginkgo.It("Should schedule jobs from the selected namespaces", func() {
			ginkgo.By("checking a job doesn't start at first")
			job := testing.MakeJob("job", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "1").Obj()
			gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
			createdJob := &batchv1.Job{}
			gomega.Eventually(func() *bool {
				gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: job.Name, Namespace: job.Namespace}, createdJob)).Should(gomega.Succeed())
				return createdJob.Spec.Suspend
			}, framework.Timeout, framework.Interval).Should(gomega.Equal(pointer.Bool(true)), "Job should be suspended")
			framework.ExpectPendingWorkloadsMetric(queue, 1)

			ginkgo.By("checking the job starts after updating namespace labels to match QC selector")
			ns.Labels = map[string]string{"dep": "eng"}
			gomega.Expect(k8sClient.Update(ctx, ns)).Should(gomega.Succeed())
			gomega.Eventually(func() *bool {
				gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: job.Name, Namespace: job.Namespace}, createdJob)).Should(gomega.Succeed())
				return createdJob.Spec.Suspend
			}, framework.Timeout, framework.Interval).Should(gomega.Equal(pointer.Bool(false)), "Job should be unsuspended")
			framework.ExpectPendingWorkloadsMetric(queue, 0)
		})
	})

	ginkgo.When("Referencing resourceFlavors in clusterQueue", func() {
		var (
			fooCq *kueue.ClusterQueue
			fooQ  *kueue.Queue
		)

		ginkgo.BeforeEach(func() {
			fooCq = testing.MakeClusterQueue("foo-cq").
				QueueingStrategy(kueue.BestEffortFIFO).
				Resource(testing.MakeResource(corev1.ResourceCPU).
					Flavor(testing.MakeFlavor("foo-flavor", "15").Obj()).
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, fooCq)).Should(gomega.Succeed())
			fooQ = testing.MakeQueue("foo-queue", ns.Name).ClusterQueue(fooCq.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, fooQ)).Should(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(framework.DeleteClusterQueue(ctx, k8sClient, fooCq)).To(gomega.Succeed())
		})

		ginkgo.It("Should be inactive until the flavor is created", func() {
			ginkgo.By("Creating one workload")
			wl := testing.MakeWorkload("workload", ns.Name).Queue(fooQ.Name).Request(corev1.ResourceCPU, "1").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).Should(gomega.Succeed())
			framework.ExpectWorkloadsToBeFrozen(ctx, k8sClient, fooCq.Name, wl)

			ginkgo.By("Creating foo flavor")
			fooFlavor := testing.MakeResourceFlavor("foo-flavor").Obj()
			defer func() {
				gomega.Expect(framework.DeleteResourceFlavor(ctx, k8sClient, fooFlavor)).To(gomega.Succeed())
			}()
			gomega.Expect(k8sClient.Create(ctx, fooFlavor)).Should(gomega.Succeed())
			framework.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, fooCq.Name, wl)
		})

		ginkgo.It("Should be inactive when corresponding flavors deleted", func() {
			ginkgo.By("Creating foo flavors")
			fooFlavor := testing.MakeResourceFlavor("foo-flavor").Obj()
			gomega.Expect(k8sClient.Create(ctx, fooFlavor)).Should(gomega.Succeed())

			ginkgo.By("Creating one workload")
			wl1 := testing.MakeWorkload("workload", ns.Name).Queue(fooQ.Name).Request(corev1.ResourceCPU, "1").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl1)).Should(gomega.Succeed())
			framework.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, fooCq.Name, wl1)

			ginkgo.By("Deleting clusterQueue and foo flavors")
			gomega.Expect(framework.DeleteClusterQueue(ctx, k8sClient, fooCq)).To(gomega.Succeed())
			gomega.Expect(framework.DeleteResourceFlavor(ctx, k8sClient, fooFlavor)).To(gomega.Succeed())

			ginkgo.By("Creating another workloads")
			wl2 := testing.MakeWorkload("wl-2", ns.Name).Queue(fooQ.Name).Request(corev1.ResourceCPU, "1").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl2)).Should(gomega.Succeed())
			gomega.Expect(wl2.Status.Conditions).Should(gomega.BeEmpty())
		})
	})

	ginkgo.When("Using taints in resourceFlavors", func() {
		var (
			cq    *kueue.ClusterQueue
			queue *kueue.Queue
		)

		ginkgo.BeforeEach(func() {
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, spotTaintedFlavor)).Should(gomega.Succeed())

			cq = testing.MakeClusterQueue("cluster-queue").
				QueueingStrategy(kueue.BestEffortFIFO).
				Resource(testing.MakeResource(corev1.ResourceCPU).
					Flavor(testing.MakeFlavor(spotTaintedFlavor.Name, "5").Max("5").Obj()).
					Flavor(testing.MakeFlavor(onDemandFlavor.Name, "5").Obj()).
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, cq)).Should(gomega.Succeed())

			queue = testing.MakeQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, queue)).Should(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(framework.DeleteClusterQueue(ctx, k8sClient, cq)).To(gomega.Succeed())
			gomega.Expect(framework.DeleteResourceFlavor(ctx, k8sClient, onDemandFlavor)).To(gomega.Succeed())
			gomega.Expect(framework.DeleteResourceFlavor(ctx, k8sClient, spotTaintedFlavor)).To(gomega.Succeed())
		})

		ginkgo.It("Should schedule workloads on tolerated flavors", func() {
			ginkgo.By("checking a workload without toleration starts on the non-tainted flavor")
			wl1 := testing.MakeWorkload("on-demand-wl1", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "5").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl1)).Should(gomega.Succeed())

			expectAdmission := testing.MakeAdmission(cq.Name).Flavor(corev1.ResourceCPU, onDemandFlavor.Name).Obj()
			wlCopy := &kueue.Workload{}
			gomega.Eventually(func() *kueue.Admission {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wlCopy)).To(gomega.Succeed())
				return wlCopy.Spec.Admission
			}, framework.Timeout, framework.Interval).Should(testing.Equal(expectAdmission))
			framework.ExpectPendingWorkloadsMetric(queue, 0)

			ginkgo.By("checking a second workload without toleration doesn't start")
			wl2 := testing.MakeWorkload("on-demand-wl2", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "5").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl2)).Should(gomega.Succeed())
			framework.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
			framework.ExpectPendingWorkloadsMetric(queue, 1)

			ginkgo.By("checking a third workload with toleration starts")
			wl3 := testing.MakeWorkload("on-demand-wl3", ns.Name).Queue(queue.Name).Toleration(spotToleration).Request(corev1.ResourceCPU, "5").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl3)).Should(gomega.Succeed())

			expectAdmission = testing.MakeAdmission(cq.Name).Flavor(corev1.ResourceCPU, spotTaintedFlavor.Name).Obj()
			gomega.Eventually(func() *kueue.Admission {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl3), wlCopy)).To(gomega.Succeed())
				return wlCopy.Spec.Admission
			}, framework.Timeout, framework.Interval).Should(testing.Equal(expectAdmission))
			framework.ExpectPendingWorkloadsMetric(queue, 1)
		})
	})

	ginkgo.When("Using affinity in resourceFlavors", func() {
		var (
			cq    *kueue.ClusterQueue
			queue *kueue.Queue
		)

		ginkgo.BeforeEach(func() {
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, spotUntaintedFlavor)).Should(gomega.Succeed())

			cq = testing.MakeClusterQueue("cluster-queue").
				Resource(testing.MakeResource(corev1.ResourceCPU).
					Flavor(testing.MakeFlavor(spotUntaintedFlavor.Name, "5").Obj()).
					Flavor(testing.MakeFlavor(onDemandFlavor.Name, "5").Obj()).
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, cq)).Should(gomega.Succeed())

			queue = testing.MakeQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, queue)).Should(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(framework.DeleteClusterQueue(ctx, k8sClient, cq)).ToNot(gomega.HaveOccurred())
			gomega.Expect(framework.DeleteResourceFlavor(ctx, k8sClient, onDemandFlavor)).To(gomega.Succeed())
			gomega.Expect(framework.DeleteResourceFlavor(ctx, k8sClient, spotUntaintedFlavor)).To(gomega.Succeed())
		})

		ginkgo.It("Should schedule jobs with affinity to specific flavor", func() {
			ginkgo.By("checking a job without affinity starts on the first flavor")
			job1 := testing.MakeJob("no-affinity-job", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "1").Obj()
			gomega.Expect(k8sClient.Create(ctx, job1)).Should(gomega.Succeed())
			createdJob1 := &batchv1.Job{}
			gomega.Eventually(func() *bool {
				lookupKey := types.NamespacedName{Name: job1.Name, Namespace: job1.Namespace}
				gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob1)).Should(gomega.Succeed())
				return createdJob1.Spec.Suspend
			}, framework.Timeout, framework.Interval).Should(gomega.Equal(pointer.Bool(false)))
			gomega.Expect(createdJob1.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(spotUntaintedFlavor.Name))
			framework.ExpectPendingWorkloadsMetric(queue, 0)

			ginkgo.By("checking a second job with affinity to on-demand")
			job2 := testing.MakeJob("affinity-job", ns.Name).Queue(queue.Name).
				NodeSelector(instanceKey, onDemandFlavor.Name).
				NodeSelector("foo", "bar").
				Request(corev1.ResourceCPU, "1").Obj()
			gomega.Expect(k8sClient.Create(ctx, job2)).Should(gomega.Succeed())
			createdJob2 := &batchv1.Job{}
			gomega.Eventually(func() bool {
				lookupKey := types.NamespacedName{Name: job2.Name, Namespace: job2.Namespace}
				return k8sClient.Get(ctx, lookupKey, createdJob2) == nil && !*createdJob2.Spec.Suspend
			}, framework.Timeout, framework.Interval).Should(gomega.BeTrue())
			gomega.Expect(len(createdJob2.Spec.Template.Spec.NodeSelector)).Should(gomega.Equal(2))
			gomega.Expect(createdJob2.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))
			framework.ExpectPendingWorkloadsMetric(queue, 0)
		})
	})

	ginkgo.When("Using cohorts for fair-sharing", func() {
		ginkgo.BeforeEach(func() {
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, spotTaintedFlavor)).Should(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(framework.DeleteResourceFlavor(ctx, k8sClient, onDemandFlavor)).To(gomega.Succeed())
			gomega.Expect(framework.DeleteResourceFlavor(ctx, k8sClient, spotTaintedFlavor)).To(gomega.Succeed())
		})

		ginkgo.It("Should schedule jobs using borrowed ClusterQueue", func() {
			cq := testing.MakeClusterQueue("cluster-queue").
				Cohort("be").
				Resource(testing.MakeResource(corev1.ResourceCPU).
					Flavor(testing.MakeFlavor(spotTaintedFlavor.Name, "5").Max("5").Obj()).
					Flavor(testing.MakeFlavor(onDemandFlavor.Name, "5").Obj()).
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, cq)).Should(gomega.Succeed())
			defer func() {
				gomega.Expect(framework.DeleteClusterQueue(ctx, k8sClient, cq)).ToNot(gomega.HaveOccurred())
			}()

			queue := testing.MakeQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, queue)).Should(gomega.Succeed())

			ginkgo.By("checking a no-fit job does not start")
			job := testing.MakeJob("job", ns.Name).Queue(queue.Name).
				Request(corev1.ResourceCPU, "10").Toleration(spotToleration).Obj()
			gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
			lookupKey := types.NamespacedName{Name: job.Name, Namespace: job.Namespace}
			createdJob := &batchv1.Job{}
			gomega.Consistently(func() *bool {
				gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
				return createdJob.Spec.Suspend
			}, framework.ConsistentDuration, framework.Interval).Should(gomega.Equal(pointer.Bool(true)))
			framework.ExpectPendingWorkloadsMetric(queue, 1)

			ginkgo.By("checking the job starts when a fallback ClusterQueue gets added")
			fallbackClusterQueue := testing.MakeClusterQueue("fallback-cq").
				Cohort(cq.Spec.Cohort).
				Resource(testing.MakeResource(corev1.ResourceCPU).
					Flavor(testing.MakeFlavor(spotTaintedFlavor.Name, "5").Obj()). // cluster-queue can't borrow this flavor due to its max quota.
					Flavor(testing.MakeFlavor(onDemandFlavor.Name, "5").Obj()).
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, fallbackClusterQueue)).Should(gomega.Succeed())
			defer func() {
				gomega.Expect(framework.DeleteClusterQueue(ctx, k8sClient, fallbackClusterQueue)).ToNot(gomega.HaveOccurred())
			}()

			gomega.Eventually(func() *bool {
				gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
				return createdJob.Spec.Suspend
			}, framework.Timeout, framework.Interval).Should(gomega.Equal(pointer.Bool(false)))
			gomega.Expect(createdJob.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor.Name))
			framework.ExpectPendingWorkloadsMetric(queue, 0)
		})

		ginkgo.It("Should schedule workloads borrowing quota from ClusterQueues in the same Cohort", func() {
			prodBEClusterQ := testing.MakeClusterQueue("prod-be-cq").
				Cohort("be").
				Resource(testing.MakeResource(corev1.ResourceCPU).
					Flavor(testing.MakeFlavor(onDemandFlavor.Name, "5").Max("15").Obj()).
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, prodBEClusterQ)).Should(gomega.Succeed())
			defer func() {
				gomega.Expect(framework.DeleteClusterQueue(ctx, k8sClient, prodBEClusterQ)).ToNot(gomega.HaveOccurred())
			}()

			devBEClusterQ := testing.MakeClusterQueue("dev-be-cq").
				Cohort("be").
				Resource(testing.MakeResource(corev1.ResourceCPU).
					Flavor(testing.MakeFlavor(onDemandFlavor.Name, "5").Max("15").Obj()).
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, devBEClusterQ)).Should(gomega.Succeed())
			defer func() {
				gomega.Expect(framework.DeleteClusterQueue(ctx, k8sClient, devBEClusterQ)).ToNot(gomega.HaveOccurred())
			}()

			prodBEQueue := testing.MakeQueue("prod-be-queue", ns.Name).ClusterQueue(prodBEClusterQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, prodBEQueue)).Should(gomega.Succeed())

			devBEQueue := testing.MakeQueue("dev-be-queue", ns.Name).ClusterQueue(devBEClusterQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, devBEQueue)).Should(gomega.Succeed())
			wl1 := testing.MakeWorkload("wl-1", ns.Name).Queue(prodBEQueue.Name).Request(corev1.ResourceCPU, "11").Obj()
			wl2 := testing.MakeWorkload("wl-2", ns.Name).Queue(devBEQueue.Name).Request(corev1.ResourceCPU, "11").Obj()

			ginkgo.By("Creating two workloads")
			gomega.Expect(k8sClient.Create(ctx, wl1)).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, wl2)).Should(gomega.Succeed())
			framework.ExpectWorkloadsToBePending(ctx, k8sClient, wl1, wl2)
			framework.ExpectPendingWorkloadsMetric(prodBEQueue, 1)
			framework.ExpectPendingWorkloadsMetric(devBEQueue, 1)

			// Make sure workloads are in the same scheduling cycle.
			testBEClusterQ := testing.MakeClusterQueue("test-be-cq").
				Cohort("be").
				Resource(testing.MakeResource(corev1.ResourceCPU).
					Flavor(testing.MakeFlavor(onDemandFlavor.Name,
						"15").Max("15").Obj()).
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, testBEClusterQ)).Should(gomega.Succeed())
			defer func() {
				gomega.Expect(framework.DeleteClusterQueue(ctx, k8sClient, testBEClusterQ)).Should(gomega.Succeed())
			}()

			framework.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, prodBEClusterQ.Name, wl1)
			framework.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, devBEClusterQ.Name, wl2)
			framework.ExpectPendingWorkloadsMetric(prodBEQueue, 0)
			framework.ExpectPendingWorkloadsMetric(devBEQueue, 0)
		})
	})

	ginkgo.When("Scheduling with multi workloads", func() {
		var (
			cq    *kueue.ClusterQueue
			queue *kueue.Queue
		)
		ginkgo.BeforeEach(func() {
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).Should(gomega.Succeed())

			cq = testing.MakeClusterQueue("cluster-queue").
				Resource(testing.MakeResource(corev1.ResourceCPU).
					Flavor(testing.MakeFlavor(onDemandFlavor.Name, "5").Obj()).
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, cq)).Should(gomega.Succeed())

			queue = testing.MakeQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, queue)).Should(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(framework.DeleteClusterQueue(ctx, k8sClient, cq)).ToNot(gomega.HaveOccurred())
			gomega.Expect(framework.DeleteResourceFlavor(ctx, k8sClient, onDemandFlavor)).To(gomega.Succeed())
		})

		ginkgo.It("Should admit two small workloads after a big one finishes", func() {
			bigWl := testing.MakeWorkload("big-wl", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "5").Obj()
			ginkgo.By("Creating big workload")
			gomega.Expect(k8sClient.Create(ctx, bigWl)).Should(gomega.Succeed())

			framework.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, cq.Name, bigWl)
			framework.ExpectPendingWorkloadsMetric(queue, 0)

			smallWl1 := testing.MakeWorkload("small-wl-1", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "2.5").Obj()
			smallWl2 := testing.MakeWorkload("small-wl-2", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "2.5").Obj()
			ginkgo.By("Creating two small workloads")
			gomega.Expect(k8sClient.Create(ctx, smallWl1)).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, smallWl2)).Should(gomega.Succeed())

			framework.ExpectWorkloadsToBePending(ctx, k8sClient, smallWl1, smallWl2)
			framework.ExpectPendingWorkloadsMetric(queue, 2)

			ginkgo.By("Marking the big workload as finished")
			framework.UpdateWorkloadStatus(ctx, k8sClient, bigWl, func(wl *kueue.Workload) {
				wl.Status.Conditions = append(wl.Status.Conditions, kueue.WorkloadCondition{
					Type:   kueue.WorkloadFinished,
					Status: corev1.ConditionTrue,
				})
			})

			framework.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, cq.Name, smallWl1, smallWl2)
			framework.ExpectPendingWorkloadsMetric(queue, 0)
		})
	})

	ginkgo.When("Queueing with StrictFIFO", func() {
		var strictFIFOClusterQ *kueue.ClusterQueue

		ginkgo.BeforeEach(func() {
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).Should(gomega.Succeed())
			strictFIFOClusterQ = testing.MakeClusterQueue("strict-fifo-cq").
				QueueingStrategy(kueue.StrictFIFO).
				Resource(testing.MakeResource(corev1.ResourceCPU).
					Flavor(testing.MakeFlavor(onDemandFlavor.Name, "5").Max("5").Obj()).
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, strictFIFOClusterQ)).Should(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(framework.DeleteClusterQueue(ctx, k8sClient, strictFIFOClusterQ)).To(gomega.Succeed())
			gomega.Expect(framework.DeleteResourceFlavor(ctx, k8sClient, onDemandFlavor)).To(gomega.Succeed())
		})

		ginkgo.It("Should schedule workloads by their priority strictly in StrictFIFO", func() {
			strictFIFOQueue := testing.MakeQueue("strict-fifo-q", ns.Name).ClusterQueue(strictFIFOClusterQ.Name).Obj()

			ginkgo.By("Creating workloads")
			wl1 := testing.MakeWorkload("wl1", ns.Name).Queue(strictFIFOQueue.
				Name).Request(corev1.ResourceCPU, "2").Priority(pointer.Int32(100)).Obj()
			gomega.Expect(k8sClient.Create(ctx, wl1)).Should(gomega.Succeed())
			wl2 := testing.MakeWorkload("wl2", ns.Name).Queue(strictFIFOQueue.
				Name).Request(corev1.ResourceCPU, "5").Priority(pointer.Int32(10)).Obj()
			gomega.Expect(k8sClient.Create(ctx, wl2)).Should(gomega.Succeed())
			// wl3 can't be scheduled before wl2 even though there is enough quota.
			wl3 := testing.MakeWorkload("wl3", ns.Name).Queue(strictFIFOQueue.
				Name).Request(corev1.ResourceCPU, "1").Priority(pointer.Int32(1)).Obj()
			gomega.Expect(k8sClient.Create(ctx, wl3)).Should(gomega.Succeed())

			gomega.Expect(k8sClient.Create(ctx, strictFIFOQueue)).Should(gomega.Succeed())

			framework.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, strictFIFOClusterQ.Name, wl1)
			framework.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
			gomega.Consistently(func() bool {
				lookupKey := types.NamespacedName{Name: wl3.Name, Namespace: wl3.Namespace}
				gomega.Expect(k8sClient.Get(ctx, lookupKey, wl3)).Should(gomega.Succeed())
				return wl3.Spec.Admission == nil
			}, framework.ConsistentDuration, framework.Interval).Should(gomega.Equal(true))
		})
	})
})
