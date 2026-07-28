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

package baseline

import (
	"os"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	podtesting "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Pod groups", ginkgo.Label("area:singlecluster", "feature:pod"), func() {
	var (
		ns             *corev1.Namespace
		onDemandRF     *kueue.ResourceFlavor
		flavorOnDemand string
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "pod-e2e-")
		flavorOnDemand = "on-demand-" + ns.Name
		onDemandRF = utiltestingapi.MakeResourceFlavor(flavorOnDemand).NodeLabel("instance-type", "on-demand").Obj()
		util.MustCreate(ctx, k8sClient, onDemandRF)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandRF, true)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.When("Custom metric labels enabled", ginkgo.Ordered, func() {
		var (
			defaultKueueCfg  *config.Configuration
			kindClusterName  = os.Getenv("KIND_CLUSTER_NAME")
			cq               *kueue.ClusterQueue
			lq               *kueue.LocalQueue
			clusterQueueName string
		)

		ginkgo.BeforeAll(func() {
			defaultKueueCfg = util.GetKueueConfiguration(ctx, k8sClient)
			util.UpdateKueueConfigurationAndRestart(ctx, k8sClient, defaultKueueCfg, kindClusterName, func(cfg *config.Configuration) {
				cfg.FeatureGates = map[string]bool{
					string(features.CustomMetricLabels): true,
				}
				cfg.Integrations.LabelKeysToCopy = []string{"toCopyKeyIntegration"}
				cfg.Metrics.CustomLabels = []config.ControllerMetricsCustomLabel{
					{
						Name:           "custom_label_key",
						SourceLabelKey: "toCopyKeyCustom",
						SourceKind:     ptr.To(config.SourceKindWorkload),
						TrackedValues:  []string{"custom_value"},
					},
					{
						Name:                "custom_annotation_key",
						SourceAnnotationKey: "toCopyAnnotation",
						SourceKind:          ptr.To(config.SourceKindWorkload),
						TrackedValues:       []string{"annotation_value"},
					},
				}
			})
		})

		ginkgo.AfterAll(func() {
			util.UpdateKueueConfigurationAndRestart(ctx, k8sClient, defaultKueueCfg, kindClusterName)
		})

		ginkgo.BeforeEach(func() {
			clusterQueueName = "cq-" + ns.Name
			cq = utiltestingapi.MakeClusterQueue(clusterQueueName).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(flavorOnDemand).Resource(corev1.ResourceCPU, "5").Obj(),
				).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

			lq = utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteAllPodsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		})

		ginkgo.It("should successfully copy labels and annotations for a single pod", func() {
			testPod := podtesting.MakePod("test-pod", ns.Name).
				Queue(lq.Name).
				Label("toCopyKeyIntegration", "integration_value").
				Label("toCopyKeyCustom", "custom_value").
				Label("dontCopyKey", "ignored").
				Annotation("toCopyAnnotation", "annotation_value").
				Annotation("dontCopyAnnotation", "ignored").
				Obj()
			util.MustCreate(ctx, k8sClient, testPod)

			wlLookupKey := types.NamespacedName{
				Namespace: ns.Name,
				Name:      pod.GetWorkloadNameForPod(testPod.Name, testPod.UID),
			}
			createdWorkload := &kueue.Workload{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Expect(createdWorkload.Labels["toCopyKeyIntegration"]).Should(gomega.Equal("integration_value"))
			gomega.Expect(createdWorkload.Labels["toCopyKeyCustom"]).Should(gomega.Equal("custom_value"))
			gomega.Expect(createdWorkload.Labels).ShouldNot(gomega.HaveKey("dontCopyKey"))
			gomega.Expect(createdWorkload.Annotations).Should(gomega.HaveKeyWithValue("toCopyAnnotation", "annotation_value"))
			gomega.Expect(createdWorkload.Annotations).ShouldNot(gomega.HaveKey("dontCopyAnnotation"))
		})

		ginkgo.It("should successfully copy labels and annotations for a pod group", func() {
			pod1 := podtesting.MakePod("test-pod1", ns.Name).
				GroupNameLabel("test-group").
				GroupTotalCount("2").
				Queue(lq.Name).
				Label("dontCopyKey", "dontCopyValue").
				Annotation("toCopyAnnotation", "annotation_value").
				Annotation("dontCopyAnnotation", "ignored1").
				Obj()
			pod2 := podtesting.MakePod("test-pod2", ns.Name).
				GroupNameLabel("test-group").
				GroupTotalCount("2").
				Queue(lq.Name).
				Label("toCopyKeyIntegration", "integration_value").
				Label("toCopyKeyCustom", "custom_value").
				Label("dontCopyKey", "dontCopyAnotherValue").
				Annotation("toCopyAnnotation", "annotation_value").
				Annotation("dontCopyAnnotation", "ignored2").
				Obj()

			util.MustCreate(ctx, k8sClient, pod1)
			util.MustCreate(ctx, k8sClient, pod2)

			wlLookupKey := types.NamespacedName{
				Namespace: ns.Name,
				Name:      "test-group",
			}
			createdWorkload := &kueue.Workload{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Expect(createdWorkload.Labels["toCopyKeyIntegration"]).Should(gomega.Equal("integration_value"))
			gomega.Expect(createdWorkload.Labels["toCopyKeyCustom"]).Should(gomega.Equal("custom_value"))
			gomega.Expect(createdWorkload.Labels).ShouldNot(gomega.HaveKey("dontCopyKey"))
			gomega.Expect(createdWorkload.Annotations).Should(gomega.HaveKeyWithValue("toCopyAnnotation", "annotation_value"))
			gomega.Expect(createdWorkload.Annotations).ShouldNot(gomega.HaveKey("dontCopyAnnotation"))
		})

		ginkgo.It("should not create workload for a pod group if there is a custom annotation mismatch", func() {
			pod1 := podtesting.MakePod("test-pod1", ns.Name).
				GroupNameLabel("test-group").
				GroupTotalCount("2").
				Queue(lq.Name).
				Annotation("toCopyAnnotation", "annotation_value").
				Obj()
			pod2 := podtesting.MakePod("test-pod2", ns.Name).
				GroupNameLabel("test-group").
				GroupTotalCount("2").
				Queue(lq.Name).
				Annotation("toCopyAnnotation", "mismatch_value").
				Obj()

			util.MustCreate(ctx, k8sClient, pod1)
			util.MustCreate(ctx, k8sClient, pod2)

			wlLookupKey := types.NamespacedName{
				Namespace: ns.Name,
				Name:      "test-group",
			}
			createdWorkload := &kueue.Workload{}
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Satisfy(apierrors.IsNotFound))
			}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
		})
	})
})
