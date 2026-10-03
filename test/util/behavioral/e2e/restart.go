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
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

// RestartKueueController restarts the Kueue controller manager pod
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

// WaitForKueueAvailabilityNoRestartCountCheck waits for Kueue availability without checking restart count
func WaitForKueueAvailabilityNoRestartCountCheck(ctx context.Context, k8sClient client.Client) {
	ginkgo.GinkgoHelper()
	waitForKueueAvailability(ctx, k8sClient, false)
}

// UpdateDeploymentAndWaitForProgressing updates deployment and waits for it to progress
func UpdateDeploymentAndWaitForProgressing(ctx context.Context, k8sClient client.Client, key types.NamespacedName, kindClusterName string, applyChanges func(deployment *appsv1.Deployment)) {
	ginkgo.GinkgoHelper()

	// Export logs before the update
	exportKindLogs(ctx, kindClusterName)

	deployment := &appsv1.Deployment{}
	var deploymentCondition *appsv1.DeploymentCondition

	// Make sure that we don't have progressing status before update
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, key, deployment)).To(gomega.Succeed())
		deploymentCondition = getDeploymentCondition(deployment, appsv1.DeploymentProgressing)
		if deploymentCondition != nil {
			gomega.Expect(deploymentCondition.Status).To(gomega.Equal(corev1.ConditionTrue))
		}
	}, VeryLongTimeout, Interval).Should(gomega.Succeed())

	oldResourceVersion := deployment.ResourceVersion

	applyChanges(deployment)
	gomega.Expect(k8sClient.Update(ctx, deployment)).To(gomega.Succeed())

	// Wait for deployment to start progressing
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, key, deployment)).To(gomega.Succeed())
		g.Expect(deployment.ResourceVersion).ShouldNot(gomega.Equal(oldResourceVersion))
		deploymentCondition = getDeploymentCondition(deployment, appsv1.DeploymentProgressing)
		g.Expect(deploymentCondition).ShouldNot(gomega.BeNil())
		g.Expect(deploymentCondition.Status).Should(gomega.Equal(corev1.ConditionTrue))
	}, VeryLongTimeout, Interval).Should(gomega.Succeed())

	// Wait for deployment to become available
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, key, deployment)).To(gomega.Succeed())
		g.Expect(deployment.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(appsv1.DeploymentCondition{
			Type:   appsv1.DeploymentAvailable,
			Status: corev1.ConditionTrue,
		}, IgnoreDeploymentConditionTimestampsAndMessage)))
	}, VeryLongTimeout, Interval).Should(gomega.Succeed())
}

func waitForKueueAvailability(ctx context.Context, k8sClient client.Client, checkNoRestarts bool) {
	ginkgo.GinkgoHelper()
	kcmKey := types.NamespacedName{Namespace: GetKueueNamespace(), Name: "kueue-controller-manager"}
	waitForDeploymentAvailability(ctx, k8sClient, kcmKey, checkNoRestarts)
	waitForKueueControllerReadyWithWebhookEndpoints(ctx, k8sClient, kcmKey)
	waitForLeaderElection(ctx, k8sClient)
}

func waitForDeploymentAvailability(ctx context.Context, k8sClient client.Client, key types.NamespacedName, checkNoRestarts bool) {
	ginkgo.GinkgoHelper()
	ginkgo.By(fmt.Sprintf("Waiting for availability of deployment: %q", key))

	gomega.Eventually(func(g gomega.Gomega) {
		deployment := &appsv1.Deployment{}
		g.Expect(k8sClient.Get(ctx, key, deployment)).To(gomega.Succeed())
		g.Expect(deployment.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(appsv1.DeploymentCondition{
			Type:   appsv1.DeploymentAvailable,
			Status: corev1.ConditionTrue,
		}, IgnoreDeploymentConditionTimestampsAndMessage)))

		if checkNoRestarts {
			for _, c := range deployment.Spec.Template.Spec.Containers {
				for _, pod := range getPodsForDeployment(ctx, g, k8sClient, key) {
					for _, cs := range pod.Status.ContainerStatuses {
						if cs.Name == c.Name {
							g.Expect(cs.RestartCount).Should(gomega.Equal(int32(0)))
						}
					}
				}
			}
		}
	}, VeryLongTimeout, Interval).Should(gomega.Succeed())
}

