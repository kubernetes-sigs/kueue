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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	ComponentTimeout         = 10 * time.Second
	ComponentMediumTimeout   = 45 * time.Second
	ComponentLongTimeout     = 90 * time.Second
	ComponentVeryLongTimeout = 5 * time.Minute
	ComponentInterval        = 250 * time.Millisecond
)

// WaitForKueueAvailability waits for Kueue controller-manager deployment to be available
// WaitForKueueAvailability waits for Kueue controller-manager deployment to be available
func WaitForKueueAvailability(ctx context.Context, k8sClient client.Client) {
	ginkgo.GinkgoHelper()
	waitForKueueAvailability(ctx, k8sClient, true)
}

// WaitForAppWrapperAvailability waits for AppWrapper controller to be available
func WaitForAppWrapperAvailability(ctx context.Context, k8sClient client.Client) {
	awmKey := types.NamespacedName{Namespace: "appwrapper-system", Name: "appwrapper-controller-manager"}
	waitForDeploymentAvailability(ctx, k8sClient, awmKey, true)
}

// WaitForJobSetAvailability waits for JobSet controller to be available
func WaitForJobSetAvailability(ctx context.Context, k8sClient client.Client) {
	jcmKey := types.NamespacedName{Namespace: "jobset-system", Name: "jobset-controller-manager"}
	waitForDeploymentAvailability(ctx, k8sClient, jcmKey, true)
}

// WaitForLeaderWorkerSetAvailability waits for LeaderWorkerSet controller to be available
func WaitForLeaderWorkerSetAvailability(ctx context.Context, k8sClient client.Client) {
	lwKey := types.NamespacedName{Namespace: "lws-system", Name: "lws-controller-manager"}
	waitForDeploymentAvailability(ctx, k8sClient, lwKey, true)
}

// WaitForKubeFlowTrainingOperatorAvailability waits for KubeFlow Training operator to be available
func WaitForKubeFlowTrainingOperatorAvailability(ctx context.Context, k8sClient client.Client) {
	kftoKey := types.NamespacedName{Namespace: "kubeflow", Name: "training-operator"}
	waitForDeploymentAvailability(ctx, k8sClient, kftoKey, true)
}

// WaitForSparkOperatorAvailability waits for Spark operator to be available
func WaitForSparkOperatorAvailability(ctx context.Context, k8sClient client.Client) {
	sparkctrKey := types.NamespacedName{Namespace: "spark-operator", Name: "spark-operator-controller"}
	waitForDeploymentAvailability(ctx, k8sClient, sparkctrKey, true)
}

// WaitForKubeFlowMPIOperatorAvailability waits for MPI operator to be available
func WaitForKubeFlowMPIOperatorAvailability(ctx context.Context, k8sClient client.Client) {
	mpiKey := types.NamespacedName{Namespace: "mpi-operator", Name: "mpi-operator"}
	waitForDeploymentAvailability(ctx, k8sClient, mpiKey, true)
}

// WaitForKubeRayOperatorAvailability waits for KubeRay operator to be available
func WaitForKubeRayOperatorAvailability(ctx context.Context, k8sClient client.Client) {
	kroKey := types.NamespacedName{Namespace: "default", Name: "kuberay-operator"}
	waitForDeploymentAvailability(ctx, k8sClient, kroKey, true)
}

// WaitForKubeFlowTrainnerControllerManagerAvailability waits for Trainer controller manager to be available
func WaitForKubeFlowTrainnerControllerManagerAvailability(ctx context.Context, k8sClient client.Client) {
	kftoKey := types.NamespacedName{Namespace: "kubeflow-system", Name: "kubeflow-trainer-controller-manager"}
	waitForDeploymentAvailability(ctx, k8sClient, kftoKey, true)
}

// WaitForPrometheusAvailability waits for Prometheus StatefulSet to be available
func WaitForPrometheusAvailability(ctx context.Context, k8sClient client.Client) {
	ginkgo.GinkgoHelper()
	key := types.NamespacedName{Namespace: "monitoring", Name: "prometheus-prometheus"}
	ginkgo.By(fmt.Sprintf("Waiting for availability of StatefulSet: %q", key))
	gomega.Eventually(func(g gomega.Gomega) {
		sts := &appsv1.StatefulSet{}
		g.Expect(k8sClient.Get(ctx, key, sts)).To(gomega.Succeed())
		g.Expect(sts.Status.ReadyReplicas).To(gomega.Equal(sts.Status.Replicas))
	}, VeryLongTimeout, Interval).Should(gomega.Succeed())
}

// WaitForPodRunning waits for a Pod to reach Running state
func WaitForPodRunning(ctx context.Context, k8sClient client.Client, pod *corev1.Pod) {
	ginkgo.GinkgoHelper()
	createdPod := &corev1.Pod{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), createdPod)).To(gomega.Succeed())
		g.Expect(createdPod.Status.Phase).To(gomega.Equal(corev1.PodRunning))
	}, LongTimeout, Interval).Should(gomega.Succeed())
}

// WaitForKubeSystemControllersAvailability waits for kube-system controllers to be available
func WaitForKubeSystemControllersAvailability(ctx context.Context, k8sClient client.Client, clusterName string) {
	ginkgo.GinkgoHelper()
	deploymentList := &appsv1.DeploymentList{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.List(ctx, deploymentList, client.InNamespace("kube-system"))).To(gomega.Succeed())
		for _, deploy := range deploymentList.Items {
			g.Expect(deploy.Status.AvailableReplicas).To(gomega.Equal(deploy.Status.Replicas))
		}
	}, VeryLongTimeout, Interval).Should(gomega.Succeed())
	ginkgo.GinkgoLogr.Info("kube-system controllers are available", "cluster", clusterName)
}

// ForceLeaderFailover deletes the current leader pod and waits for a new replica to acquire the leader lease
func ForceLeaderFailover(ctx context.Context, k8sClient client.Client) {
	ginkgo.GinkgoHelper()
	kueueNS := GetKueueNamespace()
	leaseKey := types.NamespacedName{Namespace: kueueNS, Name: "kueue.x-k8s.io"}

	// Get current leader
	lease := &coordinationv1.Lease{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, leaseKey, lease)).To(gomega.Succeed())
	}, Timeout, Interval).Should(gomega.Succeed())

	leaderPodName := *lease.Spec.HolderIdentity

	// Delete the leader pod
	leaderPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: kueueNS,
			Name:      leaderPodName,
		},
	}
	gomega.Expect(k8sClient.Delete(ctx, leaderPod)).To(gomega.Succeed())

	// Wait for new leader to be elected
	ginkgo.By("Waiting for new leader to be elected")
	gomega.Eventually(func(g gomega.Gomega) {
		newLease := &coordinationv1.Lease{}
		g.Expect(k8sClient.Get(ctx, leaseKey, newLease)).To(gomega.Succeed())
		g.Expect(*newLease.Spec.HolderIdentity).ShouldNot(gomega.Equal(leaderPodName))
	}, VeryLongTimeout, Interval).Should(gomega.Succeed())

	// Wait for controller manager to be available again
	WaitForKueueAvailabilityNoRestartCountCheck(ctx, k8sClient)
}
