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

package tas

import (
	"strings"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjobspod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("TAS Domain Usage Metrics", ginkgo.Label("area:tas", "feature:metrics"), func() {
	var (
		ns *corev1.Namespace

		metricsReaderClusterRoleBinding *rbacv1.ClusterRoleBinding
		curlContainerName               string
		curlPod                         *corev1.Pod
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-tas-metrics-")

		kueueNS := util.GetKueueNamespace()

		metricsReaderClusterRoleBinding = &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "tas-metrics-reader-" + ns.Name},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      "kueue-controller-manager",
					Namespace: kueueNS,
				},
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName,
				Kind:     "ClusterRole",
				Name:     "kueue-metrics-reader",
			},
		}
		util.MustCreate(ctx, k8sClient, metricsReaderClusterRoleBinding)

		curlPod = testingjobspod.MakePod("curl-metrics-"+ns.Name, kueueNS).
			ServiceAccountName("kueue-controller-manager").
			Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
			TerminationGracePeriod(1).
			Obj()
		util.MustCreate(ctx, k8sClient, curlPod)

		ginkgo.By("Waiting for the curl pod to be running", func() {
			util.WaitForPodRunning(ctx, k8sClient, curlPod)
		})
		curlContainerName = curlPod.Spec.Containers[0].Name
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, metricsReaderClusterRoleBinding, true)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, curlPod, true, util.MediumTimeout)
	})

	ginkgo.When("TAS workloads are admitted across multiple domains", func() {
		var (
			topology     *kueue.Topology
			tasFlavor    *kueue.ResourceFlavor
			clusterQueue *kueue.ClusterQueue
			localQueue   *kueue.LocalQueue
			wl1          *kueue.Workload
			wl2          *kueue.Workload
		)

		ginkgo.BeforeEach(func() {
			topology = utiltestingapi.MakeDefaultOneLevelTopology("tas-metrics-topology-" + ns.Name)
			util.MustCreate(ctx, k8sClient, topology)

			tasFlavor = utiltestingapi.MakeResourceFlavor("tas-metrics-flavor-"+ns.Name).
				NodeLabel(tasNodeGroupLabel, instanceType).
				TopologyName(topology.Name).
				Obj()
			util.MustCreate(ctx, k8sClient, tasFlavor)

			clusterQueue = utiltestingapi.MakeClusterQueue("").
				GeneratedName("tas-metrics-cq-").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).
						Resource(corev1.ResourceCPU, "4").
						Resource(extraResource, "8").
						Obj(),
				).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

			localQueue = utiltestingapi.MakeLocalQueue("", ns.Name).
				GeneratedName("tas-metrics-lq-").
				ClusterQueue(clusterQueue.Name).
				Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)

			wl1 = utiltestingapi.MakeWorkload("tas-metrics-wl1", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceName(extraResource), "1").
					RequiredTopologyRequest(corev1.LabelHostname).
					Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, wl1)

			wl2 = utiltestingapi.MakeWorkload("tas-metrics-wl2", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceName(extraResource), "1").
					RequiredTopologyRequest(corev1.LabelHostname).
					Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, wl2)
		})

		ginkgo.AfterEach(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, wl1, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, wl2, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		})

		ginkgo.It("should report kueue_tas_domain_usage per assigned domain and clear on deletion", func() {
			var admittedWL1, admittedWL2 kueue.Workload

			ginkgo.By("waiting for both workloads to be admitted", func() {
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1, wl2)

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), &admittedWL1)).To(gomega.Succeed())
					g.Expect(admittedWL1.Status.Admission).ShouldNot(gomega.BeNil())
					g.Expect(admittedWL1.Status.Admission.PodSetAssignments[0].TopologyAssignment).ShouldNot(gomega.BeNil())

					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), &admittedWL2)).To(gomega.Succeed())
					g.Expect(admittedWL2.Status.Admission).ShouldNot(gomega.BeNil())
					g.Expect(admittedWL2.Status.Admission.PodSetAssignments[0].TopologyAssignment).ShouldNot(gomega.BeNil())
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			// Read the assigned node hostnames from each workload's TopologyAssignment
			// so the metric assertions use the actual domain_id values.
			domainIDsFromWL := func(wl *kueue.Workload) []string {
				ta := utiltas.InternalFrom(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment)
				seen := map[string]bool{}
				var ids []string
				for _, d := range ta.Domains {
					id := strings.Join(d.Values, ",")
					if !seen[id] {
						seen[id] = true
						ids = append(ids, id)
					}
				}
				return ids
			}

			wl1DomainIDs := domainIDsFromWL(&admittedWL1)
			wl2DomainIDs := domainIDsFromWL(&admittedWL2)

			allDomainIDs := map[string]bool{}
			for _, id := range append(wl1DomainIDs, wl2DomainIDs...) {
				allDomainIDs[id] = true
			}

			ginkgo.By("verifying kueue_tas_domain_usage appears for every assigned domain", func() {
				var expectedMetrics [][]string
				for domainID := range allDomainIDs {
					expectedMetrics = append(expectedMetrics, []string{
						"kueue_tas_domain_usage",
						tasFlavor.Name,
						`"` + corev1.LabelHostname + `"`,
						`"` + domainID + `"`,
						`"` + extraResource + `"`,
					})
				}
				util.ExpectMetricsToBeAvailable(ctx, cfg, restClient, curlPod.Name, curlContainerName, expectedMetrics)
			})

			ginkgo.By("deleting wl1 and verifying its exclusive domains drop to zero", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, wl1, true)

				wl2IDs := map[string]bool{}
				for _, id := range wl2DomainIDs {
					wl2IDs[id] = true
				}

				var zeroMetrics [][]string
				for _, id := range wl1DomainIDs {
					if !wl2IDs[id] {
						zeroMetrics = append(zeroMetrics, []string{
							"kueue_tas_domain_usage",
							tasFlavor.Name,
							`"` + corev1.LabelHostname + `"`,
							`"` + id + `"`,
							`"` + extraResource + `"`,
							" 0",
						})
					}
				}
				if len(zeroMetrics) > 0 {
					util.ExpectMetricsToBeAvailable(ctx, cfg, restClient, curlPod.Name, curlContainerName, zeroMetrics)
				}
			})

			ginkgo.By("deleting wl2 and verifying all domains drop to zero", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, wl2, true)

				var zeroMetrics [][]string
				for domainID := range allDomainIDs {
					zeroMetrics = append(zeroMetrics, []string{
						"kueue_tas_domain_usage",
						tasFlavor.Name,
						`"` + corev1.LabelHostname + `"`,
						`"` + domainID + `"`,
						`"` + extraResource + `"`,
						" 0",
					})
				}
				util.ExpectMetricsToBeAvailable(ctx, cfg, restClient, curlPod.Name, curlContainerName, zeroMetrics)
			})
		})
	})
})
