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

package pod

import (
	"context"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/constants"
	ctrlconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	jobcontrollers "sigs.k8s.io/kueue/pkg/controller/jobs"
	"sigs.k8s.io/kueue/pkg/controller/jobs/deployment"
	podcontroller "sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingdeployment "sigs.k8s.io/kueue/pkg/util/testingjobs/deployment"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/webhooks"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

func deploymentParentSuspensionManagerSetup(opts ...jobframework.Option) framework.ManagerSetup {
	return func(ctx context.Context, mgr manager.Manager) {
		integrationManager := jobcontrollers.NewIntegrationManager()
		integrationManager.EnableIntegration(deployment.FrameworkName)
		opts = append(opts, jobframework.WithIntegrationManager(integrationManager))

		gomega.Expect(indexer.Setup(ctx, mgr.GetFieldIndexer())).To(gomega.Succeed())
		gomega.Expect(podcontroller.SetupIndexes(ctx, mgr.GetFieldIndexer())).To(gomega.Succeed())

		preemptionExpectations := preemptexpectations.New()
		customLabels := metrics.NewCustomLabels(nil)
		cCache := schdcache.New(mgr.GetClient(), schdcache.WithCustomLabels(customLabels))
		queues := util.NewManagerForIntegrationTests(ctx, mgr.GetClient(), cCache,
			qcache.WithPreemptionExpectations(preemptionExpectations),
			qcache.WithCustomLabels(customLabels),
		)
		opts = append(opts,
			jobframework.WithCache(cCache),
			jobframework.WithQueues(queues),
			jobframework.WithCustomLabels(customLabels),
		)

		podReconciler, err := podcontroller.NewReconciler(
			ctx,
			mgr.GetClient(),
			mgr.GetFieldIndexer(),
			mgr.GetEventRecorder(constants.JobControllerName),
			opts...)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(podReconciler.SetupWithManager(mgr)).To(gomega.Succeed())

		configuration := &config.Configuration{}
		mgr.GetScheme().Default(configuration)
		failedCtrl, err := core.SetupControllers(mgr, queues, cCache, configuration,
			core.SetupControllersOpts{PreemptionExpectations: preemptionExpectations, CustomLabels: customLabels})
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "controller", failedCtrl)

		gomega.Expect(podcontroller.SetupWebhook(mgr, opts...)).To(gomega.Succeed())
		gomega.Expect(deployment.SetupWebhook(mgr, opts...)).To(gomega.Succeed())
		failedWebhook, err := webhooks.Setup(mgr, nil)
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "webhook", failedWebhook)
	}
}

