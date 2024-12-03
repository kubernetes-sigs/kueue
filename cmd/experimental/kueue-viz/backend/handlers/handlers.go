package handlers

import (
	"github.com/gin-gonic/gin"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

func InitializeWebSocketRoutes(router *gin.Engine, dynamicClient dynamic.Interface, k8sClient *kubernetes.Clientset) {

	// Workloads
	router.GET("/ws/workloads", WorkloadsWebSocketHandler(dynamicClient, k8sClient))
	router.GET("/ws/workloads/dashboard", WorkloadsDashboardWebSocketHandler(dynamicClient, k8sClient))

	router.GET("/ws/workload/:namespace/:workload_name", WorkloadDetailsWebSocketHandler(dynamicClient))
	router.GET("/ws/workload/:namespace/:workload_name/events", WorkloadEventsWebSocketHandler(dynamicClient))

	// Local Queues
	router.GET("/ws/local-queues", LocalQueuesWebSocketHandler(dynamicClient))
	router.GET("/ws/local-queue/:namespace/:queue_name", LocalQueueDetailsWebSocketHandler(dynamicClient))
	router.GET("/ws/local-queue/:namespace/:queue_name/workloads", LocalQueueWorkloadsWebSocketHandler(dynamicClient))

	// Cluster Queues
	router.GET("/ws/cluster-queues", ClusterQueuesWebSocketHandler(dynamicClient))
	router.GET("/ws/cluster-queue/:cluster_queue_name", ClusterQueueDetailsWebSocketHandler(dynamicClient)) // New route

	// Cohorts
	router.GET("/ws/cohorts", CohortsWebSocketHandler(dynamicClient))
	router.GET("/ws/cohort/:cohort_name", CohortDetailsWebSocketHandler(dynamicClient))

	// Resource Flavors
	router.GET("/ws/resource-flavors", ResourceFlavorsWebSocketHandler(dynamicClient))
	router.GET("/ws/resource-flavor/:flavor_name", ResourceFlavorDetailsWebSocketHandler(dynamicClient))
}
