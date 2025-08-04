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
	"sort"

	"github.com/gin-gonic/gin"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
)

// NamespacesWebSocketHandler streams namespaces that are related to Kueue
func NamespacesWebSocketHandler(dynamicClient dynamic.Interface) gin.HandlerFunc {
	return GenericWebSocketHandler(func(ctx context.Context) (any, error) {
		return fetchNamespaces(ctx, dynamicClient)
	})
}

// Fetch namespaces that have LocalQueues (Kueue-related namespaces)
func fetchNamespaces(ctx context.Context, dynamicClient dynamic.Interface) (any, error) {
	// First, get all LocalQueues to find namespaces that have them
	localQueues, err := dynamicClient.Resource(LocalQueuesGVR()).List(ctx, metav1.ListOptions{})
	if err != nil {
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

	// If no LocalQueues found, return empty result with proper structure
	if len(namespaceSet) == 0 {
		return map[string]any{
			"namespaces": []string{},
		}, nil
	}

	// Convert namespace set to a slice of strings (just the names)
	var namespaceNames []string
	for namespaceName := range namespaceSet {
		namespaceNames = append(namespaceNames, namespaceName)
	}

	// Sort namespaces alphabetically for consistent ordering
	sort.Strings(namespaceNames)

	// Return in the same format as other endpoints
	result := map[string]any{
		"namespaces": namespaceNames,
	}

	return result, nil
}
