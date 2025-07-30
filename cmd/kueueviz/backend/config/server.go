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
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
	"kueueviz/middleware"

	_ "net/http/pprof"
)

// ServerConfig holds server configuration
type ServerConfig struct {
	Port string
}

// NewServerConfig creates a new server configuration
func NewServerConfig() *ServerConfig {
	viper.AutomaticEnv()
	viper.SetDefault("KUEUEVIZ_PORT", "8080")

	return &ServerConfig{
		Port: viper.GetString("KUEUEVIZ_PORT"),
	}
}

// SetupPprof starts the pprof server in development mode
func SetupPprof() {
	if gin.Mode() != gin.ReleaseMode {
		go func() {
			log.Println("Starting pprof server on localhost:6060")
			if err := http.ListenAndServe("localhost:6060", nil); err != nil {
				log.Printf("Error starting pprof server: %v", err)
			}
		}()
	}
}

// SetupGinEngine creates and configures the Gin engine with middleware
func SetupGinEngine() (*gin.Engine, error) {
	r := gin.New()
	r.Use(gin.Logger())
	r.Use(gin.Recovery())

	// Configure CORS with environment-aware settings
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
