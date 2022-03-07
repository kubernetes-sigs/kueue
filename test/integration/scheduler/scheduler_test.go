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
	"github.com/onsi/ginkgo"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	kueue "sigs.k8s.io/kueue/api/v1alpha1"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/integration/framework"
)

// +kubebuilder:docs-gen:collapse=Imports

var _ = ginkgo.Describe("Scheduler", func() {
	const (
		instanceKey    = "cloud.provider.com/instance"
		onDemandFlavor = "on-demand"
		spotFlavor     = "spot"
	)

	var (
		ns           *corev1.Namespace
		prodClusterQ *kueue.ClusterQueue
		devClusterQ  *kueue.ClusterQueue
		prodQueue    *kueue.Queue
		devQueue     *kueue.Queue
	)

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "core-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
		prodClusterQ = testing.MakeClusterQueue("prod-cq").
			Cohort("prod").
			Resource(testing.MakeResource(corev1.ResourceCPU).
				Flavor(testing.MakeFlavor(spotFlavor, "5").
					Taint(corev1.Taint{
						Key:    instanceKey,
						Value:  spotFlavor,
						Effect: corev1.TaintEffectNoSchedule,
					}).
					Label(instanceKey, spotFlavor).Obj()).
				Flavor(testing.MakeFlavor(onDemandFlavor, "5").
					Ceiling("10").
					Label(instanceKey, onDemandFlavor).Obj()).
				Obj()).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, prodClusterQ)).Should(gomega.Succeed())

		devClusterQ = testing.MakeClusterQueue("dev-capacity").
			Resource(testing.MakeResource(corev1.ResourceCPU).
				Flavor(testing.MakeFlavor(spotFlavor, "5").Label(instanceKey, spotFlavor).Obj()).
				Flavor(testing.MakeFlavor(onDemandFlavor, "5").Label(instanceKey, onDemandFlavor).Obj()).
				Obj()).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, devClusterQ)).Should(gomega.Succeed())

		prodQueue = testing.MakeQueue("prod-queue", ns.Name).Capacity(prodClusterQ.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, prodQueue)).Should(gomega.Succeed())

		devQueue = testing.MakeQueue("dev-queue", ns.Name).Capacity(devClusterQ.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, devQueue)).Should(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(framework.DeleteNamespace(ctx, k8sClient, ns)).ToNot(gomega.HaveOccurred())
		gomega.Expect(framework.DeleteClusterQueue(ctx, k8sClient, prodClusterQ)).ToNot(gomega.HaveOccurred())
		gomega.Expect(framework.DeleteClusterQueue(ctx, k8sClient, devClusterQ)).ToNot(gomega.HaveOccurred())
		ns = nil
	})

	ginkgo.It("Should schedule jobs as they fit in their capacities", func() {
		ginkgo.By("checking the first prod job starts")
		prodJob1 := testing.MakeJob("prod-job1", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "2").Obj()
		gomega.Expect(k8sClient.Create(ctx, prodJob1)).Should(gomega.Succeed())
		lookupKey1 := types.NamespacedName{Name: prodJob1.Name, Namespace: prodJob1.Namespace}
		createdProdJob1 := &batchv1.Job{}
		gomega.Eventually(func() bool {
			err := k8sClient.Get(ctx, lookupKey1, createdProdJob1)
			return err == nil && !*createdProdJob1.Spec.Suspend
		}, framework.Timeout, framework.Interval).Should(gomega.BeTrue())
		gomega.Expect(createdProdJob1.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor))

		ginkgo.By("checking a second no-fit prod job does not start")
		prodJob2 := testing.MakeJob("prod-job2", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "5").Obj()
		gomega.Expect(k8sClient.Create(ctx, prodJob2)).Should(gomega.Succeed())
		lookupKey2 := types.NamespacedName{Name: prodJob2.Name, Namespace: prodJob2.Namespace}
		createdProdJob2 := &batchv1.Job{}
		gomega.Consistently(func() bool {
			return k8sClient.Get(ctx, lookupKey2, createdProdJob2) == nil && *createdProdJob2.Spec.Suspend
		}, framework.ConsistentDuration, framework.Interval).Should(gomega.BeTrue())

		ginkgo.By("checking a dev job starts")
		devJob := testing.MakeJob("dev-job", ns.Name).Queue(devQueue.Name).Request(corev1.ResourceCPU, "5").Obj()
		gomega.Expect(k8sClient.Create(ctx, devJob)).Should(gomega.Succeed())
		createdDevJob := &batchv1.Job{}
		gomega.Eventually(func() bool {
			key := types.NamespacedName{Name: devJob.Name, Namespace: devJob.Namespace}
			return k8sClient.Get(ctx, key, createdDevJob) == nil && !*createdDevJob.Spec.Suspend
		}, framework.Timeout, framework.Interval).Should(gomega.BeTrue())
		gomega.Expect(createdDevJob.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(spotFlavor))

		ginkgo.By("checking the second prod job starts when the first finishes")
		createdProdJob1.Status.Conditions = append(createdProdJob1.Status.Conditions,
			batchv1.JobCondition{
				Type:               batchv1.JobComplete,
				Status:             corev1.ConditionTrue,
				LastProbeTime:      metav1.Now(),
				LastTransitionTime: metav1.Now(),
			})
		gomega.Expect(k8sClient.Status().Update(ctx, createdProdJob1)).Should(gomega.Succeed())
		gomega.Eventually(func() bool {
			return k8sClient.Get(ctx, lookupKey2, createdProdJob2) == nil && !*createdProdJob2.Spec.Suspend
		}, framework.Timeout, framework.Interval).Should(gomega.BeTrue())
		gomega.Expect(createdProdJob2.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor))
	})

	ginkgo.It("Should schedule jobs on tolerated flavors", func() {
		ginkgo.By("checking a job without toleration starts on the non-tainted flavor")
		job1 := testing.MakeJob("on-demand-job1", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "5").Obj()
		gomega.Expect(k8sClient.Create(ctx, job1)).Should(gomega.Succeed())
		createdJob1 := &batchv1.Job{}
		gomega.Eventually(func() bool {
			lookupKey := types.NamespacedName{Name: job1.Name, Namespace: job1.Namespace}
			return k8sClient.Get(ctx, lookupKey, createdJob1) == nil && !*createdJob1.Spec.Suspend
		}, framework.Timeout, framework.Interval).Should(gomega.BeTrue())
		gomega.Expect(createdJob1.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor))

		// TODO(#8): uncomment the following once we have proper re-queueing.
		// ginkgo.By("checking a second job without toleration doesn't start")
		// job2 := testing.MakeJob("on-demand-job2", namespace).Queue(queue.Name).Request(corev1.ResourceCPU, "5").Obj()
		// gomega.Expect(k8sClient.Create(ctx, job2)).Should(gomega.Succeed())
		// createdJob2 := &batchv1.Job{}
		// gomega.Consistently(func() bool {
		// 	lookupKey := types.NamespacedName{Name: job2.Name, Namespace: job2.Namespace}
		// 	return k8sClient.Get(ctx, lookupKey, createdJob2) == nil && *createdJob2.Spec.Suspend
		// }, consistentDuration, interval).Should(gomega.BeTrue())

		ginkgo.By("checking a third job with toleration starts")
		job3 := testing.MakeJob("spot-job3", ns.Name).Queue(prodQueue.Name).
			Toleration(corev1.Toleration{
				Key:      instanceKey,
				Operator: corev1.TolerationOpEqual,
				Value:    spotFlavor,
				Effect:   corev1.TaintEffectNoSchedule,
			}).
			Request(corev1.ResourceCPU, "5").Obj()
		gomega.Expect(k8sClient.Create(ctx, job3)).Should(gomega.Succeed())
		createdJob3 := &batchv1.Job{}
		gomega.Eventually(func() bool {
			lookupKey := types.NamespacedName{Name: job3.Name, Namespace: job3.Namespace}
			return k8sClient.Get(ctx, lookupKey, createdJob3) == nil && !*createdJob3.Spec.Suspend
		}, framework.Timeout, framework.Interval).Should(gomega.BeTrue())
		gomega.Expect(createdJob3.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(spotFlavor))
	})

	ginkgo.It("Should schedule jobs using borrowed capacity", func() {
		ginkgo.By("checking a no-fit job does not start")
		job := testing.MakeJob("job", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "10").Obj()
		gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
		lookupKey := types.NamespacedName{Name: job.Name, Namespace: job.Namespace}
		createdJob := &batchv1.Job{}
		gomega.Consistently(func() bool {
			return k8sClient.Get(ctx, lookupKey, createdJob) == nil && *createdJob.Spec.Suspend
		}, framework.ConsistentDuration, framework.Interval).Should(gomega.BeTrue())

		ginkgo.By("checking the job starts when a fallback capacity gets added")
		fallbackCapacity := testing.MakeClusterQueue("fallback-cq").
			Cohort(prodClusterQ.Spec.Cohort).
			Resource(testing.MakeResource(corev1.ResourceCPU).
				Flavor(testing.MakeFlavor(onDemandFlavor, "5").Ceiling("10").Label(instanceKey, onDemandFlavor).Obj()).
				Obj()).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, fallbackCapacity)).Should(gomega.Succeed())
		gomega.Eventually(func() bool {
			return k8sClient.Get(ctx, lookupKey, createdJob) == nil && !*createdJob.Spec.Suspend
		}, framework.Timeout, framework.Interval).Should(gomega.BeTrue())
		gomega.Expect(createdJob.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor))
	})

	ginkgo.It("Should schedule jobs with affinity to specific flavor", func() {
		ginkgo.By("checking a job without affinity starts on the first flavor")
		job1 := testing.MakeJob("no-affinity-job", ns.Name).Queue(devQueue.Name).Request(corev1.ResourceCPU, "1").Obj()
		gomega.Expect(k8sClient.Create(ctx, job1)).Should(gomega.Succeed())
		createdJob1 := &batchv1.Job{}
		gomega.Eventually(func() bool {
			lookupKey := types.NamespacedName{Name: job1.Name, Namespace: job1.Namespace}
			return k8sClient.Get(ctx, lookupKey, createdJob1) == nil && !*createdJob1.Spec.Suspend
		}, framework.Timeout, framework.Interval).Should(gomega.BeTrue())
		gomega.Expect(createdJob1.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(spotFlavor))

		ginkgo.By("checking a second job with affinity to on-demand")
		job2 := testing.MakeJob("affinity-job", ns.Name).Queue(devQueue.Name).
			NodeSelector(instanceKey, onDemandFlavor).
			NodeSelector("foo", "bar").
			Request(corev1.ResourceCPU, "1").Obj()
		gomega.Expect(k8sClient.Create(ctx, job2)).Should(gomega.Succeed())
		createdJob2 := &batchv1.Job{}
		gomega.Eventually(func() bool {
			lookupKey := types.NamespacedName{Name: job2.Name, Namespace: job2.Namespace}
			return k8sClient.Get(ctx, lookupKey, createdJob2) == nil && !*createdJob2.Spec.Suspend
		}, framework.Timeout, framework.Interval).Should(gomega.BeTrue())
		gomega.Expect(len(createdJob2.Spec.Template.Spec.NodeSelector)).Should(gomega.Equal(2))
		gomega.Expect(createdJob2.Spec.Template.Spec.NodeSelector[instanceKey]).Should(gomega.Equal(onDemandFlavor))
	})
})
