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

package jobs

import (
	kftrainerapi "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"

	"sigs.k8s.io/kueue/pkg/constants"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	workloadtrainjob "sigs.k8s.io/kueue/pkg/controller/jobs/trainjob"
	testingjobset "sigs.k8s.io/kueue/pkg/util/testingjobs/jobset"
	testingtrainjob "sigs.k8s.io/kueue/pkg/util/testingjobs/trainjob"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Trainjob Webhook", func() {
	var ns *corev1.Namespace

	ginkgo.When("with manageJobsWithoutQueueName disabled", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
		ginkgo.BeforeAll(func() {
			fwk.StartManager(ctx, cfg, managerSetup(func(mgr ctrl.Manager, opts ...jobframework.Option) error {
				// Necessary to initialize the runtimes
				if _, err := workloadtrainjob.NewReconciler(
					ctx,
					mgr.GetClient(),
					mgr.GetFieldIndexer(),
					mgr.GetEventRecorderFor(constants.JobControllerName),
					opts...); err != nil {
					return err
				}
				return workloadtrainjob.SetupTrainJobWebhook(mgr, opts...)
			}))
		})
		ginkgo.BeforeEach(func() {
			ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "trainjob-")
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		})
		ginkgo.AfterAll(func() {
			fwk.StopManager(ctx)
		})

		ginkgo.It("should succed creating the TrainJob", func() {
			testJobSet := testingjobset.MakeJobSet("", "").ReplicatedJobs(
				testingjobset.ReplicatedJobRequirements{
					Name:     "node",
					Replicas: 1,
				}).
				Obj()
			testTr := testingtrainjob.MakeTrainingRuntime("test", ns.Name, testJobSet.Spec)
			trainJob := testingtrainjob.MakeTrainJob("trainjob-test", ns.Name).RuntimeRef(kftrainerapi.RuntimeRef{
				APIGroup: ptr.To(kftrainerapi.GroupVersion.Group),
				Name:     "test",
				Kind:     ptr.To(kftrainerapi.TrainingRuntimeKind),
			}).
				Queue("queue").
				Suspend(false).
				Obj()

			ginkgo.By("by creating the TrainJob", func() {
				util.MustCreate(ctx, k8sClient, testTr)
				util.MustCreate(ctx, k8sClient, trainJob)
			})

			ginkgo.By("suspending it and setting the child jobset labels", func() {
				createdTrainJob := kftrainerapi.TrainJob{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: trainJob.Name, Namespace: ns.Name}, &createdTrainJob)).Should(gomega.Succeed())
					g.Expect(ptr.Deref(createdTrainJob.Spec.Suspend, false)).Should(gomega.BeTrue())
					g.Expect(createdTrainJob.Spec.Labels).To(gomega.HaveKeyWithValue(controllerconstants.QueueLabel, "queue"))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should not suspend a TrainJob without queue", func() {
			testJobSet := testingjobset.MakeJobSet("", "").ReplicatedJobs(
				testingjobset.ReplicatedJobRequirements{
					Name:     "node",
					Replicas: 1,
				}).
				Obj()
			testTr := testingtrainjob.MakeTrainingRuntime("test", ns.Name, testJobSet.Spec)
			trainJob := testingtrainjob.MakeTrainJob("trainjob-test", ns.Name).RuntimeRef(kftrainerapi.RuntimeRef{
				APIGroup: ptr.To(kftrainerapi.GroupVersion.Group),
				Name:     "test",
				Kind:     ptr.To(kftrainerapi.TrainingRuntimeKind),
			}).
				Suspend(false).
				Obj()

			ginkgo.By("by creating the TrainJob", func() {
				util.MustCreate(ctx, k8sClient, testTr)
				util.MustCreate(ctx, k8sClient, trainJob)
			})

			ginkgo.By("and not suspending it", func() {
				createdTrainJob := kftrainerapi.TrainJob{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: trainJob.Name, Namespace: ns.Name}, &createdTrainJob)).Should(gomega.Succeed())
					g.Expect(ptr.Deref(createdTrainJob.Spec.Suspend, false)).Should(gomega.BeFalse())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})
