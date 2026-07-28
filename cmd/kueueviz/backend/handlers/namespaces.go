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

	"github.com/gin-gonic/gin"
	"k8s.io/apimachinery/pkg/util/sets"
	"kueueviz/middleware"

	kueueapi "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

// NamespacesWebSocketHandler streams namespaces that are related to Kueue
func (h *Handlers) NamespacesWebSocketHandler() gin.HandlerFunc {
	// Use LocalQueue informer since namespaces are derived from LocalQueues
	return func(c *gin.Context) {
		identity, _ := middleware.IdentityFromContext(c)
		h.GenericWebSocketHandler(func(ctx context.Context) (any, error) {
			return h.fetchNamespaces(ctx, identity)
		}, LocalQueuesGVK())(c)
	}
}

// Fetch namespaces that have LocalQueues (Kueue-related namespaces)
func (h *Handlers) fetchNamespaces(ctx context.Context, identity middleware.Identity) (any, error) {
	lql := &kueueapi.LocalQueueList{}

	// First, get all LocalQueues to find namespaces that have them
	err := h.client.List(ctx, lql)
	if err != nil {
		return nil, err
	}

	// Extract unique namespaces from LocalQueues
	namespaceSet := sets.NewString()
	for _, lq := range lql.Items {
		ns := lq.GetNamespace()
		if !namespaceSet.Has(ns) {
			if h.authorizer != nil {
				allowed, err := h.authorizer.Authorize(ctx, identity, middleware.ResourceAccess("list", LocalQueuesGVR(), ns, ""))
				if err != nil || !allowed {
					continue
				}
			}
			namespaceSet.Insert(ns)
		}
	}

	namespaces := namespaceSet.List()
	if namespaceSet.Len() == 0 {
		namespaces = []string{}
	}

	hasClusterAccess := true
	if h.authorizer != nil {
		allowed, err := h.authorizer.Authorize(ctx, identity, middleware.ResourceAccess("list", LocalQueuesGVR(), "", ""))
		if err != nil || !allowed {
			hasClusterAccess = false
		}
	}

	return map[string]any{
		"namespaces":       namespaces,
		"hasClusterAccess": hasClusterAccess,
	}, nil
}
