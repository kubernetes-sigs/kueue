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
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"slices"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/e2e"
)

// Sanity check that the compiled kueuectl binary works end-to-end.
// API persistence cases live in the integration suite.
var _ = ginkgo.Describe("Kueuectl", ginkgo.Label("area:singlecluster", "feature:kueuectl"), func() {
	var (
		ns *corev1.Namespace
		cq *kueue.ClusterQueue
		rf *kueue.ResourceFlavor
	)

	ginkgo.BeforeEach(func() {
		rf = nil
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-")

		cq = utiltestingapi.MakeClusterQueue("e2e-cq-" + ns.Name).Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
		behavioral.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.It("Should create a local queue and list it back", func() {
		lqName := "e2e-lq-sanity"

		ginkgo.By("Create local queue via the kueuectl binary", func() {
			cmd := exec.Command(kueuectlPath, "create", "localqueue", lqName,
				"--clusterqueue", cq.Name, "--namespace", ns.Name)
			output, err := cmd.CombinedOutput()
			gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
		})

		ginkgo.By("List local queues via the kueuectl binary and verify the new one appears", func() {
			var stdout, stderr bytes.Buffer
			cmd := exec.Command(kueuectlPath, "list", "localqueue",
				"--namespace", ns.Name, "-o", "json")
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			err := cmd.Run()
			gomega.Expect(err).NotTo(gomega.HaveOccurred(), "stderr: %s", stderr.String())

			var list kueue.LocalQueueList
			gomega.Expect(json.Unmarshal(stdout.Bytes(), &list)).To(gomega.Succeed())
			gomega.Expect(list.Items).To(gomega.ContainElement(gomega.SatisfyAll(
				gomega.HaveField("Name", lqName),
				gomega.HaveField("Spec.ClusterQueue", kueue.ClusterQueueReference(cq.Name)),
			)))
		})
	})

	ginkgo.It("Should schedule a Job with an explicit empty toleration from a non-TAS resource flavor only for an empty taint value", ginkgo.Serial, func() {
		taintKey := "example.com/empty-" + ns.Name
		var node *corev1.Node
		ginkgo.By("Select a ready worker with no blocking taints", func() {
			nodes := &corev1.NodeList{}
			gomega.Expect(k8sClient.List(ctx, nodes, client.MatchingLabels{corev1.LabelOSStable: "linux"},
				client.HasLabels{corev1.LabelHostname})).To(gomega.Succeed())
			for _, candidate := range nodes.Items {
				_, controlPlane := candidate.Labels["node-role.kubernetes.io/control-plane"]
				_, master := candidate.Labels["node-role.kubernetes.io/master"]
				if controlPlane || master || candidate.Spec.Unschedulable || candidate.DeletionTimestamp != nil ||
					!utiltas.IsNodeStatusConditionTrue(candidate.Status.Conditions, corev1.NodeReady) ||
					slices.ContainsFunc(candidate.Spec.Taints, func(taint corev1.Taint) bool {
						return taint.Key == taintKey || taint.Effect == corev1.TaintEffectNoSchedule || taint.Effect == corev1.TaintEffectNoExecute
					}) {
					continue
				}
				node = candidate.DeepCopy()
				break
			}
			gomega.Expect(node).NotTo(gomega.BeNil(), "No ready, schedulable Linux worker without blocking taints")
		})
		nodeKey := client.ObjectKeyFromObject(node)
		nodeSelector := map[string]string{
			corev1.LabelOSStable: "linux",
			corev1.LabelHostname: node.Labels[corev1.LabelHostname],
		}
		ginkgo.DeferCleanup(func() {
			gomega.Expect(k8sClient.Get(ctx, nodeKey, node)).To(gomega.Succeed())
			gomega.Expect(clientutil.Patch(ctx, k8sClient, node, func() (bool, error) {
				originalCount := len(node.Spec.Taints)
				node.Spec.Taints = slices.DeleteFunc(node.Spec.Taints, func(taint corev1.Taint) bool {
					return taint.Key == taintKey && taint.Effect == corev1.TaintEffectNoSchedule
				})
				return len(node.Spec.Taints) != originalCount, nil
			}, clientutil.WithRetryOnConflict())).To(gomega.Succeed())
		})
		ginkgo.By("Taint the target node with a nonempty value", func() {
			gomega.Expect(k8sClient.Get(ctx, nodeKey, node)).To(gomega.Succeed())
			gomega.Expect(clientutil.Patch(ctx, k8sClient, node, func() (bool, error) {
				node.Spec.Taints = append(node.Spec.Taints, corev1.Taint{
					Key: taintKey, Value: "foo", Effect: corev1.TaintEffectNoSchedule,
				})
				return true, nil
			}, clientutil.WithRetryOnConflict())).To(gomega.Succeed())
		})

		rfName := "empty-toleration-" + ns.Name
		rf = utiltestingapi.MakeResourceFlavor(rfName).Obj()

		ginkgo.By("Create a non-TAS resource flavor through the CLI", func() {
			cmd := exec.Command(kueuectlPath, "create", "resourceflavor", rfName,
				"--node-labels", corev1.LabelOSStable+"=linux,"+corev1.LabelHostname+"="+nodeSelector[corev1.LabelHostname],
				"--tolerations", taintKey+"=:NoSchedule")
			output, err := cmd.CombinedOutput()
			gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: rfName}, rf)).To(gomega.Succeed())
			gomega.Expect(rf.Spec.TopologyName).To(gomega.BeNil())
			// Leave nodeTaints unset so kube-scheduler, rather than Kueue admission, evaluates the real node taint.
			gomega.Expect(rf.Spec.NodeTaints).To(gomega.BeEmpty())
		})

		ginkgo.By("Provide quota for the flavor and a local queue", func() {
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).To(gomega.Succeed())
			original := cq.DeepCopy()
			cq.Spec = utiltestingapi.MakeClusterQueue(cq.Name).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rfName).
					Resource(corev1.ResourceCPU, "1").
					Resource(corev1.ResourceMemory, "1Gi").Obj()).Obj().Spec
			gomega.Expect(k8sClient.Patch(ctx, cq, client.MergeFrom(original))).To(gomega.Succeed())
			lq := utiltestingapi.MakeLocalQueue("main", ns.Name).ClusterQueue(cq.Name).Obj()
			behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
		})

		job := testingjob.MakeJob("empty-toleration", ns.Name).
			Queue("main").
			RequestAndLimit(corev1.ResourceCPU, "100m").
			RequestAndLimit(corev1.ResourceMemory, "20Mi").
			Image(e2e.GetAgnHostImage(), e2e.BehaviorExitFast).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, job)
		ginkgo.By("Wait for admission and the unsuspended Job's pod", func() {
			workloads := behavioral.ExpectWorkloadsInNamespace(ctx, k8sClient, ns.Name, 1)
			behavioral.ExpectWorkloadsToBeAdmittedByKeysWithTimeout(ctx, k8sClient, behavioral.MediumTimeout,
				client.ObjectKeyFromObject(&workloads[0]))
			behavioral.ExpectJobUnsuspendedWithNodeSelectors(ctx, k8sClient, client.ObjectKeyFromObject(job), nodeSelector)
		})
		pods := &corev1.PodList{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name),
				client.MatchingLabels{batchv1.JobNameLabel: job.Name})).To(gomega.Succeed())
			g.Expect(pods.Items).To(gomega.HaveLen(1))
		}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
		pod := pods.Items[0].DeepCopy()
		podKey := client.ObjectKeyFromObject(pod)
		podUID := pod.UID
		ginkgo.By("Check that the nonempty taint keeps the admitted pod unschedulable", func() {
			checkUnschedulable := func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, podKey, pod)).To(gomega.Succeed())
				g.Expect(pod.UID).To(gomega.Equal(podUID))
				g.Expect(pod.Spec.SchedulingGates).To(gomega.BeEmpty())
				g.Expect(pod.Spec.NodeSelector).To(gomega.Equal(nodeSelector))
				g.Expect(pod.Spec.NodeName).To(gomega.BeEmpty())
				g.Expect(pod.Status.Phase).To(gomega.Equal(corev1.PodPending))
				g.Expect(pod.Status.Conditions).To(gomega.ContainElement(gomega.SatisfyAll(
					gomega.BeComparableTo(corev1.PodCondition{
						Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable,
					}, behavioral.IgnorePodConditionTimestampsMessageAndObservedGeneration),
					gomega.HaveField("Message", gomega.ContainSubstring("taint")),
				)))
			}
			gomega.Eventually(checkUnschedulable, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
			gomega.Consistently(checkUnschedulable, behavioral.LongConsistentDuration, behavioral.Interval).Should(gomega.Succeed())
		})
		ginkgo.By("Change the taint to an empty value and check that the same pod completes", func() {
			gomega.Expect(k8sClient.Get(ctx, nodeKey, node)).To(gomega.Succeed())
			gomega.Expect(clientutil.Patch(ctx, k8sClient, node, func() (bool, error) {
				index := slices.IndexFunc(node.Spec.Taints, func(taint corev1.Taint) bool {
					return taint.Key == taintKey && taint.Effect == corev1.TaintEffectNoSchedule
				})
				if index < 0 {
					return false, fmt.Errorf("test taint %q disappeared from node %q", taintKey, node.Name)
				}
				node.Spec.Taints[index].Value = ""
				return true, nil
			}, clientutil.WithRetryOnConflict())).To(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, podKey, pod)).To(gomega.Succeed())
				g.Expect(pod.UID).To(gomega.Equal(podUID))
				g.Expect(pod.Spec.NodeName).To(gomega.Equal(node.Name))
				g.Expect(pod.Status.Phase).To(gomega.Equal(corev1.PodSucceeded))
			}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
			e2e.ExpectJobToBeCompleted(ctx, k8sClient, job)
		})

		ginkgo.By("Check that the Job and its scheduled pod preserve Equal rather than Exists", func() {
			wantToleration := corev1.Toleration{
				Key: taintKey, Operator: corev1.TolerationOpEqual,
				Effect: corev1.TaintEffectNoSchedule,
			}
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), job)).To(gomega.Succeed())
			gomega.Expect(job.Spec.Template.Spec.NodeSelector).To(gomega.Equal(nodeSelector))
			gomega.Expect(job.Spec.Template.Spec.Tolerations).To(gomega.ContainElement(wantToleration))
			gomega.Expect(pod.Spec.Tolerations).To(gomega.ContainElement(wantToleration))
		})
	})
})
