package main

import (
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// createK8sClient initializes Kubernetes clients, checking for in-cluster or local kubeconfig
func createK8sClient() (*kubernetes.Clientset, dynamic.Interface, error) {
	var config *rest.Config
	var err error

	// Check for in-cluster configuration
	if _, err := os.Stat("/var/run/secrets/kubernetes.io/serviceaccount/token"); err == nil {
		config, err = rest.InClusterConfig()
		if err != nil {
			return nil, nil, fmt.Errorf("failed to load in-cluster config: %v", err)
		}
		fmt.Println("Using in-cluster configuration")
	} else {
		// Fall back to using local kubeconfig
		kubeconfig := filepath.Join(os.Getenv("HOME"), ".kube", "config")
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to load local kubeconfig: %v", err)
		}
		fmt.Println("Using local kubeconfig")
	}

	// Create the Kubernetes clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create Kubernetes clientset: %v", err)
	}

	// Create the dynamic client
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create dynamic client: %v", err)
	}

	return clientset, dynamicClient, nil
}
