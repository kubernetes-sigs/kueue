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
	"sigs.k8s.io/controller-runtime/pkg/manager"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/constants"
	controllerconsts "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/deployment"
	podcontroller "sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	testingdeployment "sigs.k8s.io/kueue/pkg/util/testingjobs/deployment"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/webhooks"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

// deploymentJobUIDManagerSetup enables the Deployment integration, which the shared
// managerSetup does not, so that the ancestor walk can resolve a Deployment.
func deploymentJobUIDManagerSetup(opts ...jobframework.Option) framework.ManagerSetup {
	return func(ctx context.Context, mgr manager.Manager) {
		jobframework.EnableIntegration(deployment.FrameworkName)

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

var _ = ginkgo.Describe("Pod controller with DeploymentJobUIDLabel", ginkgo.Label("job:pod", "area:jobs"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, deploymentJobUIDManagerSetup(
			jobframework.WithManageJobsWithoutQueueName(false),
			jobframework.WithKubeServerVersion(serverVersionFetcher),
		))
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	var ns *corev1.Namespace

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "pod-deployment-uid-")
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	// envtest runs no controller-manager, so the ownership chain a Deployment would
	// normally produce is created here.
	createOwnedPod := func(queueOnDeployment bool) (*appsv1.Deployment, *corev1.Pod) {
		ginkgo.GinkgoHelper()
		depWrapper := testingdeployment.MakeDeployment("dep", ns.Name)
		if queueOnDeployment {
			depWrapper = depWrapper.Queue("lq")
		}
		dep := depWrapper.Obj()
		util.MustCreate(ctx, k8sClient, dep)

		rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
			Name:      "dep-abc123",
			Namespace: ns.Name,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: appsv1.SchemeGroupVersion.String(),
				Kind:       "Deployment",
				Name:       dep.Name,
				UID:        dep.UID,
				Controller: new(true),
			}},
		}, Spec: appsv1.ReplicaSetSpec{
			Selector: dep.Spec.Selector,
			Template: dep.Spec.Template,
		}}
		util.MustCreate(ctx, k8sClient, rs)

		// The Deployment webhook puts these on the pod template; envtest has no
		// Deployment controller to propagate them.
		pod := testingpod.MakePod("dep-abc123-pod", ns.Name).
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
		return dep, pod
	}

	ginkgo.It("should label the workload with the owning Deployment UID", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.DeploymentJobUIDLabel, true)

		dep, pod := createOwnedPod(true)

		ginkgo.By("checking the workload carries the Deployment UID rather than the Pod UID")
		wlLookupKey := types.NamespacedName{Name: podcontroller.GetWorkloadNameForPod(pod.Name, pod.UID), Namespace: ns.Name}
		createdWorkload := &kueue.Workload{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
			g.Expect(createdWorkload.Labels).To(gomega.HaveKeyWithValue(controllerconsts.JobUIDLabel, string(dep.UID)))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("checking the Pod itself was not modified")
		createdPod := &corev1.Pod{}
		gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pod.Name, Namespace: ns.Name}, createdPod)).To(gomega.Succeed())
		gomega.Expect(createdPod.Labels).NotTo(gomega.HaveKey(controllerconsts.JobUIDLabel))
		gomega.Expect(createdPod.Annotations).To(gomega.HaveKeyWithValue(podconstants.SuspendedByParentAnnotation, deployment.FrameworkName))
	})

	ginkgo.It("should keep the Pod UID when the feature gate is disabled", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.DeploymentJobUIDLabel, false)

		_, pod := createOwnedPod(true)

		wlLookupKey := types.NamespacedName{Name: podcontroller.GetWorkloadNameForPod(pod.Name, pod.UID), Namespace: ns.Name}
		createdWorkload := &kueue.Workload{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
			g.Expect(createdWorkload.Labels).To(gomega.HaveKeyWithValue(controllerconsts.JobUIDLabel, string(pod.UID)))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("should keep the Pod UID when only the Pod carries the queue-name", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.DeploymentJobUIDLabel, true)

		// Kueue does not manage the Deployment, so the Pod is managed standalone.
		_, pod := createOwnedPod(false)

		wlLookupKey := types.NamespacedName{Name: podcontroller.GetWorkloadNameForPod(pod.Name, pod.UID), Namespace: ns.Name}
		createdWorkload := &kueue.Workload{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
			g.Expect(createdWorkload.Labels).To(gomega.HaveKeyWithValue(controllerconsts.JobUIDLabel, string(pod.UID)))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})
})
