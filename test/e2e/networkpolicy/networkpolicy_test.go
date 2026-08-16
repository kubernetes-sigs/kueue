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

package networkpolicy

import (
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/test/util"
)

const (
	metricsPort    = 8443
	connectTimeout = "5s"
)

var _ = ginkgo.Describe("NetworkPolicies", func() {
	const defaultFlavor = "default-flavor"

	var (
		defaultRF    *kueue.ResourceFlavor
		localQueue   *kueue.LocalQueue
		clusterQueue *kueue.ClusterQueue
		ns           *corev1.Namespace
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-netpol-")
		defaultRF = utiltestingapi.MakeResourceFlavor(defaultFlavor).Obj()
		util.MustCreate(ctx, k8sClient, defaultRF)

		clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(defaultFlavor).
					Resource(corev1.ResourceCPU, "1").
					Obj(),
			).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, defaultRF, true)
	})

	ginkgo.It("should have the policies applied to the ControllerManager", func() {
		policies := &networkingv1.NetworkPolicyList{}
		gomega.Expect(k8sClient.List(ctx, policies, client.InNamespace(kueueNS))).To(gomega.Succeed())
		gomega.Expect(policies.Items).NotTo(gomega.BeEmpty())
	})

	ginkgo.It("should still admit a Job, so the webhook stays reachable", func() {
		job := testingjob.MakeJob("job", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Request(corev1.ResourceCPU, "1").
			Obj()

		ginkgo.By("Creating the job, which the mutating webhook has to admit", func() {
			util.MustCreate(ctx, k8sClient, job)
		})

		ginkgo.By("Waiting for the workload to be admitted", func() {
			createdJob := &batchv1.Job{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), createdJob)).To(gomega.Succeed())
				g.Expect(createdJob.Spec.Suspend).To(gomega.Equal(new(false)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

			wlKey := client.ObjectKey{
				Namespace: ns.Name,
				Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
			}
			wl := &kueue.Workload{}
			gomega.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
		})
	})

	ginkgo.It("should serve the visibility API, so the aggregator stays reachable", func() {
		gomega.Eventually(func(g gomega.Gomega) {
			_, err := visibilityClient.ClusterQueues().GetPendingWorkloadsSummary(
				ctx, clusterQueue.Name, metav1.GetOptions{})
			g.Expect(err).NotTo(gomega.HaveOccurred())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("should keep a listed port reachable from another pod", func() {
		managerIP := managerPodIP()

		probe := testingpod.MakePod("netpol-probe", ns.Name).
			Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
			TerminationGracePeriod(1).
			Obj()
		util.MustCreate(ctx, k8sClient, probe)
		util.WaitForPodRunning(ctx, k8sClient, probe)

		container := probe.Spec.Containers[0].Name

		ginkgo.By("Connecting to the metrics port, which the policy allows", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				_, _, err := util.KExecute(ctx, cfg, restClient, ns.Name, probe.Name, container,
					connectCmd(managerIP, metricsPort))
				g.Expect(err).NotTo(gomega.HaveOccurred())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})

func managerPodIP() string {
	pods := &corev1.PodList{}
	gomega.Expect(k8sClient.List(ctx, pods, client.InNamespace(kueueNS),
		client.MatchingLabels{"control-plane": "controller-manager"})).To(gomega.Succeed())
	gomega.Expect(pods.Items).NotTo(gomega.BeEmpty())
	return pods.Items[0].Status.PodIP
}

func connectCmd(host string, port int) []string {
	return []string{"/agnhost", "connect", fmt.Sprintf("%s:%d", host, port), "--timeout=" + connectTimeout}
}
