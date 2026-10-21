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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

func KExecute(ctx context.Context, cfg *rest.Config, client *rest.RESTClient, ns, pod, container string, command []string) ([]byte, []byte, error) {
	var out, outErr bytes.Buffer

	req := client.Post().
		Resource("pods").
		Namespace(ns).
		Name(pod).
		SubResource("exec").
		VersionedParams(
			&corev1.PodExecOptions{
				Container: container,
				Command:   command,
				Stdout:    true,
				Stderr:    true,
			},
			scheme.ParameterCodec,
		)

	executor, err := remotecommand.NewSPDYExecutor(cfg, "POST", req.URL())
	if err != nil {
		return nil, nil, err
	}

	// Return whatever was captured even on error: when the remote command exits
	// non-zero the streams still hold its output, and stderr is usually the only
	// explanation of the failure. Callers assert on err and report stderr with it.
	err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: &out, Stderr: &outErr})

	return out.Bytes(), outErr.Bytes(), err
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
	// We add the custom handling to protect in situation when the target pods completes with the expected
	// exit code but it terminates before it completes sending the response.
	if err != nil && strings.Contains(err.Error(), "137") {
		return nil
	}
	return err
}
