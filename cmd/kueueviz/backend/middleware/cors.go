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

package middleware

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

// validateOrigin validates and sanitizes a CORS origin URL
func validateOrigin(origin string) (string, bool) {
	// Special case for wildcard origin
	if origin == "*" {
		// Only allow wildcard origin in development mode
		return origin, gin.Mode() != gin.ReleaseMode
	}

	u, err := url.Parse(origin)
	if err != nil {
		return "", false
	}

	if u.Scheme != "http" && u.Scheme != "https" {
		return "", false
	}

	if u.Hostname() == "" {
		return "", false
	}

	// Only include scheme and host (which includes port if specified)
	cleanOrigin := fmt.Sprintf("%s://%s", u.Scheme, u.Host)
	return cleanOrigin, true
}

// ConfigureCORS sets up CORS configuration based on environment
func ConfigureCORS() (cors.Config, error) {
	config := cors.DefaultConfig()

	// Get allowed origins from environment variable
	allowedOriginsEnv := viper.GetString("KUEUEVIZ_ALLOWED_ORIGINS")
	var allowedOrigins []string

	if allowedOriginsEnv != "" {
		// Split comma-separated origins and validate each
		rawOrigins := strings.SplitSeq(allowedOriginsEnv, ",")
		for origin := range rawOrigins {
			origin = strings.TrimSpace(origin)
			if cleanOrigin, valid := validateOrigin(origin); valid {
				allowedOrigins = append(allowedOrigins, cleanOrigin)
			} else {
				slog.Warn("Invalid origin rejected", "origin", origin)
			}
		}
	} else {
		// Default development origins (only in development mode)
		if gin.Mode() != gin.ReleaseMode {
			allowedOrigins = append(allowedOrigins, "*")
			slog.Info("KUEUEVIZ_ALLOWED_ORIGINS not set, using default development origins")
		} else {
			slog.Warn("Production mode: KUEUEVIZ_ALLOWED_ORIGINS must be explicitly set")
		}
	}

	config.AllowOrigins = allowedOrigins
	config.AllowMethods = []string{"GET", "HEAD", "OPTIONS"}
	config.AllowHeaders = []string{"Origin", "Content-Length", "Content-Type", "Authorization"}

	if gin.Mode() == gin.ReleaseMode {
		slog.Info("Running in production mode, applying security settings")
		config.AllowCredentials = false
		config.MaxAge = 12 * time.Hour

		slog.Info("CORS allowed origins", "origins", config.AllowOrigins)

		if len(allowedOrigins) == 0 {
			return config, errors.New("production mode requires valid CORS origins to be configured")
		}
	} else {
		slog.Info("Running in development mode")
		config.AllowCredentials = true
		config.MaxAge = 5 * time.Minute
	}
	return config, nil
}

// SetupCORS returns a configured CORS middleware
func SetupCORS() (gin.HandlerFunc, error) {
	config, err := ConfigureCORS()
	if err != nil {
		return nil, err
	}
	return cors.New(config), nil
}

// ValidateWebSocketOrigin validates if the given WebSocket origin is allowed
func ValidateWebSocketOrigin(origin, host string) bool {
	log := ctrllog.Log.WithName("cors")

	if origin == "" {
		log.V(4).Info("WebSocket origin allowed", "reason", "no origin header provided")
		return true // No origin header, typically allowed
	}

	// Fast path for same-host (default gorilla/websocket behavior)
	u, err := url.Parse(origin)
	if err != nil {
		log.V(2).Info("WebSocket origin rejected", "origin", origin, "reason", "unparseable origin URL", "error", err)
		return false
	}

	if strings.EqualFold(u.Host, host) {
		log.V(4).Info("WebSocket origin allowed", "origin", origin, "host", host, "reason", "same host")
		return true
	}

	// In development mode, allow cross-port localhost connections (e.g. for E2E testing)
	if gin.Mode() != gin.ReleaseMode && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1") {
		log.V(4).Info("WebSocket origin allowed", "origin", origin, "reason", "localhost in development mode")
		return true
	}

	config, err := ConfigureCORS()
	if err != nil {
		log.V(2).Error(err, "WebSocket origin validation failed", "reason", "could not load CORS config")
		return false
	}

	canonicalOrigin := canonicalizeOrigin(origin)
	for _, allowed := range config.AllowOrigins {
		if canonicalizeOrigin(allowed) == canonicalOrigin {
			log.V(4).Info("WebSocket origin allowed", "origin", origin, "reason", "matches allowed CORS origins", "allowed", allowed)
			return true
		}
	}

	log.V(2).Info("WebSocket origin rejected", "origin", origin, "host", host, "reason", "does not match allowed CORS origins")
	return false
}

func canonicalizeOrigin(o string) string {
	u, err := url.Parse(o)
	if err != nil {
		return o
	}

	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	port := u.Port()

	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}

	if port != "" {
		host = host + ":" + port
	}

	return fmt.Sprintf("%s://%s", scheme, host)
}
