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
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-logr/logr"
	"kueueviz/config"
	"kueueviz/handlers"
	"kueueviz/middleware"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/kueue/pkg/util/routine"
)

const shutdownTimeout = 30 * time.Second

type managerRunner interface {
	Start(context.Context) error
}

type httpServer interface {
	ListenAndServe() error
	Shutdown(context.Context) error
}

func main() {
	if err := run(); err != nil {
		slog.Error("Server exited with error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	// Initialize server configuration
	serverConfig := config.NewServerConfig()

	// Setup pprof for development
	config.SetupPprof()

	// Create Kubernetes client
	dynamicClient, manager, clientset, err := createK8sClient(ctx)
	if err != nil {
		return fmt.Errorf("creating Kubernetes client: %w", err)
	}

	// Setup Gin engine with base middleware (logger, recovery, CORS)
	r, err := config.SetupGinEngine()
	if err != nil {
		return fmt.Errorf("setting up Gin engine: %w", err)
	}

	// The timeouts below are configured to prevent Denial-of-Service (DoS) attacks,
	// such as Slowloris, where attackers can exhaust the server's resources by
	// keeping many connections open indefinitely, leading to a Denial of Service
	// (DoS) for legitimate users.
	srv := &http.Server{
		Addr:    serverConfig.GetServerAddress(),
		Handler: r.Handler(),
		// ReadHeaderTimeout is the amount of time allowed to read request headers.
		// Set to 32s to align with Kubernetes and controller-runtime standards.
		ReadHeaderTimeout: 32 * time.Second,
		// ReadTimeout is the maximum duration for reading the entire request, including the body.
		// Set to 120s to bound standard REST APIs (e.g., /api/*, /healthz). Websockets (e.g., /ws/*) are unaffected.
		ReadTimeout: 120 * time.Second,
		// WriteTimeout is the maximum duration before timing out writes of the response.
		// Set to 120s to bound standard REST APIs (e.g., /api/*, /healthz). Websockets (e.g., /ws/*) are unaffected.
		WriteTimeout: 120 * time.Second,
		// IdleTimeout is the maximum amount of time to wait for the next request when keep-alives are enabled.
		// Set to 90s to align with Kubernetes and controller-runtime standards.
		IdleTimeout: 90 * time.Second,
	}

	h := handlers.New(handlers.NewClientFromManager(manager), nil, nil)

	publicRoutes := r.Group("/")
	handlers.InitializeUnauthenticatedRoutes(publicRoutes, serverConfig.AuthMode)

	protectedRoutes := r.Group("/")
	if serverConfig.AuthMode == "TokenReview" {
		// Apply a two-level rate limiter to all protected routes to prevent
		// TokenReview amplification:
		// 1. Per-IP: 100 rps sustained, burst of 100
		// 2. Global: 500 rps sustained, burst of 500
		protectedRoutes.Use(middleware.RateLimiter(100, 100, 500, 500))
		slog.Info("Authentication enabled", "mode", serverConfig.AuthMode)
		auth := middleware.NewAuthenticator(clientset, serverConfig.AuthConfig)
		protectedRoutes.Use(auth.Middleware())
		authorizer := middleware.NewSARAuthorizer(clientset, serverConfig.AuthConfig)
		h = handlers.New(handlers.NewClientFromManager(manager), auth, authorizer)
	}

	h.InitializeWebSocketRoutes(protectedRoutes)
	h.InitializeAPIRoutes(protectedRoutes, dynamicClient)

	// Set up controller-runtime logging with slog
	ctrllog.SetLogger(logr.FromSlogHandler(slog.Default().Handler()))

	slog.Info("Starting server", "address", srv.Addr)
	return runComponents(ctx, manager, srv, shutdownTimeout)
}

func runComponents(parentCtx context.Context, manager managerRunner, srv httpServer, shutdownTimeout time.Duration) error {
	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	managerErrCh := routine.NewErrorChannel()
	serverErrCh := routine.NewErrorChannel()
	componentDone := make(chan struct{}, 2)
	wrapper := routine.NewWrapper(nil, func() {
		componentDone <- struct{}{}
	})

	wrapper.Run(func() {
		err := manager.Start(ctx)
		if err != nil {
			managerErrCh.SendErrorWithCancel(fmt.Errorf("running manager: %w", err), cancel)
		} else if ctx.Err() == nil {
			managerErrCh.SendErrorWithCancel(errors.New("manager stopped unexpectedly"), cancel)
		}
	})

	wrapper.Run(func() {
		err := srv.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrCh.SendErrorWithCancel(fmt.Errorf("running HTTP server: %w", err), cancel)
		} else if ctx.Err() == nil {
			serverErrCh.SendErrorWithCancel(errors.New("HTTP server stopped unexpectedly"), cancel)
		}
	})

	<-ctx.Done()
	var runErr error
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		runErr = errors.Join(runErr, fmt.Errorf("shutting down HTTP server: %w", err))
	}

	if err := waitForShutdown(shutdownCtx, componentDone, 2); err != nil {
		runErr = errors.Join(runErr, err)
	}

	return errors.Join(runErr, managerErrCh.ReceiveError(), serverErrCh.ReceiveError())
}

func waitForShutdown(ctx context.Context, componentDone <-chan struct{}, componentCount int) error {
	for range componentCount {
		select {
		case <-componentDone:
			continue
		case <-ctx.Done():
		}

		// The component might have completed at the same time as the deadline.
		select {
		case <-componentDone:
			continue
		default:
			return fmt.Errorf("waiting for components to stop: %w", ctx.Err())
		}
	}
	return nil
}
