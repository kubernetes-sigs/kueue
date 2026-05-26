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
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/go-logr/logr"
	"kueueviz/config"
	"kueueviz/handlers"
	"kueueviz/middleware"
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
	dynamicClient, manager, clientset, err := createK8sClient(ctx)
	if err != nil {
		slog.Error("Error creating Kubernetes client", "error", err)
		os.Exit(1)
	}

	// Setup Gin engine with base middleware (logger, recovery, CORS)
	r, err := config.SetupGinEngine()
	if err != nil {
		slog.Error("Error setting up Gin engine", "error", err)
		os.Exit(1)
	}

	srv := &http.Server{
		Addr:    serverConfig.GetServerAddress(),
		Handler: r.Handler(),
	}

	h := handlers.New(handlers.NewClientFromManager(manager))

	publicRoutes := r.Group("/")
	handlers.InitializeUnauthenticatedRoutes(publicRoutes, serverConfig.AuthMode)

	protectedRoutes := r.Group("/")
	if serverConfig.AuthMode == "TokenReview" {
		slog.Info("Authentication enabled", "mode", serverConfig.AuthMode)
		auth := middleware.NewAuthenticator(clientset, serverConfig.AuthConfig)
		protectedRoutes.Use(auth.Middleware())
	}

	h.InitializeWebSocketRoutes(protectedRoutes)
	h.InitializeAPIRoutes(protectedRoutes, dynamicClient)

	// Set up controller-runtime logging with slog
	ctrllog.SetLogger(logr.FromSlogHandler(slog.Default().Handler()))

	// Start manager in a separate goroutine
	go func() {
		if err = manager.Start(ctx); err != nil {
			slog.Error("Failed to start manager", "error", err)
			cancel()
			os.Exit(1)
		}
	}()

	// Start HTTP server in a separate goroutine
	go func() {
		slog.Info("Starting server", "address", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("Failed to start HTTP server", "error", err)
			cancel()
			os.Exit(1)
		}
	}()

	<-ctx.Done()

	// Shutdown the server gracefully
	if err := srv.Shutdown(context.Background()); err != nil {
		slog.Error("Server forced to shutdown", "error", err)
		cancel()
		os.Exit(1)
	}
}
