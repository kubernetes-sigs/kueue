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
	"github.com/gin-gonic/gin"
	"k8s.io/client-go/dynamic"
)

type Handlers struct {
	client Client
}

func New(client Client) *Handlers {
	return &Handlers{
		client: client,
	}
}

func (h *Handlers) InitializeWebSocketRoutes(router *gin.Engine) {
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

func (h *Handlers) InitializeAPIRoutes(router *gin.Engine, dynamicClient dynamic.Interface) {
	// Generic API route
	router.GET("/api/:resourceType/:name", GetResource(dynamicClient))
}
