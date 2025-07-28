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
	"log"

	"kueueviz/config"
	"kueueviz/handlers"
)

func main() {
	// Initialize server configuration
	serverConfig := config.NewServerConfig()

	// Setup pprof for development
	config.SetupPprof()

	// Create Kubernetes client
	_, dynamicClient, err := createK8sClient()
	if err != nil {
		log.Fatalf("Error creating Kubernetes client: %v", err)
	}

	// Setup Gin engine with middleware
	r, err := config.SetupGinEngine()
	if err != nil {
		log.Fatalf("Error setting up Gin engine: %v", err)
	}

	// Initialize routes
	handlers.InitializeWebSocketRoutes(r, dynamicClient)
	handlers.InitializeAPIRoutes(r, dynamicClient)

	// Start server
	if err := r.Run(serverConfig.GetServerAddress()); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
