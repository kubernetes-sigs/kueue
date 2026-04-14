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

package customconfigse2e

import (
	"os/exec"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/util"
)

const (
	podTerminationGracePeriodSeconds = 1
)

var _ = ginkgo.Describe("Failure Recovery Policy", ginkgo.Label("feature:failurerecoverypolicy"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		job *batchv1.Job
		ns  *corev1.Namespace
		rf  *kueue.ResourceFlavor
	)

	ginkgo.BeforeAll(func() {
		util.UpdateKueueConfigurationAndRestart(ctx, k8sClient, defaultKueueCfg, kindClusterName, func(cfg *configapi.Configuration) {
			cfg.FeatureGates = map[string]bool{string(features.FailureRecoveryPolicy): true}
		})
		rf = utiltestingapi.MakeResourceFlavor("rf").Obj()
		util.MustCreate(ctx, k8sClient, rf)
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "frp-")
		job = testingjob.MakeJob("test-job", ns.Name).
			Queue("lq").
			Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
			RequestAndLimit(corev1.ResourceCPU, "100m").
			RequestAndLimit(corev1.ResourceMemory, "20Mi").
			Parallelism(1).
			TerminationGracePeriod(podTerminationGracePeriodSeconds).
			PodReplacementPolicy(ptr.To(batchv1.Failed)).
			PodAnnotation(constants.SafeToForcefullyDeleteAnnotationKey, constants.SafeToForcefullyDeleteAnnotationValue).
			PodAffinity(&corev1.Affinity{
				PodAntiAffinity: &corev1.PodAntiAffinity{
					PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{
						{
							Weight: 1,
							PodAffinityTerm: corev1.PodAffinityTerm{
								Namespaces:    []string{"kube-system", "kueue-system"},
								TopologyKey:   "kubernetes.io/hostname",
								LabelSelector: &metav1.LabelSelector{},
							},
						},
					},
				},
			}).
			Toleration(corev1.Toleration{
				Key:               corev1.TaintNodeUnreachable,
				Operator:          corev1.TolerationOpExists,
				Effect:            corev1.TaintEffectNoExecute,
				TolerationSeconds: ptr.To[int64](1),
			}).
			Obj()
	})

	ginkgo.JustAfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.AfterAll(func() {
		util.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
	})

	ginkgo.When("the kubelet on a node goes down", func() {
		var (
			cq       *kueue.ClusterQueue
			lq       *kueue.LocalQueue
			pod      *corev1.Pod
			nodeName string

			// Unresponsive node is marked as unreachable after the grace period.
			nodeMonitorGracePeriod = 50 * time.Second

			// `podToleration` + `deletionGracePeriodSeconds` + `forcefulTerminationGracePeriod`
			unhealthyNodeforcefulTerminationCheckTimeout = (1 + podTerminationGracePeriodSeconds + 60) * time.Second
		)

		ginkgo.BeforeEach(func() {
			ginkgo.By("creating the cluster queue and local queue", func() {
				cq = utiltestingapi.MakeClusterQueue("cq").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).
						Resource(corev1.ResourceCPU, "8").
						Resource(corev1.ResourceMemory, "36G").
						Obj()).
					Obj()
				util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

				lq = utiltestingapi.MakeLocalQueue("lq", ns.Name).
					ClusterQueue(cq.Name).
					Obj()
				util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
			})

			ginkgo.By("creating the job and waiting for it to start", func() {
				util.MustCreate(ctx, k8sClient, job)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), job)).To(gomega.Succeed())
					g.Expect(*job.Spec.Suspend).To(gomega.BeFalse())
					g.Expect(job.Status.Active).To(gomega.Equal(int32(1)))
					g.Expect(job.Status.Ready).To(gomega.Equal(ptr.To(int32(1))))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("ensuring the pod is scheduled on a worker node", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					pods := &corev1.PodList{}
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name), client.MatchingLabels(job.Spec.Selector.MatchLabels))).To(gomega.Succeed())
					g.Expect(pods.Items).To(gomega.HaveLen(1))

					pod = &pods.Items[0]
					nodeName = pod.Spec.NodeName
					g.Expect(nodeName).ToNot(gomega.BeEmpty())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("stopping the kubelet on the node running the pod", func() {
				cmd := exec.Command("docker", "exec", nodeName, "systemctl", "stop", "kubelet")
				gomega.Expect(cmd.Run()).To(gomega.Succeed())
			})
		})

		ginkgo.JustAfterEach(func() {
			ginkgo.By("starting the kubelet on the node running the pod", func() {
				cmd := exec.Command("docker", "exec", nodeName, "systemctl", "start", "kubelet")
				gomega.Expect(cmd.Run()).To(gomega.Succeed())
			})

			ginkgo.By("waiting for the node to be ready again", func() {
				util.ExpectNodeToBecomeReady(ctx, k8sClient, nodeName, lq)
			})
		})

		ginkgo.AfterEach(func() {
			ginkgo.By("deleting the cluster queue", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			})
		})

		ginkgo.It("should delete pods running on an unreachable node", func() {
			util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, pod, false, nodeMonitorGracePeriod+unhealthyNodeforcefulTerminationCheckTimeout+util.MediumTimeout)
		})

		ginkgo.It("should unblock the stuck pod's parents that are being deleted with foreground propagation", func() {
			ginkgo.By("waiting for pods to be marked for termination", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), pod)).To(gomega.Succeed())
					g.Expect(pod.DeletionTimestamp).ToNot(gomega.BeNil())
				}, nodeMonitorGracePeriod+util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("deleting the job with foreground propagation", func() {
				gomega.Expect(k8sClient.Delete(ctx, job, &client.DeleteOptions{PropagationPolicy: ptr.To(metav1.DeletePropagationForeground)})).To(gomega.Succeed())
			})

			ginkgo.By("ensuring the job is deleted", func() {
				util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, job, false, unhealthyNodeforcefulTerminationCheckTimeout+util.Timeout)
			})
		})
	})
})
