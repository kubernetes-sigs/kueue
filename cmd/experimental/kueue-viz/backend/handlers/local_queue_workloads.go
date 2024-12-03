package handlers

import (
	"context"
	"fmt"

	"github.com/gin-gonic/gin"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
)

func LocalQueueWorkloadsWebSocketHandler(dynamicClient dynamic.Interface) gin.HandlerFunc {
	return func(c *gin.Context) {
		namespace := c.Param("namespace")
		queueName := c.Param("queue_name")
		GenericWebSocketHandler(dynamicClient, WorkloadsGVR(), namespace, func() (interface{}, error) {
			return fetchLocalQueueWorkloads(dynamicClient, namespace, queueName)
		})(c)
	}
}

func fetchLocalQueueWorkloads(dynamicClient dynamic.Interface, namespace, queueName string) (interface{}, error) {
	result, err := dynamicClient.Resource(WorkloadsGVR()).Namespace(namespace).List(context.TODO(), metav1.ListOptions{
		FieldSelector: fmt.Sprintf("spec.queueName=%s", queueName),
	})
	if err != nil {
		return nil, fmt.Errorf("error fetching workloads for local queue %s: %v", queueName, err)
	}

	var workloads []map[string]interface{}
	for _, item := range result.Items {
		workloads = append(workloads, item.Object)
	}
	return workloads, nil
}
