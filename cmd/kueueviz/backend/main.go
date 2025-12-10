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
	"context"
	"errors"
	"log"
	"net/http"
	"os/signal"
	"syscall"

	"github.com/go-logr/stdr"
	"kueueviz/config"
	"kueueviz/handlers"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	// Initialize server configuration
	serverConfig := config.NewServerConfig()

	// Setup pprof for development
	config.SetupPprof()

	// Create Kubernetes client
	dynamicClient, manager, err := createK8sClient(ctx)
	if err != nil {
		log.Fatalf("Error creating Kubernetes client: %v", err)
	}

	// Setup Gin engine with middleware
	r, err := config.SetupGinEngine()
	if err != nil {
		log.Fatalf("Error setting up Gin engine: %v", err)
	}

	srv := &http.Server{
		Addr:    serverConfig.GetServerAddress(),
		Handler: r.Handler(),
	}

	h := handlers.New(handlers.NewClientFromManager(manager))

	// Initialize routes
	h.InitializeWebSocketRoutes(r)
	h.InitializeAPIRoutes(r, dynamicClient)

	ctrllog.SetLogger(stdr.New(log.Default()))

	// Start manager in a separate goroutine
	go func() {
		if err = manager.Start(ctx); err != nil {
			log.Fatalf("Failed to start manager: %v", err)
		}
	}()

	// Start HTTP server in a separate goroutine
	go func() {
		log.Printf("Starting server on %s", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("Failed to start HTTP server: %v", err)
		}
	}()

	<-ctx.Done()

	// Shutdown the server gracefully
	if err := srv.Shutdown(context.Background()); err != nil {
		log.Fatalf("Server forced to shutdown: %v", err)
	}
}
