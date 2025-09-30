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
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/yaml"
)

// ResourceResponse represents the response structure for resource endpoints
type ResourceResponse struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Content string `json:"content"`
	Format  string `json:"format"`
}

var resourceGVRMap = map[string]schema.GroupVersionResource{
	"workload":       WorkloadsGVR(),
	"clusterqueue":   ClusterQueuesGVR(),
	"localqueue":     LocalQueuesGVR(),
	"resourceflavor": ResourceFlavorsGVR(),
	"cohort":         CohortsGVR(),
}

func GetResource(dynamicClient dynamic.Interface) gin.HandlerFunc {
	return func(c *gin.Context) {
		resourceType := c.Param("resourceType")
		name := c.Param("name")
		namespace := c.Query("namespace")
		outputType := c.Query("output")

		if outputType != "yaml" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": fmt.Sprintf("Unsupported output format: %s.", outputType),
			})
			return
		}

		gvr, ok := resourceGVRMap[resourceType]
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Unsupported resource type: %s", resourceType)})
			return
		}

		resource, fetchErr := func() (any, error) {
			return dynamicClient.Resource(gvr).Namespace(namespace).Get(c.Request.Context(), name, metav1.GetOptions{})
		}()

		if fetchErr != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Resource not found"})
			return
		}

		// Convert to YAML
		yamlBytes, err := yaml.Marshal(resource)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to process resource"})
			return
		}
		content := string(yamlBytes)

		response := ResourceResponse{
			Name:    name,
			Type:    resourceType,
			Content: content,
			Format:  outputType,
		}

		c.JSON(http.StatusOK, response)
	}
}
