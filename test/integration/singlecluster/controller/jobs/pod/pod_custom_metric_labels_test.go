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
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podcontroller "sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	testingmetrics "sigs.k8s.io/kueue/pkg/util/testing/metrics"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/test/util"
)

// The gate is removed before the metric is recorded, so a regression drops the series.
var _ = ginkgo.Describe("Pod controller with ClusterQueue custom metric labels",
	ginkgo.Label("job:pod", "area:jobs"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
		var (
			defaultFlavor *kueue.ResourceFlavor
			clusterQueue  *kueue.ClusterQueue
			localQueue    *kueue.LocalQueue
			ns            *corev1.Namespace
		)

		ginkgo.BeforeAll(func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.CustomMetricLabels, true)
			configuration := &configapi.Configuration{}
			configuration.Metrics.CustomLabels = []configapi.ControllerMetricsCustomLabel{
				{Name: "team", SourceLabelKey: "team", SourceKind: new(configapi.SourceKindClusterQueue)},
			}
			fwk.StartManager(ctx, cfg, managerSetup(false, false, configuration,
				jobframework.WithEnabledFrameworks([]string{"pod"}),
			))

			defaultFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
			util.MustCreate(ctx, k8sClient, defaultFlavor)
			clusterQueue = utiltestingapi.MakeClusterQueue("cq-custom-metric-labels").
				Label("team", "platform").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(defaultFlavor.Name).Resource(corev1.ResourceCPU, "1").Obj(),
				).Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)
		})

		ginkgo.AfterAll(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, defaultFlavor, true)
			fwk.StopManager(ctx)
			metrics.InitMetricVectors(nil)
		})

		ginkgo.BeforeEach(func() {
			ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "pod-custom-metric-labels-")
			localQueue = utiltestingapi.MakeLocalQueue("lq", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			util.MustCreate(ctx, k8sClient, localQueue)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		})

		ginkgo.It("should remove the gate and record it with the ClusterQueue's custom label", func() {
			pod := testingpod.MakePod(podName, ns.Name).
				Queue(localQueue.Name).
				Request(corev1.ResourceCPU, "1").
				Obj()
			util.MustCreate(ctx, k8sClient, pod)

			ginkgo.By("waiting for the Workload the Pod is admitted through")
			wlKey := types.NamespacedName{
				Name:      podcontroller.GetWorkloadNameForPod(pod.Name, pod.UID),
				Namespace: ns.Name,
			}
			createdWorkload := &kueue.Workload{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, createdWorkload)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("admitting it so the controller removes the gate")
			admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(clusterQueue.Name)).
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(corev1.ResourceCPU, kueue.ResourceFlavorReference(defaultFlavor.Name), "1").
					Count(createdWorkload.Spec.PodSets[0].Count).
					Obj()).
				Obj()
			util.SetQuotaReservation(ctx, k8sClient, wlKey, admission)
			util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)

			createdPod := &corev1.Pod{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), createdPod)).Should(gomega.Succeed())
				g.Expect(createdPod.Spec.SchedulingGates).Should(gomega.BeEmpty())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("checking the observation carries the ClusterQueue's label value")
			gomega.Eventually(func(g gomega.Gomega) {
				got := testingmetrics.CollectFilteredGaugeVec(metrics.PodSchedulingGateRemovalSeconds, map[string]string{
					"cluster_queue": clusterQueue.Name,
					"is_group":      "false",
					"custom_team":   "platform",
				})
				g.Expect(got).Should(gomega.HaveLen(1))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
