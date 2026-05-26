/*
Copyright 2026 The Kubeflow Authors.

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

package statusserver

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	configapi "github.com/kubeflow/trainer/v2/pkg/apis/config/v1alpha1"
	trainer "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
	trainerv1alpha1ac "github.com/kubeflow/trainer/v2/pkg/client/applyconfiguration/trainer/v1alpha1"
)

const (
	shutdownTimeout = 5 * time.Second

	// HTTP Server timeouts to prevent resource exhaustion
	readTimeout  = 10 * time.Second
	writeTimeout = 10 * time.Second
	idleTimeout  = 120 * time.Second

	// Maximum request body size (64kB)
	maxBodySize = 1 << 16
)

// Server for collecting runtime status updates.
type Server struct {
	log        logr.Logger
	httpServer *http.Server
	client     client.Client
	authorizer TokenAuthorizer
}

var (
	_ manager.Runnable               = &Server{}
	_ manager.LeaderElectionRunnable = &Server{}
)

// NewServer creates a new Server for collecting runtime status updates.
func NewServer(c client.Client, cfg *configapi.StatusServer, tlsConfig *tls.Config, authorizer TokenAuthorizer) (*Server, error) {
	if cfg == nil || cfg.Port == nil {
		return nil, fmt.Errorf("cfg info is required")
	}
	if tlsConfig == nil {
		return nil, fmt.Errorf("tlsConfig is required")
	}
	if authorizer == nil {
		return nil, fmt.Errorf("authorizer is required")
	}

	log := ctrl.Log.WithName("runtime-status")

	s := &Server{
		log:        log,
		client:     c,
		authorizer: authorizer,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST "+StatusUrl("{namespace}", "{name}"), s.handleTrainJobRuntimeStatus)
	mux.HandleFunc("/", s.handleDefault)

	// Apply middleware (authentication happens in handler)
	handler := chain(mux,
		recoveryMiddleware(log),
		loggingMiddleware(log),
		bodySizeLimitMiddleware(log, maxBodySize),
	)

	httpServer := http.Server{
		Addr:         fmt.Sprintf(":%d", *cfg.Port),
		Handler:      handler,
		TLSConfig:    tlsConfig,
		ErrorLog:     slog.NewLogLogger(logr.ToSlogHandler(log), slog.LevelInfo),
		ReadTimeout:  readTimeout,
		WriteTimeout: writeTimeout,
		IdleTimeout:  idleTimeout,
	}

	s.httpServer = &httpServer

	return s, nil
}

// Start implements manager.Runnable and starts the HTTPS Server.
// It blocks until the Server stops, either due to an error or graceful shutdown.
func (s *Server) Start(ctx context.Context) error {
	s.log.Info("Initializing token authorizer")
	if err := s.authorizer.Init(ctx); err != nil {
		return fmt.Errorf("token authorizer initialization failed: %w", err)
	}

	// Handle graceful shutdown in background
	serverShutdown := make(chan struct{})
	go func() {
		<-ctx.Done()
		s.log.Info("Shutting down runtime status server")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
			s.log.Error(err, "Error shutting down runtime status server")
		}
	}()

	s.log.Info("Starting runtime status server with TLS", "address", s.httpServer.Addr)
	if err := s.httpServer.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("runtime status server failed: %w", err)
	}

	<-serverShutdown
	return nil
}

func (s *Server) NeedLeaderElection() bool {
	// server needs to run on all replicas
	return false
}

// handleTrainJobRuntimeStatus handles POST requests to update TrainJob status.
// Expected URL format: /apis/trainer.kubeflow.org/v1alpha1/namespaces/{namespace}/trainjobs/{name}/status
func (s *Server) handleTrainJobRuntimeStatus(w http.ResponseWriter, r *http.Request) {

	namespace := r.PathValue("namespace")
	trainJobName := r.PathValue("name")

	authorized, err := s.authorizer.Authorize(r.Context(), r.Header.Get("Authorization"), namespace, trainJobName)
	if err != nil {
		badRequest(w, s.log, "Internal error", metav1.StatusReasonInternalError, http.StatusInternalServerError)
		return
	}
	if !authorized {
		badRequest(w, s.log, "Forbidden", metav1.StatusReasonForbidden, http.StatusForbidden)
		return
	}

	// Parse request body
	var updateRequest trainer.UpdateTrainJobStatusRequest
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&updateRequest); err != nil {
		s.log.V(5).Error(err, "Failed to parse runtime status", "namespace", namespace, "trainJobName", trainJobName)
		badRequest(w, s.log, "Invalid payload", metav1.StatusReasonInvalid, http.StatusUnprocessableEntity)
		return
	}

	// If the update request is empty (no trainer status), return success without applying
	if updateRequest.TrainerStatus == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(updateRequest); err != nil {
			s.log.Error(err, "Failed to write response", "namespace", namespace, "name", trainJobName)
		}
		return
	}

	var trainJob = trainerv1alpha1ac.TrainJob(trainJobName, namespace).WithStatus(toApplyConfig(updateRequest))

	if err := s.client.Status().Apply(r.Context(), trainJob, client.ForceOwnership, client.FieldOwner("trainer-status")); err != nil {
		// Check if the error is due to validation failure
		if apierrors.IsInvalid(err) || apierrors.IsBadRequest(err) {
			// Extract the validation error message for the user
			statusErr, ok := err.(*apierrors.StatusError)
			if ok && statusErr.ErrStatus.Message != "" {
				badRequest(w, s.log, statusErr.ErrStatus.Message, metav1.StatusReasonInvalid, http.StatusUnprocessableEntity)
			} else {
				badRequest(w, s.log, "Validation failed: "+err.Error(), metav1.StatusReasonInvalid, http.StatusUnprocessableEntity)
			}
			return
		}

		// Check if the error is due to missing object
		if apierrors.IsNotFound(err) {
			badRequest(w, s.log, "Train job not found", metav1.StatusReasonNotFound, http.StatusNotFound)
			return
		}

		// For other errors, return internal server error
		s.log.Error(err, "Failed to update TrainJob", "namespace", namespace, "name", trainJobName)
		badRequest(w, s.log, "Internal error", metav1.StatusReasonInternalError, http.StatusInternalServerError)
		return
	}

	// Return the parsed payload
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(updateRequest); err != nil {
		s.log.Error(err, "Failed to write TrainJob status", "namespace", namespace, "name", trainJobName)
	}
}

// handleDefault is the default handler for unknown requests.
func (s *Server) handleDefault(w http.ResponseWriter, _ *http.Request) {
	badRequest(w, s.log, "Not found", metav1.StatusReasonNotFound, http.StatusNotFound)
}

// badRequest sends a kubernetes Status response with the error message
func badRequest(w http.ResponseWriter, log logr.Logger, message string, reason metav1.StatusReason, code int32) {
	status := metav1.Status{
		Status:  metav1.StatusFailure,
		Message: message,
		Reason:  reason,
		Code:    code,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(int(code))
	if err := json.NewEncoder(w).Encode(status); err != nil {
		log.Error(err, "Failed to write bad request details")
	}
}

func toApplyConfig(updateRequest trainer.UpdateTrainJobStatusRequest) *trainerv1alpha1ac.TrainJobStatusApplyConfiguration {
	var s = trainerv1alpha1ac.TrainJobStatus()

	trainerStatus := updateRequest.TrainerStatus
	if trainerStatus != nil {
		var ts = trainerv1alpha1ac.TrainerStatus()

		if trainerStatus.ProgressPercentage != nil {
			ts = ts.WithProgressPercentage(*trainerStatus.ProgressPercentage)
		}
		if trainerStatus.EstimatedRemainingSeconds != nil {
			ts = ts.WithEstimatedRemainingSeconds(*trainerStatus.EstimatedRemainingSeconds)
		}
		for _, m := range trainerStatus.Metrics {
			ts.WithMetrics(
				trainerv1alpha1ac.Metric().
					WithName(m.Name).
					WithValue(m.Value),
			)
		}

		ts = ts.WithLastUpdatedTime(trainerStatus.LastUpdatedTime)
		s.WithTrainerStatus(ts)
	}
	return s
}
