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

package job

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	pkgconstants "sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	testingmetrics "sigs.k8s.io/kueue/pkg/util/testing/metrics"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

// The ungater gets these labels through SetupWithManager, not the shared option, so it needs its own test.
var _ = ginkgo.Describe("ElasticJobUngater with ClusterQueue custom metric labels",
	ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
		var (
			ns             *corev1.Namespace
			resourceFlavor *kueue.ResourceFlavor
			clusterQueue   *kueue.ClusterQueue
			localQueue     *kueue.LocalQueue
		)

		ginkgo.BeforeAll(func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlices, true)
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.CustomMetricLabels, true)
			configuration := &configapi.Configuration{}
			configuration.Metrics.CustomLabels = []configapi.ControllerMetricsCustomLabel{
				{Name: "team", SourceLabelKey: "team", SourceKind: new(configapi.SourceKindClusterQueue)},
			}
			fwk.StartManager(ctx, cfg, managerAndControllersSetup(false, true, configuration))
		})

		ginkgo.AfterAll(func() {
			fwk.StopManager(ctx)
			metrics.InitMetricVectors(nil)
		})

		ginkgo.BeforeEach(func() {
			ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "elastic-custom-metric-labels-")

			resourceFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
			util.MustCreate(ctx, k8sClient, resourceFlavor)

			clusterQueue = utiltestingapi.MakeClusterQueue("cq-elastic-custom-metric-labels").
				Label("team", "platform").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(resourceFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, clusterQueue)

			localQueue = utiltestingapi.MakeLocalQueue("lq", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			util.MustCreate(ctx, k8sClient, localQueue)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, resourceFlavor, true)
		})

		ginkgo.It("should remove the gate and record it with the ClusterQueue's custom label", framework.SlowSpec, func() {
			testJob := testingjob.MakeJob("elastic-custom-metric-labels", ns.Name).
				SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				Request(corev1.ResourceCPU, "1").
				Parallelism(1).
				Completions(1).
				Obj()
			util.MustCreate(ctx, k8sClient, testJob)

			var (
				slice  *kueue.Workload
				podSet kueue.PodSetReference
			)
			ginkgo.By("waiting for the slice to be admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					workloads := &kueue.WorkloadList{}
					g.Expect(k8sClient.List(ctx, workloads, client.InNamespace(ns.Name))).Should(gomega.Succeed())
					g.Expect(workloads.Items).Should(gomega.HaveLen(1))
					slice = &workloads.Items[0]
					g.Expect(workload.IsAdmitted(slice)).Should(gomega.BeTrue())
					podSet = slice.Spec.PodSets[0].Name
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			pod := testingpod.MakePod("elastic-pod", ns.Name).
				Annotation(kueue.WorkloadAnnotation, slice.Name).
				Annotation(kueue.WorkloadSliceNameAnnotation, slice.Name).
				Label(pkgconstants.PodSetLabel, string(podSet)).
				Gate(kueue.ElasticJobSchedulingGate).
				Obj()
			ginkgo.By("creating a gated pod for the admitted slice", func() {
				util.MustCreate(ctx, k8sClient, pod)
			})

			ginkgo.By("waiting for the ungater to remove the gate", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), pod)).Should(gomega.Succeed())
					g.Expect(utilpod.HasGate(pod, kueue.ElasticJobSchedulingGate)).Should(gomega.BeFalse())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("checking the observation carries the ClusterQueue's label value", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					got := testingmetrics.CollectFilteredGaugeVec(metrics.PodSchedulingGateRemovalSeconds, map[string]string{
						"name":          kueue.ElasticJobSchedulingGate,
						"cluster_queue": clusterQueue.Name,
						"is_group":      "false",
						"custom_team":   "platform",
					})
					g.Expect(got).Should(gomega.HaveLen(1))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
