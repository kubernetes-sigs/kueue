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
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"k8s.io/client-go/dynamic"
)

// TokenValidator is implemented by middleware.Authenticator and allows
// WebSocket handlers to periodically re-verify a bearer token.
type TokenValidator interface {
	ValidateToken(ctx context.Context, token string) (bool, error)
}

type Handlers struct {
	client    Client
	validator TokenValidator // nil when auth is disabled
	// tokenRevalidationInterval controls how often a live WebSocket connection
	// re-verifies the bearer token. Defaults to 30 s; overridable in tests.
	tokenRevalidationInterval time.Duration
}

func New(client Client, validator TokenValidator) *Handlers {
	return &Handlers{
		client:                    client,
		validator:                 validator,
		tokenRevalidationInterval: 30 * time.Second,
	}
}

func (h *Handlers) InitializeWebSocketRoutes(router gin.IRoutes) {
	// Namespaces
	router.GET("/ws/namespaces", h.NamespacesWebSocketHandler())

	// Workloads
	router.GET("/ws/workloads", h.WorkloadsWebSocketHandler())
	router.GET("/ws/workloads/dashboard", h.WorkloadsDashboardWebSocketHandler())

	router.GET("/ws/workload/:namespace/:workload_name", h.WorkloadDetailsWebSocketHandler())
	router.GET("/ws/workload/:namespace/:workload_name/events", h.WorkloadEventsWebSocketHandler())

	// Local Queues
	router.GET("/ws/local-queues", h.LocalQueuesWebSocketHandler())
	router.GET("/ws/local-queue/:namespace/:queue_name", h.LocalQueueDetailsWebSocketHandler())
	router.GET("/ws/local-queue/:namespace/:queue_name/workloads", h.LocalQueueWorkloadsWebSocketHandler())

	// Cluster Queues
	router.GET("/ws/cluster-queues", h.ClusterQueuesWebSocketHandler())
	router.GET("/ws/cluster-queue/:cluster_queue_name", h.ClusterQueueDetailsWebSocketHandler()) // New route

	// Cohorts
	router.GET("/ws/cohorts", h.CohortsWebSocketHandler())
	router.GET("/ws/cohort/:cohort_name", h.CohortDetailsWebSocketHandler())

	// Resource Flavors
	router.GET("/ws/resource-flavors", h.ResourceFlavorsWebSocketHandler())
	router.GET("/ws/resource-flavor/:flavor_name", h.ResourceFlavorDetailsWebSocketHandler())
}

func (h *Handlers) InitializeAPIRoutes(router gin.IRoutes, dynamicClient dynamic.Interface) {
	router.GET("/api/:resourceType/:name", GetResource(dynamicClient))
}

func InitializeUnauthenticatedRoutes(router gin.IRoutes, authMode string) {
	router.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	router.GET("/auth/status", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"authMode": authMode})
	})
}
