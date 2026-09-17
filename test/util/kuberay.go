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

package util

import (
	"context"
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	kuberayutils "github.com/ray-project/kuberay/ray-operator/controllers/ray/utils"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// GetRayClusterHeadPod returns the only head Pod associated with the RayCluster.
func GetRayClusterHeadPod(ctx context.Context, c client.Client, rayClusterKey client.ObjectKey) (*corev1.Pod, error) {
	pods := &corev1.PodList{}
	if err := c.List(ctx, pods,
		client.InNamespace(rayClusterKey.Namespace),
		client.MatchingLabels{
			kuberayutils.RayClusterLabelKey:  rayClusterKey.Name,
			kuberayutils.RayNodeTypeLabelKey: string(rayv1.HeadNode),
		},
	); err != nil {
		return nil, err
	}
	if len(pods.Items) != 1 {
		return nil, fmt.Errorf("expected exactly one head Pod for RayCluster %s, got %d", rayClusterKey, len(pods.Items))
	}
	return &pods.Items[0], nil
}

// ExecuteCommandInRayClusterHead waits for the RayCluster head to become ready,
// then executes the command in its head Pod.
func ExecuteCommandInRayClusterHead(
	ctx context.Context,
	c client.Client,
	cfg *rest.Config,
	restClient *rest.RESTClient,
	rayClusterKey client.ObjectKey,
	command []string,
) {
	ginkgo.GinkgoHelper()
	var headPod *corev1.Pod
	gomega.Eventually(func(g gomega.Gomega) {
		rayCluster := &rayv1.RayCluster{}
		g.Expect(c.Get(ctx, rayClusterKey, rayCluster)).To(gomega.Succeed())
		g.Expect(apimeta.IsStatusConditionTrue(rayCluster.Status.Conditions, string(rayv1.HeadPodReady))).To(gomega.BeTrue())

		pod, err := GetRayClusterHeadPod(ctx, c, rayClusterKey)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(pod.Status.Phase).To(gomega.Equal(corev1.PodRunning))
		headPod = pod
	}, VeryLongTimeout, Interval).Should(gomega.Succeed())

	gomega.Eventually(func(g gomega.Gomega) {
		_, stderr, err := KExecute(
			ctx,
			cfg,
			restClient,
			headPod.Namespace,
			headPod.Name,
			headPod.Spec.Containers[0].Name,
			command,
		)
		g.Expect(err).NotTo(gomega.HaveOccurred(), "stderr: %s", string(stderr))
	}, LongTimeout, Interval).Should(gomega.Succeed())
}

// GetRayClusterWorkerPods returns the worker Pods associated with the RayCluster
// whose phase matches the provided phase. If phase is empty, it returns all
// associated worker Pods.
func GetRayClusterWorkerPods(ctx context.Context, c client.Client, rayClusterKey client.ObjectKey, phase corev1.PodPhase) ([]corev1.Pod, error) {
	pods := &corev1.PodList{}
	if err := c.List(ctx, pods,
		client.InNamespace(rayClusterKey.Namespace),
		client.MatchingLabels{
			kuberayutils.RayClusterLabelKey:  rayClusterKey.Name,
			kuberayutils.RayNodeTypeLabelKey: string(rayv1.WorkerNode),
		},
	); err != nil {
		return nil, err
	}
	if phase == "" {
		return pods.Items, nil
	}
	filteredPods := make([]corev1.Pod, 0, len(pods.Items))
	for _, pod := range pods.Items {
		if pod.Status.Phase == phase {
			filteredPods = append(filteredPods, pod)
		}
	}
	return filteredPods, nil
}
