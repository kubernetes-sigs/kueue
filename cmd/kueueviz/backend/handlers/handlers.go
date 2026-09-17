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
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/client-go/dynamic"
	"kueueviz/middleware"
)

// TokenValidator is implemented by middleware.Authenticator and allows
// WebSocket handlers to periodically re-verify a bearer token.
type TokenValidator interface {
	ValidateToken(ctx context.Context, token string) (bool, error)
}

type Handlers struct {
	client     Client
	validator  TokenValidator
	authorizer middleware.Authorizer
	// tokenRevalidationInterval controls how often a live WebSocket connection
	// re-verifies the bearer token. Defaults to 30 s; overridable in tests.
	tokenRevalidationInterval time.Duration

	// heartbeatInterval controls how often a WebSocket ping control frame is sent.
	// Defaults to 30 s; overridable in tests.
	heartbeatInterval time.Duration
}

func New(client Client, validator TokenValidator, authorizer middleware.Authorizer) *Handlers {
	return &Handlers{
		client:                    client,
		validator:                 validator,
		authorizer:                authorizer,
		tokenRevalidationInterval: 30 * time.Second,
		heartbeatInterval:         30 * time.Second,
	}
}

func (h *Handlers) InitializeWebSocketRoutes(router gin.IRoutes) {
	// Namespaces
	router.GET("/ws/namespaces", h.NamespacesWebSocketHandler())

	// Workloads
	router.GET("/ws/workloads", middleware.RequireAuthorization(h.authorizer, func(c *gin.Context) []authorizationv1.ResourceAttributes {
		ns := c.Query("namespace")
		if c.Request.URL.Query().Has("namespace") && ns == "" {
			middleware.AbortWithWebSocketAwareStatus(c, http.StatusBadRequest, "namespace query parameter is required", middleware.WSCloseBadRequest)
			return nil
		}
		return []authorizationv1.ResourceAttributes{
			middleware.ResourceAccess("list", WorkloadsGVR(), ns, ""),
			middleware.ResourceAccess("watch", WorkloadsGVR(), ns, ""),
		}
	}), h.WorkloadsWebSocketHandler())

	router.GET("/ws/workloads/dashboard", middleware.RequireAuthorization(h.authorizer, func(c *gin.Context) []authorizationv1.ResourceAttributes {
		ns := c.Query("namespace")
		if c.Request.URL.Query().Has("namespace") && ns == "" {
			middleware.AbortWithWebSocketAwareStatus(c, http.StatusBadRequest, "namespace query parameter is required", middleware.WSCloseBadRequest)
			return nil
		}
		return []authorizationv1.ResourceAttributes{
			middleware.ResourceAccess("list", WorkloadsGVR(), ns, ""),
			middleware.ResourceAccess("watch", WorkloadsGVR(), ns, ""),
		}
	}), h.WorkloadsDashboardWebSocketHandler())

	router.GET("/ws/workload/:namespace/:workload_name", middleware.RequireAuthorization(h.authorizer, func(c *gin.Context) []authorizationv1.ResourceAttributes {
		return []authorizationv1.ResourceAttributes{
			middleware.ResourceAccess("get", WorkloadsGVR(), c.Param("namespace"), c.Param("workload_name")),
			middleware.ResourceAccess("watch", WorkloadsGVR(), c.Param("namespace"), c.Param("workload_name")),
		}
	}), h.WorkloadDetailsWebSocketHandler())

	router.GET("/ws/workload/:namespace/:workload_name/events", middleware.RequireAuthorization(h.authorizer, func(c *gin.Context) []authorizationv1.ResourceAttributes {
		return []authorizationv1.ResourceAttributes{
			middleware.ResourceAccess("get", WorkloadsGVR(), c.Param("namespace"), c.Param("workload_name")),
			middleware.ResourceAccess("list", EventsGVR(), c.Param("namespace"), ""),
			middleware.ResourceAccess("watch", EventsGVR(), c.Param("namespace"), ""),
		}
	}), h.WorkloadEventsWebSocketHandler())

	router.GET("/ws/local-queues", middleware.RequireAuthorization(h.authorizer, func(c *gin.Context) []authorizationv1.ResourceAttributes {
		ns := c.Query("namespace")
		if c.Request.URL.Query().Has("namespace") && ns == "" {
			middleware.AbortWithWebSocketAwareStatus(c, http.StatusBadRequest, "namespace query parameter is required", middleware.WSCloseBadRequest)
			return nil
		}
		return []authorizationv1.ResourceAttributes{
			middleware.ResourceAccess("list", LocalQueuesGVR(), ns, ""),
			middleware.ResourceAccess("watch", LocalQueuesGVR(), ns, ""),
		}
	}), h.LocalQueuesWebSocketHandler())

	router.GET("/ws/local-queue/:namespace/:queue_name", middleware.RequireAuthorization(h.authorizer, func(c *gin.Context) []authorizationv1.ResourceAttributes {
		return []authorizationv1.ResourceAttributes{
			middleware.ResourceAccess("get", LocalQueuesGVR(), c.Param("namespace"), c.Param("queue_name")),
			middleware.ResourceAccess("watch", LocalQueuesGVR(), c.Param("namespace"), c.Param("queue_name")),
		}
	}), h.LocalQueueDetailsWebSocketHandler())

	router.GET("/ws/local-queue/:namespace/:queue_name/workloads", middleware.RequireAuthorization(h.authorizer, func(c *gin.Context) []authorizationv1.ResourceAttributes {
		return []authorizationv1.ResourceAttributes{
			middleware.ResourceAccess("list", WorkloadsGVR(), c.Param("namespace"), ""),
			middleware.ResourceAccess("watch", WorkloadsGVR(), c.Param("namespace"), ""),
		}
	}), h.LocalQueueWorkloadsWebSocketHandler())

	// Cluster Queues
	router.GET("/ws/cluster-queues", middleware.RequireAuthorization(h.authorizer, func(c *gin.Context) []authorizationv1.ResourceAttributes {
		return []authorizationv1.ResourceAttributes{
			middleware.ResourceAccess("list", ClusterQueuesGVR(), "", ""),
			middleware.ResourceAccess("watch", ClusterQueuesGVR(), "", ""),
		}
	}), h.ClusterQueuesWebSocketHandler())

	router.GET("/ws/cluster-queue/:cluster_queue_name", middleware.RequireAuthorization(h.authorizer, func(c *gin.Context) []authorizationv1.ResourceAttributes {
		return []authorizationv1.ResourceAttributes{
			middleware.ResourceAccess("get", ClusterQueuesGVR(), "", c.Param("cluster_queue_name")),
			middleware.ResourceAccess("watch", ClusterQueuesGVR(), "", c.Param("cluster_queue_name")),
		}
	}), h.ClusterQueueDetailsWebSocketHandler())

	// Cohorts
	router.GET("/ws/cohorts", middleware.RequireAuthorization(h.authorizer, func(c *gin.Context) []authorizationv1.ResourceAttributes {
		return []authorizationv1.ResourceAttributes{
			middleware.ResourceAccess("list", CohortsGVR(), "", ""),
			middleware.ResourceAccess("watch", CohortsGVR(), "", ""),
		}
	}), h.CohortsWebSocketHandler())

	router.GET("/ws/cohort/:cohort_name", middleware.RequireAuthorization(h.authorizer, func(c *gin.Context) []authorizationv1.ResourceAttributes {
		return []authorizationv1.ResourceAttributes{
			middleware.ResourceAccess("get", CohortsGVR(), "", c.Param("cohort_name")),
			middleware.ResourceAccess("watch", CohortsGVR(), "", c.Param("cohort_name")),
		}
	}), h.CohortDetailsWebSocketHandler())

	// Resource Flavors
	router.GET("/ws/resource-flavors", middleware.RequireAuthorization(h.authorizer, func(c *gin.Context) []authorizationv1.ResourceAttributes {
		return []authorizationv1.ResourceAttributes{
			middleware.ResourceAccess("list", ResourceFlavorsGVR(), "", ""),
			middleware.ResourceAccess("watch", ResourceFlavorsGVR(), "", ""),
		}
	}), h.ResourceFlavorsWebSocketHandler())

	router.GET("/ws/resource-flavor/:flavor_name", middleware.RequireAuthorization(h.authorizer, func(c *gin.Context) []authorizationv1.ResourceAttributes {
		return []authorizationv1.ResourceAttributes{
			middleware.ResourceAccess("get", ResourceFlavorsGVR(), "", c.Param("flavor_name")),
			middleware.ResourceAccess("watch", ResourceFlavorsGVR(), "", c.Param("flavor_name")),
		}
	}), h.ResourceFlavorDetailsWebSocketHandler())
}

func (h *Handlers) InitializeAPIRoutes(router gin.IRoutes, dynamicClient dynamic.Interface) {
	router.GET("/api/:resourceType/:name", middleware.RequireAuthorization(h.authorizer, func(c *gin.Context) []authorizationv1.ResourceAttributes {
		resourceType := c.Param("resourceType")
		name := c.Param("name")
		namespace := c.Query("namespace")
		gvr, ok := resourceGVRMap[resourceType]
		if !ok {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("Unsupported resource type: %s", resourceType)})
			return nil
		}

		isNamespaced := resourceType == "workload" || resourceType == "localqueue" || resourceType == "event" || resourceType == "pod"
		if isNamespaced && namespace == "" {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "namespace query parameter is required for this resource"})
			return nil
		}

		return []authorizationv1.ResourceAttributes{middleware.ResourceAccess("get", gvr, namespace, name)}
	}), GetResource(dynamicClient))
}

func InitializeUnauthenticatedRoutes(router gin.IRoutes, authMode string) {
	router.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	router.GET("/auth/status", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"authMode": authMode})
	})
}
