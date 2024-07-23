/*
Copyright 2024 The Kubernetes Authors.

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

package create

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/client-go/clientset/versioned/scheme"
)

// setKubernetesDefaults sets default values on the provided client config for accessing the
// Kubernetes API or returns an error if any of the defaults are impossible or invalid.
// copied from https://github.com/kubernetes/kubectl/blob/a6de79e3d40575480caa71a8e5efffeea6587251/pkg/cmd/util/kubectl_match_version.go#L115
func setKubernetesDefaults(config *rest.Config) error {
	// This is allowing the GetOptions to be serialized.
	config.GroupVersion = &schema.GroupVersion{Group: "", Version: "v1"}

	if config.APIPath == "" {
		config.APIPath = "/api"
	}
	if config.NegotiatedSerializer == nil {
		// This codec factory ensures the resources are not converted. Therefore, resources
		// will not be round-tripped through internal versions. Defaulting does not happen
		// on the client.
		config.NegotiatedSerializer = scheme.Codecs.WithoutConversion()
	}
	return rest.SetKubernetesDefaults(config)
}

// return a condition function that indicates whether the given pod is currently running
func isPodRunning(ctx context.Context, k8sClient kubernetes.Interface, podName, namespace string) wait.ConditionWithContextFunc {
	return func(ct context.Context) (bool, error) {
		pod, err := k8sClient.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		return pod.Status.Phase == corev1.PodRunning, nil
	}
}

// Poll up to timeout seconds for pod to enter running state.
// Returns an error if the pod never enters the running state.
func waitForPodRunning(ctx context.Context, k8sClient kubernetes.Interface, namespace, podName string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, time.Second, timeout, true, isPodRunning(ctx, k8sClient, podName, namespace))
}
