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

package config

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
	"kueueviz/middleware"

	_ "net/http/pprof"
)

// ServerConfig holds server configuration
type ServerConfig struct {
	Port       string
	AuthMode   string
	AuthConfig middleware.AuthConfig
}

// NewServerConfig creates a new server configuration
func NewServerConfig() *ServerConfig {
	viper.AutomaticEnv()
	viper.SetDefault("KUEUEVIZ_PORT", "8080")
	viper.SetDefault("KUEUEVIZ_AUTH_MODE", "Disabled")
	viper.SetDefault("KUEUEVIZ_AUTH_TOKEN_REVIEW_CACHE_TTL", "60s")
	viper.SetDefault("KUEUEVIZ_AUTH_TOKEN_REVIEW_NEGATIVE_CACHE_TTL", "5s")

	var audiences []string
	if raw := viper.GetString("KUEUEVIZ_AUTH_TOKEN_REVIEW_AUDIENCES"); raw != "" {
		for a := range strings.SplitSeq(raw, ",") {
			if a = strings.TrimSpace(a); a != "" {
				audiences = append(audiences, a)
			}
		}
	}

	cacheTTL := parseDurationWithDefault(
		viper.GetString("KUEUEVIZ_AUTH_TOKEN_REVIEW_CACHE_TTL"), 60*time.Second, "KUEUEVIZ_AUTH_TOKEN_REVIEW_CACHE_TTL",
	)
	negativeCacheTTL := parseDurationWithDefault(
		viper.GetString("KUEUEVIZ_AUTH_TOKEN_REVIEW_NEGATIVE_CACHE_TTL"), 5*time.Second, "KUEUEVIZ_AUTH_TOKEN_REVIEW_NEGATIVE_CACHE_TTL",
	)

	return &ServerConfig{
		Port:     viper.GetString("KUEUEVIZ_PORT"),
		AuthMode: viper.GetString("KUEUEVIZ_AUTH_MODE"),
		AuthConfig: middleware.AuthConfig{
			Audiences:        audiences,
			CacheTTL:         cacheTTL,
			NegativeCacheTTL: negativeCacheTTL,
		},
	}
}

// SetupPprof starts the pprof server in development mode
func SetupPprof() {
	if gin.Mode() != gin.ReleaseMode {
		go func() {
			slog.Info("Starting pprof server on localhost:6060")
			if err := http.ListenAndServe("localhost:6060", nil); err != nil {
				slog.Error("Error starting pprof server", "error", err)
			}
		}()
	}
}

// SetupGinEngine creates and configures the Gin engine with base middleware.
func SetupGinEngine() (*gin.Engine, error) {
	r := gin.New()
	r.Use(gin.Logger())
	r.Use(gin.Recovery())

	corsMiddleware, err := middleware.SetupCORS()
	if err != nil {
		return nil, fmt.Errorf("error setting up CORS: %v", err)
	}
	r.Use(corsMiddleware)

	if err := r.SetTrustedProxies(nil); err != nil {
		return nil, fmt.Errorf("error setting trusted proxies: %v", err)
	}

	return r, nil
}

// GetServerAddress returns the formatted server address
func (c *ServerConfig) GetServerAddress() string {
	return fmt.Sprintf(":%s", c.Port)
}

func parseDurationWithDefault(raw string, fallback time.Duration, envName string) time.Duration {
	d, err := time.ParseDuration(raw)
	if err != nil {
		slog.Warn("Invalid duration, using default", "env", envName, "value", raw, "default", fallback)
		return fallback
	}
	return d
}
