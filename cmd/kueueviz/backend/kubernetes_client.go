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

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	corev1 "k8s.io/api/core/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/dynamic"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	kueueapi "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

var (
	scheme = runtime.NewScheme()
)

// createK8sClient initializes Kubernetes clients, checking for in-cluster or local kubeconfig
func createK8sClient(ctx context.Context) (dynamic.Interface, manager.Manager, error) {
	var config *rest.Config
	var err error

	// Check for in-cluster configuration
	if _, err := os.Stat("/var/run/secrets/kubernetes.io/serviceaccount/token"); err == nil {
		config, err = rest.InClusterConfig()
		if err != nil {
			return nil, nil, fmt.Errorf("failed to load in-cluster config: %v", err)
		}
		slog.Info("Using in-cluster configuration")
	} else {
		// Fall back to using KUBECONFIG or default kubeconfig path
		kubeconfig := os.Getenv("KUBECONFIG")
		if kubeconfig == "" {
			kubeconfig = filepath.Join(os.Getenv("HOME"), ".kube", "config")
		}

		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to load kubeconfig from %s: %v", kubeconfig, err)
		}
		slog.Info("Using kubeconfig", "path", kubeconfig)
	}

	// Create the dynamic client
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create dynamic client: %v", err)
	}

	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(schedulingv1.AddToScheme(scheme))
	utilruntime.Must(corev1.AddToScheme(scheme))
	utilruntime.Must(kueueapi.AddToScheme(scheme))

	opts := ctrl.Options{
		Scheme:  scheme,
		Metrics: metricsserver.Options{BindAddress: "0"}, // Disable metrics serving
	}

	mngr, err := ctrl.NewManager(config, opts)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create controller-runtime manager: %v", err)
	}

	err = mngr.GetFieldIndexer().IndexField(ctx, &corev1.Event{}, "involvedObject.name", func(object client.Object) []string {
		event, _ := object.(*corev1.Event)
		return []string{event.InvolvedObject.Name}
	})

	if err != nil {
		return nil, nil, fmt.Errorf("failed to index events by involvedObject.name: %v", err)
	}

	return dynamicClient, mngr, nil
}
