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
	"time"

	"github.com/bep/debounce"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"
	"kueueviz/middleware"
)

// WebSocket upgrader
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
	Subprotocols: []string{middleware.WebSocketBaseProtocol},
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
			slog.Debug("Failed to upgrade to WebSocket", "error", err)
			return
		}
		defer conn.Close()
		slog.Debug("WebSocket connection established", "duration", time.Since(connStart))

		ctx, cancel := context.WithCancel(c.Request.Context())
		defer cancel()

		// Read pump: gorilla/websocket requires an active reader to process control
		// frames and detect client disconnects. On any read error, cancel the context
		// to unblock handleInformerUpdates and trigger informer cleanup.
		go func() {
			defer cancel()
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		}()

		// Send initial data
		if err := h.sendData(ctx, conn, dataFetcher); err != nil {
			slog.Error("Error sending initial data", "error", err)
			return
		}

		// Use informer-based updates for real-time streaming
		h.handleInformerUpdates(ctx, conn, dataFetcher, gvks...)

		slog.Debug("WebSocket handler completed", "duration", time.Since(startTime))
	}
}

// sendData fetches and sends data through the WebSocket connection
func (h *Handlers) sendData(ctx context.Context, conn *websocket.Conn, dataFetcher func(ctx context.Context) (any, error)) error {
	fetchStart := time.Now()
	data, err := dataFetcher(ctx)
	if err != nil {
		return err
	}
	slog.Debug("Data fetched", "duration", time.Since(fetchStart))

	marshalStart := time.Now()
	jsonData, err := json.Marshal(data)
	if err != nil {
		return err
	}
	slog.Debug("Data marshaled into JSON", "duration", time.Since(marshalStart))

	if err := conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}

	writeStart := time.Now()
	err = conn.WriteMessage(websocket.TextMessage, jsonData)
	if err != nil {
		return err
	}
	slog.Debug("Message sent to client", "duration", time.Since(writeStart))

	return nil
}

// handleInformerUpdates uses Kubernetes informers to stream real-time changes
// Supports watching multiple resource types by registering handlers for all provided GVKs
func (h *Handlers) handleInformerUpdates(ctx context.Context, conn *websocket.Conn, dataFetcher func(ctx context.Context) (any, error), gvks ...schema.GroupVersionKind) {
	// Channel to signal when debounced update should be sent
	updateChan := make(chan struct{}, 1)

	// Debounce rapid informer events (e.g. pod churn) into a single update
	debounced := debounce.New(200 * time.Millisecond)
	triggerUpdate := func() {
		debounced(func() {
			select {
			case updateChan <- struct{}{}:
			default:
				// Channel already has a pending update
			}
		})
	}

	type registrationInfo struct {
		gvk schema.GroupVersionKind
		reg cache.ResourceEventHandlerRegistration
	}

	// Track all registrations for cleanup
	var registrations []registrationInfo
	defer func() {
		for _, r := range registrations {
			informer, err := h.client.GetInformerForKind(context.Background(), r.gvk)
			if err == nil {
				if err := informer.RemoveEventHandler(r.reg); err != nil {
					slog.Error("Error removing event handler", "gvk", r.gvk, "error", err)
				}
			}
		}
	}()

	// Register event handlers for all provided GVKs
	for _, gvk := range gvks {
		informer, err := h.client.GetInformerForKind(ctx, gvk)
		if err != nil {
			slog.Error("Error getting informer", "gvk", gvk, "error", err)
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
			slog.Error("Error adding event handler", "gvk", gvk, "error", err)
			continue
		}
		registrations = append(registrations, registrationInfo{gvk: gvk, reg: registration})
	}

	// Handle updates from the informers
	for {
		select {
		case <-ctx.Done():
			return
		case <-updateChan:
			if err := h.sendData(ctx, conn, dataFetcher); err != nil {
				slog.Error("Error sending update", "error", err)
				return
			}
		}
	}
}
