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

	kueueapi "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

// NamespacesWebSocketHandler streams namespaces that are related to Kueue
func (h *Handlers) NamespacesWebSocketHandler() gin.HandlerFunc {
	return h.GenericWebSocketHandler(func(ctx context.Context) (any, error) {
		return h.fetchNamespaces(ctx)
	})
}

// Fetch namespaces that have LocalQueues (Kueue-related namespaces)
func (h *Handlers) fetchNamespaces(ctx context.Context) (any, error) {
	l := &kueueapi.LocalQueueList{}

	// First, get all LocalQueues to find namespaces that have them
	err := h.client.List(ctx, l)
	if err != nil {
		return nil, err
	}

	// Extract unique namespaces from LocalQueues
	namespaceSet := make(map[string]struct{})
	for _, lq := range l.Items {
		namespaceSet[lq.GetNamespace()] = struct{}{}
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
