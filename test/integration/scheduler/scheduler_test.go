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
	"time"

	"github.com/onsi/ginkgo"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/kueue/pkg/util/testing"
)

// +kubebuilder:docs-gen:collapse=Imports

var _ = ginkgo.Describe("Scheduler", func() {
	const (
		namespace = "default"
		labelKey  = "cloud.provider.com/instance"

		timeout            = time.Second * 10
		consistentDuration = time.Second * 3
		interval           = time.Millisecond * 250
		onDemandFlavor     = "on-demand"
		spotFlavor         = "spot"
	)

	ginkgo.It("Should schedule jobs as they fit in their capacities", func() {
		ginkgo.By("creating capacities and queues")
		prodCapacity := testing.MakeCapacity("prod-capacity").
			Resource(testing.MakeResource(corev1.ResourceCPU).
				Flavor(testing.MakeFlavor(onDemandFlavor, "5").Label("cloud.provider.com/instance", onDemandFlavor).Obj()).
				Obj()).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, prodCapacity)).Should(gomega.Succeed())
		prodQueue := testing.MakeQueue("prod-queue", namespace).Capacity(prodCapacity.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, prodQueue)).Should(gomega.Succeed())

		devCapacity := testing.MakeCapacity("dev-capacity").
			Resource(testing.MakeResource(corev1.ResourceCPU).
				Flavor(testing.MakeFlavor(spotFlavor, "5").Label("cloud.provider.com/instance", spotFlavor).Obj()).
				Obj()).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, devCapacity)).Should(gomega.Succeed())
		devQueue := testing.MakeQueue("dev-queue", namespace).Capacity(devCapacity.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, devQueue)).Should(gomega.Succeed())

		ginkgo.By("checking the first prod job starts")
		prodJob1 := testing.MakeJob("prod-job1", namespace).Queue(prodQueue.Name).AddResource(corev1.ResourceCPU, "2").Obj()
		gomega.Expect(k8sClient.Create(ctx, prodJob1)).Should(gomega.Succeed())
		lookupKey1 := types.NamespacedName{Name: prodJob1.Name, Namespace: prodJob1.Namespace}
		createdProdJob1 := &batchv1.Job{}
		gomega.Eventually(func() bool {
			err := k8sClient.Get(ctx, lookupKey1, createdProdJob1)
			return err == nil && !*createdProdJob1.Spec.Suspend
		}, timeout, interval).Should(gomega.BeTrue())
		gomega.Expect(createdProdJob1.Spec.Template.Spec.NodeSelector[labelKey]).Should(gomega.Equal(onDemandFlavor))

		ginkgo.By("checking a second no-fit prod job does not start")
		prodJob2 := testing.MakeJob("prod-job2", namespace).Queue(prodQueue.Name).AddResource(corev1.ResourceCPU, "5").Obj()
		gomega.Expect(k8sClient.Create(ctx, prodJob2)).Should(gomega.Succeed())
		lookupKey2 := types.NamespacedName{Name: prodJob2.Name, Namespace: prodJob2.Namespace}
		createdProdJob2 := &batchv1.Job{}
		gomega.Consistently(func() bool {
			return k8sClient.Get(ctx, lookupKey2, createdProdJob2) == nil && *createdProdJob2.Spec.Suspend
		}, consistentDuration, interval).Should(gomega.BeTrue())

		ginkgo.By("checking a dev job starts")
		devJob := testing.MakeJob("dev-job", namespace).Queue(devQueue.Name).AddResource(corev1.ResourceCPU, "5").Obj()
		gomega.Expect(k8sClient.Create(ctx, devJob)).Should(gomega.Succeed())
		createdDevJob := &batchv1.Job{}
		gomega.Eventually(func() bool {
			key := types.NamespacedName{Name: devJob.Name, Namespace: devJob.Namespace}
			return k8sClient.Get(ctx, key, createdDevJob) == nil && !*createdDevJob.Spec.Suspend
		}, consistentDuration, interval).Should(gomega.BeTrue())
		gomega.Expect(createdDevJob.Spec.Template.Spec.NodeSelector[labelKey]).Should(gomega.Equal(spotFlavor))

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
		}, timeout, interval).Should(gomega.BeTrue())
		gomega.Expect(createdProdJob2.Spec.Template.Spec.NodeSelector[labelKey]).Should(gomega.Equal(onDemandFlavor))
	})

	ginkgo.It("Should schedule jobs using borrowed capacity", func() {
		ginkgo.By("creating primary capacity and queue")
		primaryCapacity := testing.MakeCapacity("primary-capacity").
			Cohort("borrowing-cohort").
			Resource(testing.MakeResource(corev1.ResourceCPU).
				Flavor(testing.MakeFlavor(onDemandFlavor, "5").Ceiling("10").Label("cloud.provider.com/instance", onDemandFlavor).Obj()).
				Obj()).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, primaryCapacity)).Should(gomega.Succeed())
		queue := testing.MakeQueue("queue", namespace).Capacity(primaryCapacity.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, queue)).Should(gomega.Succeed())

		ginkgo.By("checking a no-fit job does not start")
		job := testing.MakeJob("job", namespace).Queue(queue.Name).AddResource(corev1.ResourceCPU, "10").Obj()
		gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
		lookupKey := types.NamespacedName{Name: job.Name, Namespace: job.Namespace}
		createdJob := &batchv1.Job{}
		gomega.Consistently(func() bool {
			return k8sClient.Get(ctx, lookupKey, createdJob) == nil && *createdJob.Spec.Suspend
		}, consistentDuration, interval).Should(gomega.BeTrue())

		ginkgo.By("checking the job starts when a fallback capacity gets added")
		fallbackCapacity := testing.MakeCapacity("fallback-capacity").
			Cohort("borrowing-cohort").
			Resource(testing.MakeResource(corev1.ResourceCPU).
				Flavor(testing.MakeFlavor(onDemandFlavor, "5").Ceiling("10").Label("cloud.provider.com/instance", onDemandFlavor).Obj()).
				Obj()).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, fallbackCapacity)).Should(gomega.Succeed())
		gomega.Eventually(func() bool {
			return k8sClient.Get(ctx, lookupKey, createdJob) == nil && !*createdJob.Spec.Suspend
		}, timeout, interval).Should(gomega.BeTrue())
		gomega.Expect(createdJob.Spec.Template.Spec.NodeSelector[labelKey]).Should(gomega.Equal(onDemandFlavor))
	})
})