var _ = ginkgo.Describe("Deployment parent suspension", ginkgo.Label("job:pod", "area:jobs"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, deploymentParentSuspensionManagerSetup(
			jobframework.WithManageJobsWithoutQueueName(false),
			jobframework.WithKubeServerVersion(serverVersionFetcher),
		))
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	var (
		ns *corev1.Namespace
		fl *kueue.ResourceFlavor
		cq *kueue.ClusterQueue
		lq *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "pod-deploy-pause-")

		fl = utiltestingapi.MakeResourceFlavor("fl").Obj()
		util.MustCreate(ctx, k8sClient, fl)

		cq = utiltestingapi.MakeClusterQueue("cq").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(fl.Name).
				Resource(corev1.ResourceCPU, "9").
				Obj()).
			Obj()
		util.MustCreate(ctx, k8sClient, cq)

		lq = utiltestingapi.MakeLocalQueue("lq", ns.Name).ClusterQueue(cq.Name).Obj()
		util.MustCreate(ctx, k8sClient, lq)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, fl, true)
	})

	createOwnedPod := func(podName string) (*appsv1.Deployment, *appsv1.ReplicaSet, *corev1.Pod) {
		ginkgo.GinkgoHelper()
		dep := testingdeployment.MakeDeployment("dep", ns.Name).Queue("lq").Obj()
		util.MustCreate(ctx, k8sClient, dep)

		rs := &appsv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "dep-abc123",
				Namespace: ns.Name,
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: appsv1.SchemeGroupVersion.String(),
					Kind:       "Deployment",
					Name:       dep.Name,
					UID:        dep.UID,
					Controller: new(true),
				}},
			},
			Spec: appsv1.ReplicaSetSpec{
				Selector: dep.Spec.Selector,
				Template: dep.Spec.Template,
			},
		}
		util.MustCreate(ctx, k8sClient, rs)

		pod := testingpod.MakePod(podName, ns.Name).
			Queue("lq").
			ManagedByKueueLabel().
			SuspendedByParent(deployment.FrameworkName).
			Image("pause", nil).
			Obj()
		pod.OwnerReferences = []metav1.OwnerReference{{
			APIVersion: appsv1.SchemeGroupVersion.String(),
			Kind:       "ReplicaSet",
			Name:       rs.Name,
			UID:        rs.UID,
			Controller: new(true),
		}}
		util.MustCreate(ctx, k8sClient, pod)
		return dep, rs, pod
	}

	ginkgo.It("should pause the parent Deployment while the pod is scheduling-gated", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.DeploymentParentSuspension, true)

		dep, _, pod := createOwnedPod("dep-pod")

		ginkgo.By("waiting for the pod to be scheduling-gated")
		gomega.Eventually(func(g gomega.Gomega) {
			createdPod := &corev1.Pod{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), createdPod)).To(gomega.Succeed())
			g.Expect(createdPod.Spec.SchedulingGates).To(
				gomega.ContainElement(corev1.PodSchedulingGate{Name: podconstants.SchedulingGateName}),
			)
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("waiting for the workload to be created")
		wlLookupKey := types.NamespacedName{
			Name:      podcontroller.GetWorkloadNameForPod(pod.Name, pod.UID),
			Namespace: ns.Name,
		}
		createdWorkload := &kueue.Workload{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("checking the parent Deployment is paused")
		gomega.Eventually(func(g gomega.Gomega) {
			updatedDep := &appsv1.Deployment{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(dep), updatedDep)).To(gomega.Succeed())
			g.Expect(updatedDep.Spec.Paused).To(gomega.BeTrue(), "Deployment should be paused")
			g.Expect(updatedDep.Annotations).To(gomega.HaveKeyWithValue(
				ctrlconstants.PausedByKueueAnnotation, "true",
			))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("should unpause the parent Deployment after the workload is admitted", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.DeploymentParentSuspension, true)

		dep, _, pod := createOwnedPod("dep-pod")

		ginkgo.By("waiting for the workload to be created")
		wlLookupKey := types.NamespacedName{
			Name:      podcontroller.GetWorkloadNameForPod(pod.Name, pod.UID),
			Namespace: ns.Name,
		}
		createdWorkload := &kueue.Workload{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("waiting for the Deployment to be paused")
		gomega.Eventually(func(g gomega.Gomega) {
			updatedDep := &appsv1.Deployment{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(dep), updatedDep)).To(gomega.Succeed())
			g.Expect(updatedDep.Spec.Paused).To(gomega.BeTrue())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("admitting the workload")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(cq.Name)).
			PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
				Assignment(corev1.ResourceCPU, kueue.ResourceFlavorReference(fl.Name), "1").
				Count(createdWorkload.Spec.PodSets[0].Count).
				Obj()).
			Obj()
		util.SetQuotaReservation(ctx, k8sClient, wlLookupKey, admission)
		util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)

		ginkgo.By("checking the scheduling gate is removed")
		gomega.Eventually(func(g gomega.Gomega) {
			createdPod := &corev1.Pod{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), createdPod)).To(gomega.Succeed())
			g.Expect(createdPod.Spec.SchedulingGates).NotTo(
				gomega.ContainElement(corev1.PodSchedulingGate{Name: podconstants.SchedulingGateName}),
			)
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("checking the parent Deployment is unpaused")
		gomega.Eventually(func(g gomega.Gomega) {
			updatedDep := &appsv1.Deployment{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(dep), updatedDep)).To(gomega.Succeed())
			g.Expect(updatedDep.Spec.Paused).To(gomega.BeFalse(), "Deployment should be unpaused")
			g.Expect(updatedDep.Annotations).NotTo(gomega.HaveKey(ctrlconstants.PausedByKueueAnnotation))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("should not pause the parent Deployment when the feature gate is disabled", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.DeploymentParentSuspension, false)

		dep, _, pod := createOwnedPod("dep-pod")

		ginkgo.By("waiting for the workload to be created")
		wlLookupKey := types.NamespacedName{
			Name:      podcontroller.GetWorkloadNameForPod(pod.Name, pod.UID),
			Namespace: ns.Name,
		}
		createdWorkload := &kueue.Workload{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("checking the Deployment is NOT paused")
		gomega.Consistently(func(g gomega.Gomega) {
			updatedDep := &appsv1.Deployment{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(dep), updatedDep)).To(gomega.Succeed())
			g.Expect(updatedDep.Spec.Paused).To(gomega.BeFalse(), "Deployment should not be paused when gate is off")
		}, util.ConsistentDuration, util.Interval).Should(gomega.Succeed())
	})
})
