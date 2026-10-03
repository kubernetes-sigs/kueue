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
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// WaitForActivePodsAndTerminate waits for active pods and terminates them
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
	}, LongTimeout, Interval).Should(gomega.Succeed())

	for _, p := range activePods {
		ginkgo.GinkgoLogr.Info("Terminating pod", "pod", klog.KObj(&p))
		gomega.ExpectWithOffset(1, exitAgnHost(ctx, cfg, restClient, &p, exitCode)).To(gomega.Succeed())
	}
}

// RestartPodContainer terminates the first container of a running pod and relies on RestartPolicyAlways to restart it
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
	}, LongTimeout, Interval).Should(gomega.Succeed())
	gomega.Expect(pod.Spec.RestartPolicy).To(gomega.Equal(corev1.RestartPolicyAlways),
		"RestartPodContainer only restarts a container under the Always restart policy")

	ginkgo.GinkgoLogr.Info("Restarting pod container", "pod", klog.KObj(pod), "container", pod.Spec.Containers[0].Name)
	gomega.Expect(exitAgnHost(ctx, cfg, restClient, pod, 0)).To(gomega.Succeed())
}

// KExecute executes a command in a pod and returns stdout, stderr, and error
func KExecute(ctx context.Context, cfg *rest.Config, restClient *rest.RESTClient, namespace, podName, containerName string, cmd []string) ([]byte, []byte, error) {
	url := restClient.Post().
		Resource("pods").
		Namespace(namespace).
		Name(podName).
		SubResource("exec").
		Param("container", containerName).
		Param("command", cmd[0])

	for _, c := range cmd[1:] {
		url.Param("command", c)
	}
	url.Param("stdout", "true").
		Param("stderr", "true").
		Param("stdin", "false")

	exec, err := remotecommand.NewSPDYExecutor(cfg, "POST", url.URL())
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create executor: %w", err)
	}

	var stdout, stderr bytes.Buffer
	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
		Stdin:  nil,
		Tty:    false,
	})

	return stdout.Bytes(), stderr.Bytes(), err
}

func curlAgnHost(ctx context.Context, cfg *rest.Config, restClient *rest.RESTClient, pod *corev1.Pod, path string) error {
	cmd := []string{"/bin/sh", "-c", fmt.Sprintf("curl \"http://%s:8080/%s\"", pod.Status.PodIP, path)}
	_, _, err := KExecute(ctx, cfg, restClient, pod.Namespace, pod.Name, pod.Spec.Containers[0].Name, cmd)
	return err
}

func exitAgnHost(ctx context.Context, cfg *rest.Config, restClient *rest.RESTClient, pod *corev1.Pod, exitCode int) error {
	cmd := []string{"/bin/sh", "-c", fmt.Sprintf("curl \"http://%s:8080/exit?code=%v&timeout=2s&wait=2s\"", pod.Status.PodIP, exitCode)}
	_, _, err := KExecute(ctx, cfg, restClient, pod.Namespace, pod.Name, pod.Spec.Containers[0].Name, cmd)
	// TODO: remove the custom handling of 137 response once this is fixed in the agnhost image
	if err != nil && strings.Contains(err.Error(), "137") {
		return nil
	}
	return err
}
