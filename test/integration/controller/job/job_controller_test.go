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

package controllers

import (
	"context"
	"time"

	"github.com/onsi/ginkgo"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"

	kueue "sigs.k8s.io/kueue/api/v1alpha1"
	"sigs.k8s.io/kueue/pkg/constants"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/workload/job"
)

const (
	parallelism  = 4
	jobName      = "test-job"
	jobNamespace = "default"
	labelKey     = "cloud.provider.com/instance"

	timeout            = time.Second * 10
	consistentDuration = time.Second * 3
	interval           = time.Millisecond * 250
)

var (
	ctx    context.Context
	cancel context.CancelFunc
)

// +kubebuilder:docs-gen:collapse=Imports

var _ = ginkgo.Describe("Job controller", func() {
	ginkgo.BeforeEach(func() {
		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme: scheme.Scheme,
		})
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "failed to create manager")

		err = workloadjob.NewReconciler(mgr.GetScheme(), mgr.GetClient()).SetupWithManager(mgr)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())

		ctx, cancel = context.WithCancel(context.TODO())
		go func() {
			defer ginkgo.GinkgoRecover()
			err = mgr.Start(ctx)
			gomega.Expect(err).ToNot(gomega.HaveOccurred(), "failed to run manager")
		}()
	})

	ginkgo.AfterEach(func() { cancel() })

	ginkgo.It("Should reconcile workload and job", func() {
		ginkgo.By("checking the job gets suspended when created unsuspended")
		job := newTestJob("")
		gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
		lookupKey := types.NamespacedName{Name: jobName, Namespace: jobNamespace}
		createdJob := &batchv1.Job{}
		gomega.Eventually(func() bool {
			if err := k8sClient.Get(ctx, lookupKey, createdJob); err != nil {
				return false
			}
			return createdJob.Spec.Suspend != nil && *createdJob.Spec.Suspend
		}, timeout, interval).Should(gomega.BeTrue())

		ginkgo.By("checking the workload is created without queue assigned")
		createdWorkload := &kueue.QueuedWorkload{}
		gomega.Eventually(func() bool {
			err := k8sClient.Get(ctx, lookupKey, createdWorkload)
			return err == nil
		}, timeout, interval).Should(gomega.BeTrue())
		gomega.Expect(createdWorkload.Spec.QueueName).Should(gomega.Equal(""))

		ginkgo.By("checking the workload is updated with queue name when the job does")
		jobQueueName := "test-queue"
		createdJob.Annotations = map[string]string{constants.QueueAnnotation: jobQueueName}
		gomega.Expect(k8sClient.Update(ctx, createdJob)).Should(gomega.Succeed())

		gomega.Eventually(func() bool {
			if err := k8sClient.Get(ctx, lookupKey, createdWorkload); err != nil {
				return false
			}
			return createdWorkload.Spec.QueueName == jobQueueName
		}, timeout, interval).Should(gomega.BeTrue())

		ginkgo.By("checking a second non-matching workload is deleted")
		secondWl, _ := workloadjob.ConstructWorkloadFor(createdJob, scheme.Scheme)
		secondWl.Name = "second-workload"
		secondWl.Spec.Pods[0].Count = parallelism + 1
		gomega.Expect(k8sClient.Create(ctx, secondWl)).Should(gomega.Succeed())
		gomega.Eventually(func() bool {
			wl := &kueue.QueuedWorkload{}
			key := types.NamespacedName{Name: secondWl.Name, Namespace: secondWl.Namespace}
			if err := k8sClient.Get(ctx, key, wl); err != nil && apierrors.IsNotFound(err) {
				return true
			}
			return false
		}, timeout, interval).Should(gomega.BeTrue())
		// check the original wl is still there
		gomega.Consistently(func() bool {
			err := k8sClient.Get(ctx, lookupKey, createdWorkload)
			return err == nil
		}, consistentDuration, interval).Should(gomega.BeTrue())

		ginkgo.By("checking the job is unsuspended when workload is assigned")
		capacityName := "capacity"
		gomega.Expect(k8sClient.Create(ctx, newTestCapacity(capacityName))).Should(gomega.Succeed())
		createdWorkload.Spec.AssignedCapacity = kueue.CapacityReference(capacityName)
		createdWorkload.Spec.Pods[0].AssignedFlavors = map[corev1.ResourceName]string{"cpu": "on-demand"}
		gomega.Expect(k8sClient.Update(ctx, createdWorkload)).Should(gomega.Succeed())
		gomega.Eventually(func() bool {
			if err := k8sClient.Get(ctx, lookupKey, createdJob); err != nil {
				return false
			}
			return !*createdJob.Spec.Suspend
		}, timeout, interval).Should(gomega.BeTrue())
		gomega.Expect(len(createdJob.Spec.Template.Spec.NodeSelector)).Should(gomega.Equal(1))
		gomega.Expect(createdJob.Spec.Template.Spec.NodeSelector[labelKey]).Should(gomega.Equal("on-demand"))
		gomega.Consistently(func() bool {
			if err := k8sClient.Get(ctx, lookupKey, createdWorkload); err != nil {
				return false
			}
			return len(createdWorkload.Status.Conditions) == 0
		}, consistentDuration, interval).Should(gomega.BeTrue())

		ginkgo.By("checking the job gets suspended when parallelism changes and the added node selectors are removed")
		newParallelism := int32(parallelism + 1)
		createdJob.Spec.Parallelism = &newParallelism
		gomega.Expect(k8sClient.Update(ctx, createdJob)).Should(gomega.Succeed())
		gomega.Eventually(func() bool {
			if err := k8sClient.Get(ctx, lookupKey, createdJob); err != nil {
				return false
			}
			return createdJob.Spec.Suspend != nil && *createdJob.Spec.Suspend &&
				len(createdJob.Spec.Template.Spec.NodeSelector) == 0
		}, timeout, interval).Should(gomega.BeTrue())

		ginkgo.By("checking the workload is updated with new count")
		gomega.Eventually(func() bool {
			if err := k8sClient.Get(ctx, lookupKey, createdWorkload); err != nil {
				return false
			}
			return createdWorkload.Spec.Pods[0].Count == newParallelism
		}, timeout, interval).Should(gomega.BeTrue())
		gomega.Expect(createdWorkload.Spec.AssignedCapacity).Should(gomega.BeEmpty())

		ginkgo.By("checking the job is unsuspended and selectors added when workload is assigned again")
		createdWorkload.Spec.AssignedCapacity = "capacity"
		createdWorkload.Spec.Pods[0].AssignedFlavors = map[corev1.ResourceName]string{"cpu": "spot"}
		gomega.Expect(k8sClient.Update(ctx, createdWorkload)).Should(gomega.Succeed())
		gomega.Eventually(func() bool {
			if err := k8sClient.Get(ctx, lookupKey, createdJob); err != nil {
				return false
			}
			return !*createdJob.Spec.Suspend
		}, timeout, interval).Should(gomega.BeTrue())
		gomega.Expect(len(createdJob.Spec.Template.Spec.NodeSelector)).Should(gomega.Equal(1))
		gomega.Expect(createdJob.Spec.Template.Spec.NodeSelector[labelKey]).Should(gomega.Equal("spot"))
		gomega.Consistently(func() bool {
			if err := k8sClient.Get(ctx, lookupKey, createdWorkload); err != nil {
				return false
			}
			return len(createdWorkload.Status.Conditions) == 0
		}, consistentDuration, interval).Should(gomega.BeTrue())

		ginkgo.By("checking the workload is finished when job is completed")
		createdJob.Status.Conditions = append(createdJob.Status.Conditions,
			batchv1.JobCondition{
				Type:               batchv1.JobComplete,
				Status:             corev1.ConditionTrue,
				LastProbeTime:      metav1.Now(),
				LastTransitionTime: metav1.Now(),
			})
		gomega.Expect(k8sClient.Status().Update(ctx, createdJob)).Should(gomega.Succeed())
		gomega.Eventually(func() bool {
			err := k8sClient.Get(ctx, lookupKey, createdWorkload)
			if err != nil || len(createdWorkload.Status.Conditions) == 0 {
				return false
			}

			return createdWorkload.Status.Conditions[0].Type == kueue.QueuedWorkloadFinished &&
				createdWorkload.Status.Conditions[0].Status == corev1.ConditionTrue
		}, timeout, interval).Should(gomega.BeTrue())
	})
})

func newTestJob(jobQueueName string) *batchv1.Job {
	annotations := map[string]string{}
	if jobQueueName != "" {
		annotations[constants.QueueAnnotation] = jobQueueName
	}
	p := int32(parallelism)
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:        jobName,
			Namespace:   jobNamespace,
			Annotations: annotations,
		},
		TypeMeta: metav1.TypeMeta{
			Kind: "Job",
		},
		Spec: batchv1.JobSpec{
			Parallelism: &p,
			Completions: &p,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: "Never",
					Containers: []corev1.Container{
						{
							Name:    "c",
							Image:   "pause",
							Command: []string{},
						},
					},
				},
			},
		},
	}
}

func newTestCapacity(name string) *kueue.Capacity {
	return &kueue.Capacity{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: kueue.CapacitySpec{
			RequestableResources: []kueue.Resource{
				{
					Name: "cpu",
					Flavors: []kueue.ResourceFlavor{
						{
							Name: "on-demand",
							Labels: map[string]string{
								"cloud.provider.com/instance": "on-demand",
							},
						},
						{
							Name: "spot",
							Labels: map[string]string{
								"cloud.provider.com/instance": "spot",
							},
						},
					},
				},
			},
		},
	}
}