func waitForKueueControllerReadyWithWebhookEndpoints(ctx context.Context, k8sClient client.Client, key types.NamespacedName) {
	ginkgo.GinkgoHelper()
	waitStart := time.Now()
	ginkgo.By(fmt.Sprintf("Waiting for ready pods and webhook endpoints: %q", key))

	gomega.Eventually(func(g gomega.Gomega) {
		deployment := &appsv1.Deployment{}
		g.Expect(k8sClient.Get(ctx, key, deployment)).To(gomega.Succeed())
		desiredReplicas := ptr.Deref(deployment.Spec.Replicas, 0)

		g.Expect(deployment.Status.AvailableReplicas).To(gomega.Equal(desiredReplicas),
			fmt.Sprintf("available replicas: %d, desired: %d", deployment.Status.AvailableReplicas, desiredReplicas))

		selector, err := metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
		g.Expect(err).NotTo(gomega.HaveOccurred())

		pods := &corev1.PodList{}
		g.Expect(k8sClient.List(ctx, pods,
			client.InNamespace(key.Namespace),
			client.MatchingLabelsSelector{Selector: selector},
		)).To(gomega.Succeed())

		readyPodIPs := sets.New[string]()
		for _, pod := range pods.Items {
			if isPodReady(&pod) && pod.DeletionTimestamp == nil && pod.Status.PodIP != "" {
				readyPodIPs.Insert(pod.Status.PodIP)
			}
		}
		g.Expect(readyPodIPs).To(gomega.HaveLen(int(desiredReplicas)),
			fmt.Sprintf("ready pods: %d, desired: %d", readyPodIPs.Len(), desiredReplicas))

		endpointSlices := &discoveryv1.EndpointSliceList{}
		g.Expect(k8sClient.List(ctx, endpointSlices,
			client.InNamespace(key.Namespace),
			client.MatchingLabels{discoveryv1.LabelServiceName: "kueue-webhook-service"},
		)).To(gomega.Succeed())

		endpointIPs := sets.New[string]()
		for _, slice := range endpointSlices.Items {
			for _, ep := range slice.Endpoints {
				if ep.Conditions.Ready == nil || *ep.Conditions.Ready {
					endpointIPs.Insert(ep.Addresses...)
				}
			}
		}
		g.Expect(endpointIPs).To(gomega.Equal(readyPodIPs))
	}, LongTimeout, Interval).Should(gomega.Succeed())

	// Verify the webhook path itself
	ginkgo.By(fmt.Sprintf("Probing the webhook data path: %q", key))
	gomega.Eventually(func(g gomega.Gomega) {
		probeRF := &kueue.ResourceFlavor{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "webhook-probe-"},
		}
		g.Expect(k8sClient.Create(ctx, probeRF, client.DryRunAll)).To(gomega.Succeed())
	}, LongTimeout, Interval).Should(gomega.Succeed())

	ginkgo.GinkgoLogr.Info("Ready pods and webhook endpoints verified", "deployment", key, "waitingTime", time.Since(waitStart))
}

func waitForLeaderElection(ctx context.Context, k8sClient client.Client) {
	ginkgo.GinkgoHelper()
	ginkgo.By("Waiting for leader election")

	gomega.Eventually(func(g gomega.Gomega) {
		lease := &coordinationv1.Lease{}
		leaseKey := types.NamespacedName{
			Namespace: GetKueueNamespace(),
			Name:      "kueue.x-k8s.io",
		}
		g.Expect(k8sClient.Get(ctx, leaseKey, lease)).To(gomega.Succeed())
		g.Expect(lease.Spec.HolderIdentity).ShouldNot(gomega.BeEmpty())
	}, VeryLongTimeout, Interval).Should(gomega.Succeed())
}

func isPodReady(pod *corev1.Pod) bool {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}

func getDeploymentCondition(deployment *appsv1.Deployment, condType appsv1.DeploymentConditionType) *appsv1.DeploymentCondition {
	for i := range deployment.Status.Conditions {
		if deployment.Status.Conditions[i].Type == condType {
			return &deployment.Status.Conditions[i]
		}
	}
	return nil
}

func getPodsForDeployment(ctx context.Context, g gomega.Gomega, k8sClient client.Client, key types.NamespacedName) []*corev1.Pod {
	deployment := &appsv1.Deployment{}
	g.Expect(k8sClient.Get(ctx, key, deployment)).To(gomega.Succeed())

	selector, err := metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
	g.Expect(err).NotTo(gomega.HaveOccurred())

	pods := &corev1.PodList{}
	g.Expect(k8sClient.List(ctx, pods,
		client.InNamespace(key.Namespace),
		client.MatchingLabelsSelector{Selector: selector},
	)).To(gomega.Succeed())

	var result []*corev1.Pod
	for i := range pods.Items {
		result = append(result, &pods.Items[i])
	}
	return result
}

func exportKindLogs(ctx context.Context, clusterName string) {
	// This is a placeholder - implement based on your logging needs
	// This would typically export logs from the KIND cluster
}
