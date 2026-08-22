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
	managerPolicyName = "kueue-controller-manager-ingress"
	metricsPort       = 8443
	unlistedPort      = 9999
	connectTimeout    = "5s"
)

var managerSelector = map[string]string{"control-plane": "controller-manager"}

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

	ginkgo.It("should have the ControllerManager policy applied", func() {
		policy := &networkingv1.NetworkPolicy{}
		gomega.Expect(k8sClient.Get(ctx, client.ObjectKey{
			Namespace: kueueNS, Name: managerPolicyName,
		}, policy)).To(gomega.Succeed())

		gomega.Expect(policy.Spec.PodSelector.MatchLabels).To(gomega.Equal(managerSelector))
		gomega.Expect(policy.Spec.PolicyTypes).To(gomega.Equal([]networkingv1.PolicyType{networkingv1.PolicyTypeIngress}))

		var allowed []int32
		for _, rule := range policy.Spec.Ingress {
			for _, port := range rule.Ports {
				allowed = append(allowed, port.Port.IntVal)
			}
		}
		gomega.Expect(allowed).To(gomega.ConsistOf(int32(9443), int32(8082), int32(metricsPort), int32(8081)))
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

	ginkgo.It("should deny a port the policy does not list", func() {
		listener := newPolicyCoveredListener()
		util.MustCreate(ctx, k8sClient, listener)
		ginkgo.DeferCleanup(func() {
			gomega.Expect(util.DeleteObject(ctx, k8sClient, listener)).To(gomega.Succeed())
		})
		util.WaitForPodRunning(ctx, k8sClient, listener)

		listenerIP := podIP(listener)

		probe := testingpod.MakePod("netpol-probe", ns.Name).
			Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
			TerminationGracePeriod(1).
			Obj()
		util.MustCreate(ctx, k8sClient, probe)
		util.WaitForPodRunning(ctx, k8sClient, probe)

		container := probe.Spec.Containers[0].Name

		ginkgo.By("Reaching the listed port, which shows the listener is up", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				_, _, err := util.KExecute(ctx, cfg, restClient, ns.Name, probe.Name, container,
					connectCmd(listenerIP, metricsPort))
				g.Expect(err).NotTo(gomega.HaveOccurred())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Failing to reach the unlisted port on the same pod", func() {
			gomega.Consistently(func(g gomega.Gomega) {
				_, _, err := util.KExecute(ctx, cfg, restClient, ns.Name, probe.Name, container,
					connectCmd(listenerIP, unlistedPort))
				g.Expect(err).To(gomega.HaveOccurred())
			}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
		})
	})
})

// The listener carries the label the ControllerManager policy selects on, so the policy
// covers it. It serves one listed and one unlisted port, which keeps the denial scoped to
// the port rather than to whether the pod is up. The failing readiness probe keeps it out
// of the Kueue Services, which select on that same label.
func newPolicyCoveredListener() *corev1.Pod {
	listener := testingpod.MakePod("netpol-listener", kueueNS).
		Image(util.GetAgnHostImage(), []string{"netexec", fmt.Sprintf("--http-port=%d", metricsPort)}).
		Label("control-plane", managerSelector["control-plane"]).
		TerminationGracePeriod(1).
		Obj()
	listener.Spec.Containers[0].ReadinessProbe = &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			Exec: &corev1.ExecAction{Command: []string{"false"}},
		},
	}
	listener.Spec.Containers = append(listener.Spec.Containers, corev1.Container{
		Name:  "unlisted",
		Image: util.GetAgnHostImage(),
		Args:  []string{"netexec", fmt.Sprintf("--http-port=%d", unlistedPort)},
	})
	return listener
}

// Selecting on the chart label as well as the policy label keeps this from picking up the
// listener, which carries only the policy label.
func managerPodIP() string {
	pods := &corev1.PodList{}
	gomega.Expect(k8sClient.List(ctx, pods, client.InNamespace(kueueNS),
		client.MatchingLabels{
			"control-plane":          managerSelector["control-plane"],
			"app.kubernetes.io/name": "kueue",
		})).To(gomega.Succeed())
	gomega.Expect(pods.Items).NotTo(gomega.BeEmpty())
	return pods.Items[0].Status.PodIP
}

func podIP(pod *corev1.Pod) string {
	created := &corev1.Pod{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), created)).To(gomega.Succeed())
		g.Expect(created.Status.PodIP).NotTo(gomega.BeEmpty())
	}, util.Timeout, util.Interval).Should(gomega.Succeed())
	return created.Status.PodIP
}

func connectCmd(host string, port int) []string {
	return []string{"/agnhost", "connect", fmt.Sprintf("%s:%d", host, port), "--timeout=" + connectTimeout}
}
