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

package e2e

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

func RestartKueueController(ctx context.Context, k8sClient client.Client, kindClusterName string) {
	ginkgo.GinkgoHelper()
	kueueNS := GetKueueNamespace()
	kcmKey := types.NamespacedName{Namespace: kueueNS, Name: "kueue-controller-manager"}
	startTime := time.Now()
	UpdateDeploymentAndWaitForProgressing(ctx, k8sClient, kcmKey, kindClusterName, func(deployment *appsv1.Deployment) {
		if deployment.Spec.Template.Annotations == nil {
			deployment.Spec.Template.Annotations = make(map[string]string, 1)
		}
		deployment.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] = time.Now().Format(time.RFC3339)
	})
	WaitForKueueAvailabilityNoRestartCountCheck(ctx, k8sClient)
	ginkgo.GinkgoLogr.Info("Kueue restarted", "took", time.Since(startTime))
}

func UpdateDeploymentAndWaitForProgressing(ctx context.Context, k8sClient client.Client, key types.NamespacedName, kindClusterName string, applyChanges func(deployment *appsv1.Deployment)) {
	ginkgo.GinkgoHelper()

	// Export logs before the update to preserve logs from the previous version.
	exportKindLogs(ctx, kindClusterName)

	deployment := &appsv1.Deployment{}
	var deploymentCondition *appsv1.DeploymentCondition

	// Make sure that we don't have progressing status before update Deployment.
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, key, deployment)).To(gomega.Succeed())
		deploymentCondition = behavioral.FindDeploymentCondition(deployment, appsv1.DeploymentProgressing)
		g.Expect(deploymentCondition).NotTo(gomega.BeNil())
		g.Expect(deploymentCondition.Status).To(gomega.Equal(corev1.ConditionTrue))
		g.Expect(deploymentCondition.Reason).To(gomega.BeElementOf("NewReplicaSetCreated", "NewReplicaSetAvailable", "ReplicaSetUpdated"))
		ginkgo.GinkgoLogr.Info("Deployment status condition before the restart", "type", deploymentCondition.Type, "status", deploymentCondition.Status, "reason", deploymentCondition.Reason)
	}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

	var beforeObservedGeneration int64

	// Apply changes and update Deployment.
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, key, deployment)).To(gomega.Succeed())
		g.Expect(deployment.Generation).To(gomega.Equal(deployment.Status.ObservedGeneration))
		beforeObservedGeneration = deployment.Status.ObservedGeneration
		applyChanges(deployment)
		g.Expect(k8sClient.Update(ctx, deployment)).To(gomega.Succeed())
	}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

	// Wait for the Deployment update to be in progress.
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, key, deployment)).To(gomega.Succeed())
		g.Expect(deployment.Status.ObservedGeneration).NotTo(gomega.Equal(beforeObservedGeneration))
	}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())
}

// ForceLeaderFailover deletes the current leader pod
// and waits for a new replica to acquire the leader lease
func ForceLeaderFailover(ctx context.Context, k8sClient client.Client) {
	ginkgo.GinkgoHelper()
	kueueNS := GetKueueNamespace()
	leaseKey := types.NamespacedName{Namespace: kueueNS, Name: configapi.DefaultLeaderElectionID}

	lease := &coordinationv1.Lease{}
	gomega.Expect(k8sClient.Get(ctx, leaseKey, lease)).To(gomega.Succeed())

	holderIdentity := ptr.Deref(lease.Spec.HolderIdentity, "")
	gomega.Expect(holderIdentity).NotTo(gomega.BeEmpty(), "expected a current leader to be elected")
	leaderPodName, _, _ := strings.Cut(holderIdentity, "_")

	ginkgo.By(fmt.Sprintf("Deleting leader pod %q to force failover", leaderPodName))
	leaderPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: kueueNS, Name: leaderPodName}}
	gomega.Expect(k8sClient.Delete(ctx, leaderPod)).To(gomega.Succeed())

	ginkgo.By("Waiting for a new leader to be elected")
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, leaseKey, lease)).To(gomega.Succeed())
		newHolder := ptr.Deref(lease.Spec.HolderIdentity, "")
		g.Expect(newHolder).NotTo(gomega.BeEmpty())
		g.Expect(newHolder).NotTo(gomega.Equal(holderIdentity))
	}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())

	kcmKey := types.NamespacedName{Namespace: kueueNS, Name: "kueue-controller-manager"}
	waitForKueueControllerReadyWithWebhookEndpoints(ctx, k8sClient, kcmKey)
}

// RestartPodContainer terminates the first container of a running agnhost pod and relies on RestartPolicyAlways to restart it.
func RestartPodContainer(
	ctx context.Context,
	k8sClient client.Client,
	restClient *rest.RESTClient,
	cfg *rest.Config,
	key client.ObjectKey,
) {
	ginkgo.GinkgoHelper()
	pod := &corev1.Pod{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, key, pod)).To(gomega.Succeed())
		g.Expect(pod.Status.Phase).To(gomega.Equal(corev1.PodRunning))
		g.Expect(pod.Status.PodIP).NotTo(gomega.BeEmpty())
		g.Expect(curlAgnHost(ctx, cfg, restClient, pod, "readyz")).To(gomega.Succeed())
	}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())
	gomega.Expect(pod.Spec.RestartPolicy).To(gomega.Equal(corev1.RestartPolicyAlways),
		"RestartPodContainer only restarts a container under the Always restart policy")

	ginkgo.GinkgoLogr.Info("Restarting pod container", "pod", klog.KObj(pod), "container", pod.Spec.Containers[0].Name)
	gomega.Expect(exitAgnHost(ctx, cfg, restClient, pod, 0)).To(gomega.Succeed())
}

func WaitForActivePodsAndTerminate(
	ctx context.Context,
	k8sClient client.Client,
	restClient *rest.RESTClient,
	cfg *rest.Config,
	namespace string,
	activePodsCount, exitCode int,
	opts ...client.ListOption,
) {
	var activePods []corev1.Pod
	pods := corev1.PodList{}
	podListOpts := &client.ListOptions{}
	podListOpts.Namespace = namespace
	podListOpts.ApplyOptions(opts)
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		g.Expect(k8sClient.List(ctx, &pods, podListOpts)).To(gomega.Succeed())
		activePods = make([]corev1.Pod, 0)
		for _, p := range pods.Items {
			if len(p.Status.PodIP) != 0 && p.Status.Phase == corev1.PodRunning {
				g.Expect(curlAgnHost(ctx, cfg, restClient, &p, "readyz")).To(gomega.Succeed())
				activePods = append(activePods, p)
			}
		}
		g.Expect(activePods).To(gomega.HaveLen(activePodsCount))
	}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())

	for _, p := range activePods {
		ginkgo.GinkgoLogr.Info("Terminating pod", "pod", klog.KObj(&p))
		gomega.ExpectWithOffset(1, exitAgnHost(ctx, cfg, restClient, &p, exitCode)).To(gomega.Succeed())
	}
}
