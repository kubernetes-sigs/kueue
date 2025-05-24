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

package handlers

import (
	"context"
	"fmt"

	"github.com/gin-gonic/gin"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// NamespacesWebSocketHandler streams namespaces that are related to Kueue
func NamespacesWebSocketHandler(dynamicClient dynamic.Interface, clientset *kubernetes.Clientset) gin.HandlerFunc {
	return GenericWebSocketHandler(func() (any, error) {
		return fetchNamespaces(dynamicClient, clientset)
	})
}

// Fetch namespaces that have LocalQueues (Kueue-related namespaces)
func fetchNamespaces(dynamicClient dynamic.Interface, clientset *kubernetes.Clientset) (any, error) {
	fmt.Println("Fetching Kueue-related namespaces (those with LocalQueues)...")

	// First, get all LocalQueues to find namespaces that have them
	localQueues, err := dynamicClient.Resource(LocalQueuesGVR()).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		fmt.Printf("Error fetching LocalQueues: %v\n", err)
		return nil, err
	}

	// Extract unique namespaces from LocalQueues
	namespaceSet := make(map[string]bool)
	for _, lq := range localQueues.Items {
		namespace := lq.GetNamespace()
		if namespace != "" {
			namespaceSet[namespace] = true
		}
	}

	fmt.Printf("Found %d LocalQueues across %d namespaces\n", len(localQueues.Items), len(namespaceSet))

	// If no LocalQueues found, return empty result with proper structure
	if len(namespaceSet) == 0 {
		fmt.Println("No LocalQueues found - returning empty namespace list")
		return map[string]interface{}{
			"items": []interface{}{},
		}, nil
	}

	// Fetch full namespace objects for the namespaces that have LocalQueues
	var kueuedNamespaces []interface{}
	for namespaceName := range namespaceSet {
		ns, err := clientset.CoreV1().Namespaces().Get(context.TODO(), namespaceName, metav1.GetOptions{})
		if err != nil {
			fmt.Printf("Warning: Could not fetch namespace %s: %v\n", namespaceName, err)
			continue
		}
		kueuedNamespaces = append(kueuedNamespaces, ns)
	}

	fmt.Printf("Successfully fetched %d Kueue-related namespaces\n", len(kueuedNamespaces))

	// Return in the same format as the K8s API would
	result := map[string]interface{}{
		"items": kueuedNamespaces,
	}

	// Debug: print namespace names for clarity
	fmt.Print("Kueue-related namespaces: ")
	for namespaceName := range namespaceSet {
		fmt.Printf("%s ", namespaceName)
	}
	fmt.Println()

	return result, nil
}
