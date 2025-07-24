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
	"log"
	"net/url"
	"strings"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
)

// validateOrigin validates and sanitizes a CORS origin URL
func validateOrigin(origin string) (string, bool) {
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
		rawOrigins := strings.Split(allowedOriginsEnv, ",")
		for _, origin := range rawOrigins {
			origin = strings.TrimSpace(origin)
			if cleanOrigin, valid := validateOrigin(origin); valid {
				allowedOrigins = append(allowedOrigins, cleanOrigin)
			} else {
				log.Printf("Warning: Invalid origin '%s' rejected", origin)
			}
		}
	} else {
		// Default development origins (only in development mode)
		if gin.Mode() != gin.ReleaseMode {
			allowedOrigins = []string{
				"http://localhost:3000",
				"http://127.0.0.1:3000",
			}
			log.Println("KUEUEVIZ_ALLOWED_ORIGINS not set, using default development origins")
		} else {
			log.Println("Production mode: KUEUEVIZ_ALLOWED_ORIGINS must be explicitly set")
		}
	}

	config.AllowOrigins = allowedOrigins
	config.AllowMethods = []string{"GET", "HEAD", "OPTIONS"}
	config.AllowHeaders = []string{"Origin", "Content-Length", "Content-Type", "Authorization"}

	if gin.Mode() == gin.ReleaseMode {
		log.Println("Running in production mode, applying security settings")
		config.AllowCredentials = false
		config.MaxAge = 12 * time.Hour

		log.Printf("CORS allowed origins: %v", config.AllowOrigins)

		if len(allowedOrigins) == 0 {
			return config, errors.New("production mode requires valid CORS origins to be configured")
		}
	} else {
		log.Println("Running in development mode")
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
