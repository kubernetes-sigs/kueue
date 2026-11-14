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
	"os/exec"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/e2e"
)

var _ = ginkgo.Describe("ElasticJobUngater", ginkgo.Label("feature:job", e2e.Shard1), ginkgo.Ordered, func() {
	var (
		ns            *corev1.Namespace
		flavor        *kueue.ResourceFlavor
		clusterQueue  *kueue.ClusterQueue
		localQueue    *kueue.LocalQueue
		nodeToRestore *corev1.Node
	)

	ginkgo.BeforeAll(func() {
		e2e.UpdateKueueConfigurationAndRestart(ctx, k8sClient, defaultKueueCfg, kindClusterName, func(cfg *configapi.Configuration) {
			cfg.FeatureGates = map[string]bool{
				string(features.ElasticJobsViaWorkloadSlices): true,
				string(features.FailureRecoveryPolicy):        false,
			}
		})
	})
	ginkgo.AfterAll(func() {
		e2e.UpdateKueueConfigurationAndRestart(ctx, k8sClient, defaultKueueCfg, kindClusterName)
	})

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "elastic-ungater-")
		flavor = utiltestingapi.MakeResourceFlavor("flavor-" + ns.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, flavor)
		clusterQueue = utiltestingapi.MakeClusterQueue("cq-" + ns.Name).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).
				Resource(corev1.ResourceCPU, "1").
				Resource(corev1.ResourceMemory, "200Mi").
				Obj()).
			Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)
		localQueue = utiltestingapi.MakeLocalQueue("main", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
	})
	ginkgo.AfterEach(func() {
		if nodeToRestore != nil {
			ginkgo.By("restarting the kubelet and restoring node scheduling")
			output, err := exec.Command("docker", "exec", nodeToRestore.Name, "systemctl", "start", "kubelet").CombinedOutput()
			gomega.Expect(err).To(gomega.Succeed(), string(output))
			gomega.Eventually(func(g gomega.Gomega) {
				node := &corev1.Node{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(nodeToRestore), node)).To(gomega.Succeed())
				node.Spec.Unschedulable = nodeToRestore.Spec.Unschedulable
				g.Expect(k8sClient.Update(ctx, node)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			e2e.ExpectNodeToBecomeReady(ctx, k8sClient, nodeToRestore.Name, localQueue)
		}
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
	})

	ginkgo.It("should start a replacement while the previous Pod is stuck deleting during a kubelet outage", func() {
		job := testingjob.MakeJob("elastic-job", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			Image(e2e.GetAgnHostImage(), e2e.BehaviorWaitForDeletion).
			RequestAndLimit(corev1.ResourceCPU, "100m").
			RequestAndLimit(corev1.ResourceMemory, "20Mi").
			Parallelism(1).
			Completions(1).
			TerminationGracePeriod(1).
			PodReplacementPolicy(new(batchv1.TerminatingOrFailed)).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, job)
		var workloadKey client.ObjectKey
		ginkgo.By("waiting for the elastic Workload slice to be admitted")
		gomega.Eventually(func(g gomega.Gomega) {
			wls := &kueue.WorkloadList{}
			g.Expect(k8sClient.List(ctx, wls, client.InNamespace(ns.Name),
				client.MatchingLabels{constants.JobUIDLabel: string(job.UID)})).To(gomega.Succeed())
			g.Expect(wls.Items).To(gomega.HaveLen(1))
			g.Expect(workloadslicing.IsElasticWorkload(&wls.Items[0])).To(gomega.BeTrue())
			g.Expect(workload.IsAdmitted(&wls.Items[0])).To(gomega.BeTrue())
			workloadKey = client.ObjectKeyFromObject(&wls.Items[0])
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		var originalPod corev1.Pod
		ginkgo.By("waiting for the admitted Pod to run")
		gomega.Eventually(func(g gomega.Gomega) {
			pods := &corev1.PodList{}
			g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name),
				client.MatchingLabels{batchv1.JobNameLabel: job.Name})).To(gomega.Succeed())
			g.Expect(pods.Items).To(gomega.HaveLen(1))
			originalPod = pods.Items[0]
			g.Expect(originalPod.Status.Phase).To(gomega.Equal(corev1.PodRunning))
			g.Expect(utilpod.HasGate(&originalPod, kueue.ElasticJobSchedulingGate)).To(gomega.BeFalse())
		}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())

		ginkgo.By("cordoning the worker so the replacement must use another node")
		nodeToRestore = &corev1.Node{}
		gomega.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: originalPod.Spec.NodeName}, nodeToRestore)).To(gomega.Succeed())
		gomega.Expect(nodeToRestore.Labels).NotTo(gomega.HaveKey("node-role.kubernetes.io/control-plane"))
		gomega.Eventually(func(g gomega.Gomega) {
			node := &corev1.Node{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(nodeToRestore), node)).To(gomega.Succeed())
			node.Spec.Unschedulable = true
			g.Expect(k8sClient.Update(ctx, node)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		ginkgo.By("stopping the kubelet before deleting the running Pod")
		output, err := exec.Command("docker", "exec", nodeToRestore.Name, "systemctl", "stop", "kubelet").CombinedOutput()
		gomega.Expect(err).To(gomega.Succeed(), string(output))
		gomega.Expect(k8sClient.Delete(ctx, &originalPod)).To(gomega.Succeed())

		ginkgo.By("waiting for the Job controller's replacement to run while the old Pod remains non-terminal and deleting")
		gomega.Eventually(func(g gomega.Gomega) {
			oldPod := &corev1.Pod{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&originalPod), oldPod)).To(gomega.Succeed())
			g.Expect(oldPod.DeletionTimestamp.IsZero()).To(gomega.BeFalse())
			g.Expect(oldPod.Status.Phase).To(gomega.Equal(corev1.PodRunning))

			pods := &corev1.PodList{}
			g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name),
				client.MatchingLabels{batchv1.JobNameLabel: job.Name})).To(gomega.Succeed())
			g.Expect(pods.Items).To(gomega.HaveLen(2))
			g.Expect(pods.Items).To(gomega.ContainElement(gomega.HaveField("UID", originalPod.UID)))
			for i := range pods.Items {
				pod := &pods.Items[i]
				if pod.UID == originalPod.UID {
					continue
				}
				g.Expect(pod.DeletionTimestamp.IsZero()).To(gomega.BeTrue())
				g.Expect(utilpod.HasGate(pod, kueue.ElasticJobSchedulingGate)).To(gomega.BeFalse())
				g.Expect(pod.Status.Phase).To(gomega.Equal(corev1.PodRunning))
				g.Expect(pod.Spec.NodeName).NotTo(gomega.Equal(originalPod.Spec.NodeName))
			}

			wl := &kueue.Workload{}
			g.Expect(k8sClient.Get(ctx, workloadKey, wl)).To(gomega.Succeed())
			g.Expect(workload.IsAdmitted(wl)).To(gomega.BeTrue())
			g.Expect(workload.ExtractGrantedPodSetCounts(wl)).To(gomega.Equal(
				workload.PodSetsCounts{kueue.DefaultPodSetName: 1},
			))
		}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
	})
})
