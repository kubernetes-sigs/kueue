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
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"
)

// WebSocket upgrader
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// GenericWebSocketHandler creates a WebSocket endpoint with informer-based real-time updates
// Accepts one or more GroupVersionKinds to watch for changes
func (h *Handlers) GenericWebSocketHandler(dataFetcher func(ctx context.Context) (any, error), gvks ...schema.GroupVersionKind) gin.HandlerFunc {
	return func(c *gin.Context) {
		startTime := time.Now()
		slog.Debug("WebSocket handler started")

		// Upgrade the HTTP connection to a WebSocket connection
		connStart := time.Now()
		conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			slog.Debug("Failed to upgrade to WebSocket: %v", "error", err)
			return
		}
		defer conn.Close()
		slog.Debug("WebSocket connection established took %v", "duration", time.Since(connStart))

		ctx, cancel := context.WithCancel(c.Request.Context())
		defer cancel()

		// Send initial data
		if err := h.sendData(ctx, conn, dataFetcher); err != nil {
			slog.Error("Error sending initial data: %v", "error", err)
			return
		}

		// Use informer-based updates for real-time streaming
		h.handleInformerUpdates(ctx, conn, dataFetcher, gvks...)

		slog.Debug("WebSocket handler completed, total time: %v", "duration", time.Since(startTime))
	}
}

// sendData fetches and sends data through the WebSocket connection
func (h *Handlers) sendData(ctx context.Context, conn *websocket.Conn, dataFetcher func(ctx context.Context) (any, error)) error {
	fetchStart := time.Now()
	data, err := dataFetcher(ctx)
	if err != nil {
		return err
	}
	slog.Debug("Data fetched took %v", "duration", time.Since(fetchStart))

	marshalStart := time.Now()
	jsonData, err := json.Marshal(data)
	if err != nil {
		return err
	}
	slog.Debug("Data marshaled into JSON took %v", "duration", time.Since(marshalStart))

	if err := conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}

	writeStart := time.Now()
	err = conn.WriteMessage(websocket.TextMessage, jsonData)
	if err != nil {
		return err
	}
	slog.Debug("Message sent to client took %v", "duration", time.Since(writeStart))

	return nil
}

// handleInformerUpdates uses Kubernetes informers to stream real-time changes
// Supports watching multiple resource types by registering handlers for all provided GVKs
func (h *Handlers) handleInformerUpdates(ctx context.Context, conn *websocket.Conn, dataFetcher func(ctx context.Context) (any, error), gvks ...schema.GroupVersionKind) {
	// Channel to signal when data needs to be sent
	updateChan := make(chan struct{}, 1)
	var updateMu sync.Mutex

	// Helper to trigger update (non-blocking)
	triggerUpdate := func() {
		updateMu.Lock()
		defer updateMu.Unlock()
		select {
		case updateChan <- struct{}{}:
		default:
			// Channel already has a pending update
		}
	}

	// Track all registrations for cleanup
	var registrations []cache.ResourceEventHandlerRegistration
	defer func() {
		for i, reg := range registrations {
			if i < len(gvks) && gvks[i] != (schema.GroupVersionKind{}) {
				informer, err := h.client.GetInformerForKind(ctx, gvks[i])
				if err == nil {
					if err := informer.RemoveEventHandler(reg); err != nil {
						slog.Error("Error removing event handler: %v", "error", err)
					}
				}
			}
		}
	}()

	// Register event handlers for all provided GVKs
	for _, gvk := range gvks {
		informer, err := h.client.GetInformerForKind(ctx, gvk)
		if err != nil {
			slog.Error("Error getting informer for %v: %v", "gvk", gvk, "error", err)
			continue
		}

		registration, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj any) {
				slog.Debug("Informer: object added", "gvk", gvk)
				triggerUpdate()
			},
			UpdateFunc: func(oldObj, newObj any) {
				slog.Debug("Informer: object updated", "gvk", gvk)
				triggerUpdate()
			},
			DeleteFunc: func(obj any) {
				slog.Debug("Informer: object deleted", "gvk", gvk)
				triggerUpdate()
			},
		})
		if err != nil {
			slog.Error("Error adding event handler for %v: %v", "gvk", gvk, "error", err)
			continue
		}
		registrations = append(registrations, registration)
	}

	// Handle updates from the informers
	for {
		select {
		case <-ctx.Done():
			return
		case <-updateChan:
			if err := h.sendData(ctx, conn, dataFetcher); err != nil {
				slog.Error("Error sending update: %v", "error", err)
				return
			}
		}
	}
}
