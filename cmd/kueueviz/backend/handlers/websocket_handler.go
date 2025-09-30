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
	log "log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// WebSocket upgrader
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// GenericWebSocketHandler creates a WebSocket endpoint with periodic data updates
func GenericWebSocketHandler(dataFetcher func(ctx context.Context) (any, error)) gin.HandlerFunc {
	return func(c *gin.Context) {
		startTime := time.Now()
		log.Debug("WebSocket handler started")

		// Upgrade the HTTP connection to a WebSocket connection
		connStart := time.Now()
		conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			log.Debug("Failed to upgrade to WebSocket: %v", "error", err)
			return
		}
		defer conn.Close()
		log.Debug("WebSocket connection established took %v", "duration", time.Since(connStart))

		// Fetch the initial data to send it immediately
		fetchStart := time.Now()
		data, err := dataFetcher(c.Request.Context())
		if err != nil {
			log.Error("Error fetching data %v", "error", err)
			return
		}
		log.Debug("Data fetched took %v", "duration", time.Since(fetchStart))

		// Marshal the fetched data into JSON
		marshalStart := time.Now()
		jsonData, err := json.Marshal(data)
		if err != nil {
			log.Error("Error marshaling data : %v", "error", err)
			return
		}
		log.Debug("Data marshaled into JSON at took %v", "duration", time.Since(marshalStart))

		// Set write deadline to avoid blocking indefinitely
		if err := conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
			log.Error("Error setting write deadline: %v", "error", err)
			return
		}

		// Send the initial data to the WebSocket client immediately
		writeStart := time.Now()
		err = conn.WriteMessage(websocket.TextMessage, jsonData)
		if err != nil {
			log.Error("Error writing message : %v", "error", err)
			// If writing fails, break the loop and close the connection
			return
		}
		log.Debug("Initial message sent to client took %v", "duration", time.Since(writeStart))

		// Start a ticker for periodic updates (every 5 seconds)
		// TODO use SharedInformers and TTL to only send updates if they happen
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		// Continue sending updates every 5 seconds
		for range ticker.C {
			// Fetch the latest data
			fetchStart := time.Now()
			data, err := dataFetcher(c.Request.Context())
			if err != nil {
				log.Error("Error fetching data  %v", "error", err)
				continue
			}
			log.Debug("Data fetched at  took %v", "duration", time.Since(fetchStart))

			// Marshal the fetched data into JSON
			marshalStart := time.Now()
			jsonData, err := json.Marshal(data)
			if err != nil {
				log.Error("Error marshaling data at %v", "error", err)
				continue
			}
			log.Debug("Data marshaled into JSON took %v", "duration", time.Since(marshalStart))

			// Set write deadline to avoid blocking indefinitely
			if err := conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
				log.Error("Error writing deadline to client: %v", "error", err)
				continue
			}

			// Send the JSON data to the WebSocket client
			writeStart := time.Now()
			err = conn.WriteMessage(websocket.TextMessage, jsonData)
			if err != nil {
				log.Error("Error writing message: ", "error", err)
				// If writing fails, break the loop and close the connection
				break
			}
			log.Debug("Message sent to client  took %v", "duration", time.Since(writeStart))
		}

		log.Debug("WebSocket handler completed  total time: %v", "duration", time.Since(startTime))
	}
}
