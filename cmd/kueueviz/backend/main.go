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

package main

import (
	"fmt"
	"log"

	"kueueviz/handlers"

	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"

	"net/http"
	_ "net/http/pprof"
)

func main() {
	viper.AutomaticEnv()
	// Start pprof server for profiling
	go func() {
		log.Println("Starting pprof server on :6060")
		if err := http.ListenAndServe("localhost:6060", nil); err != nil {
			log.Fatalf("Error starting pprof server: %v", err)
		}
	}()

	k8sClient, dynamicClient, err := createK8sClient()
	if err != nil {
		log.Fatalf("Error creating Kubernetes client: %v", err)
	}
	r := gin.New()
	r.Use(gin.Logger())
	r.SetTrustedProxies(nil)

	handlers.InitializeWebSocketRoutes(r, dynamicClient, k8sClient)

	viper.SetDefault("KUEUEVIZ_PORT", "8080")
	if err := r.Run(fmt.Sprintf(":%s", viper.GetString("KUEUEVIZ_PORT"))); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}

}
