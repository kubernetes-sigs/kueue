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
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	kuberayutils "github.com/ray-project/kuberay/ray-operator/controllers/ray/utils"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
)

const rayActorNamespace = "kueue-e2e"

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

// CreateDetachedRayActor creates a detached actor that requests the specified
// custom resource from the RayCluster.
func CreateDetachedRayActor(
	ctx context.Context,
	c client.Client,
	cfg *rest.Config,
	restClient *rest.RESTClient,
	rayClusterKey client.ObjectKey,
	actorName string,
	resourceName string,
) {
	ginkgo.GinkgoHelper()
	script := fmt.Sprintf(`import ray

ray.init(namespace=%q)

@ray.remote(num_cpus=0, resources={%q: 1})
class Actor:
    pass

try:
    ray.get_actor(%q)
except ValueError:
    Actor.options(name=%q, lifetime="detached").remote()
`, rayActorNamespace, resourceName, actorName, actorName)
	ExecuteCommandInRayClusterHead(ctx, c, cfg, restClient, rayClusterKey, []string{"python", "-c", script})
}

// TerminateDetachedRayActor terminates the named detached actor if it exists in
// the RayCluster.
func TerminateDetachedRayActor(
	ctx context.Context,
	c client.Client,
	cfg *rest.Config,
	restClient *rest.RESTClient,
	rayClusterKey client.ObjectKey,
	actorName string,
) {
	ginkgo.GinkgoHelper()
	script := fmt.Sprintf(`import ray

ray.init(namespace=%q)
try:
    actor = ray.get_actor(%q)
except ValueError:
    pass
else:
    ray.kill(actor)
`, rayActorNamespace, actorName)
	ExecuteCommandInRayClusterHead(ctx, c, cfg, restClient, rayClusterKey, []string{"python", "-c", script})
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

// SetRayClusterWorkerReplicas sets the first worker group's replica count on the RayCluster.
// When pinFloor is true, MinReplicas is also raised to replicas to prevent the in-tree autoscaler
// from scaling down below this value.
func SetRayClusterWorkerReplicas(ctx context.Context, c client.Client, rayClusterKey client.ObjectKey, replicas int32, pinFloor bool) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		rayCluster := &rayv1.RayCluster{}
		g.Expect(c.Get(ctx, rayClusterKey, rayCluster)).To(gomega.Succeed())
		g.Expect(rayCluster.Spec.WorkerGroupSpecs).NotTo(gomega.BeEmpty())
		rayCluster.Spec.WorkerGroupSpecs[0].Replicas = new(replicas)
		if pinFloor {
			rayCluster.Spec.WorkerGroupSpecs[0].MinReplicas = new(replicas)
		}
		g.Expect(c.Update(ctx, rayCluster)).To(gomega.Succeed())
	}, Timeout, Interval).Should(gomega.Succeed())
}

// ExpectRayClusterWorkerPods waits until the RayCluster has exactly wantRunning running and wantGated gated
// worker Pods with the elastic-job scheduling gate.
func ExpectRayClusterWorkerPods(ctx context.Context, c client.Client, rayClusterKey client.ObjectKey, wantRunning, wantGated int) {
	ginkgo.GinkgoHelper()
	ExpectRayClusterWorkerPodsWithTimeout(ctx, c, rayClusterKey, wantRunning, wantGated, VeryLongTimeout)
}

// ExpectRayClusterWorkerPodsWithTimeout waits until the RayCluster has exactly wantRunning running and wantGated gated
// worker Pods within the given timeout.
func ExpectRayClusterWorkerPodsWithTimeout(ctx context.Context, c client.Client, rayClusterKey client.ObjectKey, wantRunning, wantGated int, timeout time.Duration) {
	ginkgo.GinkgoHelper()
	pods := &corev1.PodList{}
	gomega.Eventually(func(g gomega.Gomega) {
		workerPods, err := GetRayClusterWorkerPods(ctx, c, rayClusterKey, "")
		g.Expect(err).NotTo(gomega.HaveOccurred())
		pods.Items = workerPods
		var running, gated int
		for i := range workerPods {
			pod := &workerPods[i]
			switch {
			case pod.DeletionTimestamp != nil:
				// A scale-down deletion can outlive the replica count dropping.
			case utilpod.HasGate(pod, kueue.ElasticJobSchedulingGate):
				gated++
			case pod.Status.Phase == corev1.PodRunning:
				running++
			}
		}
		g.Expect(running).To(gomega.Equal(wantRunning), "running worker Pods")
		g.Expect(gated).To(gomega.Equal(wantGated), "gated worker Pods")
	}, timeout, Interval).Should(gomega.Succeed())
}

// WaitForRayServiceReadyToServe waits until the RayService is unsuspended and in Ready condition, then returns it.
func WaitForRayServiceReadyToServe(ctx context.Context, c client.Client, rayServiceKey client.ObjectKey) *rayv1.RayService {
	ginkgo.GinkgoHelper()
	createdRayService := &rayv1.RayService{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(c.Get(ctx, rayServiceKey, createdRayService)).To(gomega.Succeed())
		g.Expect(createdRayService.Spec.RayClusterSpec.Suspend).To(gomega.Equal(new(false)))
		g.Expect(apimeta.IsStatusConditionTrue(createdRayService.Status.Conditions, string(rayv1.RayServiceReady))).To(gomega.BeTrue())
	}, VeryLongTimeout, Interval).Should(gomega.Succeed(), AssertMsg("RayService did not become ready to serve", createdRayService))
	return createdRayService
}
